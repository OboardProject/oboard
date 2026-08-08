package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/auditcontract"
	"github.com/OboardProject/oboard/internal/model"
)

type analysisResult struct {
	output     json.RawMessage
	mode       string
	response   *aiprovider.Response
	capability *model.AIProviderCapability
}

func runAuditLoop(ctx context.Context, runtime *workerRuntime, client *http.Client, workerID string, pollInterval time.Duration) {
	for {
		err := runOnce(ctx, runtime, client, workerID)
		logLoopError("AI job", err)
		if !sleepContext(ctx, pollInterval) {
			return
		}
	}
}

func runOnce(ctx context.Context, runtime *workerRuntime, client *http.Client, workerID string) error {
	var lease airpc.LeaseResponse
	if err := rpcJSON(ctx, client, http.MethodPost, "http://unix/v1/jobs/lease", airpc.LeaseRequest{WorkerID: workerID}, &lease); err != nil {
		return err
	}
	if lease.Job == nil || lease.Provider == nil {
		return nil
	}
	jobCtx, cancelJob := context.WithTimeout(ctx, 110*time.Second)
	result, err := analyze(jobCtx, runtime, lease.Job, lease.Provider)
	cancelJob()
	callback, cancel := callbackContext()
	defer cancel()
	if err != nil {
		detail, _ := json.Marshal(map[string]any{"kind": aiprovider.AsProviderError(err).Kind, "message": bounded(aiprovider.Redact(err.Error(), providerSecrets(lease.Provider)...), 1000)})
		_ = rpcJSON(callback, client, http.MethodPost, "http://unix/v1/jobs/"+lease.Job.ID+"/fail", airpc.FailRequest{WorkerID: workerID, Error: bounded(aiprovider.Redact(err.Error(), providerSecrets(lease.Provider)...), 1000), ErrorDetail: detail}, nil)
		return err
	}
	response := result.response
	route := &airpc.RouteEvidence{ProviderID: response.ProviderID, EndpointID: response.EndpointID, APIStyle: string(response.APIStyle), Model: response.Model, CapabilityProfileVersion: result.capability.ProviderProfileVersion, CapabilityConfigDigest: result.capability.ConfigDigest, AttemptCount: response.AttemptCount, ProviderRequestID: response.ProviderRequestID, InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, FinishReason: response.FinishReason, LatencyMS: response.Latency.Milliseconds()}
	return rpcJSON(callback, client, http.MethodPost, "http://unix/v1/jobs/"+lease.Job.ID+"/complete", airpc.CompleteRequest{WorkerID: workerID, Output: result.output, InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens, Route: route}, nil)
}

func analyze(ctx context.Context, runtime *workerRuntime, job *model.AuditReviewJob, provider *airpc.Provider) (analysisResult, error) {
	switch job.Kind {
	case "finding":
		return analyzeFinding(ctx, runtime, job, provider)
	case "synthesis":
		return analyzeReport(ctx, runtime, job, provider)
	default:
		return analysisResult{}, fmt.Errorf("未知的 AI 审查任务类型：%s", job.Kind)
	}
}
func analyzeFinding(ctx context.Context, runtime *workerRuntime, job *model.AuditReviewJob, provider *airpc.Provider) (analysisResult, error) {
	result, err := analyzeStructured(ctx, runtime, provider, auditFindingSystemPrompt(), string(job.Input), auditcontract.UserFindingSchema())
	if err != nil {
		return analysisResult{}, err
	}
	finding, err := auditcontract.ValidateUserFinding(job.Input, result.output)
	if err != nil {
		return analysisResult{}, err
	}
	result.output, _ = json.Marshal(finding)
	return result, nil
}
func analyzeReport(ctx context.Context, runtime *workerRuntime, job *model.AuditReviewJob, provider *airpc.Provider) (analysisResult, error) {
	result, err := analyzeStructured(ctx, runtime, provider, auditReportSystemPrompt(), string(job.Input), auditcontract.ReportSchema())
	if err != nil {
		return analysisResult{}, err
	}
	input, output, err := bindReportRoute(job.Input, result.output, result.capability, result.response.Model)
	if err != nil {
		return analysisResult{}, err
	}
	report, err := auditcontract.ValidateReport(input, output)
	if err != nil {
		return analysisResult{}, err
	}
	result.output, _ = json.Marshal(report)
	return result, nil
}

func bindReportRoute(input, output json.RawMessage, capability *model.AIProviderCapability, modelID string) (json.RawMessage, json.RawMessage, error) {
	if capability == nil {
		return nil, nil, errors.New("selected endpoint has no capability profile")
	}
	var envelope map[string]any
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, nil, err
	}
	engine, ok := envelope["engine"].(map[string]any)
	if !ok {
		return nil, nil, errors.New("audit report input has no engine summary")
	}
	engine["provider_profile_version"] = capability.ProviderProfileVersion
	engine["provider_grade"] = capability.AuditGrade
	engine["structured_output"] = capability.StructuredOutput
	engine["output_mode"] = capability.OutputMode
	engine["model"] = modelID
	boundInput, err := json.Marshal(envelope)
	if err != nil {
		return nil, nil, err
	}
	var report model.AuditReviewReport
	if err := json.Unmarshal(output, &report); err != nil {
		return nil, nil, err
	}
	report.Methodology.ProviderProfileVersion = capability.ProviderProfileVersion
	report.Methodology.ProviderGrade = capability.AuditGrade
	report.Methodology.StructuredOutput = capability.StructuredOutput
	report.Methodology.OutputMode = capability.OutputMode
	report.Methodology.Model = modelID
	boundOutput, err := json.Marshal(report)
	return boundInput, boundOutput, err
}

