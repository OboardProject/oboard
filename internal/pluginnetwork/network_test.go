package pluginnetwork

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{AllowedOrigins: []string{"https://example.com"}, AllowedMethods: []string{"GET", "POST"}}
}

func testRequest() Request {
	return Request{URL: "https://example.com/path?token=secret", Method: "GET"}
}

func testDependencies(t *testing.T, handler http.HandlerFunc) (dependencies, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	var lookups, dials atomic.Int32
	return dependencies{
		roots: roots,
		resolve: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			lookups.Add(1)
			if network != "ip" || host != "example.com" {
				t.Errorf("unexpected DNS lookup %q %q", network, host)
			}
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials.Add(1)
			if network != "tcp" || address != "8.8.8.8:443" {
				t.Errorf("dial was not pinned: %q %q", network, address)
				return nil, errors.New("unpinned dial")
			}
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	}, &lookups, &dials
}

func TestValidatePolicy(t *testing.T) {
	for _, origin := range []string{
		"https://example.com", "https://EXAMPLE.com:443", "https://api.example.com:8443",
		"https://8.8.8.8", "https://[2606:4700:4700::1111]",
	} {
		p := testPolicy()
		p.AllowedOrigins = []string{origin}
		if err := ValidatePolicy(p); err != nil {
			t.Errorf("valid origin %q: %v", origin, err)
		}
	}
	for _, origin := range []string{
		"", "http://example.com", "https://example.com/", "https://example.com/path",
		"https://example.com?", "https://example.com?q=a", "https://example.com#",
		"https://example.com#fragment", "https://user:secret@example.com", "https://*.example.com",
		"https://example.com:0", "https://example.com:65536", "https://example.com:0443",
		"https://example.com:", "https://example.com.", "https://localhost", "https://test.local",
		"https://test.localhost", "https://test.internal", "https://test.home.arpa",
		"https://127.0.0.1", "https://[::1]", "https://[fe80::1%25eth0]", " https://example.com",
	} {
		p := testPolicy()
		p.AllowedOrigins = []string{origin}
		if err := ValidatePolicy(p); err != ErrInvalidPolicy {
			t.Errorf("invalid origin %q: %v", origin, err)
		}
	}
	for _, p := range []Policy{
		{}, {AllowedOrigins: []string{"https://example.com"}}, {AllowedMethods: []string{"GET"}},
		{AllowedOrigins: []string{"https://example.com"}, AllowedMethods: []string{"*"}},
		{AllowedOrigins: []string{"https://example.com"}, AllowedMethods: []string{"get"}},
		{AllowedOrigins: []string{"https://example.com"}, AllowedMethods: []string{"CONNECT"}},
		{AllowedOrigins: []string{"https://example.com"}, AllowedMethods: []string{"TRACE"}},
	} {
		if err := ValidatePolicy(p); err != ErrInvalidPolicy {
			t.Errorf("invalid policy accepted: %+v: %v", p, err)
		}
	}
	for _, limit := range []int64{-1, MaxResponseBytes + 1} {
		p := testPolicy()
		p.MaxResponseBytes = limit
		if ValidatePolicy(p) != ErrInvalidPolicy {
			t.Fatal("invalid response limit accepted")
		}
		p = testPolicy()
		p.MaxRequestBytes = limit
		if ValidatePolicy(p) != ErrInvalidPolicy {
			t.Fatal("invalid request limit accepted")
		}
	}
}

