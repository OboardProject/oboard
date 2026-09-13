package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

type fakeCloudflareRecord struct {
	ID, Type, Name, Content, Comment string
}

// newFakeCloudflare serves the small slice of the Cloudflare API the DNS
// integration uses. `down` makes every record call fail so an unreachable
// provider can be exercised.
func newFakeCloudflare(t *testing.T, records map[string]fakeCloudflareRecord, down *bool) *httptest.Server {
	t.Helper()
	next := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Header.Get("Authorization") != "Bearer cf-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": "bad token"}}})
			return
		}
		if *down && strings.Contains(r.URL.Path, "dns_records") {
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": "provider unreachable"}}})
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
				result = append(result, map[string]any{"id": record.ID, "type": record.Type, "name": record.Name, "content": record.Content, "comment": record.Comment, "ttl": 300})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-1/dns_records":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			next++
			id := "record-" + strconv.Itoa(next)
			records[id] = fakeCloudflareRecord{ID: id, Type: fmt.Sprint(payload["type"]), Name: fmt.Sprint(payload["name"]), Content: fmt.Sprint(payload["content"]), Comment: fmt.Sprint(payload["comment"])}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": id, "type": records[id].Type, "name": records[id].Name, "content": records[id].Content, "comment": records[id].Comment, "ttl": 300}})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/zones/zone-1/dns_records/"):
			id := strings.TrimPrefix(r.URL.Path, "/zones/zone-1/dns_records/")
			delete(records, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": id}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": r.URL.Path}}})
		}
	}))
}

// TestServerDeleteSurvivesUnreachableDNSProvider is the scenario the durable
// deletion exists for: the provider is down at delete time. The server must
// still be removed, its records must be released once the provider answers
// again, and a record this installation never created must be left alone.
func TestServerDeleteSurvivesUnreachableDNSProvider(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	records := map[string]fakeCloudflareRecord{}
	down := false
	cf := newFakeCloudflare(t, records, &down)
	defer cf.Close()

	srv := newTestServer(db, "test-secret", "")
	srv.dnsEndpoints.cloudflare = cf.URL
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(created["id"].(float64))
	credential := request(t, h, http.MethodPost, "/api/v1/ui/dns-credentials", token, map[string]any{"name": "primary", "provider": "cloudflare", "zone_name": "example.com", "config": map[string]any{"api_token": "cf-token"}}, http.StatusCreated)["dns_credential"].(map[string]any)
	credentialID := int64(credential["id"].(float64))
	request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/dns-credentials/%d/verify", credentialID), token, map[string]any{}, http.StatusOK)
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": serverID, "name": "edge-ss", "kind": "ss-2022-128", "listen_ip": "0.0.0.0", "port": 10001,
		"dns_sync_enabled": true, "dns_credential_id": credentialID, "dns_domain": "edge.example.com", "dns_record_types": "a",
		"enabled": true,
	}, http.StatusCreated)["inbound"].(map[string]any)
	request(t, h, http.MethodPost, "/api/v1/ui/dns-sync", token, map[string]any{"inbound_id": int64(inbound["id"].(float64))}, http.StatusOK)
	if len(records) != 1 {
		t.Fatalf("records after sync = %#v", records)
	}
	// Same name, not created by OBoard: deleting it would destroy a record
	// that belongs to whoever else manages this zone.
	records["foreign-1"] = fakeCloudflareRecord{ID: "foreign-1", Type: "AAAA", Name: "edge.example.com", Content: "2001:db8::1", Comment: "managed elsewhere"}

	down = true
	request(t, h, http.MethodDelete, fmt.Sprintf("/api/v1/ui/servers/%d", serverID), token, nil, http.StatusOK)
	if _, err := db.GetServer(ctx, serverID); err == nil {
		t.Fatal("an unreachable DNS provider kept the server undeletable")
	}
	pending, err := db.GetServerDeletion(ctx, serverID)
	if err != nil {
		t.Fatalf("no durable record of the unfinished cleanup: %v", err)
	}
	if pending.Stage != store.ServerDeletionExternal || pending.Attempts == 0 || pending.LastError == "" {
		t.Fatalf("deletion=%+v", pending)
	}
	if len(records) != 2 {
		t.Fatalf("records while the provider was down = %#v", records)
	}

	// The provider comes back: the retry releases only what this server owned.
	down = false
	srv.runServerDeletions(ctx)
	if _, ok := records["foreign-1"]; !ok {
		t.Fatal("cleanup deleted a record this installation never created")
	}
	if len(records) != 1 {
		t.Fatalf("records after retry = %#v", records)
	}
	if _, err := db.GetServerDeletion(ctx, serverID); err == nil {
		t.Fatal("a finished deletion is still pending")
	}
}

// TestServerDeleteResumesAfterRestart pins that a deletion interrupted between
// the claim and the row delete is finished by the worker rather than leaving a
// half-deleted server behind.
func TestServerDeleteResumesAfterRestart(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "interrupted", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(created["id"].(float64))

	// The process died right after claiming the deletion.
	if _, _, err := db.BeginServerDeletion(ctx, serverID, "interrupted", "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetServer(ctx, serverID); err != nil {
		t.Fatalf("claim must not remove the row itself: %v", err)
	}
	srv.runServerDeletions(ctx)
	if _, err := db.GetServer(ctx, serverID); err == nil {
		t.Fatal("the interrupted deletion left the server in place")
	}
	if _, err := db.GetServerDeletion(ctx, serverID); err == nil {
		t.Fatal("the resumed deletion is still pending")
	}
}
