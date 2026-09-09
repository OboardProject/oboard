package controller

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestPageDataServersIncludeDeliveryStatus(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	server := &model.Server{Name: "delivery-server", ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	eval, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 1, "digest-1", []string{"cred-1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if eval.State.DesiredRevision <= 0 {
		t.Fatalf("authorization desired revision = %d", eval.State.DesiredRevision)
	}
	if _, err := db.RecordAuthorizationConfirmation(ctx, server.ID, eval.State.DesiredRevision, 1, "digest-1", "boot-1"); err != nil {
		t.Fatal(err)
	}

	list := request(t, h, http.MethodGet, "/api/v1/ui/servers", token, nil, http.StatusOK)
	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=servers", token, nil, http.StatusOK)
	dashboard := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=dashboard", token, nil, http.StatusOK)

	listServer := firstNamedServer(t, list["servers"], "delivery-server")
	pageServer := firstNamedServer(t, page["servers"], "delivery-server")
	dashboardServer := firstNamedServer(t, dashboard["servers"], "delivery-server")
	if listServer["authorization_confirmed"] != true {
		t.Fatalf("GET /servers omitted confirmed delivery: %#v", listServer)
	}
	if pageServer["authorization_confirmed"] != true {
		t.Fatalf("servers page-data omitted confirmed delivery: %#v", pageServer)
	}
	if dashboardServer["authorization_confirmed"] != true {
		t.Fatalf("dashboard page-data omitted confirmed delivery: %#v", dashboardServer)
	}
}

func firstNamedServer(t *testing.T, raw any, name string) map[string]any {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("servers = %#v", raw)
	}
	for _, item := range items {
		server, ok := item.(map[string]any)
		if ok && server["name"] == name {
			return server
		}
	}
	t.Fatalf("server %q not found in %#v", name, raw)
	return nil
}

func TestAnnotateServerDeliveryStatusUsesConstantQueries(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	var servers []model.Server
	for i := 0; i < 8; i++ {
		item := &model.Server{Name: "delivery-const-" + string(rune('a'+i)), ListenIP: "0.0.0.0", PortRangeStart: 20000 + i*10, PortRangeEnd: 20009 + i*10, Status: model.ServerOnline}
		if err := db.CreateServer(ctx, item); err != nil {
			t.Fatal(err)
		}
		servers = append(servers, *item)
	}
	before := db.SQLStatementCount()
	srv.annotateServerDeliveryStatus(ctx, servers)
	if delta := db.SQLStatementCount() - before; delta != 3 {
		t.Fatalf("SQL statements = %d, want 3 list queries", delta)
	}
	if !servers[0].AuthorizationFastLane || !servers[7].RuntimeUsersEnabled {
		t.Fatalf("delivery flags were not applied: %#v", servers[0])
	}
}

func TestDeliveryAndLaneFieldsShareOneLedgerLoad(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	item := &model.Server{Name: "lane-share", ListenIP: "0.0.0.0", PortRangeStart: 21000, PortRangeEnd: 21010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, item); err != nil {
		t.Fatal(err)
	}
	servers := []model.Server{*item}
	views := []map[string]any{{}}
	states := []store.ConfigurationSyncState{{ServerID: item.ID}}
	before := db.SQLStatementCount()
	lanes := srv.loadServerDeliveryLaneStates(ctx)
	srv.annotateServerDeliveryStatusFromLaneStates(ctx, servers, lanes)
	srv.attachLaneFieldsFromLaneStates(ctx, views, states, lanes)
	if delta := db.SQLStatementCount() - before; delta != 3 {
		t.Fatalf("shared lane load SQL statements = %d, want 3", delta)
	}
	if views[0]["desired_state"] == nil || views[0]["effective_state"] == nil {
		t.Fatalf("lane fields missing: %#v", views[0])
	}
}

func TestWithTrafficStatusDoesNotInsertPeriods(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	h, token := loginTestAdmin(t, db)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "member", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	user := created["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	key, start, end := trafficWindow(time.Now(), model.TrafficResetMonthly, 1, time.Time{}, time.FixedZone("Asia/Shanghai", 8*3600))
	if _, err := db.GetTrafficPeriod(ctx, userID, key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("new user already had a traffic period: %v", err)
	}

	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=users", token, nil, http.StatusOK)
	list := request(t, h, http.MethodGet, "/api/v1/ui/users", token, nil, http.StatusOK)
	if _, err := db.GetTrafficPeriod(ctx, userID, key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("listing users inserted a traffic period: %v", err)
	}
	pageUser := firstNamedUser(t, page["users"], "member")
	listUser := firstNamedUser(t, list["users"], "member")
	for _, item := range []map[string]any{pageUser, listUser} {
		if item["traffic_period_key"] != key || item["traffic_quota_state"] != "active" || item["traffic_used_bytes"] != float64(0) || item["traffic_period_end"] != end.Format(time.RFC3339Nano) {
			t.Fatalf("empty current period status missing: key=%v state=%v used=%v end=%v", item["traffic_period_key"], item["traffic_quota_state"], item["traffic_used_bytes"], item["traffic_period_end"])
		}
	}
	if pageUser["protected"] == true || listUser["protected"] == true {
		t.Fatalf("member marked protected: page=%#v list=%#v", pageUser, listUser)
	}
	admin := firstNamedUser(t, page["users"], "admin")
	if admin["protected"] != true {
		t.Fatalf("bootstrap admin not protected: %#v", admin)
	}

	period, err := db.EnsureTrafficPeriod(ctx, userID, key, start, end, 0)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "traffic-list", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.AddTrafficReports(ctx, []model.TrafficReport{{
		ReportID: "list-traffic", ServerID: server.ID, UserID: userID, PeriodKey: period.PeriodKey,
		Upload: 100, Download: 40, StartedAt: now, EndedAt: now,
	}}, period); err != nil {
		t.Fatal(err)
	}
	refreshed := request(t, h, http.MethodGet, "/api/v1/ui/users", token, nil, http.StatusOK)
	member := firstNamedUser(t, refreshed["users"], "member")
	if member["traffic_used_bytes"] != float64(140) || member["traffic_period_key"] != key {
		t.Fatalf("existing period bytes not shown: %#v want key %q", member, key)
	}
}

func firstNamedUser(t *testing.T, raw any, username string) map[string]any {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("users = %#v", raw)
	}
	for _, item := range items {
		user, ok := item.(map[string]any)
		if ok && user["username"] == username {
			return user
		}
	}
	t.Fatalf("user %q not found in %#v", username, raw)
	return nil
}
