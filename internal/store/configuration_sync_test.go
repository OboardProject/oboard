package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConfigurationSyncStateLifecycleAndStaleResult(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "sync-node", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, 10, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" || state.WantedRevision != 10 {
		t.Fatalf("initial sync state = %#v, err=%v", state, err)
	}
	claimed, err := db.ClaimConfigurationSync(ctx, server.ID, 10)
	if err != nil || !claimed {
		t.Fatalf("claim = %v, err=%v", claimed, err)
	}
	if err := db.MarkConfigurationSyncQueued(ctx, server.ID, 10, 101, 7, "digest-101"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, 11, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 101, true, ""); err != nil {
		t.Fatal(err)
	}
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" || state.WantedRevision != 11 || state.RetryCount != 0 {
		t.Fatalf("stale result changed newer state = %#v, err=%v", state, err)
	}
	claimed, err = db.ClaimConfigurationSync(ctx, server.ID, 11)
	if err != nil || !claimed {
		t.Fatalf("second claim = %v, err=%v", claimed, err)
	}
	if err := db.MarkConfigurationSyncQueued(ctx, server.ID, 11, 102, 8, "digest-102"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 102, false, "generation failed"); err != nil {
		t.Fatal(err)
	}
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "failed" || state.RetryCount != 1 || state.NextRetryAt == nil || !state.NextRetryAt.After(time.Now().UTC()) {
		t.Fatalf("failed state = %#v, err=%v", state, err)
	}
}

func TestEnsureConfigurationSyncRevisionDoesNotReopenCurrentState(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "watermark-node", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if changed, err := db.EnsureConfigurationSyncRevision(ctx, server.ID, 10); err != nil || !changed {
		t.Fatalf("initial ensure = %v, err=%v", changed, err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 10); err != nil || !ok {
		t.Fatalf("claim = %v, err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncQueued(ctx, server.ID, 10, 101, 1, "digest"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 101, true, ""); err != nil {
		t.Fatal(err)
	}
	if changed, err := db.EnsureConfigurationSyncRevision(ctx, server.ID, 10); err != nil || changed {
		t.Fatalf("equal ensure reopened synced state: changed=%v err=%v", changed, err)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "synced" {
		t.Fatalf("equal ensure state = %#v err=%v", state, err)
	}
}

func TestConfigurationSyncRecoveryRequeuesMissingTask(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "recovery-node", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkConfigurationSyncPending(ctx, 20, []int64{server.ID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 20); err != nil || !ok {
		t.Fatalf("claim = %v, err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncQueued(ctx, server.ID, 20, 201, 9, "digest-201"); err != nil {
		t.Fatal(err)
	}
	if err := db.RecoverConfigurationSyncStates(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" {
		t.Fatalf("missing task was not requeued = %#v, err=%v", state, err)
	}
}
