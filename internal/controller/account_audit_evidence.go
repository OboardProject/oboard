package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) queryAccountAuditEvidence(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
	if p.Role != model.RoleAdmin {
		return nil, errors.New("forbidden: administrator required")
	}
	var q store.AccountAuditEvidenceQuery
	if err := strictAutomationInput(input, &q); err != nil {
		return nil, err
	}
	return s.store.AccountAuditEvidence(ctx, q, func(id int64) bool { return p.AllowsInt64("user_ids", id) })
}

func (s *Server) accountAuditEvidenceHTTP(w http.ResponseWriter, r *http.Request, ui bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	q := store.AccountAuditEvidenceQuery{}
	var err error
	q.EventID, err = strconv.ParseInt(r.URL.Query().Get("event_id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid event_id", 400)
		return
	}
	for key, dest := range map[string]*int{"limit": &q.Limit, "offset": &q.Offset} {
		if raw := r.URL.Query().Get(key); raw != "" {
			*dest, err = strconv.Atoi(raw)
			if err != nil {
				http.Error(w, "invalid pagination", 400)
				return
			}
		}
	}
	if err = q.Validate(); err != nil {
		http.Error(w, "invalid evidence query", 400)
		return
	}
	p, _ := apiPrincipal(r)
	if ui {
		p = application.Principal{Role: model.RoleAdmin}
	}
	if _, ok := s.capabilities.Authorize(p, "audit.evidence.read"); !ui && !ok {
		v2Error(w, r, 403, "capability_denied", "无权读取审计证据")
		return
	}
	input, _ := json.Marshal(q)
	result, err := s.queryAccountAuditEvidence(r.Context(), p, input)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if ui {
		writeJSON(w, 200, result)
	} else {
		v2Write(w, r, 200, result, nil)
	}
}
func (s *Server) accountAuditEvidenceUI(w http.ResponseWriter, r *http.Request) {
	s.accountAuditEvidenceHTTP(w, r, true)
}
func (s *Server) apiV1AccountAuditEvidence(w http.ResponseWriter, r *http.Request) {
	s.accountAuditEvidenceHTTP(w, r, false)
}
