package plugin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

func TestConfigSDKReadsOnlyInstalledPluginConfiguration(t *testing.T) {
	db, svc, actor := packageTestEnv(t)
	ctx := context.Background()
	installed := installTestPackage(t, svc, actor, packageTestZIP(t, "1.0.0", "function main(){return oboard.config.get({}).result}", "", SDKConfigGet))
	_, err := svc.UpdatePackageConfig(ctx, actor, installed.Installation.PluginID, json.RawMessage(`{"label":"first"}`))
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(db, &recordingHost{})
	run := model.PluginRun{PluginID: installed.Installation.PluginID}
	result, err := gateway.dispatch(ctx, application.Principal{}, run, model.PluginRunAction{Capability: SDKConfigGet}, sdkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err := json.Unmarshal(result["config"].(json.RawMessage), &config); err != nil || config["label"] != "first" {
		t.Fatalf("config not delivered: %v", err)
	}
	if _, err := gateway.dispatch(ctx, application.Principal{}, model.PluginRun{PluginID: 999999}, model.PluginRunAction{Capability: SDKConfigGet}, sdkArgs{}); err == nil {
		t.Fatal("missing plugin configuration allowed")
	}
	if err := svc.UninstallPackage(ctx, actor, run.PluginID, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.dispatch(ctx, application.Principal{}, run, model.PluginRunAction{Capability: SDKConfigGet}, sdkArgs{}); err == nil {
		t.Fatal("uninstalled plugin configuration allowed")
	}
}
