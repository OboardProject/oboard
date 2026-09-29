package pluginhttp

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

type testNetwork struct {
	mu        sync.Mutex
	answers   map[string][][]netip.Addr
	lookups   map[string]int
	dialed    []string
	server    *httptest.Server
	dialDelay time.Duration
}

func (n *testNetwork) deps() dependencies {
	roots := x509.NewCertPool()
	roots.AddCert(n.server.Certificate())
	return dependencies{
		resolve: func(_ context.Context, _ string, host string) ([]netip.Addr, error) {
			n.mu.Lock()
			defer n.mu.Unlock()
			sequence, ok := n.answers[host]
			if !ok {
				return nil, errors.New("nxdomain")
			}
			index := n.lookups[host]
			n.lookups[host]++
			if index >= len(sequence) {
				index = len(sequence) - 1
			}
			return sequence[index], nil
		},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			n.mu.Lock()
			n.dialed = append(n.dialed, address)
			n.mu.Unlock()
			if n.dialDelay > 0 {
				select {
				case <-time.After(n.dialDelay):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			var d net.Dialer
			return d.DialContext(ctx, "tcp", n.server.Listener.Addr().String())
		},
		roots: roots,
	}
}

func newTestNetwork(t *testing.T, handler http.HandlerFunc) *testNetwork {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.StartTLS()
	t.Cleanup(server.Close)
	return &testNetwork{server: server, answers: map[string][][]netip.Addr{}, lookups: map[string]int{}}
}

func public(addr string) []netip.Addr { return []netip.Addr{netip.MustParseAddr(addr)} }

func policy(hosts ...string) Policy {
	return Policy{Hosts: hosts, Methods: []string{"GET", "POST"}}
}

// The test certificate is issued for example.com, so request names below are
// served by it through the fake dialer.
func TestHostPatternsAndMethods(t *testing.T) {
	network := newTestNetwork(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	network.answers["example.com"] = [][]netip.Addr{public("93.184.216.34")}
	deps := network.deps()
	ctx := context.Background()
	if _, err := do(ctx, Request{URL: "https://example.com/", Method: "GET"}, policy("example.com"), deps); err != nil {
		t.Fatalf("allowed host failed: %v", err)
	}
	for _, test := range []struct {
		url      string
		patterns []string
		method   string
		want     error
	}{
		{"https://example.com/", []string{"*.example.com"}, "GET", ErrHostDenied},
		{"https://api.example.com/", []string{"example.com"}, "GET", ErrHostDenied},
		{"https://example.com:8443/", []string{"example.com"}, "GET", ErrHostDenied},
		{"https://example.com/", []string{"example.com"}, "DELETE", ErrMethodDenied},
		{"https://93.184.216.34/", []string{"example.com"}, "GET", ErrHostDenied},
		{"http://example.com/", []string{"example.com"}, "GET", ErrInvalidRequest},
		{"https://user:pw@example.com/", []string{"example.com"}, "GET", ErrInvalidRequest},
		{"file:///etc/passwd", []string{"example.com"}, "GET", ErrInvalidRequest},
		{"gopher://example.com/", []string{"example.com"}, "GET", ErrInvalidRequest},
	} {
		if _, err := do(ctx, Request{URL: test.url, Method: test.method}, policy(test.patterns...), deps); !errors.Is(err, test.want) {
			t.Errorf("%s via %v: got %v, want %v", test.url, test.patterns, err, test.want)
		}
	}
}

func TestPrivateAndMetadataAddressesAreRefused(t *testing.T) {
	network := newTestNetwork(t, func(w http.ResponseWriter, r *http.Request) {})
	for _, addr := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "224.0.0.1", "::1", "fe80::1", "fd00::1", "fd00:ec2::254", "::ffff:127.0.0.1", "64:ff9b::7f00:1"} {
		network.answers["example.com"] = [][]netip.Addr{public(addr)}
		network.lookups = map[string]int{}
		if _, err := do(context.Background(), Request{URL: "https://example.com/"}, policy("example.com"), network.deps()); !errors.Is(err, ErrPrivateAddress) {
			t.Errorf("%s: got %v", addr, err)
		}
		if PublicIP(netip.MustParseAddr(addr)) {
			t.Errorf("%s reported public", addr)
		}
	}
	network.answers["example.com"] = [][]netip.Addr{{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}}
	network.lookups = map[string]int{}
	if _, err := do(context.Background(), Request{URL: "https://example.com/"}, policy("example.com"), network.deps()); !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("a mixed answer must be refused: %v", err)
	}
	denyAll := policy("example.com")
	denyAll.Deny = func(netip.Addr) bool { return true }
	network.answers["example.com"] = [][]netip.Addr{public("93.184.216.34")}
	if _, err := do(context.Background(), Request{URL: "https://example.com/"}, denyAll, network.deps()); !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("host deny list (Controller and node addresses) ignored: %v", err)
	}
	if len(network.dialed) != 0 {
		t.Fatalf("refused addresses were dialed: %v", network.dialed)
	}
}