func TestRequestValidationBeforeNetwork(t *testing.T) {
	cases := []struct {
		name string
		edit func(*Request)
		want error
	}{
		{"subdomain", func(r *Request) { r.URL = "https://api.example.com" }, ErrOriginDenied},
		{"suffix", func(r *Request) { r.URL = "https://example.com.attacker.net" }, ErrOriginDenied},
		{"port", func(r *Request) { r.URL = "https://example.com:8443" }, ErrOriginDenied},
		{"scheme", func(r *Request) { r.URL = "http://example.com" }, ErrInvalidRequest},
		{"userinfo", func(r *Request) { r.URL = "https://token:secret@example.com" }, ErrInvalidRequest},
		{"fragment", func(r *Request) { r.URL += "#secret" }, ErrInvalidRequest},
		{"empty fragment", func(r *Request) { r.URL += "#" }, ErrInvalidRequest},
		{"private", func(r *Request) { r.URL = "https://127.0.0.1" }, ErrAddressDenied},
		{"empty method", func(r *Request) { r.Method = "" }, ErrMethodDenied},
		{"method", func(r *Request) { r.Method = "DELETE" }, ErrMethodDenied},
		{"lower method", func(r *Request) { r.Method = "get" }, ErrMethodDenied},
		{"timeout", func(r *Request) { r.TimeoutSeconds = 31 }, ErrInvalidRequest},
		{"negative timeout", func(r *Request) { r.TimeoutSeconds = -1 }, ErrInvalidRequest},
		{"body limit", func(r *Request) { r.Body = strings.Repeat("a", int(MaxRequestBytes)+1) }, ErrRequestTooLarge},
		{"URL limit", func(r *Request) { r.URL += strings.Repeat("a", maxURLBytes) }, ErrInvalidRequest},
		{"header limit", func(r *Request) { r.Headers = map[string]string{"X-Test": strings.Repeat("a", MaxHeaderBytes)} }, ErrRequestTooLarge},
		{"host header", func(r *Request) { r.Headers = map[string]string{"Host": "evil.example"} }, ErrInvalidRequest},
		{"header newline", func(r *Request) { r.Headers = map[string]string{"X-Test": "secret\r\nInjected: yes"} }, ErrInvalidRequest},
		{"invalid header name", func(r *Request) { r.Headers = map[string]string{"X-Test:": "secret"} }, ErrInvalidRequest},
		{"duplicate header", func(r *Request) { r.Headers = map[string]string{"X-Test": "a", "x-test": "b"} }, ErrInvalidRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRequest()
			tc.edit(&r)
			resp, err := do(context.Background(), r, testPolicy(), dependencies{})
			if err != tc.want || resp.Status != 0 || resp.Body != "" || resp.Headers != nil {
				t.Fatalf("response=%+v error=%v, want %v", resp, err, tc.want)
			}
		})
	}
	for _, name := range []string{"Connection", "Proxy-Authorization", "Transfer-Encoding", "Content-Length", "Trailer", "Upgrade", "Expect", "Accept-Encoding", "Cookie", "Sec-Websocket-Key"} {
		r := testRequest()
		r.Headers = map[string]string{name: "test"}
		if _, err := do(context.Background(), r, testPolicy(), dependencies{}); err != ErrInvalidRequest {
			t.Errorf("unsafe header %q accepted: %v", name, err)
		}
	}
}

func TestPublicIP(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0", "0.1.2.3", "10.0.0.1", "100.64.0.1", "100.100.100.200", "127.0.0.1",
		"169.254.169.254", "172.16.0.1", "192.0.0.9", "192.0.2.1", "192.88.99.1",
		"192.168.1.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::808:808", "100::1",
		"2001::1", "2001:2::1", "2001:db8::1", "2002:7f00:1::1", "3fff::1", "fc00::1", "fd00:ec2::254",
		"fe80::1", "ff02::1", "2606:4700::1%eth0", "4000::1", "2620:4f:8000::1",
	} {
		if publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("non-public address accepted: %s", raw)
		}
	}
	if publicIP(netip.Addr{}) {
		t.Fatal("invalid IP accepted")
	}
	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "::ffff:8.8.8.8", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		if !publicIP(netip.MustParseAddr(raw)) {
			t.Errorf("public address denied: %s", raw)
		}
	}
}

func TestDNSRejectsEveryNonPublicAnswerBeforeDial(t *testing.T) {
	for _, answers := range [][]netip.Addr{
		{netip.MustParseAddr("127.0.0.1")},
		{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.1")},
		{netip.MustParseAddr("2606:4700::1111"), netip.MustParseAddr("::ffff:169.254.169.254")},
		{netip.Addr{}},
	} {
		deps := dependencies{resolve: func(context.Context, string, string) ([]netip.Addr, error) { return answers, nil }}
		if _, err := do(context.Background(), testRequest(), testPolicy(), deps); err != ErrAddressDenied {
			t.Errorf("answers %v: %v", answers, err)
		}
	}
	for _, resolver := range []func(context.Context, string, string) ([]netip.Addr, error){
		func(context.Context, string, string) ([]netip.Addr, error) { return nil, nil },
		func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, errors.New("secret upstream DNS detail")
		},
	} {
		if _, err := do(context.Background(), testRequest(), testPolicy(), dependencies{resolve: resolver}); err != ErrDNS {
			t.Errorf("DNS failure leaked details: %v", err)
		}
	}
}

