package pluginruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/pluginsandbox"
)

// RunnerRequest is sent once from the worker to the disposable runner.
type RunnerRequest struct {
	Source      string                        `json:"source"`
	Environment map[string]pluginrpc.EnvValue `json:"environment"`
	Context     pluginrpc.RunContext          `json:"context"`
	Limits      pluginrpc.Limits              `json:"limits"`
}

type frame struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type callFrame struct {
	Method    string          `json:"method"`
	Arguments json.RawMessage `json:"arguments"`
}

type logFrame struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

// maxRunnerTraffic bounds everything a runner may write to the worker.
const maxRunnerTraffic = 32 << 20

// Report is what the worker learned from one isolated run.
type Report struct {
	Outcome Outcome
	Logs    []pluginrpc.LogLine
	Dropped int
}

// RunIsolated starts the runner inside the sandbox, relays SDK calls and
// logs, and always tears the runner and its cgroup down before returning.
func RunIsolated(ctx context.Context, self string, request RunnerRequest, call CallFunc) Report {
	report := Report{}
	timeout := time.Duration(request.Limits.TimeoutMS)*time.Millisecond + 3*time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if self == "" {
		var err error
		if self, err = os.Executable(); err != nil {
			report.Outcome = Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "worker executable unavailable"}
			return report
		}
	}
	requestRead, requestWrite, err := os.Pipe()
	if err != nil {
		report.Outcome = Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "cannot create runner pipes"}
		return report
	}
	defer requestRead.Close()
	defer requestWrite.Close()
	responseRead, responseWrite, err := os.Pipe()
	if err != nil {
		report.Outcome = Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "cannot create runner pipes"}
		return report
	}
	defer responseRead.Close()
	defer responseWrite.Close()
	command, err := pluginsandbox.IsolatedCommand(ctx, self, request.Limits.MemoryMiB, "-runner")
	if err != nil {
		report.Outcome = Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "plugin isolation unavailable"}
		return report
	}
	defer command.Close()
	command.Cmd.ExtraFiles = []*os.File{requestRead, responseWrite}
	if err := command.Cmd.Start(); err != nil {
		report.Outcome = Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "cannot start runner"}
		return report
	}
	waited := make(chan struct{})
	go func() { _ = command.Cmd.Wait(); close(waited) }()
	defer func() {
		_ = command.Cmd.Process.Kill()
		<-waited
	}()
	stop := context.AfterFunc(ctx, func() { _ = responseRead.Close(); _ = requestWrite.Close() })
	defer stop()
	_ = requestRead.Close()
	_ = responseWrite.Close()
	encoder := json.NewEncoder(requestWrite)
	if err := encoder.Encode(request); err != nil {
		report.Outcome = runnerFailure(ctx, command)
		return report
	}
	logs := newLogCollector(request.Limits)
	decoder := json.NewDecoder(&limitedReader{r: responseRead, remaining: maxRunnerTraffic})
	calls := 0
	for {
		var message frame
		if err := decoder.Decode(&message); err != nil {
			report.Outcome = runnerFailure(ctx, command)
			report.Logs, report.Dropped = logs.lines, logs.dropped
			return report
		}
		switch message.Kind {
		case "call":
			var sdk callFrame
			var response pluginrpc.CallResponse
			calls++
			switch {
			case json.Unmarshal(message.Payload, &sdk) != nil:
				response = pluginrpc.CallResponse{Code: "INVALID_ARGUMENT", Message: "malformed SDK call"}
			case request.Limits.SDKCalls > 0 && calls > request.Limits.SDKCalls:
				response = pluginrpc.CallResponse{Code: "LIMIT_EXCEEDED", Message: "SDK call budget for this run is exhausted"}
			default:
				response = call(sdk.Method, sdk.Arguments)
			}
			if err := encoder.Encode(frame{Kind: "call_result", Payload: mustRaw(response)}); err != nil {
				report.Outcome = runnerFailure(ctx, command)
				report.Logs, report.Dropped = logs.lines, logs.dropped
				return report
			}
		case "log":
			var line logFrame
			if json.Unmarshal(message.Payload, &line) == nil {
				logs.add(line.Level, line.Message)
			}
		case "done":
			var outcome Outcome
			_ = json.Unmarshal(message.Payload, &outcome)
			report.Outcome = outcome
			report.Logs, report.Dropped = logs.lines, logs.dropped
			return report
		}
	}
}

func runnerFailure(ctx context.Context, command *pluginsandbox.Command) Outcome {
	switch {
	case command.OOMKilled():
		return Outcome{Code: "RESOURCE_LIMIT", Message: "the run exceeded its memory limit"}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return Outcome{Code: "RUN_TIMEOUT", Message: "the run exceeded its time limit"}
	case errors.Is(ctx.Err(), context.Canceled):
		return Outcome{Code: "CANCELLED", Message: "the run was cancelled"}
	default:
		return Outcome{Code: "RESOURCE_LIMIT", Message: "the runner stopped unexpectedly"}
	}
}

