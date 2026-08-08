package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/auditcontract"
	"github.com/OboardProject/oboard/internal/model"
)

type aiTestOutcome struct {
	requestJSON, responseJSON string
	statusCode                int
	durationMS                int64
	content                   string
	capability                *model.AIProviderCapability
	err                       error
}

func runAITestLoop(ctx context.Context, runtime *workerRuntime, client *http.Client, workerID string, retryInterval time.Duration) {
	for {
		err := runAITestOnce(ctx, runtime, client, workerID)
		logLoopError("AI provider test", err)
		if ctx.Err() != nil {
			return
		}
		if err != nil && !sleepContext(ctx, retryInterval) {
			return
		}
	}
}

func runAITestOnce(ctx context.Context, runtime *workerRuntime, client *http.Client, workerID string) error {
	var lease airpc.AITestLeaseResponse
	if err := rpcJSON(ctx, client, http.MethodPost, "http://unix/v1/ai-test/lease", airpc.AITestLeaseRequest{WorkerID: workerID}, &lease); err != nil {
		return err
	}
	if lease.Request == nil {
		return nil
	}
	outcome := testProvider(ctx, runtime, lease.Request)
	callback, cancel := callbackContext()
	defer cancel()
	if outcome.capability != nil {
		detail := ""
		if outcome.err != nil {
			detail = bounded(outcome.err.Error(), 1000)
		}
		return rpcJSON(callback, client, http.MethodPost, "http://unix/v1/ai-test/"+lease.Request.ID+"/complete", airpc.AITestCompleteRequest{WorkerID: workerID, OK: outcome.err == nil, Error: detail, RequestJSON: outcome.requestJSON, ResponseJSON: outcome.responseJSON, StatusCode: outcome.statusCode, DurationMS: outcome.durationMS, Content: outcome.content, Capability: outcome.capability}, nil)
	}
	if outcome.err != nil {
		_ = rpcJSON(callback, client, http.MethodPost, "http://unix/v1/ai-test/"+lease.Request.ID+"/fail", airpc.AITestFailRequest{WorkerID: workerID, Error: bounded(outcome.err.Error(), 1000), RequestJSON: outcome.requestJSON, ResponseJSON: outcome.responseJSON, StatusCode: outcome.statusCode, DurationMS: outcome.durationMS}, nil)
		return outcome.err
	}
	return errors.New("AI provider test completed without a capability profile")
}

