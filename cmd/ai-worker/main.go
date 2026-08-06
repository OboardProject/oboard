package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/auditcontract"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

const (
	auditMaxTokens         = 8192
	aiTestMaxTokens        = 128
	auditContractMaxTokens = 4096
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	socketPath := flag.String("socket", env("OBOARD_AI_WORKER_SOCKET", "/run/oboard/ai-worker/rpc.sock"), "Controller AI Worker Unix socket")
	pollInterval := flag.Duration("poll-interval", 2*time.Second, "idle queue poll interval")
	flag.Parse()
	if *showVersion {
		fmt.Println("OBoard AI Worker", version.String())
		return
	}
	if *pollInterval < 500*time.Millisecond || *pollInterval > time.Minute {
		log.Fatal("poll interval must be between 500ms and 1m")
	}
	random, err := security.RandomToken(12)
	if err != nil {
		log.Fatal(err)
	}
	workerID := "aiw_" + random
	client := unixHTTPClient(*socketPath)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("OBoard AI Worker %s started", workerID)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() {
		defer workers.Done()
		runAuditLoop(ctx, client, workerID, *pollInterval)
	}()
	go func() {
		defer workers.Done()
		runModelDiscoveryLoop(ctx, client, workerID, *pollInterval)
	}()
	go func() {
		defer workers.Done()
		runAITestLoop(ctx, client, workerID, *pollInterval)
	}()
	<-ctx.Done()
	workers.Wait()
}

