package aiprovider

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestResolveEndpointURLAndHeaderSecurity(t *testing.T) {
	target, err := ResolveEndpointURL("https://api.example.com/company/v1", "/responses")
	if err != nil || target.String() != "https://api.example.com/company/v1/responses" {
		t.Fatalf("resolved URL = %v, %v", target, err)
	}
	if _, err := ResolveEndpointURL("https://api.example.com/v1", "https://attacker.example/x"); err == nil {
		t.Fatal("absolute path override was accepted")
	}
	request, _ := http.NewRequest(http.MethodGet, "https://api.example.com", nil)
	endpoint := RuntimeEndpoint{AuthMode: AuthModeBearer, Credential: "secret", Headers: map[string]string{"Authorization": "override"}}
	if err := ApplyHeaders(request, endpoint); err == nil {
		t.Fatal("reserved custom header was accepted")
	}
	if !blockedProviderIP(netip.MustParseAddr("169.254.169.254"), true) || !blockedProviderIP(netip.MustParseAddr("127.0.0.1"), false) || blockedProviderIP(netip.MustParseAddr("127.0.0.1"), true) {
		t.Fatal("provider address policy mismatch")
	}
}

func TestNormalizeEndpointBaseURLNetworkPolicy(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		allowPrivate bool
		wantError    bool
	}{
		{name: "public https", url: "https://api.example.com/v1"},
		{name: "public http", url: "http://203.0.113.10/v1", wantError: true},
		{name: "loopback denied", url: "http://127.0.0.1:8080/v1", wantError: true},
		{name: "loopback allowed", url: "http://127.0.0.1:8080/v1", allowPrivate: true},
		{name: "private denied", url: "https://10.0.0.2/v1", wantError: true},
		{name: "private allowed", url: "https://10.0.0.2/v1", allowPrivate: true},
		{name: "metadata always denied", url: "http://169.254.169.254/latest", allowPrivate: true, wantError: true},
		{name: "alibaba metadata always denied", url: "https://100.100.100.200/latest", allowPrivate: true, wantError: true},
		{name: "aws ipv6 metadata always denied", url: "https://[fd00:ec2::254]/latest", allowPrivate: true, wantError: true},
		{name: "link local always denied", url: "https://[fe80::1]/v1", allowPrivate: true, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NormalizeEndpointBaseURL(test.url, test.allowPrivate)
			if (err != nil) != test.wantError {
				t.Fatalf("NormalizeEndpointBaseURL() error = %v, want error %v", err, test.wantError)
			}
		})
	}
}

func TestConfigDigestChangesWithEndpointContract(t *testing.T) {
	endpoint := RuntimeEndpoint{BaseURL: "https://api.example.com/v1", APIStyle: APIStyleOpenAIResponses, AuthMode: AuthModeBearer, Headers: map[string]string{"X-Tenant": "one"}}
	first := ConfigDigest(endpoint, "model")
	endpoint.Headers["X-Tenant"] = "two"
	if first == ConfigDigest(endpoint, "model") {
		t.Fatal("header change did not invalidate config digest")
	}
}

type scriptedClient struct {
	results []error
	calls   int
}

func (c *scriptedClient) ListModels(context.Context, RuntimeEndpoint) ([]ModelInfo, error) {
	return []ModelInfo{{ID: "m"}}, nil
}
func (c *scriptedClient) Complete(_ context.Context, _ RuntimeEndpoint, request Request) (*Response, error) {
	c.calls++
	if len(c.results) >= c.calls && c.results[c.calls-1] != nil {
		return nil, c.results[c.calls-1]
	}
	return &Response{Text: "{}", Model: request.Model}, nil
}
func (c *scriptedClient) Stream(context.Context, RuntimeEndpoint, Request, func(StreamEvent) error) error {
	return nil
}

