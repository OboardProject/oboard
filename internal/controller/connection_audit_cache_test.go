package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// seedAuditReportingServer creates an audit-enabled server plus one user with a
// small report inside the window so ConnectionAuditOverview has something to
// aggregate.
func seedAuditReportingServer(t *testing.T, db *store.Store) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	server := &model.Server{Name: "audit-cache-node", AgentID: "audit-cache-agent", AgentTokenHash: "x", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "audit-cache-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "33333333-3333-4333-8333-333333333333", ProxyPassword: "audit-password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	report := model.ConnectionAuditReport{
		ReportID: "dashboard-audit-cache-report", ServerID: server.ID, UserID: user.ID, SourceIP: "198.51.100.9", Network: "tcp",
		Destination: "example.com", DestinationPort: 443, ConnectionCount: 2, ClosedCount: 2, DurationTotalMS: 250, DurationMaxMS: 250,
		DurationLE1SCount: 2, ActivePeak: 1, PresenceSequence: 1, BucketCapacity: 4096,
		CollectionStartedAt: nowTime.Add(-time.Minute), CollectionEndedAt: nowTime,
		StartedAt: nowTime.Add(-time.Second), EndedAt: nowTime,
	}
	if _, err := db.AddConnectionAuditReports(ctx, []model.ConnectionAuditReport{report}); err != nil {
		t.Fatal(err)
	}
	return server.ID, user.ID
}

// TestDashboardConnectionAuditColdStartIsNonBlocking proves a Controller that
// never computed the risk count answers the dashboard immediately with
// ready:false instead of waiting for the full overview, and that the first
// background refresh fills the cache.
func TestDashboardConnectionAuditColdStartIsNonBlocking(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedAuditReportingServer(t, db)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()

	first := dashboardConnectionAuditForTest(t, srv, ctx)
	if ready, exists := first["ready"]; !exists || ready != false {
		t.Fatalf("cold dashboard audit must report ready=false: %#v", first)
	}
	if first["elevated_risk_count"] != 0 || first["stale"] != true {
		t.Fatalf("cold dashboard audit must report 0 with stale=true: %#v", first)
	}

	srv.refreshDashboardConnectionAudit()
	warm := dashboardConnectionAuditForTest(t, srv, ctx)
	if _, exists := warm["ready"]; exists {
		t.Fatalf("warm dashboard audit must not carry ready: %#v", warm)
	}
	if _, exists := warm["stale"]; exists {
		t.Fatalf("fresh dashboard audit must not carry stale: %#v", warm)
	}
	// The fixture has one user with ordinary traffic, so the overview reports
	// no elevated user but must still complete and cache the count.
	if warm["elevated_risk_count"] != 0 {
		t.Fatalf("warm dashboard audit count = %#v", warm["elevated_risk_count"])
	}
}

// TestDashboardConnectionAuditStaleServesPreviousValue proves a request past
// the TTL returns the old count immediately with stale=true instead of
// blocking on a recomputation.
func TestDashboardConnectionAuditStaleServesPreviousValue(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedAuditReportingServer(t, db)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	srv.refreshDashboardConnectionAudit()
	srv.connectionAuditCacheMu.Lock()
	srv.connectionAuditCacheAt = time.Now().Add(-2 * dashboardAuditCacheTTL)
	srv.connectionAuditCacheMu.Unlock()

	stale := dashboardConnectionAuditForTest(t, srv, ctx)
	if stale["stale"] != true {
		t.Fatalf("aged cache must return stale=true: %#v", stale)
	}
	if _, exists := stale["ready"]; exists {
		t.Fatalf("stale value still counts as ready: %#v", stale)
	}
}

// TestDashboardConnectionAuditRefreshIsSingleFlight proves concurrent stale
// requests never start more than one background recomputation.
func TestDashboardConnectionAuditRefreshIsSingleFlight(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedAuditReportingServer(t, db)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()

	srv.connectionAuditCacheMu.Lock()
	srv.connectionAuditComputing = true
	srv.connectionAuditCacheMu.Unlock()
	first := dashboardConnectionAuditForTest(t, srv, ctx)
	second := dashboardConnectionAuditForTest(t, srv, ctx)
	srv.connectionAuditCacheMu.Lock()
	stillComputing := srv.connectionAuditComputing
	srv.connectionAuditCacheMu.Unlock()
	if !stillComputing {
		t.Fatalf("concurrent stale requests must not start a second refresh")
	}
	if first["stale"] != true || second["stale"] != true {
		t.Fatalf("requests while computing must return stale immediately: %#v %#v", first, second)
	}
}

// TestDashboardConnectionAuditDisabledSkipsWork proves the disabled path never
// computes and never marks the badge as computing.
func TestDashboardConnectionAuditDisabledSkipsWork(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "audit_enabled", "false"); err != nil {
		t.Fatal(err)
	}
	seedAuditReportingServer(t, db)
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	out := dashboardConnectionAuditForTest(t, srv, ctx)
	if out["elevated_risk_count"] != 0 {
		t.Fatalf("disabled audit must report zero: %#v", out)
	}
	if _, exists := out["ready"]; exists {
		t.Fatalf("disabled audit must not report ready=false: %#v", out)
	}
	srv.connectionAuditCacheMu.Lock()
	valid := srv.connectionAuditCacheValid
	computing := srv.connectionAuditComputing
	srv.connectionAuditCacheMu.Unlock()
	if valid || computing {
		t.Fatalf("disabled audit must not touch the cache or start refreshes: valid=%v computing=%v", valid, computing)
	}
}

// TestDashboardConnectionAuditPageData proves the page-data payload carries the
// stale-while-revalidate shape end to end.
func TestDashboardConnectionAuditPageData(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	seedAuditReportingServer(t, db)
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	_, token := loginTestAdmin(t, db)
	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=dashboard", token, nil, http.StatusOK)
	audit, ok := page["connection_audit"].(map[string]any)
	if !ok {
		t.Fatalf("dashboard connection_audit missing: %#v", page["connection_audit"])
	}
	if audit["ready"] != false || audit["stale"] != true {
		t.Fatalf("cold page-data audit shape = %#v", audit)
	}
}

func dashboardConnectionAuditForTest(t *testing.T, srv *Server, ctx context.Context) map[string]any {
	t.Helper()
	out, err := srv.dashboardConnectionAudit(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
