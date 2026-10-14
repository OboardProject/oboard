package controller

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestTrafficForwardingPageCombinesForwardsAndTunnels(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	first := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010, "renewal_cycle": "monthly"}, http.StatusCreated)
	firstID := int64(first["server"].(map[string]any)["id"].(float64))
	second := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s2", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20010, "renewal_cycle": "monthly"}, http.StatusCreated)
	secondID := int64(second["server"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/port-forwards", token, map[string]any{"name": "s1-to-s2", "source_server_id": firstID, "target_server_id": secondID, "listen_ip": "0.0.0.0", "listen_port": 443, "target_port": 8443, "protocol": "tcp", "backend": "auto", "priority": 10, "config_json": "{}"}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/tunnels", token, map[string]any{"name": "ssh-s1-to-s2", "source_server_id": firstID, "target_server_id": secondID, "type": "ssh", "priority": 20, "config_json": `{"user":"root"}`}, http.StatusCreated)

	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=traffic-forwarding", token, nil, http.StatusOK)
	for _, key := range []string{"servers", "port_forwards", "port_forward_probes", "tunnels"} {
		if _, ok := page[key]; !ok {
			t.Fatalf("traffic-forwarding page is missing %s: %#v", key, page)
		}
	}
	if len(page["port_forwards"].([]any)) != 1 || len(page["tunnels"].([]any)) != 1 {
		t.Fatalf("traffic-forwarding page lists forwards=%#v tunnels=%#v", page["port_forwards"], page["tunnels"])
	}
}