func TestRouterFailoverAndNonRetryableErrors(t *testing.T) {
	primary := &scriptedClient{results: []error{NewError(ErrorTimeout, true, 0, "timeout", nil)}}
	secondary := &scriptedClient{}
	router := NewRouter(Registry{APIStyleOpenAIResponses: primary, APIStyleOpenAIChatCompletions: secondary})
	router.sleep = func(context.Context, time.Duration) error { return nil }
	provider := RuntimeProvider{ID: "p", Model: "m", Endpoints: []RuntimeEndpoint{{ID: "one", APIStyle: APIStyleOpenAIResponses, Enabled: true, Priority: 10, MaxRetries: 0}, {ID: "two", APIStyle: APIStyleOpenAIChatCompletions, Enabled: true, Priority: 20}}}
	response, err := router.Complete(context.Background(), provider, Request{}, false)
	if err != nil || response.EndpointID != "two" || primary.calls != 1 || secondary.calls != 1 {
		t.Fatalf("failover response=%#v err=%v calls=%d/%d", response, err, primary.calls, secondary.calls)
	}

	primary = &scriptedClient{results: []error{NewError(ErrorAuthFailed, false, 401, "bad key", nil)}}
	secondary = &scriptedClient{}
	router = NewRouter(Registry{APIStyleOpenAIResponses: primary, APIStyleOpenAIChatCompletions: secondary})
	_, err = router.Complete(context.Background(), provider, Request{}, false)
	if AsProviderError(err).Kind != ErrorAuthFailed || secondary.calls != 0 {
		t.Fatalf("401 failover err=%v secondary calls=%d", err, secondary.calls)
	}
}

func TestRouterRetriesRateLimitAndFailsOverUpstreamErrors(t *testing.T) {
	primary := &scriptedClient{results: []error{
		NewError(ErrorRateLimited, true, 429, "limited", nil),
		NewError(ErrorRateLimited, true, 429, "limited", nil),
	}}
	secondary := &scriptedClient{}
	router := NewRouter(Registry{APIStyleOpenAIResponses: primary, APIStyleOpenAIChatCompletions: secondary})
	router.sleep = func(context.Context, time.Duration) error { return nil }
	provider := RuntimeProvider{ID: "p", Model: "m", Endpoints: []RuntimeEndpoint{{ID: "one", APIStyle: APIStyleOpenAIResponses, Enabled: true, Priority: 10, MaxRetries: 1}, {ID: "two", APIStyle: APIStyleOpenAIChatCompletions, Enabled: true, Priority: 20}}}
	response, err := router.Complete(context.Background(), provider, Request{}, false)
	if err != nil || response.EndpointID != "two" || primary.calls != 2 || secondary.calls != 1 {
		t.Fatalf("429 response=%#v err=%v calls=%d/%d", response, err, primary.calls, secondary.calls)
	}

	primary = &scriptedClient{results: []error{NewError(ErrorUpstream5xx, true, 500, "temporary", nil)}}
	secondary = &scriptedClient{}
	router = NewRouter(Registry{APIStyleOpenAIResponses: primary, APIStyleOpenAIChatCompletions: secondary})
	provider.Endpoints[0].MaxRetries = 0
	response, err = router.Complete(context.Background(), provider, Request{}, false)
	if err != nil || response.EndpointID != "two" || secondary.calls != 1 {
		t.Fatalf("500 response=%#v err=%v secondary=%d", response, err, secondary.calls)
	}

	primary = &scriptedClient{results: []error{NewError(ErrorInvalidRequest, false, 400, "bad request", nil)}}
	secondary = &scriptedClient{}
	router = NewRouter(Registry{APIStyleOpenAIResponses: primary, APIStyleOpenAIChatCompletions: secondary})
	_, err = router.Complete(context.Background(), provider, Request{}, false)
	if AsProviderError(err).Kind != ErrorInvalidRequest || secondary.calls != 0 {
		t.Fatalf("400 err=%v secondary=%d", err, secondary.calls)
	}
}

