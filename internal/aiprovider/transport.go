package aiprovider

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

type networkPolicyKey struct{}

var providerMetadataAddresses = map[netip.Addr]struct{}{
	netip.MustParseAddr("100.100.100.200"): {},
	netip.MustParseAddr("fd00:ec2::254"):   {},
}

func WithNetworkPolicy(ctx context.Context, allowPrivate bool) context.Context {
	return context.WithValue(ctx, networkPolicyKey{}, allowPrivate)
}

func NewHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	resolver := net.DefaultResolver
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		allowPrivate, _ := ctx.Value(networkPolicyKey{}).(bool)
		addresses, err := resolver.LookupNetIP(ctx, "ip", strings.Trim(host, "[]"))
		if err != nil {
			return nil, err
		}
		for _, addressIP := range addresses {
			ip := addressIP.Unmap()
			if blockedProviderIP(ip, allowPrivate) {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return nil, errors.New("provider endpoint resolved only to prohibited addresses")
	}
	return &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func blockedProviderIP(ip netip.Addr, allowPrivate bool) bool {
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if _, metadata := providerMetadataAddresses[ip]; metadata {
		return true
	}
	if !allowPrivate && (ip.IsPrivate() || ip.IsLoopback()) {
		return true
	}
	return false
}

func EndpointContext(parent context.Context, endpoint RuntimeEndpoint) (context.Context, context.CancelFunc) {
	timeout := time.Duration(endpoint.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return WithNetworkPolicy(ctx, endpoint.AllowPrivateNetwork), cancel
}

func ParseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
