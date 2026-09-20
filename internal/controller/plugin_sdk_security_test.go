package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/store"
)

func pluginSDKSecurityFixture(t *testing.T) (*Server, application.Principal, model.PluginRun, *bool) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(t.TempDir() + "/plugin-sdk.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user := &model.User{Username: "plugin-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "plugin-sdk-test", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	actor := application.HumanPrincipal(*user, model.RoleAdmin, application.Principal{}.SourceIP)
	s := &Server{store: db, capabilities: capability.NewCatalog()}
	s.plugins = plugin.NewService(db, s.capabilities.RBAC())
	s.plugins.SetCallerResolver(s.resolvePluginCaller)
	s.automation = automation.NewService(db, s.capabilities)
	s.automation.SetPluginPrincipalResolver(s.resolvePluginChangesetPrincipal)
	if err := s.plugins.UpdateSettings(ctx, actor, plugin.Settings{Enabled: true, MaxConcurrency: 2, MaxTimeoutSeconds: 30}); err != nil {
		t.Fatal(err)
	}
	item, err := s.plugins.CreatePlugin(ctx, actor, "security-test", "")
	if err != nil {
		t.Fatal(err)
	}
	caps := []string{plugin.SDKManagementApply, "management:servers.update", model.PluginSDKOperationsGet}
	manifest := model.PluginManifest{SchemaVersion: model.PluginSchemaVersion, Runtime: model.PluginRuntimeOBoardJSv1, SDKVersion: model.PluginSDKVersionV1, Entry: "main", Capabilities: caps}
	rev, err := s.plugins.SaveDraft(ctx, actor, item.ID, "function main(){return {ok:true}}", plugin.MustJSON(manifest))
	if err != nil {
		t.Fatal(err)
	}
	rev, err = s.plugins.Publish(ctx, actor, item.ID, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.plugins.UpdatePlugin(ctx, actor, item.ID, "", "", model.PluginStatusEnabled, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.plugins.CreateGrant(ctx, actor, model.PluginGrant{RevisionID: rev.ID, CapabilitiesJSON: plugin.MustJSON(caps), ResourceScopeJSON: json.RawMessage(`{"servers":{"mode":"selected","ids":[7]}}`)})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.plugins.EnqueueManualRun(ctx, actor, item.ID, rev.ID, json.RawMessage(`{}`), json.RawMessage(`{}`), "sdk-security", model.PluginRunModeLive)
	if err != nil {
		t.Fatal(err)
	}
	run, err := db.LeasePluginRun(ctx, "sdk-worker", time.Now().Add(time.Minute), s.plugins.Settings(ctx).RecoveryGeneration)
	if err != nil {
		t.Fatal(err)
	}
	applied := false
	s.automation.RegisterValidator("servers.update", func(_ context.Context, p application.Principal, _ json.RawMessage) (any, error) {
		if p.Type != model.APIPrincipalPlugin || p.Interactive || !p.AllowsInt64("server_ids", 7) || p.AllowsInt64("server_ids", 8) {
			t.Fatalf("wrong validation identity: %#v", p)
		}
		return map[string]bool{"valid": true}, nil
	})
	s.automation.Register("servers.update", func(_ context.Context, p application.Principal, _ json.RawMessage) (any, error) {
		if p.ID != "plugin:"+run.UUID || p.Interactive || p.AllowsInt64("server_ids", 8) {
			t.Fatalf("wrong execution identity: %#v", p)
		}
		applied = true
		return map[string]bool{"saved": true}, nil
	})
	return s, actor, run, &applied
}

func TestPluginManagementDeferredApprovalAndOperationReceipt(t *testing.T) {
	for _, mode := range []string{"allowed", "grant_revoked", "plugin_disabled", "runtime_disabled", "caller_denied", "caller_revoked", "run_cancelled", "run_failed", "binding_lost"} {
		t.Run(mode, func(t *testing.T) {
			s, actor, run, applied := pluginSDKSecurityFixture(t)
			ctx := context.Background()
			gateway := plugin.NewGateway(s.store, s)
			response, err := gateway.Invoke(ctx, s.plugins, pluginrpc.SDKRequest{WorkerID: run.LeaseOwner, RunUUID: run.UUID, LeaseGeneration: run.LeaseGeneration, Capability: plugin.SDKManagementApply, ActionKey: "update-server", Arguments: plugin.MustJSON(map[string]any{"management": plugin.ManagementRequest{Capability: "servers.update", Input: map[string]any{"server_id": "7"}, Reason: "plugin test"}})})
			if err != nil || !response.OK || response.OperationID == "" || response.ChangesetID == "" || *applied {
				t.Fatalf("expected durable approval receipt: %#v %v applied=%v", response, err, *applied)
			}
			effective, err := s.plugins.EffectivePrincipal(ctx, run)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := s.GetOperation(ctx, effective, response.OperationID)
			if err != nil || receipt["changeset_id"] != response.ChangesetID {
				t.Fatalf("management operation cannot be tracked: %#v %v", receipt, err)
			}
			other := effective
			other.ID = "plugin:another-run"
			if _, err := s.GetOperation(ctx, other, response.OperationID); err == nil {
				t.Fatal("cross-run receipt was disclosed")
			}
			status := model.PluginRunSucceeded
			if mode == "run_cancelled" {
				status = model.PluginRunCancelled
			}
			if mode == "run_failed" {
				status = model.PluginRunFailed
			}
			if err := s.store.FinishPluginRun(ctx, run.ID, run.LeaseGeneration, status, "", json.RawMessage(`{}`)); err != nil {
				t.Fatal(err)
			}
			completed, err := s.store.GetPluginRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.plugins.EffectivePrincipal(ctx, completed); err == nil {
				t.Fatal("completed runner retained SDK authority")
			}
			switch mode {
			case "grant_revoked":
				if err := s.store.RevokePluginGrant(ctx, *run.GrantID); err != nil {
					t.Fatal(err)
				}
			case "plugin_disabled":
				if _, err := s.plugins.UpdatePlugin(ctx, actor, run.PluginID, "", "", model.PluginStatusDisabled, ""); err != nil {
					t.Fatal(err)
				}
			case "runtime_disabled":
				if err := s.plugins.UpdateSettings(ctx, actor, plugin.Settings{Enabled: false, MaxConcurrency: 2, MaxTimeoutSeconds: 30}); err != nil {
					t.Fatal(err)
				}
			case "caller_denied":
				if err := s.store.CreateAPIPrincipal(ctx, &model.APIPrincipal{ID: actor.ID, Name: "caller policy", Type: model.APIPrincipalOAuth, Enabled: true, OwnerUserID: actor.UserID}); err != nil {
					t.Fatal(err)
				}
				if err := s.store.UpsertApprovalPolicy(ctx, &model.ApprovalPolicy{ID: "caller-denied", PrincipalID: actor.ID, Capability: "servers.update", Mode: model.ApprovalDenied, ResourceFilter: json.RawMessage(`{}`)}); err != nil {
					t.Fatal(err)
				}
			case "caller_revoked":
				s.plugins.SetCallerResolver(nil)
			case "binding_lost":
				action, err := s.store.GetPluginRunAction(ctx, run.ID, "update-server")
				if err != nil {
					t.Fatal(err)
				}
				action.ChangesetID = ""
				if err := s.store.UpdatePluginRunAction(ctx, action); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.automation.Approve(ctx, actor, response.ChangesetID, "reviewed")
			if mode == "allowed" {
				if err != nil || result.Status != model.ChangesetSucceeded || !*applied {
					t.Fatalf("approval after runner exit failed: %#v %v applied=%v", result, err, *applied)
				}
			} else if err == nil || *applied {
				t.Fatalf("revoked deferred action applied: %#v %v applied=%v", result, err, *applied)
			}
		})
	}
}