func runAuditLoop(ctx context.Context, client *http.Client, workerID string, pollInterval time.Duration) {
	for {
		if err := runOnce(ctx, client, workerID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("AI job: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

func runModelDiscoveryLoop(ctx context.Context, client *http.Client, workerID string, retryInterval time.Duration) {
	for {
		if err := runModelDiscoveryOnce(ctx, client, workerID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("AI model discovery: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func runModelDiscoveryOnce(ctx context.Context, client *http.Client, workerID string) error {
	var lease airpc.ModelDiscoveryLeaseResponse
	if err := rpcJSON(ctx, client, http.MethodPost, "http://unix/v1/model-discovery/lease", airpc.ModelDiscoveryLeaseRequest{WorkerID: workerID}, &lease); err != nil {
		return err
	}
	if lease.Request == nil {
		return nil
	}
	models, err := discoverModels(ctx, lease.Request)
	if err != nil {
		failCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rpcJSON(failCtx, client, http.MethodPost, "http://unix/v1/model-discovery/"+lease.Request.ID+"/fail", airpc.ModelDiscoveryFailRequest{WorkerID: workerID, Error: bounded(err.Error(), 1000)}, nil)
		return err
	}
	completeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return rpcJSON(completeCtx, client, http.MethodPost, "http://unix/v1/model-discovery/"+lease.Request.ID+"/complete", airpc.ModelDiscoveryCompleteRequest{WorkerID: workerID, Models: models}, nil)
}

func discoverModels(ctx context.Context, discovery *airpc.ModelDiscoveryRequest) ([]string, error) {
	if discovery == nil || len(discovery.BaseURL) == 0 || len(discovery.BaseURL) > 2048 || len(discovery.APIKey) == 0 || len(discovery.APIKey) > 8192 {
		return nil, errors.New("invalid model discovery request")
	}
	endpoint := strings.TrimRight(discovery.BaseURL, "/") + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+discovery.APIKey)
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(responseBody) > 1<<20 {
		return nil, errors.New("model list response exceeds the allowed size")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, providerRequestError("model list endpoint", response.StatusCode, responseBody, discovery.APIKey)
	}
	var envelope struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(responseBody, &envelope) != nil || len(envelope.Data) == 0 || len(envelope.Data) > 1000 {
		return nil, errors.New("model list response is not supported")
	}
	unique := make(map[string]struct{}, len(envelope.Data))
	for _, item := range envelope.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" || len(modelID) > 512 {
			return nil, errors.New("model list contains an invalid model ID")
		}
		unique[modelID] = struct{}{}
	}
	models := make([]string, 0, len(unique))
	for modelID := range unique {
		models = append(models, modelID)
	}
	sort.Strings(models)
	return models, nil
}

type aiTestOutcome struct {
	requestJSON  string
	responseJSON string
	statusCode   int
	durationMS   int64
	content      string
	capability   *model.AIProviderCapability
	err          error
}

func runAITestLoop(ctx context.Context, client *http.Client, workerID string, retryInterval time.Duration) {
	for {
		if err := runAITestOnce(ctx, client, workerID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("AI provider test: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryInterval):
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func runAITestOnce(ctx context.Context, client *http.Client, workerID string) error {
	var lease airpc.AITestLeaseResponse
	if err := rpcJSON(ctx, client, http.MethodPost, "http://unix/v1/ai-test/lease", airpc.AITestLeaseRequest{WorkerID: workerID}, &lease); err != nil {
		return err
	}
	if lease.Request == nil {
		return nil
	}
	outcome := testProvider(ctx, lease.Request)
	if outcome.err != nil {
		failCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rpcJSON(failCtx, client, http.MethodPost, "http://unix/v1/ai-test/"+lease.Request.ID+"/fail", airpc.AITestFailRequest{WorkerID: workerID, Error: bounded(outcome.err.Error(), 1000), RequestJSON: outcome.requestJSON, ResponseJSON: outcome.responseJSON, StatusCode: outcome.statusCode, DurationMS: outcome.durationMS}, nil)
		return outcome.err
	}
	completeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return rpcJSON(completeCtx, client, http.MethodPost, "http://unix/v1/ai-test/"+lease.Request.ID+"/complete", airpc.AITestCompleteRequest{WorkerID: workerID, RequestJSON: outcome.requestJSON, ResponseJSON: outcome.responseJSON, StatusCode: outcome.statusCode, DurationMS: outcome.durationMS, Content: outcome.content, Capability: outcome.capability}, nil)
}

type contractRound struct {
	mode         string
	payload      map[string]any
	ok           bool
	content      string
	usage        bool
	finishReason bool
	outputTokens int64
	statusCode   int
	body         []byte
}

// testProvider runs the audit readiness test: a connectivity probe followed by
// contract rounds that verify structured output, Chinese report length,
// truncation detection, usage and stability. The result is a capability
// profile with grade A (strict schema), B (JSON object + local repair), C
// (text only, excluded from formal audits) or unusable.
func testProvider(ctx context.Context, test *airpc.AITestRequest) aiTestOutcome {
	outcome := aiTestOutcome{}
	if test == nil || len(test.BaseURL) == 0 || len(test.BaseURL) > 2048 || len(test.APIKey) == 0 || len(test.APIKey) > 8192 || len(test.Model) == 0 || len(test.Model) > 512 {
		outcome.err = errors.New("invalid AI provider test request")
		return outcome
	}
	format := normalizeProviderFormat(test.APIFormat)
	probe := probeProvider(ctx, format, test)
	outcome.statusCode = probe.statusCode
	outcome.durationMS = probe.durationMS
	outcome.requestJSON = probe.requestJSON
	outcome.responseJSON = probe.responseJSON
	outcome.content = probe.content
	if probe.err != nil {
		outcome.err = probe.err
		outcome.capability = capabilityProfile(test, model.AuditProviderGradeUnusable, model.AuditProviderStructuredNone, model.AuditOutputModeText, 0, false, false, 0, "连接探测失败")
		return outcome
	}
	contract := runContractRounds(ctx, format, test)
	outcome.requestJSON = contract.requestJSON
	outcome.responseJSON = contract.responseJSON
	outcome.statusCode = contract.statusCode
	outcome.durationMS = contract.durationMS
	outcome.content = contract.content
	grade, structured, outputMode, note := gradeContract(contract)
	outcome.capability = capabilityProfile(test, grade, structured, outputMode, contract.schemaSuccessRate, contract.usageSupported, contract.finishReasonSupported, int(contract.maxOutputTokens), note)
	if grade == model.AuditProviderGradeUnusable {
		outcome.err = errors.New("audit readiness test failed: " + note)
		return outcome
	}
	return outcome
}

func capabilityProfile(test *airpc.AITestRequest, grade, structured, outputMode string, schemaRate float64, usage, finishReason bool, maxOutputTokens int, note string) *model.AIProviderCapability {
	if maxOutputTokens < 1024 {
		maxOutputTokens = 1024
	}
	if maxOutputTokens > auditContractMaxTokens {
		maxOutputTokens = auditContractMaxTokens
	}
	return &model.AIProviderCapability{
		ProviderProfileVersion:  model.AuditProviderProfileVersion,
		Model:                   test.Model,
		TestedAt:                time.Now().UTC(),
		AuditGrade:              grade,
		StructuredOutput:        structured,
		OutputMode:              outputMode,
		SchemaSuccessRate:       schemaRate,
		UsageSupported:          usage,
		FinishReasonSupported:   finishReason,
		MaxVerifiedOutputTokens: maxOutputTokens,
		Note:                    note,
	}
}

type probeOutcome struct {
	requestJSON  string
	responseJSON string
	statusCode   int
	durationMS   int64
	content      string
	err          error
}

func probeProvider(ctx context.Context, format string, test *airpc.AITestRequest) probeOutcome {
	outcome := probeOutcome{}
	payload := aiTestPayload(format, test.Model)
	body, _ := json.Marshal(payload)
	outcome.requestJSON = compactJSON(body)
	endpoint := strings.TrimRight(test.BaseURL, "/") + providerFormatPath(format)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		outcome.err = err
		return outcome
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+test.APIKey)
	started := time.Now()
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	outcome.durationMS = time.Since(started).Milliseconds()
	if err != nil {
		outcome.err = err
		return outcome
	}
	defer response.Body.Close()
	outcome.statusCode = response.StatusCode
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if err != nil || len(responseBody) > 512<<10 {
		outcome.err = errors.New("model response exceeds the allowed size")
		return outcome
	}
	outcome.responseJSON = redactCredential(compactJSON(responseBody), test.APIKey)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail := modelErrorMessage(responseBody, test.APIKey)
		if detail != "" {
			outcome.err = fmt.Errorf("model endpoint returned HTTP %d: %s", response.StatusCode, detail)
		} else {
			outcome.err = fmt.Errorf("model endpoint returned HTTP %d", response.StatusCode)
		}
		return outcome
	}
	content, contentErr := aiTestContent(format, responseBody)
	if contentErr != nil {
		outcome.err = contentErr
		return outcome
	}
	outcome.content = bounded(strings.TrimSpace(content), 500)
	return outcome
}

type contractOutcome struct {
	requestJSON           string
	responseJSON          string
	statusCode            int
	durationMS            int64
	content               string
	schemaSuccessRate     float64
	schemaRounds          int
	schemaSuccess         int
	objectRounds          int
	objectSuccess         int
	textOK                bool
	usageSupported        bool
	finishReasonSupported bool
	maxOutputTokens       int64
}

func runContractRounds(ctx context.Context, format string, test *airpc.AITestRequest) contractOutcome {
	schemaRounds := 0
	schemaSuccess := 0
	objectRounds := 0
	objectSuccess := 0
	textOK := false
	usage := false
	finishReason := false
	var maxOutput int64
	var lastRequestJSON, lastResponseJSON string
	lastStatus := 0
	var lastDurationMS int64
	var lastContent string
	runRound := func(mode string, payload map[string]any) (ok bool, outputTokens int64, hasUsage, hasFinish bool, content string, body []byte, status int) {
		bodyBytes, _ := json.Marshal(payload)
		endpoint := strings.TrimRight(test.BaseURL, "/") + providerFormatPath(modeFormat(mode))
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
		if err != nil {
			return false, 0, false, false, "", nil, 0
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+test.APIKey)
		started := time.Now()
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		duration := time.Since(started).Milliseconds()
		lastDurationMS = duration
		lastRequestJSON = compactJSON(bodyBytes)
		lastStatus = 0
		lastContent = ""
		if err != nil {
			return false, 0, false, false, "", nil, 0
		}
		defer response.Body.Close()
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
		lastResponseJSON = redactCredential(compactJSON(responseBody), test.APIKey)
		lastStatus = response.StatusCode
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return false, 0, false, false, "", responseBody, response.StatusCode
		}
		contentText, inputTokens, outputTokens, contentErr := providerResponseContent(modeFormat(mode), responseBody)
		hasFinish = finishReasonDetected(modeFormat(mode), responseBody)
		lastContent = bounded(strings.TrimSpace(contentText), 500)
		if contentErr != nil {
			return false, 0, false, hasFinish, "", responseBody, response.StatusCode
		}
		hasUsage = inputTokens > 0 || outputTokens > 0
		ok = contractOutputOK(contentText)
		return ok, outputTokens, hasUsage, hasFinish, contentText, responseBody, response.StatusCode
	}
	recordRound := func(mode string, ok bool, outputTokens int64, hasUsage, hasFinish bool) {
		switch mode {
		case "json_schema", "responses_schema":
			schemaRounds++
			if ok {
				schemaSuccess++
			}
		case "json_object", "responses_plain":
			objectRounds++
			if ok {
				objectSuccess++
			}
		case "text":
			textOK = ok
		}
		if hasUsage {
			usage = true
		}
		if hasFinish {
			finishReason = true
		}
		if outputTokens > maxOutput {
			maxOutput = outputTokens
		}
	}
	contractPayload := func(format, mode string) map[string]any {
		return auditContractPayload(format, mode, test.Model)
	}
	// Two rounds each for structured modes; stop as soon as a mode is stable.
	for _, mode := range []string{contractSchemaMode(format), contractObjectMode(format)} {
		for round := 0; round < 2; round++ {
			ok, outputTokens, hasUsage, hasFinish, _, _, _ := runRound(mode, contractPayload(format, mode))
			recordRound(mode, ok, outputTokens, hasUsage, hasFinish)
		}
		if modeSuccess(schemaRounds, schemaSuccess) && mode == contractSchemaMode(format) {
			break
		}
		if modeSuccess(objectRounds, objectSuccess) && mode == contractObjectMode(format) {
			break
		}
	}
	if !modeSuccess(schemaRounds, schemaSuccess) && !modeSuccess(objectRounds, objectSuccess) {
		ok, outputTokens, hasUsage, hasFinish, _, _, _ := runRound("text", contractPayload(format, "text"))
		recordRound("text", ok, outputTokens, hasUsage, hasFinish)
	}
	schemaRate := 0.0
	if schemaRounds > 0 {
		schemaRate = float64(schemaSuccess) / float64(schemaRounds)
	}
	return contractOutcome{requestJSON: lastRequestJSON, responseJSON: lastResponseJSON, statusCode: lastStatus, durationMS: lastDurationMS, content: lastContent, schemaSuccessRate: schemaRate, schemaRounds: schemaRounds, schemaSuccess: schemaSuccess, objectRounds: objectRounds, objectSuccess: objectSuccess, textOK: textOK, usageSupported: usage, finishReasonSupported: finishReason, maxOutputTokens: maxOutput}
}

func modeSuccess(rounds, success int) bool {
	return rounds >= 2 && success == rounds
}

func modeFormat(mode string) string {
	switch mode {
	case "responses_schema", "responses_plain":
		return "responses"
	default:
		return "chat_completions"
	}
}

func contractSchemaMode(format string) string {
	if format == "responses" {
		return "responses_schema"
	}
	return "json_schema"
}

func contractObjectMode(format string) string {
	if format == "responses" {
		return "responses_plain"
	}
	return "json_object"
}

func finishReasonDetected(format string, responseBody []byte) bool {
	if format == "responses" {
		var envelope struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(responseBody, &envelope) == nil {
			return strings.EqualFold(strings.TrimSpace(envelope.Status), "completed")
		}
		return false
	}
	var envelope struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(responseBody, &envelope) != nil || len(envelope.Choices) != 1 {
		return false
	}
	return strings.TrimSpace(envelope.Choices[0].FinishReason) != ""
}

func contractOutputOK(content string) bool {
	raw, err := extractJSONContent(content)
	if err != nil {
		return false
	}
	var object struct {
		SchemaVersion string `json:"schema_version"`
		SubjectRef    string `json:"subject_ref"`
		Findings      []any  `json:"findings"`
	}
	if json.Unmarshal(raw, &object) != nil || object.SchemaVersion != model.AuditUserFindingSchemaVersion || object.SubjectRef != "user:sample" || len(object.Findings) == 0 {
		return false
	}
	return len([]rune(string(raw))) >= 400
}

func auditContractPayload(format, mode, modelID string) map[string]any {
	userInput := contractSampleInput()
	if format == "responses" {
		payload := map[string]any{
			"model":             modelID,
			"temperature":       0,
			"max_output_tokens": auditContractMaxTokens,
			"input": []map[string]string{
				{"role": "system", "content": auditContractSystemPrompt()},
				{"role": "user", "content": userInput},
			},
		}
		if mode == "responses_schema" {
			schema, _ := json.Marshal(auditcontract.UserFindingSchema())
			payload["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "oboard_audit_user_finding", "strict": true, "schema": json.RawMessage(schema)}}
		}
		return payload
	}
	payload := map[string]any{
		"model":       modelID,
		"temperature": 0,
		"max_tokens":  auditContractMaxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": auditContractSystemPrompt()},
			{"role": "user", "content": userInput},
		},
	}
	switch mode {
	case "json_schema":
		schema, _ := json.Marshal(auditcontract.UserFindingSchema())
		payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "oboard_audit_user_finding", "strict": true, "schema": json.RawMessage(schema)}}
	case "json_object":
		payload["response_format"] = map[string]any{"type": "json_object"}
	}
	return payload
}

func contractSampleInput() string {
	return `请作为 OBoard 用户行为审计分析器，基于下面的证据包生成一条结构化 Finding。
证据包：
` + contractSamplePack()
}

func contractSamplePack() string {
	return `{"schema_version":"audit-evidence-v2","mode":"single_user","subject":{"ref":"user:sample","identity_mode":"device_bound","policy_profile":"balanced"},"window":{"current":"2026-08-07T09:00:00Z/2026-08-07T10:00:00Z","comparisons":["same_time_slot_28d"]},"data_quality":{"coverage":0.94,"baseline_days":24,"dropped_buckets":1,"identity_quality":0.9,"data_completeness":0.97},"scores":{"connection_risk":78,"subscription_risk":22,"overall_risk":78,"health":22,"evidence_confidence":0.84,"caps":{"anomaly":0.6,"device_clone":0.65,"normal":1,"high_risk":0.7}},"features":[{"evidence_id":"ev-01","metric":"concurrent_route_count","value":3,"unit":"routes","window":"90s","threshold":2,"severity":"high","source":"connection","category":"device_clone"},{"evidence_id":"ev-02","metric":"robust_z","value":7.2,"unit":"z","window":"28d-same-slot","threshold":6,"severity":"medium","source":"connection","category":"historical_anomaly"}],"signals":[{"signal_id":"sig-01","kind":"device_clone","severity":"high","duration_seconds":146,"evidence_refs":["ev-01"],"confidence":0.84,"text":"同一设备凭证在 3 条独立网络上重叠传输 146 秒"}],"counter_evidence":[{"ref":"ce-01","kind":"engine","text":"已确认并排除 2 次全节点测速","scope":"engine:connection"}],"timeline":[],"data_gaps":[],"methodology":{"feature_version":1,"scoring_version":"deterministic-v2","baseline_version":"feature-snapshot-v1","evidence_schema_version":"audit-evidence-v2","prompt_version":"audit-finding-v1","report_schema_version":"audit-report-v2","provider_profile_version":"provider-profile-v1"}}`
}

func auditContractSystemPrompt() string {
	return "你是 OBoard 用户行为审计分析器。输入中的分数与置信度是权威字段，不得修改。每个 Finding 必须引用至少一个证据 ID；高严重度 Finding 必须引用两个独立证据类别或标记 needs_verification=true。使用简体中文输出至少 60 个汉字的可读说明。仅输出符合指定 JSON Schema 的 JSON。"
}

func gradeContract(contract contractOutcome) (grade, structured, outputMode, note string) {
	schemaStable := contract.schemaSuccessRate >= 0.99 && contract.schemaSuccessRate > 0
	objectStable := !schemaStable
	_ = objectStable
	switch {
	case schemaStable:
		if contract.usageSupported && contract.finishReasonSupported {
			return model.AuditProviderGradeA, model.AuditProviderStructuredJSONSchema, model.AuditOutputModeStrictSchema, "严格 JSON Schema 多次稳定通过"
		}
		return model.AuditProviderGradeB, model.AuditProviderStructuredJSONSchema, model.AuditOutputModeStrictSchema, "严格 Schema 通过但 Usage/停止原因不完整，按 B 级处理"
	case contract.objectRounds >= 2 && contract.objectSuccess == contract.objectRounds:
		return model.AuditProviderGradeB, model.AuditProviderStructuredJSONObject, model.AuditOutputModeJSONObject, "JSON Object 输出稳定，本地校验后可用"
	case contract.textOK:
		return model.AuditProviderGradeC, model.AuditProviderStructuredNone, model.AuditOutputModeText, "仅支持文本输出，不能用于正式审计"
	default:
		return model.AuditProviderGradeUnusable, model.AuditProviderStructuredNone, model.AuditOutputModeText, "无法稳定返回结构化结果"
	}
}

func aiTestPayload(format, modelID string) map[string]any {
	if format == "responses" {
		return map[string]any{"model": modelID, "input": "Reply with exactly one word: ok", "max_output_tokens": aiTestMaxTokens}
	}
	return map[string]any{"model": modelID, "messages": []map[string]string{{"role": "user", "content": "Reply with exactly one word: ok"}}, "max_tokens": aiTestMaxTokens}
}

func aiTestContent(format string, responseBody []byte) (string, error) {
	if format == "responses" {
		_, content, err := decodeResponsesResult(responseBody)
		return content, err
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(responseBody, &envelope) != nil || len(envelope.Choices) != 1 {
		return "", errors.New("model response is not a supported chat completion")
	}
	choice := envelope.Choices[0]
	if strings.TrimSpace(choice.Message.Content) == "" {
		if choice.FinishReason == "length" {
			return "", errors.New("model response contains no visible content because the output limit was exhausted")
		}
		return "", errors.New("model response contains no visible content")
	}
	if choice.FinishReason == "length" {
		return "", errors.New("model response was truncated by the output limit")
	}
	return choice.Message.Content, nil
}

func compactJSON(raw []byte) string {
	var value any
	if json.Unmarshal(raw, &value) == nil {
		if compact, err := json.Marshal(value); err == nil {
			return string(compact)
		}
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r", " "), "\n", " ")), " ")
}

func redactCredential(value, credential string) string {
	if credential != "" && strings.Contains(value, credential) {
		return strings.ReplaceAll(value, credential, "[redacted]")
	}
	return value
}

func runOnce(ctx context.Context, client *http.Client, workerID string) error {
	var lease airpc.LeaseResponse
	if err := rpcJSON(ctx, client, http.MethodPost, "http://unix/v1/jobs/lease", airpc.LeaseRequest{WorkerID: workerID}, &lease); err != nil {
		return err
	}
	if lease.Job == nil || lease.Provider == nil {
		return nil
	}
	output, _, inputTokens, outputTokens, err := analyze(ctx, lease.Job, lease.Provider)
	if err != nil {
		failCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		failure := airpc.FailRequest{WorkerID: workerID, Error: bounded(err.Error(), 1000)}
		var detail *providerFailureError
		if errors.As(err, &detail) {
			failure.ErrorDetail = detail.detail
		}
		_ = rpcJSON(failCtx, client, http.MethodPost, "http://unix/v1/jobs/"+lease.Job.ID+"/fail", failure, nil)
		return err
	}
	request := airpc.CompleteRequest{WorkerID: workerID, Output: output, InputTokens: inputTokens, OutputTokens: outputTokens}
	return rpcJSON(ctx, client, http.MethodPost, "http://unix/v1/jobs/"+lease.Job.ID+"/complete", request, nil)
}

// analyze dispatches stage-0 finding extraction and stage-1 report synthesis.
// Both stages enforce the audit contract locally; plain-text output is never
// accepted for formal audits.
func analyze(ctx context.Context, job *model.AuditReviewJob, provider *airpc.Provider) (json.RawMessage, string, int64, int64, error) {
	switch job.Kind {
	case "finding":
		return analyzeFinding(ctx, job, provider)
	case "synthesis":
		return analyzeReport(ctx, job, provider)
	default:
		return nil, "", 0, 0, fmt.Errorf("未知的 AI 审查任务类型：%s", job.Kind)
	}
}

func analyzeFinding(ctx context.Context, job *model.AuditReviewJob, provider *airpc.Provider) (json.RawMessage, string, int64, int64, error) {
	output, mode, inputTokens, outputTokens, err := analyzeStructured(ctx, provider, auditFindingSystemPrompt(), string(job.Input), auditcontract.UserFindingSchema())
	if err != nil {
		return nil, "", 0, 0, err
	}
	finding, err := auditcontract.ValidateUserFinding(job.Input, output)
	if err != nil {
		return nil, "", 0, 0, err
	}
	normalized, _ := json.Marshal(finding)
	return normalized, mode, inputTokens, outputTokens, nil
}

func analyzeReport(ctx context.Context, job *model.AuditReviewJob, provider *airpc.Provider) (json.RawMessage, string, int64, int64, error) {
	output, mode, inputTokens, outputTokens, err := analyzeStructured(ctx, provider, auditReportSystemPrompt(), string(job.Input), auditcontract.ReportSchema())
	if err != nil {
		return nil, "", 0, 0, err
	}
	report, err := auditcontract.ValidateReport(job.Input, output)
	if err != nil {
		return nil, "", 0, 0, err
	}
	normalized, _ := json.Marshal(report)
	return normalized, mode, inputTokens, outputTokens, nil
}

type auditAttempt struct {
	format     string
	payload    map[string]any
	structured bool
	mode       string
}

// structuredAttempts builds the bounded fallback chain for a formal audit job.
// Text-only output is never part of the chain: grade C or missing capability
// fails immediately with an actionable error.
func structuredAttempts(format string, provider *airpc.Provider, systemPrompt, input string, schema map[string]any) ([]auditAttempt, error) {
	capability := provider.Capability
	if capability == nil || (capability.AuditGrade != model.AuditProviderGradeA && capability.AuditGrade != model.AuditProviderGradeB) {
		return nil, errors.New("provider 未通过审计就绪测试（需要 A/B 级能力），无法执行正式审计")
	}
	if capability.StructuredOutput == model.AuditProviderStructuredNone || capability.OutputMode == model.AuditOutputModeText {
		return nil, errors.New("provider 仅支持文本输出，正式审计已拒绝（不会静默降级）")
	}
	attempts := []auditAttempt{}
	if format == "responses" {
		if capability.OutputMode == model.AuditOutputModeStrictSchema {
			attempts = append(attempts, auditAttempt{format: "responses", payload: responsesAuditPayload(provider.Model, systemPrompt, input, true, schema), structured: true, mode: model.AuditOutputModeStrictSchema})
		}
		attempts = append(attempts, auditAttempt{format: "responses", payload: responsesAuditPayload(provider.Model, systemPrompt, input, false, schema), structured: true, mode: model.AuditOutputModeJSONObject})
	}
	if capability.OutputMode == model.AuditOutputModeStrictSchema {
		attempts = append(attempts, auditAttempt{format: "chat_completions", payload: chatAuditPayload(provider.Model, systemPrompt, input, "json_schema", schema), structured: true, mode: model.AuditOutputModeStrictSchema})
	}
	attempts = append(attempts, auditAttempt{format: "chat_completions", payload: chatAuditPayload(provider.Model, systemPrompt, input, "json_object", schema), structured: true, mode: model.AuditOutputModeJSONObject})
	return attempts, nil
}

// analyzeStructured runs the structured attempt chain with local validation and
// at most one repair round for JSON-object mode. Only transient errors retry.
func analyzeStructured(ctx context.Context, provider *airpc.Provider, systemPrompt, input string, schema map[string]any) (json.RawMessage, string, int64, int64, error) {
	format := normalizeProviderFormat(provider.APIFormat)
	baseURL := strings.TrimRight(provider.BaseURL, "/")
	attempts, err := structuredAttempts(format, provider, systemPrompt, input, schema)
	if err != nil {
		return nil, "", 0, 0, err
	}
	var lastErr error
	repaired := false
	for _, attempt := range attempts {
		body, _ := json.Marshal(attempt.payload)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+providerFormatPath(attempt.format), bytes.NewReader(body))
		if err != nil {
			return nil, "", 0, 0, err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+provider.APIKey)
		requestHeaders := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer [redacted]"}
		client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			return nil, "", 0, 0, err
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
		_ = response.Body.Close()
		if readErr != nil || len(responseBody) > 1<<20 {
			return nil, "", 0, 0, errors.New("model response exceeds the allowed size")
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			logDetail := providerLog(provider, request.Method, request.URL.String(), requestHeaders, body, responseBody, response)
			lastErr = providerRequestError("model endpoint", response.StatusCode, responseBody, provider.APIKey, logDetail)
			transient := response.StatusCode == 429 || response.StatusCode == 408 || response.StatusCode >= 500
			if !transient && !(attempt.structured && auditRetryable(lastErr)) && !(attempt.structured && providerRouteUnavailable(responseBody)) {
				return nil, "", 0, 0, lastErr
			}
			continue
		}
		content, inputTokens, outputTokens, contentErr := providerResponseContent(attempt.format, responseBody)
		if contentErr != nil {
			lastErr = contentErr
			if attempt.format == "responses" && attempt.mode == model.AuditOutputModeStrictSchema {
				continue
			}
			return nil, "", 0, 0, contentErr
		}
		raw, extractErr := extractJSONContent(content)
		if extractErr != nil {
			return nil, "", 0, 0, extractErr
		}
		validatedRaw, repairedInputTokens, repairedOutputTokens, validationErr := validateStructuredOutput(attempt.mode, raw, schema, systemPrompt, input, provider, baseURL, attempt.format, &repaired)
		if validationErr != nil {
			return nil, "", 0, 0, validationErr
		}
		if repairedInputTokens != 0 || repairedOutputTokens != 0 {
			inputTokens, outputTokens = repairedInputTokens, repairedOutputTokens
		}
		return validatedRaw, attempt.mode, inputTokens, outputTokens, nil
	}
	if lastErr == nil {
		lastErr = errors.New("model endpoint returned no usable response")
	}
	return nil, "", 0, 0, lastErr
}

// validateStructuredOutput runs the schema-specific local validation and one
// bounded repair round for JSON-object mode. Strict schema mode trusts the
// provider guarantee and never repairs.
func validateStructuredOutput(mode string, raw json.RawMessage, schema map[string]any, systemPrompt, input string, provider *airpc.Provider, baseURL, format string, repaired *bool) (json.RawMessage, int64, int64, error) {
	validationErr := decodeStrictSchema(mode, raw)
	if validationErr == nil || mode == model.AuditOutputModeStrictSchema {
		return raw, 0, 0, validationErr
	}
	if *repaired {
		return raw, 0, 0, validationErr
	}
	*repaired = true
	repairInput := "你的上一条回复未通过本地校验：" + validationErr.Error() + "。请重新输出完整、合法且符合 Schema 的 JSON。"
	repairPayload := chatAuditPayload(provider.Model, systemPrompt, input+"\n\n"+repairInput, "json_object", schema)
	body, _ := json.Marshal(repairPayload)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+providerFormatPath("chat_completions"), bytes.NewReader(body))
	if err != nil {
		return raw, 0, 0, validationErr
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+provider.APIKey)
	client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return raw, 0, 0, validationErr
	}
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return raw, 0, 0, validationErr
	}
	content, repairedInputTokens, repairedOutputTokens, contentErr := providerResponseContent("chat_completions", responseBody)
	if contentErr != nil {
		return raw, 0, 0, validationErr
	}
	repairedRaw, extractErr := extractJSONContent(content)
	if extractErr != nil {
		return raw, 0, 0, validationErr
	}
	return repairedRaw, repairedInputTokens, repairedOutputTokens, decodeStrictSchema(mode, repairedRaw)
}

func decodeStrictSchema(mode string, raw json.RawMessage) error {
	switch mode {
	case model.AuditOutputModeStrictSchema:
		var object map[string]any
		if json.Unmarshal(raw, &object) != nil {
			return errors.New("model report is not valid JSON")
		}
		return nil
	default:
		var object map[string]any
		if json.Unmarshal(raw, &object) != nil {
			return errors.New("model report is not valid JSON")
		}
		return nil
	}
}

func auditFindingSystemPrompt() string {
	prompt := "你是 OBoard 用户行为审计报告分析器，不是翻译器，也不是独立规则引擎。\n" +
		"输入中的风险分数、健康分数、证据置信度、数据质量、信号严重程度由确定性审计引擎生成，是权威字段。你不得重新计算、修改或替换这些数值，也不得在输出中复述或改写它们。\n" +
		"你的任务（针对输入中的单个用户）：\n" +
		"1. 描述该用户的历史正常行为模式（usual_pattern）。\n" +
		"2. 描述当前行为模式（current_pattern）以及相对历史基线的关键变化（key_changes）。\n" +
		"3. 将相关信号组织为可验证的 Findings：每个 Finding 必须引用至少一个证据 ID；high/critical Finding 必须引用两个独立证据类别，否则必须标记 needs_verification=true。\n" +
		"4. 分析反证（counter_evidence）与合理的正常解释（plausible_benign_explanations）。\n" +
		"5. 给出可执行、可逆的人工核查步骤（verification_steps）。\n" +
		"规则：\n" +
		"- 区分 observation、interpretation 与 conclusion；不要简单逐项复述输入字段，只报告对结论有实际影响的特征。\n" +
		"- 不得根据目标域名推断具体内容、用户意图或违法行为。\n" +
		"- 缺少数据时必须表达 insufficient/unknown，不得推断为正常。\n" +
		"- 不得引用输入中不存在的证据 ID、用户、事件、时间或数值；不得捏造基线数值。\n" +
		"- 不得建议由 AI 自动封禁、删除或处罚用户；自动处置由确定性规则与管理员决定。\n" +
		"- 人类可读文本使用简体中文；枚举值与证据 ID 保持原样。\n" +
		"- 仅输出符合指定 JSON Schema 的 JSON。\n" +
		"Prompt version: " + model.AuditPromptFindingVersion
	schema, _ := json.Marshal(auditcontract.UserFindingSchema())
	return prompt + " Required JSON schema: " + string(schema)
}

func auditReportSystemPrompt() string {
	prompt := "你是 OBoard 用户行为审计报告综合器。输入中的 engine 对象包含系统权威值：overall_risk、health、confidence、coverage、baseline_days、dropped_buckets、identity_quality 以及全部版本号。\n" +
		"executive.risk_score 必须等于 engine.overall_risk；health_score 必须等于 100 减去 risk_score；evidence_confidence 必须等于 engine.confidence；data_quality 与 methodology 必须与 engine 完全一致。\n" +
		"verdict 必须按风险区间选择：0-34 normal，35-69 attention，70-100 high_risk；仅当 engine 数据不足（coverage<0.8 或 baseline_days<3）时才允许 insufficient_evidence。\n" +
		"你的任务：\n" +
		"1. 综合各用户的 Findings 与行为画像，生成一句话结论与总体行为画像。\n" +
		"2. 按严重程度组织最终 Findings；每个 Finding 必须引用输入中存在的证据 ID，high/critical 必须引用两个独立证据类别或标记 needs_verification=true。\n" +
		"3. 汇总时间线、反证与数据缺口。\n" +
		"4. 给出按优先级排列、可逆的人工核查建议（recommended_actions）。\n" +
		"规则：\n" +
		"- 不得修改、重新计算或四舍五入任何系统数值。\n" +
		"- 不得根据目标域名推断具体内容、用户意图或违法行为。\n" +
		"- 不得引用输入中不存在的证据 ID、用户、事件、时间或数值。\n" +
		"- 不得建议由 AI 自动封禁、删除或处罚用户。\n" +
		"- 人类可读文本使用简体中文；枚举值与证据 ID 保持原样。\n" +
		"- 仅输出符合指定 JSON Schema 的 JSON。\n" +
		"Prompt version: " + model.AuditPromptReportVersion
	schema, _ := json.Marshal(auditcontract.ReportSchema())
	return prompt + " Required JSON schema: " + string(schema)
}

func responsesAuditPayload(modelID, systemPrompt, input string, strict bool, schema map[string]any) map[string]any {
	payload := map[string]any{
		"model":             modelID,
		"temperature":       0,
		"max_output_tokens": auditMaxTokens,
		"input": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": input},
		},
	}
	if strict {
		encoded, _ := json.Marshal(schema)
		payload["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "oboard_audit_output", "strict": true, "schema": json.RawMessage(encoded)}}
	}
	return payload
}

func chatAuditPayload(modelID, systemPrompt, input, responseFormat string, schema map[string]any) map[string]any {
	payload := map[string]any{
		"model":       modelID,
		"temperature": 0,
		"max_tokens":  auditMaxTokens,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": input},
		},
	}
	switch responseFormat {
	case "json_object":
		payload["response_format"] = map[string]any{"type": "json_object"}
	case "json_schema":
		encoded, _ := json.Marshal(schema)
		payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "oboard_audit_output", "strict": true, "schema": json.RawMessage(encoded)}}
	}
	return payload
}

