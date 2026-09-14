package confighealth

import (
	"strconv"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func id(v int64) *int64 { return &v }

// healthyInput is a small topology that must produce no findings. Every test
// starts from it and breaks exactly one thing, so a new check that fires on
// valid configuration fails the clean-baseline test instead of silently adding
// noise to every operator's dashboard.
func healthyInput() Input {
	server := model.Server{ID: 1, Name: "hk-1", PortRangeStart: 10000, PortRangeEnd: 20000}
	inbound := model.Inbound{
		ID: 10, ServerID: 1, Name: "vless-a", Protocol: model.ProtocolVLESS,
		ListenIP: "0.0.0.0", Port: 10001, ConfigJSON: `{}`, Enabled: true,
	}
	path := model.ProxyPath{ID: 100, Name: "chain-a", Kind: model.ProxyPathKindDirect, InboundID: 10, Enabled: true}
	return Input{
		Servers:                []model.Server{server},
		Inbounds:               []model.Inbound{inbound},
		ProxyPaths:             []model.ProxyPath{path},
		FamilySplitTemplateIDs: map[int64]bool{},
		UsableNodePresetIDs:    map[int64]bool{},
		UsableSnellProfileIDs:  map[int64]bool{},
		CertificateIDs:         map[int64]bool{},
		DNSCredentialIDs:       map[int64]bool{},
	}
}

func codes(report Report) []string {
	out := make([]string, 0, len(report.Findings))
	for _, f := range report.Findings {
		out = append(out, f.Code)
	}
	return out
}

func findingByCode(t *testing.T, report Report, code string) Finding {
	t.Helper()
	for _, f := range report.Findings {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("expected finding %q, got %v", code, codes(report))
	return Finding{}
}

func requireNoFinding(t *testing.T, report Report, code string) {
	t.Helper()
	for _, f := range report.Findings {
		if f.Code == code {
			t.Fatalf("unexpected finding %q: %s", code, f.Detail)
		}
	}
}

func TestEvaluateCleanTopologyReportsNothing(t *testing.T) {
	report := Evaluate(healthyInput())
	if !report.Summary.Clean() {
		t.Fatalf("healthy topology produced findings: %v", codes(report))
	}
	if report.Findings == nil {
		t.Fatal("findings must be an empty slice, not null, so the client can render it directly")
	}
}

func TestEvaluateReportsInvalidInboundDocumentWithNormalizeRemedy(t *testing.T) {
	in := healthyInput()
	in.Inbounds[0].ConfigJSON = `{"multiplex":{"enabled":true,"protocol":"smux"}}`
	report := Evaluate(in)
	finding := findingByCode(t, report, "inbound.config.invalid")
	if finding.Severity != SeverityBlocking {
		t.Fatalf("expected blocking, got %s", finding.Severity)
	}
	if finding.Remedy.Kind != RemedyNormalize {
		t.Fatalf("expected a normalize remedy, got %+v", finding.Remedy)
	}
	if len(finding.Remedy.Fields) != 1 || finding.Remedy.Fields[0] != "multiplex.protocol" {
		t.Fatalf("remedy preview did not name the removable field: %v", finding.Remedy.Fields)
	}
	if report.BlockingByServer[1] != 1 {
		t.Fatalf("blocking finding was not attributed to its server: %v", report.BlockingByServer)
	}
}

func TestEvaluateFallsBackToDisableWhenNormalizationCannotResolve(t *testing.T) {
	in := healthyInput()
	// A Reality block missing its server name cannot be fixed by removal.
	in.Inbounds[0].ConfigJSON = `{"tls":{"enabled":true,"reality":{"enabled":true}}}`
	finding := findingByCode(t, Evaluate(in), "inbound.config.invalid")
	if finding.Remedy.Kind != RemedyDisable {
		t.Fatalf("expected a disable remedy, got %+v", finding.Remedy)
	}
}

func TestEvaluateReportsUnusableNodePresetReference(t *testing.T) {
	in := healthyInput()
	in.Inbounds[0].ConfigJSON = `{"node_preset_id":7}`
	finding := findingByCode(t, Evaluate(in), "inbound.node_preset.unusable")
	if finding.Severity != SeverityWarning {
		t.Fatalf("expected warning, got %s", finding.Severity)
	}
	if finding.Remedy.Kind != RemedyNormalize || finding.Remedy.Fields[0] != "node_preset_id" {
		t.Fatalf("unexpected remedy %+v", finding.Remedy)
	}

	// A preset that is present and enabled is not a finding.
	in.UsableNodePresetIDs = map[int64]bool{7: true}
	requireNoFinding(t, Evaluate(in), "inbound.node_preset.unusable")
}

func TestEvaluateReportsPortConflictOnOverlappingListeners(t *testing.T) {
	in := healthyInput()
	in.Inbounds = append(in.Inbounds, model.Inbound{
		ID: 11, ServerID: 1, Name: "vless-b", Protocol: model.ProtocolVLESS,
		ListenIP: "0.0.0.0", Port: 10001, ConfigJSON: `{}`, Enabled: true,
	})
	finding := findingByCode(t, Evaluate(in), "inbound.port.conflict")
	if finding.ResourceID != 11 {
		t.Fatalf("conflict should be attributed to the later inbound, got %d", finding.ResourceID)
	}

	// A TCP listener and a QUIC listener do not contend for the same socket.
	in.Inbounds[1].Protocol = model.ProtocolHY2
	in.Inbounds[1].ConfigJSON = `{"tls":{"enabled":true}}`
	requireNoFinding(t, Evaluate(in), "inbound.port.conflict")
}

func TestEvaluateIgnoresPortReuseOnDistinctListenAddresses(t *testing.T) {
	in := healthyInput()
	in.Inbounds[0].ListenIP = "10.0.0.1"
	in.Inbounds = append(in.Inbounds, model.Inbound{
		ID: 11, ServerID: 1, Name: "vless-b", Protocol: model.ProtocolVLESS,
		ListenIP: "10.0.0.2", Port: 10001, ConfigJSON: `{}`, Enabled: true,
	})
	requireNoFinding(t, Evaluate(in), "inbound.port.conflict")
}

func TestEvaluateAcceptsCustomInboundPortsOutsideManagedPool(t *testing.T) {
	for _, port := range []int{443, 3002, 30205, 40000, 49999, 50000, 65535} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			in := healthyInput()
			in.Servers[0].PortRangeStart = 40000
			in.Servers[0].PortRangeEnd = 49999
			in.Inbounds[0].Port = port
			report := Evaluate(in)
			if !report.Summary.Clean() {
				t.Fatalf("valid custom port %d produced findings: %v", port, codes(report))
			}
		})
	}
}

