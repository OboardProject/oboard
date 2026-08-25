package controller

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"
)

func TestStaticAssetsUseImmutableCache(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	staticDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staticDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "assets", "icon-hash.png"), []byte("icon"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("index"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", staticDir).Handler()

	asset := httptest.NewRecorder()
	h.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, "/assets/icon-hash.png", nil))
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", got)
	}

	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/subscriptions", nil))
	if got := page.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("page Cache-Control = %q", got)
	}
	csp := page.Header().Get("Content-Security-Policy")
	for _, source := range []string{"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com", "font-src 'self' https://fonts.gstatic.com", "script-src 'self'"} {
		if !strings.Contains(csp, source) {
			t.Errorf("Content-Security-Policy missing %q: %s", source, csp)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("Content-Security-Policy unexpectedly permits inline scripts: %s", csp)
	}
}

func TestServerDNSCanBeSavedTestedAndDeployedOnDemand(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	ctx := context.Background()
	first := &model.Server{Name: "dns-edge", AgentID: "dns-agent", AgentTokenHash: security.HashSecret("dns-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, Status: model.ServerOnline}
	second := &model.Server{Name: "other-edge", AgentID: "other-agent", AgentTokenHash: security.HashSecret("other-token"), ListenIP: "0.0.0.0", PortRangeStart: 11000, PortRangeEnd: 11100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, second); err != nil {
		t.Fatal(err)
	}
	policy, err := db.GetServerDNSPolicy(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, _ := db.GetDNSList(ctx, policy.EncryptedListID)
	bootstrap, _ := db.GetDNSList(ctx, policy.BootstrapListID)
	benchmark := request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/servers/%d/dns-test", first.ID), token, map[string]any{"action": "test_and_apply"}, http.StatusAccepted)
	task := benchmark["task"].(map[string]any)
	if task["type"] != model.AgentTaskTypeBenchmarkDNS {
		t.Fatalf("dns task = %#v", task)
	}
	run := benchmark["run"].(map[string]any)
	requestID := run["request_id"].(string)
	result := model.DNSBenchmarkResult{
		ReportID: "report-one", RequestID: requestID, PolicyRevision: policy.Revision,
		EncryptedListID: encrypted.ID, EncryptedListRevision: encrypted.Revision,
		BootstrapListID: bootstrap.ID, BootstrapListRevision: bootstrap.Revision,
		Encrypted: model.DNSBenchmarkGroup{
			Items:    []model.DNSBenchmarkItem{{Tag: encrypted.Candidates[1].Tag, LatencyMS: 12}, {Tag: encrypted.Candidates[0].Tag, LatencyMS: 18}},
			BestTags: []string{encrypted.Candidates[1].Tag, encrypted.Candidates[0].Tag},
		},
		Bootstrap: model.DNSBenchmarkGroup{
			Items:    []model.DNSBenchmarkItem{{Tag: bootstrap.Candidates[1].Tag, LatencyMS: 8}, {Tag: bootstrap.Candidates[0].Tag, LatencyMS: 9}},
			BestTags: []string{bootstrap.Candidates[1].Tag, bootstrap.Candidates[0].Tag},
		},
	}
	resultBody, _ := json.Marshal(result)
	resultRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agent/dns-benchmarks", bytes.NewReader(resultBody))
	resultRequest.Header.Set("content-type", "application/json")
	resultRequest.Header.Set("X-Agent-ID", first.AgentID)
	resultRequest.Header.Set("Authorization", "Bearer dns-token")
	resultResponse := httptest.NewRecorder()
	h.ServeHTTP(resultResponse, resultRequest)
	if resultResponse.Code != http.StatusOK {
		t.Fatalf("dns result status=%d body=%s", resultResponse.Code, resultResponse.Body.String())
	}
	stored, err := db.GetServerDNSPolicy(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.EncryptedSelected) != 2 || stored.EncryptedSelected[0].Tag != encrypted.Candidates[1].Tag || len(stored.BootstrapSelected) != 2 || stored.BootstrapSelected[0].Tag != bootstrap.Candidates[1].Tag {
		t.Fatalf("automatic dns selection = %#v", stored)
	}
	results, err := db.ListDNSBenchmarkResults(ctx, first.ID, 10)
	if err != nil || len(results) != 1 || len(results[0].Encrypted.BestTags) != 2 || len(results[0].Bootstrap.BestTags) != 2 {
		t.Fatalf("stored dns results = %#v, err=%v", results, err)
	}
	tasks, err := db.ListTasks(ctx, 20)
	if err != nil || len(tasks) < 2 || tasks[0].Type != model.AgentTaskTypeApplyCoreConfig || tasks[0].ServerID != first.ID {
		t.Fatalf("targeted dns apply tasks = %#v, err=%v", tasks, err)
	}

	fallbackBenchmark := request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/servers/%d/dns-test", first.ID), token, map[string]any{"action": "test_and_apply"}, http.StatusAccepted)
	fallbackRun := fallbackBenchmark["run"].(map[string]any)
	fallbackResult := model.DNSBenchmarkResult{
		ReportID: "report-fallback", RequestID: fallbackRun["request_id"].(string), PolicyRevision: policy.Revision,
		EncryptedListID: encrypted.ID, EncryptedListRevision: encrypted.Revision,
		BootstrapListID: bootstrap.ID, BootstrapListRevision: bootstrap.Revision,
		Encrypted: model.DNSBenchmarkGroup{Items: []model.DNSBenchmarkItem{{Tag: encrypted.Candidates[0].Tag, LatencyMS: 2000, Error: "timeout"}}},
		Bootstrap: model.DNSBenchmarkGroup{Items: []model.DNSBenchmarkItem{{Tag: bootstrap.Candidates[0].Tag, LatencyMS: 2000, Error: "timeout"}}},
	}
	fallbackBody, _ := json.Marshal(fallbackResult)
	fallbackRequest := httptest.NewRequest(http.MethodPost, "/api/v1/agent/dns-benchmarks", bytes.NewReader(fallbackBody))
	fallbackRequest.Header.Set("content-type", "application/json")
	fallbackRequest.Header.Set("X-Agent-ID", first.AgentID)
	fallbackRequest.Header.Set("Authorization", "Bearer dns-token")
	fallbackResponse := httptest.NewRecorder()
	h.ServeHTTP(fallbackResponse, fallbackRequest)
	if fallbackResponse.Code != http.StatusOK {
		t.Fatalf("fallback dns result status=%d body=%s", fallbackResponse.Code, fallbackResponse.Body.String())
	}
	stored, err = db.GetServerDNSPolicy(ctx, first.ID)
	if err != nil || stored.LastError != model.DNSBenchmarkNoUsableCandidatesError {
		t.Fatalf("fallback dns policy = %#v, err=%v", stored, err)
	}
	tasks, err = db.ListTasks(ctx, 20)
	if err != nil || len(tasks) < 4 || tasks[0].Type != model.AgentTaskTypeApplyCoreConfig {
		t.Fatalf("fallback dns apply tasks = %#v, err=%v", tasks, err)
	}
	var fallbackPayload model.ApplyCoreConfigTaskPayload
	if err := json.Unmarshal([]byte(tasks[0].PayloadJSON), &fallbackPayload); err != nil {
		t.Fatal(err)
	}
	var fallbackConfig map[string]any
	if err := json.Unmarshal([]byte(fallbackPayload.Config), &fallbackConfig); err != nil {
		t.Fatal(err)
	}
	fallbackDNS := fallbackConfig["dns"].(map[string]any)
	fallbackServers := fallbackDNS["servers"].([]any)
	if fallbackDNS["final"] != "local" || len(fallbackServers) != 1 || fallbackServers[0].(map[string]any)["type"] != "local" {
		t.Fatalf("fallback core dns = %#v", fallbackDNS)
	}
	fallbackResolver := fallbackConfig["route"].(map[string]any)["default_domain_resolver"].(map[string]any)
	if fallbackResolver["server"] != "local" {
		t.Fatalf("fallback default resolver = %#v", fallbackResolver)
	}
}

func TestDNSListCRUDValidationAndDefaultProtection(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	listed := request(t, h, http.MethodGet, "/api/v1/ui/dns-lists", token, nil, http.StatusOK)
	defaults := listed["dns_lists"].([]any)
	if len(defaults) != 2 || defaults[0].(map[string]any)["protected"] != true || defaults[1].(map[string]any)["protected"] != true {
		t.Fatalf("default dns lists = %#v", defaults)
	}
	updatedDefault := request(t, h, http.MethodPut, fmt.Sprintf("/api/v1/ui/dns-lists/%d", int64(defaults[0].(map[string]any)["id"].(float64))), token, map[string]any{
		"name": "changed", "kind": defaults[0].(map[string]any)["kind"], "enabled": false, "candidates": defaults[0].(map[string]any)["candidates"],
	}, http.StatusOK)["dns_list"].(map[string]any)
	if updatedDefault["name"] != "changed" || updatedDefault["protected"] != true || updatedDefault["enabled"] != true {
		t.Fatalf("updated default dns list = %#v", updatedDefault)
	}
	request(t, h, http.MethodDelete, fmt.Sprintf("/api/v1/ui/dns-lists/%d", int64(updatedDefault["id"].(float64))), token, nil, http.StatusConflict)
	request(t, h, http.MethodPost, "/api/v1/ui/dns-lists", token, map[string]any{
		"name": "invalid bootstrap", "kind": "bootstrap", "candidates": []map[string]any{
			{"tag": "domain", "transport": "udp", "server": "dns.example", "port": 53},
			{"tag": "public", "transport": "tcp", "server": "8.8.8.8", "port": 53},
		},
	}, http.StatusBadRequest)
	created := request(t, h, http.MethodPost, "/api/v1/ui/dns-lists", token, map[string]any{
		"name": "mixed encrypted", "kind": "encrypted", "candidates": []map[string]any{
			{"tag": "one", "transport": "doh", "server": "global.novaxns.one", "port": 443, "path": "/@hockey2168/dns-query", "tls_name": "global.novaxns.one"},
			{"tag": "two", "transport": "doq", "server": "dns.quad9.net", "port": 853, "tls_name": "dns.quad9.net"},
		},
	}, http.StatusCreated)
	list := created["dns_list"].(map[string]any)
	if list["revision"] != float64(1) || len(list["candidates"].([]any)) != 2 {
		t.Fatalf("created dns list = %#v", list)
	}
	request(t, h, http.MethodDelete, fmt.Sprintf("/api/v1/ui/dns-lists/%d", int64(list["id"].(float64))), token, nil, http.StatusOK)
}

func TestNormalizeBasePath(t *testing.T) {
	for input, want := range map[string]string{
		"":            "",
		"/":           "",
		"/abc":        "/abc",
		" /abc-123/ ": "/abc-123",
		"/one/two":    "/one/two",
	} {
		got, err := NormalizeBasePath(input)
		if err != nil || got != want {
			t.Errorf("NormalizeBasePath(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"abc", "/abc//def", "/abc/../def", "/abc/%64ef", "/abc?x=1", "/中文"} {
		if got, err := NormalizeBasePath(input); err == nil {
			t.Errorf("NormalizeBasePath(%q) = %q; want error", input, got)
		}
	}
	srv := &Server{basePath: "/hidden-panel"}
	if got, err := srv.normalizeControllerURL("https://panel.example.com"); err != nil || got != "https://panel.example.com/hidden-panel" {
		t.Fatalf("normalizeControllerURL root = %q, %v", got, err)
	}
	if _, err := srv.normalizeControllerURL("https://panel.example.com/wrong"); err == nil {
		t.Fatal("normalizeControllerURL accepted a path outside the configured base path")
	}
}

func TestBasePathProtectsEveryControllerSurface(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	root := t.TempDir()
	staticDir := filepath.Join(root, "web", "dist")
	downloadDir := filepath.Join(root, "downloads")
	if err := os.MkdirAll(filepath.Join(staticDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte(`<!doctype html><html><head><base href="/" /></head><body>panel</body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "assets", "app.js"), []byte("asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "release-manifest.json"), []byte(`{"version":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(downloadDir, "oboard-subscription-relay-linux-amd64.tar.gz"), []byte("relay-package"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OBOARD_DOWNLOADS", downloadDir)
	if err := db.SetSetting(context.Background(), "controller_url", "http://example.com/hidden-panel"); err != nil {
		t.Fatal(err)
	}

	h := New(db, "test-secret", staticDir, "/hidden-panel", nil).Handler()
	for _, path := range []string{
		"/", "/healthz", "/api/v1/ui/version", "/api/v1/agent/connect",
		"/assets/app.js", "/install/agent.sh", "/downloads/release-manifest.json",
		"/hidden-panel-other/healthz", "/hidden-panel//healthz",
		"/hidden-panel/downloads", "/hidden-panel/api/v1/subscriptions",
	} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d; want 404", path, response.Code)
		}
		if location := response.Header().Get("Location"); location != "" {
			t.Errorf("GET %s unexpectedly redirects to %q", path, location)
		}
	}

	checks := []struct {
		path        string
		wantStatus  int
		wantContent string
	}{
		{"/hidden-panel", http.StatusOK, `<base href="/hidden-panel/" />`},
		{"/hidden-panel/dashboard", http.StatusOK, `<base href="/hidden-panel/" />`},
		{"/hidden-panel/assets/app.js", http.StatusOK, "asset"},
		{"/hidden-panel/healthz", http.StatusOK, `"ok":true`},
		{"/hidden-panel/api/v1/ui/version", http.StatusOK, `"api_prefix":"/hidden-panel/api/v1"`},
		{"/hidden-panel/api/v1/agent/connect", http.StatusUnauthorized, "invalid agent credentials"},
		{"/hidden-panel/install/agent.sh", http.StatusOK, "http://example.com/hidden-panel"},
		{"/hidden-panel/downloads/release-manifest.json", http.StatusOK, `"version":"test"`},
		{"/hidden-panel/downloads/oboard-subscription-relay-linux-amd64.tar.gz", http.StatusOK, "relay-package"},
	}
	for _, check := range checks {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "http://example.com"+check.path, nil)
		h.ServeHTTP(response, request)
		if response.Code != check.wantStatus {
			t.Errorf("GET %s status = %d; want %d; body=%s", check.path, response.Code, check.wantStatus, response.Body.String())
			continue
		}
		if !strings.Contains(response.Body.String(), check.wantContent) {
			t.Errorf("GET %s body does not contain %q", check.path, check.wantContent)
		}
	}
}

func TestTrafficRuntimePoliciesIncludeUnlimitedUsers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	server := &model.Server{Name: "traffic", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "traffic-user", ProxyPassword: "password", SubscriptionToken: "traffic-subscription"}
	if err := db.CreateUser(ctx, &user); err != nil {
		t.Fatal(err)
	}
	filtered, err := srv.trafficRuntimePolicies(ctx, server.ID, []model.User{user}, map[int64]bool{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 0 {
		t.Fatalf("non-accounting downstream server received user policy: %#v", filtered)
	}
	policies, err := srv.trafficRuntimePolicies(ctx, server.ID, []model.User{user}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, ok := policies[user.ID]
	if !ok || !policy.Billable || policy.UserID != user.ID {
		t.Fatalf("unlimited user policy missing: %#v", policies)
	}
	if policy.PeriodKey == "" || policy.PeriodStart == "" || policy.PeriodEnd == "" || policy.Timezone == "" {
		t.Fatalf("unlimited user period metadata missing: %#v", policy)
	}
}

func TestSSHInboundRequiresConfirmationAndBuildsPerUserPlan(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	server := &model.Server{Name: "ssh-edge", ListenIP: "0.0.0.0", PublicIPv4: "203.0.113.20", PortRangeStart: 20000, PortRangeEnd: 20100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "alice-id", ProxyPassword: "alice-pass"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}

	base := map[string]any{"server_id": server.ID, "name": "ssh proxy", "protocol": "ssh", "listen_ip": "0.0.0.0", "port": 2222, "entry_ip_mode": "ipv4", "config_json": `{"access_mode":"restricted_proxy"}`, "enabled": true}
	request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, base, http.StatusBadRequest)
	base["config_json"] = `{"exposure_confirmed":true,"exposure_confirmation_version":"ssh-inbound-v1","access_mode":"restricted_proxy"}`
	created := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, base, http.StatusCreated)
	inboundID := int64(created["inbound"].(map[string]any)["id"].(float64))
	directPath := &model.ProxyPath{Kind: model.ProxyPathKindDirect, Name: "direct", InboundID: inboundID, Secret: "direct-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, directPath); err != nil {
		t.Fatal(err)
	}
	grantTestPlanNode(t, db, user.ID, model.AssignableNodeProxyPath, directPath.ID)

	data, err := db.FullRoutingConfigData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policies, err := srv.trafficRuntimePolicies(ctx, server.ID, data.Users, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildSSHInboundPlan(2, *server, data, nil, snapshotBindingsFromData(data), policies)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Inbounds) != 1 || len(plan.Inbounds[0].Users) != 1 || plan.Inbounds[0].Users[0].Username != sshLoginName(*user, directPath.ID) || plan.Inbounds[0].Users[0].Password != user.ProxyPassword || plan.Inbounds[0].Users[0].RouteKind != "kernel" || plan.Inbounds[0].Users[0].RouteInboundTag != "in-"+strconv.FormatInt(inboundID, 10) || plan.Inbounds[0].Users[0].RouteAuthUser != user.Username+"__oboard_path_"+strconv.FormatInt(directPath.ID, 10) {
		t.Fatalf("SSH inbound plan = %#v", plan)
	}
	if _, ok := plan.Inbounds[0].Policies["user:"+strconv.FormatInt(user.ID, 10)]; !ok {
		t.Fatalf("SSH plan lacks user traffic policy: %#v", plan.Inbounds[0].Policies)
	}
}

func TestSSHInboundPlanBuildsImplicitDirectRouteForStandaloneGrant(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "ixp", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "alice-id", ProxyPassword: "alice-pass", SubscriptionToken: "implicit-ssh-subscription-token"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "standalone-ssh", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, EntryIPMode: model.EntryIPModeIPv4, ConfigJSON: `{"exposure_confirmed":true,"exposure_confirmation_version":"ssh-inbound-v1","access_mode":"restricted_proxy"}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanNode(t, db, user.ID, model.AssignableNodeInbound, inbound.ID)
	data, err := db.FullRoutingConfigData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := srv.buildAccessSnapshot(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildSSHInboundPlan(3, *server, data, snapshot.InboundUserBindings(), snapshot.ProxyPathUserBindings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Inbounds) != 1 || len(plan.Inbounds[0].Users) != 1 {
		t.Fatalf("implicit SSH direct plan = %#v", plan)
	}
	planned := plan.Inbounds[0].Users[0]
	pathID := core.SSHDirectBranchPathID(inbound.ID)
	if planned.PathID != pathID || planned.Username != sshLoginName(*user, pathID) || planned.Password != user.ProxyPassword || planned.RouteKind != "kernel" || planned.RouteInboundTag != "in-"+strconv.FormatInt(inbound.ID, 10) || planned.RouteAuthUser != user.Username+"__oboard_path_"+strconv.FormatInt(pathID, 10) {
		t.Fatalf("implicit SSH direct user = %#v", planned)
	}

	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostPublicKey, err := ssh.NewPublicKey(hostPrivateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	hostIdentity := model.SSHServerHostKey{ServerID: server.ID, PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostPublicKey))), Fingerprint: ssh.FingerprintSHA256(hostPublicKey), PlanDigest: sshInboundPlanDigest(plan), ConfigVersion: plan.Version}
	deployments, err := srv.sshPasswordDeploymentsFromPlan(server.ID, plan)
	if err != nil || len(deployments) != 1 {
		t.Fatalf("implicit SSH deployments = %#v, err=%v", deployments, err)
	}
	if err := db.ApplySSHDeploymentState(ctx, hostIdentity, deployments); err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(model.DeploymentTaskPayload{Version: plan.Version, SSHInbounds: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: string(payloadJSON), Status: "succeeded", ResultJSON: `{}`, ConfigVersion: plan.Version, Nonce: "implicit-ssh-baseline"}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	srv.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/implicit-ssh-subscription-token?format=sing-box", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("implicit SSH subscription status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var document struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	sshOutbounds := make([]map[string]any, 0, len(document.Outbounds))
	for _, outbound := range document.Outbounds {
		if outbound["type"] == "ssh" {
			sshOutbounds = append(sshOutbounds, outbound)
		}
	}
	if len(sshOutbounds) != 1 || sshOutbounds[0]["user"] != planned.Username {
		t.Fatalf("implicit SSH subscription = %#v", document.Outbounds)
	}
	workspaceNodes, _, err := srv.workspaceAllNodes(ctx, *user)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaceNodes) != 1 || workspaceNodes[0].Key != core.NodeKeyOf(model.AssignableNodeInbound, inbound.ID) || workspaceNodes[0].Raw["type"] != "ssh" {
		t.Fatalf("implicit SSH workspace nodes = %#v", workspaceNodes)
	}
}

func TestSSHInboundPlanExpandsDeviceCredentialsPerRoute(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "ssh-device", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "alice-id", ProxyPassword: "alice-pass"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	device := &model.UserDevice{ID: "dev_test", DeviceIDHash: "0123456789abcdef0123456789abcdef", UserID: user.ID, Name: "phone", TokenHash: "device-token-hash", TokenPrefix: "obd_test", CredentialEpoch: 2}
	if err := db.CreateUserDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "managed-ssh", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, EntryIPMode: model.EntryIPModeIPv4, ConfigJSON: `{"exposure_confirmed":true,"exposure_confirmation_version":"ssh-inbound-v1","access_mode":"restricted_proxy"}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Kind: model.ProxyPathKindDirect, Name: "direct", InboundID: inbound.ID, Secret: "direct-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	grantTestPlanNode(t, db, user.ID, model.AssignableNodeProxyPath, path.ID)
	data, err := db.FullRoutingConfigData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildSSHInboundPlan(7, *server, data, nil, snapshotBindingsFromData(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Inbounds) != 1 || len(plan.Inbounds[0].Users) != 2 {
		t.Fatalf("SSH device plan = %#v", plan)
	}
	var legacy, bound *model.SSHInboundUser
	for index := range plan.Inbounds[0].Users {
		candidate := &plan.Inbounds[0].Users[index]
		if candidate.DeviceIDHash == "" {
			legacy = candidate
		} else {
			bound = candidate
		}
	}
	deviceUser := core.UserForDevice(*user, *device)
	expectedDeviceCredential := core.UserCredentialForRoute(deviceUser, inbound.ID, path.ID, model.ProtocolSSH)
	if legacy == nil || legacy.Password != user.ProxyPassword || bound == nil || bound.DeviceIDHash != device.DeviceIDHash || bound.CredentialEpoch != device.CredentialEpoch || bound.CredentialStatus != "active" || bound.Password != expectedDeviceCredential.ProxyPassword || bound.Password == legacy.Password {
		t.Fatalf("SSH expanded credentials legacy=%#v device=%#v", legacy, bound)
	}
	deployments, err := newTestServer(db, "test-secret", "").sshPasswordDeploymentsFromPlan(server.ID, plan)
	if err != nil || len(deployments) != 2 {
		t.Fatalf("SSH identity deployments = %#v, err=%v", deployments, err)
	}
}

func TestSSHInboundPlanListenFollowsDetectedFamilies(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		server model.Server
		want   string
	}{
		{name: "dual-stack", server: model.Server{Name: "ssh-dual", ListenIP: "0.0.0.0", PublicIPv4: "203.0.113.20", PublicIPv6: "2001:db8::20", PortRangeStart: 20000, PortRangeEnd: 20100, Status: model.ServerOnline}, want: "::"},
		{name: "ipv4-only", server: model.Server{Name: "ssh-v4", ListenIP: "0.0.0.0", PublicIPv4: "203.0.113.21", PortRangeStart: 20000, PortRangeEnd: 20100, Status: model.ServerOnline}, want: "0.0.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := tc.server
			if err := db.CreateServer(ctx, &server); err != nil {
				t.Fatal(err)
			}
			inbound := &model.Inbound{ServerID: server.ID, Name: "ssh proxy", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, EntryIPMode: model.EntryIPModeAuto, ConfigJSON: `{"exposure_confirmed":true,"exposure_confirmation_version":"ssh-inbound-v1","access_mode":"restricted_proxy"}`, Enabled: true}
			if err := db.CreateInbound(ctx, inbound); err != nil {
				t.Fatal(err)
			}
			data, err := db.FullRoutingConfigData(ctx)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := buildSSHInboundPlan(2, server, data, nil, snapshotBindingsFromData(data), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Inbounds) != 1 || plan.Inbounds[0].ListenIP != tc.want {
				t.Fatalf("SSH inbound plan = %#v, want listen %q", plan.Inbounds, tc.want)
			}
		})
	}
}

func TestApplyDeploymentSSHStatePersistsOnlyValidatedTaskCredentials(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "ssh-edge", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "alice-id", ProxyPassword: "alice-pass"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostPublicKey, err := ssh.NewPublicKey(hostPrivateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	hostIdentity := model.SSHServerHostKey{PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostPublicKey))), Fingerprint: ssh.FingerprintSHA256(hostPublicKey)}
	payload := model.DeploymentTaskPayload{Version: 19, SSHInbounds: model.SSHInboundPlan{Version: 19, Inbounds: []model.SSHInbound{{
		InboundID: 31, ServerID: server.ID, Enabled: true,
		Users: []model.SSHInboundUser{{UserID: user.ID, Username: sshLoginName(*user, 9), Password: user.ProxyPassword, PathID: 9, RouteKind: "direct", Enabled: true}},
	}}}}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	task := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, ConfigVersion: 19, PayloadJSON: string(payloadJSON)}
	resultJSON := func(publicKey, fingerprint string) string {
		t.Helper()
		encoded, err := json.Marshal(map[string]any{"steps": []any{map[string]any{
			"key": "ssh_inbounds", "status": "succeeded", "result": map[string]any{
				"host_public_key": publicKey, "host_key_fingerprint": fingerprint,
				"users": map[string]string{strconv.FormatInt(user.ID, 10): "SHA256:agent-supplied-value-must-be-ignored"},
			},
		}}})
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}

	if err := srv.applyDeploymentSSHState(ctx, server.ID, task, resultJSON(hostIdentity.PublicKey, hostIdentity.Fingerprint)); err != nil {
		t.Fatal(err)
	}
	hostKey, err := db.GetSSHServerHostKey(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hostKey.PublicKey != hostIdentity.PublicKey || hostKey.Fingerprint != hostIdentity.Fingerprint || hostKey.ConfigVersion != 19 {
		t.Fatalf("persisted SSH host key = %#v", hostKey)
	}
	deployments, err := db.ListSSHPasswordDeploymentsForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	expectedDeployments, err := srv.sshPasswordDeploymentsFromPlan(server.ID, payload.SSHInbounds)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 1 || len(expectedDeployments) != 1 || deployments[0].PasswordDigest != expectedDeployments[0].PasswordDigest || deployments[0].ConfigVersion != 19 {
		t.Fatalf("persisted SSH password deployments = %#v", deployments)
	}

	for _, invalid := range []struct {
		name        string
		publicKey   string
		fingerprint string
	}{
		{name: "invalid public key", publicKey: "not-an-ssh-public-key", fingerprint: hostIdentity.Fingerprint},
		{name: "mismatched fingerprint", publicKey: hostIdentity.PublicKey, fingerprint: "SHA256:mismatch"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			if err := srv.applyDeploymentSSHState(ctx, server.ID, task, resultJSON(invalid.publicKey, invalid.fingerprint)); err == nil {
				t.Fatal("invalid SSH deployment report was accepted")
			}
			unchanged, err := db.GetSSHServerHostKey(ctx, server.ID)
			if err != nil {
				t.Fatal(err)
			}
			if unchanged.PublicKey != hostIdentity.PublicKey || unchanged.Fingerprint != hostIdentity.Fingerprint {
				t.Fatalf("invalid report changed persisted host identity: %#v", unchanged)
			}
		})
	}

	if err := srv.applyDeploymentSSHState(ctx, server.ID, task, `{"steps":[]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetSSHServerHostKey(ctx, server.ID); err == nil {
		t.Fatal("missing SSH deployment report retained the host key")
	}
	deployments, err = db.ListSSHPasswordDeploymentsForUser(ctx, user.ID)
	if err != nil || len(deployments) != 0 {
		t.Fatalf("missing SSH deployment report retained password state: %#v, err=%v", deployments, err)
	}
}

func TestSSHSubscriptionAppearsOnlyAfterMatchingDeployment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "tokyo", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "alice-id", ProxyPassword: "alice-pass", SubscriptionToken: "ssh-subscription-token"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "managed-ssh", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, EntryIPMode: model.EntryIPModeIPv4, ConfigJSON: `{"exposure_confirmed":true,"exposure_confirmation_version":"ssh-inbound-v1","access_mode":"restricted_proxy"}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Kind: model.ProxyPathKindDirect, Name: "direct", InboundID: inbound.ID, Secret: "direct-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	grantTestPlanNode(t, db, user.ID, model.AssignableNodeProxyPath, path.ID)
	_, hostPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostPublicKey, err := ssh.NewPublicKey(hostPrivateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	hostIdentity := model.SSHServerHostKey{PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostPublicKey))), Fingerprint: ssh.FingerprintSHA256(hostPublicKey)}
	config, err := db.FullRoutingConfigData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := buildSSHInboundPlan(0, *server, config, nil, snapshotBindingsFromData(config), nil)
	if err != nil {
		t.Fatal(err)
	}
	planDigest := sshInboundPlanDigest(plan)
	expectedDeployments, err := srv.sshPasswordDeploymentsFromPlan(server.ID, plan)
	if err != nil || len(expectedDeployments) != 1 {
		t.Fatalf("SSH password deployments = %#v, err=%v", expectedDeployments, err)
	}
	handler := srv.Handler()
	readSubscription := func() []map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/ssh-subscription-token?format=sing-box", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("subscription status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var document struct {
			Outbounds []map[string]any `json:"outbounds"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		nodes := make([]map[string]any, 0, len(document.Outbounds))
		for _, outbound := range document.Outbounds {
			if outbound["type"] == "ssh" {
				nodes = append(nodes, outbound)
			}
		}
		return nodes
	}
	readWorkspaceSSHNodes := func() []core.SubscriptionNode {
		t.Helper()
		nodes, _, err := srv.workspaceAllNodes(ctx, *user)
		if err != nil {
			t.Fatal(err)
		}
		sshNodes := make([]core.SubscriptionNode, 0, len(nodes))
		for _, node := range nodes {
			if node.Raw["type"] == "ssh" {
				sshNodes = append(sshNodes, node)
			}
		}
		return sshNodes
	}
	if nodes := readSubscription(); len(nodes) != 0 {
		t.Fatalf("SSH subscription appeared before deployment: %#v", nodes)
	}
	staleDeployments := append([]model.SSHPasswordDeployment(nil), expectedDeployments...)
	staleDeployments[0].PasswordDigest = "stale-password-digest"
	if err := db.ApplySSHDeploymentState(ctx, model.SSHServerHostKey{ServerID: server.ID, PublicKey: hostIdentity.PublicKey, Fingerprint: hostIdentity.Fingerprint, ConfigVersion: 23}, staleDeployments); err != nil {
		t.Fatal(err)
	}
	if nodes := readSubscription(); len(nodes) != 0 {
		t.Fatalf("SSH subscription appeared for stale deployed password: %#v", nodes)
	}
	if err := db.ApplySSHDeploymentState(ctx, model.SSHServerHostKey{ServerID: server.ID, PublicKey: hostIdentity.PublicKey, Fingerprint: hostIdentity.Fingerprint, PlanDigest: planDigest, ConfigVersion: 24}, expectedDeployments); err != nil {
		t.Fatal(err)
	}
	payloadJSON, err := json.Marshal(model.DeploymentTaskPayload{Version: 24, SSHInbounds: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: string(payloadJSON), Status: "succeeded", ResultJSON: `{}`, ConfigVersion: 24, Nonce: "ssh-subscription-baseline"}); err != nil {
		t.Fatal(err)
	}
	nodes := readSubscription()
	if len(nodes) != 1 || nodes[0]["type"] != "ssh" || nodes[0]["user"] != sshLoginName(*user, path.ID) || nodes[0]["server"] != server.PublicIPv4 || nodes[0]["password"] != user.ProxyPassword {
		t.Fatalf("matching SSH subscription = %#v", nodes)
	}
	workspaceNodes := readWorkspaceSSHNodes()
	if len(workspaceNodes) != 1 || workspaceNodes[0].Raw["username"] != sshLoginName(*user, path.ID) {
		t.Fatalf("matching SSH workspace nodes = %#v", workspaceNodes)
	}

	other := &model.User{Username: "bob", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "bob-id", ProxyPassword: "bob-pass", SubscriptionToken: "bob-subscription-token"}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	if len(config.SubscriptionPlans) != 1 {
		t.Fatalf("subscription plans = %#v", config.SubscriptionPlans)
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: other.ID, PlanID: config.SubscriptionPlans[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if nodes := readSubscription(); len(nodes) != 1 {
		t.Fatalf("unrelated SSH authorization hid the deployed node: %#v", nodes)
	}
	server.InterfaceIPv6 = "2400:3200::10"
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if nodes := readSubscription(); len(nodes) != 1 {
		t.Fatalf("runtime listener derivation hid the deployed node: %#v", nodes)
	}
	egernRecorder := httptest.NewRecorder()
	handler.ServeHTTP(egernRecorder, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/ssh-subscription-token?format=egern", nil))
	if egernRecorder.Code != http.StatusOK {
		t.Fatalf("Egern subscription status=%d body=%s", egernRecorder.Code, egernRecorder.Body.String())
	}
	var egernDocument struct {
		Proxies []map[string]map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(egernRecorder.Body.Bytes(), &egernDocument); err != nil {
		t.Fatal(err)
	}
	if len(egernDocument.Proxies) != 1 {
		t.Fatalf("Egern SSH proxies = %#v", egernDocument.Proxies)
	}
	egernSSH := egernDocument.Proxies[0]["ssh"]
	hostKeys, hostKeysOK := egernSSH["host_keys"].([]any)
	if egernSSH["username"] != sshLoginName(*user, path.ID) || egernSSH["server"] != server.PublicIPv4 || egernSSH["password"] != user.ProxyPassword || !hostKeysOK || len(hostKeys) != 1 || hostKeys[0] != hostIdentity.PublicKey {
		t.Fatalf("Egern SSH subscription = %#v", egernDocument.Proxies[0])
	}
}

func TestSSHInboundPlanDigestTracksRoutesWithoutDerivingFromPasswords(t *testing.T) {
	plan := model.SSHInboundPlan{Inbounds: []model.SSHInbound{{
		InboundID: 1,
		ServerID:  2,
		ListenIP:  "0.0.0.0",
		Address:   "203.0.113.10",
		Port:      2222,
		Enabled:   true,
		Users: []model.SSHInboundUser{{
			UserID: 3, Username: "u123456789012-p4", Password: "first-password", PathID: 4, RouteKind: "direct", Enabled: true,
		}},
	}}}
	original := sshInboundPlanDigest(plan)
	plan.Inbounds[0].Users[0].Password = "second-password"
	if got := sshInboundPlanDigest(plan); got != original {
		t.Fatalf("password-only change altered plan digest: %q != %q", got, original)
	}
	plan.Inbounds[0].Users[0].RouteKind = "outbound"
	plan.Inbounds[0].Users[0].OutboundTag = "path-4-step-1"
	if got := sshInboundPlanDigest(plan); got == original {
		t.Fatal("route change did not alter plan digest")
	}
}

func TestSSHSubscriptionDeploymentMatchingIsScopedToListenerAndIdentity(t *testing.T) {
	alice := model.SSHInboundUser{UserID: 3, Username: "u123456789012-p4", Password: "alice-password", PathID: 4, RouteKind: "kernel", RouteInboundTag: "in-1", RouteAuthUser: "alice__oboard_path_4", Enabled: true}
	bob := model.SSHInboundUser{UserID: 5, Username: "u987654321098-p4", Password: "bob-password", PathID: 4, RouteKind: "kernel", RouteInboundTag: "in-1", RouteAuthUser: "bob__oboard_path_4", Enabled: true}
	deployed := model.SSHInboundPlan{Inbounds: []model.SSHInbound{{InboundID: 1, ServerID: 2, ListenIP: "0.0.0.0", Address: "203.0.113.10", Port: 2222, Enabled: true, Users: []model.SSHInboundUser{alice, bob}}}}
	aliceIdentity := sshPasswordDeploymentIdentityForPlanUser(alice)

	current := deployed
	current.Inbounds = append([]model.SSHInbound(nil), deployed.Inbounds...)
	current.Inbounds[0].Users = []model.SSHInboundUser{alice}
	current.Inbounds[0].ListenIP = "::"
	current.Inbounds[0].Address = "ssh.example.com"
	if sshInboundListenerPlanDigest(current) != sshInboundListenerPlanDigest(deployed) {
		t.Fatal("runtime address or unrelated user change altered listener readiness")
	}
	if !matchingSSHIdentityRoutePlan(current, deployed, aliceIdentity) {
		t.Fatal("unrelated user change altered Alice's deployed route readiness")
	}

	current.Inbounds[0].Users[0].RouteAuthUser = "alice__oboard_path_9"
	if matchingSSHIdentityRoutePlan(current, deployed, aliceIdentity) {
		t.Fatal("Alice's undeployed route change remained ready")
	}
	current.Inbounds[0].Port = 2200
	if sshInboundListenerPlanDigest(current) == sshInboundListenerPlanDigest(deployed) {
		t.Fatal("undeployed listener port change remained ready")
	}
}

func TestDeploymentMTURunsOnlyOnFirstUseOrPolicyChange(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "mtu-edge", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline, MTUMode: model.MTUModeDetect, MTUProbeHost: "1.1.1.1", MTUProbePort: 443, MTUOverheadBytes: 80}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	plan := mtuPlanFromServer(100, *server, model.MTUModeDetect)
	if run, err := srv.shouldRunDeploymentMTU(ctx, plan); err != nil || !run {
		t.Fatalf("first MTU deployment run = %t, err=%v; want true", run, err)
	}
	if err := db.AddMTUDetectionResult(ctx, model.MTUDetectionResult{ServerID: server.ID, Mode: model.MTUModeDetect, TargetHost: plan.TargetHost, TargetPort: plan.TargetPort, RecommendedMTU: 1400, ResultJSON: `{"overhead_bytes":80,"desired_mtu":0}`}); err != nil {
		t.Fatal(err)
	}
	plan.DesiredMTU = 1400
	if run, err := srv.shouldRunDeploymentMTU(ctx, plan); err != nil || run {
		t.Fatalf("unchanged MTU deployment run = %t, err=%v; want false", run, err)
	}
	storedServer, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedServer.MTUValue != 1400 {
		t.Fatalf("MTU result did not persist the effective value: %d", storedServer.MTUValue)
	}
	changed := plan
	changed.TargetHost = "8.8.8.8"
	if run, err := srv.shouldRunDeploymentMTU(ctx, changed); err != nil || !run {
		t.Fatalf("changed MTU target run = %t, err=%v; want true", run, err)
	}
	changed = plan
	changed.Mode = model.MTUModeApply
	if run, err := srv.shouldRunDeploymentMTU(ctx, changed); err != nil || !run {
		t.Fatalf("changed MTU mode run = %t, err=%v; want true", run, err)
	}
	changed = plan
	changed.DesiredMTU = 1450
	if run, err := srv.shouldRunDeploymentMTU(ctx, changed); err != nil || !run {
		t.Fatalf("changed desired MTU run = %t, err=%v; want true", run, err)
	}
	applied := plan
	applied.Mode = model.MTUModeApply
	if err := db.AddMTUDetectionResult(ctx, model.MTUDetectionResult{ServerID: server.ID, Mode: model.MTUModeApply, TargetHost: applied.TargetHost, TargetPort: applied.TargetPort, RecommendedMTU: 1400, AppliedMTU: 1400, ResultJSON: `{"overhead_bytes":80,"desired_mtu":1400}`}); err != nil {
		t.Fatal(err)
	}
	if run, err := srv.shouldRunDeploymentMTU(ctx, applied); err != nil || run {
		t.Fatalf("unchanged applied MTU run = %t, err=%v; want false", run, err)
	}
	if err := db.AddMTUDetectionResult(ctx, model.MTUDetectionResult{ServerID: server.ID, Mode: model.MTUModeApply, TargetHost: applied.TargetHost, TargetPort: applied.TargetPort, RecommendedMTU: 1400, Error: "operation not permitted", ResultJSON: `{"overhead_bytes":80,"desired_mtu":1400}`}); err != nil {
		t.Fatal(err)
	}
	if run, err := srv.shouldRunDeploymentMTU(ctx, applied); err != nil || run {
		t.Fatalf("unchanged failed MTU run = %t, err=%v; want false", run, err)
	}
	changed = applied
	changed.TargetPort = 8443
	if run, err := srv.shouldRunDeploymentMTU(ctx, changed); err != nil || !run {
		t.Fatalf("changed MTU policy after failure run = %t, err=%v; want true", run, err)
	}
}

func TestMTUPlanInfersIPv6TargetForAutoServer(t *testing.T) {
	server := model.Server{
		ID:           8,
		IPStack:      model.IPStackAuto,
		PublicIPv6:   "2001:db8::8",
		MTUProbeHost: "1.1.1.1",
		MTUProbePort: 443,
	}
	plan := mtuPlanFromServer(100, server, model.MTUModeApply)
	if plan.TargetHost != "2606:4700:4700::1111" {
		t.Fatalf("MTU target = %q, want IPv6 Cloudflare resolver", plan.TargetHost)
	}
}

func TestServerCreationDefaultsAndExplicitOverrides(t *testing.T) {
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

	initialPage := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=servers", token, nil, http.StatusOK)
	initialDefaults := initialPage["server_creation_defaults"].(map[string]any)
	if initialDefaults["mtu_mode"] != "detect" || initialDefaults["bbr_enabled"] != true {
		t.Fatalf("initial server creation defaults = %#v", initialDefaults)
	}
	initialCreated := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "initial-server"}, http.StatusCreated)
	initialID := int64(initialCreated["server"].(map[string]any)["id"].(float64))
	initialServer, err := db.GetServer(ctx, initialID)
	if err != nil {
		t.Fatal(err)
	}
	if initialServer.MTUMode != model.MTUModeDetect || !initialServer.BBREnabled {
		t.Fatalf("initial server policy = %#v", initialServer)
	}

	request(t, h, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{
		"server_default_mtu_mode":    "apply",
		"server_default_bbr_enabled": false,
	}, http.StatusOK)
	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=servers", token, nil, http.StatusOK)
	defaults := page["server_creation_defaults"].(map[string]any)
	if defaults["mtu_mode"] != "apply" || defaults["bbr_enabled"] != false {
		t.Fatalf("server creation defaults = %#v", defaults)
	}
	proxyPage := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=proxy-paths", token, nil, http.StatusOK)
	proxyDefaults := proxyPage["server_creation_defaults"].(map[string]any)
	if proxyDefaults["mtu_mode"] != "apply" || proxyDefaults["bbr_enabled"] != false {
		t.Fatalf("proxy-path server creation defaults = %#v", proxyDefaults)
	}

	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "default-server"}, http.StatusCreated)
	defaultID := int64(created["server"].(map[string]any)["id"].(float64))
	defaultServer, err := db.GetServer(ctx, defaultID)
	if err != nil {
		t.Fatal(err)
	}
	if defaultServer.MTUMode != model.MTUModeApply || defaultServer.BBREnabled {
		t.Fatalf("default server policy = %#v", defaultServer)
	}

	overridden := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{
		"name": "override-server", "mtu_mode": "disabled", "bbr_enabled": true,
	}, http.StatusCreated)
	overrideID := int64(overridden["server"].(map[string]any)["id"].(float64))
	overrideServer, err := db.GetServer(ctx, overrideID)
	if err != nil {
		t.Fatal(err)
	}
	if overrideServer.MTUMode != model.MTUModeDisabled || !overrideServer.BBREnabled {
		t.Fatalf("explicit server policy = %#v", overrideServer)
	}

	request(t, h, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{"server_default_mtu_mode": "always"}, http.StatusBadRequest)
}

func TestDeploymentFailureDismissalPersistsUntilNextDeployment(t *testing.T) {
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
	server := &model.Server{Name: "deployment-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: "{}", Status: "failed", ResultJSON: `{"error":"test"}`, ConfigVersion: 100, Nonce: "failed-100"}); err != nil {
		t.Fatal(err)
	}

	page := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=servers", token, nil, http.StatusOK)
	status := page["deployment_status"].(map[string]any)
	if status["failure_dismissed"] != false {
		t.Fatalf("initial deployment status = %#v", status)
	}
	dismissed := request(t, h, http.MethodPost, "/api/v1/ui/deployments/100/dismiss-failure", token, map[string]any{}, http.StatusOK)
	dismissedStatus := dismissed["deployment_status"].(map[string]any)
	if dismissedStatus["failure_dismissed"] != true || dismissedStatus["failure_dismissed_at"] == nil {
		t.Fatalf("dismiss response = %#v", dismissedStatus)
	}
	page = request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=proxy-paths", token, nil, http.StatusOK)
	status = page["deployment_status"].(map[string]any)
	if status["failure_dismissed"] != true {
		t.Fatalf("dismissal was not persisted across page loads: %#v", status)
	}

	if err := db.CreateTask(ctx, &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: "{}", Status: "failed", ResultJSON: `{"error":"next"}`, ConfigVersion: 101, Nonce: "failed-101"}); err != nil {
		t.Fatal(err)
	}
	page = request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=servers", token, nil, http.StatusOK)
	status = page["deployment_status"].(map[string]any)
	if status["config_version"] != float64(101) || status["failure_dismissed"] != false {
		t.Fatalf("next deployment inherited previous dismissal: %#v", status)
	}
	dismissedLatest := request(t, h, http.MethodPost, "/api/v1/ui/deployments/100/dismiss-failure", token, map[string]any{}, http.StatusOK)
	latestStatus := dismissedLatest["deployment_status"].(map[string]any)
	if latestStatus["config_version"] != float64(101) || latestStatus["failure_dismissed"] != true {
		t.Fatalf("stale dismissal did not dismiss latest failure: %#v", latestStatus)
	}
	page = request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=dashboard", token, nil, http.StatusOK)
	status = page["deployment_status"].(map[string]any)
	if status["config_version"] != float64(101) || status["failure_dismissed"] != true {
		t.Fatalf("latest dismissal was not persisted: %#v", status)
	}
}

func TestPublicBaseURLPrefersConfiguredControllerURL(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "https://panel.example.com"); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	req := httptest.NewRequest(http.MethodGet, "http://attacker.example/install/agent.sh", nil)
	if got, err := srv.publicBaseURL(context.Background()); err != nil || got != "https://panel.example.com" {
		t.Fatalf("publicBaseURL = %q, err=%v; want configured controller URL", got, err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("install script status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "DEFAULT_BASE_URL='https://panel.example.com'") {
		t.Fatalf("install script did not use configured controller URL: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "attacker.example") {
		t.Fatalf("install script still used request Host: %s", rec.Body.String())
	}
}

func TestAgentScriptsRequireConfiguredControllerURL(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	for _, path := range []string{"/install/agent.sh", "/install/agent-self-update.sh"} {
		req := httptest.NewRequest(http.MethodGet, "http://attacker.example"+path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusPreconditionFailed {
			t.Fatalf("%s status = %d, want %d", path, rec.Code, http.StatusPreconditionFailed)
		}
		if strings.Contains(rec.Body.String(), "attacker.example") {
			t.Fatalf("%s reflected request Host: %s", path, rec.Body.String())
		}
	}
}

func TestAgentInstallScriptsUseLowSpaceTempFallback(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "http://example.com"); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	for _, path := range []string{"/install/agent.sh", "/install/agent-self-update.sh"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		script := rec.Body.String()
		for _, shellName := range []string{"bash", "dash"} {
			shell, err := exec.LookPath(shellName)
			if err != nil {
				continue
			}
			cmd := exec.Command(shell, "-n")
			cmd.Stdin = strings.NewReader(script)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%s %s syntax: %v: %s", path, shellName, err, output)
			}
		}
		for _, want := range []string{
			"make_update_tmp", "OBOARD_TMPDIR", "/var/tmp", "/run", "65536", "df -i",
			"pkg_install", "ensure_base_tools", "ensure_release_verifier",
			"detect_virt_hint", "ca-certificates", "coreutils", "command -v install", "microdnf", "zypper", "pacman",
		} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s missing %q", path, want)
			}
		}
		candidateOrder := `for base in "${OBOARD_TMPDIR:-}" /var/tmp "$STATE_DIR" /tmp /run; do`
		if !strings.Contains(script, candidateOrder) {
			t.Fatalf("%s does not prefer disk-backed update directories: missing %q", path, candidateOrder)
		}
		if path == "/install/agent.sh" {
			for _, want := range []string{
				"ACTION=${OBOARD_ACTION:-${1:-install}}",
				"DEFAULT_BASE_URL='http://example.com'",
				"ALLOW_PANEL_UPDATE=${OBOARD_ALLOW_PANEL_UPDATE:-1}",
				"UPDATE_SOURCE=${OBOARD_UPDATE_SOURCE:-panel}",
				"OBOARD_PURGE=${OBOARD_PURGE:-1}",
				"StandardOutput=append:/var/log/oboard-agent.log",
				"StandardOutput=append:/var/log/oboard-sb.log",
				"ReadWritePaths=$STATE_DIR /var/log /run",
				"# Agent reconciles tunnel prerequisites and its dedicated SSH account.\nProtectSystem=false",
				"User=root",
				"MemoryDenyWriteExecute=true",
				"RestrictSUIDSGID=true",
				"ACME_SH_VERSION=3.1.4",
				"ACME_SH_SHA256=fcabf274d4f96966ec933879ae0257266e8ef2f7d16161f14b84dd896c0cac32",
				"install_pinned_acme_sh",
				"sha256_file",
				"normalize_install_dir",
				"install_dir_from_input",
				"请输入安装目录（留空为/opt/oboard）：",
				"INSTALL_ENV_PATH=${OBOARD_AGENT_INSTALL_ENV:-/etc/oboard-agent/install.env}",
				"persist_agent_install_dir",
				"resolve_agent_install_dir",
			} {
				if !strings.Contains(script, want) {
					t.Fatalf("%s missing managed log service setting %q", path, want)
				}
			}
			if strings.Contains(script, "-enroll-token") {
				t.Fatalf("%s exposes enrollment token in process arguments", path)
			}
			uninstallPos := strings.Index(script, `if [ "$ACTION" = uninstall ]`)
			dependencyPos := strings.Index(script, "ensure_base_tools()")
			if uninstallPos < 0 || dependencyPos < 0 || uninstallPos > dependencyPos {
				t.Fatalf("%s does not handle uninstall before dependency setup", path)
			}
		}
		if !strings.Contains(script, `-h 2>&1 | grep -q -- '-verify-release'`) {
			t.Fatalf("%s does not fall back when the installed Agent lacks release verification", path)
		}
		if !strings.Contains(script, `ln -s "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/obag"`) {
			t.Fatalf("%s does not install the obag management command", path)
		}
		if !strings.Contains(script, "register_obag_path") || !strings.Contains(script, "OBOARD_PROFILE_DIR:-/etc/profile.d") || !strings.Contains(script, "oboard-agent.sh") {
			t.Fatalf("%s does not register the obag management command on PATH", path)
		}
		for _, want := range []string{"management_command=obag", "$management_command status", "$management_command check", "$management_command logs agent", "$management_command logs core"} {
			if !strings.Contains(script, want) {
				t.Fatalf("%s missing user management hint %q", path, want)
			}
		}
		if strings.Contains(script, `cp "$tmp/$agent_name" "$tmp/oboard-agent"`) || strings.Contains(script, `cp "$tmp/$core_name" "$tmp/oboard-sb"`) {
			t.Fatalf("%s still duplicates downloaded binaries", path)
		}
	}
}

func TestAgentInstallScriptACMEFallback(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "http://example.com"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/install/agent.sh", nil)
	rec := httptest.NewRecorder()
	newTestServer(db, "test-secret", "").Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("install script status = %d", rec.Code)
	}
	script := rec.Body.String()
	assertACMEInstallerBehavior(t, script)
	assertPackageManagerDispatch(t, script)
	assertInstallToolBootstrap(t, script)
	assertInstallDirectoryInputs(t, script)
}

func TestAgentInstallDirectoryPersistence(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "http://example.com"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/install/agent.sh", nil)
	rec := httptest.NewRecorder()
	newTestServer(db, "test-secret", "").Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("install script status = %d", rec.Code)
	}
	script := rec.Body.String()
	shell, err := exec.LookPath("dash")
	if err != nil {
		shell, err = exec.LookPath("sh")
	}
	if err != nil {
		t.Skip("a POSIX shell is unavailable")
	}

	source := strings.Join([]string{
		extractShellFunction(t, script, "normalize_install_dir"),
		extractShellFunction(t, script, "install_dir_from_input"),
		extractShellFunction(t, script, "configured_agent_install_dir"),
		extractShellFunction(t, script, "choose_install_dir"),
		extractShellFunction(t, script, "resolve_agent_install_dir"),
	}, "\n")
	t.Run("restore persisted directory", func(t *testing.T) {
		installEnv := filepath.Join(t.TempDir(), "install.env")
		if err := os.WriteFile(installEnv, []byte("OBOARD_INSTALL_DIR=/data/oboard/\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		harness := strings.Join([]string{
			source,
			"INSTALL_ENV_PATH=" + shellQuote(installEnv),
			"INSTALL_DIR_INPUT=",
			"INSTALL_DIR=",
			"ACTION=update",
			"resolve_agent_install_dir",
			"printf 'resolved=%s\\n' \"$INSTALL_DIR\"",
		}, "\n")
		output, err := exec.Command(shell, "-c", harness).CombinedOutput()
		if err != nil || !strings.Contains(string(output), "resolved=/data/oboard") {
			t.Fatalf("persisted Agent directory was not restored: %v\n%s", err, output)
		}
	})

	t.Run("reject directory change", func(t *testing.T) {
		installEnv := filepath.Join(t.TempDir(), "install.env")
		if err := os.WriteFile(installEnv, []byte("OBOARD_INSTALL_DIR=/data/oboard\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		harness := strings.Join([]string{
			source,
			"INSTALL_ENV_PATH=" + shellQuote(installEnv),
			"INSTALL_DIR_INPUT=/srv/oboard",
			"INSTALL_DIR=",
			"ACTION=update",
			"resolve_agent_install_dir",
		}, "\n")
		output, err := exec.Command(shell, "-c", harness).CombinedOutput()
		if err == nil || !strings.Contains(string(output), "更新或卸载时不能改为") {
			t.Fatalf("Agent install directory change was not rejected: %v\n%s", err, output)
		}
	})

	t.Run("persist root-only selection", func(t *testing.T) {
		installEnv := filepath.Join(t.TempDir(), "etc", "oboard-agent", "install.env")
		harness := strings.Join([]string{
			extractShellFunction(t, script, "persist_agent_install_dir"),
			"INSTALL_ENV_PATH=" + shellQuote(installEnv),
			"INSTALL_DIR=/data/oboard",
			"persist_agent_install_dir",
		}, "\n")
		output, err := exec.Command(shell, "-c", harness).CombinedOutput()
		if err != nil {
			t.Fatalf("persist install directory failed: %v\n%s", err, output)
		}
		content, err := os.ReadFile(installEnv)
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != "OBOARD_INSTALL_DIR=/data/oboard\n" {
			t.Fatalf("unexpected persisted install directory: %q", content)
		}
		info, err := os.Stat(installEnv)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("install directory state mode = %04o, want 0600", mode)
		}
	})
}

func TestTrafficWindowForPeriodKey(t *testing.T) {
	loc := time.FixedZone("Asia/Shanghai", 8*60*60)
	key, start, end, err := trafficWindowForPeriodKey(time.Now(), "2026-02-28", "month_day", 31, time.Time{}, loc)
	if err != nil {
		t.Fatal(err)
	}
	if key != "2026-02-28" || start.Format("2006-01-02") != key || end.Format("2006-01-02") != "2026-03-31" {
		t.Fatalf("unexpected clamped period: key=%s start=%s end=%s", key, start, end)
	}
	if _, _, _, err := trafficWindowForPeriodKey(time.Now(), "2026-02-27", "month_day", 31, time.Time{}, loc); err == nil {
		t.Fatal("period key outside the reset cycle should be rejected")
	}
}

func TestControllerFormalAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	version := request(t, h, http.MethodGet, "/api/v1/ui/version", token, nil, http.StatusOK)
	if version["name"] != "OBoard" {
		t.Fatalf("unexpected version response: %#v", version)
	}

	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverPayload := createdServer["server"].(map[string]any)
	if _, ok := serverPayload["ssh_port"]; ok {
		t.Fatalf("server API still exposes ssh_port: %#v", serverPayload)
	}
	if serverPayload["mtu_mode"] != "detect" || serverPayload["mtu_probe_host"] == "" {
		t.Fatalf("server MTU defaults missing: %#v", serverPayload)
	}
	if serverPayload["monitoring_mode"] != "lightweight" || serverPayload["traffic_reset_mode"] != "monthly" || serverPayload["latency_probe_enabled"] != true {
		t.Fatalf("server telemetry defaults missing: %#v", serverPayload)
	}
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	createdServer2 := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s2", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20010}, http.StatusCreated)
	server2ID := int64(createdServer2["server"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodGet, "/api/v1/ui/servers/"+itoa(serverID), token, nil, http.StatusOK)
	metrics := request(t, h, http.MethodGet, "/api/v1/ui/servers/"+itoa(serverID)+"/metrics", token, nil, http.StatusOK)
	if _, ok := metrics["server_metrics"]; !ok {
		t.Fatalf("server metrics missing: %#v", metrics)
	}
	tasks := request(t, h, http.MethodGet, "/api/v1/ui/servers/"+itoa(serverID)+"/tasks", token, nil, http.StatusOK)
	if _, ok := tasks["tasks"]; !ok {
		t.Fatalf("tasks missing: %#v", tasks)
	}
	createdForward := request(t, h, http.MethodPost, "/api/v1/ui/port-forwards", token, map[string]any{"name": "s1-to-s2", "source_server_id": serverID, "target_server_id": server2ID, "listen_ip": "0.0.0.0", "listen_port": 443, "target_port": 8443, "protocol": "tcp", "backend": "auto", "priority": 10, "config_json": "{}"}, http.StatusCreated)
	forwardID := int64(createdForward["port_forward"].(map[string]any)["id"].(float64))
	if forwardID == 0 {
		t.Fatalf("port forward missing id: %#v", createdForward)
	}
	externalForward := request(t, h, http.MethodPost, "/api/v1/ui/port-forwards", token, map[string]any{"name": "s1-to-external", "source_server_id": serverID, "listen_ip": "0.0.0.0", "listen_port": 444, "target_address": "198.51.100.80", "target_port": 9443, "protocol": "tcp", "backend": "auto", "priority": 20, "config_json": "{}"}, http.StatusCreated)
	externalPayload := externalForward["port_forward"].(map[string]any)
	if externalPayload["target_address"] != "198.51.100.80" {
		t.Fatalf("external port forward target missing: %#v", externalPayload)
	}
	if _, exists := externalPayload["target_server_id"]; exists {
		t.Fatalf("external port forward unexpectedly exposes a managed target: %#v", externalPayload)
	}
	probe := request(t, h, http.MethodPost, "/api/v1/ui/port-forwards/"+itoa(forwardID)+"/probe", token, map[string]any{}, http.StatusAccepted)
	if probe["task"].(map[string]any)["type"] != "probe_port_forwards" {
		t.Fatalf("unexpected probe task: %#v", probe)
	}
	mtuTask := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(serverID)+"/mtu-detect", token, map[string]any{"target_host": "1.1.1.1", "target_port": 443, "overhead_bytes": 80, "sample_count": 2}, http.StatusAccepted)
	if mtuTask["task"].(map[string]any)["type"] != "detect_mtu" || mtuTask["plan"].(map[string]any)["overhead_bytes"].(float64) != 80 {
		t.Fatalf("unexpected MTU task: %#v", mtuTask)
	}
	logTask := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(serverID)+"/logs", token, map[string]any{"lines": 80, "services": "agent"}, http.StatusAccepted)
	if logTask["task"].(map[string]any)["type"] != "collect_logs" {
		t.Fatalf("unexpected log task: %#v", logTask)
	}

	createdTunnel := request(t, h, http.MethodPost, "/api/v1/ui/tunnels", token, map[string]any{"name": "ssh-s1-to-s2", "source_server_id": serverID, "target_server_id": server2ID, "type": "ssh", "priority": 20, "config_json": `{"user":"root"}`}, http.StatusCreated)
	if createdTunnel["tunnel"].(map[string]any)["id"] == nil {
		t.Fatalf("tunnel missing id: %#v", createdTunnel)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/tunnels", token, map[string]any{"name": "cycle", "source_server_id": server2ID, "target_server_id": serverID, "type": "ssh", "priority": 20, "config_json": `{"user":"root"}`}, http.StatusBadRequest)

	deployment := request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	taskItems := deployment["tasks"].([]any)
	if len(taskItems) != 2 {
		t.Fatalf("deployment created %d tasks for 2 servers, want one per server: %#v", len(taskItems), deployment)
	}
	foundSourceDeployment := false
	for _, item := range taskItems {
		task := item.(map[string]any)
		if task["type"] != "apply_deployment" {
			t.Fatalf("deployment created split task: %#v", task)
		}
		if int64(task["server_id"].(float64)) != serverID {
			continue
		}
		var payload model.DeploymentTaskPayload
		if err := json.Unmarshal([]byte(task["payload_json"].(string)), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.PortForwards.Rules) != 2 || payload.PortForwardProbe == nil || len(payload.PortForwardProbe.Rules) != 2 {
			t.Fatalf("deployment payload missing forward apply/probe plans: %#v", payload)
		}
		if len(payload.Tunnels.Tunnels) != 1 || payload.MTUDetection == nil {
			t.Fatalf("deployment payload missing tunnel/MTU plans: %#v", payload)
		}
		foundSourceDeployment = true
	}
	if !foundSourceDeployment {
		t.Fatalf("deployment missing source server task: %#v", deployment)
	}

	createdUser := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	userID := int64(createdUser["user"].(map[string]any)["id"].(float64))
	usersPage := request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=users", token, nil, http.StatusOK)
	pageUsers := usersPage["users"].([]any)
	if len(pageUsers) == 0 || pageUsers[0].(map[string]any)["traffic_period_key"] == nil || pageUsers[0].(map[string]any)["traffic_quota_state"] != "active" {
		t.Fatalf("users page missing current traffic period status: %#v", usersPage)
	}
	createdGroup := request(t, h, http.MethodPost, "/api/v1/ui/user-groups", token, map[string]any{"name": "vip", "description": "VIP", "enabled": true}, http.StatusCreated)
	groupID := int64(createdGroup["user_group"].(map[string]any)["id"].(float64))
	patchedGroup := request(t, h, http.MethodPatch, "/api/v1/ui/user-groups/"+itoa(groupID), token, map[string]any{"description": "VIP users"}, http.StatusOK)
	if _, exists := patchedGroup["user_group"].(map[string]any)["speed_limit_mbps"]; exists {
		t.Fatalf("user group still exposes traffic limits: %#v", patchedGroup)
	}
	patchedUser := request(t, h, http.MethodPatch, "/api/v1/ui/users/"+itoa(userID), token, map[string]any{"speed_limit_mbps": 50, "traffic_limit_bytes": 268435456}, http.StatusOK)
	if patchedUser["user"].(map[string]any)["speed_limit_mbps"].(float64) != 50 {
		t.Fatalf("user limit patch missing: %#v", patchedUser)
	}
	passwordOnly := request(t, h, http.MethodPatch, "/api/v1/ui/users/"+itoa(userID), token, map[string]any{"password": "another-long-password"}, http.StatusOK)
	if passwordOnly["user"].(map[string]any)["speed_limit_mbps"].(float64) != 50 {
		t.Fatalf("password-only patch should preserve user limits: %#v", passwordOnly)
	}
	rotated := request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/rotate", token, map[string]any{}, http.StatusOK)
	if rotated["subscription_token"] == "" {
		t.Fatal("rotated token missing")
	}
	revoked := request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/revoke", token, map[string]any{}, http.StatusOK)
	if revoked["subscription_token"] != "" {
		t.Fatalf("expected revoked token: %#v", revoked)
	}
}

func TestServerMonitoringPolicyAndHealthSanitization(t *testing.T) {
	mode, interval := serverMonitoringPolicy(&model.Server{MonitoringMode: "standard"})
	if mode != "standard" || interval != 10*time.Second {
		t.Fatalf("standard policy mode=%s interval=%s", mode, interval)
	}
	mode, interval = serverMonitoringPolicy(&model.Server{MonitoringMode: "lightweight"})
	if mode != "lightweight" || interval != 20*time.Second {
		t.Fatalf("lightweight policy mode=%s interval=%s", mode, interval)
	}
	report := model.HealthReport{CPUUsagePercent: 180, MemoryUsedBytes: 200, MemoryTotalBytes: 100, DiskBytes: 200, DiskTotalBytes: 100, TCPConnectionCount: 10_000_001, UDPConnectionCount: 10_000_001, ProcessCount: 10_000_001, NetworkUploadBPS: 101 << 30, Timestamp: time.Now().Add(-time.Hour)}
	sanitizeServerHealthReport(&report)
	if report.CPUUsagePercent != 100 || report.MemoryUsedBytes != 100 {
		t.Fatalf("resource sanitization = %#v", report)
	}
	if report.NetworkUploadBPS != 0 {
		t.Fatalf("network rate was not bounded: %d", report.NetworkUploadBPS)
	}
	if report.DiskBytes != 100 || report.TCPConnectionCount != 0 || report.UDPConnectionCount != 0 || report.ProcessCount != 0 {
		t.Fatalf("extended resource sanitization = %#v", report)
	}
	if time.Since(report.Timestamp) > 5*time.Second {
		t.Fatalf("timestamp was not normalized: %s", report.Timestamp)
	}
}

func TestValidateServerListenMode(t *testing.T) {
	server := &model.Server{Name: "s1", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := validateServer(server); err != nil {
		t.Fatalf("empty listen_mode must default: %v", err)
	}
	if server.ListenMode != model.ListenModeAuto {
		t.Fatalf("listen_mode default = %q, want auto", server.ListenMode)
	}
	server.ListenMode = model.ListenModeDual
	if err := validateServer(server); err != nil {
		t.Fatalf("dual listen_mode rejected: %v", err)
	}
	server.ListenMode = model.ListenModeIPv4Only
	if err := validateServer(server); err != nil {
		t.Fatalf("ipv4_only listen_mode rejected: %v", err)
	}
	server.ListenMode = model.ListenMode("broken")
	if err := validateServer(server); err == nil {
		t.Fatal("invalid listen_mode accepted")
	}
}

func TestApplyDetectedEntryIPsRecordsInterfaceIPv6(t *testing.T) {
	server := &model.Server{}
	health := model.HealthReport{InterfaceIPv6: "2400:3200::10"}
	applyDetectedEntryIPs(server, health, "")
	if server.InterfaceIPv6 != "2400:3200::10" {
		t.Fatalf("interface ipv6 = %q, want recorded", server.InterfaceIPv6)
	}
	health = model.HealthReport{InterfaceIPv6: "fd00::1"}
	applyDetectedEntryIPs(server, health, "")
	if server.InterfaceIPv6 != "" {
		t.Fatalf("ULA interface ipv6 = %q, want cleared", server.InterfaceIPv6)
	}
}

func TestSubscriptionBurnAfterReadLifecycle(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "one-time", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	user := created["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	oneTimeToken := user["subscription_token"].(string)

	policy := request(t, h, http.MethodPatch, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/policy", adminToken, map[string]any{"burn_after_read": true}, http.StatusOK)
	if policy["user"].(map[string]any)["subscription_burn_after_read"] != true {
		t.Fatalf("burn-after-read policy was not enabled: %#v", policy)
	}

	fetch := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	first := fetch(oneTimeToken)
	if first.Code != http.StatusOK || first.Body.Len() == 0 {
		t.Fatalf("first one-time fetch status=%d body=%s", first.Code, first.Body.String())
	}
	if first.Header().Get("Cache-Control") != "no-store, max-age=0" || first.Header().Get("X-OBoard-Subscription") != "burned-after-read" {
		t.Fatalf("one-time response security headers missing: %#v", first.Header())
	}
	second := fetch(oneTimeToken)
	if second.Code != http.StatusNotFound {
		t.Fatalf("second one-time fetch status=%d body=%s", second.Code, second.Body.String())
	}

	stored := request(t, h, http.MethodGet, "/api/v1/ui/users/"+itoa(userID), adminToken, nil, http.StatusOK)["user"].(map[string]any)
	if stored["subscription_token"] != nil || stored["subscription_burned_at"] == nil || stored["subscription_burn_after_read"] != true {
		t.Fatalf("burned subscription state missing: %#v", stored)
	}

	rotated := request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/rotate", adminToken, map[string]any{}, http.StatusOK)
	persistentToken := rotated["subscription_token"].(string)
	request(t, h, http.MethodPatch, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/policy", adminToken, map[string]any{"burn_after_read": false}, http.StatusOK)
	if got := fetch(persistentToken); got.Code != http.StatusOK || got.Header().Get("X-OBoard-Subscription") != "" || got.Header().Get("Content-Type") != "application/json" || !strings.Contains(got.Body.String(), `"outbounds"`) {
		t.Fatalf("persistent subscription first fetch status=%d headers=%#v", got.Code, got.Header())
	}
	if got := fetch(persistentToken); got.Code != http.StatusOK {
		t.Fatalf("persistent subscription second fetch status=%d body=%s", got.Code, got.Body.String())
	}
	if got := fetch(persistentToken + "?format=shadowrocket"); got.Code != http.StatusOK || got.Header().Get("Content-Type") != "text/plain; charset=utf-8" || got.Body.String() != "" {
		t.Fatalf("Shadowrocket subscription status=%d headers=%#v body=%s", got.Code, got.Header(), got.Body.String())
	}
	if got := fetch(persistentToken + "?format=surge-mac"); got.Code != http.StatusOK || got.Header().Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("Surge Mac subscription status=%d headers=%#v body=%s", got.Code, got.Header(), got.Body.String())
	}
	if got := fetch(persistentToken + "?format=surge-mac&mihomo=maybe"); got.Code != http.StatusBadRequest {
		t.Fatalf("invalid Surge Mac mihomo mode status=%d body=%s", got.Code, got.Body.String())
	}
	if got := fetch(persistentToken + "?format=plain-json"); got.Code != http.StatusBadRequest {
		t.Fatalf("removed plain-json subscription status=%d body=%s", got.Code, got.Body.String())
	}

	if got := requestLogPath("/api/v1/subscriptions/secret-token"); got != "/api/v1/subscriptions/[redacted]" {
		t.Fatalf("subscription request log path was not redacted: %q", got)
	}
}

func TestQuickOneTimeSubscriptionDoesNotChangePersistentPolicy(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "quick-once", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	user := created["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	persistentToken := user["subscription_token"].(string)

	oneTime := request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/one-time", adminToken, map[string]any{}, http.StatusCreated)
	oneTimeToken := oneTime["subscription_token"].(string)
	if oneTimeToken == "" || oneTimeToken == persistentToken || oneTime["burn_after_read"] != true {
		t.Fatalf("unexpected quick one-time response: %#v", oneTime)
	}

	fetch := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+token, nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}
	if got := fetch(oneTimeToken); got.Code != http.StatusOK || got.Header().Get("X-OBoard-Subscription") != "burned-after-read" {
		t.Fatalf("quick one-time first fetch status=%d headers=%#v body=%s", got.Code, got.Header(), got.Body.String())
	}
	if got := fetch(oneTimeToken); got.Code != http.StatusNotFound {
		t.Fatalf("quick one-time second fetch status=%d body=%s", got.Code, got.Body.String())
	}
	for attempt := 1; attempt <= 2; attempt++ {
		if got := fetch(persistentToken); got.Code != http.StatusOK || got.Header().Get("X-OBoard-Subscription") != "" {
			t.Fatalf("persistent fetch %d status=%d headers=%#v body=%s", attempt, got.Code, got.Header(), got.Body.String())
		}
	}

	stored := request(t, h, http.MethodGet, "/api/v1/ui/users/"+itoa(userID), adminToken, nil, http.StatusOK)["user"].(map[string]any)
	if stored["subscription_token"] != persistentToken || stored["subscription_burn_after_read"] != false || stored["subscription_burned_at"] != nil {
		t.Fatalf("quick one-time link changed persistent subscription state: %#v", stored)
	}
}

func TestAgentUpdateAllowedForSelfUpdateCapableOlderBuild(t *testing.T) {
	originalBuild, originalAgentBuild := version.Build, version.AgentBuild
	t.Cleanup(func() {
		version.Build, version.AgentBuild = originalBuild, originalAgentBuild
	})
	version.Build, version.AgentBuild = "controller-build", "agent-build"

	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "http://localhost"); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "listen_ip": "0.0.0.0", "status": "online"}, http.StatusCreated)
	serverID := int64(created["server"].(map[string]any)["id"].(float64))
	server, err := db.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	server.AgentID = "agent-edge"
	server.AgentTokenHash = security.HashSecret("agent-token")
	server.AgentBuild = "20260706050530"
	if err := db.UpdateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}

	res := request(t, h, http.MethodPost, "/api/v1/ui/servers/"+itoa(serverID)+"/agent-update", token, map[string]any{}, http.StatusAccepted)
	task := res["task"].(map[string]any)
	if task["type"] != "update_agent" {
		t.Fatalf("unexpected update response: %#v", res)
	}
	var payload model.UpdateAgentTaskPayload
	if err := json.Unmarshal([]byte(task["payload_json"].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ExpectedBuild != version.AgentBuild {
		t.Fatalf("update task expected build = %q, want Agent build %q", payload.ExpectedBuild, version.AgentBuild)
	}
}

func TestAgentReconnectCompletesInterruptedUpdateTask(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	task, err := srv.queueAgentTask(context.Background(), server.ID, "update_agent", model.UpdateAgentTaskPayload{ExpectedBuild: "20260711050000"}, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	srv.completeAgentUpdateAfterReconnect(context.Background(), server.ID, "20260711050000")
	stored, err := db.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "succeeded" || !strings.Contains(stored.ResultJSON, "20260711050000") {
		t.Fatalf("interrupted update was not completed after reconnect: %#v", stored)
	}
}

func TestAgentSelfUpdateScriptSupportsPOSIXShellAndDeferredAckRestart(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "controller_url", "http://localhost"); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	req := httptest.NewRequest(http.MethodGet, "/install/agent-self-update.sh", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("self update script status = %d", rr.Code)
	}
	script := rr.Body.String()
	for _, want := range []string{"#!/bin/sh", "OBOARD_AGENT_RESTART:-delayed", `AGENT_RESTART" = none`, "sleep 60", `data["time_sync_command"] = "auto"`, "verify_manifest_with_openssl", "command -v install", "coreutils", "OBOARD_AGENT_INSTALL_ENV", "configured_agent_install_dir", "normalize_install_dir", `data.setdefault("core_binary", install_dir + "/oboard-sb")`, `ln -s "$INSTALL_DIR/oboard-agent" "$INSTALL_DIR/obag"`, "OBoard Agent 自更新完成", "$management_command check"} {
		if !strings.Contains(script, want) {
			t.Fatalf("self update script missing %q", want)
		}
	}
	if strings.Contains(script, "set -euo pipefail") {
		t.Fatal("self update script still requires bash pipefail")
	}
	assertInstallToolBootstrap(t, script)
}

func TestInboundPortPatchAffectsDeploymentConfig(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 60000, "status": "online"}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	createdInbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "edge-vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 10443, "entry_ip_mode": "auto", "config_json": `{"tls":{"enabled":true,"reality":{"enabled":true,"handshake":{"server":"cdn.icloud-content.com","server_port":443},"private_key":"QQgMqPRP3R8xz_Bu1ejHvZIAZvHD21UkHjzpX2YODVU","short_id":"6adc1d56"},"server_name":"cdn.icloud-content.com"}}`, "enabled": true}, http.StatusCreated)
	inbound := createdInbound["inbound"].(map[string]any)
	inboundID := int64(inbound["id"].(float64))

	patched := request(t, h, http.MethodPatch, "/api/v1/ui/inbounds/"+itoa(inboundID), token, map[string]any{"server_id": serverID, "name": "edge-vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 55555, "entry_ip_mode": "auto", "config_json": inbound["config_json"], "enabled": true}, http.StatusOK)
	if got := int(patched["inbound"].(map[string]any)["port"].(float64)); got != 55555 {
		t.Fatalf("patched inbound port = %d, want 55555: %#v", got, patched)
	}
	listed := request(t, h, http.MethodGet, "/api/v1/ui/inbounds/"+itoa(inboundID), token, nil, http.StatusOK)
	if got := int(listed["inbound"].(map[string]any)["port"].(float64)); got != 55555 {
		t.Fatalf("stored inbound port = %d, want 55555: %#v", got, listed)
	}

	deployment := request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	for _, raw := range deployment["tasks"].([]any) {
		task := raw.(map[string]any)
		if task["type"] != "apply_deployment" || task["server_id"].(float64) != float64(serverID) {
			continue
		}
		var payload model.DeploymentTaskPayload
		if err := json.Unmarshal([]byte(task["payload_json"].(string)), &payload); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(payload.Config.Config) == "" {
			t.Fatalf("apply_deployment payload missing config: %#v", task)
		}
		var cfg struct {
			Inbounds []struct {
				ListenPort int `json:"listen_port"`
			} `json:"inbounds"`
		}
		if err := json.Unmarshal([]byte(payload.Config.Config), &cfg); err != nil {
			t.Fatal(err)
		}
		if len(cfg.Inbounds) != 1 || cfg.Inbounds[0].ListenPort != 55555 {
			t.Fatalf("deployment listen_port = %#v, want 55555; config=%s", cfg.Inbounds, payload.Config.Config)
		}
		return
	}
	t.Fatalf("deployment missing apply_deployment task: %#v", deployment)
}

func TestAgentTaskResultCannotCrossServer(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	serverA := &model.Server{Name: "a", AgentID: "agent-a", AgentTokenHash: security.HashSecret("token-a"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	serverB := &model.Server{Name: "b", AgentID: "agent-b", AgentTokenHash: security.HashSecret("token-b"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, serverB); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: serverB.ID, Type: "apply_core_config", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "n"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	h := newTestServer(db, "test-secret", "").Handler()
	body, _ := json.Marshal(map[string]any{"task_id": task.ID, "status": "succeeded", "result_json": "{}"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/task-results", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", "agent-a")
	req.Header.Set("Authorization", "Bearer token-a")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestRoutingExternalOutboundAndWARPPathPublicAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "v6-edge", "listen_ip": "::", "ip_stack": "auto", "public_ipv6": "2001:db8::8", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))

	badExternal := request(t, h, http.MethodPost, "/api/v1/ui/external-outbounds", token, map[string]any{"server_id": serverID, "scope": "server", "name": "bad-v4", "protocol": "vless", "target_address": "1.1.1.1", "target_port": 443, "config_json": "{}", "enabled": true}, http.StatusBadRequest)
	if badExternal["error"] == "" {
		t.Fatalf("expected IPv6-only validation error: %#v", badExternal)
	}

	imported := request(t, h, http.MethodPost, "/api/v1/ui/external-outbounds/import", token, map[string]any{"scope": "global", "content": `[{"type":"hysteria2","tag":"hy2-import","server":"edge.example.com","server_port":443}]`}, http.StatusCreated)
	external := imported["external_outbounds"].([]any)[0].(map[string]any)
	if external["protocol"] != "hy2" {
		t.Fatalf("hysteria2 import protocol = %v, want hy2", external["protocol"])
	}
	if external["region_mode"] != "auto" || external["region_status"] != core.RegionStatusUnlinked {
		t.Fatalf("imported outbound region state = %#v", external)
	}
	externalID := int64(external["id"].(float64))

	outbound := request(t, h, http.MethodPost, "/api/v1/ui/outbounds", token, map[string]any{"server_id": serverID, "name": "local-vless", "protocol": "vless", "target_address": "next.example.com", "target_port": 443, "config_json": "{}", "enabled": true}, http.StatusCreated)
	outboundID := int64(outbound["outbound"].(map[string]any)["id"].(float64))

	directRule := request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "direct-lan", "priority": 10, "match_json": `{"domain_suffix":["lan"]}`, "action": "direct", "enabled": true}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "local-out", "priority": 20, "match_json": `{"domain_suffix":["example.org"]}`, "action": "outbound", "outbound_id": outboundID, "enabled": true}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "imported-out", "priority": 30, "match_json": `{"domain_suffix":["example.net"]}`, "action": "external", "external_outbound_id": externalID, "enabled": true}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "warp-out", "priority": 40, "match_json": `{"domain_suffix":["cloudflare.com"]}`, "action": "warp", "enabled": true}, http.StatusBadRequest)
	directRuleID := int64(directRule["routing_rule"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPatch, "/api/v1/ui/routing-rules/"+strconv.FormatInt(directRuleID, 10), token, map[string]any{"server_id": serverID, "name": "warp-out", "priority": 40, "match_json": `{"domain_suffix":["cloudflare.com"]}`, "action": "warp", "enabled": true}, http.StatusBadRequest)
	warps := request(t, h, http.MethodGet, "/api/v1/ui/warp-profiles", token, nil, http.StatusOK)
	warpItems := warps["warp_profiles"].([]any)
	if len(warpItems) != 0 {
		t.Fatalf("routing rule created WARP state: %#v", warps)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/warp-profiles", token, map[string]any{"server_id": serverID}, http.StatusMethodNotAllowed)
	inboundResult := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "warp-entry", "protocol": "vless", "listen_ip": "::", "port": 10005, "config_json": "{}", "enabled": true}, http.StatusCreated)
	inboundID := int64(inboundResult["inbound"].(map[string]any)["id"].(float64))
	pathResult := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(pathResult["proxy_path"].(map[string]any)["id"].(float64))
	warpStep := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "warp", "transport_mode": "singbox", "config_json": "{}"}, http.StatusCreated)
	if warpStep["proxy_path_step"].(map[string]any)["node_type"] != "warp" {
		t.Fatalf("WARP path step did not round-trip: %#v", warpStep)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 2, "node_type": "imported", "external_outbound_id": externalID, "transport_mode": "singbox", "config_json": "{}"}, http.StatusBadRequest)
	warps = request(t, h, http.MethodGet, "/api/v1/ui/warp-profiles", token, nil, http.StatusOK)
	warpItems = warps["warp_profiles"].([]any)
	if len(warpItems) != 1 || int64(warpItems[0].(map[string]any)["server_id"].(float64)) != serverID {
		t.Fatalf("WARP path did not create the exit server profile: %#v", warps)
	}
	warpProfileID := int64(warpItems[0].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "ssh-via-wan6", "priority": 45, "match_json": `{"port":[22]}`, "action": "interface", "interface_name": "eth1", "enabled": true}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "dynamic-v6", "priority": 46, "match_json": `{"domain_suffix":["v6.example"]}`, "action": "source_prefix", "source_prefix": "2001:db8:55::1234/64", "enabled": true}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "bad-prefix", "priority": 47, "match_json": `{"domain_suffix":["bad.example"]}`, "action": "source_prefix", "source_prefix": "2001:db8::/129", "enabled": true}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "bad-port", "priority": 50, "match_json": `{"port":[0]}`, "action": "direct", "enabled": true}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"server_id": serverID, "name": "bad-interface", "priority": 50, "match_json": `{"port":[443]}`, "action": "interface", "interface_name": "eth1;id", "enabled": true}, http.StatusBadRequest)

	listedRules := request(t, h, http.MethodGet, "/api/v1/ui/routing-rules", token, nil, http.StatusOK)
	if len(listedRules["routing_rules"].([]any)) != 5 {
		t.Fatalf("routing rules missing: %#v", listedRules)
	}
	foundInterface := false
	foundSourcePrefix := false
	for _, raw := range listedRules["routing_rules"].([]any) {
		rule := raw.(map[string]any)
		if rule["action"] == "interface" && rule["interface_name"] == "eth1" {
			foundInterface = true
		}
		if rule["action"] == "source_prefix" && rule["source_prefix"] == "2001:db8:55::/64" {
			foundSourcePrefix = true
		}
	}
	if !foundInterface {
		t.Fatalf("interface routing rule did not round-trip: %#v", listedRules)
	}
	if !foundSourcePrefix {
		t.Fatalf("source-prefix routing rule did not round-trip: %#v", listedRules)
	}
	deployment := request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	foundWARPRequest := false
	for _, raw := range deployment["tasks"].([]any) {
		task := raw.(map[string]any)
		if task["type"] != "apply_deployment" {
			continue
		}
		var payload model.DeploymentTaskPayload
		if json.Unmarshal([]byte(task["payload_json"].(string)), &payload) == nil && len(payload.WARPRequests) == 1 && payload.WARPRequests[0].ProfileID == warpProfileID {
			plan := payload.WARPRequests[0]
			if plan.IPStack != model.IPStackIPv6Only || plan.DNSStrategy != "ipv6_only" || plan.MTU != 1280 {
				t.Fatalf("auto IPv6 WARP plan = %#v", plan)
			}
			foundWARPRequest = true
		}
	}
	if !foundWARPRequest {
		t.Fatalf("deployment did not bundle single-server WARP request: %#v", deployment)
	}
	resultJSON, err := json.Marshal(map[string]any{"steps": []map[string]any{{
		"key": "warp_" + strconv.FormatInt(warpProfileID, 10),
		"result": map[string]any{
			"profile_id":  warpProfileID,
			"status":      model.WARPStatusReady,
			"config_json": `{"type":"wireguard","tag":"warp-ready"}`,
			"mtu":         1280,
			"result_json": `{}`,
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.applyDeploymentWARPReports(context.Background(), serverID, string(resultJSON)); err != nil {
		t.Fatal(err)
	}
	updatedWARP, err := db.GetWARPProfile(context.Background(), warpProfileID)
	if err != nil {
		t.Fatal(err)
	}
	if updatedWARP.Status != model.WARPStatusReady || !strings.Contains(updatedWARP.ConfigJSON, "warp-ready") {
		t.Fatalf("deployment WARP result was not persisted: %#v", updatedWARP)
	}
}

func TestImportedNodeURIProxyPathAndGrantAPI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(server["server"].(map[string]any)["id"].(float64))
	server2 := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s2", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20010}, http.StatusCreated)
	server2ID := int64(server2["server"].(map[string]any)["id"].(float64))
	user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "bob", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	userID := int64(user["user"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))

	imported := request(t, h, http.MethodPost, "/api/v1/ui/external-outbounds/import", token, map[string]any{"scope": "global", "expose_to_users": true, "content": "socks5://user:pass@socks.example.com:1080#SOCKS-A"}, http.StatusCreated)
	external := imported["external_outbounds"].([]any)[0].(map[string]any)
	if external["protocol"] != "socks" || external["target_address"] != "socks.example.com" || external["expose_to_users"] != true {
		t.Fatalf("bad imported socks: %#v", external)
	}
	externalID := int64(external["id"].(float64))

	grantPlan := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{"name": "imported", "enabled": true, "nodes": []map[string]any{{"node_type": "external_outbound", "node_id": externalID}}}, http.StatusCreated)
	if grantPlan["subscription_plan"].(map[string]any)["id"] == nil {
		t.Fatalf("plan missing id: %#v", grantPlan)
	}
	if err := db.SetUserPlanBindings(context.Background(), []model.UserPlanBinding{{UserID: userID, PlanID: int64(grantPlan["subscription_plan"].(map[string]any)["id"].(float64))}}); err != nil {
		t.Fatal(err)
	}

	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "via-socks", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(path["proxy_path"].(map[string]any)["id"].(float64))
	step := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "imported", "external_outbound_id": externalID}, http.StatusCreated)
	if step["proxy_path_step"].(map[string]any)["id"] == nil {
		t.Fatalf("step missing id: %#v", step)
	}
	importedStepID := int64(step["proxy_path_step"].(map[string]any)["id"].(float64))
	listed := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths", token, nil, http.StatusOK)
	if len(listed["proxy_paths"].([]any)) != 1 {
		t.Fatalf("proxy path not listed: %#v", listed)
	}

	serverPath := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "via-server", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	serverPathID := int64(serverPath["proxy_path"].(map[string]any)["id"].(float64))
	serverStep := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": serverPathID, "position": 1, "node_type": "server_inbound", "server_id": server2ID}, http.StatusCreated)
	serverStepValue := serverStep["proxy_path_step"].(map[string]any)
	serverStepID := int64(serverStepValue["id"].(float64))
	if got := serverStepValue["server_id"]; int64(got.(float64)) != server2ID {
		t.Fatalf("server-only step did not persist target server: %#v", serverStep)
	}
	plan := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(serverPathID)+"/plan", token, nil, http.StatusOK)
	steps := plan["plan"].(map[string]any)["steps"].([]any)
	if len(steps) != 1 || int64(steps[0].(map[string]any)["server_id"].(float64)) != server2ID {
		t.Fatalf("server-only path plan missing target server: %#v", plan)
	}
	direct := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"inbound_id": inboundID}, http.StatusCreated)["proxy_path"].(map[string]any)
	if direct["kind"] != "direct" || direct["name"] != "s1" {
		t.Fatalf("bad direct path: %#v", direct)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"inbound_id": inboundID}, http.StatusBadRequest)
	branched := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"source_step_id": serverStepID}, http.StatusCreated)
	branchedPath := branched["proxy_path"].(map[string]any)
	if branchedPath["kind"] != "direct" || branchedPath["name"] != "s1｜s2｜直出" || int64(branchedPath["branch_source_step_id"].(float64)) != serverStepID {
		t.Fatalf("bad intermediate direct branch: %#v", branched)
	}
	branchedSteps := branched["proxy_path_steps"].([]any)
	if len(branchedSteps) != 1 || int64(branchedSteps[0].(map[string]any)["server_id"].(float64)) != server2ID {
		t.Fatalf("intermediate direct branch did not copy its prefix: %#v", branched)
	}
	branchedStepID := int64(branchedSteps[0].(map[string]any)["id"].(float64))
	plan = request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(serverPathID)+"/plan", token, nil, http.StatusOK)
	if got := len(plan["plan"].(map[string]any)["steps"].([]any)); got != 1 {
		t.Fatalf("creating direct branch changed original chain: %#v", plan)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"source_step_id": serverStepID}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"source_step_id": importedStepID}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"kind": "direct", "inbound_id": inboundID, "branch_source_step_id": serverStepID, "enabled": false}, http.StatusBadRequest)
	request(t, h, http.MethodPatch, "/api/v1/ui/proxy-path-steps/"+itoa(branchedStepID), token, map[string]any{"config_json": `{}`}, http.StatusOK)
	updatedBranch := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(int64(branchedPath["id"].(float64))), token, nil, http.StatusOK)["proxy_path"].(map[string]any)
	if _, ok := updatedBranch["branch_source_step_id"]; ok {
		t.Fatalf("edited direct branch retained stale source reference: %#v", updatedBranch)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": serverPathID, "position": 2, "node_type": "server_inbound", "server_id": serverID}, http.StatusBadRequest)
}

