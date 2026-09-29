// Package pluginhttp is the only way plugin code reaches the network: a
// Controller-side HTTPS client limited to granted host patterns, public
// addresses and bounded sizes. Plugin workers themselves have no network.
package pluginhttp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	MaxRequestBytes         = 256 << 10
	MaxResponseBytes        = 1 << 20
	MaxRequestHeaders       = 32
	MaxHeaderBytes          = 16 << 10
	MaxResponseHeaders      = 64
	MaxResponseHeaderValue  = 4 << 10
	MaxRedirects            = 3
	DefaultTimeout          = 10 * time.Second
	MaxTimeout              = 30 * time.Second
	ConnectTimeout          = 5 * time.Second
	maxURLBytes             = 8 << 10
	maxResolvedAddressCount = 32
)

// Errors never carry upstream, DNS or TLS text.
var (
	ErrInvalidRequest   = errors.New("invalid_request")
	ErrHostDenied       = errors.New("host_denied")
	ErrMethodDenied     = errors.New("method_denied")
	ErrPrivateAddress   = errors.New("private_address_denied")
	ErrDNS              = errors.New("dns_failed")
	ErrRequestTooLarge  = errors.New("request_too_large")
	ErrResponseTooLarge = errors.New("response_too_large")
	ErrTimeout          = errors.New("timeout")
	ErrCanceled         = errors.New("canceled")
	ErrNetwork          = errors.New("network_failed")
)

type Request struct {
	URL     string
	Method  string
	Headers map[string]string
	// SecretHeaders are already-resolved values injected by the gateway.
	// They are never logged and disable redirect following.
	SecretHeaders   map[string]string
	Body            []byte
	Timeout         time.Duration
	FollowRedirects bool
}

// Policy is the effective allowlist: manifest hosts ∩ granted hosts, and the
// declared methods. Deny adds host-specific refusals (the Controller's own
// addresses, enrolled node addresses) on top of the public-address rule.
type Policy struct {
	Hosts            []string
	Methods          []string
	MaxResponseBytes int64
	Deny             func(netip.Addr) bool
}

type Response struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      []byte            `json:"-"`
	FinalURL  string            `json:"final_url"`
	Redirects int               `json:"redirects"`
}

type dependencies struct {
	resolve func(context.Context, string, string) ([]netip.Addr, error)
	dial    func(context.Context, string, string) (net.Conn, error)
	roots   *x509.CertPool
}

// Do performs one gateway request.
func Do(ctx context.Context, request Request, policy Policy) (Response, error) {
	dialer := &net.Dialer{Timeout: ConnectTimeout}
	return do(ctx, request, policy, dependencies{resolve: net.DefaultResolver.LookupNetIP, dial: dialer.DialContext})
}

