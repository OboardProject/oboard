package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/pluginruntime"
)

// fakeGateway answers every SDK method with a plausible, schema-shaped value
// so the examples run end to end in the real runtime.
func fakeGateway(t *testing.T, methods map[string]int) pluginruntime.CallFunc {
	return func(method string, arguments json.RawMessage) pluginrpc.CallResponse {
		methods[method]++
		results := map[string]string{
			"servers.get":          `{"server_id":"1","name":"hk-1","status":"online","online":true,"enrolled":true}`,
			"servers.health":       `{"server_id":"1","status":"online","agent_connected":true,"config_sync":"synced","stale":false}`,
			"network.ping":         `{"operation_id":"op","target":"example.com","sent":5,"received":5,"loss_percent":0,"avg_rtt_ms":12.5,"samples":[]}`,
			"network.trace":        `{"operation_id":"op","target":"example.com","reached":true,"hops":[{"hop":1,"addresses":["203.0.113.1"],"probes":[]}]}`,
			"state.get":            `{"key":"k","exists":false,"version":0}`,
			"state.set":            `{"key":"k","version":1}`,
			"state.compareAndSwap": `{"key":"k","version":1}`,
			"http.request":         `{"status":202,"headers":{},"redirects":0,"final_url":"https://api.statuspage.example.com/v1/reports","body":""}`,
			"crypto.hmac":          `{"digest":"c2lnbmF0dXJl"}`,
			"notifications.send":   `{"sent":["1"]}`,
		}
		result, ok := results[method]
		if !ok {
			t.Errorf("unexpected SDK method %s", method)
			return pluginrpc.CallResponse{Code: "INTERNAL_ERROR"}
		}
		return pluginrpc.CallResponse{OK: true, Result: json.RawMessage(result)}
	}
}

func TestExamplesRunInTheRuntime(t *testing.T) {
	secret := func(name string) pluginrpc.EnvValue {
		ref := "secret://instance/1/" + name
		return pluginrpc.EnvValue{Type: "secret", Value: json.RawMessage(`{"$secret":"` + ref + `"}`), Raw: ref}
	}
	value := func(kind, raw string) pluginrpc.EnvValue {
		return pluginrpc.EnvValue{Type: kind, Value: json.RawMessage(raw), Raw: strings.Trim(raw, `"`)}
	}
	cases := map[string]map[string]pluginrpc.EnvValue{
		"minimal": {"TARGET_SERVER": value("server", `"1"`)},
		"environment-demo": {
			"LABEL": value("string", `"巡检"`), "RETRIES": value("integer", `1`), "THRESHOLD": value("number", `80.5`), "VERBOSE": value("boolean", `true`),
			"MODE": value("select", `"advanced"`), "ADVANCED_WINDOW": value("duration", `"5m"`), "CHANNELS": value("multi_select", `["latency"]`),
			"PRIMARY": value("server", `"1"`), "PEERS": value("servers", `["2"]`), "EXTRA": value("json", `{"tags":[]}`), "API_TOKEN": secret("API_TOKEN"),
			"REGION": {Type: "string", Value: json.RawMessage(`"hk"`), Raw: "hk", Custom: true},
		},
		"external-api": {
			"SERVERS": value("servers", `["1"]`), "ENDPOINT": value("url", `"https://api.statuspage.example.com/v1/reports"`),
			"API_TOKEN": secret("API_TOKEN"), "SIGNING_KEY": secret("SIGNING_KEY"),
		},
		"trace-monitor": {
			"SOURCES": value("servers", `["1"]`), "TARGET": value("string", `"example.com"`), "MODE": value("select", `"tcp"`),
			"PORT": value("integer", `443`), "LOSS_ALERT": value("number", `20`),
		},
	}
	for name, environment := range cases {
		source, err := os.ReadFile(filepath.Join("../../examples/plugins", name, "main.js"))
		if err != nil {
			t.Fatal(err)
		}
		methods := map[string]int{}
		outcome := pluginruntime.Execute(string(source), environment, pluginrpc.RunContext{RunID: "run-1", PluginID: "example." + name, InstanceID: "1", Trigger: "manual"},
			pluginrpc.Limits{TimeoutMS: 5000, ResultBytes: 64 << 10}, fakeGateway(t, methods), func(string, string) {})
		if outcome.Code != "" {
			t.Fatalf("%s: %s %s", name, outcome.Code, outcome.Message)
		}
		if len(outcome.Result) == 0 || string(outcome.Result) == "null" {
			t.Fatalf("%s: empty result", name)
		}
		if strings.Contains(string(outcome.Result), "secret://") {
			t.Fatalf("%s: result leaked a secret reference: %s", name, outcome.Result)
		}
	}
}
