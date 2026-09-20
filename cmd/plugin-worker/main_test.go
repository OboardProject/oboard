package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/pluginrpc"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func cancelResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestRunCancelledFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, body     string
		status         int
		transportError bool
		cancelled      bool
	}{
		{name: "active", status: 200, body: `{"cancelled":false}`},
		{name: "revoked", status: 200, body: `{"cancelled":true}`, cancelled: true},
		{name: "missing", status: 200, body: `{}`, cancelled: true},
		{name: "null", status: 200, body: `{"cancelled":null}`, cancelled: true},
		{name: "invalid", status: 200, body: `{"cancelled":"false"}`, cancelled: true},
		{name: "malformed", status: 200, body: `{`, cancelled: true},
		{name: "oversized", status: 200, body: strings.Repeat(" ", 4096) + `{"cancelled":false}`, cancelled: true},
		{name: "forbidden", status: 403, body: `{"cancelled":false}`, cancelled: true},
		{name: "unavailable", transportError: true, cancelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &pluginrpc.RunLease{UUID: "run-test", LeaseGeneration: 3}
			client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.Method != http.MethodPost || req.URL.Path != "/v1/plugins/cancel-check" {
					t.Errorf("unexpected cancel request: %s %s", req.Method, req.URL.Path)
				}
				var check pluginrpc.CancelCheckRequest
				if err := json.NewDecoder(req.Body).Decode(&check); err != nil || check.RunUUID != run.UUID || check.LeaseGeneration != run.LeaseGeneration {
					t.Errorf("cancel check must bind run and lease generation: %+v %v", check, err)
				}
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) > 2*time.Second {
					t.Error("cancel check must have a bounded deadline")
				}
				if tc.transportError {
					return nil, errors.New("gateway unavailable")
				}
				return cancelResponse(tc.status, tc.body), nil
			})}
			if got := runCancelled(context.Background(), client, run); got != tc.cancelled {
				t.Fatalf("cancelled = %v, want %v", got, tc.cancelled)
			}
		})
	}
}

func TestWatchCancellationPollsWithoutSDKCalls(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var checks atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if checks.Add(1) == 1 {
			return cancelResponse(200, `{"cancelled":false}`), nil
		}
		return cancelResponse(200, `{"cancelled":true}`), nil
	})}
	stop := watchCancellation(ctx, client, &pluginrpc.RunLease{UUID: "run-test", LeaseGeneration: 1}, cancel, 10*time.Millisecond)
	defer stop()
	select {
	case <-ctx.Done():
		if checks.Load() < 2 {
			t.Fatal("cancelled an active lease without polling again")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runner without SDK calls did not observe cancellation")
	}
}

func TestWatchCancellationStopsAndJoinsInflightRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, finished := make(chan struct{}), make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(finished)
		return nil, req.Context().Err()
	})}
	stop := watchCancellation(ctx, client, &pluginrpc.RunLease{UUID: "run-test"}, cancel, time.Second)
	defer stop()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel check did not start")
	}
	stop()
	select {
	case <-finished:
	default:
		t.Fatal("stop did not join in-flight check")
	}
	if ctx.Err() != nil {
		t.Fatal("stopping the watcher cancelled an otherwise completed run")
	}
}

func TestWatchCancellationCancelsOnGatewayFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, errors.New("gateway unavailable")
	})}
	stop := watchCancellation(ctx, client, &pluginrpc.RunLease{UUID: "run-test"}, cancel, time.Second)
	defer stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("gateway failure left runner active")
	}
}
