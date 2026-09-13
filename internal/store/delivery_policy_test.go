package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestDeliveryPolicySaveAtomicAndRecoverable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "delivery.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	server := &model.Server{Name: "before", AgentID: "agent"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	before, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `create trigger reject_delivery before insert on configuration_sync_intents when new.source='delivery_policy' begin select raise(abort,'injected handoff failure'); end`); err != nil {
		t.Fatal(err)
	}
	off := false
	candidate := *server
	candidate.Name = "after"
	options := ServerUpdateOptions{RuntimeUsersEnabled: &off}
	if err := db.UpdateServerSettings(ctx, &candidate, options); err == nil {
		t.Fatal("save succeeded without handoff")
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "before" || !flags.RuntimeUsersEnabled || revision != before || candidate.UpdatedAt != server.UpdatedAt {
		t.Fatalf("partial save: server=%+v flags=%+v rev=%d", stored, flags, revision)
	}
	if _, err := db.db.ExecContext(ctx, `drop trigger reject_delivery`); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateServerSettings(ctx, &candidate, options); err != nil {
		t.Fatal(err)
	}
	flags, err = db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if flags.RuntimeUsersEnabled || !flags.AuthorizationFastLane || flags.Revision == 0 || flags.AppliedRevision != 0 {
		t.Fatalf("flags=%+v", flags)
	}
	if err := db.UpdateServerSettings(ctx, &candidate, options); err != nil {
		t.Fatal(err)
	}
	same, err := db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if same.Revision != flags.Revision {
		t.Fatal("identical save scheduled another policy revision")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	intents, err := db.DrainConfigurationSyncIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, intent := range intents {
		if intent.Source == "delivery_policy" {
			found = true
			if intent.Scope != "explicit_ids" || intent.ServerID != server.ID || intent.TargetCount != 1 {
				t.Fatalf("intent=%+v", intent)
			}
		}
	}
	if !found {
		t.Fatal("committed policy handoff lost on restart")
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.WantedRevision < flags.Revision {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestDeliveryPolicyAcknowledgementTargets(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "policy", AgentID: "agent"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	flags := defaultServerDeliveryFlags(server.ID)
	flags.RuntimeUsersEnabled = false
	if err := db.SetServerDeliveryFlags(ctx, flags); err != nil {
		t.Fatal(err)
	}
	flags, err = db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, ConfigVersion: 101, PayloadJSON: `{"force_refresh":true}`, Status: "pending", Nonce: "first"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// Attaching an old task never captures a newly saved policy.
	if err := db.MarkConfigurationSyncQueued(ctx, server.ID, flags.Revision, 101, task.ID, "digest"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 101, true, ""); err != nil {
		t.Fatal(err)
	}
	current, err := db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AppliedRevision != 0 {
		t.Fatal("adopted task confirmed new policy")
	}
	if _, err := db.db.ExecContext(ctx, `update configuration_sync_states set state='pending' where server_id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, flags.Revision); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncDeploymentQueued(ctx, server.ID, flags.Revision, 101, task.ID, "digest"); err != nil {
		t.Fatal(err)
	}
	// A newer edit commits while this deployment is in flight.
	next := flags
	next.RuntimeUsersEnabled = true
	if err := db.SetServerDeliveryFlags(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 101, true, ""); err != nil {
		t.Fatal(err)
	}
	current, err = db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AppliedRevision != flags.Revision || current.AppliedRevision >= current.Revision {
		t.Fatalf("late ACK confirmed new policy: %+v", current)
	}
	if _, err := db.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	nextTask := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, ConfigVersion: 102, PayloadJSON: `{"force_refresh":true}`, Status: "pending", Nonce: "second"}
	if err := db.CreateTask(ctx, nextTask); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, current.Revision); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncDeploymentQueued(ctx, server.ID, current.Revision, 102, nextTask.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 101, true, ""); err != nil {
		t.Fatal(err)
	}
	stale, err := db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stale.AppliedRevision != flags.Revision {
		t.Fatal("old configuration ACK confirmed newer queued task")
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 102, true, ""); err != nil {
		t.Fatal(err)
	}
	current, err = db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.AppliedRevision != current.Revision {
		t.Fatalf("current ACK not confirmed: %+v", current)
	}
}

func TestDeliveryPolicyMigrationFrom9a7fc86(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "previous", AgentID: "agent"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/delivery_flags_9a7fc86.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `drop table server_delivery_flags`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, string(fixture)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `insert into server_delivery_flags values(?,0,1)`, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `create trigger reject_policy_migration before insert on configuration_sync_intents when new.source='delivery_policy' begin select raise(abort,'migration interrupted'); end`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrateDeliveryPolicyRevisions(ctx); err == nil {
		t.Fatal("migration ignored handoff failure")
	}
	if has, err := db.tableHasColumn(ctx, "server_delivery_flags", "revision"); err != nil || has {
		t.Fatalf("failed migration left schema marker: has=%v err=%v", has, err)
	}
	if _, err := db.db.ExecContext(ctx, `drop trigger reject_policy_migration`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if flags.AuthorizationFastLane || !flags.RuntimeUsersEnabled || flags.Revision == 0 || flags.AppliedRevision != 0 {
		t.Fatalf("migration changed policy or lost handoff: %+v", flags)
	}
	var count int
	if err := db.db.QueryRowContext(ctx, `select count(*) from configuration_sync_intents where source='delivery_policy' and server_id=? and revision=?`, server.ID, flags.Revision).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	before, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	after, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration repeated on reopen")
	}
}

func TestDeliveryPolicyDoesNotCaptureChangesDuringPreparation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "racing", AgentID: "agent"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	flags := defaultServerDeliveryFlags(server.ID)
	flags.RuntimeUsersEnabled = false
	if err := db.SetServerDeliveryFlags(ctx, flags); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, state.WantedRevision); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	flags.RuntimeUsersEnabled = true
	if err := db.SetServerDeliveryFlags(ctx, flags); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, ConfigVersion: 201, PayloadJSON: `{"force_refresh":true}`, Status: "pending", Nonce: "racing"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncDeploymentQueued(ctx, server.ID, state.WantedRevision, 201, task.ID, "old"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 201, true, ""); err != nil {
		t.Fatal(err)
	}
	current, err := db.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ProcessingRevision != 0 || current.AppliedRevision != 0 {
		t.Fatalf("captured a policy newer than prepared state: %+v", current)
	}
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "pending" {
		t.Fatalf("unapplied policy settled: %+v err=%v", state, err)
	}
	if _, err := db.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.WantedRevision != current.Revision {
		t.Fatalf("newer intent lost: %+v err=%v", state, err)
	}
}