func auditRetryable(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "response_format") || strings.Contains(message, "json_schema") || strings.Contains(message, "json_object") || strings.Contains(message, "structured output")
}

func providerRouteUnavailable(responseBody []byte) bool {
	var envelope struct {
		Type  string `json:"type"`
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(responseBody, &envelope) != nil {
		return false
	}
	for _, value := range []string{envelope.Type, envelope.Error.Type, envelope.Error.Code} {
		normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "."))
		if normalized == "router.unavailable" || normalized == "service.unavailable" {
			return true
		}
	}
	return false
}

func extractJSONContent(content string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "```") {
		if lines := strings.SplitN(trimmed, "\n", 2); len(lines) == 2 {
			trimmed = strings.TrimSpace(lines[1])
		}
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "```"))
	}
	if json.Valid([]byte(trimmed)) {
		return json.RawMessage(trimmed), nil
	}
	start := strings.Index(trimmed, "{")
	if start < 0 {
		return nil, errors.New("model report is not JSON")
	}
	inString, escaped, depth := false, false, 0
	for i := start; i < len(trimmed); i++ {
		switch {
		case inString:
			if escaped {
				escaped = false
			} else if trimmed[i] == '\\' {
				escaped = true
			} else if trimmed[i] == '"' {
				inString = false
			}
		case trimmed[i] == '"':
			inString = true
		case trimmed[i] == '{':
			depth++
		case trimmed[i] == '}':
			depth--
			if depth == 0 {
				candidate := trimmed[start : i+1]
				if json.Valid([]byte(candidate)) {
					return json.RawMessage(candidate), nil
				}
				return nil, errors.New("model report is not valid JSON")
			}
		}
	}
	return nil, errors.New("model report does not contain a complete JSON object")
}