type logCollector struct {
	limits  pluginrpc.Limits
	lines   []pluginrpc.LogLine
	bytes   int
	dropped int
}

func newLogCollector(limits pluginrpc.Limits) *logCollector {
	return &logCollector{limits: limits}
}

func (c *logCollector) add(level, message string) {
	switch level {
	case "debug", "info", "warn", "error":
	default:
		level = "info"
	}
	message = strings.ReplaceAll(message, "\x1b", "")
	if c.limits.LogLineBytes > 0 && len(message) > c.limits.LogLineBytes {
		message = message[:c.limits.LogLineBytes] + "…"
	}
	if c.limits.LogLines > 0 && len(c.lines) >= c.limits.LogLines || c.limits.LogBytes > 0 && c.bytes+len(message) > c.limits.LogBytes {
		c.dropped++
		return
	}
	c.bytes += len(message)
	c.lines = append(c.lines, pluginrpc.LogLine{Seq: int64(len(c.lines) + 1), Level: level, Message: message})
}

type limitedReader struct {
	r         io.Reader
	remaining int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.remaining <= 0 {
		return 0, errors.New("runner output limit exceeded")
	}
	if int64(len(p)) > l.remaining {
		p = p[:l.remaining]
	}
	n, err := l.r.Read(p)
	l.remaining -= int64(n)
	return n, err
}

func mustRaw(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

// ServeRunner is the entry point inside the sandbox. It hardens the process
// before reading any plugin input and never writes anywhere but fd 4.
func ServeRunner() {
	responseFile := os.NewFile(4, "response")
	requestFile := os.NewFile(3, "request")
	if responseFile == nil || requestFile == nil {
		os.Exit(2)
	}
	encoder := json.NewEncoder(responseFile)
	done := func(outcome Outcome) {
		_ = encoder.Encode(frame{Kind: "done", Payload: mustRaw(outcome)})
	}
	if err := pluginsandbox.HardenRunner(); err != nil {
		done(Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "runner hardening failed"})
		os.Exit(1)
	}
	decoder := json.NewDecoder(requestFile)
	var request RunnerRequest
	if err := decoder.Decode(&request); err != nil {
		done(Outcome{Code: "RUNTIME_UNAVAILABLE", Message: "invalid runner request"})
		os.Exit(1)
	}
	calls := 0
	outcome := Execute(request.Source, request.Environment, request.Context, request.Limits, func(method string, arguments json.RawMessage) pluginrpc.CallResponse {
		calls++
		if request.Limits.SDKCalls > 0 && calls > request.Limits.SDKCalls {
			return pluginrpc.CallResponse{Code: "LIMIT_EXCEEDED", Message: "SDK call budget for this run is exhausted"}
		}
		if err := encoder.Encode(frame{Kind: "call", Payload: mustRaw(callFrame{Method: method, Arguments: arguments})}); err != nil {
			return pluginrpc.CallResponse{Code: "CANCELLED", Message: "worker connection closed"}
		}
		var reply frame
		if err := decoder.Decode(&reply); err != nil || reply.Kind != "call_result" {
			return pluginrpc.CallResponse{Code: "CANCELLED", Message: "worker connection closed"}
		}
		var response pluginrpc.CallResponse
		if json.Unmarshal(reply.Payload, &response) != nil {
			return pluginrpc.CallResponse{Code: "INTERNAL_ERROR", Message: "invalid gateway response"}
		}
		return response
	}, func(level, message string) {
		_ = encoder.Encode(frame{Kind: "log", Payload: mustRaw(logFrame{Level: level, Message: message})})
	})
	done(outcome)
}

// ServeIsolationProbe runs inside the sandbox and fails unless the host
// filesystem, capabilities and network are really absent and the runner
// hardening blocks exec and sockets.
func ServeIsolationProbe() {
	for _, path := range []string{"/etc", "/home", "/run", "/sys", "/opt", "/var"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			os.Exit(1)
		}
	}
	if err := os.WriteFile("/probe", nil, 0600); err == nil {
		os.Exit(1)
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil || !strings.Contains(string(status), "CapEff:\t0000000000000000") {
		os.Exit(1)
	}
	dialer := net.Dialer{Timeout: 500 * time.Millisecond}
	if conn, err := dialer.Dial("tcp", "1.1.1.1:443"); err == nil {
		_ = conn.Close()
		os.Exit(1)
	}
	if err := pluginsandbox.HardenRunner(); err != nil {
		os.Exit(1)
	}
	if err := pluginsandbox.VerifyHardened("/worker"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
