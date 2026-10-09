package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServeControllerDrainsActiveRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = io.WriteString(w, "drained")
	})}
	defer srv.Close()
	done := make(chan error, 1)
	go func() { done <- serveController(ctx, srv, listener, 3*time.Second) }()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	response := make(chan error, 1)
	go func() {
		r, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			defer r.Body.Close()
			var body []byte
			body, err = io.ReadAll(r.Body)
			if err == nil && string(body) != "drained" {
				err = errors.New("active response was not drained")
			}
		}
		response <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("server returned before active request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeControllerClosesRequestsAfterShutdownDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started, finished := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(finished)
	})}
	defer srv.Close()
	done := make(chan error, 1)
	go func() { done <- serveController(ctx, srv, listener, 20*time.Millisecond) }()
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		if r, err := client.Get("http://" + listener.Addr().String()); err == nil {
			r.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("active request was not canceled after shutdown deadline")
	}
	<-clientDone
}

func TestDefaultListenAddress(t *testing.T) {
	if defaultListenAddress != ":2787" {
		t.Fatalf("default listen address = %q", defaultListenAddress)
	}
}

func TestValidateSessionSecret(t *testing.T) {
	for _, value := range []string{"", " ", "\t\n", "short", "persistent-secret"} {
		if err := validateSessionSecret(value); err == nil {
			t.Fatalf("validateSessionSecret(%q) succeeded", value)
		}
	}
	if err := validateSessionSecret("persistent-secret-at-least-32-chars!"); err != nil {
		t.Fatalf("valid secret rejected: %v", err)
	}
}

func TestControllerLogOutputModes(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: "both"},
		{value: "stdout", want: "stdout"},
		{value: " FILE ", want: "file"},
		{value: "Both", want: "both"},
	} {
		got, err := parseLogOutput(test.value)
		if err != nil || got != test.want {
			t.Fatalf("parseLogOutput(%q) = %q, %v; want %q", test.value, got, err, test.want)
		}
	}
	if _, err := parseLogOutput("stderr"); err == nil {
		t.Fatal("invalid log output mode was accepted")
	}

	var stdout, file bytes.Buffer
	if _, err := controllerLogWriter("both", &stdout, &file).Write([]byte("entry")); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "entry" || file.String() != "entry" {
		t.Fatalf("both output = stdout %q, file %q", stdout.String(), file.String())
	}
}

// TestControllerLogWriterRedactsStdoutSink covers a secret disclosure: the
// managed file sink redacts on write, but stdout was wired raw, so the default
// "both" mode published unredacted tokens to journald and container logs.
func TestControllerLogWriterRedactsStdoutSink(t *testing.T) {
	const entry = "agent hello Authorization: Bearer abc123.def-456 agent_token=super-secret password: hunter2\n"
	for _, mode := range []string{"stdout", "both"} {
		var stdout, file bytes.Buffer
		if _, err := controllerLogWriter(mode, &stdout, &file).Write([]byte(entry)); err != nil {
			t.Fatal(err)
		}
		got := stdout.String()
		for _, secret := range []string{"abc123.def-456", "super-secret", "hunter2"} {
			if strings.Contains(got, secret) {
				t.Fatalf("mode %s leaked %q to stdout: %s", mode, secret, got)
			}
		}
		if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "agent hello") {
			t.Fatalf("mode %s stdout = %q, expected redacted but readable", mode, got)
		}
	}
}
