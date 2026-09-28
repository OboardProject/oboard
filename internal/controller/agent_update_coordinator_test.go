package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/controllerupdate"
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

func TestAgentUpdateRetryBudgetShrinksAcrossBuilds(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "failing-node", Status: model.ServerOnline, AgentID: "agent-1", AgentBuild: "old"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(db, "test-secret", "")
	builds := []string{"build-1", "build-2", "build-3", "build-4", "build-5", "build-6"}
	limits := []int{5, 3, 2, 1, 1, 1}
	previousBuild := version.AgentBuild
	version.AgentBuild = "build-7"
	t.Cleanup(func() { version.AgentBuild = previousBuild })
	for round, build := range builds {
		retry, err := db.GetAgentUpdateRetry(ctx, server.ID, build)
		if err != nil || agentUpdateAttemptLimit(retry.FailureRound) != limits[round] || retry.Attempts != 0 {
			t.Fatalf("round %d before update = %#v err=%v", round, retry, err)
		}
		for attempt := 1; attempt <= limits[round]; attempt++ {
			s.noteAgentUpdateOutcome(ctx, server.ID, "failed", "download interrupted", build)
			retry, err = db.GetAgentUpdateRetry(ctx, server.ID, build)
			if err != nil || retry.Attempts != attempt || retry.LastError != "download interrupted" {
				t.Fatalf("round %d attempt %d = %#v err=%v", round, attempt, retry, err)
			}
			if (retry.NextRetryAt == nil) != (attempt == limits[round]) {
				t.Fatalf("round %d attempt %d next retry = %v", round, attempt, retry.NextRetryAt)
			}
		}
		if err := db.ReleaseAgentUpdateRetryDelays(ctx, build); err != nil {
			t.Fatal(err)
		}
		candidates, err := db.ListAgentUpdateCandidates(ctx, build, 1)
		if err != nil || len(candidates) != 0 {
			t.Fatalf("exhausted build %s candidates = %#v err=%v", build, candidates, err)
		}
	}
	retry, err := db.GetAgentUpdateRetry(ctx, server.ID, "build-7")
	if err != nil || retry.FailureRound != 6 || agentUpdateAttemptLimit(retry.FailureRound) != 0 {
		t.Fatalf("seventh build should be stopped: %#v err=%v", retry, err)
	}
	candidates, err := db.ListAgentUpdateCandidates(ctx, "build-7", 1)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("stopped build candidates = %#v err=%v", candidates, err)
	}
	status, err := s.agentFleetStatus(ctx)
	if err != nil || status["failure_count"] != 1 || status["exhausted_count"] != 1 || status["auto_stopped_count"] != 1 {
		t.Fatalf("failed update status = %#v err=%v", status, err)
	}
	failures, ok := status["failed_servers"].([]store.AgentUpdateFailure)
	if !ok || len(failures) != 1 || failures[0].MaxAttempts != 0 || !failures[0].AutoStopped || failures[0].LastError != "download interrupted" {
		t.Fatalf("failed server status = %#v", status["failed_servers"])
	}
	task := model.AgentTask{ServerID: server.ID, PayloadJSON: `{"expected_build":"build-7","auto_update":false}`}
	s.recordAgentUpdateTaskOutcome(ctx, task, "failed", "manual download interrupted")
	retry, err = db.GetAgentUpdateRetry(ctx, server.ID, "build-7")
	if err != nil || retry.FailureRound != 6 || retry.Attempts != 0 || retry.LastError != "manual download interrupted" {
		t.Fatalf("manual failure changed auto budget: %#v err=%v", retry, err)
	}
	s.recordAgentUpdateTaskOutcome(ctx, task, "succeeded", "")
	retry, err = db.GetAgentUpdateRetry(ctx, server.ID, "build-7")
	if err != nil || retry.FailureRound != 6 {
		t.Fatalf("task result reset history without a new build report: %#v err=%v", retry, err)
	}
}

