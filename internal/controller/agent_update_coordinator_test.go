package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/security"

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

func TestOperatorFleetRollContinuesWithoutAutoUpdate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SetSettings(ctx, map[string]string{
		agentAutoUpdateSetting: "false",
		"controller_url":       "https://controller.example",
	}); err != nil {
		t.Fatal(err)
	}
	oldBuild := version.AgentBuild
	version.AgentBuild = "20260828010101"
	t.Cleanup(func() { version.AgentBuild = oldBuild })
	for i := 0; i < 20; i++ {
		server := &model.Server{Name: fmt.Sprintf("node-%d", i), Status: model.ServerOnline, AgentID: fmt.Sprintf("agent-%d", i), AgentBuild: "20260101000000"}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	s := newTestServer(db, "test-secret", "")
	if got := s.agentUpdates.Fill(ctx, false); got.Created != 0 {
		t.Fatalf("auto-update off Fill(false) created = %d, want 0", got.Created)
	}
	first := s.agentUpdates.Fill(ctx, true)
	if first.Created != agentUpdateAutoConcurrencyMin || !first.Rolling {
		t.Fatalf("operator fill = created %d rolling %t, want created %d rolling true", first.Created, first.Rolling, agentUpdateAutoConcurrencyMin)
	}
	active, err := db.CountActiveAgentUpdates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active != agentUpdateAutoConcurrencyMin {
		t.Fatalf("active after operator fill = %d, want %d", active, agentUpdateAutoConcurrencyMin)
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
	node, err := db.GetServer(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	node.AgentBuild = version.AgentBuild
	if err := db.UpdateServerRuntimeState(ctx, node); err != nil {
		t.Fatal(err)
	}
	s.noteAgentUpdateOutcome(ctx, 1, "succeeded", "", version.AgentBuild)
	refill := s.agentUpdates.Fill(ctx, false)
	if refill.Created != 1 || !refill.Rolling {
		t.Fatalf("refill without auto-update = created %d rolling %t, want created 1 rolling true", refill.Created, refill.Rolling)
	}
	active, err = db.CountActiveAgentUpdates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active != agentUpdateAutoConcurrencyMin {
		t.Fatalf("active after refill = %d, want %d", active, agentUpdateAutoConcurrencyMin)
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

// An Agent update is only complete when the Agent is back on the new build.
// Completing the task on the first "succeeded" report declared success while
// the old process was still serving, and it emptied the active-task row the
// reconnect confirmation reads.
func TestAgentUpdateStaysOpenUntilAgentReconnectsOnNewBuild(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	server := &model.Server{Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task, err := srv.queueAgentTask(ctx, server.ID, model.AgentTaskTypeUpdateAgent, model.UpdateAgentTaskPayload{ExpectedBuild: "20260904120000"}, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.NextTask(ctx, server.ID); err != nil {
		t.Fatal(err)
	}

	held, err := srv.holdAgentUpdateForRestart(ctx, task, "succeeded", `{"message":"agent update completed","installed":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !held {
		t.Fatal("a successful install must hold the task open for the restart")
	}
	stored, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" {
		t.Fatalf("task status = %q, want running", stored.Status)
	}
	if !agentUpdateAwaitingRestart(stored.ResultJSON) {
		t.Fatalf("task result does not record the restart phase: %s", stored.ResultJSON)
	}
	// The reconnect confirmation must still be able to find the task.
	active, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeUpdateAgent)
	if err != nil || active == nil || active.ID != task.ID {
		t.Fatalf("held update task is not the active task: %#v err=%v", active, err)
	}

	srv.completeAgentUpdateAfterReconnect(ctx, server.ID, "20260904120000")
	stored, err = db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "succeeded" {
		t.Fatalf("task status after reconnect = %q, want succeeded", stored.Status)
	}
}

// A failed install is terminal immediately: there is nothing to wait for.
func TestAgentUpdateFailureIsNotHeld(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	task := model.AgentTask{ID: 1, Type: model.AgentTaskTypeUpdateAgent, PayloadJSON: `{"expected_build":"20260904120000"}`}
	held, err := srv.holdAgentUpdateForRestart(context.Background(), task, "failed", `{"message":"agent update failed"}`)
	if err != nil {
		t.Fatal(err)
	}
	if held {
		t.Fatal("a failed update must complete immediately")
	}
}

// An Agent that installed the release but never came back on the new build must
// fail with that reason rather than sit in the intermediate phase forever.
func TestStuckAgentUpdateRestartTimesOut(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	server := &model.Server{Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task, err := srv.queueAgentTask(ctx, server.ID, model.AgentTaskTypeUpdateAgent, model.UpdateAgentTaskPayload{ExpectedBuild: "20260904120000"}, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.NextTask(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.holdAgentUpdateForRestart(ctx, task, "succeeded", `{"installed":true}`); err != nil {
		t.Fatal(err)
	}
	srv.expireStuckAgentUpdateRestartsBefore(ctx, time.Now().Add(time.Minute))
	stored, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "failed" || !strings.Contains(stored.ResultJSON, agentUpdatePhaseAwaitingRestart) {
		t.Fatalf("stuck update was not failed with its own reason: %#v", stored)
	}
}