func TestRoutingFallbackDirectBranchCanContinueToImportedNode(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "entry", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(server["server"].(map[string]any)["id"].(float64))
	targetServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "target", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 11000, "port_range_end": 11010}, http.StatusCreated)
	targetServerID := int64(targetServer["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	imported := request(t, h, http.MethodPost, "/api/v1/ui/external-outbounds/import", token, map[string]any{"scope": "global", "content": "socks5://user:pass@socks.example.com:1080#SOCKS"}, http.StatusCreated)
	externalID := int64(imported["external_outbounds"].([]any)[0].(map[string]any)["id"].(float64))
	createRootRule := func(pathID int64, name string) int64 {
		rule := request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"scope": "path_stage", "proxy_path_id": pathID, "sort_position": 0, "match_source": "inline", "name": name, "match_json": `{"geoip":["cn"]}`, "action": "direct", "enabled": true}, http.StatusCreated)["routing_rule"].(map[string]any)
		return int64(rule["id"].(float64))
	}
	assertRootRule := func(ruleID, pathID int64) {
		t.Helper()
		storedRule, err := db.GetRoutingRule(context.Background(), ruleID)
		if err != nil || storedRule.ProxyPathID == nil || *storedRule.ProxyPathID != pathID || storedRule.StageStepID != nil {
			t.Fatalf("routing rule moved while continuing fallback: %#v err=%v", storedRule, err)
		}
	}

	direct := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"inbound_id": inboundID}, http.StatusCreated)["proxy_path"].(map[string]any)
	pathID := int64(direct["id"].(float64))
	ruleID := createRootRule(pathID, "cn-imported")

	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "imported", "external_outbound_id": externalID, "transport_mode": "singbox"}, http.StatusCreated)
	updated := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(pathID), token, nil, http.StatusOK)["proxy_path"].(map[string]any)
	if updated["kind"] != "chain" {
		t.Fatalf("routing fallback path kind = %v, want chain: %#v", updated["kind"], updated)
	}
	assertRootRule(ruleID, pathID)

	warpDirect := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"inbound_id": inboundID}, http.StatusCreated)["proxy_path"].(map[string]any)
	warpPathID := int64(warpDirect["id"].(float64))
	warpRuleID := createRootRule(warpPathID, "cn-warp")
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": warpPathID, "position": 1, "node_type": "warp", "transport_mode": "singbox"}, http.StatusCreated)
	updated = request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(warpPathID), token, nil, http.StatusOK)["proxy_path"].(map[string]any)
	if updated["kind"] != "chain" {
		t.Fatalf("WARP fallback path kind = %v, want chain: %#v", updated["kind"], updated)
	}
	assertRootRule(warpRuleID, warpPathID)

	serverDirect := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"inbound_id": inboundID}, http.StatusCreated)["proxy_path"].(map[string]any)
	serverPathID := int64(serverDirect["id"].(float64))
	serverRuleID := createRootRule(serverPathID, "cn-server")
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": serverPathID, "position": 1, "node_type": "server_inbound", "server_id": targetServerID, "transport_mode": "singbox"}, http.StatusCreated)
	updated = request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(serverPathID), token, nil, http.StatusOK)["proxy_path"].(map[string]any)
	if updated["kind"] != "direct" {
		t.Fatalf("controlled-server fallback path kind = %v, want direct: %#v", updated["kind"], updated)
	}
	assertRootRule(serverRuleID, serverPathID)
}

