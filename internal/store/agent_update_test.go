package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestListAgentUpdateCandidatesIsLightweight(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	target := "20260828000000"
	for i := 0; i < 80; i++ {
		server := &model.Server{Name: fmt.Sprintf("node-%d", i), Status: model.ServerOnline, AgentID: fmt.Sprintf("agent-%d", i), AgentBuild: "20260101000000"}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	offline := &model.Server{Name: "offline", Status: model.ServerOffline, AgentID: "agent-offline", AgentBuild: "20260101000000"}
	if err := db.CreateServer(ctx, offline); err != nil {
		t.Fatal(err)
	}
	current := &model.Server{Name: "current", Status: model.ServerOnline, AgentID: "agent-current", AgentBuild: target}
	if err := db.CreateServer(ctx, current); err != nil {
		t.Fatal(err)
	}
	before := db.SQLStatementCount()
	items, err := db.ListAgentUpdateCandidates(ctx, target, 16)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 16 {
		t.Fatalf("candidates = %d, want 16", len(items))
	}
	used := db.SQLStatementCount() - before
	if used != 1 {
		t.Fatalf("ListAgentUpdateCandidates statements = %d, want 1", used)
	}
	counts, err := db.CountAgentUpdateFleet(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Current != 1 || counts.Offline != 1 || counts.Pending < 16 || counts.Outdated != 81 {
		t.Fatalf("fleet counts = %#v", counts)
	}
}

func TestEnqueueUniqueAgentTaskSuppressesDuplicates(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "node", Status: model.ServerOnline, AgentID: "agent-1"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	first := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeUpdateAgent, PayloadJSON: `{"expected_build":"a"}`, Status: "pending", ResultJSON: "{}", Nonce: "one"}
	got, created, err := db.EnqueueUniqueAgentTask(ctx, first, time.Time{})
	if err != nil || !created || got.ID == 0 {
		t.Fatalf("first enqueue = %#v created=%t err=%v", got, created, err)
	}
	second := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeUpdateAgent, PayloadJSON: `{"expected_build":"b"}`, Status: "pending", ResultJSON: "{}", Nonce: "two"}
	existing, created, err := db.EnqueueUniqueAgentTask(ctx, second, time.Time{})
	if err != nil || created || existing.ID != got.ID {
		t.Fatalf("duplicate enqueue = %#v created=%t err=%v", existing, created, err)
	}
}

func TestAgentFleetRollingColumnMigratesFromPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fleet-rolling.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.SaveAgentFleetState(ctx, AgentFleetState{TargetBuild: "old", Attempted: 3, Succeeded: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`alter table agent_fleet_update_state drop column rolling`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, err := db.GetAgentFleetState(ctx)
	if err != nil || state.Rolling || state.TargetBuild != "old" || state.Attempted != 3 || state.Succeeded != 1 {
		t.Fatalf("migrated fleet state = %#v err=%v", state, err)
	}
	state.Rolling = true
	if err := db.SaveAgentFleetState(ctx, state); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAgentFleetState(ctx)
	if err != nil || !got.Rolling || got.Attempted != 3 {
		t.Fatalf("saved rolling state = %#v err=%v", got, err)
	}
}

func TestAgentFleetStatePersistsRolling(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SaveAgentFleetState(ctx, AgentFleetState{Rolling: true, TargetBuild: "20260828010101", Attempted: 4}); err != nil {
		t.Fatal(err)
	}
	state, err := db.GetAgentFleetState(ctx)
	if err != nil || !state.Rolling || state.TargetBuild != "20260828010101" || state.Attempted != 4 {
		t.Fatalf("fleet state = %#v err=%v", state, err)
	}
}

func TestAgentUpdateIndexesExist(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"idx_servers_agent_update_scan", "idx_agent_tasks_update_active", "idx_agent_tasks_one_active_update", "idx_controller_update_runs_one_active"} {
		var count int
		if err := db.db.QueryRow(`select count(*) from sqlite_master where type='index' and name=?`, name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("missing index %s", name)
		}
	}
	var rolling int
	if err := db.db.QueryRow(`select count(*) from pragma_table_info('agent_fleet_update_state') where name='rolling'`).Scan(&rolling); err != nil {
		t.Fatal(err)
	}
	if rolling != 1 {
		t.Fatal("missing agent_fleet_update_state.rolling")
	}
}

func BenchmarkListAgentUpdateCandidates(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "oboard.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		server := &model.Server{Name: fmt.Sprintf("bench-%d", i), Status: model.ServerOnline, AgentID: fmt.Sprintf("agent-%d", i), AgentBuild: "20260101000000"}
		if err := db.CreateServer(ctx, server); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := db.ListAgentUpdateCandidates(ctx, "20260828000000", 16); err != nil {
			b.Fatal(err)
		}
	}
}
