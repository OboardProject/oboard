package controller

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestOfflineAgentReceivesLatestConfigurationAfterReconnect(t *testing.T) {
	db, srv, server, httpServer := newTaskDispatchServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.configurationDelay = 20 * time.Millisecond
	go srv.StartConfigurationReconciler(ctx)

	inbound := &model.Inbound{ServerID: server.ID, Name: "offline-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	srv.markConfigurationChanged(ctx, "/api/v1/inbounds", http.MethodPost)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := db.ConfigurationSyncState(ctx, server.ID)
		if err == nil && state.State == "queued" && state.LastTaskID > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil || state.State != "queued" || state.LastTaskID == 0 {
		t.Fatalf("offline desired state was not queued: %#v err=%v", state, err)
	}

	// The Agent was absent while the task was prepared. A fresh authenticated
	// WebSocket connection must receive that durable pending task immediately.
	socket := connectTestAgent(t, srv, httpServer.URL, server)
	defer socket.close()
	task := socket.expectTaskRequest(2 * time.Second)
	if taskID(task) != state.LastTaskID || task["type"] != model.AgentTaskTypeApplyDeployment {
		t.Fatalf("reconnected task=%#v, queued state=%#v", task, state)
	}
}
