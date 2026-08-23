package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestNodePresetAutomationLifecycle(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "root", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	adminPrincipal := userAutomationPrincipal(t, db, admin.ID)
	createInput := json.RawMessage(`{"node_preset":{"name":"机房 Reality","kind":"vless-reality","config_json":{"tls":{"server_name":"www.cloudflare.com"}}}}`)
	applyAutomationChangeset(t, server, adminPrincipal, "node-preset-create", automation.OperationRequest{Capability: "node_presets.create", Input: createInput})
	presets, err := db.ListNodePresets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var preset *model.NodePreset
	for index := range presets {
		if presets[index].Name == "机房 Reality" {
			preset = &presets[index]
			break
		}
	}
	if preset == nil || preset.Kind != "vless-reality" || preset.Protocol != "vless" {
		t.Fatalf("created preset missing: %#v", presets)
	}
	if !jsonStringContains(preset.ConfigJSON, `"www.cloudflare.com"`) || !jsonStringContains(preset.ConfigJSON, `"xtls-rprx-vision"`) {
		t.Fatalf("preset did not merge defaults: %s", preset.ConfigJSON)
	}
	updateInput, _ := json.Marshal(map[string]any{"node_preset_id": preset.ID, "changes": map[string]any{"remark": "自定义握手", "default_port": 8443}})
	applyAutomationChangeset(t, server, adminPrincipal, "node-preset-update", automation.OperationRequest{Capability: "node_presets.update", Input: updateInput})
	after, err := db.GetNodePreset(ctx, preset.ID)
	if err != nil || after.Remark != "自定义握手" || after.DefaultPort != 8443 {
		t.Fatalf("preset after update: %#v err=%v", after, err)
	}
	deleteInput, _ := json.Marshal(map[string]any{"node_preset_id": preset.ID, "confirm": true})
	applyAutomationChangeset(t, server, adminPrincipal, "node-preset-delete", automation.OperationRequest{Capability: "node_presets.delete", Input: deleteInput})
	if _, err := db.GetNodePreset(ctx, preset.ID); err == nil {
		t.Fatal("preset not deleted")
	}
}

func jsonStringContains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
