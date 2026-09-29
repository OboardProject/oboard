package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginhttp"
	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/store"
)

// maxQueuedRuns bounds the global queue so a trigger storm cannot pile up
// work faster than workers drain it.
const maxQueuedRuns = 64

// TriggerDetail is stored with each run and delivered as run context.
type TriggerDetail struct {
	ScheduleID  int64          `json:"schedule_id,omitempty"`
	ScheduledAt string         `json:"scheduled_at,omitempty"`
	Event       *EventDetail   `json:"event,omitempty"`
	Caller      *CallerRef     `json:"caller,omitempty"`
	Extra       map[string]any `json:"-"`
}

type EventDetail struct {
	Type       string `json:"type"`
	ServerID   string `json:"server_id"`
	OccurredAt string `json:"occurred_at"`
}

// runnable checks everything a run needs before it may be queued or leased.
// It is the run-validate phase: saved configuration is re-checked against
// current servers, grants, secrets and switches.
func (s *Service) runnable(ctx context.Context, loaded loadedInstallation, instance model.PluginInstance) error {
	settings := s.Settings(ctx)
	switch {
	case !settings.Enabled:
		return Fail(CodeRuntimeUnavailable, "插件执行已关闭")
	case loaded.pkg == nil:
		return Fail(CodePluginDisabled, "插件还没有已发布的版本")
	case !loaded.installation.Enabled:
		return Fail(CodePluginDisabled, "插件已停用")
	case !instance.Enabled:
		return Fail(CodePluginDisabled, "插件实例已停用")
	case instance.PermissionReviewRequired:
		return Fail(CodePermissionReviewRequired, "新版本申请了新的权限，需要管理员重新审核授权")
	}
	summary, issues, _, _ := s.evaluateInstance(ctx, loaded, instance, settings.Enabled)
	switch summary.ConfigStatus {
	case ConfigRequired:
		for _, issue := range issues {
			if issue.Code == CodeSecretNotConfigured {
				return &Error{Code: CodeSecretNotConfigured, Message: "需要先配置密钥", Issues: issues}
			}
		}
		return &Error{Code: CodeConfigurationRequired, Message: "插件配置不完整", Issues: issues}
	case ConfigInvalid:
		for _, issue := range issues {
			if issue.Code == CodeServerNotFound {
				return &Error{Code: CodeServerNotFound, Message: "配置中的服务器已不存在", Issues: issues}
			}
		}
		return &Error{Code: CodeInvalidEnvironment, Message: "插件配置已失效，请重新检查", Issues: issues}
	}
	return nil
}

// RunManually queues a manual run for an instance. The caller is recorded so
// every SDK call re-checks that the caller may still execute plugins.
func (s *Service) RunManually(ctx context.Context, actor application.Principal, instanceID int64, idempotencyKey string) (model.PluginRun, error) {
	if err := s.require(actor, PermExecute); err != nil {
		return model.PluginRun{}, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return model.PluginRun{}, FailField(CodeInvalidArgument, "idempotency_key", "idempotency_key is required (at most 128 bytes)")
	}
	loaded, instance, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return model.PluginRun{}, err
	}
	if err := s.runnable(ctx, loaded, instance); err != nil {
		return model.PluginRun{}, err
	}
	caller := &CallerRef{PrincipalID: actor.ID, Type: string(actor.Type), GrantID: actor.GrantID}
	if actor.SourceIP.IsValid() {
		caller.SourceIP = actor.SourceIP.String()
	}
	run, created, err := s.enqueue(ctx, loaded, instance, model.PluginTriggerManual, TriggerDetail{Caller: caller}, "manual:"+actor.ID+":"+idempotencyKey, actor.ID)
	if err != nil {
		return run, err
	}
	if created {
		s.Wake()
	}
	return run, nil
}