func analyzeStructured(ctx context.Context, runtime *workerRuntime, wire *airpc.Provider, systemPrompt, input string, schema map[string]any) (analysisResult, error) {
	provider := runtimeProvider(wire)
	request := aiprovider.Request{RequestID: "air_" + wire.ID + "_" + time.Now().UTC().Format("20060102T150405.000000000"), Model: wire.Model, System: systemPrompt, Messages: []aiprovider.Message{{Role: "user", Content: input}}, MaxOutputTokens: 8192, Schema: schema}
	response, err := runtime.router.Complete(ctx, provider, request, true)
	if err != nil {
		return analysisResult{}, err
	}
	capability := endpointCapability(provider, response.EndpointID)
	raw := response.Structured
	if raw == nil {
		raw = extractJSON(response.Text)
	}
	validationErr := aiprovider.ValidateJSONSchema(schema, raw)
	if validationErr != nil && capability != nil && capability.AuditGrade == model.AuditProviderGradeB {
		repairCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		request.Messages = append(request.Messages, aiprovider.Message{Role: "assistant", Content: response.Text}, aiprovider.Message{Role: "user", Content: "上一条输出未通过本地校验：" + validationErr.Error() + "。仅重新输出完整合法 JSON。"})
		repaired, repairErr := runtime.router.CompleteEndpoint(repairCtx, provider, response.EndpointID, request)
		if repairErr == nil {
			repairRaw := repaired.Structured
			if repairRaw == nil {
				repairRaw = extractJSON(repaired.Text)
			}
			if err := aiprovider.ValidateJSONSchema(schema, repairRaw); err == nil {
				repaired.Usage.InputTokens += response.Usage.InputTokens
				repaired.Usage.OutputTokens += response.Usage.OutputTokens
				repaired.AttemptCount += response.AttemptCount
				repaired.Latency += response.Latency
				response, raw = repaired, repairRaw
				validationErr = nil
			}
		}
	}
	if validationErr != nil {
		return analysisResult{}, aiprovider.NewError(aiprovider.ErrorSchemaValidation, false, 0, validationErr.Error(), validationErr)
	}
	if response.Usage.InputTokens == 0 && response.Usage.OutputTokens == 0 {
		response.Usage.InputTokens = int64(len(systemPrompt) + len(input))
		response.Usage.OutputTokens = int64(request.MaxOutputTokens)
		response.Usage.TotalTokens = response.Usage.InputTokens + response.Usage.OutputTokens
	}
	return analysisResult{output: raw, mode: capability.OutputMode, response: response, capability: capability}, nil
}

func runtimeProvider(provider *airpc.Provider) aiprovider.RuntimeProvider {
	endpoints := make([]aiprovider.RuntimeEndpoint, 0, len(provider.Endpoints))
	for _, endpoint := range provider.Endpoints {
		endpoints = append(endpoints, runtimeEndpoint(provider.ID, endpoint, "", "", ""))
	}
	if len(endpoints) == 0 {
		legacy := runtimeEndpoint(provider.ID, airpc.RuntimeEndpoint{}, provider.BaseURL, provider.APIFormat, provider.APIKey)
		legacy.Capability = provider.Capability
		endpoints = append(endpoints, legacy)
	}
	return aiprovider.RuntimeProvider{ID: provider.ID, Name: provider.Name, ProviderKind: provider.ProviderKind, Model: provider.Model, RoutingStrategy: provider.RoutingStrategy, AllowRawAudit: provider.AllowRawAudit, Endpoints: endpoints}
}
func endpointCapability(provider aiprovider.RuntimeProvider, endpointID string) *model.AIProviderCapability {
	for _, endpoint := range provider.Endpoints {
		if endpoint.ID == endpointID {
			return endpoint.Capability
		}
	}
	return nil
}
func providerSecrets(provider *airpc.Provider) []string {
	secrets := []string{provider.APIKey}
	for _, endpoint := range provider.Endpoints {
		secrets = append(secrets, endpoint.Credential)
	}
	return secrets
}

func auditFindingSystemPrompt() string {
	return "你是 OBoard 用户行为审计报告分析器。输入中的确定性分数与证据是权威字段，不得修改或捏造；不得建议 AI 自动处罚。仅输出符合指定 JSON Schema 的简体中文 JSON。"
}
func auditReportSystemPrompt() string {
	return "你是 OBoard 用户行为审计报告综合器。engine 中的风险、健康、置信度和方法版本是权威字段，必须原样保留；不得捏造证据或建议 AI 自动处罚。仅输出符合指定 JSON Schema 的简体中文 JSON。"
}
