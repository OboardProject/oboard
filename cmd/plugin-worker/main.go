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

	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/pluginruntime"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

func main() {
	if hasFlag(os.Args[1:], "-isolation-probe") {
		for _, path := range []string{"/etc", "/home", "/run", "/sys", "/opt"} {
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
		file, err := os.CreateTemp("/tmp", "probe-")
		if err != nil {
			os.Exit(1)
		}
		_ = file.Close()
		_ = os.Remove(file.Name())
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
	workerID := "plw_" + random
	client := unixHTTPClient(*socketPath)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("OBoard Plugin Worker %s started", workerID)
	isolation := plugin.ProbeIsolation()
	if !isolation.Available {
		log.Printf("plugin isolation unavailable: %s", isolation.Reason)
	}
	ticker := time.NewTicker(*pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			beat(ctx, client, workerID, isolation)
			leaseAndRun(ctx, client, workerID, isolation)
		}
	}
}

func beat(ctx context.Context, client *http.Client, workerID string, isolation plugin.IsolationStatus) {
	body, _ := json.Marshal(pluginrpc.HeartbeatRequest{WorkerID: workerID, IsolationAvailable: isolation.Available, IsolationMode: isolation.Mode, IsolationReason: isolation.Reason})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://plugin/v1/plugins/heartbeat", bytes.NewReader(body))
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func leaseAndRun(ctx context.Context, client *http.Client, workerID string, isolation plugin.IsolationStatus) {
	body, _ := json.Marshal(pluginrpc.LeaseRequest{WorkerID: workerID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://plugin/v1/plugins/lease", bytes.NewReader(body))
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var leased pluginrpc.LeaseResponse
	if json.Unmarshal(raw, &leased) != nil || leased.Run == nil {
		return
	}
	if !isolation.Available {
		complete(ctx, client, workerID, leased.Run, "failed", "runtime_unavailable", json.RawMessage(`{"error":"isolation unavailable"}`), nil)
		return
	}
	runCtx, cancelRun := context.WithTimeout(ctx, time.Duration(leased.Run.Limits.TimeoutSeconds)*time.Second)
	defer cancelRun()
	stopWatch := watchCancellation(runCtx, client, leased.Run, cancelRun, time.Second)
	defer stopWatch()
	logs := []pluginrpc.LogLine{}
	result, runErr := pluginruntime.RunIsolated(runCtx, "", pluginruntime.RunnerRequest{
		Source: leased.Run.Source, Params: leased.Run.Params, Env: leased.Run.Env, Limits: leased.Run.Limits,
	}, func(capability string, arguments json.RawMessage, actionKey string) (pluginrpc.SDKResponse, error) {
		payload, _ := json.Marshal(pluginrpc.SDKRequest{WorkerID: workerID, RunUUID: leased.Run.UUID, LeaseGeneration: leased.Run.LeaseGeneration, Capability: capability, ActionKey: actionKey, Arguments: arguments})
		sdkReq, _ := http.NewRequestWithContext(runCtx, http.MethodPost, "http://plugin/v1/plugins/sdk", bytes.NewReader(payload))
		sdkResp, err := client.Do(sdkReq)
		if err != nil {
			return pluginrpc.SDKResponse{OK: false, ErrorCode: "internal_error", Message: err.Error()}, nil
		}
		defer sdkResp.Body.Close()
		out, _ := io.ReadAll(io.LimitReader(sdkResp.Body, 256<<10))
		var decoded pluginrpc.SDKResponse
		if json.Unmarshal(out, &decoded) != nil {
			return pluginrpc.SDKResponse{OK: false, ErrorCode: "internal_error", Message: "invalid SDK gateway response"}, nil
		}
		return decoded, nil
	}, func(level, message string, fields json.RawMessage) {
		logs = append(logs, pluginrpc.LogLine{Seq: int64(len(logs) + 1), Level: level, Message: message, Fields: fields})
	})
	stopWatch()
	if err := runCtx.Err(); err != nil {
		runErr = err
	}
	status := "succeeded"
	errorCode := ""
	if runErr != nil {
		status = "failed"
		errorCode = plugin.CodeOf(runErr)
		if errorCode == "" {
			errorCode = "internal_error"
		}
		if runErr == context.Canceled {
			status = "cancelled"
			errorCode = "cancelled"
		} else if strings.Contains(runErr.Error(), "timed out") || runErr == context.DeadlineExceeded || runCtx.Err() == context.DeadlineExceeded {
			status = "timed_out"
			errorCode = "operation_expired"
		}
		result = json.RawMessage(`{"error":` + jsonQuote(runErr.Error()) + `}`)
	}
	complete(ctx, client, workerID, leased.Run, status, errorCode, result, logs)
}

func watchCancellation(ctx context.Context, client *http.Client, run *pluginrpc.RunLease, cancelRun context.CancelFunc, interval time.Duration) func() {
	watchCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if runCancelled(watchCtx, client, run) {
				if watchCtx.Err() == nil {
					cancelRun()
				}
				return
			}
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { stop(); <-done }
}

func runCancelled(ctx context.Context, client *http.Client, run *pluginrpc.RunLease) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	body, _ := json.Marshal(pluginrpc.CancelCheckRequest{RunUUID: run.UUID, LeaseGeneration: run.LeaseGeneration})
	req, err := http.NewRequestWithContext(checkCtx, http.MethodPost, "http://plugin/v1/plugins/cancel-check", bytes.NewReader(body))
	if err != nil {
		return true
	}
	resp, err := client.Do(req)
	if err != nil {
		return true
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return true
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return true
	}
	var state struct {
		Cancelled *bool `json:"cancelled"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Cancelled == nil {
		return true
	}
	return *state.Cancelled
}

func complete(ctx context.Context, client *http.Client, workerID string, run *pluginrpc.RunLease, status, errorCode string, result json.RawMessage, logs []pluginrpc.LogLine) {
	body, _ := json.Marshal(pluginrpc.CompleteRequest{WorkerID: workerID, RunUUID: run.UUID, LeaseGeneration: run.LeaseGeneration, Status: status, ErrorCode: errorCode, Result: result, Logs: logs})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://plugin/v1/plugins/complete", bytes.NewReader(body))
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func unixHTTPClient(socketPath string) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}, DisableCompression: true, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Transport: transport, Timeout: 35 * time.Second}
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

func jsonQuote(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