func TestManualFleetUpdateAttemptsEachNodeOnceWithoutUsingAutoBudget(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SetSettings(ctx, map[string]string{"controller_url": "https://controller.example"}); err != nil {
		t.Fatal(err)
	}
	oldBuild := version.AgentBuild
	version.AgentBuild = "20260929000000"
	t.Cleanup(func() { version.AgentBuild = oldBuild })
	server := &model.Server{Name: "manual-node", Status: model.ServerOnline, AgentID: "agent-manual", AgentBuild: "20260901000000"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(db, "test-secret", "")
	if got := s.agentUpdates.Fill(ctx, true); got.Created != 1 {
		t.Fatalf("manual roll created %d tasks", got.Created)
	}
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("manual tasks = %#v err=%v", tasks, err)
	}
	var payload model.UpdateAgentTaskPayload
	if err := json.Unmarshal([]byte(tasks[0].PayloadJSON), &payload); err != nil || payload.AutoUpdate {
		t.Fatalf("manual payload = %#v err=%v", payload, err)
	}
	if err := db.CompleteTask(ctx, tasks[0].ID, "failed", `{"error":"failed once"}`); err != nil {
		t.Fatal(err)
	}
	s.recordAgentUpdateTaskOutcome(ctx, tasks[0], "failed", "failed once")
	if got := s.agentUpdates.Fill(ctx, false); got.Created != 0 {
		t.Fatalf("manual roll retried failed node: %#v", got)
	}
	retry, err := db.GetAgentUpdateRetry(ctx, server.ID, version.AgentBuild)
	if err != nil || retry.Attempts != 0 || retry.FailureRound != 0 {
		t.Fatalf("manual roll consumed auto budget: %#v err=%v", retry, err)
	}
	created, err := s.agentUpdates.ManualRetryFailed(ctx)
	if err != nil || created != 1 {
		t.Fatalf("manual failed retry created %d tasks: %v", created, err)
	}
	tasks, err = db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("retry tasks = %#v err=%v", tasks, err)
	}
	if err := db.CompleteTask(ctx, tasks[0].ID, "failed", `{"error":"failed again"}`); err != nil {
		t.Fatal(err)
	}
	s.recordAgentUpdateTaskOutcome(ctx, tasks[0], "failed", "failed again")
	retry, err = db.GetAgentUpdateRetry(ctx, server.ID, version.AgentBuild)
	if err != nil || retry.Attempts != 0 || retry.FailureRound != 0 {
		t.Fatalf("manual failed retry consumed auto budget: %#v err=%v", retry, err)
	}
	if got := s.agentUpdates.Fill(ctx, true); got.Created != 1 {
		t.Fatalf("new manual click created %d tasks", got.Created)
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
	if s.reconcileControllerUpdateRun(ctx, run, controllerupdate.Status{State: "installing", Current: controllerupdate.BuildInfo{Build: version.Build}}) {
		t.Fatal("running target build was accepted before updater health confirmation")
	}
	pending, err := db.LatestControllerUpdateRun(ctx)
	if err != nil || pending.Phase != store.ControllerUpdatePhaseRestarting {
		t.Fatalf("unconfirmed run = %#v err=%v", pending, err)
	}
	s.reconcileControllerUpdateRun(ctx, run, controllerupdate.Status{State: "installed", Current: controllerupdate.BuildInfo{Build: version.Build}})
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

// A restart-induced disconnect must not wipe installed_waiting_restart back to
// pending. That slot is what completeAgentUpdateAfterReconnect looks up.
func TestRequeueDoesNotDropAgentUpdateAwaitingRestart(t *testing.T) {
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
	if err := db.RequeueTaskIfRunning(ctx, task.ID, `{"message":"agent connection closed before task result was acknowledged; task requeued"}`); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "running" || !agentUpdateAwaitingRestart(stored.ResultJSON) {
		t.Fatalf("awaiting-restart update was requeued: %#v", stored)
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

func TestRequeueStillReturnsInFlightUpdateBeforeInstallReport(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeUpdateAgent, PayloadJSON: `{"expected_build":"20260904120000"}`, Status: "running", ResultJSON: `{}`, Nonce: "n"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.RequeueTaskIfRunning(ctx, task.ID, `{"message":"agent connection closed before task result was acknowledged; task requeued"}`); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" {
		t.Fatalf("in-flight update before the install report status = %q, want pending", stored.Status)
	}
}

func TestControllerUpdateRunRecordsRollbackEvenOnTargetBuild(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := newTestServer(db, "test-secret", "")
	run := &store.ControllerUpdateRun{Source: "manual", CurrentBuild: "old", TargetBuild: version.Build, Phase: store.ControllerUpdatePhaseRestarting}
	if err := db.CreateControllerUpdateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	s.reconcileControllerUpdateRun(t.Context(), run, controllerupdate.Status{State: "failed", LastError: "health timeout; rolled back", Current: controllerupdate.BuildInfo{Build: version.Build}})
	latest, err := db.LatestControllerUpdateRun(t.Context())
	if err != nil || latest.Phase != store.ControllerUpdatePhaseFailed || latest.Error != "health timeout; rolled back" {
		t.Fatalf("rollback run = %#v err=%v", latest, err)
	}
}
