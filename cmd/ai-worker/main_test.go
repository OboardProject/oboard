package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func controllerClient(t *testing.T, baseURL string) *http.Client {
	t.Helper()
	target, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
		clone.Host = target.Host
		return transport.RoundTrip(clone)
	})}
}

func aCapability(modelID string) *model.AIProviderCapability {
	return &model.AIProviderCapability{
		ProviderProfileVersion:  model.AuditProviderProfileVersion,
		Model:                   modelID,
		AuditGrade:              model.AuditProviderGradeA,
		StructuredOutput:        model.AuditProviderStructuredJSONSchema,
		OutputMode:              model.AuditOutputModeStrictSchema,
		SchemaSuccessRate:       1.0,
		UsageSupported:          true,
		FinishReasonSupported:   true,
		MaxVerifiedOutputTokens: 4096,
	}
}

func bCapability(modelID string) *model.AIProviderCapability {
	return &model.AIProviderCapability{
		ProviderProfileVersion:  model.AuditProviderProfileVersion,
		Model:                   modelID,
		AuditGrade:              model.AuditProviderGradeB,
		StructuredOutput:        model.AuditProviderStructuredJSONObject,
		OutputMode:              model.AuditOutputModeJSONObject,
		UsageSupported:          true,
		FinishReasonSupported:   true,
		MaxVerifiedOutputTokens: 4096,
	}
}

