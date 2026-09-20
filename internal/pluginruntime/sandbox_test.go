package pluginruntime

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginrpc"
)

// Opt in only with a static Linux release worker in a delegated cgroup v2.
// A supplied worker must pass: isolation failures are not skipped.
func TestLinuxRunnerIsolation(t *testing.T) {
	worker := os.Getenv("OBOARD_PLUGIN_ISOLATION_WORKER")
	if worker == "" {
		t.Skip("requires Linux, bubblewrap and a delegated cgroup v2")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd, cleanup, err := plugin.IsolatedCommand(ctx, worker, 64, "-isolation-probe")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		cleanup()
		t.Fatal(err)
	}
	cleanup()
	limits := pluginrpc.RunLimits{TimeoutSeconds: 5, MemoryMiB: 64, SDKCalls: 2, LogBytes: 1024, ResultBytes: 1024}
	result, err := RunIsolated(ctx, worker, RunnerRequest{Source: `function main() { return {ok: true, process: typeof process, require: typeof require}; }`, Params: json.RawMessage(`{}`), Limits: limits}, nil, nil)
	if err != nil || string(result) != `{"ok":true,"process":"undefined","require":"undefined"}` {
		t.Fatalf("isolated run: %s %v", result, err)
	}
	_, err = RunIsolated(ctx, worker, RunnerRequest{Source: `function main() { const retained = []; for (;;) { retained.push(new Array(262144).fill(1)); } }`, Limits: limits}, nil, nil)
	if err == nil || err == context.DeadlineExceeded {
		t.Fatalf("memory exhaustion was not enforced before deadline: %v", err)
	}
	cancelCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	var cancelledAt time.Time
	_, err = RunIsolated(cancelCtx, worker, RunnerRequest{Source: `function main() { log.info("started"); for (;;) {} }`, Limits: limits}, nil, func(_, _ string, _ json.RawMessage) {
		cancelledAt = time.Now()
		cancelRun()
	})
	if cancelledAt.IsZero() || err != context.Canceled || time.Since(cancelledAt) > 2*time.Second {
		t.Fatalf("CPU-only runner did not stop promptly after cancellation: %v", err)
	}
	limits.TimeoutSeconds = 1
	_, err = RunIsolated(ctx, worker, RunnerRequest{Source: `function main() { for (;;) {} }`, Limits: limits}, nil, nil)
	if err == nil {
		t.Fatal("infinite run escaped execution deadline")
	}
}

func TestRunIsolatedRejectsUnboundedDeadline(t *testing.T) {
	for _, seconds := range []int{0, -1, plugin.MaxTimeoutSeconds + 1} {
		_, err := RunIsolated(context.Background(), "", RunnerRequest{Limits: pluginrpc.RunLimits{TimeoutSeconds: seconds}}, nil, nil)
		if plugin.CodeOf(err) != "runtime_unavailable" {
			t.Fatalf("deadline %d: %v", seconds, err)
		}
	}
}
