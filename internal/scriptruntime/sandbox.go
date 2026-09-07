package scriptruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/OboardProject/oboard/internal/scripting"
	"github.com/OboardProject/oboard/internal/scriptrpc"
)

type RunnerRequest struct {
	Source string            `json:"source"`
	Params json.RawMessage   `json:"params"`
	Env    map[string]string `json:"env"`
	Limits scriptrpc.RunLimits `json:"limits"`
}

func RunIsolated(ctx context.Context, self string, req RunnerRequest, call SDKFunc, log LogFunc) (json.RawMessage, error) {
	isolation := scripting.ProbeIsolation()
	if !isolation.Available && os.Getenv("OBOARD_SCRIPT_TEST_ISOLATION") != "1" {
		return nil, scripting.Coded("runtime_unavailable", isolation.Reason)
	}
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
	cr, cw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, "-runner")
	if isolation.Available {
		cmd = bubblewrapCommand(ctx, self, req.Limits)
	}
	cmd.ExtraFiles = []*os.File{pr, cw}
	cmd.Env = []string{"OBOARD_SCRIPT_RUNNER=1"}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	_ = pr.Close()
	_ = cw.Close()
	enc := json.NewEncoder(pw)
	dec := json.NewDecoder(cr)
	if err := enc.Encode(req); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for {
		var msg map[string]json.RawMessage
		if err := dec.Decode(&msg); err != nil {
			select {
			case waitErr := <-done:
				if waitErr != nil {
					return nil, waitErr
				}
			default:
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
				resp = scriptrpc.SDKResponse{OK: false, ErrorCode: "internal_error", Message: err.Error()}
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
	result, err := Execute(req.Source, req.Params, req.Env, req.Limits, func(capability string, arguments json.RawMessage, actionKey string) (scriptrpc.SDKResponse, error) {
		if err := enc.Encode(map[string]any{"kind": "sdk", "payload": map[string]any{"capability": capability, "arguments": arguments, "action_key": actionKey}}); err != nil {
			return scriptrpc.SDKResponse{}, err
		}
		var reply map[string]json.RawMessage
		if err := dec.Decode(&reply); err != nil {
			return scriptrpc.SDKResponse{}, err
		}
		var resp scriptrpc.SDKResponse
		if err := json.Unmarshal(reply["payload"], &resp); err != nil {
			return scriptrpc.SDKResponse{}, err
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

func bubblewrapCommand(ctx context.Context, self string, limits scriptrpc.RunLimits) *exec.Cmd {
	args := []string{
		"--unshare-net", "--unshare-pid", "--unshare-ipc", "--die-with-parent",
		"--ro-bind", "/", "/",
		"--tmpfs", "/tmp",
		"--proc", "/proc",
		"--dev", "/dev",
		"--chdir", "/tmp",
		self, "-runner",
	}
	cmd := exec.CommandContext(ctx, "bwrap", args...)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	_ = filepath.Dir(self)
	_ = fmt.Sprintf("%d", limits.MemoryMiB)
	if runtime.GOOS == "linux" {
		cmd.Env = []string{"OBOARD_SCRIPT_RUNNER=1"}
	}
	return cmd
}

func jsonString(raw string) string {
	raw = string(bytesTrim(raw))
	if len(raw) >= 2 && raw[0] == '"' {
		var s string
		if json.Unmarshal([]byte(raw), &s) == nil {
			return s
		}
	}
	return raw
}

func bytesTrim(raw string) []byte {
	return []byte(raw)
}

func copyPipe(dst io.Writer, src io.Reader) {
	_, _ = io.Copy(dst, src)
}
