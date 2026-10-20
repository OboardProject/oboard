package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRemoteExecTaskFailureDoesNotNotify(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v1/ui/notification-channels", login["token"].(string), map[string]any{
		"name": "phone", "type": "bark", "enabled": true, "events": notificationTaskFailed,
		"config_json": `{"device_key":"bark-device"}`,
	}, http.StatusCreated)
	ctx := context.Background()
	failed := `{"error":"shell command is required","code":"invalid_input"}`
	srv.notifyTaskFailure(ctx, model.AgentTask{ID: 1, ServerID: 1, Type: model.AgentTaskTypeRemoteExec, Status: "failed", ResultJSON: failed})
	srv.notifyTaskFailure(ctx, model.AgentTask{ID: 2, ServerID: 1, Type: model.AgentTaskTypeRemoteOperation, Status: "failed", ResultJSON: failed})
	counts, err := db.CountNotificationDeliveriesByChannelType(ctx, notificationTaskFailed)
	if err != nil || counts["bark"] != 0 {
		t.Fatalf("remote exec failure queued notifications: %+v %v", counts, err)
	}
	srv.notifyTaskFailure(ctx, model.AgentTask{ID: 3, ServerID: 1, Type: model.AgentTaskTypeApplyDeployment, Status: "failed", ResultJSON: `{"error":"boom"}`})
	counts, err = db.CountNotificationDeliveriesByChannelType(ctx, notificationTaskFailed)
	if err != nil || counts["bark"] != 1 {
		t.Fatalf("deployment failure must still notify: %+v %v", counts, err)
	}
}

func TestRemoteExecPayloadRejectsInvalidInputBeforeQueueing(t *testing.T) {
	for name, tc := range map[string]struct {
		mode  string
		argv  []string
		shell string
		cwd   string
	}{
		"empty shell":     {mode: model.RemoteExecModeShell, shell: "  "},
		"oversized shell": {mode: model.RemoteExecModeShell, shell: strings.Repeat("x", remoteExecMaxShell+1)},
		"null shell":      {mode: model.RemoteExecModeShell, shell: "id\x00"},
		"empty argv":      {mode: model.RemoteExecModeArgv},
		"empty argv item": {mode: model.RemoteExecModeArgv, argv: []string{"ls", ""}},
		"long argv item":  {mode: model.RemoteExecModeArgv, argv: []string{strings.Repeat("a", remoteExecMaxArgBytes+1)}},
		"relative cwd":    {mode: model.RemoteExecModeShell, shell: "id", cwd: "tmp"},
		"dirty cwd":       {mode: model.RemoteExecModeShell, shell: "id", cwd: "/tmp/../etc"},
	} {
		if _, err := newRemoteExecPayload(1, 1, model.PrivilegeRemoteShell, tc.mode, tc.argv, tc.shell, tc.cwd, 5); err == nil {
			t.Fatalf("%s: payload accepted", name)
		}
	}
	if _, err := newRemoteExecPayload(1, 1, model.PrivilegeRemoteShell, model.RemoteExecModeShell, nil, "uname -a", "", 5); err != nil {
		t.Fatalf("valid shell rejected: %v", err)
	}
}
