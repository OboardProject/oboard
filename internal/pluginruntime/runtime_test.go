package pluginruntime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/pluginrpc"
)

type recordedCall struct {
	method    string
	arguments string
}

func execute(t *testing.T, source string, env map[string]pluginrpc.EnvValue, handler func(method string, arguments json.RawMessage) pluginrpc.CallResponse) (Outcome, []recordedCall, []string) {
	t.Helper()
	var calls []recordedCall
	var logs []string
	if handler == nil {
		handler = func(string, json.RawMessage) pluginrpc.CallResponse {
			return pluginrpc.CallResponse{OK: true, Result: json.RawMessage(`{}`)}
		}
	}
	outcome := Execute(source, env, pluginrpc.RunContext{RunID: "run-1", PluginID: "acme.demo", InstanceID: "inst-1", Trigger: "manual"},
		pluginrpc.Limits{TimeoutMS: 2000, ResultBytes: 64 << 10},
		func(method string, arguments json.RawMessage) pluginrpc.CallResponse {
			calls = append(calls, recordedCall{method: method, arguments: string(arguments)})
			return handler(method, arguments)
		},
		func(level, message string) { logs = append(logs, level+":"+message) })
	return outcome, calls, logs
}

func TestRuntimeExposesNoHostModules(t *testing.T) {
	source := `function main(run) {
		return {
			require: typeof require, process: typeof process, fs: typeof fs, net: typeof net,
			setTimeout: typeof setTimeout, fetch: typeof fetch, WebAssembly: typeof WebAssembly,
			Buffer: typeof Buffer, module: typeof module, exports: typeof exports,
			run: run.run_id, trigger: run.trigger
		};
	}`
	outcome, _, _ := execute(t, source, nil, nil)
	if outcome.Code != "" {
		t.Fatalf("unexpected failure %s: %s", outcome.Code, outcome.Message)
	}
	var result map[string]string
	if err := json.Unmarshal(outcome.Result, &result); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"require", "process", "fs", "net", "setTimeout", "fetch", "WebAssembly", "Buffer", "module", "exports"} {
		if result[name] != "undefined" {
			t.Fatalf("%s must not be available, got %s", name, result[name])
		}
	}
	if result["run"] != "run-1" || result["trigger"] != "manual" {
		t.Fatalf("run context not passed: %v", result)
	}
}

func TestRuntimeSDKIsFrozenAndCannotBeReplaced(t *testing.T) {
	source := `function main() {
		'use strict';
		var errors = [];
		try { oboard.http = null; } catch (e) { errors.push('http'); }
		try { oboard.servers.get = function () { return 1; }; } catch (e) { errors.push('servers.get'); }
		try { globalThis.oboard = {}; } catch (e) { errors.push('global'); }
		try { env.TOKEN = 'x'; } catch (e) { errors.push('env'); }
		return errors;
	}`
	outcome, _, _ := execute(t, source, map[string]pluginrpc.EnvValue{"TOKEN": {Type: "string", Value: json.RawMessage(`"a"`), Raw: "a"}}, nil)
	if outcome.Code != "" {
		t.Fatalf("unexpected failure %s: %s", outcome.Code, outcome.Message)
	}
	if string(outcome.Result) != `["http","servers.get","global","env"]` {
		t.Fatalf("SDK objects must be immutable, got %s", outcome.Result)
	}
}

