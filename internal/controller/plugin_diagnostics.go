package controller

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
)

var pluginDiagnosticTaskTypes = map[string]string{
	plugin.CapNetworkPing:      model.AgentTaskTypeNetworkPing,
	plugin.CapNetworkTrace:     model.AgentTaskTypeNetworkTrace,
	plugin.CapNetworkTCPProbe:  model.AgentTaskTypeNetworkTCP,
	plugin.CapNetworkDNSLookup: model.AgentTaskTypeNetworkDNS,
	plugin.CapNetworkHTTPProbe: model.AgentTaskTypeNetworkHTTP,
}

// NetworkDiagnosticTaskTypes lists every plugin diagnostic task type.
func NetworkDiagnosticTaskTypes() []string {
	return []string{model.AgentTaskTypeNetworkPing, model.AgentTaskTypeNetworkTrace, model.AgentTaskTypeNetworkTCP, model.AgentTaskTypeNetworkDNS, model.AgentTaskTypeNetworkHTTP}
}

// RunNetworkDiagnostic queues exactly one signed, structured diagnostic task
// and waits for it within the SDK call deadline. A server keeps at most one
// plugin diagnostic in flight; a task that could not start in time is
// superseded, so offline or busy nodes never accumulate queued diagnostics.
func (h *pluginHost) RunNetworkDiagnostic(ctx context.Context, request plugin.DiagnosticRequest) (json.RawMessage, error) {
	s := h.server
	taskType, ok := pluginDiagnosticTaskTypes[request.Capability]
	if !ok {
		return nil, plugin.Fail(plugin.CodeUnsupportedCapability, "unsupported diagnostic")
	}
	active, err := s.store.CountActiveAgentTasks(ctx, request.ServerID, NetworkDiagnosticTaskTypes())
	if err != nil {
		return nil, plugin.Fail(plugin.CodeInternal, "internal error")
	}
	if active > 0 {
		return nil, plugin.Fail(plugin.CodeServerBusy, "another plugin diagnostic is already running on this server; retry later")
	}
	task, err := s.queueAgentTask(ctx, request.ServerID, taskType, request.Payload, 0)
	if err != nil {
		return nil, plugin.Fail(plugin.CodeOperationFailed, "the diagnostic could not be queued")
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := s.store.GetTask(ctx, task.ID)
		if err == nil && current != nil {
			switch current.Status {
			case "succeeded":
				return json.RawMessage(current.ResultJSON), nil
			case "failed", "rollback_failed":
				return nil, diagnosticFailure(current.ResultJSON)
			}
		}
		select {
		case <-ctx.Done():
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = s.store.SupersedePendingTask(cleanup, task.ID, "插件诊断已超时或取消")
			cancel()
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, plugin.Fail(plugin.CodeCancelled, "the run was cancelled")
			}
			return nil, plugin.Fail(plugin.CodeOperationTimeout, "the diagnostic did not finish in time")
		case <-ticker.C:
		}
	}
}

func diagnosticFailure(resultJSON string) error {
	var result struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Offline bool   `json:"offline"`
		Skipped bool   `json:"skipped"`
	}
	_ = json.Unmarshal([]byte(resultJSON), &result)
	switch {
	case result.Offline:
		return plugin.Fail(plugin.CodeServerOffline, "server went offline before the diagnostic ran")
	case result.Skipped:
		return plugin.Fail(plugin.CodeOperationFailed, "the diagnostic was superseded by a configuration deployment")
	}
	switch result.Code {
	case model.NetworkDiagnosticCodeLocalGateDenied:
		return plugin.Fail(plugin.CodeAgentPolicyDenied, "the server's local Agent policy does not allow plugin operations")
	case model.NetworkDiagnosticCodeUnsupported:
		return plugin.Fail(plugin.CodeUnsupportedCapability, "the Agent does not support this diagnostic on this platform")
	case model.NetworkDiagnosticCodeTargetNotAllowed:
		return plugin.Fail(plugin.CodeTargetNotAllowed, "the target resolves to a private or reserved address")
	case model.NetworkDiagnosticCodeExpired:
		return plugin.Fail(plugin.CodeOperationTimeout, "the Agent could not start the diagnostic in time")
	case model.NetworkDiagnosticCodeResolveFailed:
		return plugin.Fail(plugin.CodeOperationFailed, "the target could not be resolved")
	case model.NetworkDiagnosticCodeInvalidInput:
		return plugin.Fail(plugin.CodeInvalidArgument, "the Agent rejected the diagnostic parameters")
	default:
		return plugin.Fail(plugin.CodeOperationFailed, "the diagnostic failed")
	}
}
