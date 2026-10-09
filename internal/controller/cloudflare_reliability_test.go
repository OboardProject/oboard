package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestCloudflareResponseBodyTimeout(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			var calls atomic.Int32
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, `{"success":true,"result":`)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
			}))
			defer endpoint.Close()
			client := newCloudflareClient("token", endpoint.URL)
			client.httpClient.Timeout = 100 * time.Millisecond
			err := client.do(context.Background(), method, "/records", nil, nil, nil)
			if method == http.MethodGet {
				if err != nil || calls.Load() != 2 {
					t.Fatalf("body timeout not retried: calls=%d err=%v", calls.Load(), err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
				t.Fatalf("write retried or timeout lost: calls=%d err=%v", calls.Load(), err)
			}
		})
	}
}

func TestCloudflareTruncatedResponseRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		method    string
		status    int
		wantCalls int
	}{
		{http.MethodGet, http.StatusOK, 2},
		{http.MethodPost, http.StatusOK, 1},
		{http.MethodGet, http.StatusForbidden, 1},
	} {
		t.Run(tc.method+http.StatusText(tc.status), func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"success":`)
					return
				}
				_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
			}))
			defer endpoint.Close()
			err := newCloudflareClient("token", endpoint.URL).do(context.Background(), tc.method, "/records", nil, nil, nil)
			if calls != tc.wantCalls || (err == nil) != (tc.wantCalls == 2) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestCloudflareKnownZoneRecordOperationsAndVerification(t *testing.T) {
	var paths []string
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/zones":
			_, _ = io.WriteString(w, `{"success":true,"result":[{"id":"zone","name":"example.com"}]}`)
		case "/user/tokens/verify":
			_, _ = io.WriteString(w, `{"success":true,"result":{"status":"active"}}`)
		case "/zones/zone":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"success":false,"errors":[{"message":"forbidden"}]}`)
		case "/zones/zone/dns_records":
			_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
		case "/zones/zone/dns_records/record":
			_, _ = io.WriteString(w, `{"success":true,"result":{"id":"record"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer endpoint.Close()
	p := &cloudflareDNSProvider{dnsProviderBase: dnsProviderBase{credential: model.DNSCredential{ZoneID: "zone", ZoneName: "example.com"}, httpClient: endpoint.Client()}, token: "token", apiBase: endpoint.URL}
	ctx := context.Background()
	if _, err := p.ListRecordsForName(ctx, "edge.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpsertRecord(ctx, model.DNSRecord{ID: "record", Name: "edge.example.com", Type: "A", Content: "203.0.113.1"}); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteRecord(ctx, "record"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("redundant zone requests: %v", paths)
	}
	if err := p.Verify(ctx); err == nil || len(paths) != 5 || paths[4] != "GET /zones/zone" {
		t.Fatalf("verification skipped zone authorization: paths=%v err=%v", paths, err)
	}
	p = &cloudflareDNSProvider{dnsProviderBase: dnsProviderBase{credential: model.DNSCredential{ZoneName: "example.com"}, httpClient: endpoint.Client()}, token: "token", apiBase: endpoint.URL}
	if _, err := p.ListRecordsForName(ctx, "edge.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpsertRecord(ctx, model.DNSRecord{ID: "record", Name: "edge.example.com", Type: "A", Content: "203.0.113.1"}); err != nil {
		t.Fatal(err)
	}
	if err := p.DeleteRecord(ctx, "record"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 9 || paths[5] != "GET /zones" {
		t.Fatalf("zone discovery not reused: %v", paths)
	}
}

func TestDNSDDNSUpdatesAddressAndRejectsAmbiguousRecords(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	srv.monitorStarted.Store(true)
	node := &model.Server{Name: "edge", PublicIPv4: "203.0.113.2"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	verified := time.Now()
	encrypted, err := security.EncryptSecret("test-secret", "dns-credential", `{"api_token":"token"}`)
	if err != nil {
		t.Fatal(err)
	}
	credential := &model.DNSCredential{Name: "cf", Provider: model.DNSProviderCloudflare, Enabled: true, VerifiedAt: &verified, ConfigEncrypted: encrypted, Zones: []model.DNSCredentialZone{{ZoneName: "example.com", ProviderZoneID: "zone"}}}
	if err := db.CreateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: node.ID, Name: "entry", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: "{}", Enabled: true, DNSSyncEnabled: true, DDNSEnabled: true, DDNSInterval: 300, DNSCredentialID: &credential.ID, DNSDomain: "edge.example.com", DNSRecordTypes: "a"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	record := cloudflareDNSRecord{ID: "record", Type: "A", Name: inbound.DNSDomain, Content: "203.0.113.1", TTL: 300}
	reads, writes := 0, 0
	ambiguous := false
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone/dns_records":
			reads++
			if r.URL.Query().Get("name") != inbound.DNSDomain {
				t.Errorf("unfiltered DDNS query: %s", r.URL)
			}
			records := []cloudflareDNSRecord{record,
				{ID: "txt-1", Type: "TXT", Name: inbound.DNSDomain, Content: "one"},
				{ID: "txt-2", Type: "TXT", Name: inbound.DNSDomain, Content: "two"},
			}
			if ambiguous {
				other := record
				other.ID, other.Content = "duplicate", "203.0.113.99"
				records = append(records, other)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": records})
		case r.Method == http.MethodPatch && r.URL.Path == "/zones/zone/dns_records/record":
			writes++
			if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": record})
		default:
			t.Errorf("unexpected DDNS request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer endpoint.Close()
	srv.dnsEndpoints.cloudflare = endpoint.URL
	srv.runDNSDDNS(ctx)
	saved, err := db.GetInbound(ctx, inbound.ID)
	if err != nil || saved.DNSSyncError != "" || saved.DNSLastSyncedAt == nil || reads != 1 || writes != 1 || record.Content != node.PublicIPv4 {
		t.Fatalf("DDNS failed to update address: saved=%+v reads=%d writes=%d record=%+v err=%v", saved, reads, writes, record, err)
	}
	srv.runDNSDDNS(ctx)
	if reads != 1 {
		t.Fatal("successful DDNS ignored check interval")
	}
	if _, err := srv.syncDNSInbounds(ctx, []model.Server{*node}, []model.Inbound{*saved}); err != nil || writes != 1 {
		t.Fatalf("unchanged address was rewritten: writes=%d err=%v", writes, err)
	}
	ambiguous = true
	if _, err := srv.syncDNSInbounds(ctx, []model.Server{*node}, []model.Inbound{*saved}); err == nil || !strings.Contains(err.Error(), "存在多条 A 记录") || writes != 1 {
		t.Fatalf("ambiguous records accepted or modified: writes=%d err=%v", writes, err)
	}
}

func TestCloudflareFilteredPagination(t *testing.T) {
	calls := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("name") != "edge.example.com" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("query=%s", r.URL.RawQuery)
		}
		page := r.URL.Query().Get("page")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{{"id": page, "name": "edge.example.com", "type": "A"}}, "result_info": map[string]any{"total_pages": 2}})
	}))
	defer endpoint.Close()
	client := newCloudflareClient("token", endpoint.URL)
	items, err := client.listDNSRecordsForName(context.Background(), cloudflareZone{ID: "zone"}, "EDGE.example.com.")
	if err != nil || len(items) != 2 || calls != 2 || items[0].ID != "1" || items[1].ID != "2" {
		t.Fatalf("items=%+v calls=%d err=%v", items, calls, err)
	}
}

func TestCloudflareRetriesOnlyTransientReads(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		status, want int
		fail         bool
	}{
		{"temporary read", http.MethodGet, 503, 2, false},
		{"write", http.MethodPost, 503, 1, true},
		{"permission", http.MethodGet, 403, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					http.Error(w, "unavailable", tc.status)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
			}))
			defer endpoint.Close()
			client := newCloudflareClient("token", endpoint.URL)
			err := client.do(context.Background(), tc.method, "/records", nil, nil, nil)
			if calls != tc.want || (err != nil) != tc.fail {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestCloudflareReadTimeoutRecoveryAndCancellation(t *testing.T) {
	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
	}))
	defer endpoint.Close()
	client := newCloudflareClient("token", endpoint.URL)
	client.httpClient.Timeout = 50 * time.Millisecond
	if err := client.do(context.Background(), http.MethodGet, "/records", nil, nil, nil); err != nil || calls.Load() != 2 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.do(ctx, http.MethodGet, "/records", nil, nil, nil); err == nil || calls.Load() != 2 {
		t.Fatalf("cancelled request retried: %d %v", calls.Load(), err)
	}
}

func TestDNSDDNSFailurePreservesSuccessAndDeduplicates(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "dns.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	srv.monitorStarted.Store(true)
	server := &model.Server{Name: "edge", PublicIPv4: "203.0.113.1"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 8388, ConfigJSON: "{}", Enabled: true, DNSSyncEnabled: true, DDNSEnabled: true, DDNSInterval: 300, DNSDomain: "edge.example.com", DNSRecordTypes: "a"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "a", ProxyPassword: "a"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "ops", Type: "test", Enabled: true, Events: notificationDNSSyncFailed}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	success := time.Now().UTC().Add(-time.Hour)
	if err := db.UpdateInboundDNSSyncResult(ctx, inbound.ID, "ok", "", &success); err != nil {
		t.Fatal(err)
	}
	srv.runDNSDDNS(ctx)
	saved, err := db.GetInbound(ctx, inbound.ID)
	if err != nil || saved.DNSLastSyncedAt == nil || !saved.DNSLastSyncedAt.Equal(success) || saved.DNSSyncError == "" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if err := db.UpdateInboundDNSSyncResult(ctx, inbound.ID, "marker", "failure", nil); err != nil {
		t.Fatal(err)
	}
	srv.runDNSDDNS(ctx)
	saved, err = db.GetInbound(ctx, inbound.ID)
	if err != nil || saved.DNSSyncStatus != "marker" {
		t.Fatalf("cooldown ignored: %+v %v", saved, err)
	}
	srv.dnsDDNSNext[inbound.ID] = time.Time{}
	srv.runDNSDDNS(ctx)
	srv.notifyDNSSyncFailure(ctx, *saved, server.Name, context.DeadlineExceeded)
	pending, err := db.ListPendingNotificationDeliveries(ctx, time.Now().Add(time.Hour), 50)
	if err != nil || len(pending) != 1 {
		t.Fatalf("duplicate incident alerts: %+v %v", pending, err)
	}
	recovered := success.Add(time.Minute)
	if err := db.UpdateInboundDNSSyncResult(ctx, inbound.ID, "ok", "", &recovered); err != nil {
		t.Fatal(err)
	}
	srv.dnsDDNSNext[inbound.ID] = time.Time{}
	srv.runDNSDDNS(ctx)
	pending, err = db.ListPendingNotificationDeliveries(ctx, time.Now().Add(time.Hour), 50)
	if err != nil || len(pending) != 2 {
		t.Fatalf("new incident missing: %+v %v", pending, err)
	}
}
