package plugin

import (
	"encoding/json"
	"strings"
	"testing"
)

const envManifest = `{
  "id": "acme.environment-demo",
  "name": "Environment demo",
  "version": "1.0.0",
  "description": "",
  "runtime": "oboard-js",
  "entry": "main.js",
  "capabilities": ["servers.read", "secrets.use", "network.ping"],
  "environment": [
    {"name": "TITLE", "type": "string", "label": "标题", "required": true, "max_length": 16},
    {"name": "NOTE", "type": "text", "label": "说明"},
    {"name": "COUNT", "type": "integer", "label": "次数", "default": 3, "min": 1, "max": 10},
    {"name": "RATIO", "type": "number", "label": "比例", "default": 0.5},
    {"name": "VERBOSE", "type": "boolean", "label": "详细", "default": false},
    {"name": "LEVEL", "type": "select", "label": "级别", "default": "low", "options": [{"label": "低", "value": "low"}, {"label": "高", "value": "high"}]},
    {"name": "TAGS", "type": "multi_select", "label": "标签", "options": [{"label": "A", "value": "a"}, {"label": "B", "value": "b"}]},
    {"name": "SERVER", "type": "server", "label": "服务器", "required": true},
    {"name": "SERVERS", "type": "servers", "label": "服务器组", "max_items": 3},
    {"name": "API_KEY", "type": "secret", "label": "密钥", "required": true},
    {"name": "ENDPOINT", "type": "url", "label": "地址"},
    {"name": "EVERY", "type": "duration", "label": "间隔", "default": "5m"},
    {"name": "EXTRA", "type": "json", "label": "附加"},
    {"name": "DETAIL", "type": "string", "label": "详细说明", "depends_on": {"field": "VERBOSE", "equals": true}}
  ],
  "triggers": {"schedule": false}
}`

func raw(value string) json.RawMessage { return json.RawMessage(value) }

func TestEnvironmentSaveNormalizesTypedValues(t *testing.T) {
	manifest := mustManifest(t, envManifest)
	values, custom, issues := NormalizeEnvironment(manifest, EnvironmentInput{
		Values: map[string]json.RawMessage{
			"TITLE": raw(`"hello"`), "COUNT": raw(`7`), "RATIO": raw(`0.25`), "VERBOSE": raw(`true`), "LEVEL": raw(`"high"`),
			"TAGS": raw(`["b","a"]`), "SERVER": raw(`"12"`), "SERVERS": raw(`["9", 3]`), "ENDPOINT": raw(`"https://api.example.com/v1"`),
			"EVERY": raw(`"90s"`), "EXTRA": raw(`{ "a" : [1, 2] }`), "NOTE": raw(`""`),
		},
		Custom: []CustomVar{{Name: "REGION", Type: EnvString, Value: raw(`"Tokyo"`)}, {Name: "SECOND_SERVER", Type: EnvServer, Value: raw(`"4"`)}, {Name: "TOKEN", Type: EnvSecret}},
	})
	if len(issues) > 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	want := map[string]string{"COUNT": `7`, "VERBOSE": `true`, "TAGS": `["a","b"]`, "SERVER": `"12"`, "SERVERS": `["3","9"]`, "EVERY": `"1m30s"`, "EXTRA": `{"a":[1,2]}`}
	for name, expected := range want {
		if string(values[name]) != expected {
			t.Errorf("%s = %s, want %s", name, values[name], expected)
		}
	}
	if _, ok := values["NOTE"]; ok {
		t.Fatal("empty value must clear a field")
	}
	if len(custom) != 3 || custom[2].Value != nil {
		t.Fatalf("custom variables not normalized: %+v", custom)
	}
}

