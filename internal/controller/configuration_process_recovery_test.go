package controller

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type configurationRecoveryEvidence struct {
	ServerID int64
	Revision uint64
	TaskID   int64
}

// This is a Controller component process test, not Agent/kernel convergence or
// power-loss durability: SIGKILL interrupts the post-commit scheduling handoff.
func TestConfigurationProcessRecoveryBeforeScheduling(t *testing.T) {
	if mode := os.Getenv("OBOARD_TEST_CONFIGURATION_RECOVERY"); mode != "" {
		configurationRecoveryProcess(t, mode)
		return
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, "controller.sqlite")
	run := func(mode string, crash bool) configurationRecoveryEvidence {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer writer.Close()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfigurationProcessRecoveryBeforeScheduling$", "-test.count=1")
		cmd.Env = append(os.Environ(), "OBOARD_TEST_CONFIGURATION_RECOVERY="+mode, "OBOARD_TEST_CONFIGURATION_DB="+dbPath)
		cmd.ExtraFiles = []*os.File{writer}
		// Child output is not surfaced: database/configuration errors may contain data.
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		}()
		_ = writer.Close()
		var evidence configurationRecoveryEvidence
		// CommandContext kills the child at the deadline, closing the pipe even if
		// the child never reaches its explicit post-commit barrier.
		if err := json.NewDecoder(reader).Decode(&evidence); err != nil {
			t.Fatalf("%s evidence unavailable: %v", mode, err)
		}
		if crash {
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil || ctx.Err() != nil {
				t.Fatal("writer did not exit at the intentional crash barrier")
			}
		} else if err := cmd.Wait(); err != nil {
			t.Fatalf("recovery process failed: %v", err)
		}
		return evidence
	}
	committed := run("commit", true)
	if committed.Revision == 0 || committed.ServerID == 0 || committed.TaskID != 0 {
		t.Fatalf("invalid commit evidence: %+v", committed)
	}
	recovered := run("recover", false)
	if recovered.ServerID != committed.ServerID || recovered.Revision != committed.Revision || recovered.TaskID == 0 {
		t.Fatalf("recovery lost intent: committed=%+v recovered=%+v", committed, recovered)
	}
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, err := db.ConfigurationSyncState(context.Background(), committed.ServerID)
	if err != nil || state.WantedRevision != committed.Revision || state.LastTaskID != recovered.TaskID || state.State != "queued" {
		t.Fatalf("durable scheduling evidence=%+v err=%v", state, err)
	}
	task, err := db.GetTask(context.Background(), recovered.TaskID)
	if err != nil || task.ServerID != committed.ServerID || task.Type != model.AgentTaskTypeApplyDeployment {
		t.Fatalf("delivery task missing or mismatched: err=%v", err)
	}
}

func configurationRecoveryProcess(t *testing.T, mode string) {
	t.Helper()
	pipe := os.NewFile(3, "recovery-evidence")
	if pipe == nil {
		t.Fatal("missing evidence pipe")
	}
	defer pipe.Close()
	db, err := store.Open(os.Getenv("OBOARD_TEST_CONFIGURATION_DB"))
	if err != nil {
		t.Fatal("open synthetic database failed")
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if mode == "commit" {
		server := &model.Server{Name: "process-recovery", AgentID: "synthetic-offline-agent", Status: model.ServerOnline, ListenIP: "127.0.0.1", PortRangeStart: 10000, PortRangeEnd: 20000}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal("create synthetic server failed")
		}
		inbound := &model.Inbound{ServerID: server.ID, Name: "process-entry", Protocol: model.ProtocolVLESS, ListenIP: "127.0.0.1", Port: 10443, ConfigJSON: "{}", Enabled: true}
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal("commit desired inbound failed")
		}
		revision, err := db.ConfigurationRevision(ctx)
		if err != nil || revision == 0 {
			t.Fatal("missing durable desired revision")
		}
		if _, err := db.ConfigurationSyncState(ctx, server.ID); err == nil {
			t.Fatal("scheduling occurred before barrier")
		}
		tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
		if err != nil || len(tasks) != 0 {
			t.Fatal("unexpected pre-crash delivery")
		}
		if err := json.NewEncoder(pipe).Encode(configurationRecoveryEvidence{ServerID: server.ID, Revision: revision}); err != nil {
			t.Fatal(err)
		}
		// Parent keeps stdin open until SIGKILL. No coordinator has been constructed.
		var barrier [1]byte
		_, _ = os.Stdin.Read(barrier[:])
		t.Fatal("crash barrier released without process termination")
	}
	if mode != "recover" {
		t.Fatal("invalid helper mode")
	}
	servers, err := db.ListServers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatal("persisted desired server missing")
	}
	revision, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal("read persisted revision failed")
	}
	srv := newTestServer(db, "synthetic-process-test-secret", "")
	srv.configurationDelay = time.Millisecond
	done := make(chan struct{})
	go func() { defer close(done); srv.StartConfigurationReconciler(ctx) }()
	defer func() { cancel(); <-done }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("restarted coordinator did not schedule persisted intent")
		case <-ticker.C:
			state, err := db.ConfigurationSyncState(ctx, servers[0].ID)
			if err == nil && state.State == "queued" && state.WantedRevision == revision && state.LastTaskID > 0 {
				if err := json.NewEncoder(pipe).Encode(configurationRecoveryEvidence{ServerID: servers[0].ID, Revision: revision, TaskID: state.LastTaskID}); err != nil {
					t.Fatal(err)
				}
				return
			}
		}
	}
}