func do(parent context.Context, request Request, policy Policy, deps dependencies) (Response, error) {
	u, err := ParseURL(request.URL)
	if err != nil {
		return Response{}, err
	}
	if !HostAllowed(policy.Hosts, u) {
		return Response{}, ErrHostDenied
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !contains(policy.Methods, method) {
		return Response{}, ErrMethodDenied
	}
	if len(request.Body) > MaxRequestBytes {
		return Response{}, ErrRequestTooLarge
	}
	if len(request.Body) > 0 && (method == http.MethodGet || method == http.MethodHead) {
		return Response{}, ErrInvalidRequest
	}
	headers, err := buildHeaders(request.Headers, request.SecretHeaders)
	if err != nil {
		return Response{}, err
	}
	timeout := request.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if timeout > MaxTimeout {
		return Response{}, ErrInvalidRequest
	}
	responseLimit := policy.MaxResponseBytes
	if responseLimit <= 0 || responseLimit > MaxResponseBytes {
		responseLimit = MaxResponseBytes
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	followRedirects := request.FollowRedirects && len(request.SecretHeaders) == 0
	redirects := 0
	transport := &http.Transport{
		Proxy:                  nil,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		MaxResponseHeaderBytes: MaxHeaderBytes * 4,
		TLSHandshakeTimeout:    ConnectTimeout,
		ResponseHeaderTimeout:  timeout,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: deps.roots},
		// Every dial resolves, validates and connects to the validated
		// address itself, so a DNS answer that changes between checks
		// (rebinding) can never reach a non-public address.
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, ErrInvalidRequest
			}
			ips, err := resolvePublic(ctx, host, deps.resolve, policy.Deny)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				conn, dialErr := deps.dial(ctx, "tcp", net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
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
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if !followRedirects {
				return http.ErrUseLastResponse
			}
			if len(via) > MaxRedirects {
				return ErrHostDenied
			}
			if _, err := ParseURL(next.URL.String()); err != nil {
				return ErrHostDenied
			}
			if !HostAllowed(policy.Hosts, next.URL) {
				return ErrHostDenied
			}
			redirects = len(via)
			return nil
		},
	}
	var body io.Reader
	if len(request.Body) > 0 {
		body = bytes.NewReader(request.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
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
	payload, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil {
		return Response{}, safeError(ctx, err)
	}
	if int64(len(payload)) > responseLimit {
		return Response{}, ErrResponseTooLarge
	}
	return Response{Status: resp.StatusCode, Headers: responseHeaders(resp.Header), Body: payload, FinalURL: resp.Request.URL.String(), Redirects: redirects}, nil
}

// ParseURL accepts absolute HTTPS URLs to DNS names or public IP literals,
// without credentials or fragments, and canonicalizes the authority.
func ParseURL(raw string) (*url.URL, error) {
	if len(raw) == 0 || len(raw) > maxURLBytes || strings.ContainsAny(raw, "#\r\n\t ") {
		return nil, ErrInvalidRequest
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, ErrInvalidRequest
	}
	host := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(host); err == nil {
		if !PublicIP(ip) {
			return nil, ErrPrivateAddress
		}
		// IP literals never match a DNS host pattern; they are refused by
		// HostAllowed below, but must never be treated as a private target.
		host = ip.String()
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return nil, ErrInvalidRequest
		}
		port = "443"
	} else if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
		return nil, ErrInvalidRequest
	}
	u.Host = net.JoinHostPort(host, port)
	if port == "443" {
		u.Host = hostForURL(host)
	}
	return u, nil
}

func hostForURL(host string) string {
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}

// HostAllowed matches the URL host and port against granted patterns:
// "api.example.com" (port 443), "api.example.com:8443" and "*.example.com"
// (any subdomain, never the apex; port 443).
func HostAllowed(patterns []string, u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	for _, pattern := range patterns {
		patternHost, patternPort, hasPort := strings.Cut(strings.ToLower(pattern), ":")
		if !hasPort {
			patternPort = "443"
		}
		if patternPort != port {
			continue
		}
		if suffix, wildcard := strings.CutPrefix(patternHost, "*."); wildcard {
			if strings.HasSuffix(host, "."+suffix) && len(host) > len(suffix)+1 {
				return true
			}
			continue
		}
		if host == patternHost {
			return true
		}
	}
	return false
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func buildHeaders(plain, secret map[string]string) (http.Header, error) {
	headers := make(http.Header)
	size := 0
	add := func(name, value string) error {
		if name == "" || len(name) > 256 || len(value) > MaxHeaderBytes {
			return ErrInvalidRequest
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
				return ErrInvalidRequest
			}
		}
		for _, c := range value {
			if c == 127 || c < 32 && c != '\t' {
				return ErrInvalidRequest
			}
		}
		canonical := http.CanonicalHeaderKey(name)
		switch canonical {
		case "Host", "Connection", "Proxy-Connection", "Keep-Alive", "Transfer-Encoding", "Content-Length", "Te", "Trailer", "Upgrade", "Expect", "Accept-Encoding", "Cookie", "Cookie2", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-Ip":
			return ErrInvalidRequest
		}
		if strings.HasPrefix(canonical, "Proxy-") || strings.HasPrefix(canonical, "Sec-") {
			return ErrInvalidRequest
		}
		if _, duplicate := headers[canonical]; duplicate {
			return ErrInvalidRequest
		}
		size += len(name) + len(value) + 4
		if size > MaxHeaderBytes || len(headers) >= MaxRequestHeaders {
			return ErrRequestTooLarge
		}
		headers.Set(canonical, value)
		return nil
	}
	for name, value := range plain {
		if err := add(name, value); err != nil {
			return nil, err
		}
	}
	for name, value := range secret {
		if err := add(name, value); err != nil {
			return nil, err
		}
	}
	if headers.Get("User-Agent") == "" {
		headers.Set("User-Agent", "OBoard-Plugin")
	}
	return headers, nil
}

