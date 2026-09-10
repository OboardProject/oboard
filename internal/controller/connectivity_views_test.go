package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestConnectivityDetailsSLAAndPaginationContract(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "views.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "views", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(30 * time.Second)
	for i := 0; i < 6; i++ {
		report := model.LatencyProbeResultReport{ReportID: fmt.Sprint(i), ResourceVersion: "test", CheckedAt: at.Add(-time.Duration(6-i) * time.Minute), Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Available: i%3 != 0, LatencyMS: int64(i + 1), SampleCount: 3, SuccessCount: i % 3}}}
		if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
			t.Fatal(err)
		}
	}
	server := newTestServer(db, "views-secret", "")
	defer server.Close()
	server.latencyHistoryNow = func() time.Time { return at }
	p := application.HumanPrincipal(model.User{ID: 1}, model.RoleAdmin, netip.Addr{})
	denied := p
	denied.ResourceFilter = json.RawMessage(`{"server_ids":[999999]}`)
	for _, view := range []string{"sla", "events"} {
		if _, err := server.readConnectivityDetails(ctx, denied, view, connectivityViewInput{ServerID: node.ID}); err == nil || historyErrorStatus(err) != 403 {
			t.Fatalf("resource filter bypass: %v", err)
		}
	}

	raw, err := server.readConnectivityDetails(ctx, p, "sla", connectivityViewInput{ServerID: node.ID, Window: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	var sla connectivitySLAResponse
	if err := json.Unmarshal(raw, &sla); err != nil {
		t.Fatal(err)
	}
	history, err := db.ListConnectivityHistory(ctx, node.ID, sla.Window.From, sla.Window.To)
	if err != nil {
		t.Fatal(err)
	}
	window, _ := parseConnectivityWindow("1h", at)
	reference := BuildConnectivityResponse(node.ID, window, history)
	if !reflect.DeepEqual(sla.Summary, reference.Summary) || !reflect.DeepEqual(sla.Outages, reference.Outages) {
		t.Fatalf("SLA=%+v full=%+v", sla, reference)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, field := range []string{"latency_points", "probe_target_stats", "regional_latency_points", "latency", "current"} {
		if fields[field] != nil {
			t.Fatalf("SLA included %s", field)
		}
	}
	raw, err = server.readConnectivityDetails(ctx, p, "events", connectivityViewInput{ServerID: node.ID, Window: "1h", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	var page connectivityEventsResponse
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("page=%+v", page)
	}
	cursor := page.NextCursor
	seen := map[int64]bool{}
	for _, event := range page.Events {
		seen[event.ID] = true
	}
	for page.HasMore {
		raw, err = server.readConnectivityDetails(ctx, p, "events", connectivityViewInput{ServerID: node.ID, Window: "1h", Limit: 2, Cursor: page.NextCursor})
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Events {
			if seen[event.ID] {
				t.Fatalf("duplicate %d", event.ID)
			}
			seen[event.ID] = true
		}
	}
	if len(seen) != len(history.Events) {
		t.Fatalf("events %d != %d", len(seen), len(history.Events))
	}
	for _, input := range []connectivityViewInput{
		{ServerID: node.ID, Window: "24h", Cursor: cursor},
		{ServerID: node.ID + 1, Window: "1h", Cursor: cursor},
		{ServerID: node.ID, Window: "1h", Cursor: "invalid"},
		{ServerID: node.ID, Window: "1h", Limit: 201},
	} {
		if _, err := server.readConnectivityDetails(ctx, p, "events", input); err == nil {
			t.Fatalf("accepted %+v", input)
		}
	}
	decoded, _ := base64.RawURLEncoding.DecodeString(cursor)
	var c connectivityCursor
	_ = json.Unmarshal(decoded, &c)
	c.From = c.To.Add(-31 * 24 * time.Hour)
	decoded, _ = json.Marshal(c)
	if _, err := server.readConnectivityDetails(ctx, p, "events", connectivityViewInput{ServerID: node.ID, Window: "1h", Cursor: base64.RawURLEncoding.EncodeToString(decoded)}); err == nil {
		t.Fatal("unbounded cursor accepted")
	}
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)["token"].(string)
	for _, view := range []string{"sla", "events"} {
		path := fmt.Sprintf("/api/v1/ui/servers/%d/connectivity?view=%s&window=1h", node.ID, view)
		request(t, handler, http.MethodGet, path, "", nil, 401)
		request(t, handler, http.MethodGet, path, token, nil, 200)
		machine := fmt.Sprintf("/api/v1/servers/%d/connectivity?view=%s&window=1h", node.ID, view)
		result := request(t, handler, http.MethodGet, machine, token, nil, 200)
		if result["data"] == nil {
			t.Fatal("missing machine envelope")
		}
		request(t, handler, http.MethodGet, path+"&max_points=3", token, nil, 400)
	}
	user, err := db.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BumpSessionVersion(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	request(t, handler, http.MethodGet, fmt.Sprintf("/api/v1/ui/servers/%d/connectivity?view=sla", node.ID), token, nil, 401)
}

func TestConnectivitySLABucketsIncludePartialTail(t *testing.T) {
	from := time.Now().UTC().Truncate(time.Hour)
	window := connectivityWindow{From: from, To: from.Add(7 * time.Minute), Duration: 7 * time.Minute, BucketDuration: 5 * time.Minute}
	buckets := buildConnectivityBuckets(window, []connectivitySegment{{start: from, end: window.To, availability: connectivityUnavailable}}, nil)
	if len(buckets) != 2 || buckets[1].UnavailableSeconds != 120 || !buckets[1].EndAt.Equal(window.To) {
		t.Fatalf("buckets=%+v", buckets)
	}
}

func TestConnectivityDetailsBasePath(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "prefix.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	node := &model.Server{Name: "prefix"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	app := New(db, "prefix-secret", "", "/private", nil)
	defer app.Close()
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/private/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	token := request(t, handler, http.MethodPost, "/private/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)["token"].(string)
	for _, view := range []string{"sla", "events"} {
		for _, base := range []string{"/api/v1/ui", "/api/v1"} {
			path := fmt.Sprintf("%s/servers/%d/connectivity?view=%s", base, node.ID, view)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
			if rr.Code != 404 || rr.Body.Len() != 0 {
				t.Fatalf("outside prefix=%d %s", rr.Code, rr.Body.String())
			}
			request(t, handler, http.MethodGet, "/private"+path, token, nil, 200)
		}
	}
}