func TestRouterFormalAuditDoesNotFallBackToNonAuditReadyEndpoint(t *testing.T) {
	primary := &scriptedClient{results: []error{NewError(ErrorTimeout, true, 0, "timeout", nil)}}
	secondary := &scriptedClient{}
	router := NewRouter(Registry{APIStyleOpenAIResponses: primary, APIStyleOpenAIChatCompletions: secondary})
	ready := RuntimeEndpoint{ID: "ready", BaseURL: "https://ready.example/v1", APIStyle: APIStyleOpenAIResponses, AuthMode: AuthModeBearer, Enabled: true, Priority: 10, Capability: &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, EndpointID: "ready", Model: "m", ConnectivityOK: true, AuthenticationOK: true, TextSupported: true, AuditReady: true, StructuredOutput: model.AuditProviderStructuredJSONSchema, OutputMode: model.AuditOutputModeStrictSchema}}
	ready.Capability.ConfigDigest = ConfigDigest(ready, "m")
	notReady := RuntimeEndpoint{ID: "not-ready", BaseURL: "https://not-ready.example/v1", APIStyle: APIStyleOpenAIChatCompletions, AuthMode: AuthModeBearer, Enabled: true, Priority: 20, Capability: &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, EndpointID: "not-ready", Model: "m", ConnectivityOK: true, AuthenticationOK: true, AuditReady: false, StructuredOutput: model.AuditProviderStructuredNone, OutputMode: model.AuditOutputModeText}}
	notReady.Capability.ConfigDigest = ConfigDigest(notReady, "m")
	_, err := router.Complete(context.Background(), RuntimeProvider{ID: "p", Model: "m", Endpoints: []RuntimeEndpoint{ready, notReady}}, Request{}, true)
	if AsProviderError(err).Kind != ErrorAllEndpoints || primary.calls != 1 || secondary.calls != 0 {
		t.Fatalf("formal failover err=%v calls=%d/%d", err, primary.calls, secondary.calls)
	}
}

func TestRouterFormalAuditRejectsStaleOrNonAuditReadyEndpoints(t *testing.T) {
	client := &scriptedClient{}
	router := NewRouter(Registry{APIStyleOpenAIResponses: client})
	endpoint := RuntimeEndpoint{ID: "e", ProviderID: "p", BaseURL: "https://api.example.com/v1", APIStyle: APIStyleOpenAIResponses, AuthMode: AuthModeBearer, Enabled: true, Capability: &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, EndpointID: "e", Model: "m", ConnectivityOK: true, AuthenticationOK: true, StructuredOutput: model.AuditProviderStructuredNone, OutputMode: model.AuditOutputModeText}}
	endpoint.Capability.ConfigDigest = ConfigDigest(endpoint, "m")
	_, err := router.Complete(context.Background(), RuntimeProvider{ID: "p", Model: "m", Endpoints: []RuntimeEndpoint{endpoint}}, Request{}, true)
	if AsProviderError(err).Kind != ErrorNoEligible || client.calls != 0 {
		t.Fatalf("formal route err=%v calls=%d", err, client.calls)
	}

	endpoint.Capability.AuditReady = true
	endpoint.Capability.StructuredOutput = model.AuditProviderStructuredPromptedJSON
	endpoint.Capability.ConfigDigest = "stale"
	_, err = router.Complete(context.Background(), RuntimeProvider{ID: "p", Model: "m", Endpoints: []RuntimeEndpoint{endpoint}}, Request{}, true)
	if AsProviderError(err).Kind != ErrorNoEligible || client.calls != 0 {
		t.Fatalf("stale formal route err=%v calls=%d", err, client.calls)
	}
}

func TestRouterCircuitBreakerSkipsOpenEndpoint(t *testing.T) {
	client := &scriptedClient{results: []error{errors.New("one"), errors.New("two"), errors.New("three"), nil}}
	router := NewRouter(Registry{APIStyleOpenAIResponses: client})
	router.sleep = func(context.Context, time.Duration) error { return nil }
	provider := RuntimeProvider{ID: "p", Model: "m", Endpoints: []RuntimeEndpoint{{ID: "e", APIStyle: APIStyleOpenAIResponses, Enabled: true}}}
	for range 3 {
		_, _ = router.Complete(context.Background(), provider, Request{}, false)
	}
	_, err := router.Complete(context.Background(), provider, Request{}, false)
	if AsProviderError(err).Kind != ErrorAllEndpoints || client.calls != 3 {
		t.Fatalf("open circuit err=%v calls=%d", err, client.calls)
	}
}

