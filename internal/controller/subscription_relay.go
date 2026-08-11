package controller

import (
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/subrelay"
)

const (
	settingSubscriptionRelayURL                = "subscription_relay_url"
	settingSubscriptionControllerDirectEnabled = "subscription_controller_direct_enabled"
)

func (s *Server) withSubscriptionRelay(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isSubscriptionRelayPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		configuredURL, err := s.store.GetSetting(r.Context(), settingSubscriptionRelayURL)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		activeURL := strings.TrimRight(strings.TrimSpace(configuredURL), "/")
		if activeURL == "" {
			if r.Header.Get(subrelay.HeaderSignature) != "" {
				http.NotFound(w, r)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(subrelay.HeaderSignature) == "" {
			directSetting, err := s.store.GetSetting(r.Context(), settingSubscriptionControllerDirectEnabled)
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			directEnabled, _ := strconv.ParseBool(strings.TrimSpace(directSetting))
			if directEnabled {
				next.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
			return
		}
		relayID := strings.TrimSpace(r.Header.Get(subrelay.HeaderRelayID))
		id, idErr := strconv.ParseInt(relayID, 10, 64)
		relay, relayErr := s.store.GetSubscriptionRelay(r.Context(), id)
		if idErr != nil || relayErr != nil || relay.SigningSecretEncrypted == "" || strings.TrimRight(relay.PublicURL, "/") != activeURL {
			http.Error(w, "invalid subscription relay", http.StatusUnauthorized)
			return
		}
		secret, err := security.DecryptSecret(s.sessionSecret, subscriptionRelaySecretPurpose, relay.SigningSecretEncrypted)
		if err != nil {
			http.Error(w, "invalid subscription relay", http.StatusUnauthorized)
			return
		}
		clientIP := strings.TrimSpace(r.Header.Get(subrelay.HeaderClientIP))
		address, err := netip.ParseAddr(clientIP)
		if err != nil || address.IsUnspecified() || address.Zone() != "" {
			http.Error(w, "invalid subscription relay", http.StatusUnauthorized)
			return
		}
		err = subrelay.Verify(secret, relayID, r.Method, r.URL.RequestURI(), r.Header.Get(subrelay.HeaderTimestamp), r.Header.Get(subrelay.HeaderNonce), clientIP, r.UserAgent(), r.Header.Get("If-None-Match"), r.Header.Get(subrelay.HeaderSignature), time.Now())
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