func TestProxyPathServerOnlyStepsPlanAndValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	serverC := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "C", "entry_ip_mode": "custom", "entry_address": "203.0.113.3", "listen_ip": "0.0.0.0", "port_range_start": 32000, "port_range_end": 32100}, http.StatusCreated)
	serverCID := int64(serverC["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	imported := request(t, h, http.MethodPost, "/api/v1/ui/external-outbounds/import", token, map[string]any{"scope": "global", "content": "socks5://user:pass@socks.example.com:1080#SOCKS-P"}, http.StatusCreated)
	externalID := int64(imported["external_outbounds"].([]any)[0].(map[string]any)["id"].(float64))

	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "A-B-C-P", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(path["proxy_path"].(map[string]any)["id"].(float64))
	transparentStep := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "port_forward"}, http.StatusCreated)
	transparentStepID := int64(transparentStep["proxy_path_step"].(map[string]any)["id"].(float64))
	processingStep := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 2, "node_type": "server_inbound", "server_id": serverCID, "transport_mode": "port_forward", "processing_role": true}, http.StatusCreated)
	processingStepID := int64(processingStep["proxy_path_step"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 3, "node_type": "imported", "external_outbound_id": externalID, "transport_mode": "singbox"}, http.StatusCreated)

	plan := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(pathID)+"/plan", token, nil, http.StatusOK)
	steps := plan["plan"].(map[string]any)["steps"].([]any)
	if len(steps) != 3 || int64(steps[0].(map[string]any)["server_id"].(float64)) != serverBID || int64(steps[1].(map[string]any)["server_id"].(float64)) != serverCID {
		t.Fatalf("bad proxy path plan: %#v", plan)
	}
	runtimeNodes := plan["plan"].(map[string]any)["runtime_nodes"].([]any)
	if len(runtimeNodes) != 3 {
		t.Fatalf("bad proxy path runtime nodes: %#v", plan)
	}
	allowedRuntimeFields := map[string]bool{
		"resource_key": true, "step_id": true, "kind": true, "name": true,
		"server_id": true, "protocol": true, "profile": true, "listen_ip": true,
		"port": true, "network": true, "listen_scope": true, "shared": true,
		"reference_count": true,
	}
	for _, raw := range runtimeNodes {
		for key := range raw.(map[string]any) {
			if !allowedRuntimeFields[key] {
				t.Fatalf("proxy path runtime node exposed %q: %#v", key, raw)
			}
		}
	}
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"source_step_id": transparentStepID}, http.StatusBadRequest)
	listed := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths", token, nil, http.StatusOK)
	if got := len(listed["proxy_paths"].([]any)); got != 1 {
		t.Fatalf("failed transparent direct branch left partial path: %#v", listed)
	}
	branched := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"source_step_id": processingStepID}, http.StatusCreated)
	branchedPath := branched["proxy_path"].(map[string]any)
	if branchedPath["kind"] != "direct" || int64(branchedPath["branch_source_step_id"].(float64)) != processingStepID {
		t.Fatalf("bad direct branch at transparent processing server: %#v", branched)
	}
	branchedSteps := branched["proxy_path_steps"].([]any)
	if len(branchedSteps) != 2 || int64(branchedSteps[0].(map[string]any)["server_id"].(float64)) != serverBID || int64(branchedSteps[1].(map[string]any)["server_id"].(float64)) != serverCID {
		t.Fatalf("processing-server direct branch did not preserve the transparent prefix: %#v", branched)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths/direct-branches", token, map[string]any{"source_step_id": processingStepID}, http.StatusBadRequest)
	listed = request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths", token, nil, http.StatusOK)
	if got := len(listed["proxy_paths"].([]any)); got != 2 {
		t.Fatalf("failed duplicate transparent branch left a partial path: %#v", listed)
	}

	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 4, "node_type": "server_inbound", "server_id": serverAID, "transport_mode": "singbox"}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 4, "node_type": "imported", "external_outbound_id": externalID, "transport_mode": "port_forward"}, http.StatusBadRequest)
}

