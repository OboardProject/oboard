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

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestNetworkInterfacesEndpointQueuesSupportedAgentTask(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	server := &model.Server{
		Name:           "edge",
		AgentID:        "agent-edge",
		AgentTokenHash: security.HashSecret("agent-token"),
		AgentBuild:     "20260804000000",
		ListenIP:       "0.0.0.0",
		PortRangeStart: 10000,
		PortRangeEnd:   10100,
		Status:         model.ServerOnline,
	}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/ui/servers/%d/network-interfaces", server.ID)
	request(t, h, http.MethodPost, path, token, map[string]any{}, http.StatusConflict)

	server.AgentBuild = agentBuildMinNetworkInterfaces
	if err := db.UpdateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	response := request(t, h, http.MethodPost, path, token, map[string]any{}, http.StatusAccepted)
	task := response["task"].(map[string]any)
	if task["type"] != model.AgentTaskTypeListNetworkInterfaces || task["status"] != "pending" {
		t.Fatalf("unexpected network interface task: %#v", task)
	}
}

func TestValidateNetworkInterfacesTaskResult(t *testing.T) {
	valid := `{"message":"network interfaces listed","interfaces":[{"name":"eth0","up":true,"running":true,"loopback":false,"addresses":["192.0.2.10/24","2001:db8::10/64"]}]}`
	if err := validateNetworkInterfacesTaskResult(valid); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	for name, raw := range map[string]string{
		"empty name":        `{"interfaces":[{"name":"","addresses":[]}]}`,
		"invalid name":      `{"interfaces":[{"name":"eth0;id","addresses":[]}]}`,
		"duplicate name":    `{"interfaces":[{"name":"eth0","addresses":[]},{"name":"eth0","addresses":[]}]}`,
		"invalid address":   `{"interfaces":[{"name":"eth0","addresses":["not-an-ip"]}]}`,
		"duplicate address": `{"interfaces":[{"name":"eth0","addresses":["192.0.2.1/24","192.0.2.1/24"]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateNetworkInterfacesTaskResult(raw); err == nil {
				t.Fatal("invalid result was accepted")
			}
		})
	}
}

func TestNetworkInterfaceIPStackIgnoresLocalOnlyAddresses(t *testing.T) {
	for _, test := range []struct {
		name      string
		addresses []string
		want      model.IPStack
	}{
		{name: "IPv4", addresses: []string{"192.0.2.10/24", "fe80::1/64"}, want: model.IPStackIPv4Only},
		{name: "HE IPv6", addresses: []string{"2001:470:1f00::2/64", "fe80::1/64"}, want: model.IPStackIPv6Only},
		{name: "dual stack", addresses: []string{"10.0.0.2/24", "2001:db8::2/64"}, want: model.IPStackDualStack},
		{name: "local only", addresses: []string{"127.0.0.1/8", "::1/128", "fe80::1/64"}, want: model.IPStackAuto},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := networkInterfaceIPStack(model.NetworkInterfaceInfo{Addresses: test.addresses}); got != test.want {
				t.Fatalf("networkInterfaceIPStack(%v) = %q, want %q", test.addresses, got, test.want)
			}
		})
	}
}

func TestRoutingRulesUseLatestAgentInterfaceIPStack(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := &model.Server{Name: "edge", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeListNetworkInterfaces, PayloadJSON: `{}`, Status: "pending", ResultJSON: `{}`, Nonce: "interfaces"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := db.CompleteTask(ctx, task.ID, "succeeded", `{"interfaces":[{"name":"he-ipv6","up":true,"running":true,"loopback":false,"addresses":["2001:470:1f00::2/64","fe80::1/64"]}]}`); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "test-secret", "")
	rules, err := srv.routingRulesWithInterfaceIPStacks(ctx, server.ID, []model.RoutingRule{{ServerID: server.ID, Action: model.RouteActionProxyPath, InterfaceName: "he-ipv6", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].InterfaceIPStack != model.IPStackIPv6Only {
		t.Fatalf("resolved rules = %#v", rules)
	}
}

func TestNetworkInterfacesTaskCallbackRejectsInvalidResult(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{
		Name: "edge", AgentID: "agent-edge", AgentTokenHash: security.HashSecret("agent-token"),
		ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10100, Status: model.ServerOnline,
	}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	task := &model.AgentTask{
		ServerID: server.ID, Type: model.AgentTaskTypeListNetworkInterfaces, PayloadJSON: "{}",
		Status: "pending", ResultJSON: "{}", ConfigVersion: 1, Nonce: "network-interfaces",
	}
	if err := db.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	report, _ := json.Marshal(map[string]any{
		"task_id": task.ID, "status": "succeeded",
		"result_json": `{"interfaces":[{"name":"eth0;id","addresses":[]}]}`,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/task-results", bytes.NewReader(report))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", server.AgentID)
	req.Header.Set("Authorization", "Bearer agent-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid callback status=%d body=%s", rr.Code, rr.Body.String())
	}
	stored, err := db.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "pending" {
		t.Fatalf("invalid callback completed task: %#v", stored)
	}
}
