package pluginruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/pluginsandbox"
)

func TestRunnerStdioHelper(t *testing.T) {
	if os.Getenv("OBOARD_TEST_RUNNER_STDIO") != "1" {
		return
	}
	serveRunnerRPC(os.Stdin, os.Stdout)
	os.Exit(0)
}

func TestRunnerRPCOverStdio(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerStdioHelper$")
	cmd.Env = append(os.Environ(), "OBOARD_TEST_RUNNER_STDIO=1")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = input.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	encoder := json.NewEncoder(input)
	request := RunnerRequest{
		Source: `function main() { log.info('started'); return oboard.servers.list(); }`,
		Limits: pluginrpc.Limits{TimeoutMS: 2000, SDKCalls: 1, ResultBytes: 4096},
	}
	if err := encoder.Encode(request); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(output)
	for _, kind := range []string{"log", "call", "done"} {
		var message frame
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		if message.Kind != kind {
			t.Fatalf("got %s, want %s: %s", message.Kind, kind, message.Payload)
		}
		switch kind {
		case "call":
			var call callFrame
			if err := json.Unmarshal(message.Payload, &call); err != nil || call.Method != "servers.list" {
				t.Fatalf("invalid SDK call: %s", message.Payload)
			}
			response := pluginrpc.CallResponse{OK: true, Result: json.RawMessage(`{"servers":[{"id":1}]}`)}
			if err := encoder.Encode(frame{Kind: "call_result", Payload: mustRaw(response)}); err != nil {
				t.Fatal(err)
			}
		case "done":
			var outcome Outcome
			if err := json.Unmarshal(message.Payload, &outcome); err != nil || outcome.Code != "" || string(outcome.Result) != `[{"id":1}]` {
				t.Fatalf("unexpected result: %s", message.Payload)
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("runner: %v; %s", err, diagnostics.String())
	}
}

func TestRunnerFailureDoesNotInventResourceExhaustion(t *testing.T) {
	if got := runnerFailure(context.Background(), &pluginsandbox.Command{}); got.Code != "RUNTIME_UNAVAILABLE" {
		t.Fatalf("unexpected exit classified as %+v", got)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := runnerFailure(cancelled, nil); got.Code != "CANCELLED" {
		t.Fatalf("cancelled run classified as %+v", got)
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if got := runnerFailure(expired, nil); got.Code != "RUN_TIMEOUT" {
		t.Fatalf("expired run classified as %+v", got)
	}
}