func TestProxyPathRejectsExplicitIPv6TargetFromIPv4OnlySource(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	source := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{
		"name": "IPv4 source", "public_ipv4": "198.51.100.10", "entry_ip_mode": "ipv4", "listen_ip": "0.0.0.0", "ip_stack": "ipv4_only", "port_range_start": 30000, "port_range_end": 30100,
	}, http.StatusCreated)["server"].(map[string]any)
	target := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{
		"name": "dual target", "public_ipv4": "203.0.113.20", "public_ipv6": "2001:db8::20", "entry_ip_mode": "ipv6", "listen_ip": "::", "ip_stack": "dual_stack", "port_range_start": 31000, "port_range_end": 31100,
	}, http.StatusCreated)["server"].(map[string]any)
	sourceID := int64(source["id"].(float64))
	targetID := int64(target["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": sourceID, "name": "entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true,
	}, http.StatusCreated)["inbound"].(map[string]any)
	inboundID := int64(inbound["id"].(float64))

	enabled := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "enabled-invalid", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	enabledID := int64(enabled["id"].(float64))
	badStep := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": enabledID, "node_type": "server_inbound", "server_id": targetID, "transport_mode": "singbox"}, http.StatusBadRequest)
	if message := fmt.Sprint(badStep["error"]); !strings.Contains(message, "IPv4 source") || !strings.Contains(message, "dual target") || !strings.Contains(message, "IPv6") {
		t.Fatalf("step error is not actionable: %q", message)
	}
	if steps, err := db.ListProxyPathStepsForPath(context.Background(), enabledID); err != nil || len(steps) != 0 {
		t.Fatalf("rejected step persisted: steps=%#v err=%v", steps, err)
	}

	disabled := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "stored-invalid", "inbound_id": inboundID, "enabled": false}, http.StatusCreated)["proxy_path"].(map[string]any)
	disabledID := int64(disabled["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": disabledID, "node_type": "server_inbound", "server_id": targetID, "transport_mode": "singbox"}, http.StatusCreated)
	enableError := request(t, h, http.MethodPatch, "/api/v1/ui/proxy-paths/"+itoa(disabledID), token, map[string]any{"enabled": true}, http.StatusBadRequest)
	if message := fmt.Sprint(enableError["error"]); !strings.Contains(message, "IPv6") {
		t.Fatalf("enable error is not actionable: %q", message)
	}

	stored, err := db.GetProxyPath(context.Background(), disabledID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Enabled = true
	if err := db.UpdateProxyPath(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	before, err := db.ListTasks(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	applyError := request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusBadRequest)
	if message := fmt.Sprint(applyError["error"]); !strings.Contains(message, "IPv6") || strings.Contains(message, "internal server error") {
		t.Fatalf("deployment error is not actionable: %q", message)
	}
	after, err := db.ListTasks(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("invalid deployment queued tasks: before=%d after=%d", len(before), len(after))
	}
}

func TestProxyPathAutomaticAndCustomNamesFollowTopology(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "香港", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "洛杉矶", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	serverC := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "东京", "entry_ip_mode": "custom", "entry_address": "203.0.113.3", "listen_ip": "0.0.0.0", "port_range_start": 32000, "port_range_end": 32100}, http.StatusCreated)
	serverCID := int64(serverC["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	createdPath := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name_mode": "auto", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(createdPath["proxy_path"].(map[string]any)["id"].(float64))
	createdStep := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox", "config_json": `{}`}, http.StatusCreated)
	stepID := int64(createdStep["proxy_path_step"].(map[string]any)["id"].(float64))

	path := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(pathID), token, nil, http.StatusOK)["proxy_path"].(map[string]any)
	if path["name"] != "香港｜洛杉矶" || path["name_mode"] != "auto" {
		t.Fatalf("automatic path = %#v", path)
	}
	template := []map[string]any{
		{"kind": "literal", "value": "专线 "},
		{"kind": "server", "server_id": serverAID},
		{"kind": "literal", "value": "｜"},
		{"kind": "server", "server_id": serverBID},
	}
	path = request(t, h, http.MethodPatch, "/api/v1/ui/proxy-paths/"+itoa(pathID), token, map[string]any{"name_mode": "custom", "name_template": template}, http.StatusOK)["proxy_path"].(map[string]any)
	if path["name"] != "专线 香港｜洛杉矶" || path["name_mode"] != "custom" {
		t.Fatalf("custom path = %#v", path)
	}
	storedB, err := db.GetServer(context.Background(), serverBID)
	if err != nil {
		t.Fatal(err)
	}
	storedB.Name = "纽约"
	storedB.UpdatedAt = time.Now().UTC()
	if err := db.UpdateServer(context.Background(), storedB); err != nil {
		t.Fatal(err)
	}
	path = request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(pathID), token, nil, http.StatusOK)["proxy_path"].(map[string]any)
	if path["name"] != "专线 香港｜纽约" {
		t.Fatalf("renamed custom path = %#v", path)
	}
	request(t, h, http.MethodPatch, "/api/v1/ui/proxy-path-steps/"+itoa(stepID), token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverCID, "transport_mode": "singbox", "config_json": `{}`}, http.StatusOK)
	path = request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(pathID), token, nil, http.StatusOK)["proxy_path"].(map[string]any)
	if path["name"] != "香港｜东京" || path["name_mode"] != "auto" {
		t.Fatalf("topology fallback path = %#v", path)
	}
}

