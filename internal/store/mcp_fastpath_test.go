package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestMCPPreparedPlanLifecycleAndBindings(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	plan := &model.MCPPreparedPlan{
		ID: "prep_test", PrincipalID: "principal-a", GrantID: "grant-a", RecipeID: "server.manage", RecipeVersion: "1",
		Operations: []byte(`[{"capability":"servers.update","input":{"server_id":1}}]`), ExpectedRevisions: []byte(`{"server:1":"rev-1"}`),
		PlanHash: "hash", Summary: []byte(`{"action":"update_server"}`), Verification: []byte(`{"after_commit":["workflow_terminal"]}`), ExpiresAt: now.Add(time.Minute),
	}
	if err := db.CreateMCPPreparedPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetMCPPreparedPlan(ctx, plan.ID)
	if err != nil || stored.RecipeID != plan.RecipeID || stored.PlanHash != plan.PlanHash {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	if _, err := db.ClaimMCPPreparedPlan(ctx, plan.ID, "other", plan.GrantID, "commit-key", now); !errors.Is(err, ErrMCPPreparedPlanUnauthorized) {
		t.Fatalf("principal mismatch error=%v", err)
	}
	if _, err := db.ClaimMCPPreparedPlan(ctx, plan.ID, plan.PrincipalID, "other", "commit-key", now); !errors.Is(err, ErrMCPPreparedPlanUnauthorized) {
		t.Fatalf("grant mismatch error=%v", err)
	}
	claimed, err := db.ClaimMCPPreparedPlan(ctx, plan.ID, plan.PrincipalID, plan.GrantID, "commit-key", now)
	if err != nil || claimed.CommitKey != "commit-key" || claimed.ClaimedAt == nil {
		t.Fatalf("claimed=%#v err=%v", claimed, err)
	}
	if _, err := db.ClaimMCPPreparedPlan(ctx, plan.ID, plan.PrincipalID, plan.GrantID, "other-key", now); !errors.Is(err, ErrMCPPreparedPlanCommitKey) {
		t.Fatalf("commit key mismatch error=%v", err)
	}
	if err := db.ReleaseMCPPreparedPlanClaim(ctx, plan.ID, "commit-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimMCPPreparedPlan(ctx, plan.ID, plan.PrincipalID, plan.GrantID, "commit-key", now); err != nil {
		t.Fatal(err)
	}
	if err := db.ConsumeMCPPreparedPlan(ctx, plan.ID, "commit-key", "chg-1", "wf-1", now); err != nil {
		t.Fatal(err)
	}
	replayed, err := db.ClaimMCPPreparedPlan(ctx, plan.ID, plan.PrincipalID, plan.GrantID, "commit-key", now)
	if err != nil || replayed.ConsumedAt == nil || replayed.WorkflowID != "wf-1" {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if _, err := db.FindMCPPreparedPlanByWorkflow(ctx, "wf-1", plan.PrincipalID, plan.GrantID); err != nil {
		t.Fatal(err)
	}
}

func TestMCPPreparedPlanExpiryAndAtomicClaim(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	create := func(id string, expires time.Time) {
		t.Helper()
		if err := db.CreateMCPPreparedPlan(ctx, &model.MCPPreparedPlan{ID: id, PrincipalID: "p", GrantID: "g", RecipeID: "r", RecipeVersion: "1", Operations: []byte(`[]`), ExpectedRevisions: []byte(`{}`), PlanHash: "h", Summary: []byte(`{}`), Verification: []byte(`{}`), ExpiresAt: expires}); err != nil {
			t.Fatal(err)
		}
	}
	create("expired", now.Add(-time.Second))
	if _, err := db.ClaimMCPPreparedPlan(ctx, "expired", "p", "g", "same-key", now); !errors.Is(err, ErrMCPPreparedPlanExpired) {
		t.Fatalf("expired error=%v", err)
	}
	create("race", now.Add(time.Minute))
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, claimErr := db.ClaimMCPPreparedPlan(ctx, "race", "p", "g", "same-key", now)
			errs <- claimErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, claimed := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrMCPPreparedPlanClaimed):
			claimed++
		default:
			t.Fatalf("unexpected claim error: %v", err)
		}
	}
	if succeeded != 1 || claimed != 1 {
		t.Fatalf("claim results succeeded=%d claimed=%d", succeeded, claimed)
	}
}

func TestMCPContinuationAndMetricPersistence(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	continuation := &model.MCPTaskContinuation{ID: "cont-1", PrincipalID: "p", GrantID: "g", RecipeID: "server.onboard", RecipeVersion: "1", Goal: "add server", Params: []byte(`{"x":1}`), TargetRefs: []byte(`[]`), State: []byte(`{}`), ExpiresAt: now.Add(time.Minute)}
	if err := db.CreateMCPTaskContinuation(ctx, continuation); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetMCPTaskContinuation(ctx, continuation.ID, "other", "g", now); err == nil {
		t.Fatal("principal mismatch unexpectedly returned continuation")
	}
	if _, err := db.GetMCPTaskContinuation(ctx, continuation.ID, "p", "g", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetMCPTaskContinuation(ctx, continuation.ID, "p", "g", now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired continuation was returned")
	}
	metric := &model.MCPFastPathMetric{ID: "mfm-1", PrincipalID: "p", GrantID: "g", RecipeID: "server.onboard", RecipeVersion: "1", FastPathUsed: true, Phase: "prepare", Status: "ready", DurationMS: 4}
	if err := db.CreateMCPFastPathMetric(ctx, metric); err != nil {
		t.Fatal(err)
	}
	metrics, err := db.ListMCPFastPathMetrics(ctx, 10)
	if err != nil || len(metrics) != 1 || metrics[0].RecipeID != "server.onboard" || !metrics[0].FastPathUsed || metrics[0].DurationMS != 4 {
		t.Fatalf("metrics=%#v err=%v", metrics, err)
	}
}

func TestExternalActionWorkflowUniquenessMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`create table automation_external_actions (id text primary key, grant_id text not null default '', workflow_id text not null default '', kind text not null, payload_encrypted text not null, expires_at text not null, consumed_at text, created_at text not null)`); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`insert into automation_external_actions values('old-live','g','wf-duplicate','execute','old','2030-01-01T00:00:00Z',null,'2026-01-01T00:00:00Z')`,
		`insert into automation_external_actions values('new-consumed','g','wf-duplicate','execute','','2030-01-01T00:00:00Z','2026-02-01T00:00:00Z','2026-02-01T00:00:00Z')`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	action, err := db.FindExternalActionByWorkflow(ctx, "wf-duplicate")
	if err != nil || action.ID != "old-live" {
		t.Fatalf("retained action=%#v err=%v", action, err)
	}
	duplicate := &ExternalAction{ID: "another", GrantID: "g", WorkflowID: "wf-duplicate", Kind: "execute", Payload: "payload", ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.CreateExternalAction(ctx, duplicate); err == nil {
		t.Fatal("duplicate workflow external action was accepted")
	}
}
