package controller

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/auditreview"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) auditAIReviews(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := s.store.ListAuditReviews(r.Context(), intQuery(r, "limit", 50))
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"ai_audit_reviews": items})
	case http.MethodPost:
		actor := currentUser(r)
		if actor == nil {
			fail(w, errors.New("需要管理员登录"), http.StatusUnauthorized)
			return
		}
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
		if !decode(w, r, &request) {
			return
		}
		start, end, err := auditReviewTimeRange(request.TimeRange.Mode, request.TimeRange.Preset, request.TimeRange.StartedAt, request.TimeRange.EndedAt, time.Now().UTC())
		if err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		item, err := s.auditReviews.Create(r.Context(), auditreview.CreateRequest{
			RequestID: request.RequestID, ProviderID: request.ProviderID, RequestedBy: actor.ID, Scope: request.Scope,
			EvidenceTypes: request.EvidenceTypes, WindowStart: start, WindowEnd: end,
		})
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			fail(w, err, status)
			return
		}
		auditReq(s, r, "create", "ai-audit-review", item.ID)
		s.publishRealtime("audit", "ai-reviews")
		write(w, http.StatusAccepted, map[string]any{"ai_audit_review": item})
	default:
		method(w)
	}
}

func (s *Server) auditAIReview(w http.ResponseWriter, r *http.Request) {
	parts := pathParts(r.URL.Path, "/api/v1/audit/ai-reviews/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		http.NotFound(w, r)
		return
	}
	reviewID := parts[0]
	item, err := s.store.GetAuditReview(r.Context(), reviewID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		fail(w, err, status)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		jobs, err := s.store.ListAuditReviewJobs(r.Context(), reviewID, false)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"ai_audit_review": item, "jobs": jobs})
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		if err := s.store.DeleteAuditReview(r.Context(), reviewID); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, store.ErrAuditReviewActive) {
				status = http.StatusConflict
			} else if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			fail(w, err, status)
			return
		}
		auditReq(s, r, "delete", "ai-audit-review", reviewID)
		s.publishRealtime("audit", "ai-reviews")
		write(w, http.StatusOK, map[string]any{"deleted": true, "review_id": reviewID})
		return
	}
	if len(parts) == 2 && parts[1] == "evidence" && r.Method == http.MethodGet {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		items, total, err := s.store.ListAuditReviewEvidence(r.Context(), reviewID, offset, intQuery(r, "limit", 50))
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"evidence": items, "total": total, "offset": offset})
		return
	}
	if len(parts) == 2 && parts[1] == "jobs" && r.Method == http.MethodGet {
		items, err := s.store.ListAuditReviewJobs(r.Context(), reviewID, false)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"jobs": items})
		return
	}
	if len(parts) == 3 && parts[1] == "jobs" && r.Method == http.MethodGet {
		item, err := s.store.GetAuditReviewJob(r.Context(), reviewID, parts[2])
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, sql.ErrNoRows) {
				status = http.StatusNotFound
			}
			fail(w, err, status)
			return
		}
		write(w, http.StatusOK, map[string]any{"job": item})
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		if err := s.store.CancelAuditReview(r.Context(), reviewID); err != nil {
			fail(w, err, http.StatusConflict)
			return
		}
		auditReq(s, r, "cancel", "ai-audit-review", reviewID)
		s.publishRealtime("audit", "ai-reviews")
		item, _ := s.store.GetAuditReview(r.Context(), reviewID)
		write(w, http.StatusOK, map[string]any{"ai_audit_review": item})
		return
	}
	method(w)
}

func auditReviewTimeRange(mode, preset string, startedAt, endedAt *time.Time, nowTime time.Time) (time.Time, time.Time, error) {
	switch strings.TrimSpace(mode) {
	case "preset", "":
		duration := 24 * time.Hour
		switch strings.TrimSpace(preset) {
		case "1h":
			duration = time.Hour
		case "", "24h":
			duration = 24 * time.Hour
		case "7d":
			duration = 7 * 24 * time.Hour
		case "30d":
			duration = 30 * 24 * time.Hour
		default:
			return time.Time{}, time.Time{}, errors.New("审查时间预设无效")
		}
		return nowTime.Add(-duration), nowTime, nil
	case "custom":
		if startedAt == nil || endedAt == nil {
			return time.Time{}, time.Time{}, errors.New("自定义审查需要起止时间")
		}
		return startedAt.UTC(), endedAt.UTC(), nil
	default:
		return time.Time{}, time.Time{}, errors.New("审查时间模式无效")
	}
}
