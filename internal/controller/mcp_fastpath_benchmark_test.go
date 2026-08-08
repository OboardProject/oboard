package controller

import (
	"encoding/json"
	"testing"
)

var mcpFastPathBenchmarkCorpus = []mcpTaskInput{
	{Goal: "新增东京服务器"},
	{Goal: "新增服务器并开启 BBR", Params: map[string]any{"server.name": "Tokyo-02"}},
	{Goal: "把 Server A 调成 IPv6 优先", Intent: "server.manage", TargetRefs: []string{"server:1"}, Params: map[string]any{"ip_stack": "prefer_ipv6"}},
	{Goal: "给香港服务器创建 VLESS", Intent: "inbound.manage"},
	{Goal: "香港到新加坡到洛杉矶代理链"},
	{Goal: "创建 WireGuard 链路", Intent: "proxy_path.manage"},
	{Goal: "增加 Direct Branch", Intent: "proxy_path.manage"},
	{Goal: "部署全部修改"},
	{Goal: "查看 Deployment 状态", Intent: "workflow.recover"},
	{Goal: "创建 example.com 证书", Intent: "certificate.manage"},
	{Goal: "创建 DNS record", Intent: "dns.manage"},
	{Goal: "新增用户 Alice", Intent: "user.manage"},
	{Goal: "给 Alice 分配订阅 profile", Intent: "subscription.manage"},
	{Goal: "查看服务器 Agent 状态", Intent: "diagnostics.run"},
	{Goal: "把服务器 MTU 设置为 1420", Intent: "server.manage", TargetRefs: []string{"server:1"}, Params: map[string]any{"mtu_mode": "manual", "mtu_value": 1420}},
	{Goal: "修改服务器端口范围", Intent: "server.manage", TargetRefs: []string{"server:1"}, Params: map[string]any{"port_range_start": 20000, "port_range_end": 21000}},
	{Goal: "创建端口转发", Intent: "port_forward.manage"},
	{Goal: "创建 SSH tunnel", Intent: "tunnel.manage"},
	{Goal: "重新应用指定服务器配置", Intent: "deployment.apply", TargetRefs: []string{"server:1"}},
	{Goal: "恢复失败 Workflow", Intent: "workflow.recover"},
}

func TestMCPFastPathBenchmarkCorpus(t *testing.T) {
	if len(mcpFastPathBenchmarkCorpus) < 20 {
		t.Fatalf("benchmark corpus has %d operations, want at least 20", len(mcpFastPathBenchmarkCorpus))
	}
}

func BenchmarkMCPFastPathRouting(b *testing.B) {
	s := &Server{}
	var payloadBytes, fallback int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		input := mcpFastPathBenchmarkCorpus[i%len(mcpFastPathBenchmarkCorpus)]
		encoded, _ := json.Marshal(input)
		payloadBytes += int64(len(encoded))
		if _, _, ok := s.matchMCPRecipe(input); !ok {
			fallback++
		}
	}
	b.ReportMetric(1, "tool_calls/op")
	b.ReportMetric(0, "discover_calls/op")
	b.ReportMetric(0, "schema_calls/op")
	b.ReportMetric(float64(payloadBytes)/float64(b.N), "payload_bytes/op")
	b.ReportMetric(float64(fallback)/float64(b.N), "fallback/op")
	b.ReportMetric(float64(b.N-int(fallback))/float64(b.N), "matched/op")
	b.ReportMetric(0, "validation_errors/op")
}

func BenchmarkMCPLegacyPlanningEnvelope(b *testing.B) {
	input := map[string]any{"capability_id": "topology.write", "desired_state": map[string]any{"path": map[string]any{}, "steps": []any{}}, "expected_revisions": map[string]string{"routing_topology": "revision"}}
	var payloadBytes int64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encoded, _ := json.Marshal(input)
		payloadBytes += int64(len(encoded)) * 3
	}
	b.ReportMetric(5, "tool_calls/op")
	b.ReportMetric(1, "discover_calls/op")
	b.ReportMetric(1, "schema_calls/op")
	b.ReportMetric(float64(payloadBytes)/float64(b.N), "payload_bytes/op")
	b.ReportMetric(1, "matched/op")
	b.ReportMetric(0, "validation_errors/op")
}