func TestProxyPathTransportCanChangeAndDeleteCascades(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	serverC := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "C", "entry_ip_mode": "custom", "entry_address": "203.0.113.3", "listen_ip": "0.0.0.0", "port_range_start": 32000, "port_range_end": 32100}, http.StatusCreated)
	serverCID := int64(serverC["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "A-B-C", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(path["proxy_path"].(map[string]any)["id"].(float64))

	created := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox", "processing_role": true, "config_json": `{}`}, http.StatusCreated)
	step := created["proxy_path_step"].(map[string]any)
	stepID := int64(step["id"].(float64))
	if step["processing_role"] != false {
		t.Fatalf("sing-box processing role must be derived, got %#v", step)
	}
	if !strings.Contains(step["config_json"].(string), `"chain_method":"2022-blake3-aes-128-gcm"`) {
		t.Fatalf("sing-box chain method did not default to SS2022-128: %#v", step)
	}
	for _, invalidMethod := range []string{"aes-128-gcm", "2022-blake3-aes-192-gcm"} {
		request(t, h, http.MethodPatch, "/api/v1/ui/proxy-path-steps/"+itoa(stepID), token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox", "config_json": `{"chain_method":"` + invalidMethod + `"}`}, http.StatusBadRequest)
	}

	changed := request(t, h, http.MethodPatch, "/api/v1/ui/proxy-path-steps/"+itoa(stepID), token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "port_forward", "processing_role": false, "config_json": `{}`}, http.StatusOK)
	if changed["proxy_path_step"].(map[string]any)["processing_role"] != true {
		t.Fatalf("last transparent step must become processor: %#v", changed)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"kind": "direct", "inbound_id": inboundID, "enabled": true}, http.StatusBadRequest)
	second := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 2, "node_type": "server_inbound", "server_id": serverCID, "transport_mode": "singbox", "config_json": `{}`}, http.StatusCreated)
	secondID := int64(second["proxy_path_step"].(map[string]any)["id"].(float64))

	changed = request(t, h, http.MethodPatch, "/api/v1/ui/proxy-path-steps/"+itoa(stepID), token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox", "processing_role": true, "config_json": `{}`}, http.StatusOK)
	if changed["proxy_path_step"].(map[string]any)["processing_role"] != false {
		t.Fatalf("switching back to chain proxy must clear transparent processor: %#v", changed)
	}
	subscriptionPlan := &model.SubscriptionPlan{Name: "管理员", Enabled: true}
	if err := db.CreateSubscriptionPlan(context.Background(), subscriptionPlan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeProxyPath, NodeID: pathID}}); err != nil {
		t.Fatal(err)
	}

	deleted := request(t, h, http.MethodDelete, "/api/v1/ui/proxy-path-steps/"+itoa(stepID), token, nil, http.StatusOK)
	if int(deleted["deleted_steps"].(float64)) != 2 || deleted["path_deleted"] != true {
		t.Fatalf("delete must remove this and downstream steps: %#v", deleted)
	}
	request(t, h, http.MethodGet, "/api/v1/ui/proxy-path-steps/"+itoa(secondID), token, nil, http.StatusNotFound)
	paths := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths", token, nil, http.StatusOK)
	if paths["proxy_paths"] != nil && len(paths["proxy_paths"].([]any)) != 0 {
		t.Fatalf("empty path should be removed: %#v", paths)
	}
	planNodes, err := db.ListActivePlanNodes(context.Background(), subscriptionPlan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(planNodes) != 0 {
		t.Fatalf("deleted path remains in subscription plan: %#v", planNodes)
	}
}

