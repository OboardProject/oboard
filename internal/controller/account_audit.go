package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/store"
)

type accountAuditReviewRequest struct {
	UserID           int64      `json:"user_id"`
	EventID          int64      `json:"event_id"`
	ExpectedRevision int64      `json:"expected_revision"`
	Status           string     `json:"status"`
	Reason           string     `json:"reason"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

func (s *Server) accountAuditReviewCandidate(ctx context.Context, p application.Principal, input json.RawMessage) (accountAuditReviewRequest, error) {
	var req accountAuditReviewRequest
	if err := strictAutomationInput(input, &req); err != nil {
		return req, err
	}
	if req.UserID <= 0 || req.EventID <= 0 || req.ExpectedRevision <= 0 || !p.AllowsInt64("user_ids", req.UserID) {
		return req, errors.New("audit account denied")
	}
	switch req.Status {
	case "pending", "observing", "handled", "closed", "false_positive":
	default:
		return req, errors.New("invalid review status")
	}
	if strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 2048 {
		return req, errors.New("review reason required")
	}
	if (req.Status == "observing" || req.Status == "false_positive") && req.ExpiresAt == nil {
		return req, errors.New("review expiry required")
	}
	if req.ExpiresAt != nil && (!req.ExpiresAt.After(time.Now()) || req.ExpiresAt.After(time.Now().Add(30*24*time.Hour))) {
		return req, errors.New("invalid review expiry")
	}
	page, err := s.store.ListAccountAuditEvents(ctx, store.AccountAuditQuery{UserID: req.UserID, EventID: req.EventID})
	if err != nil {
		return req, err
	}
	events := page.Items.([]store.AccountAuditEvent)
	if len(events) != 1 || events[0].Revision != req.ExpectedRevision {
		return req, errors.New("audit event revision conflict")
	}
	return req, nil
}

func (s *Server) registerAccountAuditReviewOperation() {
	const name = "audit.events.review"
	s.automation.RegisterValidator(name, func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		return s.accountAuditReviewCandidate(ctx, p, input)
	})
	s.automation.RegisterRevisionResolver(name, func(ctx context.Context, p application.Principal, input json.RawMessage) (map[string]string, error) {
		req, err := s.accountAuditReviewCandidate(ctx, p, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"audit_event:" + strconv.FormatInt(req.EventID, 10): strconv.FormatInt(req.ExpectedRevision, 10)}, nil
	})
	s.automation.Register(name, func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		req, err := s.accountAuditReviewCandidate(ctx, p, input)
		if err != nil {
			return nil, err
		}
		var expiry time.Time
		if req.ExpiresAt != nil {
			expiry = *req.ExpiresAt
		}
		actor := p.ID
		if actor == "" && p.UserID != nil {
			actor = "user:" + strconv.FormatInt(*p.UserID, 10)
		}
		if err := s.store.SetAccountAuditEventStatus(ctx, req.EventID, req.ExpectedRevision, req.Status, actor, req.Reason, expiry, time.Now().UTC()); err != nil {
			return nil, err
		}
		s.publishRealtime("audit")
		return map[string]any{"event_id": req.EventID, "revision": req.ExpectedRevision + 1, "status": req.Status, "execution_status": "applied"}, nil
	})
}

func accountAuditRequest(r *http.Request) (store.AccountAuditQuery, error) {
	q := store.AccountAuditQuery{Status: r.URL.Query().Get("status")}
	for key, dest := range map[string]*int{"limit": &q.Limit, "offset": &q.Offset} {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil {
				return q, err
			}
			*dest = n
		}
	}
	for key, dest := range map[string]*int64{"user_id": &q.UserID, "event_id": &q.EventID} {
		if raw := r.URL.Query().Get(key); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return q, err
			}
			*dest = n
		}
	}
	return q, q.Validate()
}
func (s *Server) accountAuditRead(ctx context.Context, kind string, q store.AccountAuditQuery) (store.AccountAuditPage, error) {
	if err := q.Validate(); err != nil {
		return store.AccountAuditPage{}, err
	}
	switch kind {
	case "accounts":
		return s.store.ListAccountAuditSnapshots(ctx, q)
	case "events":
		return s.accountAuditEventsWithAssistance(ctx, q)
	case "executions":
		return s.store.ListAccountAuditActions(ctx, q)
	default:
		return store.AccountAuditPage{}, errors.New("unknown audit view")
	}
}
func (s *Server) accountAuditUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	q, err := accountAuditRequest(r)
	if err != nil {
		http.Error(w, "invalid audit query", 400)
		return
	}
	result, err := s.accountAuditRead(r.Context(), strings.TrimPrefix(r.URL.Path, "/api/v1/audit/"), q)
	if err != nil {
		http.Error(w, "audit query unavailable", 500)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func (s *Server) apiV1AccountAudit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		v2Error(w, r, 405, "method_not_allowed", "仅支持读取")
		return
	}
	q, err := accountAuditRequest(r)
	if err != nil {
		v2Error(w, r, 400, "invalid_audit_query", "查询参数无效")
		return
	}
	kind := strings.TrimPrefix(r.URL.Path, "/api/v1/audit/")
	principal, _ := apiPrincipal(r)
	if _, ok := s.capabilities.Authorize(principal, "audit."+kind+".list"); !ok {
		v2Error(w, r, 403, "capability_denied", "无权读取审计")
		return
	}
	input, _ := json.Marshal(q)
	result, err := s.queryAccountAudit(r.Context(), principal, "audit."+kind+".list", input)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, 200, result, nil)
}
func (s *Server) queryAccountAudit(ctx context.Context, p application.Principal, name string, input json.RawMessage) (any, error) {
	var q store.AccountAuditQuery
	if err := strictAutomationInput(input, &q); err != nil {
		return nil, err
	}
	if !p.AllowsInt64("user_ids", -1) {
		q.AllowedUserIDs = []int64{}
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
					q.AllowedUserIDs = append(q.AllowedUserIDs, id)
				}
			}
		}
	}
	return s.accountAuditRead(ctx, strings.TrimSuffix(strings.TrimPrefix(name, "audit."), ".list"), q)
}