func TestEnvironmentSaveRejectsUnsafeValues(t *testing.T) {
	manifest := mustManifest(t, envManifest)
	cases := map[string]EnvironmentInput{
		"server name":      {Values: map[string]json.RawMessage{"SERVER": raw(`"Tokyo-01"`)}},
		"server zero":      {Values: map[string]json.RawMessage{"SERVER": raw(`0`)}},
		"too many servers": {Values: map[string]json.RawMessage{"SERVERS": raw(`["1","2","3","4"]`)}},
		"bool as string":   {Values: map[string]json.RawMessage{"VERBOSE": raw(`"true"`)}},
		"integer float":    {Values: map[string]json.RawMessage{"COUNT": raw(`2.5`)}},
		"integer range":    {Values: map[string]json.RawMessage{"COUNT": raw(`11`)}},
		"number string":    {Values: map[string]json.RawMessage{"RATIO": raw(`"1"`)}},
		"undeclared":       {Values: map[string]json.RawMessage{"ALLOW_SHELL": raw(`true`)}},
		"secret in values": {Values: map[string]json.RawMessage{"API_KEY": raw(`"plain"`)}},
		"option":           {Values: map[string]json.RawMessage{"LEVEL": raw(`"root"`)}},
		"url scheme":       {Values: map[string]json.RawMessage{"ENDPOINT": raw(`"file:///etc/passwd"`)}},
		"url credentials":  {Values: map[string]json.RawMessage{"ENDPOINT": raw(`"https://user:pw@example.com"`)}},
		"duration":         {Values: map[string]json.RawMessage{"EVERY": raw(`"-5m"`)}},
		"too long":         {Values: map[string]json.RawMessage{"TITLE": raw(`"` + strings.Repeat("x", 17) + `"`)}},
		"control char":     {Values: map[string]json.RawMessage{"TITLE": raw(`"a\u0000b"`)}},
		"custom reserved":  {Custom: []CustomVar{{Name: "OBOARD_TOKEN", Type: EnvString, Value: raw(`"x"`)}}},
		"custom overrides": {Custom: []CustomVar{{Name: "SERVER", Type: EnvServer, Value: raw(`"1"`)}}},
		"custom duplicate": {Custom: []CustomVar{{Name: "A", Type: EnvString, Value: raw(`"x"`)}, {Name: "A", Type: EnvString, Value: raw(`"y"`)}}},
		"custom select":    {Custom: []CustomVar{{Name: "A", Type: EnvSelect, Value: raw(`"x"`)}}},
		"custom lowercase": {Custom: []CustomVar{{Name: "path", Type: EnvString, Value: raw(`"x"`)}}},
		"custom secret":    {Custom: []CustomVar{{Name: "KEY", Type: EnvSecret, Value: raw(`"leaked"`)}}},
	}
	for name, input := range cases {
		if _, _, issues := NormalizeEnvironment(manifest, input); len(issues) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestEnvironmentRunValidationReflectsCurrentReality(t *testing.T) {
	manifest := mustManifest(t, envManifest)
	values := map[string]json.RawMessage{"TITLE": raw(`"x"`), "SERVER": raw(`"2"`), "SERVERS": raw(`["3"]`)}
	exists := func(id int64) bool { return id != 404 }
	status, _ := EvaluateEnvironment(EvaluationInput{Manifest: manifest, Values: values, SecretsConfigured: map[string]bool{}, ServerExists: exists, GrantedServers: map[int64]bool{2: true, 3: true}})
	if status != ConfigRequired {
		t.Fatalf("missing secret must require configuration, got %s", status)
	}
	secrets := map[string]bool{"API_KEY": true}
	if status, issues := EvaluateEnvironment(EvaluationInput{Manifest: manifest, Values: values, SecretsConfigured: secrets, ServerExists: exists, GrantedServers: map[int64]bool{2: true, 3: true}}); status != ConfigOK {
		t.Fatalf("complete config rejected: %s %+v", status, issues)
	}
	// A server granted earlier and then narrowed away makes the saved value
	// invalid; it is never silently rewritten to another server.
	status, issues := EvaluateEnvironment(EvaluationInput{Manifest: manifest, Values: values, SecretsConfigured: secrets, ServerExists: exists, GrantedServers: map[int64]bool{3: true}})
	if status != ConfigInvalid || issues[0].Code != CodeResourceDenied || string(values["SERVER"]) != `"2"` {
		t.Fatalf("narrowed grant must invalidate the selection: %s %+v", status, issues)
	}
	values["SERVER"] = raw(`"404"`)
	status, issues = EvaluateEnvironment(EvaluationInput{Manifest: manifest, Values: values, SecretsConfigured: secrets, ServerExists: exists, GrantedServers: map[int64]bool{3: true, 404: true}})
	if status != ConfigInvalid || issues[0].Code != CodeServerNotFound {
		t.Fatalf("deleted server must be reported: %s %+v", status, issues)
	}
}

func TestConditionalFieldsAndRuntimeEnvironment(t *testing.T) {
	manifest := mustManifest(t, envManifest)
	values := map[string]json.RawMessage{"TITLE": raw(`"x"`), "SERVER": raw(`"2"`), "DETAIL": raw(`"hidden"`), "REMOVED": raw(`"old"`)}
	custom := []CustomVar{{Name: "REGION", Type: EnvString, Value: raw(`"Tokyo"`)}, {Name: "TOKEN", Type: EnvSecret}}
	env := ResolveRuntimeEnvironment(7, manifest, values, custom, map[string]bool{"API_KEY": true})
	if _, ok := env["DETAIL"]; ok {
		t.Fatal("inactive conditional field was delivered")
	}
	if _, ok := env["REMOVED"]; ok {
		t.Fatal("undeclared stored value was delivered")
	}
	if _, ok := env["TOKEN"]; ok {
		t.Fatal("unconfigured custom secret was delivered")
	}
	if env["COUNT"].Type != EnvInteger || string(env["COUNT"].Value) != "3" || string(env["VERBOSE"].Value) != "false" || string(env["EVERY"].Value) != "300000" || env["EVERY"].Raw != "5m" {
		t.Fatalf("typed defaults wrong: %+v", env)
	}
	if env["SERVER"].Raw != "2" || !env["REGION"].Custom {
		t.Fatalf("server or custom value wrong: %+v", env)
	}
	secret := env["API_KEY"]
	if secret.Type != EnvSecret || secret.Raw != "secret://instance/7/API_KEY" || strings.Contains(string(secret.Value), "plain") {
		t.Fatalf("secret must be a reference: %+v", secret)
	}
	if id, name, ok := ParseSecretRef(secret.Raw); !ok || id != 7 || name != "API_KEY" {
		t.Fatal("secret reference does not parse")
	}
	for _, bad := range []string{"secret://instance/0/A", "secret://instance/x/A", "secret://other/7/A", "secret://instance/7/lower"} {
		if _, _, ok := ParseSecretRef(bad); ok {
			t.Errorf("reference %q accepted", bad)
		}
	}
	values["VERBOSE"] = raw(`true`)
	if env := ResolveRuntimeEnvironment(7, manifest, values, nil, nil); env["DETAIL"].Raw != "hidden" {
		t.Fatal("active conditional field was not delivered")
	}
}

func TestGrantNeverExceedsManifestAndCarriesForwardSafely(t *testing.T) {
	manifest := mustManifest(t, withField(t, withField(t, traceManifest, "capabilities", []string{"network.trace", "http.request", "notifications.send"}), "http", map[string]any{"hosts": []string{"*.aliyuncs.com"}, "methods": []string{"GET"}}))
	exists := func(int64) bool { return true }
	bad := []Grant{
		{Capabilities: map[string]CapabilityGrant{"network.ping": {Servers: []int64{1}}}},
		{Capabilities: map[string]CapabilityGrant{"network.trace": {}}},
		{Capabilities: map[string]CapabilityGrant{"network.trace": {Hosts: []string{"x"}}}},
		{Capabilities: map[string]CapabilityGrant{"http.request": {Hosts: []string{"evil.example.com"}}}},
		{Capabilities: map[string]CapabilityGrant{"notifications.send": {}}},
		{Capabilities: map[string]CapabilityGrant{"shell.exec": {}}},
	}
	for i, grant := range bad {
		if err := ValidateGrant(manifest, &grant, exists, exists, exists, exists); err == nil {
			t.Errorf("grant %d accepted", i)
		}
	}
	grant := Grant{Capabilities: map[string]CapabilityGrant{"network.trace": {Servers: []int64{3, 1, 3}}, "http.request": {Hosts: []string{"*.AliyunCS.com"}}, "notifications.send": {Channels: []int64{5}}}}
	if err := ValidateGrant(manifest, &grant, exists, exists, exists, exists); err != nil {
		t.Fatal(err)
	}
	if !grant.AllowsServer("network.trace", 1) || grant.AllowsServer("network.trace", 2) || grant.AllowsServer("network.ping", 1) || !grant.AllowsChannel(5) {
		t.Fatalf("grant scope wrong: %+v", grant)
	}
	next := mustManifest(t, withField(t, withField(t, traceManifest, "capabilities", []string{"network.trace", "network.ping", "http.request"}), "http", map[string]any{"hosts": []string{"*.aliyuncs.com", "api.example.com"}, "methods": []string{"GET", "POST"}}))
	carried := grant.RestrictTo(next)
	if carried.Allows("network.ping") || carried.Allows("notifications.send") || strings.Join(carried.HTTPHosts(), ",") != "*.aliyuncs.com" {
		t.Fatalf("grant carried more than before: %+v", carried)
	}
	diff := DiffPermissions(&manifest, next)
	if !diff.Expanded || strings.Join(diff.AddedCapabilities, ",") != "network.ping" || strings.Join(diff.AddedHosts, ",") != "api.example.com" || strings.Join(diff.AddedMethods, ",") != "POST" || strings.Join(diff.RemovedCapabilities, ",") != "notifications.send" {
		t.Fatalf("diff wrong: %+v", diff)
	}
	if reduced := DiffPermissions(&next, manifest); strings.Join(reduced.AddedCapabilities, ",") != "notifications.send" {
		t.Fatalf("reverse diff wrong: %+v", reduced)
	}
	shrink := mustManifest(t, withField(t, traceManifest, "capabilities", []string{"network.trace"}))
	if diff := DiffPermissions(&manifest, shrink); diff.Expanded {
		t.Fatalf("shrinking permissions must not require review: %+v", diff)
	}
	withSecret := withField(t, traceManifest, "capabilities", []string{"network.trace", "secrets.use"})
	withSecret = withField(t, withSecret, "environment", []map[string]any{{"name": "SERVER", "type": "server", "label": "s", "required": true}, {"name": "TOKEN", "type": "secret", "label": "t"}, {"name": "NEW", "type": "string", "label": "n", "required": true}})
	diff = DiffPermissions(&shrink, mustManifest(t, withSecret))
	if !diff.Expanded || strings.Join(diff.AddedSecrets, ",") != "TOKEN" || !strings.Contains(strings.Join(diff.NewRequired, ","), "NEW") {
		t.Fatalf("new secret and required field must be reported: %+v", diff)
	}
}
