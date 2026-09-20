package controller

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/store"
)

// Register on the existing base-path-scoped mux only. The receive route uses
// endpoint HMAC authentication; management additionally requires the Web admin.
func (s *Server) registerPluginWebhookRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/plugin-webhooks/receive/", s.apiV1PluginWebhookReceive)
	mux.HandleFunc("/api/v1/plugin-webhooks", s.apiAuth(s.apiV1PluginWebhooks, model.RoleAdmin))
	mux.HandleFunc("/api/v1/plugin-webhooks/", s.apiAuth(s.apiV1PluginWebhookItem, model.RoleAdmin))
}

func (s *Server) apiV1PluginWebhookReceive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/plugin-webhooks/receive/")
	if len(id) != 64 || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	if r.URL.RawQuery != "" {
		http.Error(w, "query parameters are not accepted", http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, plugin.MaxWebhookBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "webhook body exceeds 64 KiB", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid request body", http.StatusBadRequest)
		}
		return
	}
	for _, name := range []string{"X-Oboard-Timestamp", "X-Oboard-Nonce", "X-Oboard-Signature"} {
		if len(r.Header.Values(name)) != 1 {
			http.Error(w, "invalid webhook authentication", http.StatusUnauthorized)
			return
		}
	}
	run, err := s.plugins.ReceiveWebhook(r.Context(), id, r.Header.Get("X-Oboard-Timestamp"), r.Header.Get("X-Oboard-Nonce"), r.Header.Get("X-Oboard-Signature"), body, s.sessionSecret)
	if err != nil {
		status := http.StatusForbidden
		switch {
		case errors.Is(err, plugin.ErrWebhookAuthentication):
			status = http.StatusUnauthorized
		case errors.Is(err, store.ErrPluginWebhookRate):
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", "60")
		case errors.Is(err, store.ErrPluginWebhookReplay):
			status = http.StatusConflict
		case plugin.CodeOf(err) == model.PluginErrorInvalidInput:
			status = http.StatusBadRequest
		case plugin.CodeOf(err) == model.PluginErrorLimitExceeded:
			status = http.StatusTooManyRequests
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	// No run snapshot, grant details or plugin output crosses the public endpoint.
	pluginOK(w, r, http.StatusAccepted, map[string]any{"accepted": true, "run_id": run.ID})
}

func (s *Server) apiV1PluginWebhooks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, _ := apiPrincipal(r)
	switch r.Method {
	case http.MethodGet:
		id, err := strconv.ParseInt(r.URL.Query().Get("plugin_id"), 10, 64)
		if err != nil || id <= 0 {
			http.Error(w, "plugin_id is required", http.StatusBadRequest)
			return
		}
		items, err := s.plugins.ListWebhooks(r.Context(), actor, id)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"webhooks": items})
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var input struct {
			BindingID int64 `json:"binding_id"`
			GrantID   int64 `json:"grant_id"`
		}
		if !decodeV2(w, r, &input) {
			return
		}
		item, secret, err := s.plugins.CreateWebhook(r.Context(), actor, input.BindingID, input.GrantID, s.sessionSecret)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusCreated, map[string]any{"webhook": item, "secret": secret})
	default:
		method(w)
	}
}

func (s *Server) apiV1PluginWebhookItem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/plugin-webhooks/")
	if len(id) != 64 || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var input struct {
		Generation int64 `json:"expected_generation"`
		Enabled    *bool `json:"enabled"`
		Rotate     bool  `json:"rotate_secret"`
	}
	if !decodeV2(w, r, &input) {
		return
	}
	if input.Enabled == nil || input.Generation <= 0 {
		http.Error(w, "enabled and expected_generation are required", http.StatusBadRequest)
		return
	}
	actor, _ := apiPrincipal(r)
	item, secret, err := s.plugins.ChangeWebhook(r.Context(), actor, id, input.Generation, *input.Enabled, input.Rotate, s.sessionSecret)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	result := map[string]any{"webhook": item}
	if secret != "" {
		result["secret"] = secret
	}
	pluginOK(w, r, http.StatusOK, result)
}