// samplePackJSON returns a valid audit-evidence-v2 pack for the given subject,
// with field-level evidence refs ev-01 (device_clone), ev-02
// (historical_anomaly), signal sig-01 and counter ce-01.
func samplePackJSON(t *testing.T, subjectRef string) json.RawMessage {
	t.Helper()
	var pack map[string]any
	if err := json.Unmarshal([]byte(contractSamplePack()), &pack); err != nil {
		t.Fatal(err)
	}
	subject, ok := pack["subject"].(map[string]any)
	if !ok {
		t.Fatal("sample pack has no subject")
	}
	subject["ref"] = subjectRef
	pack["subject"] = subject
	raw, err := json.Marshal(pack)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func findingJobInput(t *testing.T, subjectRef string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"subject_ref": subjectRef,
		"pack":        json.RawMessage(samplePackJSON(t, subjectRef)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func reportJobInput(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"engine": engineFixture()})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func engineFixture() map[string]any {
	return map[string]any{
		"overall_risk":             78,
		"health":                   22,
		"confidence":               0.84,
		"coverage":                 0.94,
		"baseline_days":            24,
		"dropped_buckets":          1,
		"identity_quality":         0.9,
		"feature_version":          1,
		"scoring_version":          model.AuditScoringVersion,
		"baseline_version":         model.AuditBaselineVersion,
		"evidence_schema_version":  model.AuditEvidenceSchemaVersion,
		"prompt_version":           model.AuditPromptReportVersion,
		"report_schema_version":    model.AuditReportSchemaVersion,
		"provider_profile_version": model.AuditProviderProfileVersion,
		"provider_grade":           model.AuditProviderGradeA,
		"structured_output":        model.AuditProviderStructuredJSONSchema,
		"output_mode":              model.AuditOutputModeStrictSchema,
		"model":                    "test-model",
		"subjects":                 []string{"user:sample"},
		"ref_categories": map[string]any{
			"user:sample/ev-01":  []string{"device_clone"},
			"user:sample/ev-02":  []string{"historical_anomaly"},
			"user:sample/sig-01": []string{"device_clone"},
		},
	}
}

func findingBody(t *testing.T, subjectRef string) map[string]any {
	t.Helper()
	return map[string]any{
		"schema_version": model.AuditUserFindingSchemaVersion,
		"subject_ref":    subjectRef,
		"behavior_profile": map[string]any{
			"usual_pattern":   []string{"该用户通常只有一条独立路由", "主要在工作日夜间活跃", "设备绑定质量良好"},
			"current_pattern": []string{"出现三条独立路由并发传输", "活跃时段与历史基本一致"},
			"key_changes":     []string{"独立路由峰值从 1 条上升至 3 条", "多路由重叠持续约 146 秒"},
		},
		"findings": []any{map[string]any{
			"finding_id": "finding-01",
			"title":      "同一凭据出现多路由并发",
			"severity":   "high",
			"observation": "过去 15 分钟出现 3 条独立路由重叠传输，重叠持续时间约 146 秒，" +
				"显著高于该用户过去 28 天同一时段的基线水平。",
			"baseline_comparison": map[string]any{
				"current": 3, "baseline_p95": 1, "threshold": 2, "duration_seconds": 146,
			},
			"interpretation":                "该行为明显偏离该用户历史模式，存在凭据跨独立网络使用的可能，需要人工核查。",
			"evidence_refs":                 []string{"ev-01", "ev-02"},
			"counter_evidence_refs":         []string{"ce-01"},
			"plausible_benign_explanations": []string{"多设备切换", "共享网络出口"},
			"verification_steps":            []string{"核对设备绑定记录", "检查重叠连接是否来自独立路由"},
		}},
		"counter_evidence": []string{"已确认并排除两次全节点测速事件", "数据覆盖率为 94%"},
		"data_gaps":        []string{},
	}
}

func validFindingJSON(t *testing.T, subjectRef string) string {
	t.Helper()
	content, err := json.Marshal(findingBody(t, subjectRef))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func validReportJSON(t *testing.T) string {
	t.Helper()
	content, err := json.Marshal(map[string]any{
		"schema_version": model.AuditReportSchemaVersion,
		"executive": map[string]any{
			"verdict": "high_risk", "risk_score": 78, "health_score": 22, "evidence_confidence": 0.84,
			"one_line_conclusion": "该用户当前行为明显偏离历史基线，建议优先进行人工核查。",
		},
		"behavior_profile": map[string]any{
			"usual_pattern":   []string{"平时单路由单设备活跃", "主要在工作日夜间连接"},
			"current_pattern": []string{"多路由并发，跨两个独立网络"},
			"key_changes":     []string{"独立路由峰值从 1 条增至 3 条"},
		},
		"findings": []any{map[string]any{
			"finding_id":  "finding-01",
			"title":       "同一凭据出现多路由并发",
			"severity":    "high",
			"observation": "过去 15 分钟出现 3 条独立路由重叠传输，重叠持续时间约 146 秒。",
			"baseline_comparison": map[string]any{
				"current": 3, "baseline_p95": 1, "threshold": 2, "duration_seconds": 146,
			},
			"interpretation":                "明显偏离该用户历史模式，需要进一步核查。",
			"evidence_refs":                 []string{"user:sample/ev-01", "user:sample/ev-02"},
			"counter_evidence_refs":         []string{"user:sample/ce-01"},
			"plausible_benign_explanations": []string{"多设备切换", "共享网络出口"},
			"verification_steps":            []string{"核对设备绑定记录", "检查重叠连接是否来自独立路由"},
		}},
		"timeline": []any{map[string]any{
			"timeline_id": "tl-01", "kind": "anomaly", "title": "多路由重叠",
			"detail": "重叠持续约 146 秒", "started_at": "2026-08-07T09:15:00Z", "ended_at": "2026-08-07T09:17:26Z",
			"evidence_refs": []string{"user:sample/ev-01"},
		}},
		"counter_evidence": []any{map[string]any{
			"counter_id": "ce-01", "text": "已排除两次全节点测速事件", "evidence_refs": []string{"user:sample/ce-01"},
		}},
		"recommended_actions": []any{map[string]any{
			"action": "request_manual_review", "reason": "需要人工核对设备绑定和凭据共享情况",
		}},
		"data_quality": map[string]any{
			"coverage": 0.94, "baseline_days": 24, "dropped_buckets": 1, "identity_quality": 0.9,
		},
		"data_gaps": []string{},
		"methodology": map[string]any{
			"feature_version":          1,
			"scoring_version":          model.AuditScoringVersion,
			"baseline_version":         model.AuditBaselineVersion,
			"evidence_schema_version":  model.AuditEvidenceSchemaVersion,
			"prompt_version":           model.AuditPromptReportVersion,
			"report_schema_version":    model.AuditReportSchemaVersion,
			"provider_profile_version": model.AuditProviderProfileVersion,
			"provider_grade":           model.AuditProviderGradeA,
			"structured_output":        model.AuditProviderStructuredJSONSchema,
			"output_mode":              model.AuditOutputModeStrictSchema,
			"model":                    "test-model",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func chatCompletion(body map[string]any, promptTokens, completionTokens int64) map[string]any {
	return map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]string{"content": mustJSON(body)},
			"finish_reason": "stop",
		}},
		"usage": map[string]int64{"prompt_tokens": promptTokens, "completion_tokens": completionTokens},
	}
}

func mustJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func TestRunOnceCompletesFindingWithoutReturningProviderCredential(t *testing.T) {
	const apiKey = "provider-secret-that-must-not-return"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("model authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), apiKey) {
			t.Fatal("provider credential leaked into model request body")
		}
		_ = json.NewEncoder(w).Encode(chatCompletion(findingBody(t, "user:1"), 120, 40))
	}))
	defer modelServer.Close()

	var mu sync.Mutex
	var completed airpc.CompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/lease":
			_ = json.NewEncoder(w).Encode(airpc.LeaseResponse{Job: &model.AuditReviewJob{ID: "job-1", ReviewID: "review-1", Kind: "finding", Input: findingJobInput(t, "user:1")}, Provider: &airpc.Provider{ID: "provider-1", BaseURL: modelServer.URL, Model: "test-model", APIKey: apiKey, Capability: aCapability("test-model")}})
		case "/v1/jobs/job-1/complete":
			mu.Lock()
			defer mu.Unlock()
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()

	client := controllerClient(t, controller.URL)
	if err := runOnce(context.Background(), client, "worker-1"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	encoded, _ := json.Marshal(completed)
	if strings.Contains(string(encoded), apiKey) {
		t.Fatal("provider credential leaked into completion RPC")
	}
	if completed.WorkerID != "worker-1" || completed.InputTokens != 120 || completed.OutputTokens != 40 || len(completed.Output) == 0 {
		t.Fatalf("unexpected completion: %#v", completed)
	}
	var finding model.AuditUserFinding
	if err := json.Unmarshal(completed.Output, &finding); err != nil {
		t.Fatalf("completion output is not a finding: %v", err)
	}
	if finding.SchemaVersion != model.AuditUserFindingSchemaVersion || finding.SubjectRef != "user:1" || len(finding.Findings) != 1 {
		t.Fatalf("unexpected finding: %#v", finding)
	}
	if len(finding.Findings[0].EvidenceRefs) != 2 || finding.Findings[0].EvidenceRefs[0] != "ev-01" {
		t.Fatalf("finding evidence refs = %#v", finding.Findings[0].EvidenceRefs)
	}
}

func TestRunOnceSupportsResponsesAPIFormat(t *testing.T) {
	const apiKey = "responses-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Method != http.MethodPost {
			t.Fatalf("responses request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("responses authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			Model       string                 `json:"model"`
			Temperature int                    `json:"temperature"`
			Input       []map[string]string    `json:"input"`
			Text        map[string]interface{} `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "responses-model" || request.Temperature != 0 || len(request.Input) != 2 || request.Input[1]["role"] != "user" {
			t.Fatalf("responses payload = %#v", request)
		}
		format, _ := request.Text["format"].(map[string]interface{})
		if format == nil || format["type"] != "json_schema" || format["strict"] != true {
			t.Fatalf("responses strict schema missing: %#v", request.Text)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": []any{map[string]any{
				"type":    "message",
				"content": []any{map[string]any{"type": "output_text", "text": validFindingJSON(t, "user:1")}},
			}},
			"usage": map[string]int64{"input_tokens": 200, "output_tokens": 60},
		})
	}))
	defer modelServer.Close()

	var completed airpc.CompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/lease":
			_ = json.NewEncoder(w).Encode(airpc.LeaseResponse{Job: &model.AuditReviewJob{ID: "job-responses", ReviewID: "review-responses", Kind: "finding", Input: findingJobInput(t, "user:1")}, Provider: &airpc.Provider{ID: "provider-2", BaseURL: modelServer.URL, Model: "responses-model", APIFormat: "responses", APIKey: apiKey, Capability: aCapability("responses-model")}})
		case "/v1/jobs/job-responses/complete":
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()

	if err := runOnce(context.Background(), controllerClient(t, controller.URL), "worker-responses"); err != nil {
		t.Fatal(err)
	}
	if completed.WorkerID != "worker-responses" || completed.InputTokens != 200 || completed.OutputTokens != 60 {
		t.Fatalf("unexpected completion: %#v", completed)
	}
}

func TestAnalyzeSurfacesProviderErrorMessageWithoutCredential(t *testing.T) {
	const apiKey = "error-body-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key error-body-secret","type":"authentication_error"}}`))
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "m", APIKey: apiKey, Capability: aCapability("m")}
	_, _, _, _, err := analyze(context.Background(), job, provider)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "invalid api key") || strings.Contains(err.Error(), apiKey) {
		t.Fatalf("provider error = %v", err)
	}
}