func TestProxyPathWireGuardTunnelCreatesManagedPair(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "A-WG-B", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(path["proxy_path"].(map[string]any)["id"].(float64))
	created := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "tunnel", "config_json": `{"type":"wireguard","source_private_key":"user-controlled","source_public_key":"user-controlled","target_private_key":"user-controlled","target_public_key":"user-controlled"}`}, http.StatusCreated)
	step := created["proxy_path_step"].(map[string]any)
	var cfg map[string]any
	if err := json.Unmarshal([]byte(step["config_json"].(string)), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source_private_key", "source_public_key", "target_private_key", "target_public_key"} {
		if _, ok := cfg[key]; ok {
			t.Fatalf("managed WireGuard response leaked %s: %#v", key, cfg)
		}
	}
	stepID := int64(step["id"].(float64))
	stored, err := db.GetProxyPathStep(context.Background(), stepID)
	if err != nil {
		t.Fatal(err)
	}
	var storedCfg map[string]any
	if err := json.Unmarshal([]byte(stored.ConfigJSON), &storedCfg); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source_private_key", "source_public_key", "target_private_key", "target_public_key"} {
		if _, ok := storedCfg[key]; ok {
			t.Fatalf("path record retained shared WireGuard credential %s: %#v", key, storedCfg)
		}
	}
	storedConfig := stored.ConfigJSON
	request(t, h, http.MethodPatch, "/api/v1/ui/proxy-path-steps/"+itoa(stepID), token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "tunnel", "config_json": step["config_json"]}, http.StatusOK)
	stored, err = db.GetProxyPathStep(context.Background(), stepID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConfigJSON != storedConfig {
		t.Fatalf("ordinary patch changed shared WireGuard conditions: before=%s after=%s", storedConfig, stored.ConfigJSON)
	}
	plan := request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(pathID)+"/plan", token, nil, http.StatusOK)
	if len(plan["plan"].(map[string]any)["tunnels"].([]any)) != 1 {
		t.Fatalf("WireGuard path missing tunnel plan: %#v", plan)
	}
	publicTunnel := plan["plan"].(map[string]any)["tunnels"].([]any)[0].(map[string]any)
	var publicConfig map[string]any
	if err := json.Unmarshal([]byte(publicTunnel["config_json"].(string)), &publicConfig); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source_private_key", "source_public_key", "target_private_key", "target_public_key"} {
		if publicConfig[key] != "<redacted>" {
			t.Fatalf("WireGuard plan did not redact %s: %#v", key, publicConfig)
		}
	}
}

func TestProxyPathSSHTunnelOwnsAndHidesManagedKey(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "A-SSH-B", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(path["proxy_path"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "tunnel", "config_json": `{"type":"ssh"}`}, http.StatusBadRequest)
	attackerPrivate, attackerPublic := "attacker-private-key", "ssh-ed25519 attacker-public-key"
	malicious, _ := json.Marshal(map[string]any{"type": "ssh", "ssh_port": 31005, "client_private_key": attackerPrivate, "client_public_key": attackerPublic})
	created := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "tunnel", "config_json": string(malicious)}, http.StatusCreated)
	step := created["proxy_path_step"].(map[string]any)
	if strings.Contains(fmt.Sprint(step), "PRIVATE KEY") || strings.Contains(fmt.Sprint(step), attackerPublic) {
		t.Fatalf("SSH step response leaked managed key: %#v", step)
	}
	stored, err := db.GetProxyPathStep(context.Background(), int64(step["id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.ConfigJSON, attackerPrivate) || strings.Contains(stored.ConfigJSON, attackerPublic) {
		t.Fatal("controller accepted user-provided managed SSH key")
	}
	var storedCfg map[string]any
	_ = json.Unmarshal([]byte(stored.ConfigJSON), &storedCfg)
	if storedCfg["client_private_key"] != nil || storedCfg["client_public_key"] != nil {
		t.Fatalf("path record retained shared SSH credentials: %#v", storedCfg)
	}
	if intFromAnyController(storedCfg["ssh_port"]) != 31005 || !strings.Contains(step["config_json"].(string), `"ssh_port":31005`) {
		t.Fatalf("controller did not preserve the managed SSH port: stored=%#v public=%#v", storedCfg, step["config_json"])
	}
}

func TestDeletingServerCutsDependentProxyPath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	serverC := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "C", "entry_ip_mode": "custom", "entry_address": "203.0.113.3", "listen_ip": "0.0.0.0", "port_range_start": 32000, "port_range_end": 32100}, http.StatusCreated)
	serverCID := int64(serverC["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"name": "A-B-C", "inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(path["proxy_path"].(map[string]any)["id"].(float64))
	first := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox", "config_json": `{}`}, http.StatusCreated)
	firstID := int64(first["proxy_path_step"].(map[string]any)["id"].(float64))
	second := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 2, "node_type": "server_inbound", "server_id": serverCID, "transport_mode": "singbox", "config_json": `{}`}, http.StatusCreated)
	secondID := int64(second["proxy_path_step"].(map[string]any)["id"].(float64))

	request(t, h, http.MethodDelete, "/api/v1/ui/servers/"+itoa(serverCID), token, nil, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/ui/proxy-path-steps/"+itoa(secondID), token, nil, http.StatusNotFound)
	remaining := request(t, h, http.MethodGet, "/api/v1/ui/proxy-path-steps/"+itoa(firstID), token, nil, http.StatusOK)
	if int64(remaining["proxy_path_step"].(map[string]any)["server_id"].(float64)) != serverBID {
		t.Fatalf("path prefix was not preserved: %#v", remaining)
	}

	request(t, h, http.MethodDelete, "/api/v1/ui/servers/"+itoa(serverBID), token, nil, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/ui/proxy-paths/"+itoa(pathID), token, nil, http.StatusNotFound)
}

func TestAgentPortForwardProbeAcceptsDerivedProxyPathRule(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	source := &model.Server{Name: "YT", AgentID: "agent-yt", AgentTokenHash: security.HashSecret("token-yt"), PublicIPv4: "199.30.91.70", ListenIP: "0.0.0.0", PortRangeStart: 55777, PortRangeEnd: 55780, Status: model.ServerOnline}
	target := &model.Server{Name: "WAWO", PublicIPv4: "2.27.109.100", ListenIP: "0.0.0.0", PortRangeStart: 443, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, target); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: source.ID, Name: "YT-vless-55778", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 55778, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Name: "YT-WAWO", InboundID: inbound.ID, Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ServerID: &target.ID, ConfigJSON: `{}`}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	derived, err := core.DerivedPortForwardsFromProxyPaths([]model.ProxyPath{*path}, []model.ProxyPathStep{*step}, []model.Server{*source, *target}, []model.Inbound{*inbound})
	if err != nil || len(derived) != 1 {
		t.Fatalf("derived forwards = %#v, err=%v", derived, err)
	}

	body, _ := json.Marshal(model.PortForwardProbeResult{PortForwardID: derived[0].ID, Mode: "task", Available: true, LatencyMS: 12, SampleCount: 5, ResultJSON: `{}`})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/port-forward-probes", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", source.AgentID)
	req.Header.Set("Authorization", "Bearer token-yt")
	rr := httptest.NewRecorder()
	newTestServer(db, "test-secret", "").Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("derived probe status=%d body=%s", rr.Code, rr.Body.String())
	}
	items, err := db.ListPortForwardProbeResults(ctx, source.ID, derived[0].ID, 10)
	if err != nil || len(items) != 1 || !items[0].Available {
		t.Fatalf("stored probes = %#v, err=%v", items, err)
	}
}

func TestProtocolAuthDefaultsArePersisted(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	outbound := request(t, h, http.MethodPost, "/api/v1/ui/outbounds", token, map[string]any{"server_id": serverID, "name": "auto-vless", "protocol": "vless", "target_address": "next.example.com", "target_port": 443, "config_json": "{}", "enabled": true}, http.StatusCreated)
	outboundCfg := configJSONFrom(t, outbound["outbound"].(map[string]any))
	if outboundCfg["uuid"] == "" || outboundCfg["_oboard"].(map[string]any)["username"] == "" {
		t.Fatalf("vless auth defaults missing: %#v", outboundCfg)
	}

	customUUID := "11111111-1111-4111-8111-111111111111"
	custom := request(t, h, http.MethodPost, "/api/v1/ui/outbounds", token, map[string]any{"server_id": serverID, "name": "custom-vless", "protocol": "vless", "target_address": "custom.example.com", "target_port": 443, "config_json": `{"uuid":"` + customUUID + `","_oboard":{"username":"custom-node"}}`, "enabled": true}, http.StatusCreated)
	customCfg := configJSONFrom(t, custom["outbound"].(map[string]any))
	if customCfg["uuid"] != customUUID || customCfg["_oboard"].(map[string]any)["username"] != "custom-node" {
		t.Fatalf("custom auth overwritten: %#v", customCfg)
	}

	external := request(t, h, http.MethodPost, "/api/v1/ui/external-outbounds", token, map[string]any{"scope": "global", "name": "ss-ext", "protocol": "shadowsocks", "target_address": "ss.example.com", "target_port": 8388, "config_json": "{}", "enabled": true}, http.StatusCreated)
	externalCfg := configJSONFrom(t, external["external_outbound"].(map[string]any))
	if externalCfg["method"] == "" || externalCfg["password"] == "" || externalCfg["_oboard"].(map[string]any)["username"] == "" {
		t.Fatalf("external ss auth defaults missing: %#v", externalCfg)
	}
}

