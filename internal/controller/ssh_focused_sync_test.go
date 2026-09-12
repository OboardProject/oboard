package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// verifiedFocusedSSHTestResult is the apply_core_config shape of the same
// authentication evidence verifiedSSHTestResult reports for a full deployment.
func verifiedFocusedSSHTestResult(t *testing.T, plan model.SSHInboundPlan) string {
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
	data, err := json.Marshal(map[string]any{"ssh_inbounds": model.SSHAuthenticationVerification{
		Version: plan.Version, AuthenticationVerified: true, AuthenticatedUsers: count, AuthenticationPlanDigest: model.SSHAuthenticationPlanDigest(plan),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestLastAppliedSSHPlanDecodesOnePayloadPerDeployment pins the cost boundary of
// the subscription SSH readiness check: an account holding SSH credentials used
// to pay a full deployment-payload decode on every subscription pull, because
// an apply_deployment payload embeds the whole generated kernel configuration.
// Repeated lookups must reuse the memoized projection and a newer succeeded
// deployment must replace it.
func TestLastAppliedSSHPlanDecodesOnePayloadPerDeployment(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "ssh-node", PublicIPv4: "203.0.113.31"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	createDeployment := func(version int64, nonce string) model.SSHInboundPlan {
		plan := model.SSHInboundPlan{Version: version, Inbounds: []model.SSHInbound{{InboundID: 1, ServerID: server.ID, Enabled: true}}}
		payload, marshalErr := json.Marshal(model.DeploymentTaskPayload{Version: version, SSHInbounds: plan})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		task := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, ConfigVersion: version, PayloadJSON: string(payload), Nonce: nonce, Status: "succeeded", ResultJSON: verifiedSSHTestResult(t, plan)}
		if err := db.CreateTask(ctx, &task); err != nil {
			t.Fatal(err)
		}
		return plan
	}
	assertPlan := func(want model.SSHInboundPlan, wantDecodes uint64) {
		t.Helper()
		plan, version, planErr := srv.lastAppliedSSHPlan(ctx, server.ID)
		if planErr != nil {
			t.Fatal(planErr)
		}
		if version != want.Version {
			t.Fatalf("version = %d want %d", version, want.Version)
		}
		if sshInboundPlanDigest(plan) != sshInboundPlanDigest(want) {
			t.Fatalf("plan = %#v want %#v", plan, want)
		}
		if got := srv.deployedSSHPlans.decodes.Load(); got != wantDecodes {
			t.Fatalf("payload decodes = %d want %d", got, wantDecodes)
		}
	}

	first := createDeployment(1, "first")
	assertPlan(first, 1)
	assertPlan(first, 1)
	assertPlan(first, 1)

	second := createDeployment(2, "second")
	assertPlan(second, 2)
	assertPlan(second, 2)
}

// TestLastAppliedSSHPlanKeepsUnverifiedAndFocusedSelection keeps the selection
// and authentication-evidence semantics the memoized projection replaced.
func TestLastAppliedSSHPlanKeepsUnverifiedAndFocusedSelection(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "ssh-node", PublicIPv4: "203.0.113.32"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	plan := model.SSHInboundPlan{Version: 1, Inbounds: []model.SSHInbound{{InboundID: 1, ServerID: server.ID, Enabled: true, Users: []model.SSHInboundUser{{Enabled: true}}}}}
	payload, _ := json.Marshal(model.DeploymentTaskPayload{Version: 1, SSHInbounds: plan})
	unverified := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, ConfigVersion: 1, PayloadJSON: string(payload), Nonce: "unverified", Status: "succeeded", ResultJSON: `{"steps":[{"key":"ssh_inbounds","status":"succeeded","result":{"host_public_key":"old-key"}}]}`}
	if err := db.CreateTask(ctx, &unverified); err != nil {
		t.Fatal(err)
	}
	if got, version, planErr := srv.lastAppliedSSHPlan(ctx, server.ID); planErr != nil || version != 0 || len(got.Inbounds) != 0 {
		t.Fatalf("deployment without authentication evidence was applied: version=%d plan=%#v err=%v", version, got, planErr)
	}

	// A focused apply_core_config at a newer version wins the selection, while a
	// focused task that carries no SSH section must not.
	focusedPlan := plan
	focusedPlan.Version = 3
	focused, _ := json.Marshal(model.ApplyCoreConfigTaskPayload{SSHInbounds: &focusedPlan})
	focusedTask := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyCoreConfig, ConfigVersion: 3, PayloadJSON: string(focused), Nonce: "focused", Status: "succeeded", ResultJSON: verifiedFocusedSSHTestResult(t, focusedPlan)}
	if err := db.CreateTask(ctx, &focusedTask); err != nil {
		t.Fatal(err)
	}
	if _, version, planErr := srv.lastAppliedSSHPlan(ctx, server.ID); planErr != nil || version != 3 {
		t.Fatalf("focused SSH deployment did not win selection: version=%d err=%v", version, planErr)
	}
	bare, _ := json.Marshal(model.ApplyCoreConfigTaskPayload{})
	bareTask := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyCoreConfig, ConfigVersion: 4, PayloadJSON: string(bare), Nonce: "bare", Status: "succeeded", ResultJSON: `{}`}
	if err := db.CreateTask(ctx, &bareTask); err != nil {
		t.Fatal(err)
	}
	if _, version, planErr := srv.lastAppliedSSHPlan(ctx, server.ID); planErr != nil || version != 0 {
		t.Fatalf("focused task without an SSH section changed the applied plan: version=%d err=%v", version, planErr)
	}
}