func providerResponseContent(format string, responseBody []byte) (string, int64, int64, error) {
	if format == "responses" {
		envelope, content, err := decodeResponsesResult(responseBody)
		if err != nil {
			return "", 0, 0, err
		}
		inputTokens := envelope.Usage.InputTokens
		if inputTokens == 0 {
			inputTokens = envelope.Usage.PromptTokens
		}
		outputTokens := envelope.Usage.OutputTokens
		if outputTokens == 0 {
			outputTokens = envelope.Usage.CompletionTokens
		}
		return content, inputTokens, outputTokens, nil
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(responseBody, &envelope) != nil || len(envelope.Choices) != 1 {
		return "", 0, 0, errors.New("model response is not a supported chat completion")
	}
	choice := envelope.Choices[0]
	if strings.TrimSpace(choice.Message.Content) == "" {
		if choice.FinishReason == "length" {
			return "", envelope.Usage.PromptTokens, envelope.Usage.CompletionTokens, errors.New("model response contains no visible content because the output limit was exhausted")
		}
		return "", envelope.Usage.PromptTokens, envelope.Usage.CompletionTokens, errors.New("model response contains no visible content")
	}
	if choice.FinishReason == "length" {
		return "", envelope.Usage.PromptTokens, envelope.Usage.CompletionTokens, errors.New("model response was truncated by the output limit")
	}
	return choice.Message.Content, envelope.Usage.PromptTokens, envelope.Usage.CompletionTokens, nil
}

type responsesResult struct {
	OutputText string `json:"output_text"`
	Status     string `json:"status"`
	Output     []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens      int64 `json:"input_tokens"`
		OutputTokens     int64 `json:"output_tokens"`
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
	} `json:"usage"`
}