func TestModelErrorMessageSupportsTopLevelProviderError(t *testing.T) {
	message := modelErrorMessage([]byte(`{"type":"Router.Unavailable","modelID":"deepseek-v4-flash"}`), "")
	if message != "Router.Unavailable (model: deepseek-v4-flash)" {
		t.Fatalf("provider message = %q", message)
	}
}

func TestRunOnceReportsBoundedModelFailure(t *testing.T) {
	const apiKey = "cred-XK9q7z-credential"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream rejected the model request"}}`))
	}))
	defer modelServer.Close()
	failed := make(chan airpc.FailRequest, 1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/lease":
			_ = json.NewEncoder(w).Encode(airpc.LeaseResponse{Job: &model.AuditReviewJob{ID: "job-2", ReviewID: "review-2", Kind: "finding", Input: findingJobInput(t, "user:sample")}, Provider: &airpc.Provider{ID: "provider-1", BaseURL: modelServer.URL, Model: "test-model", APIKey: apiKey, Capability: aCapability("test-model")}})
		case "/v1/jobs/job-2/fail":
			var request airpc.FailRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			failed <- request
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	if err := runOnce(context.Background(), controllerClient(t, controller.URL), "worker-2"); err == nil {
		t.Fatal("model failure was not returned")
	}
	request := <-failed
	if request.WorkerID != "worker-2" || len(request.Error) > 1000 || strings.Contains(request.Error, apiKey) {
		t.Fatalf("unexpected failure RPC: %#v", request)
	}
	if len(request.ErrorDetail) == 0 || !strings.Contains(string(request.ErrorDetail), "upstream rejected the model request") || (!strings.Contains(string(request.ErrorDetail), "HTTP 502") && !strings.Contains(string(request.ErrorDetail), "502")) {
		t.Fatalf("failure detail = %s", request.ErrorDetail)
	}
	var logDetail map[string]any
	if err := json.Unmarshal(request.ErrorDetail, &logDetail); err != nil {
		t.Fatalf("failure detail is not JSON: %v", err)
	}
	if logDetail["request_method"] != "POST" || logDetail["request_url"] != modelServer.URL+"/chat/completions" || logDetail["status"] != float64(502) {
		t.Fatalf("failure detail fields = %#v", logDetail)
	}
	requestHeaders, ok := logDetail["request_headers"].(map[string]any)
	if !ok || requestHeaders["Authorization"] != "Bearer [redacted]" {
		t.Fatalf("failure request headers = %#v", logDetail["request_headers"])
	}
	encoded, _ := json.Marshal(logDetail)
	if strings.Contains(string(encoded), apiKey) {
		t.Fatal("provider credential leaked into failure detail")
	}
}

