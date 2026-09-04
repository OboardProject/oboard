package controller

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestBuildInboundProbePlanUsesProtocolTransport(t *testing.T) {
	server := model.Server{ID: 2, PublicIPv4: "203.0.113.2"}
	plan, _ := buildInboundProbePlans(9, server, []model.Inbound{
		{ID: 1, ServerID: 2, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, Enabled: true},
		{ID: 2, ServerID: 2, Name: "hy2", Protocol: model.ProtocolHY2, ListenIP: "0.0.0.0", Port: 8443, Enabled: true},
		{ID: 3, ServerID: 2, Name: "ssh", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, Enabled: true},
	}, nil, false)
	if len(plan.EntryTargets) != 2 || plan.EntryTargets[0].Transport != "tcp" || plan.EntryTargets[1].Transport != "udp" {
		t.Fatalf("unexpected probe plan: %#v", plan)
	}
}

func TestBuildInboundProbePlansUseClientFacingExternalEndpoint(t *testing.T) {
	tests := []struct {
		name         string
		server       model.Server
		inbound      model.Inbound
		wantHost     string
		wantLocal    int
		wantExternal int
	}{
		{
			name:         "inbound custom address and NAT port",
			server:       model.Server{ID: 2, PublicIPv4: "203.0.113.2"},
			inbound:      model.Inbound{ID: 1, ServerID: 2, Name: "ss", Protocol: model.ProtocolSS, ListenIP: "0.0.0.0", Port: 3002, AdvertisePort: 30618, EntryIPMode: model.EntryIPModeCustom, ExternalIP: "jp22s", Enabled: true},
			wantHost:     "jp22s",
			wantLocal:    3002,
			wantExternal: 30618,
		},
		{
			name:         "server custom address",
			server:       model.Server{ID: 2, EntryIPMode: model.EntryIPModeCustom, EntryAddress: "edge.example.com"},
			inbound:      model.Inbound{ID: 1, ServerID: 2, Name: "hy2", Protocol: model.ProtocolHY2, ListenIP: "0.0.0.0", Port: 8443, Enabled: true},
			wantHost:     "edge.example.com",
			wantLocal:    8443,
			wantExternal: 8443,
		},
		{
			name:         "NAT port with detected address",
			server:       model.Server{ID: 2, PublicIPv4: "203.0.113.2"},
			inbound:      model.Inbound{ID: 1, ServerID: 2, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, AdvertisePort: 30443, Enabled: true},
			wantHost:     "203.0.113.2",
			wantLocal:    443,
			wantExternal: 30443,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			local, external := buildInboundProbePlans(9, test.server, []model.Inbound{test.inbound}, nil, false)
			if len(local.EntryTargets) != 1 || local.EntryTargets[0].Port != test.wantLocal {
				t.Fatalf("local probe targets = %#v, want port %d", local.EntryTargets, test.wantLocal)
			}
			if len(external.EntryTargets) != 1 || external.EntryTargets[0].Host != test.wantHost || external.EntryTargets[0].Port != test.wantExternal {
				t.Fatalf("external probe targets = %#v, want %s:%d", external.EntryTargets, test.wantHost, test.wantExternal)
			}
		})
	}
}

func TestBuildInboundProbePlansUseMieruAdvertisedPort(t *testing.T) {
	server := model.Server{ID: 2, PublicIPv4: "203.0.113.2"}
	inbound := model.Inbound{ID: 3, ServerID: 2, Name: "mieru", Protocol: model.ProtocolMieru, ListenIP: "0.0.0.0", Port: 3002, AdvertisePort: 30618, ConfigJSON: `{"transport":"TCP"}`, Enabled: true}
	local, external := buildInboundProbePlans(9, server, []model.Inbound{inbound}, nil, false)
	if len(local.EntryTargets) != 1 || local.EntryTargets[0].Port != 3002 {
		t.Fatalf("Mieru local probe targets = %#v", local.EntryTargets)
	}
	if len(external.EntryTargets) != 1 || external.EntryTargets[0].Port != 30618 {
		t.Fatalf("Mieru external probe targets = %#v", external.EntryTargets)
	}
}

