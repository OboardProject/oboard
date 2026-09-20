package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConfigurationSyncProblemsEffectiveRetryPolicy(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		retries int
		version int
		policy  string
	}{
		{"retryable", 4, 0, "automatic"},
		{"budget_exhausted", 5, 0, "manual"},
		{"previous_delivery", 0, 123, "manual"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &model.Server{Name: tc.name, Status: model.ServerOnline, PortRangeStart: 10000, PortRangeEnd: 20000}
			if err := db.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			if _, err := db.MarkConfigurationSyncPending(ctx, 10, []int64{server.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.ExecContext(ctx, `update configuration_sync_states set state='preparing',retry_count=?,last_config_version=? where server_id=?`, tc.retries, tc.version, server.ID); err != nil {
				t.Fatal(err)
			}
			problem := model.ConfigurationSyncProblem{Code: "preparation_failed", Category: "preparation", RetryPolicy: "automatic", Message: "safe"}
			if err := db.MarkConfigurationSyncPreparationFailure(ctx, server.ID, 10, "", problem); err != nil {
				t.Fatal(err)
			}
			state, err := db.ConfigurationSyncState(ctx, server.ID)
			if err != nil || len(state.Problems) != 1 || state.Problems[0].RetryPolicy != tc.policy || state.RetryCount != tc.retries+1 || state.LastError != "safe" {
				t.Fatalf("state: %#v %v", state, err)
			}
			ready, err := db.ListConfigurationSyncStates(ctx, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range ready {
				if row.ServerID == server.ID {
					found = true
				}
			}
			if found != (tc.policy == "automatic") {
				t.Fatalf("scheduler eligibility %v disagrees with policy %s", found, tc.policy)
			}
		})
	}
}

func TestConfigurationSyncProblemsMigrationAndLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	server := &model.Server{Name: "problems", Status: model.ServerOnline, PortRangeStart: 10000, PortRangeEnd: 20000}
	if err = db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err = db.MarkConfigurationSyncPending(ctx, 10, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	// Restore the actual previous table shape, preserving its row, then use Open's migration.
	if _, err = db.db.ExecContext(ctx, `alter table configuration_sync_states drop column problems_json`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || len(state.Problems) != 0 || state.WantedRevision != 10 {
		t.Fatalf("migration: %#v %v", state, err)
	}
	problems := []model.ConfigurationSyncProblem{{Code: "database_busy", Category: "waiting", RetryPolicy: "automatic", Message: "safe message", Resources: []model.ConfigurationSyncResource{{Type: "server", ID: "opaque-id"}}}, {Code: "future_code", Category: "waiting", RetryPolicy: "automatic", Message: "second", Resources: []model.ConfigurationSyncResource{}}}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 10); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err = db.MarkConfigurationSyncWaiting(ctx, server.ID, 10, time.Now().Add(time.Minute), "ignored", problems...); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || !reflect.DeepEqual(state.Problems, problems) || state.LastError != "safe message" || state.State != "pending" || state.RetryCount != 0 {
		t.Fatalf("reopen: %#v %v", state, err)
	}
	if _, err = db.MarkConfigurationSyncPending(ctx, 11, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 11); err != nil || !ok {
		t.Fatal("claim newer", err)
	}
	if err = db.MarkConfigurationSyncPreparationFailure(ctx, server.ID, 11, "", problems...); err != nil {
		t.Fatal(err)
	}
	if err = db.MarkConfigurationSyncPreparationFailure(ctx, server.ID, 10, "old"); err != nil {
		t.Fatal(err)
	}
	if err = db.MarkConfigurationSyncResult(ctx, server.ID, 100, true, ""); err != nil {
		t.Fatal(err)
	}
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || !reflect.DeepEqual(state.Problems, problems) || state.State != "failed" {
		t.Fatalf("late result: %#v %v", state, err)
	}
	if err = db.MarkConfigurationSyncNoop(ctx, server.ID, 11, "digest"); err != nil {
		t.Fatal(err)
	}
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || len(state.Problems) != 0 || state.LastError != "" || state.State != "synced" {
		t.Fatalf("clear: %#v %v", state, err)
	}
	if _, err = model.EncodeConfigurationSyncProblems(make([]model.ConfigurationSyncProblem, 17)); err == nil {
		t.Fatal("unbounded problems accepted")
	}
}
