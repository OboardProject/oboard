package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAIJobRetryLimitAndDailyBudget(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "ai.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	provider := &model.AIProvider{ID: "provider-1", Name: "test", BaseURL: "http://127.0.0.1", Model: "test", CredentialEncrypted: "encrypted", Enabled: true, DailyTokenLimit: 100}
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "audit-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	snapshot := &model.AuditFeatureSnapshot{ID: "snapshot-1", UserID: user.ID, Window: "15m", WindowStartedAt: now.Add(-15 * time.Minute), WindowEndedAt: now, FeatureVersion: 1, RuleScore: 80, Features: json.RawMessage(`{}`), Fingerprint: "snapshot"}
	if err := db.CreateAuditFeatureSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	incident := &model.AuditIncident{ID: "incident-1", UserID: user.ID, Status: "open", Severity: "high", RuleScore: 80, Fingerprint: "incident", LatestSnapshotID: snapshot.ID}
	if err := db.UpsertAuditIncident(ctx, incident); err != nil {
		t.Fatal(err)
	}
	retryJob := &model.AIAnalysisJob{ID: "retry-job", Kind: "audit_incident", ProviderID: provider.ID, Fingerprint: "retry", Input: json.RawMessage(`{}`)}
	if _, err := db.CreateAIAnalysisJobIfAbsent(ctx, retryJob, time.Hour); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		leased, _, err := db.LeaseAIAnalysisJob(ctx, "worker", time.Now().UTC(), time.Minute)
		if err != nil || leased.ID != retryJob.ID {
			t.Fatalf("lease attempt %d: job=%#v err=%v", attempt, leased, err)
		}
		if err := db.FailAIAnalysisJob(ctx, "worker", retryJob.ID, "temporary model failure"); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	if err := db.db.QueryRowContext(ctx, `select status from ai_analysis_jobs where id=?`, retryJob.ID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("retry job status=%q err=%v", status, err)
	}

	completed := &model.AIAnalysisJob{ID: "completed-job", Kind: "audit_incident", IncidentID: "incident-1", ProviderID: provider.ID, Fingerprint: "completed", Input: json.RawMessage(`{}`)}
	if _, err := db.CreateAIAnalysisJobIfAbsent(ctx, completed, time.Hour); err != nil {
		t.Fatal(err)
	}
	leased, _, err := db.LeaseAIAnalysisJob(ctx, "worker", time.Now().UTC(), time.Minute)
	if err != nil || leased.ID != completed.ID {
		t.Fatalf("completed job lease=%#v err=%v", leased, err)
	}
	leased.InputTokens, leased.OutputTokens, leased.Output = 80, 30, json.RawMessage(`{}`)
	finding := &model.AIFinding{ID: "finding-1", JobID: leased.ID, IncidentID: "incident-1", Classification: "possible_abuse", Confidence: .8, EvidenceRefs: []string{}, CounterEvidence: []string{}, RecommendedActions: []string{"request_manual_review"}, Summary: "test", ProviderID: provider.ID, Model: provider.Model, PromptVersion: "test"}
	if err := db.CompleteAIAnalysisJob(ctx, "worker", leased, finding); err != nil {
		t.Fatal(err)
	}
	budgetJob := &model.AIAnalysisJob{ID: "budget-job", Kind: "audit_incident", ProviderID: provider.ID, Fingerprint: "budget", Input: json.RawMessage(`{}`)}
	if _, err := db.CreateAIAnalysisJobIfAbsent(ctx, budgetJob, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LeaseAIAnalysisJob(ctx, "worker", time.Now().UTC(), time.Minute); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("daily token budget did not stop leasing: %v", err)
	}
}