func TestBuildInboundProbePlansUseSnellRuntimeAndAdvertisedPorts(t *testing.T) {
	server := model.Server{ID: 2, PublicIPv4: "203.0.113.2", ListenIP: "0.0.0.0"}
	inbound := model.Inbound{ID: 4, ServerID: server.ID, Name: "snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 52243, AdvertisePort: 443, Enabled: true}
	ledger := core.NewProxyPathPortLedger([]model.ProxyPathPortAllocation{
		{Kind: model.ProxyPathPortKindSnellUser, ScopeKey: "inbound:4:user:7:path:0", ServerID: server.ID, Port: 32107, State: model.PortAllocationStateActive, Generation: 1},
		{Kind: model.ProxyPathPortKindSnellUser, ScopeKey: "inbound:9:user:7:path:0", ServerID: server.ID, Port: 32108, State: model.PortAllocationStateActive, Generation: 1},
	})
	local, external := buildInboundProbePlans(9, server, []model.Inbound{inbound}, ledger, false)
	if len(local.EntryTargets) != 1 || local.EntryTargets[0].Port != 32107 {
		t.Fatalf("Snell local probe must use the runtime listener: %#v", local.EntryTargets)
	}
	if len(external.EntryTargets) != 1 || external.EntryTargets[0].Port != 443 {
		t.Fatalf("Snell external probe must use the advertised endpoint: %#v", external.EntryTargets)
	}
	if local.EntryTargets[0].Port == inbound.Port {
		t.Fatalf("Snell logical port %d must not be probed", inbound.Port)
	}
}

func TestFocusedCoreRefreshPersistsSnellRuntimeProbePort(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	server := model.Server{Name: "snell-edge", AgentID: "agent-1", ListenIP: "0.0.0.0", PublicIPv4: "203.0.113.2", PortRangeStart: 32000, PortRangeEnd: 32100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: server.ID, Name: "snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 52243, ConfigJSON: `{"version":4,"psk":"inbound-seed-psk-1234"}`, Enabled: true}
	if err := db.CreateInbound(ctx, &inbound); err != nil {
		t.Fatal(err)
	}
	otherServer := model.Server{Name: "other-edge", AgentID: "agent-2", ListenIP: "0.0.0.0", PublicIPv4: "203.0.113.3", PortRangeStart: 33000, PortRangeEnd: 33100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, &otherServer); err != nil {
		t.Fatal(err)
	}
	otherInbound := model.Inbound{ServerID: otherServer.ID, Name: "other-snell", Protocol: model.ProtocolSnell, ListenIP: "0.0.0.0", Port: 52244, ConfigJSON: `{"version":4,"psk":"other-inbound-seed-psk"}`, Enabled: true}
	if err := db.CreateInbound(ctx, &otherInbound); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "secret", "")
	if err := srv.queueCoreConfigRefreshForServers(ctx, []int64{server.ID}, "snell_test"); err != nil {
		t.Fatal(err)
	}
	allocations, err := db.ListProxyPathPortAllocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ledger := core.NewProxyPathPortLedger(allocations)
	ports := core.SnellRuntimeProbePorts(ledger, inbound, false)
	if len(ports) != 1 || ports[0] == inbound.Port {
		t.Fatalf("focused refresh did not persist the generated Snell listener port: ports=%v allocations=%#v", ports, allocations)
	}
	if otherPorts := core.SnellRuntimeProbePorts(ledger, otherInbound, false); len(otherPorts) != 0 {
		t.Fatalf("focused refresh persisted ports for an unqueued server: %v", otherPorts)
	}
}

func TestControllerProbeTargetCollectsSamples(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	result := controllerProbeTarget(context.Background(), 12, model.InboundProbeTarget{InboundID: 3, Host: "127.0.0.1", Port: port, Transport: "tcp"}, "controller_external")
	if !result.Available || !result.Confirmed || result.SuccessCount != inboundProbeSamples || result.P95LatencyMS < result.MinLatencyMS {
		t.Fatalf("unexpected controller probe: %#v", result)
	}
}

func TestControllerProbeTaskWaitsForAppliedConfigAndStoresResult(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	server := model.Server{Name: "probe", PublicIPv4: "127.0.0.1", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(context.Background(), &server); err != nil {
		t.Fatal(err)
	}
	inbound := model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: listener.Addr().(*net.TCPAddr).Port, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(context.Background(), &inbound); err != nil {
		t.Fatal(err)
	}
	applyTask := model.AgentTask{ServerID: server.ID, Type: "apply_core_config", PayloadJSON: "{}", ResultJSON: "{}", Status: "succeeded", ConfigVersion: 44, Nonce: "apply"}
	if err := db.CreateTask(context.Background(), &applyTask); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(db, "secret", "")
	_, plan := buildInboundProbePlans(44, server, []model.Inbound{inbound}, nil, false)
	probeTask, err := srv.createControllerInboundProbeTask(context.Background(), applyTask.ID, 0, plan)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := db.GetTask(context.Background(), probeTask.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Status == "succeeded" {
			results, err := db.ListInboundProbeResults(context.Background(), server.ID, inbound.ID, 10)
			if err != nil || len(results) != 1 || !results[0].Available {
				t.Fatalf("probe result not stored: results=%#v err=%v", results, err)
			}
			return
		}
		if current.Status == "failed" {
			t.Fatalf("probe task failed: %s", current.ResultJSON)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("controller probe task did not finish")
}
