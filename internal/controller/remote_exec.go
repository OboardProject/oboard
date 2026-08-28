package controller

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) captureRemoteExecResult(task model.AgentTask, status, resultJSON string) string {
	var wire model.RemoteExecWireResult
	if json.Unmarshal([]byte(resultJSON), &wire) != nil {
		return `{"error":"invalid remote exec result"}`
	}
	requestID := remoteExecRequestID(task)
	s.remoteExecHub.Put(model.RemoteExecTransientResult{
		RequestID: requestID, TaskID: task.ID, Meta: wire.RemoteExecResultMeta,
		Stdout: wire.Stdout, Stderr: wire.Stderr, Finished: time.Now().UTC(),
	})
	meta, _ := json.Marshal(wire.RemoteExecResultMeta)
	_ = status
	return string(meta)
}

func remoteExecRequestID(task model.AgentTask) string {
	var payload model.RemoteExecTaskPayload
	if json.Unmarshal([]byte(task.PayloadJSON), &payload) == nil && payload.RequestID != "" {
		return payload.RequestID
	}
	var op model.RemoteOperationTaskPayload
	if json.Unmarshal([]byte(task.PayloadJSON), &op) == nil {
		return op.RequestID
	}
	return ""
}

func (s *Server) waitRemoteExec(ctx context.Context, principal mcpauth.GrantPrincipal, server *model.Server, privilege string, payload any, timeout time.Duration) (map[string]any, error) {
	if err := s.assertRemoteExecAllowedHTTP(ctx, server, privilege); err != nil {
		return nil, err
	}
	taskType := model.AgentTaskTypeRemoteExec
	if privilege == model.PrivilegeRemoteOperations {
		taskType = model.AgentTaskTypeRemoteOperation
	}
	task, err := s.queueAgentTask(ctx, server.ID, taskType, payload, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	requestID := remoteExecRequestID(task)
	result, ok := s.remoteExecHub.Wait(requestID, timeout)
	if !ok {
		_ = s.sendAgentControl(server.ID, map[string]any{"type": "remote_exec_cancel", "request_id": requestID})
		return nil, codedError("remote_exec_timeout", "remote execution timed out waiting for the agent")
	}
	out := map[string]any{
		"exit_code": result.Meta.ExitCode, "duration_ms": result.Meta.DurationMS,
		"stdout": result.Stdout, "stderr": result.Stderr,
		"stdout_bytes": result.Meta.StdoutBytes, "stderr_bytes": result.Meta.StderrBytes,
		"stdout_truncated": result.Meta.StdoutTruncated, "stderr_truncated": result.Meta.StderrTruncated,
		"stdout_sha256": result.Meta.StdoutSHA256, "stderr_sha256": result.Meta.StderrSHA256,
	}
	if result.Meta.Error != "" {
		out["error"] = result.Meta.Error
		out["code"] = result.Meta.Error
		if result.Meta.Error == "remote_exec_cancelled" {
			return out, codedError("remote_exec_cancelled", "remote execution was cancelled")
		}
		if result.Meta.Error == "remote_exec_timeout" {
			return out, codedError("remote_exec_timeout", "remote execution timed out")
		}
		if result.Meta.Error == "request_id_conflict" {
			return out, codedError("request_id_conflict", "request_id was reused with a different payload")
		}
		if result.Meta.Error == "agent_local_gate_denied" {
			return out, codedError("agent_local_gate_denied", "agent local security policy denied this operation")
		}
	}
	if result.Meta.StdoutTruncated || result.Meta.StderrTruncated {
		out["code"] = "remote_exec_output_truncated"
	}
	_ = principal
	return out, nil
}

func (s *Server) assertRemoteExecAllowedHTTP(ctx context.Context, server *model.Server, privilege string) error {
	settings, _ := s.store.ListSettings(ctx)
	policy, err := s.store.GetServerRemoteAccessPolicy(ctx, server.ID)
	if err != nil {
		return err
	}
	status, err := s.store.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		return err
	}
	if !settingBool(settings, settingRemoteTerminalEnabled, true) {
		return codedError("remote_access_global_disabled", "remote control is globally disabled")
	}
	if !policy.RemoteTerminalEnabled {
		return codedError("remote_access_server_disabled", "remote control is disabled on the server")
	}
	globalMCPEnabled := settingBool(settings, settingMCPRemoteOperationsEnabled, false) &&
		settingBool(settings, settingMCPStructuredExecEnabled, false) &&
		settingBool(settings, settingMCPRawShellEnabled, false)
	if !globalMCPEnabled {
		return codedError("remote_access_global_disabled", "MCP control is globally disabled")
	}
	if !policy.MCPRemoteOperationsEnabled || !policy.MCPStructuredExecEnabled || !policy.MCPRawShellEnabled {
		return codedError("remote_access_server_disabled", "MCP control is disabled on the server")
	}
	enabled := false
	switch privilege {
	case model.PrivilegeRemoteOperations:
		enabled = settingBool(settings, settingMCPRemoteOperationsEnabled, false) && policy.MCPRemoteOperationsEnabled
	case model.PrivilegeRemoteExec:
		enabled = settingBool(settings, settingMCPStructuredExecEnabled, false) && policy.MCPStructuredExecEnabled
	case model.PrivilegeRemoteShell:
		enabled = settingBool(settings, settingMCPRawShellEnabled, false) && policy.MCPRawShellEnabled
	}
	if !enabled {
		if !settingBool(settings, settingKeyForPrivilege(privilege), false) {
			return codedError("remote_access_global_disabled", "this remote access feature is globally disabled")
		}
		return codedError("remote_access_server_disabled", "this remote access feature is disabled on the server")
	}
	if server.Status != model.ServerOnline {
		return codedError("agent_offline", "agent is offline")
	}
	if !containsString(status.Capabilities, model.RemoteAccessCapabilityExec) {
		return codedError("agent_upgrade_required", "agent does not advertise remote_exec_v1")
	}
	if status.LocalMode == model.RemoteAccessModeHardened {
		allowed := false
		switch privilege {
		case model.PrivilegeRemoteOperations:
			allowed = status.LocalAllow.MCPRemoteOperations
		case model.PrivilegeRemoteExec:
			allowed = status.LocalAllow.MCPStructuredExec
		case model.PrivilegeRemoteShell:
			allowed = status.LocalAllow.MCPRawShell
		}
		if !allowed {
			return codedError("agent_local_gate_denied", "agent local security policy denied this operation")
		}
	}
	return nil
}