func decodeResponsesResult(responseBody []byte) (responsesResult, string, error) {
	var envelope responsesResult
	if json.Unmarshal(responseBody, &envelope) != nil {
		return responsesResult{}, "", errors.New("model response is not a supported responses API result")
	}
	if strings.EqualFold(strings.TrimSpace(envelope.Status), "incomplete") {
		return envelope, "", errors.New("model response was truncated by the output limit")
	}
	content := strings.TrimSpace(envelope.OutputText)
	if content == "" {
		var parts []string
		for _, output := range envelope.Output {
			if output.Type != "" && output.Type != "message" {
				continue
			}
			for _, item := range output.Content {
				if item.Type != "" && item.Type != "output_text" {
					continue
				}
				if text := strings.TrimSpace(item.Text); text != "" {
					parts = append(parts, text)
				}
			}
		}
		content = strings.Join(parts, "\n")
	}
	if content == "" {
		return envelope, "", errors.New("model response contains no visible content")
	}
	return envelope, content, nil
}

func modelErrorMessage(responseBody []byte, credential string) string {
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
		ModelID string          `json:"modelID"`
	}
	if json.Unmarshal(responseBody, &payload) != nil {
		return ""
	}
	message := ""
	if len(payload.Error) == 0 {
		message = strings.TrimSpace(payload.Message)
		if message == "" {
			message = strings.TrimSpace(payload.Type)
		}
		if message != "" && strings.TrimSpace(payload.ModelID) != "" {
			message += " (model: " + strings.TrimSpace(payload.ModelID) + ")"
		}
	}
	var object struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if len(payload.Error) > 0 && json.Unmarshal(payload.Error, &object) == nil {
		message = object.Message
		if object.Type != "" && message == "" {
			message = object.Type
		}
	} else if len(payload.Error) > 0 && json.Unmarshal(payload.Error, &message) != nil {
		return ""
	}
	message = strings.TrimSpace(message)
	if credential != "" {
		message = strings.ReplaceAll(message, credential, "[redacted]")
	}
	message = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 300 {
		message = message[:300] + "…"
	}
	return message
}

