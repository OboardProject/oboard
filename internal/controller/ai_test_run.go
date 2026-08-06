package controller

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	aiTestTimeout      = 90 * time.Second
	aiTestRawJSONLimit = 512 << 10
)

type aiTestResult struct {
	ok           bool
	requestJSON  string
	responseJSON string
	statusCode   int
	durationMS   int64
	content      string
	detail       string
	capability   *model.AIProviderCapability
}

// validateAITestCapability rejects forged, stale or contradictory capability
// profiles. Only the audit readiness test may produce one.
func validateAITestCapability(capability *model.AIProviderCapability) error {
	if capability == nil {
		return nil
	}
	now := time.Now().UTC()
	if capability.ProviderProfileVersion != model.AuditProviderProfileVersion || capability.TestedAt.Before(now.Add(-24*time.Hour)) || capability.TestedAt.After(now.Add(5*time.Minute)) {
		return errors.New("AI provider capability 版本或时间无效")
	}
	if len(capability.Model) == 0 || len(capability.Model) > 512 || len(capability.Note) > 500 {
		return errors.New("AI provider capability 字段无效")
	}
	gradeOK := capability.AuditGrade == model.AuditProviderGradeA || capability.AuditGrade == model.AuditProviderGradeB || capability.AuditGrade == model.AuditProviderGradeC || capability.AuditGrade == model.AuditProviderGradeUnusable
	structuredOK := capability.StructuredOutput == model.AuditProviderStructuredJSONSchema || capability.StructuredOutput == model.AuditProviderStructuredJSONObject || capability.StructuredOutput == model.AuditProviderStructuredNone
	outputOK := capability.OutputMode == model.AuditOutputModeStrictSchema || capability.OutputMode == model.AuditOutputModeJSONObject || capability.OutputMode == model.AuditOutputModeText
	if !gradeOK || !structuredOK || !outputOK || capability.SchemaSuccessRate < 0 || capability.SchemaSuccessRate > 1 || capability.MaxVerifiedOutputTokens <= 0 || capability.MaxVerifiedOutputTokens > 1<<20 {
		return errors.New("AI provider capability 枚举无效")
	}
	if (capability.AuditGrade == model.AuditProviderGradeA || capability.AuditGrade == model.AuditProviderGradeB) && capability.OutputMode == model.AuditOutputModeText {
		return errors.New("AI provider A/B 级能力不允许纯文本输出")
	}
	if capability.AuditGrade == model.AuditProviderGradeC && capability.OutputMode != model.AuditOutputModeText {
		return errors.New("AI provider C 级能力必须标记为纯文本输出")
	}
	return nil
}

