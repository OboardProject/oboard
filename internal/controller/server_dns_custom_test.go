package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestServerDNSPolicyCustomResolversDeployWithoutEncryptedDNS(t *testing.T) {
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
	node := &model.Server{Name: "custom-dns-edge", AgentID: "custom-dns-agent", AgentTokenHash: security.HashSecret("custom-dns-token"), ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 12100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}

	saved := request(t, h, http.MethodPut, fmt.Sprintf("/api/v1/ui/servers/%d/dns-policy", node.ID), token, map[string]any{
		"encrypted_list_id":    0,
		"bootstrap_candidates": []map[string]any{{"transport": "udp", "server": "9.9.9.9", "port": 53}},
		"strategy":             "auto",
		"auto_test":            "first_apply",
	}, http.StatusOK)["dns_policy"].(map[string]any)
	if saved["encrypted_source"] != "none" || saved["bootstrap_source"] != "custom" {
		t.Fatalf("saved sources = %v/%v, want none/custom", saved["encrypted_source"], saved["bootstrap_source"])
	}
	policy, err := db.GetServerDNSPolicy(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := db.GetDNSList(ctx, policy.BootstrapListID)
	if err != nil || owned.OwnerServerID != node.ID {
		t.Fatalf("custom bootstrap list = %#v, err=%v", owned, err)
	}
	request(t, h, http.MethodPut, fmt.Sprintf("/api/v1/ui/dns-lists/%d", owned.ID), token, map[string]any{"name": "x", "kind": "bootstrap", "enabled": true, "candidates": owned.Candidates}, http.StatusConflict)
	request(t, h, http.MethodDelete, fmt.Sprintf("/api/v1/ui/dns-lists/%d", owned.ID), token, nil, http.StatusConflict)
	request(t, h, http.MethodPost, "/api/v1/ui/dns-lists", token, map[string]any{"name": "@server/1/bootstrap", "kind": "bootstrap", "candidates": []map[string]any{{"tag": "a", "transport": "udp", "server": "1.1.1.1", "port": 53}, {"tag": "b", "transport": "udp", "server": "8.8.8.8", "port": 53}}}, http.StatusBadRequest)

	benchmark := request(t, h, http.MethodPost, fmt.Sprintf("/api/v1/ui/servers/%d/dns-test", node.ID), token, map[string]any{"action": "test_and_apply"}, http.StatusAccepted)
	task, err := db.GetTask(ctx, int64(benchmark["task"].(map[string]any)["id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	var plan model.DNSBenchmarkPlan
	if err := json.Unmarshal([]byte(task.PayloadJSON), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.BootstrapListID != owned.ID || len(plan.BootstrapCandidates) != 1 || plan.BootstrapCandidates[0].Server != "9.9.9.9" || len(plan.EncryptedCandidates) != 0 {
		t.Fatalf("custom benchmark plan = %#v", plan)
	}
	result := model.DNSBenchmarkResult{
		ReportID: "custom-report", RequestID: benchmark["run"].(map[string]any)["request_id"].(string), PolicyRevision: policy.Revision,
		BootstrapListID: owned.ID, BootstrapListRevision: owned.Revision,
		Bootstrap: model.DNSBenchmarkGroup{Items: []model.DNSBenchmarkItem{{Tag: "custom-1", LatencyMS: 5}}, BestTags: []string{"custom-1"}},
	}
	body, _ := json.Marshal(result)
	report := httptest.NewRequest(http.MethodPost, "/api/v1/agent/dns-benchmarks", bytes.NewReader(body))
	report.Header.Set("content-type", "application/json")
	report.Header.Set("X-Agent-ID", node.AgentID)
	report.Header.Set("Authorization", "Bearer custom-dns-token")
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, report)
	if recorder.Code != http.StatusOK {
		t.Fatalf("custom dns report status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	tasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil || len(tasks) == 0 || tasks[0].Type != model.AgentTaskTypeApplyCoreConfig {
		t.Fatalf("custom dns apply tasks = %#v, err=%v", tasks, err)
	}
	var payload model.ApplyCoreConfigTaskPayload
	if err := json.Unmarshal([]byte(tasks[0].PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(payload.Config), &config); err != nil {
		t.Fatal(err)
	}
	dns := config["dns"].(map[string]any)
	if dns["final"] != "bootstrap-primary" {
		t.Fatalf("custom plain dns final = %v, want bootstrap-primary", dns["final"])
	}
	found := false
	for _, item := range dns["servers"].([]any) {
		server := item.(map[string]any)
		if server["tag"] == "remote-primary" {
			t.Fatal("plain custom dns must not emit an encrypted resolver")
		}
		if server["tag"] == "bootstrap-primary" && server["server"] == "9.9.9.9" {
			found = true
		}
	}
	if !found {
		t.Fatalf("custom bootstrap resolver missing from %#v", dns["servers"])
	}
}

func TestServerDNSPolicyCapabilitySwitchesCustomResolvers(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "root", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	node := &model.Server{Name: "entry", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	shared, err := db.EnsureServerDNSPolicy(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	customInput, _ := json.Marshal(map[string]any{"server_id": node.ID, "changes": map[string]any{
		"encrypted_candidates": []map[string]any{{"transport": "dot", "server": "dns.example.net", "port": 853}},
	}})
	applyAutomationChangeset(t, server, principal, "dns-policy-custom", automation.OperationRequest{Capability: "servers.dns_policy.set", Input: customInput})
	policy, err := db.GetServerDNSPolicy(ctx, node.ID)
	if err != nil || policy.EncryptedSource != model.DNSSourceCustom || policy.BootstrapListID != shared.BootstrapListID {
		t.Fatalf("custom policy = %#v, err=%v", policy, err)
	}
	sharedInput, _ := json.Marshal(map[string]any{"server_id": node.ID, "changes": map[string]any{"encrypted_list_id": shared.EncryptedListID}})
	applyAutomationChangeset(t, server, principal, "dns-policy-shared", automation.OperationRequest{Capability: "servers.dns_policy.set", Input: sharedInput})
	policy, err = db.GetServerDNSPolicy(ctx, node.ID)
	if err != nil || policy.EncryptedSource != model.DNSSourceShared || policy.EncryptedListID != shared.EncryptedListID {
		t.Fatalf("shared policy = %#v, err=%v", policy, err)
	}
}
