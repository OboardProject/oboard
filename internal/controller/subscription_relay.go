package controller

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/subrelay"
)

const settingSubscriptionRelayURL = "subscription_relay_url"

func (s *Server) withSubscriptionRelay(next http.Handler) http.Handler {
	secret := strings.TrimSpace(os.Getenv("OBOARD_SUBSCRIPTION_RELAY_SECRET"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(subrelay.HeaderSignature) == "" {
			next.ServeHTTP(w, r)
			return
		}
		if secret == "" || !s.isSubscriptionRelayPath(r.URL.Path) {
			http.Error(w, "invalid subscription relay", http.StatusUnauthorized)
			return
		}
		clientIP := strings.TrimSpace(r.Header.Get(subrelay.HeaderClientIP))
		address, err := netip.ParseAddr(clientIP)
		if err != nil || address.IsUnspecified() || address.Zone() != "" {
			http.Error(w, "invalid subscription relay", http.StatusUnauthorized)
			return
		}
		err = subrelay.Verify(secret, r.Method, r.URL.RequestURI(), r.Header.Get(subrelay.HeaderTimestamp), r.Header.Get(subrelay.HeaderNonce), clientIP, r.UserAgent(), r.Header.Get("If-None-Match"), r.Header.Get(subrelay.HeaderSignature), time.Now())
		if err != nil || !s.consumeSubscriptionRelayNonce(r.Header.Get(subrelay.HeaderNonce), time.Now()) {
			http.Error(w, "invalid subscription relay", http.StatusUnauthorized)
			return
		}
		request := r.Clone(r.Context())
		request.RemoteAddr = net.JoinHostPort(address.String(), "0")
		request.Header.Del("Forwarded")
		request.Header.Del("X-Forwarded-For")
		request.Header.Del("X-Real-IP")
		next.ServeHTTP(w, request)
	})
}

func (s *Server) isSubscriptionRelayPath(path string) bool {
	basePath, ok := s.matchBasePath(path)
	if !ok {
		return false
	}
	relative := strings.TrimPrefix(path, basePath)
	if relative == "" {
		relative = "/"
	}
	return strings.HasPrefix(relative, "/api/v1/subscriptions/") || strings.HasPrefix(relative, "/s/")
}

func (s *Server) consumeSubscriptionRelayNonce(nonce string, now time.Time) bool {
	s.subscriptionRelayMu.Lock()
	defer s.subscriptionRelayMu.Unlock()
	for value, expires := range s.subscriptionRelayNonces {
		if !expires.After(now) {
			delete(s.subscriptionRelayNonces, value)
		}
	}
	if _, exists := s.subscriptionRelayNonces[nonce]; exists {
		return false
	}
	if len(s.subscriptionRelayNonces) >= 65536 {
		return false
	}
	s.subscriptionRelayNonces[nonce] = now.Add(subrelay.MaxClockSkew)
	return true
}
