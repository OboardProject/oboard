package model

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// network_diagnostics_contract.json is byte-identical in the Controller and
// Agent repositories; a model change on either side must update both.
func networkDiagnosticContractSamples() map[string]any {
	issued := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	envelope := NetworkDiagnosticEnvelope{ProtocolVersion: NetworkDiagnosticProtocolVersion, OperationID: "op-1", Origin: NetworkDiagnosticOriginPlugin, RunID: "run-1", PluginID: "acme.monitor", InstanceID: 7, IssuedAt: issued, ExpiresAt: issued.Add(time.Minute)}
	return map[string]any{
		AgentTaskTypeNetworkPing:  NetworkPingTaskPayload{NetworkDiagnosticEnvelope: envelope, Target: "example.com", IPFamily: NetworkFamilyAuto, Count: 4, IntervalMS: 1000, TimeoutMS: 1000, PacketSize: 56},
		AgentTaskTypeNetworkTrace: NetworkTraceTaskPayload{NetworkDiagnosticEnvelope: envelope, Target: "example.com", IPFamily: NetworkFamilyIPv4, Mode: TraceModeTCP, Port: 443, MaxHops: 20, QueriesPerHop: 3, PerHopTimeoutMS: 1000},
		AgentTaskTypeNetworkTCP:   NetworkTCPProbeTaskPayload{NetworkDiagnosticEnvelope: envelope, Host: "example.com", Port: 443, IPFamily: NetworkFamilyAuto, TimeoutMS: 3000},
		AgentTaskTypeNetworkDNS:   NetworkDNSLookupTaskPayload{NetworkDiagnosticEnvelope: envelope, Name: "example.com", RecordTypes: []string{"A", "AAAA"}, TimeoutMS: 3000},
		AgentTaskTypeNetworkHTTP:  NetworkHTTPProbeTaskPayload{NetworkDiagnosticEnvelope: envelope, URL: "https://example.com/health", Method: "GET", IPFamily: NetworkFamilyAuto, TimeoutMS: 5000, FollowRedirects: true},
		"result.ping":             NetworkPingResult{OperationID: "op-1", Target: "example.com", ResolvedIP: "93.184.215.14", IPFamily: NetworkFamilyIPv4, Sent: 2, Received: 1, LossPercent: 50, MinRTTMS: 11.5, AvgRTTMS: 11.5, MaxRTTMS: 11.5, Samples: []NetworkPingSample{{Seq: 1, RTTMS: 11.5}, {Seq: 2, Timeout: true}}},
		"result.trace":            NetworkTraceResult{OperationID: "op-1", Target: "example.com", ResolvedIP: "93.184.215.14", IPFamily: NetworkFamilyIPv4, Mode: TraceModeTCP, Port: 443, StartedAt: issued, DurationMS: 812, Reached: true, Hops: []NetworkTraceHop{{Hop: 1, Addresses: []string{"203.0.113.1"}, Probes: []NetworkTraceProbe{{Address: "203.0.113.1", RTTMS: 1.2}, {Timeout: true}}}}},
		"result.tcp":              NetworkTCPProbeResult{OperationID: "op-1", Host: "example.com", Port: 443, ResolvedIP: "93.184.215.14", IPFamily: NetworkFamilyIPv4, Connected: true, ConnectMS: 12.25},
		"result.dns":              NetworkDNSLookupResult{OperationID: "op-1", Name: "example.com", Records: []NetworkDNSRecord{{Type: "A", Address: "93.184.215.14"}}, DurationMS: 4.5},
		"result.http":             NetworkHTTPProbeResult{OperationID: "op-1", URL: "https://example.com/health", FinalURL: "https://example.com/health", ResolvedIP: "93.184.215.14", StatusCode: 200, BodyBytes: 2, Timings: NetworkHTTPTimings{DNSMS: 1, ConnectMS: 10, TLSMS: 20, TTFBMS: 40, TotalMS: 41}, TLS: &NetworkHTTPTLS{Version: "TLS 1.3", ServerName: "example.com", CertExpires: issued.AddDate(0, 3, 0)}},
	}
}

func TestNetworkDiagnosticWireContract(t *testing.T) {
	encoded, err := json.MarshalIndent(networkDiagnosticContractSamples(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("OBOARD_UPDATE_CONTRACT") == "1" {
		if err := os.WriteFile("testdata/network_diagnostics_contract.json", encoded, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.ReadFile("testdata/network_diagnostics_contract.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, fixture) {
		t.Fatalf("network diagnostic wire contract drifted:\n%s", encoded)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &decoded); err != nil {
		t.Fatal(err)
	}
	for key, sample := range networkDiagnosticContractSamples() {
		target := newContractValue(sample)
		dec := json.NewDecoder(bytes.NewReader(decoded[key]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(target); err != nil {
			t.Fatalf("%s: strict decode: %v", key, err)
		}
	}
}

func newContractValue(sample any) any {
	switch sample.(type) {
	case NetworkPingTaskPayload:
		return &NetworkPingTaskPayload{}
	case NetworkTraceTaskPayload:
		return &NetworkTraceTaskPayload{}
	case NetworkTCPProbeTaskPayload:
		return &NetworkTCPProbeTaskPayload{}
	case NetworkDNSLookupTaskPayload:
		return &NetworkDNSLookupTaskPayload{}
	case NetworkHTTPProbeTaskPayload:
		return &NetworkHTTPProbeTaskPayload{}
	case NetworkPingResult:
		return &NetworkPingResult{}
	case NetworkTraceResult:
		return &NetworkTraceResult{}
	case NetworkTCPProbeResult:
		return &NetworkTCPProbeResult{}
	case NetworkDNSLookupResult:
		return &NetworkDNSLookupResult{}
	default:
		return &NetworkHTTPProbeResult{}
	}
}