func TestRouterCompleteEndpointRecordsLatency(t *testing.T) {
	client := &scriptedClient{}
	router := NewRouter(Registry{APIStyleOpenAIResponses: client})
	started := time.Unix(100, 0)
	clockCalls := 0
	router.now = func() time.Time {
		clockCalls++
		return started.Add(time.Duration(clockCalls-1) * time.Second)
	}
	response, err := router.CompleteEndpoint(context.Background(), RuntimeProvider{ID: "p", Model: "m", Endpoints: []RuntimeEndpoint{{ID: "e", APIStyle: APIStyleOpenAIResponses, Enabled: true}}}, "e", Request{})
	if err != nil || response.Latency != time.Second || response.AttemptCount != 1 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestValidateJSONSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []string{"status", "name", "count"}, "additionalProperties": false, "properties": map[string]any{
		"status": map[string]any{"type": "string", "enum": []string{"ok", "failed"}},
		"name":   map[string]any{"type": "string", "maxLength": 4},
		"count":  map[string]any{"type": "integer", "minimum": 0, "maximum": 2},
	}}
	if err := ValidateJSONSchema(schema, []byte(`{"status":"ok","name":"test","count":2}`)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"status":"other","name":"test","count":2}`,
		`{"status":"ok","name":"too-long","count":2}`,
		`{"status":"ok","name":"test","count":3}`,
		`{"status":"ok","name":"test","count":2,"extra":true}`,
		`{"status":"ok","name":"test"}`,
	} {
		if err := ValidateJSONSchema(schema, []byte(raw)); err == nil {
			t.Fatalf("invalid document accepted: %s", raw)
		}
	}
}

func TestAdaptiveStructuredOutputHelpers(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []string{"ok"}, "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}}
	request := PrepareStructuredRequest(Request{System: "existing instructions", Schema: schema, OutputMode: model.AuditOutputModeText})
	if !strings.Contains(request.System, "existing instructions") || !strings.Contains(request.System, `"type":"object"`) || !strings.Contains(request.System, "不要使用 Markdown") {
		t.Fatalf("prompted JSON instructions = %q", request.System)
	}
	strict := PrepareStructuredRequest(Request{System: "unchanged", Schema: schema, OutputMode: model.AuditOutputModeStrictSchema})
	if strict.System != "unchanged" {
		t.Fatalf("strict-schema instructions changed: %q", strict.System)
	}

	raw := ExtractJSONObject("prefix {not-json} then {\"ok\":true} trailing {\"ok\":false}")
	if string(raw) != `{"ok":true}` {
		t.Fatalf("first valid object = %s", raw)
	}
	if raw := ExtractJSONObject("text without an object"); raw != nil {
		t.Fatalf("unexpected object = %s", raw)
	}
}

func TestCapabilityAuditReadyAcceptsEverySupportedOutputMode(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		structured string
		mode       string
	}{
		{name: "strict schema", structured: model.AuditProviderStructuredJSONSchema, mode: model.AuditOutputModeStrictSchema},
		{name: "JSON object", structured: model.AuditProviderStructuredJSONObject, mode: model.AuditOutputModeJSONObject},
		{name: "prompted JSON", structured: model.AuditProviderStructuredPromptedJSON, mode: model.AuditOutputModeText},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			capability := &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, ConnectivityOK: true, AuthenticationOK: true, TextSupported: true, AuditReady: true, StructuredOutput: testCase.structured, OutputMode: testCase.mode}
			if !CapabilityAuditReady(capability) {
				t.Fatalf("capability was not audit ready: %#v", capability)
			}
			capability.StructuredOutput = model.AuditProviderStructuredNone
			if CapabilityAuditReady(capability) {
				t.Fatalf("contradictory capability was accepted: %#v", capability)
			}
		})
	}
}
