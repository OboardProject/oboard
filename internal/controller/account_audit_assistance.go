package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type accountAuditAssistanceRequest struct {
	UserID           int64  `json:"user_id"`
	EventID          int64  `json:"event_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	ProviderID       string `json:"provider_id"`
}

func (s *Server) accountAuditAssistanceCandidate(ctx context.Context, p application.Principal, input json.RawMessage) (accountAuditAssistanceRequest, error) {
	var req accountAuditAssistanceRequest
	if err := strictAutomationInput(input, &req); err != nil {
		return req, err
	}
	if !roleAllows(p.Role, model.RoleAdmin) || p.UserID == nil || req.UserID <= 0 || req.EventID <= 0 || req.ExpectedRevision <= 0 || len(req.ProviderID) > 128 || !p.AllowsInt64("user_ids", req.UserID) {
		return req, errors.New("audit assistance denied")
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
func (s *Server) registerAccountAuditAssistanceOperation() {
	const name = "audit.events.analyze"
	s.automation.RegisterValidator(name, func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		return s.accountAuditAssistanceCandidate(ctx, p, input)
	})
	s.automation.RegisterRevisionResolver(name, func(ctx context.Context, p application.Principal, input json.RawMessage) (map[string]string, error) {
		req, err := s.accountAuditAssistanceCandidate(ctx, p, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"audit_event:" + strconv.FormatInt(req.EventID, 10): strconv.FormatInt(req.ExpectedRevision, 10)}, nil
	})
	s.automation.Register(name, func(ctx context.Context, p application.Principal, input json.RawMessage) (any, error) {
		req, err := s.accountAuditAssistanceCandidate(ctx, p, input)
		if err != nil {
			return nil, err
		}
		result, err := s.store.QueueAccountAuditAssistance(ctx, req.UserID, req.EventID, req.ExpectedRevision, *p.UserID, req.ProviderID, time.Now().UTC())
		if err == nil && result.Status != "unavailable" {
			s.publishRealtime("audit", "ai-reviews")
		}
		return result, err
	})
}

func (s *Server) accountAuditEventsWithAssistance(ctx context.Context, q store.AccountAuditQuery) (store.AccountAuditPage, error) {
	page, err := s.store.ListAccountAuditEvents(ctx, q)
	if err != nil || q.EventID == 0 {
		return page, err
	}
	type detail struct {
		store.AccountAuditEvent
		Assistance *store.AccountAuditAssistance `json:"assistance"`
	}
	items := []detail{}
	for _, event := range page.Items.([]store.AccountAuditEvent) {
		analysis, err := s.store.GetAccountAuditAssistance(ctx, event.UserID, event.ID, event.Revision)
		if err != nil {
			return page, err
		}
		items = append(items, detail{event, analysis})
	}
	page.Items = items
	return page, nil
}
