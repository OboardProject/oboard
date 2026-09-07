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
