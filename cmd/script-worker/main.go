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

	"github.com/OboardProject/oboard/internal/scripting"
	"github.com/OboardProject/oboard/internal/scriptrpc"
	"github.com/OboardProject/oboard/internal/scriptruntime"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

func main() {
	if hasFlag(os.Args[1:], "-runner") || os.Getenv("OBOARD_SCRIPT_RUNNER") == "1" {
		scriptruntime.ServeRunner()
		return
	}
	showVersion := flag.Bool("version", false, "print version and exit")
	socketPath := flag.String("socket", env("OBOARD_SCRIPT_WORKER_SOCKET", "/run/oboard/script-worker/rpc.sock"), "Controller Script Worker Unix socket")
	pollInterval := flag.Duration("poll-interval", 2*time.Second, "idle queue poll interval")
	flag.Parse()
	if *showVersion {
		fmt.Println("OBoard Script Worker", version.String())
		return
	}
	random, err := security.RandomToken(12)
	if err != nil {
		log.Fatal(err)
	}
	workerID := "scw_" + random
	client := unixHTTPClient(*socketPath)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("OBoard Script Worker %s started", workerID)
	isolation := scripting.ProbeIsolation()
	if !isolation.Available {
		log.Printf("script isolation unavailable: %s", isolation.Reason)
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

func beat(ctx context.Context, client *http.Client, workerID string, isolation scripting.IsolationStatus) {
	body, _ := json.Marshal(scriptrpc.HeartbeatRequest{WorkerID: workerID, IsolationAvailable: isolation.Available, IsolationMode: isolation.Mode, IsolationReason: isolation.Reason})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://script/v1/scripts/heartbeat", bytes.NewReader(body))
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func leaseAndRun(ctx context.Context, client *http.Client, workerID string, isolation scripting.IsolationStatus) {
	body, _ := json.Marshal(scriptrpc.LeaseRequest{WorkerID: workerID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://script/v1/scripts/lease", bytes.NewReader(body))
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var leased scriptrpc.LeaseResponse
	if json.Unmarshal(raw, &leased) != nil || leased.Run == nil {
		return
	}
	if !isolation.Available && os.Getenv("OBOARD_SCRIPT_TEST_ISOLATION") != "1" {
		complete(ctx, client, workerID, leased.Run, "failed", "runtime_unavailable", json.RawMessage(`{"error":"isolation unavailable"}`), nil)
		return
	}
	logs := []scriptrpc.LogLine{}
	result, runErr := scriptruntime.RunIsolated(ctx, "", scriptruntime.RunnerRequest{
		Source: leased.Run.Source, Params: leased.Run.Params, Env: leased.Run.Env, Limits: leased.Run.Limits,
	}, func(capability string, arguments json.RawMessage, actionKey string) (scriptrpc.SDKResponse, error) {
		payload, _ := json.Marshal(scriptrpc.SDKRequest{WorkerID: workerID, RunUUID: leased.Run.UUID, LeaseGeneration: leased.Run.LeaseGeneration, Capability: capability, ActionKey: actionKey, Arguments: arguments})
		sdkReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://script/v1/scripts/sdk", bytes.NewReader(payload))
		sdkResp, err := client.Do(sdkReq)
		if err != nil {
			return scriptrpc.SDKResponse{OK: false, ErrorCode: "internal_error", Message: err.Error()}, nil
		}
		defer sdkResp.Body.Close()
		out, _ := io.ReadAll(io.LimitReader(sdkResp.Body, 256<<10))
		var decoded scriptrpc.SDKResponse
		if json.Unmarshal(out, &decoded) != nil {
			return scriptrpc.SDKResponse{OK: false, ErrorCode: "internal_error", Message: "invalid SDK gateway response"}, nil
		}
		return decoded, nil
	}, func(level, message string, fields json.RawMessage) {
		logs = append(logs, scriptrpc.LogLine{Seq: int64(len(logs) + 1), Level: level, Message: message, Fields: fields})
	})
	status := "succeeded"
	errorCode := ""
	if runErr != nil {
		status = "failed"
		errorCode = "internal_error"
		if strings.Contains(runErr.Error(), "timed out") {
			status = "timed_out"
			errorCode = "operation_expired"
		}
		result = json.RawMessage(`{"error":` + jsonQuote(runErr.Error()) + `}`)
	}
	complete(ctx, client, workerID, leased.Run, status, errorCode, result, logs)
}

func complete(ctx context.Context, client *http.Client, workerID string, run *scriptrpc.RunLease, status, errorCode string, result json.RawMessage, logs []scriptrpc.LogLine) {
	body, _ := json.Marshal(scriptrpc.CompleteRequest{WorkerID: workerID, RunUUID: run.UUID, LeaseGeneration: run.LeaseGeneration, Status: status, ErrorCode: errorCode, Result: result, Logs: logs})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://script/v1/scripts/complete", bytes.NewReader(body))
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
