// Package pluginnetwork implements the host-controlled HTTPS boundary for plugins.
package pluginnetwork

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	MaxRequestBytes  int64 = 64 << 10
	MaxResponseBytes int64 = 256 << 10
	MaxHeaderBytes         = 16 << 10
	maxURLBytes            = 8 << 10
)

// Errors intentionally carry no request, DNS, TLS, or upstream error details.
var (
	ErrInvalidPolicy    = errors.New("plugin_network_invalid_policy")
	ErrInvalidRequest   = errors.New("plugin_network_invalid_request")
	ErrOriginDenied     = errors.New("plugin_network_origin_denied")
	ErrMethodDenied     = errors.New("plugin_network_method_denied")
	ErrAddressDenied    = errors.New("plugin_network_address_denied")
	ErrDNS              = errors.New("plugin_network_dns_failed")
	ErrRequestTooLarge  = errors.New("plugin_network_request_too_large")
	ErrResponseTooLarge = errors.New("plugin_network_response_too_large")
	ErrTimeout          = errors.New("plugin_network_timeout")
	ErrCanceled         = errors.New("plugin_network_canceled")
	ErrNetwork          = errors.New("plugin_network_request_failed")
)

type Request struct {
	URL            string            `json:"url"`
	Method         string            `json:"method"`
	Headers        map[string]string `json:"headers,omitempty"`
	Body           string            `json:"body,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

// Policy must be the effective intersection of version declarations and grants.
// Empty allowlists deny all. Zero byte limits use the constants above; positive
// limits may only lower those ceilings. Origins have no path, query or fragment.
type Policy struct {
	AllowedOrigins   []string `json:"allowed_origins"`
	AllowedMethods   []string `json:"allowed_methods"`
	MaxRequestBytes  int64    `json:"max_request_bytes,omitempty"`
	MaxResponseBytes int64    `json:"max_response_bytes,omitempty"`
}

type Response struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// ValidatePolicy checks syntax and limits without doing DNS or other I/O.
func ValidatePolicy(policy Policy) error {
	if len(policy.AllowedOrigins) == 0 || len(policy.AllowedOrigins) > 128 ||
		len(policy.AllowedMethods) == 0 || len(policy.AllowedMethods) > 7 ||
		policy.MaxRequestBytes < 0 || policy.MaxRequestBytes > MaxRequestBytes ||
		policy.MaxResponseBytes < 0 || policy.MaxResponseBytes > MaxResponseBytes {
		return ErrInvalidPolicy
	}
	for _, raw := range policy.AllowedOrigins {
		u, _, err := parseURL(raw)
		if err != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery {
			return ErrInvalidPolicy
		}
	}
	for _, method := range policy.AllowedMethods {
		if !validMethod(method) {
			return ErrInvalidPolicy
		}
	}
	return nil
}

func validMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
		return true
	default:
		return false
	}
}

// Do makes exactly one HTTPS request, never follows redirects, and does not use
// environment proxies or cookies. All failures return a zero Response and one
// of the stable errors above, never an error containing upstream-controlled text.
// TimeoutSeconds defaults to 10; explicit values must be between 1 and 30.
func Do(ctx context.Context, request Request, policy Policy) (Response, error) {
	return do(ctx, request, policy, dependencies{
		resolve: net.DefaultResolver.LookupNetIP,
		dial:    (&net.Dialer{}).DialContext,
	})
}

type dependencies struct {
	resolve func(context.Context, string, string) ([]netip.Addr, error)
	dial    func(context.Context, string, string) (net.Conn, error)
	roots   *x509.CertPool
}

func do(parent context.Context, request Request, policy Policy, deps dependencies) (Response, error) {
	if err := ValidatePolicy(policy); err != nil {
		return Response{}, err
	}
	u, origin, err := parseURL(request.URL)
	if err != nil {
		return Response{}, err
	}
	allowed := false
	for _, raw := range policy.AllowedOrigins {
		_, candidate, _ := parseURL(raw)
		allowed = allowed || origin == candidate
	}
	if !allowed {
		return Response{}, ErrOriginDenied
	}
	allowed = false
	for _, method := range policy.AllowedMethods {
		allowed = allowed || request.Method == method
	}
	if !allowed {
		return Response{}, ErrMethodDenied
	}
	if request.TimeoutSeconds < 0 || request.TimeoutSeconds > 30 {
		return Response{}, ErrInvalidRequest
	}
	timeout := request.TimeoutSeconds
	if timeout == 0 {
		timeout = 10
	}
	requestLimit, responseLimit := policy.MaxRequestBytes, policy.MaxResponseBytes
	if requestLimit == 0 {
		requestLimit = MaxRequestBytes
	}
	if responseLimit == 0 {
		responseLimit = MaxResponseBytes
	}
	if int64(len(request.Body)) > requestLimit {
		return Response{}, ErrRequestTooLarge
	}
	headers, err := requestHeaders(request.Headers)
	if err != nil {
		return Response{}, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Response{}, safeError(ctx, err)
	}
	ips, err := resolvePublic(ctx, u.Hostname(), deps.resolve)
	if err != nil {
		return Response{}, safeError(ctx, err)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		MaxResponseHeaderBytes: MaxHeaderBytes,
		TLSHandshakeTimeout:    time.Duration(timeout) * time.Second,
		ResponseHeaderTimeout:  time.Duration(timeout) * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: u.Hostname(),
			RootCAs:    deps.roots,
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			for _, ip := range ips {
				conn, err := deps.dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
			return nil, ErrNetwork
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	req, err := http.NewRequestWithContext(ctx, request.Method, u.String(), strings.NewReader(request.Body))
	if err != nil {
		return Response{}, ErrInvalidRequest
	}
	req.Header = headers
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, safeError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.ContentLength > responseLimit {
		return Response{}, ErrResponseTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil {
		return Response{}, safeError(ctx, err)
	}
	if int64(len(body)) > responseLimit {
		return Response{}, ErrResponseTooLarge
	}
	return Response{Status: resp.StatusCode, Headers: responseHeaders(resp.Header), Body: string(body)}, nil
}

func safeError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return ErrCanceled
	}
	var networkError net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		(errors.As(err, &networkError) && networkError.Timeout()) {
		return ErrTimeout
	}
	if errors.Is(err, ErrAddressDenied) {
		return ErrAddressDenied
	}
	if errors.Is(err, ErrDNS) {
		return ErrDNS
	}
	return ErrNetwork
}

func parseURL(raw string) (*url.URL, string, error) {
	if len(raw) == 0 || len(raw) > maxURLBytes || strings.Contains(raw, "#") {
		return nil, "", ErrInvalidRequest
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, "", ErrInvalidRequest
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicIP(ip) {
			return nil, "", ErrAddressDenied
		}
		host = ip.String()
	} else if !validHostname(host) {
		return nil, "", ErrInvalidRequest
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return nil, "", ErrInvalidRequest
		}
		port = "443"
	} else {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return nil, "", ErrInvalidRequest
		}
	}
	// Canonical authority is used both for comparison and by net/http.
	u.Host = net.JoinHostPort(host, port)
	return u, "https://" + u.Host, nil
}

func validHostname(host string) bool {
	if len(host) > 253 || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func requestHeaders(input map[string]string) (http.Header, error) {
	headers := make(http.Header)
	size := 0
	for name, value := range input {
		if len(name) == 0 {
			return nil, ErrInvalidRequest
		}
		if len(name) > MaxHeaderBytes || len(value) > MaxHeaderBytes {
			return nil, ErrRequestTooLarge
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
				return nil, ErrInvalidRequest
			}
		}
		for _, c := range value {
			if c == 127 || c < 32 && c != '\t' {
				return nil, ErrInvalidRequest
			}
		}
		canonical := http.CanonicalHeaderKey(name)
		switch canonical {
		case "Host", "Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding", "Content-Length", "Te", "Trailer", "Upgrade", "Expect", "Accept-Encoding", "Cookie", "Cookie2":
			return nil, ErrInvalidRequest
		}
		if strings.HasPrefix(canonical, "Proxy-") || strings.HasPrefix(canonical, "Sec-") {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := headers[canonical]; duplicate {
			return nil, ErrInvalidRequest
		}
		size += len(name) + len(value) + 4
		if size > MaxHeaderBytes {
			return nil, ErrRequestTooLarge
		}
		headers.Set(canonical, value)
	}
	return headers, nil
}

func responseHeaders(input http.Header) map[string]string {
	result := make(map[string]string)
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Cache-Control", "Etag", "Last-Modified", "Retry-After"} {
		if values := input.Values(name); len(values) > 0 {
			result[name] = strings.Join(values, ", ")
		}
	}
	return result
}

func resolvePublic(ctx context.Context, host string, resolver func(context.Context, string, string) ([]netip.Addr, error)) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicIP(ip) {
			return nil, ErrAddressDenied
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	ips, err := resolver(ctx, "ip", host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrDNS
	}
	if len(ips) == 0 || len(ips) > 64 {
		return nil, ErrDNS
	}
	for i, ip := range ips {
		if !publicIP(ip) {
			return nil, ErrAddressDenied
		}
		ips[i] = ip.Unmap()
	}
	return ips, nil
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
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

var ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

func publicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		(ip.Is6() && !ipv6GlobalUnicast.Contains(ip)) {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
