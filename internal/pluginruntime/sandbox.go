package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginrpc"
)

type RunnerRequest struct {
	Source string              `json:"source"`
	Params json.RawMessage     `json:"params"`
	Env    map[string]string   `json:"env"`
	Limits pluginrpc.RunLimits `json:"limits"`
}

func RunIsolated(ctx context.Context, self string, req RunnerRequest, call SDKFunc, log LogFunc) (json.RawMessage, error) {
	if req.Limits.TimeoutSeconds <= 0 || req.Limits.TimeoutSeconds > plugin.MaxTimeoutSeconds {
		return nil, plugin.Coded("runtime_unavailable", "invalid execution deadline")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	if self == "" {
		var err error
		self, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	defer pw.Close()
	cr, cw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer cr.Close()
	defer cw.Close()
	cmd, cleanup, err := plugin.IsolatedCommand(ctx, self, req.Limits.MemoryMiB, "-runner")
	if err != nil {
		return nil, plugin.Coded("runtime_unavailable", err.Error())
	}
	defer cleanup()
	cmd.ExtraFiles = []*os.File{pr, cw}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	stopClosing := context.AfterFunc(ctx, func() { _ = cr.Close(); _ = pw.Close() })
	defer stopClosing()
	_ = pr.Close()
	_ = cw.Close()
	enc := json.NewEncoder(pw)
	dec := json.NewDecoder(cr)
	if err := enc.Encode(req); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	for {
		var msg map[string]json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, err
		}
		kind := string(msg["kind"])
		switch jsonString(kind) {
		case "sdk":
			var sdk struct {
				Capability string          `json:"capability"`
				Arguments  json.RawMessage `json:"arguments"`
				ActionKey  string          `json:"action_key"`
			}
			_ = json.Unmarshal(msg["payload"], &sdk)
			resp, err := call(sdk.Capability, sdk.Arguments, sdk.ActionKey)
			if err != nil {
				resp = pluginrpc.SDKResponse{OK: false, ErrorCode: "internal_error", Message: err.Error()}
			}
			if err := enc.Encode(map[string]any{"kind": "sdk_result", "payload": resp}); err != nil {
				_ = cmd.Process.Kill()
				return nil, err
			}
		case "log":
			var line struct {
				Level   string `json:"level"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(msg["payload"], &line)
			if log != nil {
				log(line.Level, line.Message, nil)
			}
		case "result":
			_ = cmd.Process.Kill()
			return msg["payload"], nil
		case "error":
			_ = cmd.Process.Kill()
			return nil, errors.New(jsonString(string(msg["payload"])))
		}
	}
}

func ServeRunner() {
	reqFile := os.NewFile(3, "req")
	respFile := os.NewFile(4, "resp")
	if reqFile == nil || respFile == nil {
		os.Exit(2)
	}
	dec := json.NewDecoder(reqFile)
	enc := json.NewEncoder(respFile)
	var req RunnerRequest
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(map[string]any{"kind": "error", "payload": err.Error()})
		os.Exit(1)
	}
	result, err := Execute(req.Source, req.Params, req.Env, req.Limits, func(capability string, arguments json.RawMessage, actionKey string) (pluginrpc.SDKResponse, error) {
		if err := enc.Encode(map[string]any{"kind": "sdk", "payload": map[string]any{"capability": capability, "arguments": arguments, "action_key": actionKey}}); err != nil {
			return pluginrpc.SDKResponse{}, err
		}
		var reply map[string]json.RawMessage
		if err := dec.Decode(&reply); err != nil {
			return pluginrpc.SDKResponse{}, err
		}
		var resp pluginrpc.SDKResponse
		if err := json.Unmarshal(reply["payload"], &resp); err != nil {
			return pluginrpc.SDKResponse{}, err
		}
		return resp, nil
	}, func(level, message string, _ json.RawMessage) {
		_ = enc.Encode(map[string]any{"kind": "log", "payload": map[string]any{"level": level, "message": message}})
	})
	if err != nil {
		_ = enc.Encode(map[string]any{"kind": "error", "payload": err.Error()})
		os.Exit(1)
	}
	_ = enc.Encode(map[string]any{"kind": "result", "payload": json.RawMessage(result)})
}

func jsonString(raw string) string {
	if len(raw) >= 2 && raw[0] == '"' {
		var s string
		if json.Unmarshal([]byte(raw), &s) == nil {
			return s
		}
	}
	return raw
}
