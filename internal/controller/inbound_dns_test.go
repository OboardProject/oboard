package controller

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestPickInboundDNSCredentialAutoFill(t *testing.T) {
	verified := time.Now()
	first := model.DNSCredential{ID: 2, Name: "cf", Provider: model.DNSProviderCloudflare, Enabled: true, VerifiedAt: &verified, Zones: []model.DNSCredentialZone{{ZoneName: "example.com"}}}
	second := model.DNSCredential{ID: 5, Name: "ali", Provider: model.DNSProviderAliDNS, Enabled: true, Zones: []model.DNSCredentialZone{{ZoneName: "example.net"}}}
	id, available, ok := pickInboundDNSCredential([]model.DNSCredential{first}, "entry.example.com", 1, 0)
	if !ok || id != 2 || len(available) != 1 {
		t.Fatalf("single credential: id=%d ok=%v available=%#v", id, ok, available)
	}
	id, available, ok = pickInboundDNSCredential([]model.DNSCredential{first, second}, "entry.example.com", 1, 0)
	if !ok || id != 2 {
		t.Fatalf("default among many: id=%d ok=%v available=%#v", id, ok, available)
	}
	_, available, ok = pickInboundDNSCredential(nil, "entry.example.com", 1, 0)
	if ok || len(available) != 0 {
		t.Fatalf("empty tenant: ok=%v available=%#v", ok, available)
	}
	unverifiedA := model.DNSCredential{ID: 8, Name: "a", Provider: model.DNSProviderCloudflare, Enabled: true, Zones: []model.DNSCredentialZone{{ZoneName: "example.com"}}}
	unverifiedB := model.DNSCredential{ID: 9, Name: "b", Provider: model.DNSProviderAliDNS, Enabled: true, Zones: []model.DNSCredentialZone{{ZoneName: "example.com"}}}
	id, available, ok = pickInboundDNSCredential([]model.DNSCredential{unverifiedA, unverifiedB}, "entry.example.com", 1, 0)
	if ok || id != 0 || len(available) != 2 {
		t.Fatalf("ambiguous unverified: id=%d ok=%v available=%#v", id, ok, available)
	}
}

func TestInboundCreateAutoFillsSingleDNSCredential(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	verified := time.Now()
	credential := &model.DNSCredential{Name: "primary", Provider: model.DNSProviderCloudflare, ZoneName: "example.com", Enabled: true, VerifiedAt: &verified, Zones: []model.DNSCredentialZone{{ZoneName: "example.com"}}}
	if err := db.CreateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "dns-auto", "protocol": "shadowsocks", "listen_ip": "0.0.0.0", "port": 8388, "entry_ip_mode": "auto", "dns_domain": "entry.example.com", "dns_sync_enabled": true, "dns_record_types": "both", "config_json": `{"method":"2022-blake3-aes-128-gcm"}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	if inbound["dns_sync_enabled"] != true {
		t.Fatalf("dns_sync_enabled = %#v", inbound["dns_sync_enabled"])
	}
	if int64(inbound["dns_credential_id"].(float64)) != credential.ID {
		t.Fatalf("dns_credential_id = %#v, want %d", inbound["dns_credential_id"], credential.ID)
	}
}

func TestInboundCreateMissingDNSCredentialError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	body := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "dns-missing", "protocol": "shadowsocks", "listen_ip": "0.0.0.0", "port": 8388, "entry_ip_mode": "auto", "dns_domain": "entry.example.com", "dns_sync_enabled": true, "dns_record_types": "both", "config_json": `{"method":"2022-blake3-aes-128-gcm"}`, "enabled": true}, http.StatusBadRequest)
	if body["code"] != missingDNSCredentialCode {
		t.Fatalf("code = %#v body=%s", body["code"], mustJSON(t, body))
	}
	available, _ := body["available_credentials"].([]any)
	if len(available) != 0 {
		t.Fatalf("available_credentials = %#v", body["available_credentials"])
	}
}

func TestInboundCreateRecipeMissingDNSCredentialFailsBeforeReady(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "OC", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	_, err := server.prepareInboundCreateRecipe(ctx, principal, mcpTaskInput{
		Goal:       "给 OC 创建一个 anytls 节点，同步 dns wahk.example.com",
		Params:     map[string]any{"protocol": "anytls", "port": 443, "dns_sync_enabled": true, "dns_record_types": "both"},
		TargetRefs: []string{"server:" + int64String(node.ID)},
	})
	var missing missingDNSCredentialError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want missing_dns_credential", err)
	}
	if len(missing.Available) != 0 {
		t.Fatalf("available = %#v", missing.Available)
	}
}

func TestInboundCreateRecipeUsesDefaultDNSCredential(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	node := &model.Server{Name: "OC", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	verified := time.Now()
	first := &model.DNSCredential{Name: "cf", Provider: model.DNSProviderCloudflare, ZoneName: "example.com", Enabled: true, VerifiedAt: &verified, Zones: []model.DNSCredentialZone{{ZoneName: "example.com"}}}
	second := &model.DNSCredential{Name: "ali", Provider: model.DNSProviderAliDNS, ZoneName: "example.com", Enabled: true, Zones: []model.DNSCredentialZone{{ZoneName: "example.com"}}}
	if err := db.CreateDNSCredential(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateDNSCredential(ctx, second); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	prepared, err := server.prepareInboundCreateRecipe(ctx, principal, mcpTaskInput{
		Goal:       "给 OC 创建一个 anytls 节点，同步 dns wahk.example.com",
		Params:     map[string]any{"protocol": "anytls", "port": 443, "dns_sync_enabled": true, "dns_record_types": "both"},
		TargetRefs: []string{"server:" + int64String(node.ID)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "ready" {
		t.Fatalf("prepared status = %s questions=%#v", prepared.Status, prepared.Questions)
	}
	inbound, _ := prepared.Operations[0].Input["inbound"].(map[string]any)
	if inbound["dns_sync_enabled"] != true {
		t.Fatalf("dns_sync_enabled = %#v", inbound["dns_sync_enabled"])
	}
	if inbound["dns_credential_id"] != first.ID {
		t.Fatalf("dns_credential_id = %#v, want %d", inbound["dns_credential_id"], first.ID)
	}
}

func TestDNSBootstrapContextExposesDefaultCredential(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	verified := time.Now()
	credential := &model.DNSCredential{Name: "cf", Provider: model.DNSProviderCloudflare, ZoneName: "example.com", Enabled: true, VerifiedAt: &verified, Zones: []model.DNSCredentialZone{{ZoneName: "example.com"}}}
	if err := db.CreateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	payload := server.dnsBootstrapContext(ctx)
	if payload["default_dns_credential_id"] != credential.ID {
		t.Fatalf("default = %#v", payload["default_dns_credential_id"])
	}
	available, _ := payload["available_credentials"].([]dnsCredentialRef)
	if len(available) != 1 || available[0].ID != credential.ID || available[0].Provider != string(model.DNSProviderCloudflare) {
		t.Fatalf("available = %#v", payload["available_credentials"])
	}
}