func TestSingleUserInboundPlanCapacityPreview(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(server["server"].(map[string]any)["id"].(float64))
	userA := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "alice", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	userB := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "bob", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	userAID, userBID := int64(userA["user"].(map[string]any)["id"].(float64)), int64(userB["user"].(map[string]any)["id"].(float64))

	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "single-password-ss", "protocol": "shadowsocks", "listen_ip": "0.0.0.0", "port": 8388, "config_json": `{"method":"chacha20-ietf-poly1305"}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	plan := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{"name": "single", "enabled": true, "nodes": []map[string]any{{"node_type": "inbound", "node_id": inboundID}}}, http.StatusCreated)
	planID := int64(plan["subscription_plan"].(map[string]any)["id"].(float64))
	if err := db.SetUserPlanBindings(context.Background(), []model.UserPlanBinding{{UserID: userAID, PlanID: planID}, {UserID: userBID, PlanID: planID}}); err != nil {
		t.Fatal(err)
	}
	preview := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans/"+strconv.FormatInt(planID, 10)+"/nodes/preview", token, map[string]any{"op": "replace", "nodes": []map[string]any{{"node_type": "inbound", "node_id": inboundID}}}, http.StatusOK)
	capacity := preview["preview"].(map[string]any)["capacity_issues"].([]any)
	if len(capacity) == 0 {
		t.Fatalf("single-user inbound overflow was not reported: %#v", preview)
	}
}

func TestPlanGrantedSubscriptionIncludesInboundNode(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(server["server"].(map[string]any)["id"].(float64))
	user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "bob", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	userID := int64(user["user"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))

	plan := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{"name": "vip", "enabled": true, "nodes": []map[string]any{{"node_type": "inbound", "node_id": inboundID}}}, http.StatusCreated)
	planID := int64(plan["subscription_plan"].(map[string]any)["id"].(float64))
	if err := db.SetUserPlanBindings(context.Background(), []model.UserPlanBinding{{UserID: userID, PlanID: planID}}); err != nil {
		t.Fatal(err)
	}
	sub, err := core.GenerateSubscriptionWithOptions(
		model.User{ID: userID, Username: "bob", Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "pass"},
		[]model.Server{{ID: serverID, Name: "s1", PublicIPv4: "203.0.113.1"}},
		[]model.Inbound{{ID: inboundID, ServerID: serverID, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}},
		core.SubscriptionOptions{EffectiveNodes: map[string]bool{core.NodeKeyOf(model.AssignableNodeInbound, inboundID): true}},
	)
	if err != nil || !strings.Contains(sub, "vless") {
		t.Fatalf("subscription did not include plan-granted inbound: %v %s", err, sub)
	}
}

func TestPlanNodeSyncChangesOnlyTheDraftRevision(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "listen_ip": "0.0.0.0"}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	inboundID := int64(inbound["id"].(float64))
	pathA := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	pathB := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)["proxy_path"].(map[string]any)
	pathAID, pathBID := int64(pathA["id"].(float64)), int64(pathB["id"].(float64))
	plan := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{"name": "paths", "enabled": true, "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathAID}}}, http.StatusCreated)
	planID := int64(plan["subscription_plan"].(map[string]any)["id"].(float64))

	request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans/"+strconv.FormatInt(planID, 10)+"/nodes/sync", token, map[string]any{"op": "add", "nodes": []map[string]any{{"node_type": "proxy_path", "node_id": pathBID}}}, http.StatusOK)
	activeNodes, err := db.ListActivePlanNodes(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if len(activeNodes) != 1 || activeNodes[0].NodeID != pathAID {
		t.Fatalf("active plan nodes changed while editing draft: %#v", activeNodes)
	}
	draftNodes, err := db.ListDraftPlanNodes(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, node := range draftNodes {
		got[node.NodeID] = true
	}
	if !got[pathAID] || !got[pathBID] {
		t.Fatalf("draft plan nodes = %#v, want A and B", draftNodes)
	}
}

func TestRealityInboundDefaults(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "entry_ip_mode": "custom", "entry_address": "203.0.113.10", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id":   serverID,
		"name":        "reality-auto",
		"protocol":    "vless",
		"listen_ip":   "0.0.0.0",
		"port":        443,
		"config_json": `{"flow":"xtls-rprx-vision","packet_encoding":"xudp","tls":{"enabled":true,"server_name":"cdn.icloud-content.com","reality":{"enabled":true,"handshake":{"server":"cdn.icloud-content.com","server_port":443}}}}`,
		"enabled":     true,
	}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	cfg := configJSONFrom(t, inbound["inbound"].(map[string]any))
	reality := cfg["tls"].(map[string]any)["reality"].(map[string]any)
	generatedPrivate := reality["private_key"].(string)
	generatedPublic := reality["public_key"].(string)
	if generatedPrivate == "" || generatedPublic == "" || reality["short_id"] == "" {
		t.Fatalf("reality defaults missing: %#v", reality)
	}
	if got := deriveRealityPublicForTest(t, generatedPrivate); got != generatedPublic {
		t.Fatalf("generated public key mismatch: got %q want %q", got, generatedPublic)
	}

	server, err := db.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	storedInbound, err := db.GetInbound(context.Background(), inboundID)
	if err != nil {
		t.Fatal(err)
	}
	users, err := db.ListUsers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	singBoxConfig, err := core.GenerateServerConfigWithOptions(*server, []model.Inbound{*storedInbound}, nil, nil, users, core.ConfigOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(singBoxConfig, generatedPrivate) {
		t.Fatalf("server config missing private key: %s", singBoxConfig)
	}
	if strings.Contains(singBoxConfig, generatedPublic) || strings.Contains(singBoxConfig, `"public_key"`) {
		t.Fatalf("server config leaked public key: %s", singBoxConfig)
	}

	// The subscription is authorized by the plan snapshot: bind the user to a
	// plan containing the inbound and derive the effective node key.
	plan := &model.SubscriptionPlan{Name: "reality-plan", Enabled: true}
	if err := db.CreateSubscriptionPlan(context.Background(), plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: inboundID}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserPlanBindings(context.Background(), []model.UserPlanBinding{{UserID: users[0].ID, PlanID: plan.ID}}); err != nil {
		t.Fatal(err)
	}
	plans, err := db.ListSubscriptionPlans(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	planNodes, err := db.ListAllPlanNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := db.ListEffectiveUserPlanBindings(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	snap := core.BuildEffectiveAccessSnapshot(core.EffectiveAccessInput{
		Users: users, Bindings: bindings, Plans: plans, PlanNodes: planNodes,
		Inbounds: []model.Inbound{*storedInbound}, Now: time.Now(),
	})
	effective := snap.EffectiveNodeKeys(users[0].ID)
	subscription, err := core.GenerateSubscriptionWithOptions(users[0], []model.Server{*server}, []model.Inbound{*storedInbound}, core.SubscriptionOptions{Format: model.SubscriptionFormatSingBox, EffectiveNodes: effective})
	if err != nil {
		t.Fatal(err)
	}
	if len(effective) != 1 {
		t.Fatalf("effective nodes = %#v", effective)
	}
	if !strings.Contains(subscription, generatedPublic) {
		t.Fatalf("subscription missing public key: %s", subscription)
	}
	if strings.Contains(subscription, generatedPrivate) {
		t.Fatalf("subscription leaked private key: %s", subscription)
	}
}

func TestControlledRealityInboundCreateUpdateAndRotate(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "listen_ip": "0.0.0.0"}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(server["id"].(float64))

	created := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": serverID, "name": "controlled-reality", "kind": "vless-reality", "listen_ip": "0.0.0.0", "port": 443,
		"reality": map[string]any{"handshake_server": "www.nvidia.com", "handshake_port": 443}, "config_json": `{"packet_encoding":"xudp"}`, "enabled": true,
	}, http.StatusCreated)["inbound"].(map[string]any)
	if created["protocol"] != "vless" || created["tls"] != false || created["certificate_mode"] != "external" {
		t.Fatalf("controlled fields = %#v", created)
	}
	inboundID := int64(created["id"].(float64))
	first := configJSONFrom(t, created)
	firstTLS := first["tls"].(map[string]any)
	firstReality := firstTLS["reality"].(map[string]any)
	firstPrivate := firstReality["private_key"].(string)
	if firstTLS["server_name"] != "www.nvidia.com" || firstReality["public_key"] == "" || firstReality["short_id"] == "" {
		t.Fatalf("controlled Reality not completed: %#v", first)
	}

	updated := request(t, h, http.MethodPatch, "/api/v1/ui/inbounds/"+strconv.FormatInt(inboundID, 10), token, map[string]any{
		"kind": "vless-reality", "reality": map[string]any{"handshake_server": "www.sony.com", "handshake_port": 8443},
	}, http.StatusOK)["inbound"].(map[string]any)
	updatedReality := configJSONFrom(t, updated)["tls"].(map[string]any)["reality"].(map[string]any)
	if updatedReality["private_key"] != firstPrivate {
		t.Fatal("ordinary controlled update rotated the Reality key")
	}
	if updatedReality["handshake"].(map[string]any)["server"] != "www.sony.com" {
		t.Fatalf("handshake was not updated: %#v", updatedReality)
	}

	rotated := request(t, h, http.MethodPatch, "/api/v1/ui/inbounds/"+strconv.FormatInt(inboundID, 10), token, map[string]any{
		"kind": "vless-reality", "rotate_reality_key": true,
	}, http.StatusOK)["inbound"].(map[string]any)
	rotatedReality := configJSONFrom(t, rotated)["tls"].(map[string]any)["reality"].(map[string]any)
	if rotatedReality["private_key"] == firstPrivate {
		t.Fatal("explicit rotation kept the old Reality key")
	}

	bad := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": serverID, "name": "caller-key", "kind": "vless-reality", "port": 8443,
		"config_json": `{"tls":{"reality":{"private_key":"caller-controlled"}}}`,
	}, http.StatusBadRequest)
	if bad["error_path"] != "config_json.tls.reality.private_key" {
		t.Fatalf("unexpected managed-key error: %#v", bad)
	}

	tcp := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": serverID, "name": "plain-vless", "kind": "vless-tcp", "port": 9443, "enabled": true,
	}, http.StatusCreated)["inbound"].(map[string]any)
	if tcp["protocol"] != "vless" || tcp["tls"] != false || tcp["certificate_mode"] != "external" {
		t.Fatalf("VLESS TCP was not derived from kind: %#v", tcp)
	}
	if strings.Contains(tcp["config_json"].(string), "reality") {
		t.Fatalf("VLESS TCP unexpectedly contains Reality: %s", tcp["config_json"])
	}
}

func TestDNSCredentialMaskedAndInboundValidation(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	secretTokenActive := true
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Header.Get("Authorization") != "Bearer secret-token" || !secretTokenActive {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": "invalid token"}}})
			return
		}
		switch r.URL.Path {
		case "/user/tokens/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "token-1", "status": "active"}})
		case "/zones/zone-1":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "zone-1", "name": "example.com"}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": "not found"}}})
		}
	}))
	defer cf.Close()
	srv := newTestServer(db, "test-secret", "")
	srv.dnsEndpoints.cloudflare = cf.URL
	h := srv.Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v1/ui/dns-credentials", token, map[string]any{"name": "primary", "provider": "cloudflare", "zone_name": "example.com", "zone_id": "zone-1", "config": map[string]any{"api_token": "secret-token"}}, http.StatusCreated)
	credential := created["dns_credential"].(map[string]any)
	credentialID := int64(credential["id"].(float64))
	if credential["configured"] != true || credential["config"] != nil || strings.Contains(fmt.Sprint(created), "secret-token") {
		t.Fatalf("DNS credential leaked secret or is not configured: %#v", created)
	}
	zones, ok := credential["zones"].([]any)
	if !ok || len(zones) != 1 || zones[0].(map[string]any)["zone_name"] != "example.com" {
		t.Fatalf("DNS credential zones = %#v", credential["zones"])
	}
	request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/dns-credentials/%d/verify", credentialID), token, map[string]any{}, http.StatusOK)
	secretTokenActive = false
	request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/dns-credentials/%d/verify", credentialID), token, map[string]any{}, http.StatusBadRequest)
	listed := request(t, h, http.MethodGet, "/api/v1/ui/dns-credentials", token, nil, http.StatusOK)["dns_credentials"].([]any)[0].(map[string]any)
	if listed["configured"] != true || listed["verified_at"] != nil || listed["last_error"] == "" || strings.Contains(fmt.Sprint(listed), "secret-token") {
		t.Fatalf("unexpected failed verification state: %#v", listed)
	}

	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "s1", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "missing-domain", "protocol": "shadowsocks", "listen_ip": "0.0.0.0", "port": 8388, "entry_ip_mode": "auto", "dns_sync_enabled": true, "config_json": `{"method":"2022-blake3-aes-128-gcm"}`, "enabled": true}, http.StatusBadRequest)
	request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "bad-domain", "protocol": "shadowsocks", "listen_ip": "0.0.0.0", "port": 8388, "entry_ip_mode": "custom", "external_ip": "203.0.113.20", "dns_domain": "not a domain", "dns_sync_enabled": true, "config_json": `{"method":"2022-blake3-aes-128-gcm"}`, "enabled": true}, http.StatusBadRequest)
}

func TestDeploymentSyncsDNSBeforeCreatingTasks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var postedRecord map[string]any
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.Header.Get("Authorization") != "Bearer cf-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": "bad token"}}})
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/tokens/verify":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "token-1", "status": "active"}})
		case r.Method == http.MethodGet && r.URL.Path == "/zones":
			name := r.URL.Query().Get("name")
			result := []map[string]any{}
			if name == "example.com" {
				result = []map[string]any{{"id": "zone-1", "name": "example.com"}}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []map[string]any{}})
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-1/dns_records":
			if err := json.NewDecoder(r.Body).Decode(&postedRecord); err != nil {
				t.Fatal(err)
			}
			postedRecord["id"] = "record-1"
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": postedRecord})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"message": r.URL.Path}}})
		}
	}))
	defer cf.Close()

	srv := newTestServer(db, "test-secret", "")
	srv.dnsEndpoints.cloudflare = cf.URL
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))
	createdCredential := request(t, h, http.MethodPost, "/api/v1/ui/dns-credentials", token, map[string]any{"name": "primary", "provider": "cloudflare", "zone_name": "example.com", "config": map[string]any{"api_token": "cf-token"}}, http.StatusCreated)
	credentialID := int64(createdCredential["dns_credential"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverID, "name": "dns-ss", "protocol": "shadowsocks", "listen_ip": "0.0.0.0", "port": 8388, "entry_ip_mode": "auto", "dns_credential_id": credentialID, "dns_domain": "entry.example.com", "dns_sync_enabled": true, "dns_record_types": "a", "config_json": `{"method":"2022-blake3-aes-128-gcm"}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))

	request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusBadRequest)
	tasks, err := db.ListTasksByServer(context.Background(), serverID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("deployment created tasks despite failed DNS sync: %#v", tasks)
	}

	request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/dns-credentials/%d/verify", credentialID), token, map[string]any{}, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	if postedRecord["type"] != "A" || postedRecord["name"] != "entry.example.com" || postedRecord["content"] != "203.0.113.10" || postedRecord["proxied"] != false || postedRecord["comment"] != "OBoard: 入口 dns-ss / 服务器 edge" {
		t.Fatalf("unexpected Cloudflare record payload: %#v", postedRecord)
	}
	storedInbound, err := db.GetInbound(context.Background(), inboundID)
	if err != nil {
		t.Fatal(err)
	}
	if storedInbound.DNSSyncError != "" || storedInbound.DNSSyncStatus == "" || storedInbound.DNSLastSyncedAt == nil {
		t.Fatalf("sync result not persisted: %#v", storedInbound)
	}
	manual := request(t, h, http.MethodPost, "/api/v1/ui/dns-sync", token, map[string]any{"inbound_id": inboundID}, http.StatusOK)
	if manual["success_count"] != float64(1) {
		t.Fatalf("unexpected manual DNS sync result: %#v", manual)
	}
}

func TestDeployFailsTasksForOfflineAgentImmediately(t *testing.T) {
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

	offline := &model.Server{Name: "offline-edge", AgentID: "agent-offline", AgentTokenHash: security.HashSecret("token-offline"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOffline}
	online := &model.Server{Name: "online-edge", AgentID: "agent-online", AgentTokenHash: security.HashSecret("token-online"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	for _, server := range []*model.Server{offline, online} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}

	deployment := request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	tasks := deployment["tasks"].([]any)
	if len(tasks) == 0 {
		t.Fatalf("expected deployment tasks, got %#v", deployment)
	}
	var offlineTasks, onlinePending int
	for _, raw := range tasks {
		task := raw.(map[string]any)
		serverID := int64(task["server_id"].(float64))
		status := task["status"].(string)
		switch serverID {
		case offline.ID:
			if status != "failed" {
				t.Fatalf("offline server task status = %q, want failed: %#v", status, task)
			}
			var result struct {
				Offline bool   `json:"offline"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(task["result_json"].(string)), &result); err != nil {
				t.Fatal(err)
			}
			if !result.Offline || !strings.Contains(result.Message, "离线") {
				t.Fatalf("offline failure result = %#v", result)
			}
			offlineTasks++
		case online.ID:
			if status != "pending" && status != "succeeded" {
				t.Fatalf("online server task status = %q, want pending or succeeded: %#v", status, task)
			}
			if status == "pending" {
				onlinePending++
			}
		}
	}
	if offlineTasks == 0 {
		t.Fatalf("expected failed tasks for offline agent: %#v", deployment)
	}
	if onlinePending == 0 {
		t.Fatalf("expected pending tasks for online agent: %#v", deployment)
	}
}

func TestExpireTimedOutTasksMarksPendingAndRunningAfterFiveMinutes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	pending := &model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "pending-timeout"}
	running := &model.AgentTask{ServerID: server.ID, Type: "detect_mtu", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "running-timeout"}
	fresh := &model.AgentTask{ServerID: server.ID, Type: "collect_logs", PayloadJSON: "{}", Status: "pending", ResultJSON: "{}", Nonce: "fresh"}
	for _, task := range []*model.AgentTask{pending, running, fresh} {
		if err := db.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-6 * time.Minute)
	if err := db.SetTaskStateForTest(ctx, pending.ID, "pending", old); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskStateForTest(ctx, running.ID, "running", old); err != nil {
		t.Fatal(err)
	}

	srv.expireTimedOutTasks(ctx)
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]model.AgentTask{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	for _, id := range []int64{pending.ID, running.ID} {
		task := byID[id]
		if task.Status != "failed" {
			t.Fatalf("task %d status = %q, want failed", id, task.Status)
		}
		var result struct {
			Timeout bool   `json:"timeout"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(task.ResultJSON), &result); err != nil {
			t.Fatal(err)
		}
		if !result.Timeout || !strings.Contains(result.Message, "超时") {
			t.Fatalf("timeout result for task %d = %#v (%s)", id, result, task.ResultJSON)
		}
	}
	if byID[fresh.ID].Status != "pending" {
		t.Fatalf("fresh task status = %q, want pending", byID[fresh.ID].Status)
	}
	if agentTaskPendingTimeout != 5*time.Minute || agentTaskRunningTimeout != 5*time.Minute {
		t.Fatalf("timeouts = %s/%s, want 5m/5m", agentTaskPendingTimeout, agentTaskRunningTimeout)
	}
}

func configJSONFrom(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal([]byte(payload["config_json"].(string)), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestPanelInboundWritesRejectUnknownRealityFieldWithPath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	createdServer := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "edge", "entry_ip_mode": "custom", "entry_address": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	serverID := int64(createdServer["server"].(map[string]any)["id"].(float64))

	invalidConfig := `{"tls":{"reality":{"enabled":true,"dest":"gateway.icloud.com:443"}}}`
	badCreate := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": serverID, "name": "invalid", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 10443,
		"config_json": invalidConfig, "enabled": true,
	}, http.StatusBadRequest)
	if badCreate["error_path"] != "config_json.tls.reality.dest" || !strings.Contains(fmt.Sprint(badCreate["error"]), "unsupported field") {
		t.Fatalf("create error = %#v, want precise Reality path", badCreate)
	}
	items, err := db.ListInbounds(context.Background())
	if err != nil || len(items) != 0 {
		t.Fatalf("invalid create persisted inbounds=%#v err=%v", items, err)
	}

	created := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{
		"server_id": serverID, "name": "valid", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 10443,
		"config_json": `{}`, "enabled": true,
	}, http.StatusCreated)
	inboundID := int64(created["inbound"].(map[string]any)["id"].(float64))
	badPatch := request(t, h, http.MethodPatch, "/api/v1/ui/inbounds/"+strconv.FormatInt(inboundID, 10), token, map[string]any{"config_json": invalidConfig}, http.StatusBadRequest)
	if badPatch["error_path"] != "config_json.tls.reality.dest" || !strings.Contains(fmt.Sprint(badPatch["error"]), "unsupported field") {
		t.Fatalf("patch error = %#v, want precise Reality path", badPatch)
	}
	stored, err := db.GetInbound(context.Background(), inboundID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ConfigJSON != "{}" {
		t.Fatalf("invalid patch changed stored config: %s", stored.ConfigJSON)
	}
}

func deriveRealityPublicForTest(t *testing.T, privateKey string) string {
	t.Helper()
	decoded, err := base64.RawURLEncoding.DecodeString(privateKey)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(privateKey)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 32 {
		t.Fatalf("private key length = %d, want 32", len(decoded))
	}
	key, err := ecdh.X25519().NewPrivateKey(decoded)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
}

func request(t *testing.T, h http.Handler, method, path, token string, body any, want int) map[string]any {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("content-type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("%s %s: want %d got %d body=%s", method, path, want, rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func TestAgentsUpdateAllCreatesTasks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SetSetting(ctx, "controller_url", "http://localhost"); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	online := &model.Server{Name: "online", AgentID: "agent-1", AgentTokenHash: security.HashSecret("t1"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	offline := &model.Server{Name: "offline", AgentID: "agent-2", AgentTokenHash: security.HashSecret("t2"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOffline}
	skipped := &model.Server{Name: "empty", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerUnknown}
	for _, server := range []*model.Server{online, offline, skipped} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}

	res := request(t, h, http.MethodPost, "/api/v1/ui/agents/update-all", token, map[string]any{}, http.StatusAccepted)
	summary := res["summary"].(map[string]any)
	if int(summary["total"].(float64)) != 3 {
		t.Fatalf("total = %#v, want 3", summary)
	}
	if int(summary["created"].(float64))+int(summary["failed"].(float64)) < 2 {
		t.Fatalf("expected created/failed for enrolled agents: %#v", summary)
	}
	if int(summary["skipped"].(float64)) != 1 {
		t.Fatalf("skipped = %#v, want 1", summary["skipped"])
	}
	tasks := res["tasks"].([]any)
	if len(tasks) < 1 {
		t.Fatalf("expected tasks: %#v", res)
	}
	// Same config_version batches bulk updates in the task center.
	version := res["config_version"]
	if version == nil || version == float64(0) {
		t.Fatalf("missing config_version: %#v", res)
	}
}

func TestDeploymentListenerConflictQueuesNoPartialTasks(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	first := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "first", "public_ipv4": "203.0.113.10", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010}, http.StatusCreated)
	second := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "second", "public_ipv4": "203.0.113.11", "listen_ip": "0.0.0.0", "port_range_start": 11000, "port_range_end": 11010}, http.StatusCreated)
	firstID := int64(first["server"].(map[string]any)["id"].(float64))
	secondID := int64(second["server"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": secondID, "name": "core-443", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/port-forwards", token, map[string]any{"name": "forward-443", "source_server_id": secondID, "target_server_id": firstID, "listen_ip": "192.0.2.20", "listen_port": 443, "target_address": "203.0.113.10", "target_port": 8443, "protocol": "tcp", "backend": "builtin", "config_json": `{}`, "enabled": true}, http.StatusCreated)

	request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusBadRequest)
	tasks, err := db.ListTasks(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("later-server listener conflict queued partial deployment tasks: %#v", tasks)
	}
}