func TestRuntimeSecretRefNeverExposesValue(t *testing.T) {
	env := map[string]pluginrpc.EnvValue{
		"API_TOKEN": {Type: "secret", Value: json.RawMessage(`{"$secret":"secret://instance/inst-1/API_TOKEN"}`), Raw: "secret://instance/inst-1/API_TOKEN"},
	}
	source := `function main() {
		var token = env.API_TOKEN;
		log.info('token', token, String(token));
		oboard.http.request({ url: 'https://api.example.com/v1', headers: { Authorization: token.withPrefix('Bearer ') } });
		return { text: String(token), raw: env.raw('API_TOKEN'), json: JSON.stringify(token), isRef: token instanceof SecretRef };
	}`
	outcome, calls, logs := execute(t, source, env, func(string, json.RawMessage) pluginrpc.CallResponse {
		return pluginrpc.CallResponse{OK: true, Result: json.RawMessage(`{"status":200,"headers":{},"body":""}`)}
	})
	if outcome.Code != "" {
		t.Fatalf("unexpected failure %s: %s", outcome.Code, outcome.Message)
	}
	var result map[string]any
	_ = json.Unmarshal(outcome.Result, &result)
	if result["text"] != "[SecretRef API_TOKEN]" || result["raw"] != "[SecretRef API_TOKEN]" || result["isRef"] != true {
		t.Fatalf("unexpected secret rendering %v", result)
	}
	if len(calls) != 1 || calls[0].method != "http.request" {
		t.Fatalf("unexpected calls %v", calls)
	}
	if !strings.Contains(calls[0].arguments, `"Authorization":{"$secret":"secret://instance/inst-1/API_TOKEN","prefix":"Bearer "}`) {
		t.Fatalf("secret must travel as a reference, got %s", calls[0].arguments)
	}
	for _, line := range logs {
		if !strings.Contains(line, "[SecretRef API_TOKEN]") && strings.Contains(line, "token") {
			t.Fatalf("log leaked secret reference internals: %s", line)
		}
	}
}

func TestRuntimeAsyncMainAndGatewayErrors(t *testing.T) {
	source := `async function main() {
		try {
			await oboard.servers.get('srv-1');
		} catch (e) {
			return { code: e.code, name: e.name, isError: e instanceof OBoardError };
		}
		return null;
	}`
	outcome, calls, _ := execute(t, source, nil, func(string, json.RawMessage) pluginrpc.CallResponse {
		return pluginrpc.CallResponse{Code: "RESOURCE_DENIED", Message: "server is not granted"}
	})
	if outcome.Code != "" {
		t.Fatalf("unexpected failure %s: %s", outcome.Code, outcome.Message)
	}
	if string(outcome.Result) != `{"code":"RESOURCE_DENIED","name":"OBoardError","isError":true}` {
		t.Fatalf("unexpected result %s", outcome.Result)
	}
	if len(calls) != 1 || calls[0].arguments != `{"server_id":"srv-1"}` {
		t.Fatalf("unexpected call %v", calls)
	}
}

func TestRuntimeUncaughtErrorsKeepTheirCode(t *testing.T) {
	outcome, _, _ := execute(t, `function main() { throw new OBoardError('CONFIG_INVALID', 'bad threshold'); }`, nil, nil)
	if outcome.Code != "CONFIG_INVALID" || outcome.Message != "bad threshold" {
		t.Fatalf("unexpected outcome %+v", outcome)
	}
	outcome, _, _ = execute(t, `async function main() { throw new Error('boom'); }`, nil, nil)
	if outcome.Code != codeScriptError || !strings.Contains(outcome.Message, "boom") {
		t.Fatalf("unexpected outcome %+v", outcome)
	}
	outcome, _, _ = execute(t, `var x = 1;`, nil, nil)
	if outcome.Code != codeScriptError {
		t.Fatalf("missing main must fail, got %+v", outcome)
	}
}

