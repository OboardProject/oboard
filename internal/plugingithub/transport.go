package plugingithub

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/netip"
	"time"
)

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy:                  nil,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialPublic(ctx, address, net.DefaultResolver.LookupNetIP, (&net.Dialer{Timeout: 10 * time.Second}).DialContext)
		},
	}
}

func dialPublic(ctx context.Context, address string, resolve func(context.Context, string, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "api.github.com" || port != "443" {
		return nil, ErrAddress
	}
	ips, err := resolve(ctx, "ip", host)
	if err != nil || len(ips) == 0 || len(ips) > 64 {
		return nil, ErrUpstream
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, ErrAddress
		}
	}
	for _, ip := range ips {
		conn, err := dial(ctx, "tcp", net.JoinHostPort(ip.Unmap().String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, ErrUpstream
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
}

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || (ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip)) {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
