package controller

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestBuildInboundProbePlanUsesProtocolTransport(t *testing.T) {
	server := model.Server{ID: 2, PublicIPv4: "203.0.113.2"}
	plan := buildInboundProbePlan(9, server, []model.Inbound{
		{ID: 1, ServerID: 2, Name: "vless", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, Enabled: true},
		{ID: 2, ServerID: 2, Name: "hy2", Protocol: model.ProtocolHY2, ListenIP: "0.0.0.0", Port: 8443, Enabled: true},
		{ID: 3, ServerID: 2, Name: "ssh", Protocol: model.ProtocolSSH, ListenIP: "0.0.0.0", Port: 2222, Enabled: true},
	})
	if len(plan.EntryTargets) != 2 || plan.EntryTargets[0].Transport != "tcp" || plan.EntryTargets[1].Transport != "udp" {
		t.Fatalf("unexpected probe plan: %#v", plan)
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
	plan := buildInboundProbePlan(44, server, []model.Inbound{inbound})
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
