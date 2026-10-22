package core

import (
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func portPolicyMigrationFixture(t *testing.T) (model.Server, model.Server, PortPolicyMigrationInput) {
	t.Helper()
	serverA := model.Server{ID: 1, Name: "A", ChainSecret: "chain-a", PublicIPv4: "203.0.113.1", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 30000, PortRangeEnd: 30100}
	serverB := model.Server{ID: 2, Name: "B", ChainSecret: "chain-b", PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0", IPStack: model.IPStackIPv4Only, PortRangeStart: 31000, PortRangeEnd: 31100}
	root := model.Inbound{ID: 10, ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	path := model.ProxyPath{ID: 10, Name: "ss", InboundID: root.ID, Secret: "path-ss", Enabled: true}
	bID := serverB.ID
	step := model.ProxyPathStep{ID: 11, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &bID, TransportMode: model.ProxyPathTransportSingBox, ConfigJSON: `{"chain_protocol":"shadowsocks"}`}
	input := PortPolicyMigrationInput{Inbounds: []model.Inbound{root}, Paths: []model.ProxyPath{path}, Steps: []model.ProxyPathStep{step}}
	ledger := NewProxyPathPortLedger(nil)
	if _, err := BuildProxyPathPlansWithLedger(input.Paths, input.Steps, []model.Server{serverA, serverB}, input.Inbounds, ledger); err != nil {
		t.Fatal(err)
	}
	for i, row := range ledger.Pending() {
		row.ID = int64(i + 1)
		input.Allocations = append(input.Allocations, row)
	}
	if len(input.Allocations) != 1 || input.Allocations[0].Kind != model.ProxyPathPortKindChainService || input.Allocations[0].ServerID != serverB.ID {
		t.Fatalf("fixture allocations = %#v", input.Allocations)
	}
	return serverA, serverB, input
}

func TestPlanServerPortPolicyMigrationMovesManagedListenerIntoNewRange(t *testing.T) {
	serverA, current, input := portPolicyMigrationFixture(t)
	next := current
	next.PortRangeStart, next.PortRangeEnd = 32000, 32001
	input.Server = next
	input.Servers = []model.Server{serverA, next}
	// A manual forward already holds 32000, so the only free port is 32001.
	input.Forwards = []model.PortForward{{ID: 1, SourceServerID: next.ID, ListenIP: "0.0.0.0", ListenPort: 32000, Protocol: model.ForwardProtocolTCPUDP, Enabled: true}}
	preview := PreviewServerPortPolicyChange(current, next, input.Allocations, input.Inbounds)
	if !preview.RequiresMigration() {
		t.Fatalf("preview = %#v", preview)
	}
	plan, err := PlanServerPortPolicyMigration(preview, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].FromPort != input.Allocations[0].Port || plan.Moves[0].ToPort != 32001 {
		t.Fatalf("moves = %#v", plan.Moves)
	}
	if len(plan.RemovedIDs) != 1 || plan.RemovedIDs[0] != 1 || len(plan.Added) != 1 || plan.Added[0].Port != 32001 || plan.Added[0].State != model.PortAllocationStateActive {
		t.Fatalf("plan rows = removed %#v added %#v", plan.RemovedIDs, plan.Added)
	}
}

func TestPlanServerPortPolicyMigrationFailsWhenNewRangeIsFull(t *testing.T) {
	serverA, current, input := portPolicyMigrationFixture(t)
	next := current
	next.PortRangeStart, next.PortRangeEnd = 32000, 32000
	input.Server = next
	input.Servers = []model.Server{serverA, next}
	input.Forwards = []model.PortForward{{ID: 1, SourceServerID: next.ID, ListenIP: "0.0.0.0", ListenPort: 32000, Protocol: model.ForwardProtocolTCPUDP, Enabled: true}}
	preview := PreviewServerPortPolicyChange(current, next, input.Allocations, input.Inbounds)
	_, err := PlanServerPortPolicyMigration(preview, input)
	if err == nil || !strings.Contains(err.Error(), "已耗尽") {
		t.Fatalf("err = %v", err)
	}
}

func TestPlanServerPortPolicyMigrationReleasesUnclaimedOwner(t *testing.T) {
	current := model.Server{ID: 2, Name: "B", ListenIP: "0.0.0.0", PortRangeStart: 31000, PortRangeEnd: 31100}
	next := current
	next.PortRangeStart, next.PortRangeEnd = 32000, 32100
	allocations := []model.ProxyPathPortAllocation{{ID: 7, Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: 2, Pool: model.PortPoolPublic, Port: 31050}}
	preview := PreviewServerPortPolicyChange(current, next, allocations, nil)
	plan, err := PlanServerPortPolicyMigration(preview, PortPolicyMigrationInput{Server: next, Servers: []model.Server{next}, Allocations: allocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Moves) != 0 || len(plan.Released) != 1 || len(plan.RemovedIDs) != 1 || plan.RemovedIDs[0] != 7 {
		t.Fatalf("plan = %#v", plan)
	}
}