func (s *Service) enqueue(ctx context.Context, loaded loadedInstallation, instance model.PluginInstance, trigger string, detail TriggerDetail, key, caller string) (model.PluginRun, bool, error) {
	queued, err := s.store.CountPluginRunsByStatus(ctx, model.PluginRunQueued)
	if err != nil {
		return model.PluginRun{}, false, storeError(err)
	}
	if queued >= maxQueuedRuns {
		return model.PluginRun{}, false, Fail(CodeRateLimited, "插件执行队列已满，请稍后再试")
	}
	run := model.PluginRun{
		UUID: "prun_" + randomToken(12), InstallationID: loaded.installation.ID, InstanceID: instance.ID, PackageID: loaded.pkg.ID,
		PluginKey: loaded.installation.PluginKey, PluginVersion: loaded.pkg.Version, Trigger: trigger, TriggerJSON: mustMarshal(detail),
		CallerPrincipal: caller, IdempotencyKey: key, RecoveryGeneration: s.Settings(ctx).RecoveryGen,
	}
	created, err := s.store.CreatePluginRun(ctx, &run)
	if errors.Is(err, store.ErrPluginConflict) {
		return model.PluginRun{}, false, Fail(CodeRunInProgress, "该实例已有正在排队或执行的任务")
	}
	if err != nil {
		return model.PluginRun{}, false, storeError(err)
	}
	return run, created, nil
}

// Wake nudges idle workers after a new run was queued.
func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) WakeChannel() <-chan struct{} { return s.wake }

func (s *Service) CancelRun(ctx context.Context, actor application.Principal, runID int64) (model.PluginRun, error) {
	if err := s.require(actor, PermExecute); err != nil {
		return model.PluginRun{}, err
	}
	if err := s.store.RequestPluginRunCancel(ctx, runID); err != nil {
		return model.PluginRun{}, storeError(err)
	}
	run, err := s.store.GetPluginRun(ctx, runID)
	return run, storeError(err)
}

func (s *Service) GetRun(ctx context.Context, actor application.Principal, runID int64) (model.PluginRun, error) {
	if err := s.require(actor, PermRead); err != nil {
		return model.PluginRun{}, err
	}
	run, err := s.store.GetPluginRun(ctx, runID)
	return run, storeError(err)
}

func (s *Service) ListRuns(ctx context.Context, actor application.Principal, filter store.PluginRunFilter) ([]model.PluginRun, error) {
	if err := s.require(actor, PermRead); err != nil {
		return nil, err
	}
	runs, err := s.store.ListPluginRuns(ctx, filter)
	return runs, storeError(err)
}

func (s *Service) ListRunLogs(ctx context.Context, actor application.Principal, runID, afterSeq int64) ([]model.PluginRunLog, error) {
	if err := s.require(actor, PermRead); err != nil {
		return nil, err
	}
	logs, err := s.store.ListPluginRunLogs(ctx, runID, afterSeq, 1000)
	return logs, storeError(err)
}

// Lease hands one queued run to a worker after re-validating it. Runs that
// became invalid while queued are finished here with a precise code and are
// never delivered to a runner.
func (s *Service) Lease(ctx context.Context, workerID string) (*pluginrpc.RunLease, error) {
	settings := s.Settings(ctx)
	if !settings.Enabled {
		return nil, nil
	}
	for attempt := 0; attempt < 4; attempt++ {
		run, err := s.store.LeasePluginRun(ctx, workerID, s.now().Add(LeaseDuration), settings.RecoveryGen, settings.MaxConcurrency)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, storeError(err)
		}
		lease, runErr := s.buildLease(ctx, run, settings)
		if runErr == nil {
			return lease, nil
		}
		coded := AsError(runErr)
		status := model.PluginRunFailed
		if coded.Code == CodePermissionReviewRequired || coded.Code == CodeCapabilityDenied {
			status = model.PluginRunPermissionDenied
		}
		if finished, err := s.store.FinishPluginRun(ctx, run.UUID, run.LeaseGeneration, status, coded.Code, coded.Message, nil); err == nil {
			s.recordOutcome(ctx, finished)
		}
	}
	return nil, nil
}