func TestRunModelDiscoveryOnceReturnsSortedModelsWithoutCredential(t *testing.T) {
	const apiKey = "model-list-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			t.Fatalf("model request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("model authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "z-model"}, map[string]string{"id": "a-model"}, map[string]string{"id": "z-model"}}})
	}))
	defer modelServer.Close()

	completed := make(chan airpc.ModelDiscoveryCompleteRequest, 1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/model-discovery/lease":
			_ = json.NewEncoder(w).Encode(airpc.ModelDiscoveryLeaseResponse{Request: &airpc.ModelDiscoveryRequest{ID: "discovery-1", BaseURL: modelServer.URL + "/v1", APIKey: apiKey}})
		case "/v1/model-discovery/discovery-1/complete":
			var request airpc.ModelDiscoveryCompleteRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			completed <- request
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	if err := runModelDiscoveryOnce(context.Background(), controllerClient(t, controller.URL), "worker-models"); err != nil {
		t.Fatal(err)
	}
	request := <-completed
	encoded, _ := json.Marshal(request)
	if strings.Contains(string(encoded), apiKey) {
		t.Fatal("provider credential leaked into model discovery callback")
	}
	if request.WorkerID != "worker-models" || len(request.Models) != 2 || request.Models[0] != "a-model" || request.Models[1] != "z-model" {
		t.Fatalf("unexpected completion: %#v", request)
	}
}

func TestDiscoverModelsRejectsRedirectAndMalformedResponse(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"id": "redirected"}}})
	}))
	defer redirectTarget.Close()
	redirectServer := httptest.NewServer(http.RedirectHandler(redirectTarget.URL, http.StatusFound))
	defer redirectServer.Close()
	if _, err := discoverModels(context.Background(), &airpc.ModelDiscoveryRequest{BaseURL: redirectServer.URL, APIKey: "secret"}); err == nil {
		t.Fatal("redirected model endpoint was accepted")
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":""}]}`))
	}))
	defer malformed.Close()
	if _, err := discoverModels(context.Background(), &airpc.ModelDiscoveryRequest{BaseURL: malformed.URL, APIKey: "secret"}); err == nil {
		t.Fatal("malformed model list was accepted")
	}
}

