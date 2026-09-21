package controller

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func automationAdminPrincipal(t *testing.T, db *store.Store) application.Principal {
	t.Helper()
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	return application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
}

func unusedStealthAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestStealthTransportSettingsLifecycle(t *testing.T) {
	t.Setenv("OBOARD_STEALTH_ADDR", "")
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	defer srv.Close()
	certRoot := filepath.Join(t.TempDir(), "controller.sqlite")
	srv.ConfigureStealthTransport(certRoot)
	if err := srv.StartStealthTransport(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := stealthTransportConfig{Enabled: true, ListenAddress: unusedStealthAddress(t), PublicAddress: "agent.example.com:24443"}
	if err := srv.updateStealthConfig(ctx, cfg, true); err != nil {
		t.Fatal(err)
	}
	addr, pin, active := srv.StealthTransportInfo()
	if !active || addr != cfg.PublicAddress || pin == "" {
		t.Fatalf("active transport = %q %q %v", addr, pin, active)
	}
	conn, err := net.DialTimeout("tcp", cfg.ListenAddress, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	bad := cfg
	bad.ListenAddress = occupied.Addr().String()
	if err := srv.updateStealthConfig(ctx, bad, true); err == nil {
		t.Fatal("accepted occupied port")
	}
	items, _ := db.ListSettings(ctx)
	persisted, err := stealthConfigFromSettings(items)
	if err != nil || persisted != cfg {
		t.Fatalf("failed save changed config: %+v %v", persisted, err)
	}
	if got, _, ok := srv.StealthTransportInfo(); !ok || got != cfg.PublicAddress {
		t.Fatal("failed save stopped active transport")
	}

	// Closing and restarting must reuse the persisted endpoint and certificate.
	srv.Close()
	restarted := newTestServer(db, "test-secret", "")
	defer restarted.Close()
	restarted.ConfigureStealthTransport(certRoot)
	if err := restarted.StartStealthTransport(ctx); err != nil {
		t.Fatal(err)
	}
	if got, gotPin, ok := restarted.StealthTransportInfo(); !ok || got != addr || gotPin != pin {
		t.Fatal("restart changed endpoint or certificate")
	}
	changed := cfg
	changed.PublicAddress = "new.example.com:24443"
	if err := restarted.updateStealthConfig(ctx, changed, true); err != nil {
		t.Fatal(err)
	}
	if got, gotPin, ok := restarted.StealthTransportInfo(); !ok || got != changed.PublicAddress || gotPin != pin {
		t.Fatal("public address change did not apply")
	}
	changed.ListenAddress = unusedStealthAddress(t)
	if err := restarted.updateStealthConfig(ctx, changed, true); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", cfg.ListenAddress, time.Second); err == nil {
		conn.Close()
		t.Fatal("old listener remained open")
	}
	changed.Enabled = false
	if err := restarted.updateStealthConfig(ctx, changed, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := restarted.StealthTransportInfo(); ok {
		t.Fatal("disabled transport remains active")
	}
}

func TestStealthTransportSettingsProtectEnrolledAgents(t *testing.T) {
	t.Setenv("OBOARD_STEALTH_ADDR", "")
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	defer srv.Close()
	srv.ConfigureStealthTransport(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err := srv.StartStealthTransport(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := stealthTransportConfig{Enabled: true, ListenAddress: unusedStealthAddress(t), PublicAddress: "agent.example.com:24443"}
	if err := srv.updateStealthConfig(ctx, cfg, true); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "protected", AgentID: "agent-1", StealthEnabled: true, Status: model.ServerOffline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*stealthTransportConfig){
		func(c *stealthTransportConfig) { c.Enabled = false },
		func(c *stealthTransportConfig) { c.PublicAddress = "new.example.com:24443" },
		func(c *stealthTransportConfig) { c.ListenAddress = "127.0.0.1:24444" },
	} {
		change := cfg
		mutate(&change)
		for _, apply := range []bool{false, true} {
			if err := srv.updateStealthConfig(ctx, change, apply); err == nil || !strings.Contains(err.Error(), "protected") {
				t.Fatalf("unsafe change accepted: %v", err)
			}
		}
	}
	if err := srv.updateStealthConfig(ctx, cfg, true); err != nil {
		t.Fatalf("same config rejected: %v", err)
	}
}

func TestStealthTransportSettingsHTTPAndMCP(t *testing.T) {
	t.Setenv("OBOARD_STEALTH_ADDR", "")
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	defer srv.Close()
	srv.ConfigureStealthTransport(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err := srv.StartStealthTransport(ctx); err != nil {
		t.Fatal(err)
	}
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	cfg := stealthTransportConfig{Enabled: true, ListenAddress: unusedStealthAddress(t), PublicAddress: "agent.example.com:24443"}
	result := request(t, h, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{settingStealthTransport: cfg}, http.StatusOK)
	status := result["settings"].(map[string]any)[settingStealthTransport].(map[string]any)
	if status["active"] != true || status["public_address"] != cfg.PublicAddress {
		t.Fatalf("bad status: %#v", status)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{settingStealthTransport: cfg, "controller_url": "https://example.com"}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{settingStealthTransport: nil}, http.StatusBadRequest)
	if err := db.SetSetting(ctx, "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	command, env, err := srv.agentEnrollmentCommand(ctx, false, false, true)
	if err != nil || env["OBOARD_STEALTH_ADDR"] != cfg.PublicAddress || !strings.Contains(command, cfg.PublicAddress) {
		t.Fatalf("install address: %v", err)
	}
	principal := automationAdminPrincipal(t, db)
	cfg.Enabled = false
	raw, _ := json.Marshal(map[string]any{"changes": map[string]any{settingStealthTransport: cfg}})
	applyAutomationChangeset(t, srv, principal, "disable-stealth", automation.OperationRequest{Capability: "settings.update", Input: raw})
	if _, _, active := srv.StealthTransportInfo(); active {
		t.Fatal("MCP change did not stop listener")
	}
}

func TestStealthTransportSettingsEndpointValidation(t *testing.T) {
	for _, address := range []string{"0.0.0.0:24443", "[::]:24443", "https://example.com:443", "host/path:443", "host:0", "host:65536", "host:abc", "host\nname:443", "-host:443"} {
		if _, err := normalizeStealthEndpoint(address, false); err == nil {
			t.Errorf("accepted public endpoint %q", address)
		}
	}
	for _, address := range []string{"agent.example.com:24443", "192.0.2.1:24443", "[2001:db8::1]:24443"} {
		if _, err := normalizeStealthEndpoint(address, false); err != nil {
			t.Errorf("rejected %q: %v", address, err)
		}
	}
	t.Setenv("OBOARD_STEALTH_ADDR", "127.0.0.1:24443")
	cfg, err := stealthConfigFromSettings(nil)
	if err != nil || !cfg.Enabled {
		t.Fatalf("environment config: %v", err)
	}
	raw, _ := json.Marshal(stealthTransportConfig{ListenAddress: "0.0.0.0:24443"})
	cfg, err = stealthConfigFromSettings(map[string]string{settingStealthTransport: string(raw)})
	if err != nil || cfg.Enabled {
		t.Fatal("persisted disabled setting did not override environment")
	}
}