func TestHTTPSPinnedNoProxyAndFilteredResponse(t *testing.T) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(key, "http://127.0.0.1:1")
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	deps, lookups, dials := testDependencies(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com:443" || r.TLS.ServerName != "example.com" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("hostname, TLS identity or authorization was lost")
		}
		if r.Header.Get("Accept-Encoding") != "" {
			t.Error("automatic compression enabled")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "request payload" {
			t.Error("request body lost")
		}
		w.Header().Set("Set-Cookie", "session=secret")
		w.Header().Set("Authorization", "secret")
		w.Header().Set("Location", "https://example.com?token=secret")
		w.Header().Set("X-Secret", "secret")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r := testRequest()
	r.URL = "https://EXAMPLE.com:443/path"
	r.Method, r.Body = "POST", "request payload"
	r.Headers = map[string]string{"Authorization": "Bearer secret"}
	resp, err := do(context.Background(), r, testPolicy(), deps)
	if err != nil || resp.Status != 201 || resp.Body != `{"ok":true}` {
		t.Fatalf("response=%+v error=%v", resp, err)
	}
	if resp.Headers["Content-Type"] != "application/json" || resp.Headers["Retry-After"] != "5" {
		t.Fatalf("safe headers missing: %v", resp.Headers)
	}
	for _, name := range []string{"Set-Cookie", "Authorization", "Location", "X-Secret"} {
		if _, ok := resp.Headers[name]; ok {
			t.Errorf("unsafe response header returned: %s", name)
		}
	}
	if lookups.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("lookup/dial counts = %d/%d", lookups.Load(), dials.Load())
	}
}

func TestDNSRebindingIsPinnedAndRecheckedNextRequest(t *testing.T) {
	deps, _, dials := testDependencies(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	lookups := 0
	deps.resolve = func(context.Context, string, string) ([]netip.Addr, error) {
		lookups++
		if lookups == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := do(context.Background(), testRequest(), testPolicy(), deps); err != nil {
		t.Fatalf("initial pinned request failed: %v", err)
	}
	if _, err := do(context.Background(), testRequest(), testPolicy(), deps); err != ErrAddressDenied {
		t.Fatalf("rebound request not rejected: %v", err)
	}
	if lookups != 2 || dials.Load() != 1 {
		t.Fatalf("lookup/dial counts = %d/%d", lookups, dials.Load())
	}
}

func TestRedirectNeverFollowed(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			deps, lookups, dials := testDependencies(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "https://127.0.0.1/metadata?token=secret")
				w.WriteHeader(status)
			})
			resp, err := do(context.Background(), testRequest(), testPolicy(), deps)
			if err != nil || resp.Status != status || lookups.Load() != 1 || dials.Load() != 1 {
				t.Fatalf("redirect response=%+v error=%v lookups=%d dials=%d", resp, err, lookups.Load(), dials.Load())
			}
		})
	}
}

func TestResponseLimits(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		for _, length := range []int{8, 9} {
			t.Run(fmt.Sprintf("chunked=%v/length=%d", chunked, length), func(t *testing.T) {
				deps, _, _ := testDependencies(t, func(w http.ResponseWriter, r *http.Request) {
					if chunked {
						w.(http.Flusher).Flush()
					} else {
						w.Header().Set("Content-Length", fmt.Sprint(length))
					}
					_, _ = w.Write([]byte(strings.Repeat("a", length)))
				})
				p := testPolicy()
				p.MaxResponseBytes = 8
				resp, err := do(context.Background(), testRequest(), p, deps)
				if length == 8 {
					if err != nil || len(resp.Body) != 8 {
						t.Fatalf("exact limit response=%+v error=%v", resp, err)
					}
				} else if err != ErrResponseTooLarge || resp.Body != "" || resp.Status != 0 {
					t.Fatalf("oversized response=%+v error=%v", resp, err)
				}
			})
		}
	}
	t.Run("headers", func(t *testing.T) {
		deps, _, _ := testDependencies(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Large", strings.Repeat("a", MaxHeaderBytes+1))
			_, _ = w.Write([]byte("ok"))
		})
		if _, err := do(context.Background(), testRequest(), testPolicy(), deps); err != ErrNetwork {
			t.Fatalf("oversized headers accepted: %v", err)
		}
	})
}

