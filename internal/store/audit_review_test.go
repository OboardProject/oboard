package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAuditReviewRetryCancellationAndDailyBudget(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit-review.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	actor := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "http://127.0.0.1", Model: "model", CredentialEncrypted: "encrypted", Enabled: true, DailyTokenLimit: 10}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAIProviderEndpoint(ctx, &model.AIProviderEndpoint{ID: "endpoint", ProviderID: provider.ID, Name: "Primary", BaseURL: "https://api.example.com/v1", APIStyle: "openai_responses", AuthMode: "none", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	retryReview, retryJob := auditReviewFixture(actor.ID, provider.ID, "retry", "retry-job")
	if err := db.CreateAuditReview(ctx, retryReview, nil, []model.AuditReviewJob{retryJob}); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		leased, _, err := db.LeaseAuditReviewJob(ctx, "retry-worker", time.Now().UTC(), time.Minute)
		if err != nil || leased.ID != retryJob.ID {
			t.Fatalf("retry lease %d job=%#v err=%v", attempt, leased, err)
		}
		if err := db.FailAuditReviewJob(ctx, "retry-worker", retryJob.ID, "temporary failure", nil); err != nil {
			t.Fatal(err)
		}
	}
	storedRetry, err := db.GetAuditReview(ctx, retryReview.ID)
	if err != nil || storedRetry.Status != "failed" {
		t.Fatalf("retry review=%#v err=%v", storedRetry, err)
	}

	cancelReview, cancelJob := auditReviewFixture(actor.ID, provider.ID, "cancel", "cancel-job")
	if err := db.CreateAuditReview(ctx, cancelReview, nil, []model.AuditReviewJob{cancelJob}); err != nil {
		t.Fatal(err)
	}
	if err := db.CancelAuditReview(ctx, cancelReview.ID); err != nil {
		t.Fatal(err)
	}
	jobs, err := db.ListAuditReviewJobs(ctx, cancelReview.ID, false)
	if err != nil || len(jobs) != 1 || jobs[0].Status != "cancelled" {
		t.Fatalf("cancelled jobs=%#v err=%v", jobs, err)
	}

	budgetReview, budgetJob := auditReviewFixture(actor.ID, provider.ID, "budget", "budget-job")
	if err := db.CreateAuditReview(ctx, budgetReview, nil, []model.AuditReviewJob{budgetJob}); err != nil {
		t.Fatal(err)
	}
	leased, _, err := db.LeaseAuditReviewJob(ctx, "budget-worker", time.Now().UTC(), time.Minute)
	if err != nil || leased.ID != budgetJob.ID {
		t.Fatalf("budget job lease=%#v err=%v", leased, err)
	}
	route := json.RawMessage(`{"provider_id":"provider","endpoint_id":"endpoint","api_style":"openai_responses","model":"model"}`)
	if _, err := db.CompleteAuditReviewJob(ctx, "budget-worker", budgetJob.ID, json.RawMessage(`{"verdict":"normal"}`), 8, 3, route); err != nil {
		t.Fatal(err)
	}
	jobs, err = db.ListAuditReviewJobs(ctx, budgetReview.ID, false)
	if err != nil || len(jobs) != 1 || !json.Valid(jobs[0].Route) || !strings.Contains(string(jobs[0].Route), `"endpoint_id":"endpoint"`) {
		t.Fatalf("route evidence was not returned with job: jobs=%#v err=%v", jobs, err)
	}
	blockedReview, blockedJob := auditReviewFixture(actor.ID, provider.ID, "blocked", "blocked-job")
	if err := db.CreateAuditReview(ctx, blockedReview, nil, []model.AuditReviewJob{blockedJob}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LeaseAuditReviewJob(ctx, "budget-worker", time.Now().UTC(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("daily budget did not stop leasing: %v", err)
	}
}

func TestAuditReviewJobPersistsErrorDetail(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit-review-detail.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	actor := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "33333333-3333-4333-8333-333333333333", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "http://127.0.0.1", Model: "model", CredentialEncrypted: "encrypted", Enabled: true}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAIProviderEndpoint(ctx, &model.AIProviderEndpoint{ID: "endpoint", ProviderID: provider.ID, Name: "Primary", BaseURL: "https://api.example.com/v1", APIStyle: "openai_responses", AuthMode: "none", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	review, job := auditReviewFixture(actor.ID, provider.ID, "detail", "detail-job")
	if err := db.CreateAuditReview(ctx, review, nil, []model.AuditReviewJob{job}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LeaseAuditReviewJob(ctx, "detail-worker", time.Now().UTC(), time.Minute); err != nil {
		t.Fatal(err)
	}
	detail := json.RawMessage(`{"provider":"p","endpoint":"https://api.example.com/v1/chat/completions","status":400,"response_body":"{\"error\":{\"message\":\"unsupported\"}}"}`)
	if err := db.FailAuditReviewJob(ctx, "detail-worker", job.ID, "model endpoint returned HTTP 400: unsupported", detail); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetAuditReviewJob(ctx, review.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.ErrorDetail) != string(detail) || stored.Error != "model endpoint returned HTTP 400: unsupported" {
		t.Fatalf("stored job error=%q detail=%s", stored.Error, stored.ErrorDetail)
	}
}

func TestAuditReviewEvidenceRefsAreScopedToReview(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit-review-evidence.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	actor := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "http://127.0.0.1", Model: "model", CredentialEncrypted: "encrypted", Enabled: true}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	userID := actor.ID
	for _, suffix := range []string{"first", "second"} {
		review, job := auditReviewFixture(actor.ID, provider.ID, suffix, suffix+"-job")
		evidence := []model.AuditReviewEvidence{{Ref: "user:stable-ref", ReviewID: review.ID, Kind: "user", UserID: &userID, Payload: json.RawMessage(`{"subject_ref":"user:stable-ref"}`)}}
		if err := db.CreateAuditReview(ctx, review, evidence, []model.AuditReviewJob{job}); err != nil {
			t.Fatalf("create %s review: %v", suffix, err)
		}
		stored, err := db.GetAuditReview(ctx, review.ID)
		if err != nil || stored.FinalOutput != nil {
			t.Fatalf("queued review final output=%s err=%v", stored.FinalOutput, err)
		}
	}
}

func auditReviewFixture(actorID int64, providerID, suffix, jobID string) (*model.AuditReview, model.AuditReviewJob) {
	nowTime := time.Now().UTC()
	review := &model.AuditReview{
		ID: "review-" + suffix, RequestID: "request-" + suffix, ProviderID: providerID, RequestedBy: actorID,
		Scope:         model.AuditReviewScope{Users: model.AuditReviewSelector{Mode: "all"}, Servers: model.AuditReviewSelector{Mode: "all"}},
		EvidenceTypes: []string{"connection"}, WindowStartedAt: nowTime.Add(-time.Hour), WindowEndedAt: nowTime, SnapshotAt: nowTime, PrivacyMode: "masked",
	}
	job := model.AuditReviewJob{ID: jobID, ReviewID: review.ID, ProviderID: providerID, Kind: "evidence", Input: json.RawMessage(`{"evidence":[]}`)}
	return review, job
}
