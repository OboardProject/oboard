package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

func TestAgentFleetCoordinatorRespectsConcurrency(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SetSettings(ctx, map[string]string{
		agentAutoUpdateSetting:           "true",
		agentUpdateMaxConcurrencySetting: "8",
		"controller_url":                 "https://controller.example",
	}); err != nil {
		t.Fatal(err)
	}
	oldBuild := version.AgentBuild
	version.AgentBuild = "20260828010101"
	t.Cleanup(func() { version.AgentBuild = oldBuild })
	for i := 0; i < 40; i++ {
		server := &model.Server{Name: fmt.Sprintf("node-%d", i), Status: model.ServerOnline, AgentID: fmt.Sprintf("agent-%d", i), AgentBuild: "20260101000000"}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	s := newTestServer(db, "test-secret", "")
	s.agentUpdates.Fill(ctx, false)
	active, err := db.CountActiveAgentUpdates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active != 8 {
		t.Fatalf("active update_agent tasks = %d, want 8", active)
	}
	tasks, err := db.ListTasksByServer(ctx, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("server 1 tasks = %#v", tasks)
	}
	payload, _ := json.Marshal(map[string]any{"message": "updated"})
	if err := db.CompleteTask(ctx, tasks[0].ID, "succeeded", string(payload)); err != nil {
		t.Fatal(err)
	}
	s.noteAgentUpdateOutcome(ctx, 1, "succeeded", "", version.AgentBuild)
	s.agentUpdates.Fill(ctx, false)
	active, err = db.CountActiveAgentUpdates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active != 8 {
		t.Fatalf("after refill active = %d, want 8", active)
	}
}

func TestAgentFleetCircuitBreakerPauses(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	if err := db.SaveAgentFleetState(ctx, store.AgentFleetState{TargetBuild: "t", Attempted: 10, Failed: 4}); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "node", Status: model.ServerOnline, AgentID: "agent-1"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	s.noteAgentUpdateOutcome(ctx, server.ID, "failed", "boom", "t")
	state, err := db.GetAgentFleetState(ctx)
	if err != nil || !state.Paused {
		t.Fatalf("circuit breaker state = %#v err=%v", state, err)
	}
}

func TestControllerUpdateRunRecoversOnNewBuild(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	run := &store.ControllerUpdateRun{Source: "manual", CurrentBuild: "old", TargetBuild: version.Build, Phase: store.ControllerUpdatePhaseRestarting}
	if err := db.CreateControllerUpdateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	s.recoverControllerUpdateRun(ctx)
	latest, err := db.LatestControllerUpdateRun(ctx)
	if err != nil || latest == nil || latest.Phase != store.ControllerUpdatePhaseSucceeded {
		t.Fatalf("recovered run = %#v err=%v", latest, err)
	}
	tasks, err := db.ListTasksByServer(ctx, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.Type == model.AgentTaskTypeApplyDeployment {
			t.Fatalf("Controller update recovery queued apply_deployment: %#v", task)
		}
	}
}

func TestEnqueueAgentUpdateDoesNotCreateOfflineTask(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	if err := db.SetSetting(ctx, "controller_url", "https://controller.example"); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "offline", Status: model.ServerOffline, AgentID: "agent-1"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.enqueueAgentUpdate(ctx, server, model.AgentUpdateRequest{})
	if err == nil {
		t.Fatal("expected offline enqueue to fail")
	}
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("offline update tasks = %#v err=%v", tasks, err)
	}
}

func TestListAgentUpdateCandidatesDoesNotTouchTelemetry(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "node", Status: model.ServerOnline, AgentID: "agent-1", AgentBuild: "20260101000000"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	before := db.SQLStatementCount()
	if _, err := db.ListAgentUpdateCandidates(ctx, "20260828000000", 8); err != nil {
		t.Fatal(err)
	}
	if db.SQLStatementCount()-before != 1 {
		t.Fatalf("candidate query statements = %d", db.SQLStatementCount()-before)
	}
}