func providerLog(provider *airpc.Provider, method, requestURL string, requestHeaders map[string]string, requestBody, responseBody []byte, response *http.Response) json.RawMessage {
	if provider == nil {
		return nil
	}
	credential := provider.APIKey
	status := 0
	responseHeaders := map[string]string{}
	if response != nil {
		status = response.StatusCode
		for key, values := range response.Header {
			if len(values) > 0 {
				responseHeaders[key] = redactCredential(strings.Join(values, ", "), credential)
			}
		}
	}
	detail, err := json.Marshal(airpc.ProviderLog{
		Provider: provider.Name, Model: provider.Model, APIFormat: normalizeProviderFormat(provider.APIFormat),
		RequestMethod: method, RequestURL: requestURL, RequestHeaders: requestHeaders,
		RequestBody: compactJSON(boundedBytesBytes(requestBody, 32<<10)),
		Status:      status, ResponseHeaders: responseHeaders,
		ResponseBody: boundedBytes(redactCredential(strings.TrimSpace(string(responseBody)), credential), 64<<10),
	})
	if err != nil {
		return nil
	}
	return detail
}

func providerRequestError(prefix string, status int, responseBody []byte, credential string, logDetail ...json.RawMessage) error {
	detail := modelErrorMessage(responseBody, credential)
	var detailValue json.RawMessage
	if len(logDetail) > 0 {
		detailValue = logDetail[0]
	}
	if detail != "" {
		return &providerFailureError{message: fmt.Sprintf("%s returned HTTP %d: %s", prefix, status, detail), detail: detailValue}
	}
	return &providerFailureError{message: fmt.Sprintf("%s returned HTTP %d", prefix, status), detail: detailValue}
}

func boundedBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func boundedBytesBytes(value []byte, limit int) []byte {
	if len(value) <= limit {
		return value
	}
	return append(append([]byte{}, value[:limit]...), []byte("…")...)
}

type providerFailureError struct {
	message string
	detail  json.RawMessage
}

func (e *providerFailureError) Error() string {
	return e.message
}

func normalizeProviderFormat(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "responses":
		return "responses"
	default:
		return "chat_completions"
	}
}

func providerFormatPath(format string) string {
	if format == "responses" {
		return "/responses"
	}
	return "/chat/completions"
}

func unixHTTPClient(socketPath string) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}, DisableCompression: true, MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Transport: transport, Timeout: 35 * time.Second}
}

func rpcJSON(ctx context.Context, client *http.Client, method, target string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Controller RPC returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

func mustRandomToken() string {
	value, err := security.RandomToken(18)
	if err != nil {
		panic(err)
	}
	return value
}

func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