func testProvider(ctx context.Context, runtime *workerRuntime, test *airpc.AITestRequest) aiTestOutcome {
	outcome := aiTestOutcome{}
	if test == nil || strings.TrimSpace(test.Model) == "" {
		outcome.err = errors.New("invalid AI provider test request")
		return outcome
	}
	providerID := strings.TrimSpace(test.ProviderID)
	if providerID == "" {
		providerID = "draft:" + test.ID
	}
	endpoint := runtimeEndpoint(providerID, test.Endpoint, test.BaseURL, test.APIFormat, test.APIKey)
	if endpoint.ID == "" || endpoint.BaseURL == "" {
		outcome.err = errors.New("invalid AI provider endpoint")
		return outcome
	}
	provider := aiprovider.RuntimeProvider{ID: providerID, Name: test.Name, ProviderKind: test.ProviderKind, Model: test.Model, RoutingStrategy: "ordered_failover", Endpoints: []aiprovider.RuntimeEndpoint{endpoint}}
	schema := auditcontract.UserFindingSchema()
	request := aiprovider.Request{RequestID: test.ID, Model: test.Model, System: auditContractSystemPrompt(), Messages: []aiprovider.Message{{Role: "user", Content: contractSampleInput()}}, MaxOutputTokens: 4096, Schema: schema}
	strictSuccess := 0
	usageSupported := false
	finishSupported := false
	var last *aiprovider.Response
	started := time.Now()
	for range 2 {
		response, err := runtime.router.Complete(ctx, provider, request, false)
		if err != nil {
			outcome.err = err
			break
		}
		last = response
		if response.Structured != nil && aiprovider.ValidateJSONSchema(schema, response.Structured) == nil {
			strictSuccess++
		}
		usageSupported = usageSupported || response.Usage.InputTokens > 0 || response.Usage.OutputTokens > 0
		finishSupported = finishSupported || response.FinishReason != "unknown"
	}
	grade, structured, mode := model.AuditProviderGradeUnusable, model.AuditProviderStructuredNone, model.AuditOutputModeText
	notes := []string{}
	if strictSuccess == 2 {
		grade, structured, mode = model.AuditProviderGradeA, model.AuditProviderStructuredJSONSchema, model.AuditOutputModeStrictSchema
	} else {
		request.OutputMode = model.AuditOutputModeJSONObject
		objectSuccess := 0
		for range 2 {
			response, err := runtime.router.Complete(ctx, provider, request, false)
			if err != nil {
				continue
			}
			last = response
			raw := response.Structured
			if raw == nil {
				raw = extractJSON(response.Text)
			}
			if raw != nil && aiprovider.ValidateJSONSchema(schema, raw) == nil {
				objectSuccess++
			}
			usageSupported = usageSupported || response.Usage.InputTokens > 0 || response.Usage.OutputTokens > 0
			finishSupported = finishSupported || response.FinishReason != "unknown"
		}
		if objectSuccess == 2 {
			grade, structured, mode = model.AuditProviderGradeB, model.AuditProviderStructuredJSONObject, model.AuditOutputModeJSONObject
			outcome.err = nil
		} else if last != nil && strings.TrimSpace(last.Text) != "" {
			grade = model.AuditProviderGradeC
			outcome.err = nil
		}
	}
	lengthFinishSupported := false
	if grade != model.AuditProviderGradeUnusable {
		lengthRequest := aiprovider.Request{RequestID: test.ID + "-length", Model: test.Model, Messages: []aiprovider.Message{{Role: "user", Content: "输出至少 500 个英文单词，不要提前结束。"}}, MaxOutputTokens: 16}
		if response, err := runtime.router.Complete(ctx, provider, lengthRequest, false); err == nil {
			lengthFinishSupported = response.FinishReason == "length"
			usageSupported = usageSupported || response.Usage.InputTokens > 0 || response.Usage.OutputTokens > 0
		}
	}
	finishSupported = finishSupported && lengthFinishSupported
	if grade == model.AuditProviderGradeA && (!usageSupported || !finishSupported) {
		grade = model.AuditProviderGradeB
		notes = append(notes, "Usage 或长度截断 Finish Reason 不完整")
	}
	if strings.EqualFold(test.ProviderKind, "anthropic") && endpoint.APIStyle == aiprovider.APIStyleOpenAIChatCompletions && (grade == model.AuditProviderGradeA || mode == model.AuditOutputModeStrictSchema) {
		grade, structured, mode = model.AuditProviderGradeB, model.AuditProviderStructuredJSONObject, model.AuditOutputModeJSONObject
		notes = append(notes, "Anthropic OpenAI compatibility 不作为原生 strict schema 能力")
	}
	modelsSupported := false
	if models, err := runtime.router.ListModels(ctx, endpoint); err == nil && len(models) > 0 {
		modelsSupported = true
	} else {
		notes = append(notes, "模型发现不可用，可继续手工填写模型")
	}
	streaming := false
	streamRequest := aiprovider.Request{Model: test.Model, Messages: []aiprovider.Message{{Role: "user", Content: "Reply with ok"}}, MaxOutputTokens: 16, Stream: true}
	if client, err := runtime.routerClient(endpoint.APIStyle); err == nil {
		streamCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		streamErr := client.Stream(streamCtx, endpoint, streamRequest, func(aiprovider.StreamEvent) error { streaming = true; return nil })
		cancel()
		if streamErr == nil {
			streaming = true
		}
	}
	if grade == model.AuditProviderGradeUnusable && outcome.err == nil {
		outcome.err = errors.New("provider did not return a stable structured or text result")
	}
	connectivityOK := last != nil || modelsSupported || streaming
	authenticationOK := connectivityOK
	outcome.statusCode = 200
	if outcome.err != nil {
		providerErr := aiprovider.AsProviderError(outcome.err)
		outcome.statusCode = providerErr.HTTPStatus
		if providerErr.HTTPStatus > 0 {
			connectivityOK = true
			authenticationOK = providerErr.Kind != aiprovider.ErrorAuthFailed && providerErr.Kind != aiprovider.ErrorForbidden
		}
		notes = append(notes, "上游测试失败："+providerErr.Kind)
	}
	outcome.durationMS = time.Since(started).Milliseconds()
	outcome.requestJSON = `{"credential":"[redacted]","probe":"audit-schema-v2"}`
	summary, _ := json.Marshal(map[string]any{"grade": grade, "strict_schema_success": strictSuccess, "models_supported": modelsSupported, "usage_supported": usageSupported, "finish_reason_supported": finishSupported, "streaming_supported": streaming})
	outcome.responseJSON = string(summary)
	if last != nil {
		outcome.content = bounded(last.Text, 500)
	}
	outcome.capability = &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, ProviderID: providerID, EndpointID: endpoint.ID, APIStyle: string(endpoint.APIStyle), Model: test.Model, ConfigDigest: aiprovider.ConfigDigest(endpoint, test.Model), TestedAt: time.Now().UTC(), ConnectivityOK: connectivityOK, AuthenticationOK: authenticationOK, ModelsSupported: modelsSupported, AuditGrade: grade, StructuredOutput: structured, OutputMode: mode, SchemaSuccessRate: float64(strictSuccess) / 2, UsageSupported: usageSupported, FinishReasonSupported: finishSupported, StreamingSupported: streaming, ProviderRequestIDSupported: last != nil && last.ProviderRequestID != "", MaxVerifiedOutputTokens: 4096, LatencyMS: outcome.durationMS, Notes: notes}
	return outcome
}

func (r *workerRuntime) routerClient(style aiprovider.APIStyle) (aiprovider.Client, error) {
	return r.registry.Client(style)
}

func extractJSON(value string) json.RawMessage {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	if !json.Valid([]byte(trimmed)) {
		return nil
	}
	return json.RawMessage(trimmed)
}
func contractSampleInput() string {
	return "请基于证据生成结构化审计结果：" + contractSamplePack()
}
func contractSamplePack() string {
	return `{"schema_version":"audit-evidence-v2","mode":"single_user","subject":{"ref":"user:sample","identity_mode":"device_bound","policy_profile":"balanced"},"window":{"current":"2026-08-07T09:00:00Z/2026-08-07T10:00:00Z","comparisons":[]},"data_quality":{"coverage":1,"baseline_days":20,"dropped_buckets":0,"identity_quality":1,"data_completeness":1},"scores":{"connection_risk":10,"subscription_risk":10,"overall_risk":10,"health":90,"evidence_confidence":1,"caps":{}},"features":[],"signals":[],"counter_evidence":[],"timeline":[],"data_gaps":[],"methodology":{"feature_version":1,"scoring_version":"deterministic-v2","baseline_version":"feature-snapshot-v1","evidence_schema_version":"audit-evidence-v2","prompt_version":"audit-finding-v1","report_schema_version":"audit-report-v2","provider_profile_version":"provider-profile-v2"}}`
}
func auditContractSystemPrompt() string {
	return "你是 OBoard 审计分析器。只输出符合 JSON Schema 的对象，不得执行任何操作。"
}
