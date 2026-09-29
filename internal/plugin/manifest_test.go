package plugin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const traceManifest = `{
  "id": "acme.trace-monitor",
  "name": "Trace 监控",
  "version": "1.0.0",
  "description": "定期从指定服务器追踪路由",
  "runtime": "oboard-js",
  "entry": "main.js",
  "capabilities": ["network.trace", "servers.read", "state.read", "state.write"],
  "resources": {"servers": {"min": 1, "reason": "执行 Trace 的服务器"}},
  "environment": [
    {"name": "SERVER", "type": "server", "label": "执行服务器", "required": true, "filter": ["online"]},
    {"name": "TARGET", "type": "string", "label": "Trace 目标", "required": true},
    {"name": "MODE", "type": "select", "label": "模式", "default": "icmp", "options": [{"label": "ICMP", "value": "icmp"}, {"label": "TCP", "value": "tcp"}]},
    {"name": "PORT", "type": "integer", "label": "端口", "default": 443, "min": 1, "max": 65535, "depends_on": {"field": "MODE", "equals": "tcp"}},
    {"name": "INTERVAL", "type": "duration", "label": "间隔", "default": "5m", "min_duration": "1m"}
  ],
  "triggers": {"schedule": true},
  "limits": {"timeout": "45s", "agent_operations": 3}
}`

func mustManifest(t *testing.T, raw string) Manifest {
	t.Helper()
	manifest, err := ParseManifest([]byte(raw))
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return manifest
}

func withField(t *testing.T, raw, key string, value any) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		delete(doc, key)
	} else {
		doc[key] = value
	}
	out, _ := json.Marshal(doc)
	return string(out)
}

func TestManifestAcceptsCapabilityDeclaration(t *testing.T) {
	manifest := mustManifest(t, traceManifest)
	if manifest.Runtime != RuntimeJS || !manifest.HasCapability(CapNetworkTrace) || !manifest.UsesServerResources() {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	if field, ok := manifest.EnvField("PORT"); !ok || string(field.Default) != "443" || field.DependsOn == nil || string(field.DependsOn.Equals) != `"tcp"` {
		t.Fatalf("PORT field not normalized: %+v", field)
	}
}

func TestManifestRejectsRetiredRuntimeAndUnknownFields(t *testing.T) {
	retired := `{"plugin_id":"acme.old","name":"old","version":"1.0.0","description":"","schema_version":1,"runtime":"oboard-js-v1","sdk_version":"oboard-sdk-v1","entry":"main","capabilities":["services.restart"]}`
	if _, err := ParseManifest([]byte(retired)); CodeOf(err) != CodeInvalidManifest || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("retired manifest must be incompatible: %v", err)
	}
	if _, err := ParseManifest([]byte(withField(t, traceManifest, "commands", []string{"ping"}))); CodeOf(err) != CodeInvalidManifest {
		t.Fatalf("unknown manifest field accepted: %v", err)
	}
	if _, err := ParseManifest([]byte(withField(t, traceManifest, "runtime", "node"))); CodeOf(err) != CodeInvalidManifest {
		t.Fatalf("node runtime accepted: %v", err)
	}
}

func TestManifestNeverAcceptsCommandCapabilities(t *testing.T) {
	for _, name := range []string{"shell.exec", "exec.run", "command", "process.spawn", "terminal.open", "pty.open", "filesystem.host.read", "database.raw.query", "sql.query", "agent.raw", "agent.task.create", "controller.raw", "unix_socket.connect", "remote_exec", "remote_operation", "host.poweroff", "management.apply", "tasks.create"} {
		if _, err := ParseManifest([]byte(withField(t, traceManifest, "capabilities", []string{name}))); CodeOf(err) != CodeInvalidManifest {
			t.Errorf("capability %q accepted", name)
		}
	}
}

func TestManifestHTTPScopeRules(t *testing.T) {
	base := withField(t, traceManifest, "capabilities", []string{"http.request"})
	base = withField(t, base, "resources", nil)
	if _, err := ParseManifest([]byte(base)); CodeOf(err) != CodeInvalidManifest {
		t.Fatal("http.request without hosts accepted")
	}
	for _, host := range []string{"localhost", "127.0.0.1", "10.0.0.1", "*.com", "api.*.example.com", "metadata.google.internal", "printer.local", "http://api.example.com", "example"} {
		raw := withField(t, base, "http", map[string]any{"hosts": []string{host}, "methods": []string{"GET"}})
		if _, err := ParseManifest([]byte(raw)); CodeOf(err) != CodeInvalidManifest {
			t.Errorf("host %q accepted", host)
		}
	}
	raw := withField(t, base, "http", map[string]any{"hosts": []string{"*.AliyunCS.com", "api.example.com:8443"}, "methods": []string{"post", "GET"}})
	manifest := mustManifest(t, raw)
	if strings.Join(manifest.HTTP.Hosts, ",") != "*.aliyuncs.com,api.example.com:8443" || strings.Join(manifest.HTTP.Methods, ",") != "GET,POST" {
		t.Fatalf("http scope not normalized: %+v", manifest.HTTP)
	}
}

