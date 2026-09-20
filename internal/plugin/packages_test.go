package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugingithub"
	"github.com/OboardProject/oboard/internal/pluginpackage"
	"github.com/OboardProject/oboard/internal/store"
)

func packageTestEnv(t *testing.T) (*store.Store, *Service, application.Principal) {
	t.Helper()
	db, svc, user := testPluginEnv(t)
	if err := db.MigratePluginExtensionsSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db, svc, adminActor(user)
}
func packageTestZIP(t *testing.T, version, source, ui string, capabilities ...string) []byte {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(testManifest(capabilities...), &fields); err != nil {
		t.Fatal(err)
	}
	fields["plugin_id"] = "example.inspector"
	fields["name"] = "Inspector"
	fields["description"] = "Inspect nodes"
	fields["version"] = version
	fields["config_schema"] = map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"label": map[string]any{"type": "string"}}}
	fields["secrets"] = []model.PluginSecretRef{{Name: "api_key", Purpose: "network"}}
	var uiBytes []byte
	if ui != "" {
		uiBytes = []byte(ui)
	}
	raw, err := pluginpackage.Build(MustJSON(fields), []byte(source), uiBytes)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func installTestPackage(t *testing.T, svc *Service, actor application.Principal, data []byte) model.PluginPackageInstallResult {
	t.Helper()
	preview, err := svc.PreviewPackage(context.Background(), actor, data)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := svc.InstallPackage(context.Background(), actor, data, preview.SHA256, true, PackageSource{Kind: "upload"})
	if err != nil {
		t.Fatal(err)
	}
	return installed
}
func TestPackageRevisionDigestBindsConfigSchemaAndUI(t *testing.T) {
	data := packageTestZIP(t, "1.0.0", "function main(){return {ok:true}}", `{"pages":[{"id":"overview","title":"Overview","components":[{"id":"status","type":"text","text":"Ready"}]}]}`)
	original, err := validatePackage(data)
	if err != nil {
		t.Fatal(err)
	}
	digest := RevisionDigest(string(original.archive.Source), original.manifest)
	if len(original.manifest.ConfigSchema) == 0 {
		t.Fatal("runtime manifest discarded config_schema")
	}
	for _, field := range []string{"config_schema", "ui", "source"} {
		t.Run(field, func(t *testing.T) {
			manifest := append([]byte(nil), original.archive.Manifest...)
			source := append([]byte(nil), original.archive.Source...)
			ui := append([]byte(nil), original.archive.UI...)
			switch field {
			case "config_schema":
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(manifest, &fields); err != nil {
					t.Fatal(err)
				}
				fields["config_schema"] = json.RawMessage(`{"type":"object","properties":{"other":{"type":"string"}},"additionalProperties":false}`)
				manifest = MustJSON(fields)
			case "ui":
				ui = []byte(strings.ReplaceAll(string(ui), "Ready", "Changed"))
			case "source":
				source = []byte("function main(){return {ok:false}}")
			}
			data, err := pluginpackage.Build(manifest, source, ui)
			if err != nil {
				t.Fatal(err)
			}
			next, err := validatePackage(data)
			if err != nil {
				t.Fatal(err)
			}
			if RevisionDigest(string(next.archive.Source), next.manifest) == digest {
				t.Fatalf("%s change reused the grant-bound digest", field)
			}
		})
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(original.archive.Manifest, &fields); err != nil {
		t.Fatal(err)
	}
	fields["ui_content_sha256"] = MustJSON(strings.Repeat("a", 64))
	if _, _, _, err := parsePackageManifest(MustJSON(fields)); err == nil {
		t.Fatal("package supplied a host-managed digest")
	}
	delete(fields, "ui_content_sha256")
	fields["config_schema"] = json.RawMessage(`{"type":"string"}`)
	if _, err := ParseManifest(MustJSON(fields)); err == nil {
		t.Fatal("runtime manifest accepted a non-object configuration schema")
	}
}

func TestPackageLifecycle(t *testing.T) {
	db, svc, actor := packageTestEnv(t)
	ctx := context.Background()
	ui := `{"pages":[{"id":"overview","title":"Overview","components":[{"id":"status","type":"text","text":"Ready"}]}]}`
	data := packageTestZIP(t, "1.0.0", "function main(){return {ok:true}}", ui)
	preview, err := svc.PreviewPackage(ctx, actor, data)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.SourceCompiled || !preview.HasUI || preview.ExistingPluginID != 0 {
		t.Fatalf("preview: %+v", preview)
	}
	for _, confirm := range []bool{false, true} {
		if _, err = svc.InstallPackage(ctx, actor, data, strings.Repeat("0", 64), confirm, PackageSource{}); err == nil {
			t.Fatal("missing confirmation/stale digest accepted")
		}
	}
	first := installTestPackage(t, svc, actor, data)
	id := first.Installation.PluginID
	item, err := db.GetPlugin(ctx, id)
	if err != nil || item.Status != model.PluginStatusDisabled {
		t.Fatalf("install enabled execution: %+v %v", item, err)
	}
	replay := installTestPackage(t, svc, actor, data)
	if replay.Version.RevisionID != first.Version.RevisionID {
		t.Fatal("replay created a second version")
	}
	versions, err := svc.ListPackageVersions(ctx, actor, id)
	if err != nil || len(versions) != 1 {
		t.Fatalf("versions: %+v %v", versions, err)
	}
	rev, err := db.GetPluginRevision(ctx, first.Version.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil || rev.SourceDigest != RevisionDigest(rev.Source, manifest) || manifest.UIContentSHA256 == "" || manifest.PluginID != "example.inspector" {
		t.Fatalf("revision not bound to complete runtime manifest: %+v %v", rev, err)
	}
	returnedUI, err := svc.GetPackageUI(ctx, actor, id)
	if err != nil || string(returnedUI) != ui {
		t.Fatalf("UI: %s %v", returnedUI, err)
	}
	if _, err = svc.UpdatePackageConfig(ctx, actor, id, json.RawMessage(`{"api_key":"secret"}`)); err == nil {
		t.Fatal("secret accepted in ordinary config")
	}
	if _, err = svc.UpdatePackageConfig(ctx, actor, id, json.RawMessage(`{"label":1}`)); err == nil {
		t.Fatal("invalid config type accepted")
	}
	if _, err = svc.UpdatePackageConfig(ctx, actor, id, json.RawMessage(`{"label":"fleet"}`)); err != nil {
		t.Fatal(err)
	}
	if err = svc.SetPackageSecretEncrypted(ctx, actor, id, "api_key", "ciphertext"); err != nil {
		t.Fatal(err)
	}
	if err = svc.SetPackageSecretEncrypted(ctx, actor, id, "undeclared", "ciphertext"); err == nil {
		t.Fatal("undeclared secret accepted")
	}
	value, err := db.GetPluginInstallationSecretEncrypted(ctx, id, "api_key")
	if err != nil || value != "ciphertext" {
		t.Fatal("secret not stored")
	}
	secondData := packageTestZIP(t, "1.1.0", "function main(){return {ok:true}}", ui, model.PluginSDKServersStatus, model.PluginSDKStateGet)
	second := installTestPackage(t, svc, actor, secondData)
	if second.Installation.ActiveRevisionID != first.Version.RevisionID || len(second.Capabilities.Added) != 1 {
		t.Fatal("update silently activated or diff missing")
	}
	nextRev, err := db.GetPluginRevision(ctx, second.Version.RevisionID)
	if err != nil || nextRev.SourceDigest == rev.SourceDigest {
		t.Fatal("permission/version change reused revision digest")
	}
	if _, err = svc.ActivatePackageVersion(ctx, actor, id, second.Version.RevisionID, false); err == nil {
		t.Fatal("activation without confirm")
	}
	active, err := svc.ActivatePackageVersion(ctx, actor, id, second.Version.RevisionID, true)
	if err != nil || active.ActiveRevisionID != second.Version.RevisionID {
		t.Fatalf("activate: %+v %v", active, err)
	}
	changed := packageTestZIP(t, "1.1.0", "function main(){return 9}", ui)
	changedPreview, err := svc.PreviewPackage(ctx, actor, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.InstallPackage(ctx, actor, changed, changedPreview.SHA256, true, PackageSource{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable version changed: %v", err)
	}
	if _, err = db.CompareAndSetPluginState(ctx, id, "saved", 0, json.RawMessage(`{"value":1}`)); err != nil {
		t.Fatal(err)
	}
	if err = svc.UninstallPackage(ctx, actor, id, true, false); err == nil {
		t.Fatal("uninstall without confirm")
	}
	if err = svc.UninstallPackage(ctx, actor, id, true, true); err != nil {
		t.Fatal(err)
	}
	installed, err := db.GetPluginInstallation(ctx, id)
	if err != nil || installed.Installed || installed.ActiveRevisionID != 0 {
		t.Fatalf("uninstall: %+v %v", installed, err)
	}
	if _, err = svc.GetPackageUI(ctx, actor, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("uninstalled UI still served")
	}
	if _, err = db.GetPluginInstallationSecretEncrypted(ctx, id, "api_key"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("uninstall retained secret")
	}
	if _, err = db.GetPluginState(ctx, id, "saved"); err != nil {
		t.Fatal("keep_state erased state")
	}
	reinstalled := installTestPackage(t, svc, actor, data)
	if reinstalled.Installation.PluginID != id || !reinstalled.Installation.Installed {
		t.Fatal("reinstall lost identity")
	}
	item, _ = db.GetPlugin(ctx, id)
	if item.Status != model.PluginStatusDisabled {
		t.Fatal("reinstall enabled runtime")
	}
	if err = svc.UninstallPackage(ctx, actor, id, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err = db.GetPluginState(ctx, id, "saved"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("uninstall failed to erase state")
	}
}

func TestPackageValidationAndAuthorization(t *testing.T) {
	_, svc, actor := packageTestEnv(t)
	ctx := context.Background()
	data := packageTestZIP(t, "1.0.0", "throw new Error('must not execute'); function main(){}", "")
	installed := installTestPackage(t, svc, actor, data)
	for _, bad := range []struct{ source, ui string }{{"function main(}", ""}, {"function main(){}", `{"pages":[],"html":"<script>"}`}} {
		if _, err := svc.PreviewPackage(ctx, actor, packageTestZIP(t, "2.0.0", bad.source, bad.ui)); err == nil {
			t.Fatal("invalid executable/UI accepted")
		}
	}
	restricted := actor
	restricted.ResourceFilter = json.RawMessage(`{"plugin_ids":[999]}`)
	if _, err := svc.ListPackageVersions(ctx, restricted, installed.Installation.PluginID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("cross-resource read: %v", err)
	}
	machine := actor
	machine.Interactive = false
	machine.Scopes = nil
	if _, err := svc.PreviewPackage(ctx, machine, data); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("machine scope bypass")
	}
	machine.Scopes = []string{"plugins:*"}
	if err := svc.SetPackageSecretEncrypted(ctx, machine, installed.Installation.PluginID, "api_key", "ciphertext"); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("machine secret write")
	}
	self := actor
	self.Type = model.APIPrincipalPlugin
	if _, err := svc.PreviewPackage(ctx, self, data); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal("plugin principal management bypass")
	}
}

func TestPackageGitHubConfirmationPinsCommit(t *testing.T) {
	_, svc, actor := packageTestEnv(t)
	ctx := context.Background()
	data := packageTestZIP(t, "1.0.0", "function main(){}", "")
	commit := strings.Repeat("a", 40)
	calls := []string{}
	fetch := func(_ context.Context, repository, ref string) (plugingithub.Result, error) {
		if repository != "https://github.com/example/plugin" {
			t.Fatal("repository changed")
		}
		calls = append(calls, ref)
		return plugingithub.Result{Archive: data, Source: plugingithub.Source{Owner: "example", Repo: "plugin", Commit: commit}}, nil
	}
	preview, err := svc.previewGitHubPackage(ctx, actor, "https://github.com/example/plugin", "main", fetch)
	if err != nil || preview.Source == nil || preview.Source.Commit != commit {
		t.Fatalf("preview provenance: %+v %v", preview, err)
	}
	if _, err = svc.installGitHubPackage(ctx, actor, "https://github.com/example/plugin", "main", preview.SHA256, true, fetch); err == nil {
		t.Fatal("mutable ref accepted for installation")
	}
	if _, err = svc.installGitHubPackage(ctx, actor, "https://github.com/example/plugin", commit, strings.Repeat("0", 64), true, fetch); !errors.Is(err, ErrConflict) {
		t.Fatal("unexpected content accepted")
	}
	result, err := svc.installGitHubPackage(ctx, actor, "https://github.com/example/plugin", commit, preview.SHA256, true, fetch)
	if err != nil || result.Version.SourceCommit != commit || result.Version.SourceRepository != "example/plugin" {
		t.Fatalf("install provenance: %+v %v", result, err)
	}
	if len(calls) != 3 || calls[0] != "main" || calls[1] != commit || calls[2] != commit {
		t.Fatalf("fetch refs: %v", calls)
	}
	mismatch := func(ctx context.Context, url, ref string) (plugingithub.Result, error) {
		result, _ := fetch(ctx, url, ref)
		result.Source.Commit = strings.Repeat("b", 40)
		return result, nil
	}
	if _, err = svc.installGitHubPackage(ctx, actor, "https://github.com/example/plugin", commit, preview.SHA256, true, mismatch); !errors.Is(err, ErrConflict) {
		t.Fatal("resolved different commit accepted")
	}
}
