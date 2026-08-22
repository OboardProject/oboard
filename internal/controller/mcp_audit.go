package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/auditreview"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// registerAuditAutomationOperations wires the AI audit review operations of
// the MCP automation layer. The audit read surfaces are served through MCP
// resources (mcpAuditQuery* helpers) because they depend on Controller state
// such as the live audit policy and GeoIP status.

func (s *Server) registerAuditAutomationOperations() {
	s.automation.RegisterValidator("audit.ai_reviews.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := s.auditReviewCreateCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"review_id": request.RequestID, "status": "pending", "job_count": 0}, nil
	})
	s.automation.RegisterRevisionResolver("audit.ai_reviews.create", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("audit.ai_reviews.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := s.auditReviewCreateCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if principal.UserID == nil {
			return nil, errors.New("authentication required")
		}
		item, err := s.auditReviews.Create(ctx, auditreview.CreateRequest{
			RequestID: request.RequestID, ProviderID: request.ProviderID, RequestedBy: *principal.UserID,
			Scope: request.Scope, EvidenceTypes: request.EvidenceTypes, WindowStart: request.Start, WindowEnd: request.End,
		})
		if err != nil {
			return nil, err
		}
		s.publishRealtime("audit", "ai-reviews")
		return map[string]any{"review_id": item.ID, "status": item.Status, "job_count": item.JobCount}, nil
	})

	s.automation.RegisterValidator("audit.ai_reviews.cancel", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		reviewID, err := auditReviewCancelInput(input)
		if err != nil {
			return nil, err
		}
		review, err := s.store.GetAuditReview(ctx, reviewID)
		if err != nil {
			return nil, err
		}
		if review.Status != "queued" && review.Status != "running" {
			return nil, errors.New("only queued or running reviews can be cancelled")
		}
		return map[string]any{"review_id": reviewID}, nil
	})
	s.automation.RegisterRevisionResolver("audit.ai_reviews.cancel", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("audit.ai_reviews.cancel", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		reviewID, err := auditReviewCancelInput(input)
		if err != nil {
			return nil, err
		}
		if err := s.store.CancelAuditReview(ctx, reviewID); err != nil {
			return nil, err
		}
		s.publishRealtime("audit", "ai-reviews")
		return map[string]any{"cancelled": true, "review_id": reviewID}, nil
	})

	s.automation.RegisterValidator("audit.ai_reviews.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		review, err := s.auditReviewDeleteCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"review_id": review.ID, "status": review.Status}, nil
	})
	s.automation.RegisterRevisionResolver("audit.ai_reviews.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		review, err := s.auditReviewDeleteCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"ai-audit-review:" + review.ID: review.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("audit.ai_reviews.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		review, err := s.auditReviewDeleteCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteAuditReview(ctx, review.ID); err != nil {
			return nil, err
		}
		s.publishRealtime("audit", "ai-reviews")
		return map[string]any{"deleted": true, "review_id": review.ID}, nil
	})
}

type auditReviewCreateRequest struct {
	RequestID     string                 `json:"request_id"`
	ProviderID    string                 `json:"provider_id"`
	Scope         model.AuditReviewScope `json:"scope"`
	EvidenceTypes []string               `json:"evidence_types"`
	Start         time.Time              `json:"-"`
	End           time.Time              `json:"-"`
}

func (s *Server) auditReviewCreateCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (auditReviewCreateRequest, error) {
	var request struct {
		RequestID     string                 `json:"request_id"`
		ProviderID    string                 `json:"provider_id"`
		Scope         model.AuditReviewScope `json:"scope"`
		EvidenceTypes []string               `json:"evidence_types"`
		TimeRange     struct {
			Mode      string     `json:"mode"`
			Preset    string     `json:"preset"`
			StartedAt *time.Time `json:"started_at"`
			EndedAt   *time.Time `json:"ended_at"`
		} `json:"time_range"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return auditReviewCreateRequest{}, err
	}
	for _, userID := range request.Scope.Users.IDs {
		if !principal.AllowsInt64("user_ids", userID) {
			return auditReviewCreateRequest{}, errors.New("review scope includes an unauthorized user")
		}
	}
	for _, serverID := range request.Scope.Servers.IDs {
		if !principal.AllowsInt64("server_ids", serverID) {
			return auditReviewCreateRequest{}, errors.New("review scope includes an unauthorized server")
		}
	}
	start, end, err := auditReviewTimeRange(request.TimeRange.Mode, request.TimeRange.Preset, request.TimeRange.StartedAt, request.TimeRange.EndedAt, time.Now().UTC())
	if err != nil {
		return auditReviewCreateRequest{}, err
	}
	if providerID := strings.TrimSpace(request.ProviderID); providerID != "" {
		provider, err := s.store.GetAIProvider(ctx, providerID)
		if err != nil {
			return auditReviewCreateRequest{}, errors.New("provider_id must reference an enabled AI provider")
		}
		if !provider.Enabled {
			return auditReviewCreateRequest{}, errors.New("provider_id must reference an enabled AI provider")
		}
	}
	return auditReviewCreateRequest{
		RequestID: strings.TrimSpace(request.RequestID), ProviderID: strings.TrimSpace(request.ProviderID),
		Scope: request.Scope, EvidenceTypes: request.EvidenceTypes, Start: start, End: end,
	}, nil
}

func auditReviewCancelInput(input json.RawMessage) (string, error) {
	var request struct {
		ReviewID string `json:"review_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil || strings.TrimSpace(request.ReviewID) == "" {
		return "", errors.New("review_id is required")
	}
	return strings.TrimSpace(request.ReviewID), nil
}

