package controller

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestSSHPlanPublishPreparesDeniedThenFinalizesActive(t *testing.T) {
	h, srv, token, ids := setupOrderingTestTopology(t)
	ctx := t.Context()
	server, err := srv.store.GetServer(ctx, ids["s1"])
	if err != nil {
		t.Fatal(err)
	}
	server.AgentID = "ssh-test-agent"
	if err := srv.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "ssh-member", PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := srv.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := srv.store.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: ids["plan"]}}); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "ssh", Protocol: model.ProtocolSSH, ListenIP: "::", Port: 7001, Enabled: true, ConfigJSON: `{"exposure_confirmed":true,"exposure_confirmation_version":"ssh-inbound-v1","access_mode":"restricted_proxy"}`}
	if err := srv.store.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	saved := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans/"+itoa(ids["plan"])+"/nodes/apply", token, map[string]any{
		"op": "add", "nodes": []map[string]any{{"node_type": "inbound", "node_id": inbound.ID}},
	}, http.StatusOK)
	changeID := reconcileSavedPlanChange(t, srv, saved)
	checkPhase := func(phase, wantStatus string, wantGrant bool) {
		t.Helper()
		targets, err := srv.store.ListAccessChangeTargets(ctx, changeID)
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			if target.ServerID != server.ID {
				continue
			}
			taskID := target.PrepareTaskID
			if phase == "finalize" {
				taskID = target.FinalizeTaskID
			}
			task, err := srv.store.GetTask(ctx, taskID)
			if err != nil {
				t.Fatal(err)
			}
			var payload model.ApplyCoreConfigTaskPayload
			if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.SSHInbounds == nil || len(payload.SSHInbounds.Inbounds) != 1 || len(payload.SSHInbounds.Inbounds[0].Users) != 1 {
				t.Fatal("missing SSH candidate")
			}
			candidate := payload.SSHInbounds.Inbounds[0].Users[0]
			if candidate.PathID != inbound.ID || candidate.CredentialStatus != wantStatus {
				t.Fatalf("%s: path=%d status=%s", phase, candidate.PathID, candidate.CredentialStatus)
			}
			var config struct {
				Metadata struct {
					Authorization model.AuthorizationLease `json:"authorization"`
				} `json:"_oboard"`
			}
			if err := json.Unmarshal([]byte(payload.Config), &config); err != nil {
				t.Fatal(err)
			}
			if got := config.Metadata.Authorization.Grants[candidate.AuthorizationKey] != ""; got != wantGrant {
				t.Fatalf("%s: grant=%v want=%v", phase, got, wantGrant)
			}
			return
		}
		t.Fatal("missing server target")
	}
	checkPhase("prepare", "reject_new", false)
	change, err := srv.store.GetAccessChange(ctx, changeID)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.activateAccessChange(ctx, change); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.queueAccessChangePhase(ctx, change, "finalize"); err != nil {
		t.Fatal(err)
	}
	checkPhase("finalize", "active", true)
}

func TestPrepareSSHAuthorizationKeepsCandidatesDeniedUntilActivation(t *testing.T) {
	now := time.Now().UTC()
	users := []model.SSHInboundUser{
		{UserID: 1, PathID: 48, Enabled: true, CredentialStatus: "active", AuthorizationKey: "candidate"},
		{UserID: 2, PathID: 48, Enabled: true, CredentialStatus: "active", AuthorizationKey: "existing"},
		{UserID: 3, PathID: 48, Enabled: true, CredentialStatus: "reject_new", AuthorizationKey: "restricted"},
		{UserID: 4, PathID: 48, Enabled: true, CredentialStatus: "active", AuthorizationKey: "expired"},
	}
	plan := model.SSHInboundPlan{Inbounds: []model.SSHInbound{{InboundID: 48, Enabled: true, Users: append([]model.SSHInboundUser(nil), users...)}}}
	lease := &model.AuthorizationLease{Grants: map[string]string{
		"existing":   now.Add(time.Minute).Format(time.RFC3339Nano),
		"restricted": now.Add(time.Minute).Format(time.RFC3339Nano),
		"expired":    now.Add(-time.Second).Format(time.RFC3339Nano),
	}}
	prepareSSHAuthorization(&plan, lease, now)
	for i, want := range []string{"reject_new", "active", "reject_new", "reject_new"} {
		if got := plan.Inbounds[0].Users[i].CredentialStatus; got != want {
			t.Fatalf("user %d: got %s, want %s", i, got, want)
		}
	}
	if _, ok := lease.Grants["candidate"]; ok {
		t.Fatal("prepare granted candidate access before activation")
	}
	lease.Grants["candidate"] = now.Add(time.Minute).Format(time.RFC3339Nano)
	plan.Inbounds[0].Users = append([]model.SSHInboundUser(nil), users...)
	prepareSSHAuthorization(&plan, lease, now)
	if plan.Inbounds[0].Users[0].CredentialStatus != "active" {
		t.Fatal("already activated candidate was blocked")
	}
}