func TestEvaluateReportsCustomPortConflictOutsideManagedPool(t *testing.T) {
	in := healthyInput()
	in.Inbounds[0].Port = 443
	duplicate := in.Inbounds[0]
	duplicate.ID = 11
	in.Inbounds = append(in.Inbounds, duplicate)
	finding := findingByCode(t, Evaluate(in), "inbound.port.conflict")
	if finding.Severity != SeverityBlocking {
		t.Fatalf("expected blocking port conflict, got %+v", finding)
	}
}

func TestEvaluateReportsInboundPointingAtDeletedServer(t *testing.T) {
	in := healthyInput()
	in.Servers = nil
	finding := findingByCode(t, Evaluate(in), "inbound.server.missing")
	if finding.Severity != SeverityBlocking || finding.Remedy.Kind != RemedyDelete || !finding.Remedy.Destructive {
		t.Fatalf("an orphan inbound should offer a destructive delete, got %+v", finding)
	}
}

func TestEvaluateReportsProxyPathPointingAtDeletedInbound(t *testing.T) {
	in := healthyInput()
	in.ProxyPaths[0].InboundID = 999
	finding := findingByCode(t, Evaluate(in), "proxy_path.inbound.missing")
	if finding.Remedy.Kind != RemedyDelete || !finding.Remedy.Destructive {
		t.Fatalf("an orphan path should offer a destructive delete, got %+v", finding.Remedy)
	}
}

func TestEvaluateReportsEnabledPathOverDisabledInbound(t *testing.T) {
	in := healthyInput()
	in.Inbounds[0].Enabled = false
	finding := findingByCode(t, Evaluate(in), "proxy_path.inbound.disabled")
	if finding.Severity != SeverityWarning {
		t.Fatalf("expected warning, got %s", finding.Severity)
	}
}

