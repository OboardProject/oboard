package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

// TestSubscriptionPullReusesRoutingSnapshot pins the two properties a client
// refresh depends on: an unchanged configuration is not re-read per pull, and a
// configuration change is still visible on the very next pull.
func TestSubscriptionPullReusesRoutingSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "pull-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	token := created["subscription_token"].(string)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", adminToken, map[string]any{"name": "pull-node", "entry_ip_mode": "custom", "entry_address": "198.51.100.11", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))

	pull := func() {
		t.Helper()
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("pull status=%d body=%s", response.Code, response.Body.String())
		}
	}

	pull()
	before := db.SQLStatementCount()
	pull()
	warm := db.SQLStatementCount() - before
	// A routing change must invalidate the reused snapshot.
	request(t, h, http.MethodPost, "/api/v1/ui/inbounds", adminToken, map[string]any{
		"server_id": serverID, "name": "pull-inbound", "kind": "ss-2022-128", "listen_ip": "0.0.0.0", "port": 10001,
		"enabled": true,
	}, http.StatusCreated)
	inbound := request(t, h, http.MethodGet, "/api/v1/ui/inbounds", adminToken, nil, http.StatusOK)["inbounds"].([]any)[0].(map[string]any)
	inboundID := int64(inbound["id"].(float64))
	before = db.SQLStatementCount()
	pull()
	cold := db.SQLStatementCount() - before
	if warm >= cold {
		t.Fatalf("a pull against unchanged routing costs as much as one after a change: warm=%d cold=%d", warm, cold)
	}
	// The pull itself must have rebuilt against the new revision rather than
	// rendering from the configuration it had already cached.
	current := srv.routingSnapshotCache.Load()
	if current == nil {
		t.Fatal("the pull did not leave a routing snapshot behind")
	}
	if _, ok := current.inboundsByID[inboundID]; !ok {
		t.Fatal("the first pull after the change rendered from the superseded configuration")
	}
}
