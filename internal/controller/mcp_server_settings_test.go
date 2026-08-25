package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestMCPServerSettingsRoundTripThroughChangeset(t *testing.T) {
	const trafficLimitBytes int64 = 578 * 1024 * 1024 * 1024
	const trafficUsedBytes int64 = 123456789

	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111113", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBootstrapAdmin(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)

	onboard, _ := json.Marshal(map[string]any{
		"server": map[string]any{
			"name":                      "mcp-settings",
			"listen_ip":                 "0.0.0.0",
			"entry_ip_mode":             "custom",
			"entry_address":             "203.0.113.1",
			"region_mode":               "manual",
			"region_code":               "JP",
			"port_range_start":          12000,
			"port_range_end":            13000,
			"internal_port_range_start": 40000,
			"internal_port_range_end":   45000,
			"connection_audit_enabled":  false,
			"time_correction_mode":      "auto",
			"offline_notify_enabled":    false,
			"offline_after_seconds":     120,
			"expires_at":                "2027-01-02T03:04:05Z",
			"auto_renew_enabled":        true,
			"renewal_cycle":             "quarterly",
			"expiry_notify_enabled":     false,
		},
		"issue_enrollment_token": false,
	})
	applyAutomationChangeset(t, srv, principal, "onboard-settings", automation.OperationRequest{Capability: "servers.onboard", Input: onboard})

	servers, err := db.ListServers(ctx)
	if err != nil || len(servers) != 1 {
		t.Fatalf("servers=%#v err=%v", servers, err)
	}
	stored := servers[0]
	expiry, _ := time.Parse(time.RFC3339Nano, "2027-01-02T03:04:05Z")
	assertStoredServerSettings(t, stored, 12000, 13000, 40000, 45000, expiry, model.ServerRenewalCycleQuarterly, true, false)

	dto, err := srv.application.GetServer(ctx, principal, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertServerDTOSettings(t, dto, 12000, 13000, 40000, 45000, expiry, model.ServerRenewalCycleQuarterly, true, false)
	assertCapabilityOutputSchema(t, srv, "servers.get", dto)

	update, _ := json.Marshal(map[string]any{
		"server_id": stored.ID,
		"changes": map[string]any{
			"port_range_start":          14000,
			"port_range_end":            15000,
			"internal_port_range_start": 50000,
			"internal_port_range_end":   55000,
			"expires_at":                "2028-03-04T05:06:07Z",
			"renewal_cycle":             "monthly",
			"auto_renew_enabled":        false,
			"traffic_limit_bytes":       trafficLimitBytes,
			"traffic_used_bytes":        trafficUsedBytes,
		},
	})
	applyAutomationChangeset(t, srv, principal, "update-settings", automation.OperationRequest{Capability: "servers.update", Input: update})

	updated, err := db.GetServer(ctx, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	nextExpiry, _ := time.Parse(time.RFC3339Nano, "2028-03-04T05:06:07Z")
	assertStoredServerSettings(t, *updated, 14000, 15000, 50000, 55000, nextExpiry, model.ServerRenewalCycleMonthly, false, false)
	if updated.TrafficLimitBytes != trafficLimitBytes {
		t.Fatalf("stored traffic_limit_bytes = %d, want %d", updated.TrafficLimitBytes, trafficLimitBytes)
	}
	if used := updated.TrafficUploadBytes + updated.TrafficDownloadBytes; used != uint64(trafficUsedBytes) {
		t.Fatalf("stored traffic used = %d, want %d", used, trafficUsedBytes)
	}

	updatedDTO, err := srv.application.GetServer(ctx, principal, stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertServerDTOSettings(t, updatedDTO, 14000, 15000, 50000, 55000, nextExpiry, model.ServerRenewalCycleMonthly, false, false)
	assertCapabilityOutputSchema(t, srv, "servers.get", updatedDTO)

	onboardDescriptor, ok := srv.capabilities.Get("servers.onboard")
	if !ok {
		t.Fatal("servers.onboard capability missing")
	}
	serverDescriptor, ok := srv.capabilities.Get("servers.get")
	if !ok {
		t.Fatal("servers.get capability missing")
	}
	for _, field := range []string{`"port_range_start"`, `"internal_port_range_start"`, `"expires_at"`, `"renewal_cycle"`} {
		if !strings.Contains(string(onboardDescriptor.InputSchema), field) {
			t.Fatalf("servers.onboard input schema missing %s", field)
		}
		if !strings.Contains(string(serverDescriptor.OutputSchema), field) {
			t.Fatalf("servers.get output schema missing %s", field)
		}
	}
}

func assertStoredServerSettings(t *testing.T, server model.Server, portStart, portEnd, internalStart, internalEnd int, expiresAt time.Time, cycle model.ServerRenewalCycle, autoRenew, expiryNotify bool) {
	t.Helper()
	if server.PortRangeStart != portStart || server.PortRangeEnd != portEnd || server.InternalPortRangeStart != internalStart || server.InternalPortRangeEnd != internalEnd {
		t.Fatalf("stored port ranges = %d-%d / %d-%d", server.PortRangeStart, server.PortRangeEnd, server.InternalPortRangeStart, server.InternalPortRangeEnd)
	}
	if server.ExpiresAt == nil || !server.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("stored expires_at = %v, want %v", server.ExpiresAt, expiresAt)
	}
	if server.RenewalCycle != cycle || server.AutoRenewEnabled != autoRenew || server.ExpiryNotifyEnabled != expiryNotify {
		t.Fatalf("stored expiry settings = cycle:%s auto_renew:%v expiry_notify:%v", server.RenewalCycle, server.AutoRenewEnabled, server.ExpiryNotifyEnabled)
	}
	if server.ListenIP != "0.0.0.0" || server.EntryIPMode != model.EntryIPModeCustom || server.RegionMode != "manual" || server.RegionCode != "JP" {
		t.Fatalf("stored addressing settings = %#v", server)
	}
	if server.TimeCorrectionMode != model.TimeCorrectionAuto || server.OfflineNotifyEnabled || server.OfflineAfterSeconds != 120 || server.ConnectionAuditEnabled {
		t.Fatalf("stored operational settings = %#v", server)
	}
}

func assertServerDTOSettings(t *testing.T, server application.ServerDTO, portStart, portEnd, internalStart, internalEnd int, expiresAt time.Time, cycle model.ServerRenewalCycle, autoRenew, expiryNotify bool) {
	t.Helper()
	if server.PortRangeStart != portStart || server.PortRangeEnd != portEnd || server.InternalPortRangeStart != internalStart || server.InternalPortRangeEnd != internalEnd {
		t.Fatalf("DTO port ranges = %d-%d / %d-%d", server.PortRangeStart, server.PortRangeEnd, server.InternalPortRangeStart, server.InternalPortRangeEnd)
	}
	if server.ExpiresAt == nil || !server.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("DTO expires_at = %v, want %v", server.ExpiresAt, expiresAt)
	}
	if server.RenewalCycle != cycle || server.AutoRenewEnabled != autoRenew || server.ExpiryNotifyEnabled != expiryNotify {
		t.Fatalf("DTO expiry settings = cycle:%s auto_renew:%v expiry_notify:%v", server.RenewalCycle, server.AutoRenewEnabled, server.ExpiryNotifyEnabled)
	}
	if server.ListenIP != "0.0.0.0" || server.EntryIPMode != model.EntryIPModeCustom || server.RegionMode != "manual" || server.RegionCode != "JP" {
		t.Fatalf("DTO addressing settings = %#v", server)
	}
	if server.TimeCorrectionMode != model.TimeCorrectionAuto || server.OfflineNotifyEnabled || server.OfflineAfterSeconds != 120 || server.ConnectionAuditEnabled {
		t.Fatalf("DTO operational settings = %#v", server)
	}
}