func TestRunAITestOnceCompletesWithGradeACapability(t *testing.T) {
	const apiKey = "ai-test-secret"
	var probeChecks int
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" || r.Method != http.MethodPost {
			t.Fatalf("test request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			t.Fatalf("test authorization = %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), apiKey) {
			t.Fatal("provider credential leaked into test request body")
		}
		var payload struct {
			ResponseFormat map[string]any `json:"response_format"`
			Temperature    *int           `json:"temperature"`
			MaxTokens      int64          `json:"max_tokens"`
			Messages       []any          `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ResponseFormat == nil {
			probeChecks++
			if payload.Temperature != nil {
				t.Fatal("config test payload must stay minimal")
			}
			if payload.MaxTokens != aiTestMaxTokens {
				t.Fatalf("probe max_tokens = %d", payload.MaxTokens)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "ok"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 1}})
			return
		}
		if payload.Temperature == nil || *payload.Temperature != 0 || payload.MaxTokens != auditContractMaxTokens || len(payload.Messages) != 2 {
			t.Fatalf("contract payload = %#v", payload)
		}
		_ = json.NewEncoder(w).Encode(chatCompletion(findingBody(t, "user:sample"), 90, 20))
	}))
	defer modelServer.Close()

	var completed airpc.AITestCompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ai-test/lease":
			_ = json.NewEncoder(w).Encode(airpc.AITestLeaseResponse{Request: &airpc.AITestRequest{ID: "test-1", BaseURL: modelServer.URL, APIKey: apiKey, Model: "test-model", APIFormat: "chat_completions"}})
		case "/v1/ai-test/test-1/complete":
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()

	if err := runAITestOnce(context.Background(), controllerClient(t, controller.URL), "worker-ai-test"); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(completed)
	if strings.Contains(string(encoded), apiKey) {
		t.Fatal("provider credential leaked into AI test completion RPC")
	}
	if probeChecks != 1 {
		t.Fatalf("probe requests = %d", probeChecks)
	}
	if completed.WorkerID != "worker-ai-test" || completed.StatusCode != 200 || completed.DurationMS < 0 {
		t.Fatalf("unexpected AI test completion: %#v", completed)
	}
	if completed.Capability == nil || completed.Capability.AuditGrade != model.AuditProviderGradeA || completed.Capability.OutputMode != model.AuditOutputModeStrictSchema || completed.Capability.SchemaSuccessRate != 1.0 || !completed.Capability.UsageSupported || !completed.Capability.FinishReasonSupported {
		t.Fatalf("unexpected capability: %#v", completed.Capability)
	}
	if !strings.Contains(completed.RequestJSON, `"messages"`) || !strings.Contains(completed.ResponseJSON, `"choices"`) || !strings.Contains(completed.ResponseJSON, "audit-user-finding-v1") {
		t.Fatalf("raw JSON missing: %#v", completed)
	}
}

func TestRunAITestOnceSupportsResponsesFormat(t *testing.T) {
	var probeChecks int
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Method != http.MethodPost {
			t.Fatalf("responses test request = %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Text            map[string]any `json:"text"`
			MaxOutputTokens int64          `json:"max_output_tokens"`
			Input           any            `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Text == nil {
			probeChecks++
			if payload.MaxOutputTokens != aiTestMaxTokens || payload.Input != "Reply with exactly one word: ok" {
				t.Fatalf("responses probe payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output_text": "ok", "usage": map[string]int{"input_tokens": 8, "output_tokens": 1}})
			return
		}
		if payload.MaxOutputTokens != auditContractMaxTokens {
			t.Fatalf("responses contract max_output_tokens = %d", payload.MaxOutputTokens)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": []any{map[string]any{
				"type":    "message",
				"content": []any{map[string]any{"type": "output_text", "text": validFindingJSON(t, "user:sample")}},
			}},
			"usage": map[string]int{"input_tokens": 200, "output_tokens": 60},
		})
	}))
	defer modelServer.Close()

	var completed airpc.AITestCompleteRequest
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ai-test/lease":
			_ = json.NewEncoder(w).Encode(airpc.AITestLeaseResponse{Request: &airpc.AITestRequest{ID: "test-responses", BaseURL: modelServer.URL, APIKey: "responses-key", Model: "responses-model", APIFormat: "responses"}})
		case "/v1/ai-test/test-responses/complete":
			if err := json.NewDecoder(r.Body).Decode(&completed); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	if err := runAITestOnce(context.Background(), controllerClient(t, controller.URL), "worker-ai-responses"); err != nil {
		t.Fatal(err)
	}
	if probeChecks != 1 {
		t.Fatalf("probe requests = %d", probeChecks)
	}
	if completed.WorkerID != "worker-ai-responses" || completed.StatusCode != 200 {
		t.Fatalf("unexpected AI test completion: %#v", completed)
	}
	if completed.Capability == nil || completed.Capability.AuditGrade != model.AuditProviderGradeA {
		t.Fatalf("unexpected capability: %#v", completed.Capability)
	}
}

func TestRunAITestOnceReportsHTTPErrorWithRedactedRawResponse(t *testing.T) {
	const apiKey = "error-body-secret"
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key error-body-secret","type":"authentication_error"}}`))
	}))
	defer modelServer.Close()

	failed := make(chan airpc.AITestFailRequest, 1)
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ai-test/lease":
			_ = json.NewEncoder(w).Encode(airpc.AITestLeaseResponse{Request: &airpc.AITestRequest{ID: "test-error", BaseURL: modelServer.URL, APIKey: apiKey, Model: "m"}})
		case "/v1/ai-test/test-error/fail":
			var request airpc.AITestFailRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			failed <- request
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer controller.Close()
	if err := runAITestOnce(context.Background(), controllerClient(t, controller.URL), "worker-ai-error"); err == nil {
		t.Fatal("model failure was not returned")
	}
	request := <-failed
	if request.WorkerID != "worker-ai-error" || request.StatusCode != 401 || request.DurationMS < 0 {
		t.Fatalf("unexpected AI test failure RPC: %#v", request)
	}
	if !strings.Contains(request.Error, "HTTP 401") || !strings.Contains(request.Error, "invalid api key") || strings.Contains(request.Error, apiKey) || len(request.Error) > 1000 {
		t.Fatalf("AI test failure error = %q", request.Error)
	}
	if strings.Contains(request.ResponseJSON, apiKey) || !strings.Contains(request.ResponseJSON, "[redacted]") || request.RequestJSON == "" {
		t.Fatalf("AI test failure raw JSON = %#v", request)
	}
}

func TestAITestContentRejectsEmptyOrTruncatedChatCompletion(t *testing.T) {
	if _, err := aiTestContent("chat_completions", []byte(`{"choices":[{"finish_reason":"length","message":{"content":"","reasoning_content":"reasoning"}}]}`)); err == nil || !strings.Contains(err.Error(), "no visible content") {
		t.Fatalf("empty truncated response error = %v", err)
	}
	if _, err := aiTestContent("chat_completions", []byte(`{"choices":[{"finish_reason":"length","message":{"content":"partial"}}]}`)); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("truncated response error = %v", err)
	}
	if content, err := aiTestContent("chat_completions", []byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`)); err != nil || content != "ok" {
		t.Fatalf("valid response = %q, %v", content, err)
	}
}

func TestProviderResponseContentSupportsOfficialResponsesShape(t *testing.T) {
	body := []byte(`{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":12,"output_tokens":3}}`)
	content, inputTokens, outputTokens, err := providerResponseContent("responses", body)
	if err != nil || content != "ok" || inputTokens != 12 || outputTokens != 3 {
		t.Fatalf("responses result = %q, %d, %d, %v", content, inputTokens, outputTokens, err)
	}
	if _, _, _, err := providerResponseContent("responses", []byte(`{"status":"incomplete","output":[]}`)); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("incomplete responses error = %v", err)
	}
}

