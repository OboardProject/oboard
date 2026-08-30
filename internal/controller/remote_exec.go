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

func settingKeyForPrivilege(privilege string) string {
	switch privilege {
	case model.PrivilegeRemoteOperations:
		return settingMCPRemoteOperationsEnabled
	case model.PrivilegeRemoteExec:
		return settingMCPStructuredExecEnabled
	case model.PrivilegeRemoteShell:
		return settingMCPRawShellEnabled
	case model.PrivilegeRemoteInteractive:
		return settingMCPInteractiveTerminalEnabled
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
