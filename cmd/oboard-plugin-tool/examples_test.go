package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/pluginruntime"
)

func resource(t *testing.T, parts ...string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func compileSchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	data := resource(t, "sdk", "plugins", name+".schema.json")
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	location := "mem://" + name + ".json"
	if err := compiler.AddResource(location, doc); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile(location)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validateSchema(schema *jsonschema.Schema, data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	return schema.Validate(value)
}

func TestAllExamplePackagesAndSchemas(t *testing.T) {
	manifestSchema, uiSchema := compileSchema(t, "manifest"), compileSchema(t, "ui")
	for _, name := range []string{"template", "node-inspection", "https-notification", "batch-operations"} {
		t.Run(name, func(t *testing.T) {
			archive, err := buildDirectory(filepath.Join("..", "..", "examples", "plugins", name))
			if err != nil {
				t.Fatal(err)
			}
			pkg, err := validate(archive)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateSchema(manifestSchema, pkg.Manifest); err != nil {
				t.Fatal(err)
			}
			if pkg.UI != nil {
				if err := validateSchema(uiSchema, pkg.UI); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestSchemasRejectUnsupportedShapes(t *testing.T) {
	manifest, ui := compileSchema(t, "manifest"), compileSchema(t, "ui")
	var valid map[string]any
	if err := json.Unmarshal(resource(t, "examples", "plugins", "template", "manifest.json"), &valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		key   string
		value any
	}{
		{"entry", "index.js"}, {"runtime", "node"}, {"capabilities", []string{"node.exec_shell"}},
		{"plugin_id", "../escape"}, {"version", "latest"}, {"unknown", true},
		{"limits", map[string]any{"sdk_calls": 101}}, {"ui_content_sha256", "untrusted"},
	} {
		t.Run(change.key, func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range valid {
				copy[k] = v
			}
			copy[change.key] = change.value
			data, _ := json.Marshal(copy)
			if err := validateSchema(manifest, data); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	for _, data := range []string{
		`{"pages":[]}`,
		`{"pages":[{"id":"x","title":"X","components":[{"id":"a","type":"iframe"}]}]}`,
		`{"pages":[{"id":"x","title":"X","components":[{"id":"a","type":"form"}]}]}`,
		`{"pages":[{"id":"x","title":"X","components":[{"id":"a","type":"text","action":{"label":"go"}}]}]}`,
		`{"pages":[{"id":"x","title":"X","components":[{"id":"a","type":"form","action":{"label":"go"},"fields":[{"name":"a","label":"A","type":"select","options":[]}]}]}]}`,
	} {
		if err := validateSchema(ui, []byte(data)); err == nil {
			t.Fatalf("invalid UI accepted: %s", data)
		}
	}
}

func executeExample(t *testing.T, name string, params string, invoke pluginruntime.SDKFunc) (map[string]any, error) {
	t.Helper()
	source := resource(t, "examples", "plugins", name, "main.js")
	manifestRaw := resource(t, "examples", "plugins", name, "manifest.json")
	var manifest struct {
		Params       json.RawMessage `json:"params_schema"`
		Capabilities []string        `json:"capabilities"`
	}
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := plugin.ValidateParams(manifest.Params, json.RawMessage(params)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	result, err := pluginruntime.Execute(string(source), json.RawMessage(params), map[string]string{"REPORT_LABEL": "test"}, pluginrpc.RunLimits{TimeoutSeconds: 5, SDKCalls: 100, LogBytes: 4096, ResultBytes: 65536}, func(cap string, args json.RawMessage, key string) (pluginrpc.SDKResponse, error) {
		calls++
		allowed := false
		for _, declaration := range manifest.Capabilities {
			if cap == declaration {
				allowed = true
			}
		}
		if !allowed {
			t.Fatalf("undeclared capability %s", cap)
		}
		if !strings.HasPrefix(key, cap+":") {
			t.Fatalf("invalid action key %q", key)
		}
		return invoke(cap, args, key)
	}, func(level, message string, fields json.RawMessage) {
		if strings.Contains(message, "do-not-expose") {
			t.Fatal("secret in log")
		}
	})
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(result), "do-not-expose") {
		t.Fatal("secret in result")
	}
	var decoded map[string]any
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded, nil
}

func sdkOK(value any) pluginrpc.SDKResponse {
	raw, _ := json.Marshal(value)
	return pluginrpc.SDKResponse{OK: true, Result: raw}
}

func TestTemplateAndInspectionRuntime(t *testing.T) {
	for _, exists := range []bool{false, true} {
		result, err := executeExample(t, "template", `{}`, func(cap string, args json.RawMessage, key string) (pluginrpc.SDKResponse, error) {
			if cap != "state.get" || string(args) != `{"key":"greeting"}` {
				t.Fatalf("unexpected call %s %s", cap, args)
			}
			return sdkOK(map[string]any{"exists": exists, "version": "1", "value": "hello"}), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		want := "你好，OBoard"
		if exists {
			want = "hello"
		}
		if result["message"] != want {
			t.Fatalf("unexpected result: %v", result)
		}
	}
	for _, allowed := range []bool{false, true} {
		result, err := executeExample(t, "node-inspection", `{"server_id":"42"}`, func(cap string, args json.RawMessage, key string) (pluginrpc.SDKResponse, error) {
			if (cap != "servers.status" && cap != "metrics.latest") || string(args) != `{"server_id":"42"}` {
				t.Fatalf("unexpected call %s %s", cap, args)
			}
			if cap == "metrics.latest" {
				return sdkOK(map[string]any{"exists": false, "stale": true}), nil
			}
			if !allowed {
				return pluginrpc.SDKResponse{OK: false, ErrorCode: "permission_denied", Message: "denied"}, nil
			}
			return sdkOK(map[string]any{"server_id": "42", "stale": true, "core_running": "unknown"}), nil
		})
		if !allowed {
			if err == nil {
				t.Fatal("permission denial swallowed")
			}
			continue
		}
		if err != nil || result["status"].(map[string]any)["core_running"] != "unknown" {
			t.Fatalf("result=%v err=%v", result, err)
		}
	}
}

func TestInspectionMetricsAndOptionalDiagnostic(t *testing.T) {
	for _, mode := range []string{"read-only", "live", "simulate", "denied"} {
		t.Run(mode, func(t *testing.T) {
			calls := []string{}
			diagnose := "true"
			if mode == "read-only" {
				diagnose = "false"
			}
			result, err := executeExample(t, "node-inspection", `{"server_id":"42","diagnose":`+diagnose+`}`, func(cap string, args json.RawMessage, key string) (pluginrpc.SDKResponse, error) {
				calls = append(calls, cap)
				switch cap {
				case "servers.status":
					return sdkOK(map[string]any{"stale": false, "config_applied": "false", "core_running": "unknown"}), nil
				case "metrics.latest":
					return sdkOK(map[string]any{"exists": true, "stale": false, "cpu_usage_percent": 91, "memory_used_bytes": 95, "memory_total_bytes": 100}), nil
				case "services.status":
					if string(args) != `{"server_id":"42","service":"oboard-sb"}` {
						t.Fatal(string(args))
					}
					if mode == "denied" {
						return pluginrpc.SDKResponse{ErrorCode: "permission_denied", Message: "denied"}, nil
					}
					if mode == "simulate" {
						return sdkOK(map[string]any{"simulated": true}), nil
					}
					return sdkOK(map[string]any{"accepted": true, "operation_id": "op"}), nil
				case "operations.get":
					if string(args) != `{"operation_id":"op"}` {
						t.Fatal(string(args))
					}
					return sdkOK(map[string]any{"status": "dispatching", "task_status": "running"}), nil
				default:
					t.Fatalf("unexpected capability %s", cap)
					return pluginrpc.SDKResponse{}, nil
				}
			})
			if mode == "denied" {
				if err == nil {
					t.Fatal("diagnostic denial swallowed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result["findings"].([]any)) != 3 {
				t.Fatalf("missing measured warnings: %v", result)
			}
			want := map[string]int{"read-only": 2, "live": 4, "simulate": 3}[mode]
			if len(calls) != want {
				t.Fatalf("unexpected calls: %v", calls)
			}
			if mode == "live" && result["diagnostic"].(map[string]any)["receipt"].(map[string]any)["task_status"] != "running" {
				t.Fatal("accepted diagnostic was treated as complete")
			}
		})
	}
}

func TestHTTPSNotificationRuntimeDoesNotLeakUpstream(t *testing.T) {
	for _, status := range []int{200, 202, 302, 401, 500, 0} {
		result, err := executeExample(t, "https-notification", `{"message":"test"}`, func(cap string, args json.RawMessage, key string) (pluginrpc.SDKResponse, error) {
			var input struct {
				Secret  string `json:"secret"`
				Request struct {
					URL    string `json:"url"`
					Method string `json:"method"`
					Body   string `json:"body"`
				} `json:"request"`
			}
			if json.Unmarshal(args, &input) != nil || cap != "network.request" || input.Secret != "notify_token" || input.Request.URL != "https://example.com/notify" || input.Request.Method != "POST" || input.Request.Body != `{"message":"test"}` {
				t.Fatalf("unexpected call %s %s", cap, args)
			}
			if status == 0 {
				return sdkOK(map[string]any{"simulated": true}), nil
			}
			return sdkOK(map[string]any{"status": status, "body": "do-not-expose", "headers": map[string]string{"authorization": "do-not-expose"}}), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if status == 0 {
			if result["simulated"] != true {
				t.Fatal(result)
			}
			continue
		}
		if result["accepted"] != (status >= 200 && status < 300) {
			t.Fatal(result)
		}
	}
}

func TestBatchOperationsPreviewConfirmAndPartialFailure(t *testing.T) {
	for _, mode := range []string{"preview", "live", "simulate", "partial-failure"} {
		t.Run(mode, func(t *testing.T) {
			calls := []string{}
			confirm := "true"
			if mode == "preview" {
				confirm = "false"
			}
			result, err := executeExample(t, "batch-operations", `{"server_ids":"42,43","service":"oboard-sb","confirm":`+confirm+`}`, func(cap string, args json.RawMessage, key string) (pluginrpc.SDKResponse, error) {
				calls = append(calls, cap)
				var input map[string]any
				if json.Unmarshal(args, &input) != nil {
					t.Fatal(string(args))
				}
				switch cap {
				case "servers.get":
					return sdkOK(map[string]any{"name": "test"}), nil
				case "services.restart":
					if len(calls) < 3 || input["service"] != "oboard-sb" {
						t.Fatal("restart before all targets were read")
					}
					if mode == "simulate" {
						return sdkOK(map[string]any{"simulated": true, "operation_id": "sim"}), nil
					}
					if mode == "partial-failure" && input["server_id"] == "43" {
						return pluginrpc.SDKResponse{OK: false, ErrorCode: "target_offline", Message: "offline"}, nil
					}
					return sdkOK(map[string]any{"accepted": true, "operation_id": "op"}), nil
				case "operations.get":
					return sdkOK(map[string]any{"status": "dispatching"}), nil
				default:
					t.Fatalf("unexpected call: %s", cap)
					return pluginrpc.SDKResponse{}, nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if result["preview"] != (mode == "preview") {
				t.Fatal(result)
			}
			if mode == "preview" && len(calls) != 2 {
				t.Fatalf("preview wrote: %v", calls)
			}
			rows := result["rows"].([]any)
			if len(rows) != 2 {
				t.Fatal(result)
			}
			if mode == "partial-failure" && rows[1].(map[string]any)["error_code"] != "target_offline" {
				t.Fatal(result)
			}
		})
	}
	for _, ids := range []string{"42,42", "42,bad", "1,2,3,4,5,6"} {
		_, err := executeExample(t, "batch-operations", `{"server_ids":"`+ids+`","service":"oboard-sb","confirm":true}`, func(string, json.RawMessage, string) (pluginrpc.SDKResponse, error) {
			t.Fatal("invalid targets reached SDK")
			return pluginrpc.SDKResponse{}, nil
		})
		if err == nil {
			t.Fatalf("invalid target list accepted: %s", ids)
		}
	}
}

func TestManifestSDKMethodsExistInRunnerAndTypeDeclarations(t *testing.T) {
	var schema struct {
		Properties struct {
			Capabilities struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"capabilities"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(resource(t, "sdk", "plugins", "manifest.schema.json"), &schema); err != nil {
		t.Fatal(err)
	}
	types := string(resource(t, "sdk", "plugins", "oboard.d.ts"))
	for _, name := range schema.Properties.Capabilities.Items.Enum {
		if strings.HasPrefix(name, "management:") {
			if !plugin.ManagementCapabilityAllowed(strings.TrimPrefix(name, "management:")) {
				t.Fatalf("unsupported management declaration: %s", name)
			}
			continue
		}
		parts := strings.Split(name, ".")
		if len(parts) != 2 || !strings.Contains(types, parts[0]+":") || !strings.Contains(types, parts[1]+"(") && !strings.Contains(types, parts[1]+"<") {
			t.Fatalf("type declaration missing %s", name)
		}
		raw, err := pluginruntime.Execute("function main(){return typeof oboard."+name+";}", json.RawMessage(`{}`), nil, pluginrpc.RunLimits{TimeoutSeconds: 5, ResultBytes: 1024}, nil, nil)
		if err != nil || string(raw) != `"function"` {
			t.Fatalf("documented SDK missing in runner: %s result=%s err=%v", name, raw, err)
		}
	}
}
