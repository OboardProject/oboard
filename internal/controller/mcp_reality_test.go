package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// TestMCPVLESSRealityInboundMatchesPanelPreset verifies that creating a VLESS
// Reality inbound through the automation layer with structured, non-secret
// input yields exactly the same stored shape as the panel's vless-reality
// preset: Vision flow, tls.enabled, handshake server_port, and a complete
// Controller-generated keypair.
func TestMCPVLESSRealityInboundMatchesPanelPreset(t *testing.T) {
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
	// The MCP recipe injects the panel preset without a keypair; the panel
	// flow also saves an empty keypair and lets the Controller generate it.
	createInput, _ := json.Marshal(map[string]any{"inbound": map[string]any{
		"server_id": node.ID, "name": "VLESS-Reality", "kind": "vless-reality",
		"listen_ip": "0.0.0.0", "port": 10443,
		"reality": map[string]any{"handshake_server": "gateway.icloud.com", "handshake_port": 443}, "enabled": true,
	}})
	applyAutomationChangeset(t, server, principal, "vless-reality-create", automation.OperationRequest{Capability: "inbounds.create", Input: createInput})

	inbounds, err := db.ListInbounds(ctx)
	if err != nil || len(inbounds) != 1 {
		t.Fatalf("inbounds=%#v err=%v", inbounds, err)
	}
	inbound := inbounds[0]
	var cfg map[string]any
	if err := json.Unmarshal([]byte(inbound.ConfigJSON), &cfg); err != nil {
		t.Fatalf("stored config is not valid JSON: %v", err)
	}
	tls, ok := cfg["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls missing: %s", inbound.ConfigJSON)
	}
	if enabled, _ := tls["enabled"].(bool); !enabled {
		t.Fatalf("tls.enabled missing or false: %s", inbound.ConfigJSON)
	}
	if flow, _ := cfg["flow"].(string); flow != "xtls-rprx-vision" {
		t.Fatalf("flow = %q, want xtls-rprx-vision", flow)
	}
	reality, ok := tls["reality"].(map[string]any)
	if !ok {
		t.Fatalf("reality missing: %s", inbound.ConfigJSON)
	}
	handshake, ok := reality["handshake"].(map[string]any)
	if !ok {
		t.Fatalf("handshake missing: %s", inbound.ConfigJSON)
	}
	if port, _ := handshake["server_port"].(float64); int(port) != 443 {
		t.Fatalf("handshake.server_port = %v, want 443", handshake["server_port"])
	}
	privateKey, _ := reality["private_key"].(string)
	publicKey, _ := reality["public_key"].(string)
	shortID, _ := reality["short_id"].(string)
	if privateKey == "" || publicKey == "" || shortID == "" {
		t.Fatalf("reality keypair incomplete: %s", inbound.ConfigJSON)
	}
	derived, err := deriveRealityPublicKey(privateKey)
	if err != nil || derived != publicKey {
		t.Fatalf("public_key does not match private_key: derived=%q stored=%q", derived, publicKey)
	}
	if _, err := db.GetUserByUsername(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	// The subscription-facing projection must keep the Vision flow and the
	// Reality public key, and never leak the private key.
	adapter, err := core.AdapterFor(inbound.Protocol)
	if err != nil {
		t.Fatal(err)
	}
	nodeView, err := adapter.SubscriptionNode(*admin, inbound, *node)
	if err != nil {
		t.Fatalf("subscription node: %v", err)
	}
	encoded, _ := json.Marshal(nodeView)
	payload := string(encoded)
	if !contains([]byte(payload), "xtls-rprx-vision") {
		t.Fatalf("subscription node lost the Vision flow: %s", payload)
	}
	if !contains([]byte(payload), "public_key") {
		t.Fatalf("subscription node lost the Reality public key: %s", payload)
	}
	if contains([]byte(payload), "private_key") {
		t.Fatalf("subscription node leaked the private key: %s", payload)
	}
}

// TestMCPPartialRealityConfigIsCompleted verifies that a partial reality
// config (as produced by MCP when the user only describes Reality) is
// completed server-side exactly like the panel save path.
func TestMCPPartialRealityConfigIsCompleted(t *testing.T) {
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
	createInput, _ := json.Marshal(map[string]any{"inbound": map[string]any{
		"server_id": node.ID, "name": "VLESS-Reality", "kind": "vless-reality",
		"listen_ip": "0.0.0.0", "port": 10444,
		"reality": map[string]any{"handshake_server": "cdn.icloud-content.com"}, "enabled": true,
	}})
	applyAutomationChangeset(t, server, principal, "vless-reality-partial", automation.OperationRequest{Capability: "inbounds.create", Input: createInput})
	inbounds, err := db.ListInbounds(ctx)
	if err != nil || len(inbounds) != 1 {
		t.Fatalf("inbounds=%#v err=%v", inbounds, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(inbounds[0].ConfigJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	tls := cfg["tls"].(map[string]any)
	if enabled, _ := tls["enabled"].(bool); !enabled {
		t.Fatalf("tls.enabled was not completed: %s", inbounds[0].ConfigJSON)
	}
	if flow, _ := cfg["flow"].(string); flow != "xtls-rprx-vision" {
		t.Fatalf("flow was not completed: %s", inbounds[0].ConfigJSON)
	}
	handshake := tls["reality"].(map[string]any)["handshake"].(map[string]any)
	if port, _ := handshake["server_port"].(float64); int(port) != 443 {
		t.Fatalf("handshake.server_port was not completed: %s", inbounds[0].ConfigJSON)
	}
}

func TestMCPRealityUnknownFieldFailsBeforeChangesetSave(t *testing.T) {
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
	input, _ := json.Marshal(map[string]any{"inbound": map[string]any{
		"server_id": node.ID, "name": "Invalid Reality", "kind": "vless-reality",
		"listen_ip": "0.0.0.0", "port": 10445,
		"config_json": `{"tls":{"reality":{"enabled":true,"dest":"gateway.icloud.com:443"}}}`,
		"enabled":     true,
	}})
	_, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "inbounds.create", Input: input}}})
	if err == nil || !strings.Contains(err.Error(), "config_json.tls.reality.dest: unsupported field") {
		t.Fatalf("error = %v, want precise Reality field path", err)
	}
	inbounds, listErr := db.ListInbounds(ctx)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(inbounds) != 0 {
		t.Fatalf("invalid MCP inbound was persisted: %#v", inbounds)
	}
}