func (s *Server) apiV2AIProviderTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	var request struct {
		ProviderID string `json:"provider_id"`
		BaseURL    string `json:"base_url"`
		APIKey     string `json:"api_key"`
		APIFormat  string `json:"api_format"`
		Model      string `json:"model"`
	}
	if !decodeV2(w, r, &request) {
		return
	}
	modelID := strings.TrimSpace(request.Model)
	if modelID == "" || len(modelID) > aiModelIDLimit {
		v2Error(w, r, http.StatusBadRequest, "invalid_provider_model", "请填写要测试的模型 ID")
		return
	}
	baseURL, err := normalizeAIProviderBaseURL(request.BaseURL)
	if err != nil {
		v2Error(w, r, http.StatusBadRequest, "invalid_provider_url", "Base URL 必须是有效的 HTTPS 版本根端点；本机服务可使用 HTTP loopback 地址")
		return
	}
	credential := strings.TrimSpace(request.APIKey)
	providerID := strings.TrimSpace(request.ProviderID)
	providerName := ""
	if credential == "" && providerID != "" {
		provider, loadErr := s.store.GetAIProvider(r.Context(), providerID)
		if loadErr != nil || provider.CredentialEncrypted == "" {
			v2Error(w, r, http.StatusBadRequest, "provider_credential_missing", "该 Provider 没有可用的 API Key")
			return
		}
		credential, err = security.DecryptSecret(s.sessionSecret, "ai-provider-credential:"+provider.ID, provider.CredentialEncrypted)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		providerName = provider.Name
	}
	if credential == "" {
		v2Error(w, r, http.StatusBadRequest, "provider_credential_missing", "请先填写 API Key")
		return
	}
	if len(credential) > aiProviderCredentialLimit {
		v2Error(w, r, http.StatusBadRequest, "provider_credential_invalid", "API Key 长度无效")
		return
	}
	random, err := security.RandomToken(18)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	requestID := "ait_" + random
	entry, err := s.aiTests.submit(airpc.AITestRequest{ID: requestID, ProviderID: providerID, Name: providerName, BaseURL: baseURL, APIFormat: normalizeAIProviderFormat(request.APIFormat), APIKey: credential, Model: modelID}, requestID)
	if errors.Is(err, errAIModelDiscoveryBusy) {
		v2Error(w, r, http.StatusTooManyRequests, "provider_test_busy", "AI Provider 测试请求较多，请稍后重试")
		return
	}
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	defer s.aiTests.cancel(entry.request.ID)
	timer := time.NewTimer(s.aiTestTimeout)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-timer.C:
		v2Error(w, r, http.StatusServiceUnavailable, "provider_test_unavailable", "AI Worker 未及时返回测试结果，请确认服务正在运行")
	case result := <-entry.result:
		requestJSON := redactAICredential(result.requestJSON, credential)
		responseJSON := redactAICredential(result.responseJSON, credential)
		payload := map[string]any{"ok": result.ok, "request_json": requestJSON, "response_json": responseJSON}
		if result.capability != nil {
			payload["capability"] = result.capability
		}
		if result.statusCode != 0 {
			payload["status_code"] = result.statusCode
		}
		if result.durationMS >= 0 {
			payload["duration_ms"] = result.durationMS
		}
		if result.content != "" {
			payload["content"] = result.content
		}
		if result.ok {
			payload["message"] = auditTestMessage(result.capability)
			v2Write(w, r, http.StatusOK, payload, nil)
			return
		}
		message := "AI Provider 配置测试失败，请检查 Base URL、API Key、模型和服务兼容性"
		if result.detail != "" {
			message += "；" + result.detail
		}
		payload["message"] = message
		v2Write(w, r, http.StatusOK, payload, nil)
	}
}

func auditTestMessage(capability *model.AIProviderCapability) string {
	if capability == nil {
		return "连接成功，但未完成审计就绪测试"
	}
	switch capability.AuditGrade {
	case model.AuditProviderGradeA:
		return "审计就绪测试通过（A 级）：严格 Schema 输出稳定，可用于正式审计"
	case model.AuditProviderGradeB:
		return "审计就绪测试通过（B 级）：支持 JSON Object 输出并在本地校验修复后可用"
	case model.AuditProviderGradeC:
		return "审计就绪测试完成（C 级）：仅支持文本输出，不能用于正式审计报告"
	default:
		return "审计就绪测试未通过：无法稳定返回结构化结果"
	}
}

func (s *Server) apiV2AIProviderTestLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		v2Error(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "请求方法不受支持")
		return
	}
	if s.logs == nil {
		fail(w, errors.New("controller log storage is not configured"), http.StatusServiceUnavailable)
		return
	}
	lines := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 2000 {
			fail(w, errors.New("lines must be between 1 and 2000"), 400)
			return
		}
		lines = value
	}
	snapshot, err := s.logs.Snapshot(lines, "ai-test[")
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"logs": snapshot})
}

func redactAICredential(value, credential string) string {
	if credential != "" && strings.Contains(value, credential) {
		return strings.ReplaceAll(value, credential, "[redacted]")
	}
	return value
}

func recordAITestLog(requested airpc.AITestRequest, result aiTestResult) {
	name := strings.TrimSpace(requested.Name)
	if name == "" {
		name = strings.TrimSpace(requested.ProviderID)
	}
	if name == "" {
		name = "draft"
	}
	label := "failed"
	if result.ok {
		label = "ok"
	}
	parts := []string{fmt.Sprintf("ai-test[%s] provider=%q model=%q format=%s status=%d duration=%dms", label, name, requested.Model, requested.APIFormat, result.statusCode, result.durationMS)}
	if result.detail != "" {
		parts = append(parts, "error="+strconv.Quote(result.detail))
	}
	if result.content != "" {
		parts = append(parts, "content="+strconv.Quote(result.content))
	}
	if result.requestJSON != "" {
		parts = append(parts, "request="+redactAICredential(result.requestJSON, requested.APIKey))
	}
	if result.responseJSON != "" {
		parts = append(parts, "response="+redactAICredential(result.responseJSON, requested.APIKey))
	}
	log.Printf("%s", strings.Join(parts, " "))
}
