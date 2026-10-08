package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/store"
)

const pageManifest = `{
  "id": "acme.page-test",
  "name": "Page test",
  "version": "1.0.0",
  "description": "",
  "runtime": "oboard-js",
  "entry": "main.js",
  "capabilities": ["ui.page", "servers.read", "servers.metrics.read", "secrets.use"],
  "environment": [
    {"name": "TARGET_SERVER", "type": "server", "label": "服务器", "required": true},
    {"name": "API_KEY", "type": "secret", "label": "密钥"}
  ],
  "pages": [{"id": "overview", "title": "巡检", "actions": ["recheck"]}],
  "triggers": {"schedule": false},
  "limits": {}
}`

func (h *harness) installPages() int64 {
	h.t.Helper()
	candidate := h.candidate(pageManifest, model.PluginPublisherLocal)
	result, err := h.service.InstallPackage(h.ctx, adminPrincipal(), candidate, candidate.SHA256)
	if err != nil {
		h.t.Fatal(err)
	}
	instance, err := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.service.SaveEnvironment(h.ctx, operatorPrincipal(), result.InstanceID, instance.Revision, EnvironmentInput{Values: map[string]json.RawMessage{"TARGET_SERVER": raw(`"2"`)}}); err != nil {
		h.t.Fatal(err)
	}
	if err := h.service.SetSecret(h.ctx, adminPrincipal(), result.InstanceID, "API_KEY", "s3cret-value"); err != nil {
		h.t.Fatal(err)
	}
	grant := Grant{Capabilities: map[string]CapabilityGrant{
		CapUIPage: {}, CapServersRead: {Servers: []int64{2}}, CapServersMetricsRead: {Servers: []int64{2}}, CapSecretsUse: {},
	}}
	if _, err := h.service.SetGrant(h.ctx, adminPrincipal(), result.InstanceID, 0, grant); err != nil {
		h.t.Fatal(err)
	}
	if err := h.service.SetInstallationEnabled(h.ctx, adminPrincipal(), result.InstallationID, true); err != nil {
		h.t.Fatal(err)
	}
	enabled := true
	if _, err := h.service.UpdateInstance(h.ctx, adminPrincipal(), result.InstanceID, InstanceUpdate{Enabled: &enabled}); err != nil {
		h.t.Fatal(err)
	}
	return result.InstanceID
}

func TestPluginPagePublishResolveAndRefresh(t *testing.T) {
	h := newHarness(t)
	instanceID := h.installPages()
	before, err := h.service.ListRuns(h.ctx, adminPrincipal(), store.PluginRunFilter{InstanceID: instanceID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.ListInstancePages(h.ctx, operatorPrincipal(), instanceID); err != nil {
		t.Fatal(err)
	}
	after, err := h.service.ListRuns(h.ctx, adminPrincipal(), store.PluginRunFilter{InstanceID: instanceID})
	if err != nil || len(after) != len(before) {
		t.Fatalf("reading a page must not queue a run: before %d after %d %v", len(before), len(after), err)
	}

	lease := h.startRun(instanceID)
	document := map[string]any{
		"title": "巡检",
		"body": []any{
			map[string]any{"type": "metric", "label": "状态", "value": "正常"},
			map[string]any{"type": "text", "text": "token s3cret-value"},
			map[string]any{"type": "binding", "source": "servers.metrics", "server": map[string]any{"$env": "TARGET_SERVER"}},
			map[string]any{"type": "button", "action": "recheck", "label": "立即复测"},
		},
	}
	published := h.call(lease, "ui.publish", map[string]any{"page": "overview", "document": document})
	if !published.OK {
		t.Fatalf("publish failed: %+v", published)
	}
	expectCode(t, h.call(lease, "ui.publish", map[string]any{"page": "overview", "document": map[string]any{"body": []any{map[string]any{"type": "button", "action": "delete", "label": "删除"}}}}), CodeInvalidArgument)

	pages, err := h.service.ListInstancePages(h.ctx, operatorPrincipal(), instanceID)
	if err != nil || len(pages.Pages) != 1 || pages.Pages[0].PublishedAt == "" {
		t.Fatalf("page not stored: %+v %v", pages, err)
	}
	encoded, _ := json.Marshal(pages)
	if strings.Contains(string(encoded), "s3cret-value") || !strings.Contains(string(encoded), "[REDACTED]") {
		t.Fatalf("secret leaked into the page: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"server_id":"2"`) {
		t.Fatalf("binding was not resolved for the operator: %s", encoded)
	}
	restricted := operatorPrincipal()
	restricted.ResourceFilter = json.RawMessage(`{"servers":{"mode":"selected","ids":[3]}}`)
	hidden, err := h.service.ListInstancePages(h.ctx, restricted, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	hiddenJSON, _ := json.Marshal(hidden)
	if strings.Contains(string(hiddenJSON), `"server_id":"2"`) || !strings.Contains(string(hiddenJSON), CodeResourceDenied) {
		t.Fatalf("binding ignored the operator scope: %s", hiddenJSON)
	}

	if err := h.service.Complete(h.ctx, pluginrpc.CompleteRequest{WorkerID: "worker-1", RunUUID: lease.RunUUID, LeaseGeneration: lease.LeaseGeneration, Status: model.PluginRunSucceeded}); err != nil {
		t.Fatal(err)
	}
	run, err := h.service.RefreshInstancePage(h.ctx, operatorPrincipal(), instanceID, "overview", "refresh-1")
	if err != nil || run.Trigger != model.PluginTriggerUI {
		t.Fatalf("refresh did not queue a ui run: %+v %v", run, err)
	}
	refreshed, err := h.service.Lease(h.ctx, "worker-1")
	if err != nil || refreshed == nil || refreshed.Context.Trigger != model.PluginTriggerUI || refreshed.Context.Page != "overview" {
		t.Fatalf("ui lease missing page: %+v %v", refreshed, err)
	}
	if err := h.service.Complete(h.ctx, pluginrpc.CompleteRequest{WorkerID: "worker-1", RunUUID: refreshed.RunUUID, LeaseGeneration: refreshed.LeaseGeneration, Status: model.PluginRunSucceeded}); err != nil {
		t.Fatal(err)
	}
	action, err := h.service.RunInstancePageAction(h.ctx, operatorPrincipal(), instanceID, "overview", "recheck", "action-1")
	if err != nil || action.Trigger != model.PluginTriggerAction {
		t.Fatalf("action did not queue: %+v %v", action, err)
	}
	if _, err := h.service.RunInstancePageAction(h.ctx, operatorPrincipal(), instanceID, "overview", "delete", "action-2"); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("undeclared action queued: %v", err)
	}
	if err := h.service.RevokeGrant(h.ctx, adminPrincipal(), instanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.ListInstancePages(h.ctx, operatorPrincipal(), instanceID); CodeOf(err) != CodeCapabilityDenied {
		t.Fatalf("revoked grant still exposes the page: %v", err)
	}
}
