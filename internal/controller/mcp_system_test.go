package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestSettingsCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	updateInput, _ := json.Marshal(map[string]any{"changes": map[string]any{"audit_enabled": false, "traffic_timezone": "Asia/Tokyo", "traffic_enforcement_mode": "reject_new"}})
	applyAutomationChangeset(t, server, principal, "settings-update", automation.OperationRequest{Capability: "settings.update", Input: updateInput})
	settings, err := db.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["audit_enabled"] != "false" || settings["traffic_timezone"] != "Asia/Tokyo" || settings["traffic_enforcement_mode"] != "reject_new" {
		t.Fatalf("settings not applied: %#v", settings)
	}
	payload, err := server.readSystemResource(ctx, principal, "settings")
	if err != nil {
		t.Fatalf("settings.get: %v", err)
	}
	encoded, _ := json.Marshal(payload)
	if len(encoded) == 0 || string(encoded) == "{}" {
		t.Fatalf("empty settings payload")
	}
}

func TestApprovalPolicyAndNotificationCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	serviceAccount, err := server.newServicePrincipal(*admin, "MCP 自动化", []string{"servers:*"}, json.RawMessage(`{}`), nil, 60, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAPIPrincipal(ctx, serviceAccount); err != nil {
		t.Fatal(err)
	}
	policyInput, _ := json.Marshal(map[string]any{"principal_id": serviceAccount.ID, "capability": "servers.update", "mode": "automatic"})
	applyAutomationChangeset(t, server, principal, "policy-set", automation.OperationRequest{Capability: "approval_policies.set", Input: policyInput})
	policies, err := db.ListApprovalPolicies(ctx, serviceAccount.ID)
	if err != nil || len(policies) != 1 {
		t.Fatalf("policies=%#v err=%v", policies, err)
	}
	if policies[0].Mode != model.ApprovalAutomatic || policies[0].Capability != "servers.update" {
		t.Fatalf("unexpected policy: %#v", policies[0])
	}
	channelInput := json.RawMessage(`{"notification_channel":{"name":"测试频道","type":"bark","enabled":true,"events":"server_offline","config_json":"{\"device_key\":\"test-device-key\",\"group\":\"测试\"}","user_ids":[]}}`)
	applyAutomationChangeset(t, server, principal, "channel-create", automation.OperationRequest{Capability: "notification_channels.create", Input: channelInput})
	channels, err := db.ListNotificationChannelsByOwner(ctx, admin.ID)
	if err != nil || len(channels) != 1 {
		t.Fatalf("channels=%#v err=%v", channels, err)
	}
	channel := channels[0]
	if channel.Type != "bark" || !channel.Enabled {
		t.Fatalf("unexpected channel: %#v", channel)
	}
	updateInput, _ := json.Marshal(map[string]any{"channel_id": channel.ID, "changes": map[string]any{"enabled": false}})
	applyAutomationChangeset(t, server, principal, "channel-update", automation.OperationRequest{Capability: "notification_channels.update", Input: updateInput})
	updated, err := db.GetNotificationChannel(ctx, channel.ID)
	if err != nil || updated.Enabled {
		t.Fatalf("channel not updated: %#v err=%v", updated, err)
	}
	deleteInput, _ := json.Marshal(map[string]any{"channel_id": channel.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "channel-delete", automation.OperationRequest{Capability: "notification_channels.delete", Input: deleteInput})
	if _, err := db.GetNotificationChannel(ctx, channel.ID); err == nil {
		t.Fatal("deleted channel still exists")
	}
}

func (s *Server) readSystemResource(ctx context.Context, principal application.Principal, name string) (any, error) {
	switch name {
	case "settings":
		items, err := s.store.ListSettings(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"settings": s.publicSettings(ctx, items)}, nil
	default:
		return nil, nil
	}
}
