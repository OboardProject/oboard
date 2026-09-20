package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugingithub"
	"github.com/OboardProject/oboard/internal/pluginpackage"
)

func controllerPluginZIP(t *testing.T, version string) []byte {
	t.Helper()
	manifest := fmt.Sprintf(`{"plugin_id":"example.http-test","name":"HTTP test","description":"Package integration test","version":%q,"schema_version":1,"runtime":"oboard-js-v1","sdk_version":"oboard-sdk-v1","entry":"main","params_schema":{"type":"object","additionalProperties":false},"config_schema":{"type":"object","properties":{"label":{"type":"string"}},"additionalProperties":false},"env":[],"secrets":[{"name":"api_key","purpose":"network"}],"capabilities":["servers.status"],"resource_types":["servers"],"limits":{"timeout_seconds":10,"sdk_calls":1}}`, version)
	data, err := pluginpackage.Build([]byte(manifest), []byte("function main(){return {ok:true}}"), []byte(`{"pages":[{"id":"overview","title":"Overview","components":[{"id":"status","type":"text","text":"Ready"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPluginPackageHTTPAndSharedCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	app := newTestServer(db, "package-test-secret", "")
	h := app.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	call := func(method, path string, body any, status int) map[string]any {
		t.Helper()
		envelope := request(t, h, method, "/api/v1/plugins"+path, token, body, status)
		if status != http.StatusOK {
			return envelope
		}
		data, ok := envelope["data"].(map[string]any)
		if !ok {
			t.Fatalf("missing v1 data envelope: %#v", envelope)
		}
		return data
	}
	data := controllerPluginZIP(t, "1.0.0")
	preview := call(http.MethodPost, "/packages/preview", map[string]any{"package_zip": data}, http.StatusOK)
	assertCapabilityOutputSchema(t, app, "plugins.packages.preview", preview)
	input := map[string]any{"package_zip": data, "expected_sha256": preview["sha256"], "confirm": false}
	call(http.MethodPost, "/packages/install", input, http.StatusBadRequest)
	input["confirm"] = true
	input["expected_sha256"] = strings.Repeat("0", 64)
	call(http.MethodPost, "/packages/install", input, http.StatusConflict)
	input["expected_sha256"] = preview["sha256"]
	installed := call(http.MethodPost, "/packages/install", input, http.StatusOK)
	assertCapabilityOutputSchema(t, app, "plugins.packages.install", installed)
	installation := installed["installation"].(map[string]any)
	id := int64(installation["plugin_id"].(float64))
	prefix := "/" + itoa(id)
	plugin, err := db.GetPlugin(t.Context(), id)
	if err != nil || plugin.Status != model.PluginStatusDisabled {
		t.Fatalf("install enabled execution: %#v %v", plugin, err)
	}
	actor := userAutomationPrincipal(t, db, plugin.OwnerUserID)
	for _, tc := range []struct{ path, name string }{{"/versions", "plugins.versions.list"}, {"/config", "plugins.config.get"}, {"/ui", "plugins.ui.get"}} {
		got := call(http.MethodGet, prefix+tc.path, nil, http.StatusOK)
		assertCapabilityOutputSchema(t, app, tc.name, got)
		raw := json.RawMessage(fmt.Sprintf(`{"plugin_id":%q}`, itoa(id)))
		shared, err := app.queryManagementCapability(t.Context(), actor, tc.name, raw)
		if err != nil {
			t.Fatalf("MCP shared query %s: %v", tc.name, err)
		}
		assertCapabilityOutputSchema(t, app, tc.name, shared)
		descriptor, ok := app.capabilities.Get(tc.name)
		if !ok || !descriptor.MCPEnabled {
			t.Fatalf("missing MCP descriptor: %s", tc.name)
		}
		if _, err := descriptor.ResolveResourceRefs(t.Context(), map[string]any{"plugin_id": itoa(id)}); err != nil {
			t.Fatal(err)
		}
	}
	config := call(http.MethodPut, prefix+"/config", map[string]any{"config": map[string]any{"label": "fleet"}}, http.StatusOK)
	assertCapabilityOutputSchema(t, app, "plugins.config.update", config)
	call(http.MethodPut, prefix+"/config", map[string]any{"config": map[string]any{"api_key": "never-store-as-config"}}, http.StatusBadRequest)
	call(http.MethodPut, prefix+"/secrets/api_key", map[string]any{"value": "test-secret-value"}, http.StatusOK)
	encrypted, err := db.GetPluginInstallationSecretEncrypted(t.Context(), id, "api_key")
	if err != nil || encrypted == "" || strings.Contains(encrypted, "test-secret-value") {
		t.Fatalf("secret not encrypted: %v", err)
	}
	machine := actor
	machine.Interactive = false
	machine.ClientName = "mcp-client"
	if err := app.setPluginPackageSecret(t.Context(), machine, id, "api_key", "forbidden"); err == nil {
		t.Fatal("MCP could set a secret")
	}
	for _, name := range []string{"plugins.secrets.set", "plugins.secrets.get"} {
		if _, ok := app.capabilities.Get(name); ok {
			t.Fatalf("secret capability exposed: %s", name)
		}
	}

	second := controllerPluginZIP(t, "1.1.0")
	commit := strings.Repeat("a", 40)
	refs := []string{}
	app.plugins.SetPackageFetcher(func(_ context.Context, repository, ref string) (plugingithub.Result, error) {
		if repository != "https://github.com/example/plugin" {
			t.Fatalf("repository: %s", repository)
		}
		refs = append(refs, ref)
		return plugingithub.Result{Archive: second, Source: plugingithub.Source{Owner: "example", Repo: "plugin", Commit: commit}}, nil
	})
	github := call(http.MethodPost, "/github/preview", map[string]any{"repository_url": "https://github.com/example/plugin", "ref": "main"}, http.StatusOK)
	assertCapabilityOutputSchema(t, app, "plugins.github.preview", github)
	if github["source"].(map[string]any)["commit"] != commit {
		t.Fatal("preview omitted immutable commit")
	}
	ghInput := map[string]any{"repository_url": "https://github.com/example/plugin", "commit": commit, "expected_sha256": github["sha256"], "confirm": true, "ref": "main"}
	call(http.MethodPost, "/github/install", ghInput, http.StatusBadRequest)
	delete(ghInput, "ref")
	update := call(http.MethodPost, "/github/install", ghInput, http.StatusOK)
	assertCapabilityOutputSchema(t, app, "plugins.github.install", update)
	for _, ref := range refs[1:] {
		if ref != commit {
			t.Fatalf("install followed mutable ref %q", ref)
		}
	}
	revision := int64(update["version"].(map[string]any)["revision_id"].(float64))
	if update["installation"].(map[string]any)["active_revision_id"] != installation["active_revision_id"] {
		t.Fatal("update activated without confirmation")
	}
	activated := call(http.MethodPost, prefix+"/versions/activate", map[string]any{"revision_id": revision, "confirm": true}, http.StatusOK)
	assertCapabilityOutputSchema(t, app, "plugins.versions.activate", activated)
	if int64(activated["installation"].(map[string]any)["active_revision_id"].(float64)) != revision {
		t.Fatal("activation did not persist")
	}
	// The same registered handler accepts the canonical string IDs used by MCP.
	applied := applyAutomationChangesetResult(t, app, actor, "package-shared-config", automation.OperationRequest{Capability: "plugins.config.update", Input: json.RawMessage(fmt.Sprintf(`{"plugin_id":%q,"config":{"label":"mcp"}}`, itoa(id)))})
	if applied.Status != model.ChangesetSucceeded {
		t.Fatalf("shared Changeset: %s", applied.Status)
	}
	saved, err := db.GetPluginInstallation(t.Context(), id)
	if err != nil || !strings.Contains(string(saved.ConfigJSON), "mcp") {
		t.Fatalf("MCP mutation not applied: %+v %v", saved, err)
	}
	call(http.MethodPost, prefix+"/uninstall", map[string]any{"confirm": true}, http.StatusBadRequest)
	uninstalled := call(http.MethodPost, prefix+"/uninstall", map[string]any{"confirm": true, "keep_state": false}, http.StatusOK)
	assertCapabilityOutputSchema(t, app, "plugins.uninstall", uninstalled)
	saved, err = db.GetPluginInstallation(t.Context(), id)
	if err != nil || saved.Installed {
		t.Fatalf("uninstall not persisted: %+v %v", saved, err)
	}
}
