package controller

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestParseMieruSimpleImportSplitsMixedTransports(t *testing.T) {
	items, err := parseExternalOutboundLine("mierus://alice:secret@example.com?profile=edge&port=8964&port=8965-8966&port=9000&protocol=TCP&protocol=TCP&protocol=UDP&multiplexing=MULTIPLEXING_HIGH")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("imported nodes = %d, want 2", len(items))
	}
	if items[0].Protocol != model.ProtocolMieru || items[0].TargetPort != 8964 || !strings.HasSuffix(items[0].Name, "TCP") {
		t.Fatalf("TCP node = %#v", items[0])
	}
	if items[1].TargetPort != 9000 || !strings.HasSuffix(items[1].Name, "UDP") {
		t.Fatalf("UDP node = %#v", items[1])
	}
	var tcpConfig map[string]any
	if err := json.Unmarshal([]byte(items[0].ConfigJSON), &tcpConfig); err != nil {
		t.Fatal(err)
	}
	if tcpConfig["transport"] != "TCP" || tcpConfig["username"] != "alice" || tcpConfig["password"] != "secret" {
		t.Fatalf("TCP Mieru config = %#v", tcpConfig)
	}
	if got := stringSliceFromControllerJSON(tcpConfig["server_ports"]); !reflect.DeepEqual(got, []string{"8965-8966"}) {
		t.Fatalf("TCP ranges = %v", got)
	}
}

func TestParseMieruBinaryImportIsRejected(t *testing.T) {
	if _, err := parseExternalOutboundLine("mieru://AAAA"); err == nil || !strings.Contains(err.Error(), "mieru://") {
		t.Fatalf("binary Mieru import error = %v", err)
	}
}

func TestRawMieruOutboundImportIsAccepted(t *testing.T) {
	items, err := parseExternalOutboundImport(`{"type":"mieru","tag":"edge","server":"edge.example.com","server_port":8964,"server_ports":["8965-8966"],"transport":"TCP","username":"alice","password":"secret"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Protocol != model.ProtocolMieru || items[0].TargetPort != 8964 {
		t.Fatalf("raw Mieru import = %#v", items)
	}
}

func TestBuildInboundProbePlanExpandsMieruPorts(t *testing.T) {
	server := model.Server{ID: 1, PublicIPv4: "203.0.113.10"}
	inbound := model.Inbound{
		ID: 2, ServerID: server.ID, Name: "Mieru", Protocol: model.ProtocolMieru,
		Port: 8964, ListenIP: "0.0.0.0", EntryIPMode: model.EntryIPModeIPv4, ConfigJSON: `{"transport":"UDP","listen_ports":["8965-8966"]}`, Enabled: true,
	}
	plan := buildInboundProbePlan(10, server, []model.Inbound{inbound})
	if len(plan.EntryTargets) != 3 {
		t.Fatalf("probe targets = %#v", plan.EntryTargets)
	}
	ports := []int{plan.EntryTargets[0].Port, plan.EntryTargets[1].Port, plan.EntryTargets[2].Port}
	if !reflect.DeepEqual(ports, []int{8964, 8965, 8966}) {
		t.Fatalf("probe ports = %v", ports)
	}
	if plan.EntryTargets[0].SampleCount != inboundProbeSamples || plan.EntryTargets[1].SampleCount != 1 || plan.EntryTargets[2].SampleCount != 1 {
		t.Fatalf("probe sample counts = %#v", plan.EntryTargets)
	}
	for _, target := range plan.EntryTargets {
		if target.Transport != "udp" {
			t.Fatalf("probe transport = %q", target.Transport)
		}
	}
}

func stringSliceFromControllerJSON(value any) []string {
	list, _ := value.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}
