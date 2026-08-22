package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"embed"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/subrelay"
	"github.com/OboardProject/oboard/internal/version"
)

const subscriptionRelaySecretPurpose = "subscription-relay-signing-secret"

//go:embed assets/install-subscription-relay.sh
var subscriptionRelayAssets embed.FS

func (s *Server) subscriptionRelays(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.publicSubscriptionRelays(r.Context())
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"subscription_relays": items})
	case http.MethodPost:
		var request struct {
			Name      string `json:"name"`
			PublicURL string `json:"public_url"`
		}
		if !decode(w, r, &request) {
			return
		}
		request.Name = strings.TrimSpace(request.Name)
		if request.Name == "" || len(request.Name) > 80 {
			fail(w, errors.New("中继名称不能为空且不能超过 80 个字符"), http.StatusBadRequest)
			return
		}
		publicURL, err := s.normalizeSubscriptionRelayURL(request.PublicURL)
		if err != nil || publicURL == "" {
			fail(w, errors.New("中继公开地址必须是使用当前基础路径的 HTTPS 地址"), http.StatusBadRequest)
			return
		}
		if exists, err := s.store.SubscriptionRelayURLExists(r.Context(), publicURL, 0); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		} else if exists {
			fail(w, errors.New("该中继公开地址已存在"), http.StatusConflict)
			return
		}
		token, err := security.RandomToken(32)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
		relay := model.SubscriptionRelay{Name: request.Name, PublicURL: publicURL, Status: "pending", EnrollmentHash: security.HashSecret(token), EnrollmentExpiresAt: &expiresAt}
		if err := s.store.CreateSubscriptionRelay(r.Context(), &relay); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		auditReq(s, r, "create", "subscription-relay", strconv.FormatInt(relay.ID, 10))
		items, _ := s.publicSubscriptionRelays(r.Context())
		write(w, http.StatusCreated, map[string]any{"subscription_relay": findPublicSubscriptionRelay(items, relay.ID), "enrollment_token": token, "expires_at": expiresAt})
	default:
		method(w)
	}
}

func (s *Server) subscriptionRelaySubroutes(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/subscription-relays/")
	if len(parts) == 0 {
		fail(w, errors.New("missing relay id"), http.StatusBadRequest)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		fail(w, errors.New("invalid relay id"), http.StatusBadRequest)
		return
	}
	relay, err := s.store.GetSubscriptionRelay(r.Context(), id)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "enroll-token":
			if r.Method != http.MethodPost {
				method(w)
				return
			}
			token, err := security.RandomToken(32)
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
			if err := s.store.SetSubscriptionRelayEnrollment(r.Context(), id, security.HashSecret(token), expiresAt); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			auditReq(s, r, "create", "subscription-relay-enroll-token", strconv.FormatInt(id, 10))
			write(w, http.StatusOK, map[string]any{"enrollment_token": token, "expires_at": expiresAt})
			return
		case "activate":
			if r.Method != http.MethodPost {
				method(w)
				return
			}
			if relay.TokenHash == "" || relay.SigningSecretEncrypted == "" {
				fail(w, errors.New("中继尚未接入，不能设为订阅入口"), http.StatusConflict)
				return
			}
			if !subscriptionRelayRecentlySeen(relay, time.Now()) {
				fail(w, errors.New("中继当前不在线（最近 2 分钟没有心跳），请等待其恢复在线后再设为订阅入口"), http.StatusConflict)
				return
			}
			if err := s.store.SetSettings(r.Context(), map[string]string{settingSubscriptionRelayURL: relay.PublicURL, settingSubscriptionControllerDirectEnabled: "false"}); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			auditReq(s, r, "activate", "subscription-relay", strconv.FormatInt(id, 10))
			write(w, http.StatusOK, map[string]any{"active": true})
			return
		case "update":
			if r.Method != http.MethodPost {
				method(w)
				return
			}
			if relay.TokenHash == "" {
				fail(w, errors.New("中继尚未接入，不能下发更新"), http.StatusConflict)
				return
			}
			if err := s.store.RequestSubscriptionRelayUpdate(r.Context(), id, version.Version, version.Build); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			auditReq(s, r, "update", "subscription-relay", strconv.FormatInt(id, 10))
			write(w, http.StatusAccepted, map[string]any{"status": "updating", "target_version": version.Version, "target_build": version.Build})
			return
		}
	}
	switch r.Method {
	case http.MethodPatch:
		var request struct {
			Name      string `json:"name"`
			PublicURL string `json:"public_url"`
		}
		if !decode(w, r, &request) {
			return
		}
		request.Name = strings.TrimSpace(request.Name)
		if request.Name == "" || len(request.Name) > 80 {
			fail(w, errors.New("中继名称不能为空且不能超过 80 个字符"), http.StatusBadRequest)
			return
		}
		publicURL, err := s.normalizeSubscriptionRelayURL(request.PublicURL)
		if err != nil || publicURL == "" {
			fail(w, errors.New("中继公开地址必须是使用当前基础路径的 HTTPS 地址"), http.StatusBadRequest)
			return
		}
		if exists, err := s.store.SubscriptionRelayURLExists(r.Context(), publicURL, id); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		} else if exists {
			fail(w, errors.New("该中继公开地址已存在"), http.StatusConflict)
			return
		}
		settings, _ := s.store.ListSettings(r.Context())
		wasActive := strings.TrimRight(settings[settingSubscriptionRelayURL], "/") == strings.TrimRight(relay.PublicURL, "/")
		relay.Name, relay.PublicURL = request.Name, publicURL
		if err := s.store.UpdateSubscriptionRelay(r.Context(), relay); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		if wasActive {
			if err := s.store.SetSetting(r.Context(), settingSubscriptionRelayURL, publicURL); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
		}
		auditReq(s, r, "update", "subscription-relay", strconv.FormatInt(id, 10))
		items, _ := s.publicSubscriptionRelays(r.Context())
		write(w, http.StatusOK, map[string]any{"subscription_relay": findPublicSubscriptionRelay(items, id)})
	case http.MethodDelete:
		if err := s.store.DeleteSubscriptionRelay(r.Context(), id); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		auditReq(s, r, "delete", "subscription-relay", strconv.FormatInt(id, 10))
		write(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) subscriptionRelayEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request struct {
		EnrollmentToken string `json:"enrollment_token"`
	}
	if !decode(w, r, &request) {
		return
	}
	if strings.TrimSpace(request.EnrollmentToken) == "" {
		fail(w, errors.New("接入令牌不能为空"), http.StatusBadRequest)
		return
	}
	token, err := security.RandomToken(32)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	signingSecret, err := security.RandomToken(32)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	encrypted, err := security.EncryptSecret(s.sessionSecret, subscriptionRelaySecretPurpose, signingSecret)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	relay, err := s.store.ClaimSubscriptionRelayEnrollment(r.Context(), security.HashSecret(request.EnrollmentToken), security.HashSecret(token), encrypted)
	if err != nil {
		fail(w, errors.New("接入令牌无效或已过期"), http.StatusUnauthorized)
		return
	}
	write(w, http.StatusOK, map[string]any{"relay_id": relay.ID, "relay_token": token, "signing_secret": signingSecret, "public_url": relay.PublicURL})
}

