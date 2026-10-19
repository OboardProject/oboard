package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

// TestServersUpdateStoresBBROverride verifies that MCP servers.update stores a
// per-server BBR install override and that a non-cake override drops the
// cake bandwidth.
func TestServersUpdateStoresBBROverride(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "bbr-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, user.ID)
	server := &model.Server{Name: "bbr-node", AgentID: "bbr-agent", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	update, _ := json.Marshal(map[string]any{"server_id": server.ID, "changes": map[string]any{"bbr_qdisc": "cake", "bbr_cake_bandwidth_mbit": 300}})
	applyAutomationChangeset(t, srv, principal, "bbr-override", automation.OperationRequest{Capability: "servers.update", Input: update})
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BBRQdisc != "cake" || stored.BBRCakeBandwidthMbit != 300 {
		t.Fatalf("override not stored: %q/%d", stored.BBRQdisc, stored.BBRCakeBandwidthMbit)
	}
	update, _ = json.Marshal(map[string]any{"server_id": server.ID, "changes": map[string]any{"bbr_qdisc": "fq_pie"}})
	applyAutomationChangeset(t, srv, principal, "bbr-override-fq-pie", automation.OperationRequest{Capability: "servers.update", Input: update})
	stored, err = db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.BBRQdisc != "fq_pie" || stored.BBRCakeBandwidthMbit != 0 {
		t.Fatalf("non-cake override kept a bandwidth: %q/%d", stored.BBRQdisc, stored.BBRCakeBandwidthMbit)
	}
}
