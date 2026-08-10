package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/OboardProject/oboard/internal/subrelay"
)

const maxResponseBytes = 16 << 20

type relay struct {
	upstream       *url.URL
	secret         string
	client         *http.Client
	trustedProxies []netip.Prefix
	slots          chan struct{}
}

func main() {
	address := flag.String("addr", env("OBOARD_SUBSCRIPTION_RELAY_ADDR", ":8080"), "listen address")
	upstreamValue := flag.String("upstream", env("OBOARD_CONTROLLER_URL", ""), "Controller base URL")
	secret := flag.String("secret", env("OBOARD_SUBSCRIPTION_RELAY_SECRET", ""), "shared relay secret")
	allowHTTP := flag.Bool("allow-http-upstream", false, "allow an insecure upstream for local testing")
	trustedProxyValue := flag.String("trusted-proxy-cidrs", env("OBOARD_SUBSCRIPTION_RELAY_TRUSTED_PROXY_CIDRS", "127.0.0.0/8,::1/128"), "comma-separated reverse proxy networks")
	flag.Parse()
	upstream, err := validateUpstream(*upstreamValue, *allowHTTP)
	if err != nil {
		log.Fatal(err)
	}
	if err := subrelay.ValidateSecret(*secret); err != nil {
		log.Fatal(err)
	}
	trustedProxies, err := parseTrustedProxies(*trustedProxyValue)
	if err != nil {
		log.Fatal(err)
	}
	handler := &relay{upstream: upstream, secret: *secret, trustedProxies: trustedProxies, slots: make(chan struct{}, 256), client: &http.Client{Timeout: 65 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 64, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second}}}
	server := &http.Server{Addr: *address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 70 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("OBoard subscription relay listening on %s", *address)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (s *relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	healthPath := strings.TrimSuffix(s.upstream.EscapedPath(), "/") + "/healthz"
	if r.URL.Path == healthPath {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
		return
	}
	if r.Method != http.MethodGet || !allowedPath(r.URL.Path, s.upstream.EscapedPath()) {
		http.NotFound(w, r)
		return
	}
	if s.slots != nil {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
		default:
			http.Error(w, "subscription relay is busy", http.StatusServiceUnavailable)
			return
		}
	}
	clientIP, err := s.clientIP(r)
	if err != nil {
		http.Error(w, "invalid client address", http.StatusBadRequest)
		return
	}
	target := *s.upstream
	target.Path = r.URL.Path
	target.RawQuery = r.URL.RawQuery
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), nil)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	request.Header.Set("User-Agent", r.UserAgent())
	request.Header.Set("Accept", r.Header.Get("Accept"))
	request.Header.Set("If-None-Match", r.Header.Get("If-None-Match"))
	timestamp := fmt.Sprint(time.Now().Unix())
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		http.Error(w, "relay unavailable", http.StatusServiceUnavailable)
		return
	}
	nonce := hex.EncodeToString(nonceBytes)
	request.Header.Set(subrelay.HeaderTimestamp, timestamp)
	request.Header.Set(subrelay.HeaderNonce, nonce)
	request.Header.Set(subrelay.HeaderClientIP, clientIP)
	request.Header.Set(subrelay.HeaderSignature, subrelay.Sign(s.secret, request.Method, request.URL.RequestURI(), timestamp, nonce, clientIP, request.UserAgent(), request.Header.Get("If-None-Match")))
	response, err := s.client.Do(request)
	if err != nil {
		http.Error(w, "subscription upstream unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	copyResponseHeaders(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, maxResponseBytes+1))
}

func validateUpstream(raw string, allowHTTP bool) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Host == "" || target.User != nil || target.RawQuery != "" || target.Fragment != "" {
		return nil, errors.New("OBOARD_CONTROLLER_URL must be a valid Controller base URL")
	}
	if target.Scheme != "https" && !(allowHTTP && target.Scheme == "http") {
		return nil, errors.New("OBOARD_CONTROLLER_URL must use HTTPS")
	}
	return target, nil
}

func allowedPath(path, basePath string) bool {
	if strings.Contains(path, "//") || strings.Contains(path, "..") {
		return false
	}
	basePath = strings.TrimSuffix(basePath, "/")
	relative := strings.TrimPrefix(path, basePath)
	if basePath != "" && relative == path {
		return false
	}
	return strings.HasPrefix(relative, "/api/v1/subscriptions/") || strings.HasPrefix(relative, "/s/")
}

func remoteIP(value string) (string, error) {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return "", err
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "", err
	}
	return address.Unmap().String(), nil
}

func parseTrustedProxies(raw string) ([]netip.Prefix, error) {
	items := []netip.Prefix{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy CIDR %q", value)
		}
		items = append(items, prefix.Masked())
	}
	return items, nil
}

func (s *relay) clientIP(r *http.Request) (string, error) {
	direct, err := remoteIP(r.RemoteAddr)
	if err != nil {
		return "", err
	}
	directAddress, _ := netip.ParseAddr(direct)
	trusted := false
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(directAddress) {
			trusted = true
			break
		}
	}
	if !trusted {
		return direct, nil
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for index := len(forwarded) - 1; index >= 0; index-- {
		value := strings.TrimSpace(forwarded[index])
		if value == "" && len(forwarded) == 1 {
			return direct, nil
		}
		candidate, parseErr := netip.ParseAddr(value)
		if parseErr != nil {
			return "", errors.New("invalid X-Forwarded-For address")
		}
		candidate = candidate.Unmap()
		candidateTrusted := false
		for _, prefix := range s.trustedProxies {
			if prefix.Contains(candidate) {
				candidateTrusted = true
				break
			}
		}
		if !candidateTrusted {
			return candidate.String(), nil
		}
	}
	return direct, nil
}

func copyResponseHeaders(target, source http.Header) {
	for _, name := range []string{"Content-Type", "Content-Disposition", "Cache-Control", "Pragma", "ETag", "Last-Modified", "Retry-After", "Subscription-Encryption", "Subscription-Userinfo", "Profile-Update-Interval", "Profile-Web-Page-Url", "X-OBoard-Subscription"} {
		for _, value := range source.Values(name) {
			target.Add(name, value)
		}
	}
	target.Set("X-Content-Type-Options", "nosniff")
	target.Set("Referrer-Policy", "no-referrer")
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