func (s *Server) subscriptionRelayHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	relay, ok := s.authenticateSubscriptionRelay(w, r, body)
	if !ok {
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var request struct {
		Version        string  `json:"version"`
		Build          string  `json:"build"`
		Commit         string  `json:"commit"`
		OS             string  `json:"os"`
		Arch           string  `json:"arch"`
		ServiceManager string  `json:"service_manager"`
		UpdateError    *string `json:"update_error,omitempty"`
	}
	if !decode(w, r, &request) {
		return
	}
	now := time.Now().UTC()
	relay.Version = boundedRelayField(request.Version, 64)
	relay.Build = boundedRelayField(request.Build, 64)
	relay.Commit = boundedRelayField(request.Commit, 64)
	relay.OS = boundedRelayField(request.OS, 32)
	relay.Arch = boundedRelayField(request.Arch, 32)
	relay.ServiceManager = boundedRelayField(request.ServiceManager, 32)
	relay.Status = "online"
	relay.LastSeenAt = &now
	if request.UpdateError != nil {
		relay.LastUpdateError = boundedRelayField(*request.UpdateError, 500)
		if relay.LastUpdateError != "" {
			relay.Status = "failed"
		}
	}
	if err := s.store.UpdateSubscriptionRelayHeartbeat(r.Context(), relay); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.store.SetSubscriptionRelayActiveIfUnset(r.Context(), relay.PublicURL); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if relay.UpdateRequestedAt != nil && relay.UpdateTargetBuild != "" && relay.Build == relay.UpdateTargetBuild {
		if err := s.store.CompleteSubscriptionRelayUpdate(r.Context(), relay.ID); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"action": "none"})
		return
	}
	if relay.UpdateRequestedAt != nil && relay.LastUpdateError == "" {
		write(w, http.StatusOK, map[string]any{"action": "update", "target_version": relay.UpdateTargetVersion, "target_build": relay.UpdateTargetBuild})
		return
	}
	write(w, http.StatusOK, map[string]any{"action": "none"})
}

func (s *Server) subscriptionRelayUninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	relay, ok := s.authenticateSubscriptionRelay(w, r, body)
	if !ok {
		return
	}
	if err := s.store.MarkSubscriptionRelayUninstalled(r.Context(), relay.ID); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"acknowledged": true})
}

