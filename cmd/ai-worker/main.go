package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/aiprovider/anthropic"
	"github.com/OboardProject/oboard/internal/aiprovider/openai"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

type workerRuntime struct {
	router   *aiprovider.Router
	registry aiprovider.Registry
}

func newWorkerRuntime() *workerRuntime {
	client := aiprovider.NewHTTPClient()
	registry := aiprovider.Registry{
		aiprovider.APIStyleOpenAIResponses:       openai.NewResponsesClient(client),
		aiprovider.APIStyleOpenAIChatCompletions: openai.NewChatClient(client),
		aiprovider.APIStyleAnthropicMessages:     anthropic.NewMessagesClient(client),
	}
	return &workerRuntime{router: aiprovider.NewRouter(registry), registry: registry}
}

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	socketPath := flag.String("socket", env("OBOARD_AI_WORKER_SOCKET", "/run/oboard/ai-worker/rpc.sock"), "Controller AI Worker Unix socket")
	pollInterval := flag.Duration("poll-interval", 2*time.Second, "idle queue poll interval")
	flag.Parse()
	if *showVersion {
		fmt.Println("OBoard AI Worker", version.String())
		return
	}
	if *pollInterval < 500*time.Millisecond || *pollInterval > time.Minute {
		log.Fatal("poll interval must be between 500ms and 1m")
	}
	random, err := security.RandomToken(12)
	if err != nil {
		log.Fatal(err)
	}
	workerID := "aiw_" + random
	controller := unixHTTPClient(*socketPath)
	runtime := newWorkerRuntime()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("OBoard AI Worker %s started", workerID)
	var workers sync.WaitGroup
	workers.Add(3)
	go func() { defer workers.Done(); runAuditLoop(ctx, runtime, controller, workerID, *pollInterval) }()
	go func() { defer workers.Done(); runModelDiscoveryLoop(ctx, runtime, controller, workerID, *pollInterval) }()
	go func() { defer workers.Done(); runAITestLoop(ctx, runtime, controller, workerID, *pollInterval) }()
	<-ctx.Done()
	workers.Wait()
}

func unixHTTPClient(socketPath string) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}, DisableCompression: true, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Transport: transport, Timeout: 35 * time.Second}
}

func rpcJSON(ctx context.Context, client *http.Client, method, target string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Controller RPC returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output)
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func bounded(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
func callbackContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}
func logLoopError(prefix string, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("%s: %v", prefix, err)
	}
}