func TestTimeoutAndCancel(t *testing.T) {
	t.Run("DNS deadline", func(t *testing.T) {
		deps := dependencies{resolve: func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if _, err := do(ctx, testRequest(), testPolicy(), deps); err != ErrTimeout {
			t.Fatalf("DNS timeout: %v", err)
		}
	})
	t.Run("body deadline", func(t *testing.T) {
		deps, _, _ := testDependencies(t, func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		r := testRequest()
		r.TimeoutSeconds = 1
		started := time.Now()
		if _, err := do(context.Background(), r, testPolicy(), deps); err != ErrTimeout {
			t.Fatalf("body timeout: %v", err)
		}
		if time.Since(started) > 3*time.Second {
			t.Fatal("request timeout was not bounded")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := do(ctx, testRequest(), testPolicy(), dependencies{}); err != ErrCanceled {
			t.Fatalf("canceled request: %v", err)
		}
	})
}

func TestTLSVerificationAndRedactedErrors(t *testing.T) {
	t.Run("untrusted certificate", func(t *testing.T) {
		deps, _, _ := testDependencies(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unverified TLS reached handler") })
		deps.roots = x509.NewCertPool()
		if _, err := do(context.Background(), testRequest(), testPolicy(), deps); err != ErrNetwork {
			t.Fatalf("TLS failure not safely returned: %v", err)
		}
	})
	t.Run("hostname verification", func(t *testing.T) {
		deps, _, _ := testDependencies(t, func(w http.ResponseWriter, r *http.Request) { t.Error("wrong hostname reached handler") })
		deps.resolve = func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		r, p := testRequest(), testPolicy()
		r.URL, p.AllowedOrigins = "https://wrong.example/path?token=secret", []string{"https://wrong.example"}
		if _, err := do(context.Background(), r, p, deps); err != ErrNetwork {
			t.Fatalf("wrong hostname not rejected: %v", err)
		}
	})
	t.Run("dial error", func(t *testing.T) {
		deps := dependencies{
			resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
			},
			dial: func(context.Context, string, string) (net.Conn, error) {
				return nil, errors.New("https://token:secret@example.com?secret=yes")
			},
		}
		if _, err := do(context.Background(), testRequest(), testPolicy(), deps); err != ErrNetwork {
			t.Fatalf("dial error leaked: %v", err)
		}
	})
}

func TestTimeoutDefaultsAndMaximum(t *testing.T) {
	for _, tc := range []struct{ configured, expected int }{{0, 10}, {30, 30}} {
		r := testRequest()
		r.TimeoutSeconds = tc.configured
		deps := dependencies{resolve: func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			deadline, ok := ctx.Deadline()
			remaining := time.Until(deadline)
			if !ok || remaining > time.Duration(tc.expected)*time.Second || remaining < time.Duration(tc.expected-1)*time.Second {
				t.Errorf("timeout %d: remaining %v", tc.configured, remaining)
			}
			return nil, errors.New("stop before dialing")
		}}
		if _, err := do(context.Background(), r, testPolicy(), deps); err != ErrDNS {
			t.Fatal(err)
		}
	}
}

func TestLiteralIPsBypassDNSButRemainValidated(t *testing.T) {
	for _, raw := range []string{"8.8.8.8", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		ips, err := resolvePublic(context.Background(), raw, nil)
		if err != nil || len(ips) != 1 || ips[0] != netip.MustParseAddr(raw).Unmap() {
			t.Errorf("literal %s: %v, %v", raw, ips, err)
		}
	}
	if _, err := resolvePublic(context.Background(), "127.0.0.1", nil); err != ErrAddressDenied {
		t.Fatalf("private literal accepted: %v", err)
	}
}

func TestConfiguredRequestAndDefaultResponseLimits(t *testing.T) {
	r, p := testRequest(), testPolicy()
	r.Body, p.MaxRequestBytes = "12345", 4
	if _, err := do(context.Background(), r, p, dependencies{}); err != ErrRequestTooLarge {
		t.Fatalf("configured request limit ignored: %v", err)
	}
	if _, err := requestHeaders(map[string]string{
		"X-One": strings.Repeat("a", MaxHeaderBytes/2),
		"X-Two": strings.Repeat("b", MaxHeaderBytes/2),
	}); err != ErrRequestTooLarge {
		t.Fatalf("aggregate header limit ignored: %v", err)
	}
	deps, _, _ := testDependencies(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(MaxResponseBytes+1))
		_, _ = w.Write([]byte(strings.Repeat("a", int(MaxResponseBytes)+1)))
	})
	if _, err := do(context.Background(), testRequest(), testPolicy(), deps); err != ErrResponseTooLarge {
		t.Fatalf("default response limit ignored: %v", err)
	}
}

func TestPublicDoDeniesEmptyPolicy(t *testing.T) {
	if _, err := Do(context.Background(), testRequest(), Policy{}); err != ErrInvalidPolicy {
		t.Fatalf("empty policy accepted: %v", err)
	}
}
