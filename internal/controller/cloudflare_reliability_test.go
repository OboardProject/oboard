package controller

import (
	"context"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

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
