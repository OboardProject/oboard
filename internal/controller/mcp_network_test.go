package controller

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)



func TestDNSListAndPolicyCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	operator := &model.User{Username: "operator", PasswordHash: "unused", Role: model.RoleOperator, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, operator); err != nil {
		t.Fatal(err)
	}
	admin := &model.User{Username: "root", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	adminPrincipal := userAutomationPrincipal(t, db, admin.ID)
	node := &model.Server{Name: "entry", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	encryptedInput := json.RawMessage(`{"dns_list":{"name":"加密组","kind":"encrypted","candidates":[{"tag":"cf","transport":"doh","server":"1.1.1.1","port":443},{"tag":"gg","transport":"doh","server":"8.8.8.8","port":443}]}}`)
	applyAutomationChangeset(t, server, adminPrincipal, "dns-list-create", automation.OperationRequest{Capability: "dns_lists.create", Input: encryptedInput})
	lists, err := db.ListDNSLists(ctx, false)
	if err != nil {
		t.Fatalf("lists err=%v", err)
	}
	var list *model.DNSList
	for index := range lists {
		if lists[index].Name == "加密组" {
			list = &lists[index]
			break
		}
	}
	if list == nil || list.Kind != model.DNSListEncrypted || len(list.Candidates) != 2 {
		t.Fatalf("created list missing: %#v", lists)
	}
	bootstrapInput := json.RawMessage(`{"dns_list":{"name":"引导组","kind":"bootstrap","candidates":[{"tag":"a1","transport":"udp","server":"1.1.1.1","port":53},{"tag":"a2","transport":"udp","server":"8.8.8.8","port":53}]}}`)
	applyAutomationChangeset(t, server, adminPrincipal, "dns-list-create2", automation.OperationRequest{Capability: "dns_lists.create", Input: bootstrapInput})
	lists, err = db.ListDNSLists(ctx, false)
	if err != nil {
		t.Fatalf("lists err=%v", err)
	}
	var bootstrap *model.DNSList
	for index := range lists {
		if lists[index].Name == "引导组" {
			bootstrap = &lists[index]
			break
		}
	}
	if bootstrap == nil {
		t.Fatal("created bootstrap list missing")
	}
	policyInput, _ := json.Marshal(map[string]any{"server_id": node.ID, "changes": map[string]any{"encrypted_list_id": list.ID, "bootstrap_list_id": bootstrap.ID, "strategy": "prefer_ipv4", "auto_test": "never"}})
	applyAutomationChangeset(t, server, adminPrincipal, "dns-policy-set", automation.OperationRequest{Capability: "servers.dns_policy.set", Input: policyInput})
	policy, err := db.GetServerDNSPolicy(ctx, node.ID)
	if err != nil || policy.Strategy != "prefer_ipv4" || policy.EncryptedListID != list.ID {
		t.Fatalf("policy=%#v err=%v", policy, err)
	}
	dnsTestInput, _ := json.Marshal(map[string]any{"server_id": node.ID, "action": "test"})
	applyAutomationChangeset(t, server, adminPrincipal, "dns-test", automation.OperationRequest{Capability: "servers.dns_test", Input: dnsTestInput})
	tasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
	if tasks[0].Type != model.AgentTaskTypeBenchmarkDNS {
		t.Fatalf("unexpected task type: %s", tasks[0].Type)
	}
	mtuInput, _ := json.Marshal(map[string]any{"server_id": node.ID, "target_host": "1.1.1.1", "target_port": 443})
	applyAutomationChangeset(t, server, adminPrincipal, "mtu-detect", automation.OperationRequest{Capability: "servers.mtu_detect", Input: mtuInput})
	mtuTasks, err := db.ListTasksByServer(ctx, node.ID, 10)
	if err != nil || len(mtuTasks) < 2 {
		t.Fatalf("mtu tasks=%#v err=%v", mtuTasks, err)
	}
	found := false
	for _, task := range mtuTasks {
		if task.Type == model.AgentTaskTypeDetectMTU {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("detect_mtu task was not queued")
	}
}

func TestPortForwardAndTunnelCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	operator := &model.User{Username: "operator", PasswordHash: "unused", Role: model.RoleOperator, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, operator); err != nil {
		t.Fatal(err)
	}
	principal := trafficAutomationPrincipal(t, server, "operator")
	source := &model.Server{Name: "source", PublicIPv4: "203.0.113.10", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 11000, Status: model.ServerOnline}
	target := &model.Server{Name: "target", PublicIPv4: "203.0.113.20", ListenIP: "0.0.0.0", PortRangeStart: 12000, PortRangeEnd: 13000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, target); err != nil {
		t.Fatal(err)
	}
	forwardInput := json.RawMessage(`{"port_forward":{"name":"测试转发","source_server_id":1,"listen_ip":"0.0.0.0","listen_port":10001,"target_address":"203.0.113.99","target_port":8080,"protocol":"tcp","backend":"auto","probe_mode":"never"}}`)
	applyAutomationChangeset(t, server, principal, "pf-create", automation.OperationRequest{Capability: "port_forwards.create", Input: forwardInput})
	forwards, err := db.ListPortForwards(ctx)
	if err != nil || len(forwards) != 1 {
		t.Fatalf("forwards=%#v err=%v", forwards, err)
	}
	forward := forwards[0]
	if forward.ListenPort != 10001 || forward.TargetServerID != 0 || !forward.Enabled {
		t.Fatalf("unexpected forward: %#v", forward)
	}
	updateInput, _ := json.Marshal(map[string]any{"port_forward_id": forward.ID, "changes": map[string]any{"listen_port": 10002}})
	applyAutomationChangeset(t, server, principal, "pf-update", automation.OperationRequest{Capability: "port_forwards.update", Input: updateInput})
	updated, err := db.GetPortForward(ctx, forward.ID)
	if err != nil || updated.ListenPort != 10002 {
		t.Fatalf("forward not updated: %#v err=%v", updated, err)
	}
	tunnelInput := json.RawMessage(`{"tunnel":{"name":"测试隧道","source_server_id":1,"target_server_id":2,"type":"wireguard","local_address":"10.0.0.1/30","peer_address":"10.0.0.2/30","listen_port":51820,"target_endpoint":"203.0.113.20","target_port":51820,"config_json":"{\"private_key\":\"aFakePrivateKeyForTestingOnly0123456789abcdefghijklmnopqrstuvwxyz\",\"peer_public_key\":\"aFakePublicKeyForTestingOnly0123456789abcdefghijklmnopqrstuvwxyz\"}"}}`)
	applyAutomationChangeset(t, server, principal, "tunnel-create", automation.OperationRequest{Capability: "tunnels.create", Input: tunnelInput})
	tunnels, err := db.ListTunnels(ctx)
	if err != nil || len(tunnels) != 1 {
		t.Fatalf("tunnels=%#v err=%v", tunnels, err)
	}
	tunnel := tunnels[0]
	if tunnel.Type != model.TunnelTypeWireGuard || tunnel.SourceServerID != source.ID {
		t.Fatalf("unexpected tunnel: %#v", tunnel)
	}
	deleteInput, _ := json.Marshal(map[string]any{"tunnel_id": tunnel.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "tunnel-delete", automation.OperationRequest{Capability: "tunnels.delete", Input: deleteInput})
	if _, err := db.GetTunnel(ctx, tunnel.ID); err == nil {
		t.Fatal("deleted tunnel still exists")
	}
	deleteForward, _ := json.Marshal(map[string]any{"port_forward_id": forward.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "pf-delete", automation.OperationRequest{Capability: "port_forwards.delete", Input: deleteForward})
	if _, err := db.GetPortForward(ctx, forward.ID); err == nil {
		t.Fatal("deleted forward still exists")
	}
}