func responseHeaders(input http.Header) map[string]string {
	names := make([]string, 0, len(input))
	for name := range input {
		switch http.CanonicalHeaderKey(name) {
		case "Set-Cookie", "Set-Cookie2":
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	out := map[string]string{}
	for _, name := range names {
		if len(out) >= MaxResponseHeaders {
			break
		}
		value := strings.Join(input.Values(name), ", ")
		if len(value) > MaxResponseHeaderValue {
			value = value[:MaxResponseHeaderValue]
		}
		out[strings.ToLower(name)] = value
	}
	return out
}

func resolvePublic(ctx context.Context, host string, resolver func(context.Context, string, string) ([]netip.Addr, error), deny func(netip.Addr) bool) ([]netip.Addr, error) {
	check := func(ip netip.Addr) error {
		ip = ip.Unmap()
		if !PublicIP(ip) || deny != nil && deny(ip) {
			return ErrPrivateAddress
		}
		return nil
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if err := check(ip); err != nil {
			return nil, err
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
	if len(ips) == 0 || len(ips) > maxResolvedAddressCount {
		return nil, ErrDNS
	}
	out := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		// One non-public answer refuses the whole name: a mixed answer is how
		// a hostile resolver steers a client at an internal address.
		if err := check(ip); err != nil {
			return nil, err
		}
		out = append(out, ip.Unmap())
	}
	return out, nil
}

func safeError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, ErrHostDenied):
		return ErrHostDenied
	case errors.Is(err, ErrPrivateAddress):
		return ErrPrivateAddress
	case errors.Is(err, ErrDNS):
		return ErrDNS
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return ErrCanceled
	}
	var networkError net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout() {
		return ErrTimeout
	}
	return ErrNetwork
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
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2620:4f:8000::/48"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

var ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

// PublicIP reports whether ip is a globally routable unicast address. It
// refuses loopback, RFC1918, CGNAT (including 100.100.100.200 metadata),
// link-local (169.254.169.254 metadata), ULA, multicast and documentation
// ranges, and NAT64 prefixes that could map to them.
func PublicIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.Zone() != "" {
		return false
	}
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || (ip.Is6() && !ipv6GlobalUnicast.Contains(ip)) {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// Redact replaces every occurrence of each secret, and its common encodings,
// in text. Remote endpoints can echo credentials; nothing a secret touched is
// returned to plugin code, logs or audit unredacted.
func Redact(text string, secrets []string) string {
	for _, form := range secretForms(secrets) {
		text = strings.ReplaceAll(text, form, "[REDACTED]")
	}
	return text
}

func RedactBytes(body []byte, secrets []string) []byte {
	for _, form := range secretForms(secrets) {
		body = bytes.ReplaceAll(body, []byte(form), []byte("[REDACTED]"))
	}
	return body
}

func secretForms(secrets []string) []string {
	forms := []string{}
	seen := map[string]bool{}
	add := func(value string) {
		if len(value) >= 4 && !seen[value] {
			seen[value] = true
			forms = append(forms, value)
		}
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		add(secret)
		add(url.QueryEscape(secret))
		add(url.PathEscape(secret))
		add(base64.StdEncoding.EncodeToString([]byte(secret)))
		add(base64.RawStdEncoding.EncodeToString([]byte(secret)))
		add(base64.URLEncoding.EncodeToString([]byte(secret)))
		add(base64.RawURLEncoding.EncodeToString([]byte(secret)))
		add(hex.EncodeToString([]byte(secret)))
		add(strings.ToUpper(hex.EncodeToString([]byte(secret))))
	}
	// Longest first so a secret never leaves a partial encoded tail.
	sort.Slice(forms, func(i, j int) bool { return len(forms[i]) > len(forms[j]) })
	return forms
}