func settingKeyForPrivilege(privilege string) string {
	switch privilege {
	case model.PrivilegeRemoteOperations:
		return settingMCPRemoteOperationsEnabled
	case model.PrivilegeRemoteExec:
		return settingMCPStructuredExecEnabled
	case model.PrivilegeRemoteShell:
		return settingMCPRawShellEnabled
	default:
		return settingMCPStructuredExecEnabled
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func newRemoteExecPayload(serverID, grantID int64, privilege, mode string, argv []string, shell, cwd string, timeout int) (model.RemoteExecTaskPayload, error) {
	requestID, err := security.RandomToken(16)
	if err != nil {
		return model.RemoteExecTaskPayload{}, err
	}
	now := time.Now().UTC()
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		timeout = 300
	}
	if strings.TrimSpace(cwd) == "" {
		cwd = "/"
	}
	payload := model.RemoteExecTaskPayload{
		RequestID: requestID, Origin: model.RemoteExecOriginMCP, Privilege: privilege,
		GrantID: grantID, ServerID: serverID, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
		Command: model.RemoteExecCommand{Mode: mode, Argv: argv, Shell: shell, Cwd: cwd},
		Limits:  model.RemoteExecLimits{TimeoutSeconds: timeout, StdoutBytes: 1 << 20, StderrBytes: 1 << 20},
	}
	if mode != model.RemoteExecModeArgv && mode != model.RemoteExecModeShell {
		return model.RemoteExecTaskPayload{}, errors.New("unsupported exec mode")
	}
	return payload, nil
}

func newRemoteOperationPayload(serverID, grantID int64, kind, service string, lines int) (model.RemoteOperationTaskPayload, error) {
	requestID, err := security.RandomToken(16)
	if err != nil {
		return model.RemoteOperationTaskPayload{}, err
	}
	now := time.Now().UTC()
	return model.RemoteOperationTaskPayload{
		RequestID: requestID, Origin: model.RemoteExecOriginMCP, Kind: kind,
		GrantID: grantID, ServerID: serverID, IssuedAt: now, ExpiresAt: now.Add(time.Minute),
		Service: service, Lines: lines,
	}, nil
}