func (s *Server) auditReviewDeleteCandidate(ctx context.Context, input json.RawMessage) (*model.AuditReview, error) {
	var request struct {
		ReviewID string `json:"review_id"`
		Confirm  bool   `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	request.ReviewID = strings.TrimSpace(request.ReviewID)
	if request.ReviewID == "" || !request.Confirm {
		return nil, errors.New("review_id and confirm=true are required")
	}
	review, err := s.store.GetAuditReview(ctx, request.ReviewID)
	if err != nil {
		return nil, err
	}
	if review.Status == "queued" || review.Status == "running" {
		return nil, store.ErrAuditReviewActive
	}
	jobs, err := s.store.ListAuditReviewJobs(ctx, review.ID, false)
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		if job.Status == "running" {
			return nil, store.ErrAuditReviewActive
		}
	}
	return review, nil
}

// ---- MCP audit resource queries ----

func (s *Server) mcpAuditConnectionOverview(ctx context.Context, principal application.Principal, windowHours int) (any, error) {
	overview, err := s.store.ConnectionAuditOverview(ctx, windowHours, s.connectionAuditEnabled(ctx), s.auditPolicy(ctx))
	if err != nil {
		return nil, err
	}
	overview.GeoDatabase = s.geoIPStatus
	overview.Users = filterAuditUsers(overview.Users, principal)
	return overview, nil
}

func (s *Server) mcpAuditConnectionUser(ctx context.Context, principal application.Principal, userID, windowHours int64) (any, error) {
	if !principal.AllowsInt64("user_ids", userID) {
		return nil, errors.New("not authorized")
	}
	detail, err := s.store.ConnectionAuditUserDetail(ctx, userID, int(windowHours), s.auditPolicy(ctx))
	if err != nil {
		return nil, err
	}
	return detail, nil
}

func (s *Server) mcpAuditSubscriptionOverview(ctx context.Context, principal application.Principal, windowHours int) (any, error) {
	overview, err := s.subscriptionAuditOverviewData(ctx, windowHours)
	if err != nil {
		return nil, err
	}
	overview.Users = filterSubscriptionAuditUsers(overview.Users, principal)
	return overview, nil
}

func (s *Server) mcpAuditSubscriptionUser(ctx context.Context, principal application.Principal, userID, windowHours int64) (any, error) {
	if !principal.AllowsInt64("user_ids", userID) {
		return nil, errors.New("not authorized")
	}
	return s.store.SubscriptionAuditUserDetail(ctx, userID, int(windowHours), s.auditPolicy(ctx))
}

func (s *Server) mcpAuditRiskOverview(ctx context.Context, principal application.Principal, windowHours int) (any, error) {
	_, _, combined, err := s.auditOverviewData(ctx, windowHours)
	if err != nil {
		return nil, err
	}
	combined.Users = filterCombinedAuditUsers(combined.Users, principal)
	return combined, nil
}

func (s *Server) mcpAuditLogs(ctx context.Context, principal application.Principal, limit, offset int, action string) (any, error) {
	items, err := s.store.ListAuditPage(ctx, limit, offset, action)
	if err != nil {
		return nil, err
	}
	logs := make([]map[string]any, 0, len(items))
	for _, item := range items {
		logs = append(logs, map[string]any{
			"id": item.ID, "actor": item.ActorID, "action": item.Action, "target": item.Target,
			"detail": item.Detail, "ip": item.IP, "created_at": item.CreatedAt,
		})
	}
	nextOffset := offset + len(logs)
	return map[string]any{"logs": logs, "count": len(logs), "offset": offset, "next_offset": nextOffset}, nil
}

func (s *Server) mcpAuditAIReviews(ctx context.Context, principal application.Principal, limit int) (any, error) {
	items, err := s.store.ListAuditReviews(ctx, limit)
	if err != nil {
		return nil, err
	}
	reviews := make([]map[string]any, 0, len(items))
	for _, item := range items {
		reviews = append(reviews, map[string]any{
			"id": item.ID, "status": item.Status, "requested_by": item.RequestedBy,
			"window_started_at": item.WindowStartedAt, "window_ended_at": item.WindowEndedAt,
			"job_count": item.JobCount, "completed_job_count": item.CompletedJobCount,
			"created_at": item.CreatedAt, "completed_at": item.CompletedAt,
		})
	}
	return map[string]any{"reviews": reviews, "count": len(reviews)}, nil
}

func filterAuditUsers(users []model.ConnectionAuditUserSummary, principal application.Principal) []model.ConnectionAuditUserSummary {
	out := make([]model.ConnectionAuditUserSummary, 0, len(users))
	for _, user := range users {
		if principal.AllowsInt64("user_ids", user.UserID) {
			out = append(out, user)
		}
	}
	return out
}

func filterSubscriptionAuditUsers(users []model.SubscriptionAuditUserSummary, principal application.Principal) []model.SubscriptionAuditUserSummary {
	out := make([]model.SubscriptionAuditUserSummary, 0, len(users))
	for _, user := range users {
		if principal.AllowsInt64("user_ids", user.UserID) {
			out = append(out, user)
		}
	}
	return out
}

func filterCombinedAuditUsers(users []model.CombinedAuditUserSummary, principal application.Principal) []model.CombinedAuditUserSummary {
	out := make([]model.CombinedAuditUserSummary, 0, len(users))
	for _, user := range users {
		if principal.AllowsInt64("user_ids", user.UserID) {
			out = append(out, user)
		}
	}
	return out
}

func mcpAuditWindow(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > 720 {
		return 720
	}
	return value
}
