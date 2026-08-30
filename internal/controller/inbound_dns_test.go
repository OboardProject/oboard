package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
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

func TestInboundUpdateReplacesDNSRecordsAndFollowsCertificateDomain(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	type cfRecord struct {
		ID, Type, Name, Content, Comment string
		Proxied                       bool
		TTL                           int
	}
	records := map[string]cfRecord{}
	nextID := 0
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Header.Get("Authorization") != "Bearer cf-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": "bad token"}}})
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/tokens/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "token-1", "status": "active"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			result := []map[string]any{}
			if r.URL.Query().Get("name") == "example.com" {
				result = []map[string]any{{"id": "zone-1", "name": "example.com"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "zone-1", "name": "example.com"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			result := make([]map[string]any, 0, len(records))
			for _, record := range records {
				result = append(result, map[string]any{"id": record.ID, "type": record.Type, "name": record.Name, "content": record.Content, "comment": record.Comment, "proxied": record.Proxied, "ttl": record.TTL})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-1/dns_records":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			nextID++
			id := "record-" + strconv.Itoa(nextID)
			record := cfRecord{ID: id, Type: fmt.Sprint(payload["type"]), Name: fmt.Sprint(payload["name"]), Content: fmt.Sprint(payload["content"]), Comment: fmt.Sprint(payload["comment"]), TTL: 300}
			if proxied, ok := payload["proxied"].(bool); ok {
				record.Proxied = proxied
			}
			records[id] = record
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": record.ID, "type": record.Type, "name": record.Name, "content": record.Content, "comment": record.Comment, "proxied": record.Proxied, "ttl": record.TTL}})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/zones/zone-1/dns_records/"):
			id := strings.TrimPrefix(r.URL.Path, "/zones/zone-1/dns_records/")
			delete(records, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": id}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": r.URL.Path}}})
		}
	}))
	defer cf.Close()

	srv := newTestServer(db, "test-secret", "")
	srv.dnsEndpoints.cloudflare = cf.URL
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	createdCredential := request(t, h, http.MethodPost, "/api/v1/ui/dns-credentials", token, map[string]any{"name": "primary", "provider": "cloudflare", "zone_name": "example.com", "config": map[string]any{"api_token": "cf-token"}}, http.StatusCreated)
	credentialID := int64(createdCredential["dns_credential"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/dns-credentials/%d/verify", credentialID), token, map[string]any{}, http.StatusOK)

	expiry := time.Now().Add(40 * 24 * time.Hour)
	certificate := &model.Certificate{Name: "old", PrimaryDomain: "old.example.com", Domains: []string{"old.example.com"}, ChallengeType: model.CertificateChallengeDNS, ACMECA: "letsencrypt", Status: model.CertificateStatusReady, Revision: "rev-1", NotAfter: &expiry, DNSCredentialID: &credentialID}
	if err := db.CreateCertificate(context.Background(), certificate); err != nil {
		t.Fatal(err)
	}

	createdInbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": serverID, "name": "oc-hy2", "kind": "hy2-tls", "listen_ip": "0.0.0.0", "port": 443,
		"dns_sync_enabled": true, "dns_credential_id": credentialID, "dns_domain": "old.example.com", "dns_record_types": "a",
		"certificate_mode": "auto", "enabled": true,
	}, http.StatusCreated)["inbound"].(map[string]any)
	inboundID := int64(createdInbound["id"].(float64))
	if err := db.UpsertInboundCertificateBinding(context.Background(), &model.InboundCertificateBinding{InboundID: inboundID, CertificateID: &certificate.ID, Mode: model.CertificateModeAuto, ServerName: "old.example.com"}); err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/dns-sync", token, map[string]any{"inbound_id": inboundID}, http.StatusOK)
	if len(records) != 1 {
		t.Fatalf("initial records = %#v", records)
	}
	for _, record := range records {
		if record.Name != "old.example.com" {
			t.Fatalf("initial record = %#v", record)
		}
	}

	updated := request(t, h, http.MethodPatch, fmt.Sprintf("/api/v1/ui/inbounds/%d", inboundID), token, map[string]any{
		"dns_domain": "new.example.com", "certificate_domain": "old.example.com", "certificate_id": certificate.ID, "certificate_mode": "auto",
		"dns_sync_enabled": true, "dns_credential_id": credentialID, "dns_record_types": "a",
	}, http.StatusOK)["inbound"].(map[string]any)
	if updated["dns_domain"] != "new.example.com" {
		t.Fatalf("dns_domain = %#v", updated["dns_domain"])
	}
	if updated["certificate_domain"] != "new.example.com" {
		t.Fatalf("certificate_domain = %#v, want new.example.com", updated["certificate_domain"])
	}
	if updated["certificate_id"] != nil {
		t.Fatalf("stale certificate_id kept: %#v", updated["certificate_id"])
	}
	if len(records) != 1 {
		t.Fatalf("records after domain change = %#v", records)
	}
	for _, record := range records {
		if record.Name != "new.example.com" || record.Type != "A" || record.Content != "203.0.113.10" {
			t.Fatalf("replacement record = %#v", record)
		}
	}
}