func TestAnalyzeRejectsProviderWithoutCapability(t *testing.T) {
	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	provider := &airpc.Provider{BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "key"}
	if _, _, _, _, err := analyze(context.Background(), job, provider); err == nil || !strings.Contains(err.Error(), "审计就绪测试") {
		t.Fatalf("missing capability error = %v", err)
	}
}

func TestAnalyzeRejectsGradeCProvider(t *testing.T) {
	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	capability := bCapability("m")
	capability.AuditGrade = model.AuditProviderGradeC
	capability.StructuredOutput = model.AuditProviderStructuredNone
	capability.OutputMode = model.AuditOutputModeText
	provider := &airpc.Provider{BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "key", Capability: capability}
	if _, _, _, _, err := analyze(context.Background(), job, provider); err == nil || !strings.Contains(err.Error(), "审计就绪测试") {
		t.Fatalf("grade C error = %v", err)
	}
}

func TestAnalyzeRejectsTextOnlyOutputMode(t *testing.T) {
	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	capability := bCapability("m")
	capability.OutputMode = model.AuditOutputModeText
	provider := &airpc.Provider{BaseURL: "http://127.0.0.1:1", Model: "m", APIKey: "key", Capability: capability}
	if _, _, _, _, err := analyze(context.Background(), job, provider); err == nil || !strings.Contains(err.Error(), "文本输出") {
		t.Fatalf("text output error = %v", err)
	}
}

func TestAnalyzeFallsBackFromJSONSchemaToJSONObject(t *testing.T) {
	var mu sync.Mutex
	var requested []string
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ResponseFormat map[string]any `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		format := ""
		if payload.ResponseFormat != nil {
			format, _ = payload.ResponseFormat["type"].(string)
		}
		mu.Lock()
		requested = append(requested, format)
		mu.Unlock()
		if format == "json_schema" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This response_format type is unavailable now","type":"invalid_request_error"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(chatCompletion(findingBody(t, "user:sample"), 100, 30))
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "m", APIKey: "key", Capability: aCapability("m")}
	output, mode, inputTokens, outputTokens, err := analyze(context.Background(), job, provider)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(requested, ",") != "json_schema,json_object" {
		t.Fatalf("requested response formats = %#v", requested)
	}
	if mode != model.AuditOutputModeJSONObject || inputTokens != 100 || outputTokens != 30 {
		t.Fatalf("unexpected result mode/tokens: %s %d %d", mode, inputTokens, outputTokens)
	}
	var finding model.AuditUserFinding
	if err := json.Unmarshal(output, &finding); err != nil || finding.SubjectRef != "user:sample" {
		t.Fatalf("unexpected finding: %#v, %v", finding, err)
	}
}

func TestAnalyzeFallsBackFromRouterUnavailableOnStructuredOutput(t *testing.T) {
	var requested []string
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			ResponseFormat map[string]any `json:"response_format"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		format := ""
		if payload.ResponseFormat != nil {
			format, _ = payload.ResponseFormat["type"].(string)
		}
		requested = append(requested, format)
		if format == "json_schema" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"type":"Router.Unavailable","modelID":"m"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(chatCompletion(findingBody(t, "user:sample"), 70, 18))
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "m", APIKey: "key", Capability: aCapability("m")}
	output, mode, inputTokens, outputTokens, err := analyze(context.Background(), job, provider)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(requested, ",") != "json_schema,json_object" {
		t.Fatalf("requested response formats = %#v", requested)
	}
	if mode != model.AuditOutputModeJSONObject || inputTokens != 70 || outputTokens != 18 {
		t.Fatalf("unexpected result: %s %d %d", mode, inputTokens, outputTokens)
	}
	var finding model.AuditUserFinding
	if err := json.Unmarshal(output, &finding); err != nil || finding.SubjectRef != "user:sample" {
		t.Fatalf("unexpected finding: %#v, %v", finding, err)
	}
}

func TestAnalyzeResponsesStrictFallsBackToResponsesPlain(t *testing.T) {
	var mu sync.Mutex
	var strictRequests int
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var payload struct {
			Text map[string]any `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		if payload.Text != nil {
			strictRequests++
		}
		mu.Unlock()
		if payload.Text != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"This response_format type is unavailable now","type":"invalid_request_error"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": []any{map[string]any{
				"type":    "message",
				"content": []any{map[string]any{"type": "output_text", "text": validFindingJSON(t, "user:sample")}},
			}},
			"usage": map[string]int{"input_tokens": 80, "output_tokens": 15},
		})
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "m", APIFormat: "responses", APIKey: "key", Capability: aCapability("m")}
	output, mode, inputTokens, outputTokens, err := analyze(context.Background(), job, provider)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strictRequests != 1 {
		t.Fatalf("strict requests = %d", strictRequests)
	}
	if mode != model.AuditOutputModeJSONObject || inputTokens != 80 || outputTokens != 15 {
		t.Fatalf("unexpected result: %s %d %d", mode, inputTokens, outputTokens)
	}
	var finding model.AuditUserFinding
	if err := json.Unmarshal(output, &finding); err != nil || finding.SubjectRef != "user:sample" {
		t.Fatalf("unexpected finding: %#v, %v", finding, err)
	}
}

func TestAnalyzeResponsesNotFoundDoesNotFallBackToChat(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"message":"responses endpoint is not available"}}`))
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "m", APIFormat: "responses", APIKey: "key", Capability: aCapability("m")}
	_, _, _, _, err := analyze(context.Background(), job, provider)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
}