func TestDisabledProxyPathDoesNotBlockPageDataOrDeployment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))

	serverC := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "C", "entry_ip_mode": "custom", "entry_address": "203.0.113.3", "listen_ip": "0.0.0.0", "port_range_start": 32000, "port_range_end": 32100}, http.StatusCreated)
	serverCID := int64(serverC["server"].(map[string]any)["id"].(float64))

	// Build a branch while it is disabled, then force a shape that path semantics
	// reject: a transparent forward after sing-box already decrypted the traffic.
	created := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": false}, http.StatusCreated)
	pathID := int64(created["proxy_path"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox"}, http.StatusCreated)
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 2, "node_type": "server_inbound", "server_id": serverCID, "transport_mode": "singbox"}, http.StatusCreated)
	steps, err := db.ListProxyPathStepsForPath(context.Background(), pathID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("step count = %d", len(steps))
	}
	steps[1].TransportMode = model.ProxyPathTransportPortForward
	if err := db.UpdateProxyPathStep(context.Background(), &steps[1]); err != nil {
		t.Fatal(err)
	}

	// A disabled half-configured branch must not take down the page or block the
	// deployment of every other server.
	request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=proxy-paths", token, nil, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)

	// Enabling it is the point where the operator must be told it is invalid. The
	// complete projection runs before any write, so neither the row nor the
	// durable runtime revision changes.
	beforeRevision, err := db.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodPatch, "/api/v1/ui/proxy-paths/"+itoa(pathID), token, map[string]any{"enabled": true}, http.StatusBadRequest)
	afterRevision, err := db.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if afterRevision != beforeRevision {
		t.Fatalf("rejected path update advanced runtime revision: %d -> %d", beforeRevision, afterRevision)
	}
	stored, err := db.GetProxyPath(context.Background(), pathID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("rejected enable must not persist")
	}
}

func TestProxyPathStepConfigDropsUnsupportedKeys(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	created := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(created["proxy_path"].(map[string]any)["id"].(float64))

	// internal_port is read by the config generator. Accepting it would bypass
	// port allocation and desynchronize the plan from the generated config.
	step := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox", "config_json": `{"chain_method":"2022-blake3-aes-256-gcm","internal_port":45678,"unknown":"x"}`}, http.StatusCreated)
	cfg := map[string]any{}
	if err := json.Unmarshal([]byte(step["proxy_path_step"].(map[string]any)["config_json"].(string)), &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg["internal_port"]; ok {
		t.Fatalf("internal_port must be dropped: %#v", cfg)
	}
	if _, ok := cfg["unknown"]; ok {
		t.Fatalf("unknown keys must be dropped: %#v", cfg)
	}
	if cfg["chain_method"] != "2022-blake3-aes-256-gcm" {
		t.Fatalf("chain_method must survive: %#v", cfg)
	}
	// An invalid forward backend is rejected instead of being stored verbatim.
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 2, "node_type": "server_inbound", "server_id": serverAID, "transport_mode": "port_forward", "config_json": `{"backend":"bogus"}`}, http.StatusBadRequest)
}

func TestProxyPathStepDeleteKeepsChainWhenValidationFails(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	serverC := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "C", "entry_ip_mode": "custom", "entry_address": "203.0.113.3", "listen_ip": "0.0.0.0", "port_range_start": 32000, "port_range_end": 32100}, http.StatusCreated)
	serverCID := int64(serverC["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	created := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(created["proxy_path"].(map[string]any)["id"].(float64))

	// A transparent prefix whose processing hop is the second step.
	first := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "port_forward"}, http.StatusCreated)
	firstID := int64(first["proxy_path_step"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 2, "node_type": "server_inbound", "server_id": serverCID, "transport_mode": "port_forward"}, http.StatusCreated)

	// Deleting from the first hop removes the whole chain, so the path goes too.
	result := request(t, h, http.MethodDelete, "/api/v1/ui/proxy-path-steps/"+itoa(firstID), token, nil, http.StatusOK)
	if result["path_deleted"] != true || int(result["deleted_steps"].(float64)) != 2 {
		t.Fatalf("delete result = %#v", result)
	}
	remaining, err := db.ListProxyPathStepsForPath(context.Background(), pathID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("steps survived a full-chain delete: %#v", remaining)
	}
}

func TestDeploymentPersistsAndReusesGeneratedPorts(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "vless", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	created := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(created["proxy_path"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox"}, http.StatusCreated)

	request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	allocations, err := db.ListProxyPathPortAllocations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 1 || allocations[0].Kind != model.ProxyPathPortKindChainService || allocations[0].ServerID != serverBID {
		t.Fatalf("deployment did not persist the shared listener port: %#v", allocations)
	}
	assigned := allocations[0].Port
	if assigned < 31000 || assigned > 31100 {
		t.Fatalf("allocated port %d is outside the target's configured range", assigned)
	}

	// A manual inbound must not be allowed to take a persisted generated port.
	// Letting the write succeed makes the next deployment fail after the
	// operator has already saved an apparently valid topology.
	request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverBID, "name": "squatter", "protocol": "vless", "listen_ip": "0.0.0.0", "port": assigned, "config_json": `{}`, "enabled": true}, http.StatusConflict)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	after, err := db.ListProxyPathPortAllocations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Port != assigned {
		t.Fatalf("stored allocation changed: before=%d after=%#v", assigned, after)
	}

	// Removing the branch releases the record so the port returns to the pool.
	request(t, h, http.MethodDelete, "/api/v1/ui/proxy-paths/"+itoa(pathID), token, nil, http.StatusOK)
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
	released, err := db.ListProxyPathPortAllocations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("allocation survived removal of its only consumer: %#v", released)
	}
}

func TestProxyPathStepDeletePreservesRootRoutingRulesAsDirectPath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	serverA := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "A", "entry_ip_mode": "custom", "entry_address": "203.0.113.1", "listen_ip": "0.0.0.0", "port_range_start": 30000, "port_range_end": 30100}, http.StatusCreated)
	serverAID := int64(serverA["server"].(map[string]any)["id"].(float64))
	serverB := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "B", "entry_ip_mode": "custom", "entry_address": "203.0.113.2", "listen_ip": "0.0.0.0", "port_range_start": 31000, "port_range_end": 31100}, http.StatusCreated)
	serverBID := int64(serverB["server"].(map[string]any)["id"].(float64))
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": serverAID, "name": "entry", "protocol": "vless", "listen_ip": "0.0.0.0", "port": 443, "config_json": `{}`, "enabled": true}, http.StatusCreated)
	inboundID := int64(inbound["inbound"].(map[string]any)["id"].(float64))
	path := request(t, h, http.MethodPost, "/api/v1/ui/proxy-paths", token, map[string]any{"inbound_id": inboundID, "enabled": true}, http.StatusCreated)
	pathID := int64(path["proxy_path"].(map[string]any)["id"].(float64))
	step := request(t, h, http.MethodPost, "/api/v1/ui/proxy-path-steps", token, map[string]any{"path_id": pathID, "position": 1, "node_type": "server_inbound", "server_id": serverBID, "transport_mode": "singbox"}, http.StatusCreated)
	stepID := int64(step["proxy_path_step"].(map[string]any)["id"].(float64))
	rule := request(t, h, http.MethodPost, "/api/v1/ui/routing-rules", token, map[string]any{"scope": "path_stage", "proxy_path_id": pathID, "sort_position": 0, "match_source": "inline", "name": "all-via-eth0", "match_json": `{}`, "action": "interface", "interface_name": "eth0", "enabled": true}, http.StatusCreated)
	ruleID := int64(rule["routing_rule"].(map[string]any)["id"].(float64))

	deleted := request(t, h, http.MethodDelete, "/api/v1/ui/proxy-path-steps/"+itoa(stepID), token, nil, http.StatusOK)
	if deleted["path_deleted"] != false || int(deleted["deleted_steps"].(float64)) != 1 {
		t.Fatalf("delete result = %#v", deleted)
	}
	retained := deleted["proxy_path"].(map[string]any)
	if retained["id"] != float64(pathID) || retained["kind"] != "direct" {
		t.Fatalf("retained path = %#v, want direct path %d", retained, pathID)
	}
	storedRule, err := db.GetRoutingRule(context.Background(), ruleID)
	if err != nil || storedRule.ProxyPathID == nil || *storedRule.ProxyPathID != pathID || storedRule.StageStepID != nil {
		t.Fatalf("root routing rule disappeared after removing its downstream hop: %#v err=%v", storedRule, err)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/deployments/apply", token, map[string]any{}, http.StatusAccepted)
}

func TestTrustedForwardAgentBuildGateAndSecretScrubbing(t *testing.T) {
	servers := []model.Server{
		{ID: 1, Name: "entry", AgentBuild: agentBuildMinTrustedForward},
		{ID: 2, Name: "processor", AgentBuild: "20260728000000"},
	}
	if err := validateTrustedForwardAgentBuilds(servers, map[int64]bool{1: true, 2: true}); err == nil {
		t.Fatal("old processing Agent passed trusted-forward build gate")
	}
	servers[1].AgentBuild = agentBuildMinTrustedForward
	if err := validateTrustedForwardAgentBuilds(servers, map[int64]bool{1: true, 2: true}); err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedForwardDeploymentScope(1, map[int64]bool{1: true, 2: true}); err == nil {
		t.Fatal("single-server deployment was allowed for a trusted transparent prefix")
	}
	if err := validateTrustedForwardDeploymentScope(3, map[int64]bool{1: true, 2: true}); err != nil {
		t.Fatalf("unrelated single-server deployment was rejected: %v", err)
	}
	if err := validateTrustedForwardDeploymentScope(0, map[int64]bool{1: true, 2: true}); err != nil {
		t.Fatalf("full deployment was rejected: %v", err)
	}

	raw := `{"port_forwards":{"rules":[{"trusted_forward":{"version":1,"receiver_id":"one","key":"sender-secret","max_clock_skew_seconds":120}}]},"config":"{\"_oboard\":{\"trusted_forward\":{\"receivers\":[{\"version\":1,\"id\":\"one\",\"target_port\":1234,\"key\":\"receiver-secret\",\"max_clock_skew_seconds\":120}]}}}"}`
	scrubbed := scrubManagedTunnelSecretsJSON(raw)
	if strings.Contains(scrubbed, "sender-secret") || strings.Contains(scrubbed, "receiver-secret") || strings.Count(scrubbed, "redacted") < 2 {
		t.Fatalf("trusted-forward secrets were not scrubbed: %s", scrubbed)
	}
	sshPayload := `{"ssh_inbounds":{"inbounds":[{"users":[{"user_id":7,"username":"oboard-7","password":"proxy-secret","enabled":true}]}]}}`
	sshScrubbed := scrubManagedTunnelSecretsJSON(sshPayload)
	if strings.Contains(sshScrubbed, "proxy-secret") || !strings.Contains(sshScrubbed, "redacted") {
		t.Fatalf("SSH inbound password was not scrubbed: %s", sshScrubbed)
	}
}

func TestTrustedForwardCoreRefreshRequiresMatchingFullDeployment(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "entry", AgentID: "agent-1", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	sender := &model.TrustedForwardSender{Version: 1, ReceiverID: "path-1", Key: "0123456789012345678901234567890123456789012", MaxClockSkewSeconds: 120}
	plan := model.PortForwardPlan{Rules: []model.PortForward{{ID: -1, ListenIP: "0.0.0.0", ListenPort: 443, TargetAddress: "203.0.113.2", TargetPort: 31000, Protocol: model.ForwardProtocolTCP, TrustedForward: sender}}}
	srv := newTestServer(db, "test-secret", "")
	if err := srv.requireTrustedForwardDeploymentBaseline(ctx, *server, `{}`, plan); err == nil {
		t.Fatal("trusted core refresh passed without a full deployment baseline")
	}
	payload, err := json.Marshal(model.DeploymentTaskPayload{Config: model.ApplyCoreConfigTaskPayload{Config: `{}`}, PortForwards: plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTask(ctx, &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: string(payload), Status: "succeeded", ResultJSON: `{}`, ConfigVersion: 1, Nonce: "trusted-baseline"}); err != nil {
		t.Fatal(err)
	}
	if err := srv.requireTrustedForwardDeploymentBaseline(ctx, *server, `{}`, plan); err != nil {
		t.Fatal(err)
	}
	changed := plan
	changed.Rules = append([]model.PortForward(nil), plan.Rules...)
	changed.Rules[0].TrustedForward = &model.TrustedForwardSender{Version: 1, ReceiverID: "path-2", Key: sender.Key, MaxClockSkewSeconds: 120}
	if err := srv.requireTrustedForwardDeploymentBaseline(ctx, *server, `{}`, changed); err == nil {
		t.Fatal("trusted core refresh passed with a stale full deployment baseline")
	}
}

func TestTrustedForwardFootprintIgnoresSharedReceiverOwnerPath(t *testing.T) {
	config := func(pathID int64) string {
		return fmt.Sprintf(`{"_oboard":{"trusted_forward":{"receivers":[{"version":1,"id":"inbound-17-transparent-step-1","path_id":%d,"inbound_tag":"oboard-inbound-17-transparent-step-1-in","network":"tcp","listen":"0.0.0.0","listen_port":31050,"target":"127.0.0.1","target_port":31051,"key":"receiver-key","max_clock_skew_seconds":120}]}}}`, pathID)
	}
	first, required, err := trustedForwardFootprint(config(29), model.PortForwardPlan{})
	if err != nil || !required {
		t.Fatalf("first shared receiver footprint = %q, required=%v, err=%v", first, required, err)
	}
	second, required, err := trustedForwardFootprint(config(30), model.PortForwardPlan{})
	if err != nil || !required {
		t.Fatalf("second shared receiver footprint = %q, required=%v, err=%v", second, required, err)
	}
	if first != second {
		t.Fatalf("shared receiver owner path changed topology footprint: first=%s second=%s", first, second)
	}
}

func TestDNSListSetDefault(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	listed := request(t, h, http.MethodGet, "/api/v1/ui/dns-lists", token, nil, http.StatusOK)
	defaults := listed["dns_lists"].([]any)
	var defaultBootstrapID int64
	for _, raw := range defaults {
		item := raw.(map[string]any)
		if item["kind"] == "bootstrap" && item["protected"] == true {
			defaultBootstrapID = int64(item["id"].(float64))
		}
	}
	candidates := []map[string]any{
		{"tag": "one", "transport": "doh", "server": "global.novaxns.one", "port": 443, "path": "/@hockey2168/dns-query", "tls_name": "global.novaxns.one"},
		{"tag": "two", "transport": "doq", "server": "dns.quad9.net", "port": 853, "tls_name": "dns.quad9.net"},
	}
	created := request(t, h, http.MethodPost, "/api/v1/ui/dns-lists", token, map[string]any{"name": "default candidate", "kind": "encrypted", "candidates": candidates}, http.StatusCreated)
	list := created["dns_list"].(map[string]any)
	setDefault := request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/dns-lists/%d/set-default", int64(list["id"].(float64))), token, nil, http.StatusOK)["dns_list"].(map[string]any)
	if setDefault["protected"] != true || setDefault["enabled"] != true {
		t.Fatalf("set-default response = %#v", setDefault)
	}
	after := request(t, h, http.MethodGet, "/api/v1/ui/dns-lists", token, nil, http.StatusOK)["dns_lists"].([]any)
	encryptedDefaults := 0
	for _, raw := range after {
		item := raw.(map[string]any)
		switch item["kind"] {
		case "encrypted":
			if item["protected"] == true {
				encryptedDefaults++
				if int64(item["id"].(float64)) != int64(list["id"].(float64)) {
					t.Fatalf("old default is still protected: %#v", item)
				}
			}
		case "bootstrap":
			if item["protected"] != true || int64(item["id"].(float64)) != defaultBootstrapID {
				t.Fatalf("bootstrap default was demoted: %#v", item)
			}
		}
	}
	if encryptedDefaults != 1 {
		t.Fatalf("encrypted defaults = %d, want 1", encryptedDefaults)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/dns-lists/999999/set-default", token, nil, http.StatusNotFound)
	disabled := request(t, h, http.MethodPost, "/api/v1/ui/dns-lists", token, map[string]any{"name": "disabled candidate", "kind": "encrypted", "candidates": candidates}, http.StatusCreated)["dns_list"].(map[string]any)
	request(t, h, http.MethodPut, fmt.Sprintf("/api/v1/ui/dns-lists/%d", int64(disabled["id"].(float64))), token, map[string]any{"name": "disabled candidate", "kind": "encrypted", "enabled": false, "candidates": candidates}, http.StatusOK)
	request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/dns-lists/%d/set-default", int64(disabled["id"].(float64))), token, nil, http.StatusBadRequest)
}

func TestServerPortRangePatchRejectsManagedPortExclusion(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	ctx := context.Background()
	server := &model.Server{Name: "port-edge", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	allocation := []model.ProxyPathPortAllocation{{Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: server.ID, Pool: model.PortPoolPublic, ListenIP: "0.0.0.0", Network: "tcp_udp", Port: 10050}}
	if err := db.SaveProxyPathPortAllocations(ctx, allocation, nil); err != nil {
		t.Fatal(err)
	}

	// Narrowing the public range below a managed chain-service port must be
	// rejected with the preview instead of silently saving.
	blocked := request(t, h, http.MethodPatch, fmt.Sprintf("/api/v1/ui/servers/%d", server.ID), token, map[string]any{"name": "port-edge", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20100}, http.StatusConflict)
	if blocked["error"] != "port_migration_required" {
		t.Fatalf("conflict body = %#v", blocked)
	}
	preview, ok := blocked["preview"].(map[string]any)
	if !ok {
		t.Fatalf("missing preview: %#v", blocked)
	}
	if !preview["public_range_changed"].(bool) {
		t.Fatalf("preview = %#v", preview)
	}
	affected := preview["affected_managed"].([]any)
	if len(affected) != 1 {
		t.Fatalf("affected managed = %#v", affected)
	}
	if item := affected[0].(map[string]any); int(item["port"].(float64)) != 10050 {
		t.Fatalf("affected item = %#v", item)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PortRangeStart != 10000 {
		t.Fatalf("server range was modified despite conflict: %#v", stored)
	}

	// Widening the range touches nothing and saves normally.
	widened := request(t, h, http.MethodPatch, fmt.Sprintf("/api/v1/ui/servers/%d", server.ID), token, map[string]any{"name": "port-edge", "listen_ip": "0.0.0.0", "port_range_start": 9000, "port_range_end": 20000}, http.StatusOK)
	if updated := widened["server"].(map[string]any); int(updated["port_range_start"].(float64)) != 9000 {
		t.Fatalf("widened server = %#v", updated)
	}
}

func TestServerPortRangePatchSeparatesPublicAndInternalPools(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	ctx := context.Background()
	server := &model.Server{Name: "pool-edge", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	allocation := []model.ProxyPathPortAllocation{
		{Kind: model.ProxyPathPortKindTunnelSSH, ScopeKey: "555", ServerID: server.ID, Pool: model.PortPoolInternal, ListenIP: "127.0.0.1", Network: "tcp", Port: 40010},
	}
	if err := db.SaveProxyPathPortAllocations(ctx, allocation, nil); err != nil {
		t.Fatal(err)
	}

	// Moving the public range must not be blocked by loopback allocations.
	request(t, h, http.MethodPatch, fmt.Sprintf("/api/v1/ui/servers/%d", server.ID), token, map[string]any{"name": "pool-edge", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20100, "internal_port_range_start": 30000, "internal_port_range_end": 59999}, http.StatusOK)

	// Excluding the loopback allocation from the internal pool is a conflict.
	blocked := request(t, h, http.MethodPatch, fmt.Sprintf("/api/v1/ui/servers/%d", server.ID), token, map[string]any{"name": "pool-edge", "listen_ip": "0.0.0.0", "port_range_start": 20000, "port_range_end": 20100, "internal_port_range_start": 60000, "internal_port_range_end": 61000}, http.StatusConflict)
	if blocked["error"] != "port_migration_required" {
		t.Fatalf("conflict body = %#v", blocked)
	}
	preview := blocked["preview"].(map[string]any)
	if !preview["internal_range_changed"].(bool) {
		t.Fatalf("preview = %#v", preview)
	}
	if affected := preview["affected_managed"].([]any); len(affected) != 1 {
		t.Fatalf("affected managed = %#v", affected)
	}
}
