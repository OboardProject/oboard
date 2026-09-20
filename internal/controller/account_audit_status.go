package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/store"
)

type accountAuditStatusResponse struct {
	store.AccountAuditStatus
	AuditEnabled   bool   `json:"audit_enabled"`
	CollectionMode string `json:"collection_mode"`
	ActionMode     string `json:"action_mode"`
}

func (s *Server) queryAccountAuditStatus(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
	if err := strictAutomationInput(input, &struct{}{}); err != nil {
		return nil, err
	}
	var ids []int64
	if !p.AllowsInt64("user_ids", -1) {
		ids = []int64{}
		var filter struct {
			Users *application.ResourceSelection `json:"users"`
			IDs   []int64                        `json:"user_ids"`
		}
		if json.Unmarshal(p.ResourceFilter, &filter) == nil {
			candidates := filter.IDs
			if filter.Users != nil {
				candidates = filter.Users.IDs
			}
			if len(candidates) > 4096 {
				return nil, errors.New("audit resource filter too large")
			}
			for _, id := range candidates {
				if id > 0 && p.AllowsInt64("user_ids", id) {
					ids = append(ids, id)
				}
			}
		}
	}
	return s.readAccountAuditStatus(ctx, ids, ids == nil && p.AllowsInt64("server_ids", -1))
}
func (s *Server) readAccountAuditStatus(ctx context.Context, ids []int64, global bool) (accountAuditStatusResponse, error) {
	result := accountAuditStatusResponse{ActionMode: "alert_only"}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return result, err
	}
	result.AuditEnabled = settingBool(settings, settingAuditEnabled, false)
	collection, err := s.store.AuditCollection(ctx)
	if err != nil {
		return result, err
	}
	result.CollectionMode = collection.Mode
	result.AccountAuditStatus, err = s.store.AccountAuditStatus(ctx, ids, global, time.Now().UTC())
	return result, err
}
func (s *Server) accountAuditStatusUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	result, err := s.readAccountAuditStatus(r.Context(), nil, true)
	if err != nil {
		http.Error(w, "audit status unavailable", 500)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) apiV1AccountAuditStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		v2Error(w, r, 405, "method_not_allowed", "仅支持读取")
		return
	}
	p, _ := apiPrincipal(r)
	if _, ok := s.capabilities.Authorize(p, "audit.status.read"); !ok {
		v2Error(w, r, 403, "capability_denied", "无权读取审计")
		return
	}
	result, err := s.queryAccountAuditStatus(r.Context(), p, json.RawMessage(`{}`))
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, 200, result, nil)
}