func TestAnalyzeJSONObjectRepairRound(t *testing.T) {
	var requests int
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		requests++
		if strings.Contains(string(body), "未通过本地校验") {
			_ = json.NewEncoder(w).Encode(chatCompletion(findingBody(t, "user:sample"), 110, 25))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message":       map[string]string{"content": `[1,2,3]`},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 95, "completion_tokens": 5},
		})
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "m", APIKey: "key", Capability: bCapability("m")}
	output, mode, inputTokens, outputTokens, err := analyze(context.Background(), job, provider)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if mode != model.AuditOutputModeJSONObject || inputTokens != 110 || outputTokens != 25 {
		t.Fatalf("unexpected result: %s %d %d", mode, inputTokens, outputTokens)
	}
	var finding model.AuditUserFinding
	if err := json.Unmarshal(output, &finding); err != nil || finding.SubjectRef != "user:sample" {
		t.Fatalf("repaired finding = %#v, %v", finding, err)
	}
}

func TestAnalyzeRejectsFindingWithInvalidSchema(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		body := findingBody(t, "user:sample")
		body["schema_version"] = "audit-user-finding-v0"
		_ = json.NewEncoder(w).Encode(chatCompletion(body, 60, 10))
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "finding", Input: findingJobInput(t, "user:sample")}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "m", APIKey: "key", Capability: aCapability("m")}
	if _, _, _, _, err := analyze(context.Background(), job, provider); err == nil || !strings.Contains(err.Error(), "Schema 版本无效") {
		t.Fatalf("invalid schema error = %v", err)
	}
}

func TestAnalyzeSynthesisProducesNormalizedReport(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(chatCompletion(map[string]any{
			"schema_version": model.AuditReportSchemaVersion,
			"executive": map[string]any{
				"verdict": "high_risk", "risk_score": 78, "health_score": 22, "evidence_confidence": 0.84,
				"one_line_conclusion": "该用户当前行为明显偏离历史基线，建议优先进行人工核查。",
			},
			"behavior_profile": map[string]any{
				"usual_pattern":   []string{"平时单路由单设备活跃"},
				"current_pattern": []string{"多路由并发"},
				"key_changes":     []string{"独立路由峰值从 1 条增至 3 条"},
			},
			"findings": []any{map[string]any{
				"finding_id": "finding-01", "title": "同一凭据出现多路由并发", "severity": "high",
				"observation":                   "过去 15 分钟出现 3 条独立路由重叠传输。",
				"baseline_comparison":           map[string]any{"current": 3, "baseline_p95": 1, "threshold": 2, "duration_seconds": 146},
				"interpretation":                "明显偏离该用户历史模式。",
				"evidence_refs":                 []string{"user:sample/ev-01", "user:sample/ev-02"},
				"counter_evidence_refs":         []string{"user:sample/ce-01"},
				"plausible_benign_explanations": []string{"多设备切换"},
				"verification_steps":            []string{"核对设备绑定记录"},
			}},
			"timeline": []any{},
			"counter_evidence": []any{map[string]any{
				"counter_id": "ce-01", "text": "已排除两次全节点测速事件",
			}},
			"recommended_actions": []any{map[string]any{
				"action": "request_manual_review", "reason": "需要人工核对设备绑定情况",
			}},
			"data_quality": map[string]any{"coverage": 0.94, "baseline_days": 24, "dropped_buckets": 1, "identity_quality": 0.9},
			"data_gaps":    []string{},
			"methodology": map[string]any{
				"feature_version": 1, "scoring_version": model.AuditScoringVersion, "baseline_version": model.AuditBaselineVersion,
				"evidence_schema_version": model.AuditEvidenceSchemaVersion, "prompt_version": model.AuditPromptReportVersion,
				"report_schema_version": model.AuditReportSchemaVersion, "provider_profile_version": model.AuditProviderProfileVersion,
				"provider_grade": model.AuditProviderGradeA, "structured_output": model.AuditProviderStructuredJSONSchema,
				"output_mode": model.AuditOutputModeStrictSchema, "model": "test-model",
			},
		}, 130, 40))
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "synthesis", Input: reportJobInput(t)}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "test-model", APIKey: "key", Capability: aCapability("test-model")}
	output, mode, inputTokens, outputTokens, err := analyze(context.Background(), job, provider)
	if err != nil {
		t.Fatal(err)
	}
	if mode != model.AuditOutputModeStrictSchema || inputTokens != 130 || outputTokens != 40 {
		t.Fatalf("unexpected result: %s %d %d", mode, inputTokens, outputTokens)
	}
	var report model.AuditReviewReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != model.AuditReportSchemaVersion || report.Executive.RiskScore != 78 || report.Executive.HealthScore != 22 || report.Executive.Verdict != "high_risk" {
		t.Fatalf("unexpected executive: %#v", report.Executive)
	}
	if report.Executive.EvidenceConfidence != 0.84 || report.Methodology.ProviderGrade != model.AuditProviderGradeA || report.Methodology.OutputMode != model.AuditOutputModeStrictSchema {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestAnalyzeRejectsEngineScoreTampering(t *testing.T) {
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		var output map[string]any
		if err := json.Unmarshal([]byte(validReportJSON(t)), &output); err != nil {
			t.Fatal(err)
		}
		executive := output["executive"].(map[string]any)
		executive["risk_score"] = 80
		output["executive"] = executive
		_ = json.NewEncoder(w).Encode(chatCompletion(output, 60, 10))
	}))
	defer modelServer.Close()

	job := &model.AuditReviewJob{Kind: "synthesis", Input: reportJobInput(t)}
	provider := &airpc.Provider{BaseURL: modelServer.URL, Model: "test-model", APIKey: "key", Capability: aCapability("test-model")}
	if _, _, _, _, err := analyze(context.Background(), job, provider); err == nil || !strings.Contains(err.Error(), "AI 修改了系统风险评分") {
		t.Fatalf("tampering error = %v", err)
	}
}