func TestManifestEnvironmentSchemaRules(t *testing.T) {
	cases := map[string]any{
		"reserved":          []map[string]any{{"name": "OBOARD_TOKEN", "type": "string", "label": "x"}},
		"lowercase":         []map[string]any{{"name": "server", "type": "server", "label": "x"}},
		"duplicate":         []map[string]any{{"name": "A", "type": "string", "label": "x"}, {"name": "A", "type": "string", "label": "y"}},
		"unknown type":      []map[string]any{{"name": "A", "type": "command", "label": "x"}},
		"secret no use":     []map[string]any{{"name": "KEY", "type": "secret", "label": "x"}},
		"server default":    []map[string]any{{"name": "S", "type": "server", "label": "x", "default": "1"}},
		"select options":    []map[string]any{{"name": "S", "type": "select", "label": "x"}},
		"forward depends":   []map[string]any{{"name": "A", "type": "string", "label": "x", "depends_on": map[string]any{"field": "B", "equals": true}}, {"name": "B", "type": "boolean", "label": "y"}},
		"bad default":       []map[string]any{{"name": "N", "type": "integer", "label": "x", "default": "12"}},
		"pattern on int":    []map[string]any{{"name": "N", "type": "integer", "label": "x", "pattern": "a"}},
		"invalid pattern":   []map[string]any{{"name": "A", "type": "string", "label": "x", "pattern": "("}},
		"bad server filter": []map[string]any{{"name": "S", "type": "server", "label": "x", "filter": []string{"cheap"}}},
	}
	for name, env := range cases {
		if _, err := ParseManifest([]byte(withField(t, traceManifest, "environment", env))); CodeOf(err) != CodeInvalidManifest {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestManifestEventsRequireCapability(t *testing.T) {
	raw := withField(t, traceManifest, "triggers", map[string]any{"schedule": false, "events": []string{"server.offline"}})
	if _, err := ParseManifest([]byte(raw)); CodeOf(err) != CodeInvalidManifest {
		t.Fatal("event without events.server_status accepted")
	}
	raw = withField(t, raw, "capabilities", []string{"network.trace", "events.server_status"})
	if manifest := mustManifest(t, raw); len(manifest.Triggers.Events) != 1 {
		t.Fatal("event declaration lost")
	}
	if _, err := ParseManifest([]byte(withField(t, raw, "triggers", map[string]any{"events": []string{"*"}}))); CodeOf(err) != CodeInvalidManifest {
		t.Fatal("wildcard event accepted")
	}
}

func TestManifestLimitsOnlyLowerCeilings(t *testing.T) {
	for _, limits := range []map[string]any{{"timeout": "10m"}, {"memory_mib": 1024}, {"sdk_calls": 100000}, {"http_requests": 1000}, {"agent_operations": 500}} {
		if _, err := ParseManifest([]byte(withField(t, traceManifest, "limits", limits))); CodeOf(err) != CodeInvalidManifest {
			t.Errorf("limits %v accepted", limits)
		}
	}
	limits, err := EffectiveLimits(DeclaredLimits{Timeout: "90s"}, 30*time.Second)
	if err != nil || limits.TimeoutMS != 30000 {
		t.Fatalf("system ceiling not applied: %+v %v", limits, err)
	}
}

func TestCatalogHasNoForbiddenCapability(t *testing.T) {
	for _, spec := range CapabilityCatalog() {
		if forbiddenCapability(spec.Name) {
			t.Errorf("forbidden capability %s in catalog", spec.Name)
		}
		for _, word := range []string{"exec", "shell", "command", "spawn", "process", "terminal", "pty", "sql", "raw"} {
			if strings.Contains(spec.Name, word) {
				t.Errorf("capability %s looks like a command surface", spec.Name)
			}
			for _, method := range spec.Methods {
				if strings.Contains(strings.ToLower(method), word) {
					t.Errorf("SDK method %s looks like a command surface", method)
				}
			}
		}
	}
	for _, name := range []string{CapServersRead, CapServersHealthRead, CapServersMetricsRead, CapNetworkPing, CapNetworkTrace, CapNetworkTCPProbe, CapNetworkDNSLookup, CapNetworkHTTPProbe, CapHTTPRequest, CapStateRead, CapStateWrite, CapSecretsUse, CapNotificationsSend, CapEventsServerStatus} {
		if _, ok := LookupCapability(name); !ok {
			t.Errorf("required capability %s missing", name)
		}
	}
}
