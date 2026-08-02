package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRealtimeBrokerFiltersAndCoalescesResources(t *testing.T) {
	broker := newRealtimeBroker()
	viewer, _, ok := broker.subscribe(model.RoleViewer)
	if !ok {
		t.Fatal("viewer subscription failed")
	}
	broker.publish("settings")
	select {
	case <-viewer.wake:
		t.Fatal("viewer was notified about an administrator resource")
	default:
	}
	broker.publish("subscriptions", "account", "subscriptions")
	<-viewer.wake
	message, ok := viewer.drain()
	if !ok || message.Type != "invalidate" || !slices.Equal(message.Resources, []string{"account", "subscriptions"}) {
		t.Fatalf("unexpected coalesced event: %#v", message)
	}
	for range 65 {
		broker.publish("subscriptions")
	}
	<-viewer.wake
	message, ok = viewer.drain()
	if !ok || message.Type != "resync_required" {
		t.Fatalf("slow client event = %#v, want resync_required", message)
	}
}

func TestRealtimeInvalidationPreservesAPIV2ReadSemantics(t *testing.T) {
	app := &Server{realtime: newRealtimeBroker()}
	client, _, ok := app.realtime.subscribe(model.RoleAdmin)
	if !ok {
		t.Fatal("subscription failed")
	}
	handler := app.realtimeInvalidation(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v2/query", nil))
	select {
	case <-client.wake:
		t.Fatal("capability query emitted a mutation event")
	default:
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v2/changesets/cs_1/apply", nil))
	<-client.wake
	event, ok := client.drain()
	if !ok || event.Type != "invalidate" || !slices.Equal(event.Resources, []string{"all"}) {
		t.Fatalf("changeset event = %#v", event)
	}
}

func TestUIRealtimeEventsRequireCookieAndSameOrigin(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/ui/events"
	if conn, response, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil {
		conn.Close()
		t.Fatal("unauthenticated websocket unexpectedly connected")
	} else if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %#v, err=%v", response, err)
	}

	token, cookie, _ := realtimeLogin(t, server.URL)
	header := http.Header{"Cookie": []string{cookie.String()}, "Origin": []string{"https://evil.example"}}
	if conn, response, err := websocket.DefaultDialer.Dial(wsURL, header); err == nil {
		conn.Close()
		t.Fatal("cross-origin websocket unexpectedly connected")
	} else if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %#v, err=%v", response, err)
	}

	header = http.Header{"Cookie": []string{cookie.String()}, "Origin": []string{server.URL}}
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var ready realtimeMessage
	if err := conn.ReadJSON(&ready); err != nil {
		t.Fatal(err)
	}
	if ready.Type != "ready" || ready.Protocol != realtimeProtocolVersion {
		t.Fatalf("ready event = %#v", ready)
	}

	body := bytes.NewBufferString(`{"name":"live-node","listen_ip":"0.0.0.0","port_range_start":10000,"port_range_end":10100}`)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v2/ui/servers", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create server status = %d", response.StatusCode)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var event realtimeMessage
	if err := conn.ReadJSON(&event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "invalidate" || !slices.Contains(event.Resources, "servers") || !slices.Contains(event.Resources, "tasks") {
		t.Fatalf("server mutation event = %#v", event)
	}
}

func TestUIRealtimeEventsRespectBasePath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := New(db, "test-secret", "", "/panel", nil)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	_, cookie, _ := realtimeLogin(t, server.URL+"/panel")
	header := http.Header{"Cookie": []string{cookie.String()}, "Origin": []string{server.URL}}

	unprefixed := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/ui/events"
	if conn, response, err := websocket.DefaultDialer.Dial(unprefixed, header); err == nil {
		conn.Close()
		t.Fatal("unprefixed websocket unexpectedly connected")
	} else if response == nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("unprefixed status = %#v, err=%v", response, err)
	}
	prefixed := "ws" + strings.TrimPrefix(server.URL, "http") + "/panel/api/v2/ui/events"
	conn, _, err := websocket.DefaultDialer.Dial(prefixed, header)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestRealtimeSessionRevalidationRejectsRevocation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	token, _, _ := realtimeLogin(t, server.URL)
	user, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	valid, role := app.realtimeSessionValid(context.Background(), token, user.ID, user.SessionVersion)
	if !valid || role != model.RoleAdmin {
		t.Fatalf("valid session = %t/%q", valid, role)
	}
	if err := db.RevokeUserSession(context.Background(), user.ID, security.HashSecret(token), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	valid, _ = app.realtimeSessionValid(context.Background(), token, user.ID, user.SessionVersion)
	if valid {
		t.Fatal("revoked realtime session remained valid")
	}
}

func realtimeLogin(t *testing.T, baseURL string) (string, *http.Cookie, string) {
	t.Helper()
	postJSON := func(path string, value any) (*http.Response, map[string]any) {
		body, _ := json.Marshal(value)
		response, err := http.Post(baseURL+path, "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		return response, result
	}
	response, _ := postJSON("/api/v2/ui/auth/bootstrap", map[string]any{"username": "admin", "password": "very-secure-password"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("bootstrap status = %d", response.StatusCode)
	}
	response, result := postJSON("/api/v2/ui/auth/login", map[string]any{"username": "admin", "password": "very-secure-password"})
	if response.StatusCode != http.StatusOK || len(response.Cookies()) != 1 {
		t.Fatalf("login status/cookies = %d/%d", response.StatusCode, len(response.Cookies()))
	}
	token, _ := result["token"].(string)
	csrf, _ := result["csrf_token"].(string)
	return token, response.Cookies()[0], csrf
}
