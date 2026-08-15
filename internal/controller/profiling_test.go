package controller

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return fmt.Sprint(port)
}

func TestStartProfilingRejectsNonLoopback(t *testing.T) {
	srv := &Server{}
	if err := srv.StartProfiling(context.Background(), "0.0.0.0:6060"); err == nil {
		t.Fatal("non-loopback profiling address was accepted")
	}
	if err := srv.StartProfiling(context.Background(), "192.168.1.5:6060"); err == nil {
		t.Fatal("private-network profiling address was accepted")
	}
	if err := srv.StartProfiling(context.Background(), "example.com:6060"); err == nil {
		t.Fatal("hostname profiling address was accepted")
	}
	// Empty address is the default: profiling stays disabled.
	if err := srv.StartProfiling(context.Background(), ""); err != nil {
		t.Fatalf("empty profiling address should be a no-op: %v", err)
	}
}

func TestStartProfilingEndpoints(t *testing.T) {
	srv := &Server{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address := "127.0.0.1:" + freeLoopbackPort(t)
	if err := srv.StartProfiling(ctx, address); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/vars"} {
		response, err := client.Get("http://" + address + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d", path, response.StatusCode)
		}
	}
}
