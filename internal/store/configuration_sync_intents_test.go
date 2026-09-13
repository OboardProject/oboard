package store

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// Captured from Controller aaff75142d31, before intent-bearing triggers.
//
//go:embed testdata/configuration_revision_aaff751.sql
var previousConfigurationRevisionTriggers string

func TestConfigurationIntentDrainBudgetAndNewRevisionDuringProgress(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "budget.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var lastID int64
	for i := 0; i < configurationSyncTargetBatchSize+12; i++ {
		server := &model.Server{Name: fmt.Sprintf("budget-%d", i), AgentID: fmt.Sprintf("agent-%d", i), Status: model.ServerOffline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
		if err := s.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		lastID = server.ID
	}
	for pass := 0; pass < 20; pass++ {
		intents, err := s.DrainConfigurationSyncIntents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(intents) == 0 {
			break
		}
		if pass == 19 {
			t.Fatal("initial intent never drained")
		}
	}
	if _, err := s.db.ExecContext(ctx, `update servers set name='next-'||name`); err != nil {
		t.Fatal(err)
	}
	firstRevision, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.DrainConfigurationSyncIntents(ctx)
	if err != nil || len(first) != 1 || first[0].TargetCount != configurationSyncTargetBatchSize {
		t.Fatalf("unbounded drain=%v err=%v", first, err)
	}
	if _, err := s.db.ExecContext(ctx, `update servers set name='latest-'||name where id=1`); err != nil {
		t.Fatal(err)
	}
	latestRevision, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	second, err := s.DrainConfigurationSyncIntents(ctx)
	if err != nil || len(second) != 1 || second[0].TargetCount != 12 {
		t.Fatalf("progress restarted on new revision: %v %v", second, err)
	}
	state, err := s.ConfigurationSyncState(ctx, lastID)
	if err != nil || state.WantedRevision != firstRevision {
		t.Fatalf("last server starved: %v %v", state, err)
	}
	for pass := 0; pass < 5; pass++ {
		intents, err := s.DrainConfigurationSyncIntents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		targets := 0
		for _, intent := range intents {
			targets += intent.TargetCount
		}
		if targets > configurationSyncTargetBatchSize {
			t.Fatalf("target budget exceeded: %d", targets)
		}
		if len(intents) == 0 {
			break
		}
		if pass == 4 {
			t.Fatal("latest revision never drained")
		}
	}
	state, err = s.ConfigurationSyncState(ctx, lastID)
	if err != nil || state.WantedRevision != latestRevision {
		t.Fatalf("new intent lost: %v %v", state, err)
	}
	var id, parent, unused int
	var detail string
	if err := s.db.QueryRowContext(ctx, `explain query plan select source,scope,server_id,revision,processing_revision,last_server_id from configuration_sync_intents order by first_changed_at,source,server_id limit ?`, configurationSyncIntentBatchSize).Scan(&id, &parent, &unused, &detail); err != nil {
		t.Fatal(err)
	}
	t.Logf("intent claim plan: %s", detail)
}

func TestConfigurationIntentSurvivesProcessExitAfterCommit(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "crash.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "crash-start", AgentID: "crash-agent", Status: model.ServerOffline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestConfigurationIntentCrashChild$")
	child.Env = append(os.Environ(), "OBOARD_TEST_CONFIG_CRASH_DB="+path)
	output, err := child.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 || !strings.Contains(string(output), "committed") {
		t.Fatalf("crash child: %s %v", output, err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	saved, err := s.GetServer(ctx, server.ID)
	if err != nil || saved.Name != "child-saved" {
		t.Fatalf("business commit missing: %v %v", saved, err)
	}
	revision, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := s.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.WantedRevision != revision {
		t.Fatalf("committed effect missing: %v %v", state, err)
	}
}

func TestConfigurationIntentCrashChild(t *testing.T) {
	path := os.Getenv("OBOARD_TEST_CONFIG_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.db.Exec(`update servers set name='child-saved' where name='crash-start'`)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("commit rows=%d err=%v", changed, err)
	}
	fmt.Println("committed")
	// Deliberately bypass Close and all deferred cleanup after SQLite commits.
	os.Exit(23)
}

func TestConfigurationIntentTriggerMigrationFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "migration-failure.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	before, err := s.revisionTriggerSQL(ctx, "config_rev_servers_update")
	if err != nil {
		t.Fatal(err)
	}
	// A missing target makes trigger installation fail after the managed
	// trigger drop. The transaction must restore every preceding definition.
	if _, err := s.db.ExecContext(ctx, `drop table external_outbounds`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateManagedRevisionTriggers(ctx); err == nil {
		t.Fatal("expected trigger installation failure")
	}
	after, err := s.revisionTriggerSQL(ctx, "config_rev_servers_update")
	if err != nil || after != before {
		t.Fatalf("migration left partial triggers: %q %v", after, err)
	}
}

func TestConfigurationIntentCommitRollbackAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "intents.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "committed", AgentID: "agent", Status: model.ServerOffline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `update servers set name='rolled-back' where id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.DrainConfigurationSyncIntents(ctx); err != nil || len(got) != 0 {
		t.Fatalf("rolled-back intent=%v err=%v", got, err)
	}
	for _, name := range []string{"saved-1", "saved-2", "saved-3"} {
		if _, err := s.db.ExecContext(ctx, `update servers set name=? where id=?`, name, server.ID); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.ConfigurationRevision(ctx)
	if err != nil || after <= before {
		t.Fatalf("revision=%d err=%v", after, err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from configuration_sync_intents where source='servers.update'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("uncoalesced intents=%d err=%v", count, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	intents, err := s.DrainConfigurationSyncIntents(ctx)
	if err != nil || len(intents) == 0 {
		t.Fatalf("lost committed intent=%v err=%v", intents, err)
	}
	state, err := s.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.WantedRevision != after || state.State != "pending" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if intents, err := s.DrainConfigurationSyncIntents(ctx); err != nil || len(intents) != 0 {
		t.Fatalf("replayed drain=%v err=%v", intents, err)
	}
}

func TestConfigurationIntentFailedDrainRetainsWork(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "atomic.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "atomic", AgentID: "atomic-agent", Status: model.ServerOffline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `create trigger reject_sync_state before insert on configuration_sync_states begin select raise(abort,'injected write failure'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainConfigurationSyncIntents(ctx); err == nil {
		t.Fatal("injected failure ignored")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from configuration_sync_intents where source='servers.insert' and server_id=?`, server.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed handoff lost intent: %d %v", count, err)
	}
	if _, err := s.db.ExecContext(ctx, `drop trigger reject_sync_state`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfigurationSyncState(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationIntentMigrationFromPreviousTriggers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "previous", AgentID: "old-agent", Status: model.ServerOffline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, previousConfigurationRevisionTriggers); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `drop table configuration_sync_intents`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `update servers set name='saved-without-handoff' where id=?`, server.ID); err != nil {
		t.Fatal(err)
	}
	revision, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		intents, err := s.DrainConfigurationSyncIntents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if pass == 0 && (len(intents) != 1 || intents[0].Source != "upgrade_handoff") {
			t.Fatalf("migration intent=%v", intents)
		}
		if pass == 1 && len(intents) != 0 {
			t.Fatalf("migration repeated: %v", intents)
		}
		state, err := s.ConfigurationSyncState(ctx, server.ID)
		if err != nil || state.WantedRevision != revision {
			t.Fatalf("recovered state=%v err=%v", state, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigurationIntentWaitsForEnrollment(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "enrollment.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "waiting", Status: model.ServerOffline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRowContext(ctx, "select count(*) from configuration_sync_states where server_id=?", server.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unenrolled server entered deployment state: %d %v", count, err)
	}
	before, err := s.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "update servers set agent_id='joined-agent' where id=?", server.ID); err != nil {
		t.Fatal(err)
	}
	revision, err := s.ConfigurationRevision(ctx)
	if err != nil || revision <= before {
		t.Fatalf("enrollment did not advance revision: %d %d %v", before, revision, err)
	}
	if _, err := s.DrainConfigurationSyncIntents(ctx); err != nil {
		t.Fatal(err)
	}
	state, err := s.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.WantedRevision != revision || state.State != "pending" {
		t.Fatalf("enrollment lost handoff: %#v %v", state, err)
	}
}