func TestRuntimeEnforcesDeadlineAndResultSize(t *testing.T) {
	outcome := Execute(`function main() { for (;;) {} }`, nil, pluginrpc.RunContext{}, pluginrpc.Limits{TimeoutMS: 100, ResultBytes: 1024},
		func(string, json.RawMessage) pluginrpc.CallResponse { return pluginrpc.CallResponse{} }, func(string, string) {})
	if outcome.Code != codeRunTimeout {
		t.Fatalf("infinite loop must time out, got %+v", outcome)
	}
	outcome = Execute(`function main() { return 'x'.repeat(4096); }`, nil, pluginrpc.RunContext{}, pluginrpc.Limits{TimeoutMS: 1000, ResultBytes: 1024},
		func(string, json.RawMessage) pluginrpc.CallResponse { return pluginrpc.CallResponse{} }, func(string, string) {})
	if outcome.Code != codeResourceLimit {
		t.Fatalf("oversized result must fail, got %+v", outcome)
	}
	outcome = Execute(`function main() { return new Promise(function () {}); }`, nil, pluginrpc.RunContext{}, pluginrpc.Limits{TimeoutMS: 1000},
		func(string, json.RawMessage) pluginrpc.CallResponse { return pluginrpc.CallResponse{} }, func(string, string) {})
	if outcome.Code != codeScriptError {
		t.Fatalf("unsettled promise must fail, got %+v", outcome)
	}
	outcome = Execute(`function f() { return f(); } function main() { return f(); }`, nil, pluginrpc.RunContext{}, pluginrpc.Limits{TimeoutMS: 1000},
		func(string, json.RawMessage) pluginrpc.CallResponse { return pluginrpc.CallResponse{} }, func(string, string) {})
	if outcome.Code != codeResourceLimit {
		t.Fatalf("stack overflow must be a resource limit, got %+v", outcome)
	}
}

func TestRuntimeLocalCrypto(t *testing.T) {
	source := `function main() {
		var c = oboard.crypto;
		return {
			sha256: c.sha256('abc'),
			hmac: c.hmacSha256('key', 'The quick brown fox jumps over the lazy dog'),
			b64: c.base64Encode('hello'),
			b64url: c.base64Encode('ÿþ', { url: true }),
			unb64: c.base64Decode('aGVsbG8='),
			hex: c.hexEncode('hi'),
			random: c.randomBytes(8).length,
			uuid: /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(c.uuid())
		};
	}`
	outcome, calls, _ := execute(t, source, nil, nil)
	if outcome.Code != "" {
		t.Fatalf("unexpected failure %s: %s", outcome.Code, outcome.Message)
	}
	if len(calls) != 0 {
		t.Fatalf("local crypto must not reach the gateway: %v", calls)
	}
	var result map[string]any
	_ = json.Unmarshal(outcome.Result, &result)
	want := map[string]any{
		"sha256": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		"hmac":   "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8",
		"b64":    "aGVsbG8=",
		"unb64":  "hello",
		"hex":    "6869",
		"random": float64(16),
		"uuid":   true,
	}
	for key, value := range want {
		if result[key] != value {
			t.Fatalf("%s = %v, want %v", key, result[key], value)
		}
	}
}

func TestRuntimeSecretHMACIsComputedByTheGateway(t *testing.T) {
	env := map[string]pluginrpc.EnvValue{
		"SIGNING_KEY": {Type: "secret", Value: json.RawMessage(`{"$secret":"secret://instance/inst-1/SIGNING_KEY"}`), Raw: "secret://instance/inst-1/SIGNING_KEY"},
	}
	outcome, calls, _ := execute(t, `function main() { return oboard.crypto.hmacSha256(env.SIGNING_KEY, 'payload', { output: 'base64' }); }`, env,
		func(string, json.RawMessage) pluginrpc.CallResponse {
			return pluginrpc.CallResponse{OK: true, Result: json.RawMessage(`{"digest":"c2lnbmVk"}`)}
		})
	if outcome.Code != "" || string(outcome.Result) != `"c2lnbmVk"` {
		t.Fatalf("unexpected outcome %+v", outcome)
	}
	if len(calls) != 1 || calls[0].method != "crypto.hmac" || !strings.Contains(calls[0].arguments, `"key":{"$secret":"secret://instance/inst-1/SIGNING_KEY"}`) {
		t.Fatalf("secret HMAC must be delegated, got %v", calls)
	}
}
