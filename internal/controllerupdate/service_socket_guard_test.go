package controllerupdate

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Serve used to unlink the socket path unconditionally, so a second updater
// took the control channel away from the one systemd was running: the displaced
// process kept serving an unlinked inode no client could reach, and Controller
// update requests went to the newcomer instead. A production host was found
// with three processes listening on this path.
func TestServeRefusesToTakeALiveSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updater.sock")
	incumbent, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer incumbent.Close()
	go func() {
		for {
			conn, acceptErr := incumbent.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = NewService(ServiceConfig{SocketPath: path}).Serve(ctx)
	if err == nil || !strings.Contains(err.Error(), "already serving") {
		t.Fatalf("Serve = %v, want a refusal naming the live socket", err)
	}
	// The incumbent must still own the path it was serving.
	if incumbent.Addr().String() != path {
		t.Fatalf("incumbent address = %q, want %q", incumbent.Addr(), path)
	}
	conn, dialErr := net.DialTimeout("unix", path, time.Second)
	if dialErr != nil {
		t.Fatalf("live socket no longer reachable: %v", dialErr)
	}
	_ = conn.Close()
}

// A socket file left behind by a process that is gone is not a live one.
func TestServeReplacesAStaleSocketFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updater.sock")
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	if err := errIfSocketIsLive(path); err != nil {
		t.Fatalf("stale socket treated as live: %v", err)
	}
}
