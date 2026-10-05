package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestConfigurationQueuePreservesTopologyAndAccessPhases(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "edge", AgentID: "agent-edge", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	var queued []model.AgentTask
	for i, kind := range []string{model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeApplyCoreConfig, model.AgentTaskTypeApplyCoreConfig, model.AgentTaskTypeApplyDeployment} {
		task, err := srv.queueAgentTask(ctx, server.ID, kind, map[string]any{"version": i + 1}, int64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		queued = append(queued, task)
	}
	for _, want := range queued {
		got, err := db.NextTask(ctx, server.ID)
		if err != nil || got.ID != want.ID {
			t.Fatalf("prerequisite was discarded or reordered: got=%+v err=%v want=%d", got, err, want.ID)
		}
		if err := db.CompleteTask(ctx, got.ID, "succeeded", `{}`); err != nil {
			t.Fatal(err)
		}
	}
}
