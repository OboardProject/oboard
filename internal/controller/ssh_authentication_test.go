package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func verifiedSSHTestResult(t *testing.T, plan model.SSHInboundPlan) string {
	t.Helper()
	count := 0
	for _, inbound := range plan.Inbounds {
		if inbound.Enabled {
			for _, user := range inbound.Users {
				if user.Enabled {
					count++
				}
			}
		}
	}
	data, err := json.Marshal(map[string]any{"steps": []any{map[string]any{
		"key": "ssh_inbounds", "status": "succeeded", "result": model.SSHAuthenticationVerification{
			Version: plan.Version, AuthenticationVerified: true, AuthenticatedUsers: count, AuthenticationPlanDigest: model.SSHAuthenticationPlanDigest(plan),
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSSHAuthenticationEvidenceMustMatchTask(t *testing.T) {
	plan := model.SSHInboundPlan{Version: 7, Inbounds: []model.SSHInbound{{Enabled: true, Users: []model.SSHInboundUser{{Enabled: true}}}}}
	valid := verifiedSSHTestResult(t, plan)
	for _, test := range []struct {
		name, report string
		want         bool
	}{
		{"verified", valid, true},
		{"old successful report", `{"steps":[{"key":"ssh_inbounds","status":"succeeded","result":{"host_public_key":"old-key"}}]}`, false},
		{"wrong version", strings.Replace(valid, `"version":7`, `"version":6`, 1), false},
		{"partial credentials", strings.Replace(valid, `"authenticated_users":1`, `"authenticated_users":0`, 1), false},
		{"failed handshake", strings.Replace(valid, `"authentication_verified":true`, `"authentication_verified":false`, 1), false},
		{"failed step", strings.Replace(valid, `"succeeded"`, `"failed"`, 1), false},
		{"negative count", strings.Replace(valid, `"authenticated_users":1`, `"authenticated_users":-1`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := sshTaskAuthenticationVerified(model.AgentTask{Type: model.AgentTaskTypeApplyDeployment, ResultJSON: test.report}, plan); got != test.want {
				t.Fatalf("verified=%v want=%v", got, test.want)
			}
		})
	}
	if sshTaskAuthenticationVerified(model.AgentTask{Type: model.AgentTaskTypeApplyCoreConfig, ResultJSON: `{"ssh_inbounds":{"host_public_key":"old-key"}}`}, plan) {
		t.Fatal("focused report without authentication evidence accepted")
	}
}

func TestUnconfirmedUsersIsExplicitInPublicJSON(t *testing.T) {
	for _, value := range []any{model.Server{}, application.ServerDTO{}} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"users_confirmed":false`) {
			t.Fatal("unconfirmed status omitted")
		}
	}
}

func TestSSHFailedDeploymentCannotBeRevivedByLateSuccess(t *testing.T) {
	for _, sameVersion := range []bool{false, true} {
		t.Run(map[bool]string{false: "newer version", true: "newer task at same version"}[sameVersion], func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			srv := newTestServer(db, "test-secret", "")
			server := &model.Server{Name: "ssh-node", PublicIPv4: "203.0.113.20"}
			if err := db.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			plan := model.SSHInboundPlan{Version: 1, Inbounds: []model.SSHInbound{{InboundID: 1, ServerID: server.ID, Enabled: true}}}
			payload, _ := json.Marshal(model.DeploymentTaskPayload{Version: 1, SSHInbounds: plan})
			old := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, ConfigVersion: 1, PayloadJSON: string(payload), Nonce: "old", Status: "succeeded", ResultJSON: verifiedSSHTestResult(t, plan)}
			if err := db.CreateTask(ctx, &old); err != nil {
				t.Fatal(err)
			}
			if err := db.ApplySSHDeploymentState(ctx, model.SSHServerHostKey{ServerID: server.ID, PublicKey: "test-host", ConfigVersion: 1}, nil, 0); err != nil {
				t.Fatal(err)
			}
			version := int64(2)
			if sameVersion {
				version = 1
			}
			newPlan := plan
			newPlan.Version = version
			focused, _ := json.Marshal(model.ApplyCoreConfigTaskPayload{SSHInbounds: &newPlan})
			current := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyCoreConfig, ConfigVersion: version, PayloadJSON: string(focused), Nonce: "new", Status: "pending"}
			if err := db.CreateTask(ctx, &current); err != nil {
				t.Fatal(err)
			}
			status, result, err := srv.validateSSHAuthenticationTaskResult(ctx, current, "succeeded", `{"ssh_inbounds":{"host_public_key":"test-host"}}`)
			if err != nil || status != "failed" {
				t.Fatalf("missing proof: status=%s err=%v", status, err)
			}
			if err := db.CompleteTask(ctx, current.ID, status, result); err != nil {
				t.Fatal(err)
			}
			if _, err := db.GetSSHServerHostKey(ctx, server.ID); err == nil {
				t.Fatal("failed verification retained SSH state")
			}
			if err := srv.applyDeploymentSSHState(ctx, server.ID, old, old.ResultJSON); err != nil {
				t.Fatal(err)
			}
			if err := db.ApplySSHDeploymentState(ctx, model.SSHServerHostKey{ServerID: server.ID, PublicKey: "test-host", ConfigVersion: 1}, nil, old.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.GetSSHServerHostKey(ctx, server.ID); err == nil {
				t.Fatal("late success revived old SSH state")
			}
		})
	}
}