func (s *Service) buildLease(ctx context.Context, run model.PluginRun, settings Settings) (*pluginrpc.RunLease, error) {
	loaded, instance, err := s.loadInstance(ctx, run.InstanceID)
	if err != nil {
		return nil, Fail(CodePluginDisabled, "插件实例已不存在")
	}
	if loaded.pkg == nil || loaded.pkg.ID != run.PackageID {
		return nil, Fail(CodePluginDisabled, "插件版本已变更，本次执行作废")
	}
	if err := s.runnable(ctx, loaded, instance); err != nil {
		return nil, err
	}
	limits, err := EffectiveLimits(loaded.manifest.Limits, settings.MaxTimeout)
	if err != nil {
		return nil, err
	}
	values, custom := decodeEnvironment(instance)
	secretNames, _ := s.store.ListPluginSecretNames(ctx, instance.ID)
	configured := map[string]bool{}
	for name := range secretNames {
		configured[name] = true
	}
	env := map[string]pluginrpc.EnvValue{}
	for name, value := range ResolveRuntimeEnvironment(instance.ID, *loaded.manifest, values, custom, configured) {
		env[name] = pluginrpc.EnvValue{Type: value.Type, Value: value.Value, Raw: value.Raw, Custom: value.Custom}
	}
	var detail TriggerDetail
	_ = json.Unmarshal(run.TriggerJSON, &detail)
	runContext := pluginrpc.RunContext{RunID: run.UUID, PluginID: run.PluginKey, PluginVersion: run.PluginVersion, InstanceID: strconv.FormatInt(instance.ID, 10), Trigger: run.Trigger, ScheduledAt: detail.ScheduledAt}
	if detail.Event != nil {
		runContext.Event = mustMarshal(detail.Event)
	}
	return &pluginrpc.RunLease{
		RunUUID: run.UUID, LeaseGeneration: run.LeaseGeneration, PluginKey: run.PluginKey, PluginVersion: run.PluginVersion,
		InstanceID: instance.ID, Source: loaded.pkg.Source, Environment: env, Context: runContext,
		Limits: pluginrpc.Limits{TimeoutMS: limits.TimeoutMS, MemoryMiB: limits.MemoryMiB, SDKCalls: limits.SDKCalls, HTTPRequests: limits.HTTPRequests, AgentOperations: limits.AgentOperations, LogBytes: limits.LogBytes, LogLines: limits.LogLines, LogLineBytes: limits.LogLineBytes, ResultBytes: limits.ResultBytes},
	}, nil
}

var workerStatuses = map[string]bool{
	model.PluginRunSucceeded: true, model.PluginRunFailed: true, model.PluginRunTimeout: true, model.PluginRunCancelled: true,
	model.PluginRunPermissionDenied: true, model.PluginRunResourceLimit: true,
}

// Complete records a worker's terminal report for the current lease. Logs,
// result and error text are bounded and redacted against the instance's
// configured secrets before they are stored.
func (s *Service) Complete(ctx context.Context, request pluginrpc.CompleteRequest) error {
	run, err := s.store.GetPluginRunByUUID(ctx, request.RunUUID)
	if err != nil {
		return storeError(err)
	}
	if run.Status != model.PluginRunRunning || run.LeaseOwner != request.WorkerID || run.LeaseGeneration != request.LeaseGeneration {
		return ErrConflict
	}
	status := request.Status
	if !workerStatuses[status] {
		status = model.PluginRunFailed
	}
	code := sanitizeCode(request.ErrorCode)
	if status == model.PluginRunSucceeded {
		code = ""
	} else if code == "" {
		code = CodeScriptError
	}
	if run.CancelRequested && status != model.PluginRunSucceeded {
		status, code = model.PluginRunCancelled, CodeCancelled
	}
	secrets := s.instanceSecretValues(ctx, run.InstanceID)
	message := boundText(pluginhttp.Redact(request.ErrorMessage, secrets), 1024)
	result := json.RawMessage(nil)
	if len(request.Result) > 0 {
		if len(request.Result) > MaxResultBytes {
			status, code, message = model.PluginRunResourceLimit, CodeResourceLimit, "执行结果超过 64 KiB"
		} else if json.Valid(request.Result) {
			result = pluginhttp.RedactBytes(append([]byte(nil), request.Result...), secrets)
			if !json.Valid(result) {
				result = mustMarshal(map[string]string{"redacted": "结果包含密钥内容，已隐藏"})
			}
		}
	}
	logs := make([]model.PluginRunLog, 0, len(request.Logs))
	totalBytes := 0
	for i, line := range request.Logs {
		if i >= MaxLogLines {
			break
		}
		text := boundText(pluginhttp.Redact(line.Message, secrets), MaxLogLineBytes)
		totalBytes += len(text)
		if totalBytes > MaxLogBytes {
			break
		}
		level := line.Level
		switch level {
		case "debug", "info", "warn", "error":
		default:
			level = "info"
		}
		logs = append(logs, model.PluginRunLog{Seq: int64(i + 1), Level: level, Message: text})
	}
	if request.DroppedLogs > 0 || len(logs) < len(request.Logs) {
		dropped := request.DroppedLogs + len(request.Logs) - len(logs)
		logs = append(logs, model.PluginRunLog{Seq: int64(len(logs) + 1), Level: "warn", Message: "日志超过限额，已丢弃 " + strconv.Itoa(dropped) + " 行"})
	}
	_ = s.store.AppendPluginRunLogs(ctx, run.ID, logs)
	finished, err := s.store.FinishPluginRun(ctx, run.UUID, run.LeaseGeneration, status, code, message, result)
	if err != nil {
		return storeError(err)
	}
	s.recordOutcome(ctx, finished)
	return nil
}

