// oboard-plugin-worker leases plugin runs from the Controller over a Unix
// socket and executes each one in a disposable, isolated runner. The worker
// itself only speaks to the Controller socket; it never opens network
// connections, and runners have no network namespace at all.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/pluginruntime"
	"github.com/OboardProject/oboard/internal/pluginsandbox"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

func main() {
	if hasFlag(os.Args[1:], "-isolation-probe") {
		pluginruntime.ServeIsolationProbe()
		return
	}
	if hasFlag(os.Args[1:], "-runner") {
		pluginruntime.ServeRunner()
		return
	}
	showVersion := flag.Bool("version", false, "print version and exit")
	socketPath := flag.String("socket", env("OBOARD_PLUGIN_WORKER_SOCKET", "/run/oboard/plugin-worker/rpc.sock"), "Controller Plugin Worker Unix socket")
	pollInterval := flag.Duration("poll-interval", 2*time.Second, "idle queue poll interval")
	flag.Parse()
	if *showVersion {
		fmt.Println("OBoard Plugin Worker", version.String())
		return
	}
	random, err := security.RandomToken(12)
	if err != nil {
		log.Fatal(err)
	}
	worker := &worker{id: "plw_" + random, client: unixHTTPClient(*socketPath)}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("OBoard Plugin Worker %s started", worker.id)
	worker.isolation = pluginsandbox.ProbeIsolation()
	if !worker.isolation.Available {
		log.Printf("plugin isolation unavailable: %s", worker.isolation.Reason)
	}
	ticker := time.NewTicker(*pollInterval)
	defer ticker.Stop()
	for {
		worker.heartbeat(ctx)
		for worker.isolation.Available && worker.leaseAndRun(ctx) {
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type worker struct {
	id        string
	client    *http.Client
	isolation pluginsandbox.IsolationStatus
}

func (w *worker) post(ctx context.Context, path string, body any, out any, limit int64) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://plugin"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := w.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("controller answered %d", response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (w *worker) heartbeat(ctx context.Context) {
	request := pluginrpc.HeartbeatRequest{WorkerID: w.id, ProtocolVersion: pluginrpc.ProtocolVersion, IsolationAvailable: w.isolation.Available, IsolationMode: w.isolation.Mode, IsolationReason: w.isolation.Reason}
	_ = w.post(ctx, "/rpc/plugins/heartbeat", request, nil, 4096)
}

// leaseAndRun executes at most one run and reports whether it found work.
func (w *worker) leaseAndRun(ctx context.Context) bool {
	var leased pluginrpc.LeaseResponse
	if err := w.post(ctx, "/rpc/plugins/lease", pluginrpc.LeaseRequest{WorkerID: w.id}, &leased, 4<<20); err != nil || leased.Run == nil {
		return false
	}
	run := leased.Run
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	stopWatch := w.watchCancellation(runCtx, run, cancelRun)
	report := pluginruntime.RunIsolated(runCtx, "", pluginruntime.RunnerRequest{Source: run.Source, Environment: run.Environment, Context: run.Context, Limits: run.Limits}, func(method string, arguments json.RawMessage) pluginrpc.CallResponse {
		var response pluginrpc.CallResponse
		request := pluginrpc.CallRequest{WorkerID: w.id, RunUUID: run.RunUUID, LeaseGeneration: run.LeaseGeneration, Method: method, Arguments: arguments}
		if err := w.post(runCtx, "/rpc/plugins/call", request, &response, 2<<20); err != nil {
			if runCtx.Err() != nil {
				return pluginrpc.CallResponse{Code: "CANCELLED", Message: "the run was cancelled"}
			}
			return pluginrpc.CallResponse{Code: "RUNTIME_UNAVAILABLE", Message: "the capability gateway is unavailable"}
		}
		return response
	})
	stopWatch()
	status := statusFor(report.Outcome.Code)
	complete := pluginrpc.CompleteRequest{WorkerID: w.id, RunUUID: run.RunUUID, LeaseGeneration: run.LeaseGeneration, Status: status, ErrorCode: report.Outcome.Code, ErrorMessage: report.Outcome.Message, Result: report.Outcome.Result, Logs: report.Logs, DroppedLogs: report.Dropped}
	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := w.post(completeCtx, "/rpc/plugins/complete", complete, nil, 4096); err != nil {
		log.Printf("report plugin run %s: %v", run.RunUUID, err)
	}
	return true
}

// statusFor maps a runtime outcome code to the PluginRun terminal status.
func statusFor(code string) string {
	switch code {
	case "":
		return "succeeded"
	case "RUN_TIMEOUT":
		return "timeout"
	case "CANCELLED":
		return "cancelled"
	case "RESOURCE_LIMIT", "LIMIT_EXCEEDED", "STATE_QUOTA_EXCEEDED":
		return "resource_limit"
	case "CAPABILITY_DENIED", "RESOURCE_DENIED", "PERMISSION_REVIEW_REQUIRED":
		return "permission_denied"
	default:
		return "failed"
	}
}

func (w *worker) watchCancellation(ctx context.Context, run *pluginrpc.RunLease, cancelRun context.CancelFunc) func() {
	watchCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
			}
			var state pluginrpc.CancelCheckResponse
			checkCtx, cancel := context.WithTimeout(watchCtx, 2*time.Second)
			err := w.post(checkCtx, "/rpc/plugins/cancel-check", pluginrpc.CancelCheckRequest{RunUUID: run.RunUUID, LeaseGeneration: run.LeaseGeneration}, &state, 4096)
			cancel()
			if watchCtx.Err() != nil {
				return
			}
			// An unreachable Controller also stops the run: a runner must
			// never outlive the authority that leased it.
			if err != nil || state.Cancelled {
				cancelRun()
				return
			}
		}
	}()
	return func() { stop(); <-done }
}

func unixHTTPClient(socketPath string) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}, Proxy: nil, DisableCompression: true, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Transport: transport, Timeout: 60 * time.Second}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}
