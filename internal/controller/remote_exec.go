package controller

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
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
	return settingMCPEnabled
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
	if mode == model.RemoteExecModeArgv && remoteExecArgvInvokesShell(argv) {
		return model.RemoteExecTaskPayload{}, errors.New("structured exec cannot invoke a shell; use remote_shell")
	}
	return payload, nil
}

func remoteExecArgvInvokesShell(argv []string) bool {
	return remoteExecArgvInvokesShellDepth(argv, 0)
}

func remoteExecArgvInvokesShellDepth(argv []string, depth int) bool {
	if len(argv) == 0 {
		return false
	}
	if depth > 8 {
		return true
	}
	if remoteExecArgvIsShellName(argv[0]) {
		return true
	}
	if remoteExecArgvIsFind(argv[0]) && remoteExecFindInvokesShell(argv, depth) {
		return true
	}
	if !remoteExecArgvIsCommandWrapper(argv[0]) {
		return false
	}
	cmd, rest, ok := remoteExecSplitWrappedCommand(argv)
	if !ok {
		return false
	}
	return remoteExecArgvInvokesShellDepth(append([]string{cmd}, rest...), depth+1)
}

func remoteExecArgvBase(arg string) string {
	return strings.ToLower(filepath.Base(strings.TrimSpace(arg)))
}

func remoteExecArgvIsShellName(arg string) bool {
	switch remoteExecArgvBase(arg) {
	case "sh", "bash", "dash", "ash", "zsh", "ksh", "csh", "tcsh", "fish", "busybox", "script", "su", "runuser", "login":
		return true
	}
	return false
}

func remoteExecArgvIsFind(arg string) bool {
	return remoteExecArgvBase(arg) == "find"
}

func remoteExecFindInvokesShell(argv []string, depth int) bool {
	for i := 1; i < len(argv); i++ {
		switch strings.TrimSpace(argv[i]) {
		case "-exec", "-execdir", "-ok", "-okdir":
			if remoteExecArgvInvokesShellDepth(remoteExecFindExecArgv(argv[i+1:]), depth+1) {
				return true
			}
		}
	}
	return false
}

func remoteExecFindExecArgv(rest []string) []string {
	out := make([]string, 0, len(rest))
	for _, arg := range rest {
		if arg == ";" || arg == "+" {
			break
		}
		out = append(out, arg)
	}
	return out
}

func remoteExecArgvIsCommandWrapper(arg string) bool {
	switch remoteExecArgvBase(arg) {
	case "env", "nice", "nohup", "timeout", "stdbuf", "ionice", "chrt", "time", "watch", "xargs", "sudo", "doas", "flock", "setpriv", "capsh", "unshare", "nsenter", "setsid":
		return true
	}
	return false
}

func remoteExecLooksLikeTimeoutDuration(arg string) bool {
	if arg == "" {
		return false
	}
	digits := 0
	for i, r := range arg {
		if r >= '0' && r <= '9' || r == '.' {
			digits++
			continue
		}
		if i > 0 && (r == 's' || r == 'm' || r == 'h' || r == 'd') && i == len(arg)-1 {
			return digits > 0
		}
		return false
	}
	return digits > 0
}

func remoteExecSplitWrappedCommand(argv []string) (string, []string, bool) {
	if len(argv) < 2 {
		return "", nil, false
	}
	wrapper := remoteExecArgvBase(argv[0])
	for i := 1; i < len(argv); i++ {
		arg := strings.TrimSpace(argv[i])
		if arg == "" {
			continue
		}
		if arg == "--" {
			if i+1 >= len(argv) {
				return "", nil, false
			}
			return argv[i+1], argv[i+2:], true
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if wrapper == "env" && strings.Contains(arg, "=") && !strings.HasPrefix(arg, "/") {
			continue
		}
		if wrapper == "timeout" && remoteExecLooksLikeTimeoutDuration(arg) {
			continue
		}
		return arg, argv[i+1:], true
	}
	return "", nil, false
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