func TestEvaluateReportsStepTargetsThatNoLongerExist(t *testing.T) {
	in := healthyInput()
	in.ProxyPaths[0].Kind = model.ProxyPathKindChain
	in.ProxyPathSteps = []model.ProxyPathStep{{
		ID: 500, PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound,
		ServerID: id(404), InboundID: id(405),
	}}
	report := Evaluate(in)
	findingByCode(t, report, "proxy_path.step.server_missing")
	findingByCode(t, report, "proxy_path.step.inbound_missing")
}

func TestEvaluateReportsOrphanStep(t *testing.T) {
	in := healthyInput()
	in.ProxyPathSteps = []model.ProxyPathStep{{ID: 500, PathID: 777, Position: 1}}
	findingByCode(t, Evaluate(in), "proxy_path.step.orphan")
}

func TestEvaluateReportsRoutingRuleWithDanglingTarget(t *testing.T) {
	in := healthyInput()
	in.RoutingRules = []model.RoutingRule{{
		ID: 900, ServerID: 1, Name: "to-jp", Action: model.RouteActionProxyPath,
		TargetProxyPathID: id(888), Enabled: true,
	}}
	finding := findingByCode(t, Evaluate(in), "routing_rule.target_path.missing")
	if finding.Severity != SeverityBlocking || finding.Remedy.Kind != RemedyDelete {
		t.Fatalf("unexpected finding %+v", finding)
	}
}

func TestEvaluateReportsRoutingRuleTargetingDisabledPath(t *testing.T) {
	in := healthyInput()
	in.ProxyPaths[0].Enabled = false
	in.Inbounds[0].Enabled = true
	in.RoutingRules = []model.RoutingRule{{
		ID: 900, ServerID: 1, Name: "to-hk", Action: model.RouteActionProxyPath,
		TargetProxyPathID: id(100), Enabled: true,
	}}
	finding := findingByCode(t, Evaluate(in), "routing_rule.target_path.disabled")
	if finding.Remedy.Kind != RemedyDisable {
		t.Fatalf("a disabled target should offer the reversible remedy, got %+v", finding.Remedy)
	}
}

func TestEvaluateReportsUndecodableMatchDocument(t *testing.T) {
	in := healthyInput()
	in.RoutingRules = []model.RoutingRule{{ID: 901, ServerID: 1, Name: "bad", MatchJSON: "{oops", Enabled: true}}
	findingByCode(t, Evaluate(in), "routing_rule.match.invalid")
}

func TestEvaluateReportsDanglingDNSPolicyList(t *testing.T) {
	in := healthyInput()
	in.ServerDNSPolicies = []model.ServerDNSPolicy{{ServerID: 1, BootstrapListID: 5, EncryptedListID: 0}}
	findingByCode(t, Evaluate(in), "dns_policy.bootstrap_list.missing")

	// encrypted_list_id = 0 is the valid plain-DNS-only policy.
	requireNoFinding(t, Evaluate(in), "dns_policy.encrypted_list.missing")
}

func TestEvaluateOrdersBlockingFindingsFirst(t *testing.T) {
	in := healthyInput()
	in.Inbounds[0].ConfigJSON = `{"node_preset_id":7}`                                // warning
	in.RoutingRules = []model.RoutingRule{{ID: 901, ServerID: 1, MatchJSON: "{oops"}} // blocking
	report := Evaluate(in)
	if len(report.Findings) != 2 {
		t.Fatalf("expected two findings, got %v", codes(report))
	}
	if report.Findings[0].Severity != SeverityBlocking {
		t.Fatalf("blocking finding was not ranked first: %v", codes(report))
	}
	if report.Summary.Blocking != 1 || report.Summary.Warning != 1 || report.Summary.Notice != 0 {
		t.Fatalf("unexpected summary %+v", report.Summary)
	}
}

func TestEvaluateTruncatesAtTheFindingLimit(t *testing.T) {
	in := healthyInput()
	for i := int64(0); i < findingLimit+50; i++ {
		in.RoutingRules = append(in.RoutingRules, model.RoutingRule{ID: 1000 + i, ServerID: 1, MatchJSON: "{oops"})
	}
	report := Evaluate(in)
	if len(report.Findings) != findingLimit {
		t.Fatalf("expected the report to stop at %d findings, got %d", findingLimit, len(report.Findings))
	}
	if !report.Summary.Truncated {
		t.Fatal("a truncated report must say so")
	}
}