func (s *Service) recordOutcome(ctx context.Context, run model.PluginRun) {
	if run.Status == model.PluginRunCancelled {
		return
	}
	finished := s.now()
	if run.FinishedAt != nil {
		finished = *run.FinishedAt
	}
	_ = s.store.RecordPluginRunOutcome(ctx, run.InstanceID, run.Status == model.PluginRunSucceeded, run.ErrorCode, finished, AutoPauseFailureStreak)
}

// CancelRequested answers the worker's cancel poll. A lease that no longer
// matches is reported as cancelled so the runner stops.
func (s *Service) CancelRequested(ctx context.Context, runUUID string, generation int64) bool {
	run, err := s.store.GetPluginRunByUUID(ctx, runUUID)
	if err != nil {
		return true
	}
	return run.Status != model.PluginRunRunning || run.LeaseGeneration != generation || run.CancelRequested || !s.Settings(ctx).Enabled
}

// Sweep fails runs whose lease lapsed and prunes history past retention.
func (s *Service) Sweep(ctx context.Context) {
	expired, err := s.store.ExpirePluginRunLeases(ctx, s.now())
	if err == nil {
		for _, run := range expired {
			s.recordOutcome(ctx, run)
		}
	}
	settings := s.Settings(ctx)
	now := s.now()
	_ = s.store.PrunePluginHistory(ctx, now.AddDate(0, 0, -settings.LogRetentionDays), now.AddDate(0, 0, -settings.RunRetentionDays))
}

// instanceSecretValues decrypts an instance's secrets for redaction only.
func (s *Service) instanceSecretValues(ctx context.Context, instanceID int64) []string {
	encrypted, err := s.store.ListPluginSecretsEncrypted(ctx, instanceID)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(encrypted))
	for _, value := range encrypted {
		if plain, err := s.host.DecryptSecret(value); err == nil && plain != "" {
			out = append(out, plain)
		}
	}
	return out
}

func sanitizeCode(code string) string {
	code = strings.TrimSpace(code)
	if len(code) > 64 {
		return CodeScriptError
	}
	for _, c := range code {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return CodeScriptError
		}
	}
	return code
}

func boundText(text string, limit int) string {
	text = strings.ToValidUTF8(text, "�")
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func (s *Service) runDeadline(run model.PluginRun, manifest Manifest) time.Time {
	limits, _ := EffectiveLimits(manifest.Limits, s.Settings(context.Background()).MaxTimeout)
	start := s.now()
	if run.StartedAt != nil {
		start = *run.StartedAt
	}
	return start.Add(limits.Timeout() + 2*time.Second)
}