func TestDNSRebindingIsCheckedAtEveryDial(t *testing.T) {
	hits := 0
	network := newTestNetwork(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			http.Redirect(w, r, "https://example.com/second", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("done"))
	})
	// First resolution is public, the second (for the redirect's new
	// connection) turns private: the second dial must be refused.
	network.answers["example.com"] = [][]netip.Addr{public("93.184.216.34"), public("127.0.0.1")}
	_, err := do(context.Background(), Request{URL: "https://example.com/", FollowRedirects: true}, policy("example.com"), network.deps())
	if !errors.Is(err, ErrPrivateAddress) {
		t.Fatalf("rebinding to loopback was not refused: %v", err)
	}
}

func TestRedirectsAreRevalidated(t *testing.T) {
	network := newTestNetwork(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/escape":
			http.Redirect(w, r, "https://attacker.example.net/", http.StatusFound)
		case "/plain":
			http.Redirect(w, r, "http://example.com/", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			_, _ = w.Write([]byte("ok"))
		}
	})
	network.answers["example.com"] = [][]netip.Addr{public("93.184.216.34")}
	network.answers["attacker.example.net"] = [][]netip.Addr{public("198.41.0.4")}
	deps := network.deps()
	if _, err := do(context.Background(), Request{URL: "https://example.com/escape", FollowRedirects: true}, policy("example.com"), deps); !errors.Is(err, ErrHostDenied) {
		t.Fatalf("redirect to an ungranted host followed: %v", err)
	}
	if _, err := do(context.Background(), Request{URL: "https://example.com/plain", FollowRedirects: true}, policy("example.com"), deps); !errors.Is(err, ErrHostDenied) {
		t.Fatalf("redirect to plain HTTP followed: %v", err)
	}
	if _, err := do(context.Background(), Request{URL: "https://example.com/loop", FollowRedirects: true}, policy("example.com"), deps); !errors.Is(err, ErrHostDenied) {
		t.Fatalf("redirect loop not bounded: %v", err)
	}
	response, err := do(context.Background(), Request{URL: "https://example.com/escape"}, policy("example.com"), deps)
	if err != nil || response.Status != http.StatusFound {
		t.Fatalf("redirects are not followed by default: %+v %v", response, err)
	}
	// A request carrying a secret never follows redirects.
	response, err = do(context.Background(), Request{URL: "https://example.com/escape", FollowRedirects: true, SecretHeaders: map[string]string{"X-Api-Key": "k"}}, policy("example.com"), deps)
	if err != nil || response.Status != http.StatusFound {
		t.Fatalf("secret-bearing request followed a redirect: %+v %v", response, err)
	}
}

func TestSizeTimeoutAndHeaderLimits(t *testing.T) {
	network := newTestNetwork(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=1")
		if r.URL.Path == "/big" {
			_, _ = w.Write([]byte(strings.Repeat("x", 2048)))
			return
		}
		_, _ = w.Write([]byte("small"))
	})
	network.answers["example.com"] = [][]netip.Addr{public("93.184.216.34")}
	deps := network.deps()
	limited := policy("example.com")
	limited.MaxResponseBytes = 1024
	if _, err := do(context.Background(), Request{URL: "https://example.com/big"}, limited, deps); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversized response accepted: %v", err)
	}
	response, err := do(context.Background(), Request{URL: "https://example.com/small"}, limited, deps)
	if err != nil || string(response.Body) != "small" || response.Headers["set-cookie"] != "" {
		t.Fatalf("small response wrong: %+v %v", response, err)
	}
	if _, err := do(context.Background(), Request{URL: "https://example.com/", Method: "POST", Body: make([]byte, MaxRequestBytes+1)}, policy("example.com"), deps); !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("oversized request accepted: %v", err)
	}
	for _, header := range []string{"Host", "Cookie", "Transfer-Encoding", "Proxy-Authorization", "X-Forwarded-For", "Bad Name"} {
		if _, err := do(context.Background(), Request{URL: "https://example.com/", Headers: map[string]string{header: "x"}}, policy("example.com"), deps); err == nil {
			t.Errorf("header %q accepted", header)
		}
	}
	if _, err := do(context.Background(), Request{URL: "https://example.com/", Headers: map[string]string{"X-Test": "a\r\nInjected: 1"}}, policy("example.com"), deps); err == nil {
		t.Fatal("header injection accepted")
	}
	network.dialDelay = 500 * time.Millisecond
	if _, err := do(context.Background(), Request{URL: "https://example.com/", Timeout: 100 * time.Millisecond}, policy("example.com"), deps); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout not enforced: %v", err)
	}
	if _, err := do(context.Background(), Request{URL: "https://example.com/", Timeout: time.Minute}, policy("example.com"), deps); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("timeout ceiling not enforced: %v", err)
	}
}

func TestRedactCoversEncodings(t *testing.T) {
	secret := "s3cret+/=value"
	text := "raw=s3cret+/=value b64=czNjcmV0Ky89dmFsdWU= hex=733363726574" + "2b2f3d76616c7565 url=s3cret%2B%2F%3Dvalue"
	redacted := Redact(text, []string{secret})
	for _, leaked := range []string{"s3cret", "czNjcmV0Ky89dmFsdWU", "7333637265742b2f3d76616c7565"} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("secret form %q survived: %s", leaked, redacted)
		}
	}
}
