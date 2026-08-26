package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestAnyTLSPaddingRESTCreatePersistsAndExplicitlyRegenerates(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	node := &model.Server{Name: "anytls-edge", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	create := func(name string, port int, explicit bool) *model.Inbound {
		payload := map[string]any{
			"server_id": node.ID, "name": name, "kind": "anytls-basic", "listen_ip": "0.0.0.0", "port": port,
			"certificate_mode": "external", "config_json": `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"}}`,
			"enabled": true,
		}
		if explicit {
			payload["anytls_padding"] = map[string]any{"preset_id": "balanced_v1", "auto_tune": true}
		}
		response := request(t, handler, http.MethodPost, "/api/v1/ui/inbounds", token, payload, http.StatusCreated)
		encoded, _ := json.Marshal(response["inbound"])
		var inbound model.Inbound
		if err := json.Unmarshal(encoded, &inbound); err != nil {
			t.Fatal(err)
		}
		return &inbound
	}
	first := create("AnyTLS A", 10443, false)
	second := create("AnyTLS B", 10444, true)
	firstMeta, firstScheme, _ := core.AnyTLSPaddingMetadataFromJSON(first.ConfigJSON)
	secondMeta, _, _ := core.AnyTLSPaddingMetadataFromJSON(second.ConfigJSON)
	if firstMeta == nil || firstMeta.Mode != core.AnyTLSPaddingModeTuned || firstMeta.PresetID != core.AnyTLSPaddingBalancedV1 {
		t.Fatalf("first metadata = %#v", firstMeta)
	}
	if firstMeta.Fingerprint == secondMeta.Fingerprint {
		t.Fatalf("two inbounds shared fingerprint %s", firstMeta.Fingerprint)
	}

	patchConfig := `{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem","server_name":"changed.example"},"padding_scheme":["stop=1","0=1-2"]}`
	updated := request(t, handler, http.MethodPatch, fmt.Sprintf("/api/v1/ui/inbounds/%d", first.ID), token, map[string]any{"name": "renamed", "port": 10543, "config_json": patchConfig}, http.StatusOK)
	updatedJSON, _ := json.Marshal(updated["inbound"])
	var updatedInbound model.Inbound
	_ = json.Unmarshal(updatedJSON, &updatedInbound)
	updatedMeta, updatedScheme, _ := core.AnyTLSPaddingMetadataFromJSON(updatedInbound.ConfigJSON)
	if updatedMeta.Fingerprint != firstMeta.Fingerprint || strings.Join(updatedScheme, "\n") != strings.Join(firstScheme, "\n") {
		t.Fatalf("ordinary update changed padding: %s", updatedInbound.ConfigJSON)
	}

	regenerated := request(t, handler, http.MethodPost, fmt.Sprintf("/api/v1/ui/inbounds/%d/padding", first.ID), token, map[string]any{"operation": "regenerate"}, http.StatusOK)
	regeneratedJSON, _ := json.Marshal(regenerated["inbound"])
	var regeneratedInbound model.Inbound
	_ = json.Unmarshal(regeneratedJSON, &regeneratedInbound)
	regeneratedMeta, _, _ := core.AnyTLSPaddingMetadataFromJSON(regeneratedInbound.ConfigJSON)
	if regeneratedMeta.Generation != 2 || regeneratedMeta.Fingerprint == firstMeta.Fingerprint {
		t.Fatalf("regenerated metadata = %#v", regeneratedMeta)
	}
	adapter, _ := core.AdapterFor(model.ProtocolAnyTLS)
	projected, err := adapter.Inbound(regeneratedInbound, []model.User{{Username: "user", ProxyPassword: "password"}})
	if err != nil {
		t.Fatal(err)
	}
	encodedProjected, _ := json.Marshal(projected)
	if !strings.Contains(string(encodedProjected), `"padding_scheme"`) || strings.Contains(string(encodedProjected), "_oboard_padding") {
		t.Fatalf("data-plane projection = %s", encodedProjected)
	}
	projectedAgain, err := adapter.Inbound(regeneratedInbound, []model.User{{Username: "user", ProxyPassword: "password"}})
	if err != nil {
		t.Fatal(err)
	}
	encodedAgain, _ := json.Marshal(projectedAgain)
	if string(encodedAgain) != string(encodedProjected) {
		t.Fatalf("repeated config generation changed padding: %s != %s", encodedAgain, encodedProjected)
	}
	subscriptionNode, err := adapter.SubscriptionNode(model.User{Username: "user", ProxyPassword: "password"}, regeneratedInbound, *node)
	if err != nil {
		t.Fatal(err)
	}
	encodedSubscription, _ := json.Marshal(subscriptionNode)
	if strings.Contains(string(encodedSubscription), "padding_scheme") || strings.Contains(string(encodedSubscription), "_oboard_padding") {
		t.Fatalf("subscription leaked padding metadata: %s", encodedSubscription)
	}
}

func TestAnyTLSPaddingAutomationCreateAndOperationShareApplicationPath(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	node := &model.Server{Name: "entry", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	autoTune := false
	presets, _ := db.ListNodePresets(ctx)
	var templateID int64
	for _, preset := range presets {
		if preset.Kind == "anytls-basic" {
			templateID = preset.ID
			break
		}
	}
	createInput, _ := json.Marshal(map[string]any{"inbound": map[string]any{
		"server_id": node.ID, "name": "AnyTLS light", "kind": "anytls-basic", "listen_ip": "0.0.0.0", "port": 10443,
		"config_json":      fmt.Sprintf(`{"tls":{"enabled":true,"certificate_path":"/tmp/cert.pem","key_path":"/tmp/key.pem"},"node_preset_id":%d}`, templateID),
		"certificate_mode": "external", "anytls_padding": map[string]any{"preset_id": "light_v1", "auto_tune": autoTune}, "enabled": true,
	}})
	applyAutomationChangeset(t, server, principal, "anytls-create", automation.OperationRequest{Capability: "inbounds.create", Input: createInput})
	items, _ := db.ListInbounds(ctx)
	if len(items) != 1 {
		t.Fatalf("inbounds = %#v", items)
	}
	metadata, scheme, _ := core.AnyTLSPaddingMetadataFromJSON(items[0].ConfigJSON)
	if metadata.Mode != core.AnyTLSPaddingModePreset || strings.Join(scheme, "\n") != strings.Join(core.AnyTLSLightPaddingScheme(), "\n") {
		t.Fatalf("automation create = %#v %#v", metadata, scheme)
	}
	operationInput, _ := json.Marshal(map[string]any{"inbound_id": items[0].ID, "operation": "replace_preset", "preset_id": "balanced_v1", "auto_tune": true})
	applyAutomationChangeset(t, server, principal, "anytls-replace", automation.OperationRequest{Capability: "inbounds.padding.update", Input: operationInput})
	stored, _ := db.GetInbound(ctx, items[0].ID)
	replaced, _, _ := core.AnyTLSPaddingMetadataFromJSON(stored.ConfigJSON)
	if replaced.Mode != core.AnyTLSPaddingModeTuned || replaced.PresetID != core.AnyTLSPaddingBalancedV1 || replaced.Generation != 2 {
		t.Fatalf("automation replace = %#v", replaced)
	}
}
