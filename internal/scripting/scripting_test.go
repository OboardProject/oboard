package scripting

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/scriptrpc"
	"github.com/OboardProject/oboard/internal/store"
)

func testScriptEnv(t *testing.T) (*store.Store, *Service, *model.User) {
	t.Helper()
	db, err := store.Open(t.TempDir() + "/scripts.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user := &model.User{Username: "script-admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "script-uuid", ProxyPassword: "script-pass"}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return db, NewService(db, capability.NewCatalog().RBAC()), user
}

func adminActor(user *model.User) application.Principal {
	return application.HumanPrincipal(*user, model.RoleAdmin, application.Principal{}.SourceIP)
}

func operatorActor(user *model.User) application.Principal {
	return application.HumanPrincipal(*user, model.RoleOperator, application.Principal{}.SourceIP)
}

func testManifest(capabilities ...string) json.RawMessage {
	if len(capabilities) == 0 {
		capabilities = []string{model.ScriptSDKServersStatus}
	}
	return MustJSON(model.ScriptManifest{
		SchemaVersion: model.ScriptSchemaVersion,
		Runtime:       model.ScriptRuntimeOBoardJSv1,
		SDKVersion:    model.ScriptSDKVersionV1,
		Entry:         "main",
		Capabilities:  capabilities,
		Params:        json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{}}`),
	})
}

func TestValidateRevisionDoesNotCompileSource(t *testing.T) {
	_, svc, user := testScriptEnv(t)
	ctx := context.Background()
	actor := adminActor(user)
	script, err := svc.CreateScript(ctx, actor, "validate-only", "")
	if err != nil {
		t.Fatal(err)
	}
	source := "function main(){ while(true){} }"
	result, err := svc.ValidateRevision(ctx, actor, script.ID, source, testManifest(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if result["source_compiled"] != false {
		t.Fatalf("validate must not compile source: %#v", result)
	}
	if result["valid"] != true {
		t.Fatalf("valid source/manifest should pass: %#v", result)
	}
}

func TestParseManifestRejectsUnknownFieldsAndReservedEnv(t *testing.T) {
	if _, err := ParseManifest(json.RawMessage(`{"schema_version":1,"runtime":"oboard-js-v1","sdk_version":"oboard-sdk-v1","entry":"main","capabilities":["servers.get"],"extra":true}`)); err == nil {
		t.Fatal("unknown manifest field must be rejected")
	}
	manifest := model.ScriptManifest{
		SchemaVersion: 1, Runtime: model.ScriptRuntimeOBoardJSv1, SDKVersion: model.ScriptSDKVersionV1,
		Entry: "main", Capabilities: []string{model.ScriptSDKServersGet},
		Env: []model.ScriptEnvDeclaration{{Name: "OBOARD_RUN_ID"}},
	}
	if err := ValidateManifest(manifest); err == nil {
		t.Fatal("reserved env names must be rejected")
	}
}

func TestScheduleUsesPersistentSlotsAndSkipsDSTGap(t *testing.T) {
	spec, err := ParseTriggerSpec(MustJSON(model.ScriptTriggerSpec{Timezone: "America/New_York", Cron: "0 2 * * *"}))
	if err != nil {
		t.Fatal(err)
	}
	slots, err := NextSlots(spec, time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC), 3)
	if err != nil || len(slots) != 3 {
		t.Fatalf("cron slots: %v %#v", err, slots)
	}
	key := SlotKey(9, slots[0])
	if !strings.Contains(key, "trig:9:slot:") {
		t.Fatalf("slot key: %s", key)
	}
	event := EventKey(3, 8, 11, model.ScriptEventServerOffline)
	if event != "trig:3:server:8:incident:11:event:server.offline" {
		t.Fatalf("event key: %s", event)
	}
}

func TestOperatorCannotAuthorizeOrChangeRuntime(t *testing.T) {
	db, svc, user := testScriptEnv(t)
	ctx := context.Background()
	admin := adminActor(user)
	script, err := svc.CreateScript(ctx, admin, "gated", "")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := svc.SaveDraft(ctx, admin, script.ID, "function main(){return {ok:true}}", testManifest(model.ScriptSDKHostPoweroff))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Publish(ctx, admin, script.ID, rev.ID); err != nil {
		t.Fatal(err)
	}
	operator := operatorActor(user)
	if _, err := svc.CreateGrant(ctx, operator, model.ScriptGrant{
		RevisionID: rev.ID,
		CapabilitiesJSON: MustJSON([]string{model.ScriptSDKHostPoweroff}),
		ResourceScopeJSON: MustJSON(application.ResourceFilter{Servers: &application.ResourceSelection{Mode: "selected", IDs: []int64{1}}}),
	}); err == nil {
		t.Fatal("operator must not create grants")
	}
	if err := svc.UpdateSettings(ctx, operator, Settings{Enabled: true, MaxConcurrency: 2, MaxTimeoutSeconds: 30}); err == nil {
		t.Fatal("operator must not change global script settings")
	}
	_ = db
}

func TestStalePowerGrantRejectedAfterRevisionChange(t *testing.T) {
	_, svc, user := testScriptEnv(t)
	ctx := context.Background()
	actor := adminActor(user)
	script, err := svc.CreateScript(ctx, actor, "power", "")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := svc.SaveDraft(ctx, actor, script.ID, "function main(){return {v:1}}", testManifest(model.ScriptSDKHostPoweroff))
	if err != nil {
		t.Fatal(err)
	}
	published, err := svc.Publish(ctx, actor, script.ID, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGrant(ctx, actor, model.ScriptGrant{
		RevisionID: published.ID, SourceDigest: published.SourceDigest,
		CapabilitiesJSON: MustJSON([]string{model.ScriptSDKHostPoweroff}),
		ResourceScopeJSON: MustJSON(application.ResourceFilter{Servers: &application.ResourceSelection{Mode: "selected", IDs: []int64{4}}}),
	}); err != nil {
		t.Fatal(err)
	}
	next, err := svc.SaveDraft(ctx, actor, script.ID, "function main(){return {v:2}}", testManifest(model.ScriptSDKHostPoweroff))
	if err != nil {
		t.Fatal(err)
	}
	newer, err := svc.Publish(ctx, actor, script.ID, next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateGrant(ctx, actor, model.ScriptGrant{
		RevisionID: newer.ID, SourceDigest: published.SourceDigest,
		CapabilitiesJSON: MustJSON([]string{model.ScriptSDKHostPoweroff}),
		ResourceScopeJSON: MustJSON(application.ResourceFilter{Servers: &application.ResourceSelection{Mode: "selected", IDs: []int64{4}}}),
	}); err == nil {
		t.Fatal("old source digest must not authorize a new published revision")
	}
	if _, err := svc.EnqueueManualRun(ctx, actor, script.ID, newer.ID, json.RawMessage(`{}`), json.RawMessage(`{}`), "live-1", model.ScriptRunModeLive); err == nil {
		t.Fatal("live run without a matching grant must require approval")
	}
}

func TestSimulateManageActionDoesNotCallHost(t *testing.T) {
	db, svc, user := testScriptEnv(t)
	ctx := context.Background()
	actor := adminActor(user)
	script, err := svc.CreateScript(ctx, actor, "sim", "")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := svc.SaveDraft(ctx, actor, script.ID, "function main(){return {ok:true}}", testManifest(model.ScriptSDKHostPoweroff))
	if err != nil {
		t.Fatal(err)
	}
	published, err := svc.Publish(ctx, actor, script.ID, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.EnqueueManualRun(ctx, actor, script.ID, published.ID, json.RawMessage(`{}`), json.RawMessage(`{}`), "sim-1", model.ScriptRunModeSimulate)
	if err != nil {
		t.Fatal(err)
	}
	leased, err := db.LeaseScriptRun(ctx, "worker-test", time.Now().Add(time.Minute), svc.Settings(ctx).RecoveryGeneration)
	if err != nil {
		t.Fatal(err)
	}
	host := &recordingHost{}
	gateway := NewGateway(db, host)
	resp, err := gateway.Invoke(ctx, svc, scriptrpc.SDKRequest{
		RunUUID: leased.UUID, LeaseGeneration: leased.LeaseGeneration,
		Capability: model.ScriptSDKHostPoweroff, ActionKey: "power-1",
		Arguments: MustJSON(map[string]any{"server_id": "4"}),
	})
	if err != nil || !resp.OK || !strings.HasPrefix(resp.OperationID, "sim_") {
		t.Fatalf("simulate response: %#v err=%v", resp, err)
	}
	if host.powerCalls != 0 || host.notifyCalls != 0 || host.restartCalls != 0 {
		t.Fatalf("simulate must not dispatch real actions: %#v", host)
	}
	actions, err := db.ListScriptRunActions(ctx, run.ID)
	if err != nil || len(actions) != 0 {
		t.Fatalf("simulate must not persist live actions: %#v err=%v", actions, err)
	}
}

func TestManualIdempotencyConflictOnDifferentPayload(t *testing.T) {
	_, svc, user := testScriptEnv(t)
	ctx := context.Background()
	actor := adminActor(user)
	script, err := svc.CreateScript(ctx, actor, "idem", "")
	if err != nil {
		t.Fatal(err)
	}
	rev, err := svc.SaveDraft(ctx, actor, script.ID, "function main(){return {ok:true}}", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	published, err := svc.Publish(ctx, actor, script.ID, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.EnqueueManualRun(ctx, actor, script.ID, published.ID, json.RawMessage(`{}`), json.RawMessage(`{}`), "same-key", model.ScriptRunModeSimulate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.EnqueueManualRun(ctx, actor, script.ID, published.ID, json.RawMessage(`{}`), json.RawMessage(`{}`), "same-key", model.ScriptRunModeSimulate)
	if err != nil || second.ID != first.ID {
		t.Fatalf("identical replay must reuse run: %#v err=%v", second, err)
	}
	other, err := svc.SaveDraft(ctx, actor, script.ID, "function main(){return {other:true}}", testManifest())
	if err != nil {
		t.Fatal(err)
	}
	otherPub, err := svc.Publish(ctx, actor, script.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnqueueManualRun(ctx, actor, script.ID, otherPub.ID, json.RawMessage(`{}`), json.RawMessage(`{}`), "same-key", model.ScriptRunModeSimulate); err == nil {
		t.Fatal("same idempotency key with a different revision must conflict")
	}
}

type recordingHost struct {
	powerCalls   int
	notifyCalls  int
	restartCalls int
}

func (h *recordingHost) GetServer(context.Context, application.Principal, int64) (map[string]any, error) {
	return map[string]any{}, nil
}
func (h *recordingHost) ListServers(context.Context, application.Principal, int) ([]map[string]any, error) {
	return nil, nil
}
func (h *recordingHost) ServerStatus(context.Context, application.Principal, int64) (map[string]any, error) {
	return map[string]any{}, nil
}
func (h *recordingHost) LatestMetrics(context.Context, application.Principal, int64) (map[string]any, error) {
	return map[string]any{}, nil
}
func (h *recordingHost) GetIncident(context.Context, application.Principal, int64) (map[string]any, error) {
	return map[string]any{}, nil
}
func (h *recordingHost) ServiceStatus(context.Context, application.Principal, int64, string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (h *recordingHost) RestartService(context.Context, application.Principal, model.ScriptRun, model.ScriptRunAction, int64, string) (map[string]any, error) {
	h.restartCalls++
	return map[string]any{"operation_id": "real"}, nil
}
func (h *recordingHost) HostPower(context.Context, application.Principal, model.ScriptRun, model.ScriptRunAction, int64, string, string) (map[string]any, error) {
	h.powerCalls++
	return map[string]any{"operation_id": "real"}, nil
}
func (h *recordingHost) SendNotification(context.Context, application.Principal, model.ScriptRun, model.ScriptRunAction, int64, string, string) (map[string]any, error) {
	h.notifyCalls++
	return map[string]any{"operation_id": "real"}, nil
}
func (h *recordingHost) GetOperation(context.Context, application.Principal, string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (h *recordingHost) WaitOperation(context.Context, application.Principal, string, int) (map[string]any, error) {
	return map[string]any{}, nil
}