func TestInboundUpdateRebindsCoveringCertificateOnDomainChange(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	type cfRecord struct {
		ID, Type, Name, Content string
	}
	records := map[string]cfRecord{}
	nextID := 0
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Header.Get("Authorization") != "Bearer cf-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": "bad token"}}})
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/tokens/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "token-1", "status": "active"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			result := []map[string]any{}
			if r.URL.Query().Get("name") == "example.com" {
				result = []map[string]any{{"id": "zone-1", "name": "example.com"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "zone-1", "name": "example.com"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			result := make([]map[string]any, 0, len(records))
			for _, record := range records {
				result = append(result, map[string]any{"id": record.ID, "type": record.Type, "name": record.Name, "content": record.Content, "ttl": 300})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-1/dns_records":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			nextID++
			id := "record-" + strconv.Itoa(nextID)
			record := cfRecord{ID: id, Type: fmt.Sprint(payload["type"]), Name: fmt.Sprint(payload["name"]), Content: fmt.Sprint(payload["content"])}
			records[id] = record
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": record.ID, "type": record.Type, "name": record.Name, "content": record.Content, "ttl": 300}})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/zones/zone-1/dns_records/"):
			id := strings.TrimPrefix(r.URL.Path, "/zones/zone-1/dns_records/")
			delete(records, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": id}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": r.URL.Path}}})
		}
	}))
	defer cf.Close()

	srv := newTestServer(db, "test-secret", "")
	srv.dnsEndpoints.cloudflare = cf.URL
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	createdCredential := request(t, h, http.MethodPost, "/api/v1/ui/dns-credentials", token, map[string]any{"name": "primary", "provider": "cloudflare", "zone_name": "example.com", "config": map[string]any{"api_token": "cf-token"}}, http.StatusCreated)
	credentialID := int64(createdCredential["dns_credential"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/dns-credentials/%d/verify", credentialID), token, map[string]any{}, http.StatusOK)

	expiry := time.Now().Add(40 * 24 * time.Hour)
	oldCert := &model.Certificate{Name: "old", PrimaryDomain: "old.example.com", Domains: []string{"old.example.com"}, ChallengeType: model.CertificateChallengeDNS, ACMECA: "letsencrypt", Status: model.CertificateStatusReady, Revision: "rev-old", NotAfter: &expiry, DNSCredentialID: &credentialID}
	if err := db.CreateCertificate(context.Background(), oldCert); err != nil {
		t.Fatal(err)
	}
	covering := &model.Certificate{Name: "wildcard", PrimaryDomain: "*.example.com", Domains: []string{"*.example.com"}, ChallengeType: model.CertificateChallengeDNS, ACMECA: "letsencrypt", Status: model.CertificateStatusReady, Revision: "rev-wild", NotAfter: &expiry, DNSCredentialID: &credentialID}
	if err := db.CreateCertificate(context.Background(), covering); err != nil {
		t.Fatal(err)
	}

	createdInbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": serverID, "name": "oc-anytls", "kind": "anytls-basic", "listen_ip": "0.0.0.0", "port": 443,
		"dns_sync_enabled": true, "dns_credential_id": credentialID, "dns_domain": "old.example.com", "dns_record_types": "a",
		"certificate_mode": "auto", "enabled": true,
	}, http.StatusCreated)["inbound"].(map[string]any)
	inboundID := int64(createdInbound["id"].(float64))

	updated := request(t, h, http.MethodPatch, fmt.Sprintf("/api/v1/ui/inbounds/%d", inboundID), token, map[string]any{
		"dns_domain": "new.example.com", "certificate_domain": "old.example.com", "certificate_id": oldCert.ID, "certificate_mode": "auto",
		"dns_sync_enabled": true, "dns_credential_id": credentialID, "dns_record_types": "a",
	}, http.StatusOK)["inbound"].(map[string]any)
	if updated["certificate_domain"] != "new.example.com" {
		t.Fatalf("certificate_domain = %#v", updated["certificate_domain"])
	}
	if updated["certificate_id"] == nil || int64(updated["certificate_id"].(float64)) != covering.ID {
		t.Fatalf("covering certificate_id = %#v, want %d", updated["certificate_id"], covering.ID)
	}
	if len(records) != 1 {
		t.Fatalf("records after domain change = %#v", records)
	}
	for _, record := range records {
		if record.Name != "new.example.com" {
			t.Fatalf("replacement record = %#v", record)
		}
	}
}