func TestContractOutputOK(t *testing.T) {
	if !contractOutputOK(validFindingJSON(t, "user:sample")) {
		t.Fatal("valid contract output was rejected")
	}
	if contractOutputOK(`{"schema_version":"audit-user-finding-v1","subject_ref":"user:sample","findings":[{}]}`) {
		t.Fatal("short contract output was accepted")
	}
	body := findingBody(t, "user:sample")
	body["subject_ref"] = "user:other"
	if contractOutputOK(mustJSON(body)) {
		t.Fatal("wrong subject contract output was accepted")
	}
	body = findingBody(t, "user:sample")
	body["findings"] = []any{}
	if contractOutputOK(mustJSON(body)) {
		t.Fatal("finding-less contract output was accepted")
	}
}

func TestAuditPayloadBoundsOutputAndIncludesSchema(t *testing.T) {
	systemPrompt := auditReportSystemPrompt()
	chat := chatAuditPayload("m", systemPrompt, "user input", "json_object", map[string]any{"type": "object"})
	if chat["max_tokens"] != auditMaxTokens {
		t.Fatalf("chat max_tokens = %#v", chat["max_tokens"])
	}
	messages, ok := chat["messages"].([]map[string]string)
	if !ok || len(messages) != 2 || !strings.Contains(messages[0]["content"], "Required JSON schema:") || !strings.Contains(messages[0]["content"], `"executive"`) {
		t.Fatalf("chat system prompt = %#v", chat["messages"])
	}
	format, ok := chat["response_format"].(map[string]any)
	if !ok || format["type"] != "json_object" {
		t.Fatalf("chat response_format = %#v", chat["response_format"])
	}
	strict := chatAuditPayload("m", systemPrompt, "user input", "json_schema", map[string]any{"type": "object"})
	if _, ok := strict["response_format"].(map[string]any)["json_schema"]; !ok {
		t.Fatalf("chat strict schema missing: %#v", strict["response_format"])
	}
	responses := responsesAuditPayload("m", systemPrompt, "user input", true, map[string]any{"type": "object"})
	if responses["max_output_tokens"] != auditMaxTokens {
		t.Fatalf("responses max_output_tokens = %#v", responses["max_output_tokens"])
	}
	text, ok := responses["text"].(map[string]any)
	if !ok {
		t.Fatalf("responses text missing: %#v", responses)
	}
	responseFormat, ok := text["format"].(map[string]any)
	if !ok || responseFormat["type"] != "json_schema" || responseFormat["strict"] != true {
		t.Fatalf("responses strict schema = %#v", text)
	}
	plain := responsesAuditPayload("m", systemPrompt, "user input", false, map[string]any{"type": "object"})
	if _, exists := plain["text"]; exists {
		t.Fatalf("responses plain payload must not include text: %#v", plain)
	}
}

func TestExtractJSONContent(t *testing.T) {
	raw, err := extractJSONContent(`{"a":1}`)
	if err != nil || string(raw) != `{"a":1}` {
		t.Fatalf("plain JSON = %s, %v", raw, err)
	}
	raw, err = extractJSONContent("```json\n{\"a\":1}\n```")
	if err != nil || string(raw) != `{"a":1}` {
		t.Fatalf("fenced JSON = %s, %v", raw, err)
	}
	raw, err = extractJSONContent(`prefix {"a":{"b":2}} suffix`)
	if err != nil || string(raw) != `{"a":{"b":2}}` {
		t.Fatalf("embedded JSON = %s, %v", raw, err)
	}
	if _, err = extractJSONContent("no json here"); err == nil {
		t.Fatal("missing JSON was accepted")
	}
	if _, err = extractJSONContent(`{"a":`); err == nil {
		t.Fatal("truncated JSON was accepted")
	}
}