func (s *Server) authenticateSubscriptionRelay(w http.ResponseWriter, r *http.Request, body []byte) (*model.SubscriptionRelay, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get("X-OBoard-Relay-ID")), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid relay credentials", http.StatusUnauthorized)
		return nil, false
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	relay, err := s.store.GetSubscriptionRelay(r.Context(), id)
	if err != nil || token == "" || !hmac.Equal([]byte(relay.TokenHash), []byte(security.HashSecret(token))) {
		http.Error(w, "invalid relay credentials", http.StatusUnauthorized)
		return nil, false
	}
	signingSecret, err := security.DecryptSecret(s.sessionSecret, subscriptionRelaySecretPurpose, relay.SigningSecretEncrypted)
	now := time.Now()
	if err != nil || subrelay.VerifyControl(signingSecret, strconv.FormatInt(id, 10), r.Method, r.URL.RequestURI(), r.Header.Get(subrelay.HeaderTimestamp), r.Header.Get(subrelay.HeaderNonce), body, r.Header.Get(subrelay.HeaderSignature), now) != nil || !s.consumeSubscriptionRelayNonce(r.Header.Get(subrelay.HeaderNonce), now) {
		http.Error(w, "invalid relay credentials", http.StatusUnauthorized)
		return nil, false
	}
	return relay, true
}

func (s *Server) subscriptionRelayInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	payload, err := subscriptionRelayAssets.ReadFile("assets/install-subscription-relay.sh")
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(payload)
}

func subscriptionRelayRecentlySeen(relay *model.SubscriptionRelay, now time.Time) bool {
	return relay.LastSeenAt != nil && now.UTC().Sub(*relay.LastSeenAt) <= 2*time.Minute
}

// subscriptionRelayURLMatchesEnrolled reports whether raw equals the public URL
// of at least one relay that finished enrollment. An empty value clears the
// active entry and is always accepted.
func (s *Server) subscriptionRelayURLMatchesEnrolled(ctx context.Context, raw string) (bool, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return true, nil
	}
	items, err := s.store.ListSubscriptionRelays(ctx)
	if err != nil {
		return false, err
	}
	for _, relay := range items {
		if relay.TokenHash != "" && strings.TrimRight(relay.PublicURL, "/") == value {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) publicSubscriptionRelays(ctx context.Context) ([]map[string]any, error) {
	items, err := s.store.ListSubscriptionRelays(ctx)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return nil, err
	}
	activeURL := strings.TrimRight(settings[settingSubscriptionRelayURL], "/")
	controllerURL, _ := s.publicBaseURL(ctx)
	installPreview := ""
	if controllerURL != "" {
		releaseVersion := version.Version
		if strings.Contains(releaseVersion, "dev") {
			releaseVersion = "dev"
		}
		installPreview = "curl -fsSL " + shellSingleQuote(controllerURL+"/install/subscription-relay.sh") + ` | env VERSION=` + shellSingleQuote(releaseVersion) + ` OBOARD_CONTROLLER_URL=` + shellSingleQuote(controllerURL) + ` OBOARD_SUBSCRIPTION_RELAY_ENROLLMENT_TOKEN='<one-time-token>' /bin/sh`
	}
	now := time.Now().UTC()
	out := make([]map[string]any, 0, len(items))
	for _, relay := range items {
		status := relay.Status
		if relay.TokenHash == "" && status != "uninstalled" {
			status = "pending"
		} else if status != "uninstalled" && relay.LastSeenAt != nil && now.Sub(*relay.LastSeenAt) > 2*time.Minute {
			status = "offline"
		}
		out = append(out, map[string]any{
			"id": relay.ID, "name": relay.Name, "public_url": relay.PublicURL, "status": status,
			"enrolled": relay.TokenHash != "", "active": activeURL != "" && strings.TrimRight(relay.PublicURL, "/") == activeURL,
			"install_command_preview": installPreview, "version": relay.Version, "build": relay.Build, "commit": relay.Commit, "os": relay.OS, "arch": relay.Arch,
			"service_manager": relay.ServiceManager, "update_target_version": relay.UpdateTargetVersion,
			"update_target_build": relay.UpdateTargetBuild, "update_requested_at": relay.UpdateRequestedAt,
			"last_update_error": relay.LastUpdateError, "last_seen_at": relay.LastSeenAt,
			"enrollment_expires_at": relay.EnrollmentExpiresAt, "created_at": relay.CreatedAt, "updated_at": relay.UpdatedAt,
		})
	}
	return out, nil
}

func findPublicSubscriptionRelay(items []map[string]any, id int64) map[string]any {
	for _, item := range items {
		if value, _ := item["id"].(int64); value == id {
			return item
		}
	}
	return nil
}

func boundedRelayField(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}
