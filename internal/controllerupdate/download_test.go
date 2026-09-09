package controllerupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func downloadFixture(t *testing.T, handler http.HandlerFunc) (*Service, remoteRelease, Artifact) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	sum := sha256.Sum256([]byte("abcdefgh"))
	a := Artifact{Name: "controller.tar.gz", SHA256: hex.EncodeToString(sum[:]), Size: 8}
	root := t.TempDir()
	service := NewService(ServiceConfig{WorkRoot: root, StatePath: filepath.Join(root, "status.json"), HTTPClient: server.Client(), DownloadRetryDelay: time.Millisecond, DownloadIdleTimeout: time.Second, DownloadTimeout: 2 * time.Second})
	return service, remoteRelease{Manifest: Manifest{Build: "test-build"}, Assets: map[string]string{a.Name: server.URL}}, a
}

func TestDownloadResumesInterruptedResponse(t *testing.T) {
	var requests atomic.Int32
	s, release, a := downloadFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"same-object"`)
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", "8")
			io.WriteString(w, "abcd")
			return
		}
		if r.Header.Get("Range") != "bytes=4-" || r.Header.Get("If-Range") != `"same-object"` {
			t.Errorf("resume headers: %v", r.Header)
		}
		w.Header().Set("Content-Range", "bytes 4-7/8")
		w.WriteHeader(http.StatusPartialContent)
		io.WriteString(w, "efgh")
	})
	path, err := s.downloadControllerArchive(t.Context(), release, a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "abcdefgh" {
		t.Fatalf("download: %q %v", got, err)
	}
	if d := s.status.Download; d == nil || !d.Complete || d.Bytes != 8 || d.Attempt != 2 {
		t.Fatalf("progress: %+v", d)
	}
}

func TestDownloadRetriesTransientHTTP(t *testing.T) {
	var requests atomic.Int32
	s, release, a := downloadFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "abcdefgh")
	})
	if _, err := s.downloadControllerArchive(t.Context(), release, a); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 {
		t.Fatalf("requests=%d", requests.Load())
	}
}

func TestDownloadRejectsUntrustedResponses(t *testing.T) {
	for _, kind := range []string{"range", "length", "hash", "oversize", "unauthorized"} {
		t.Run(kind, func(t *testing.T) {
			var requests atomic.Int32
			s, release, a := downloadFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				switch kind {
				case "range":
					w.Header().Set("Content-Range", "bytes 0-7/9")
					w.WriteHeader(206)
					io.WriteString(w, "abcdefgh")
				case "length":
					io.WriteString(w, "short")
				case "hash":
					io.WriteString(w, "wrong!!!")
				case "oversize":
					w.(http.Flusher).Flush()
					io.WriteString(w, "abcdefghx")
				case "unauthorized":
					w.WriteHeader(403)
				}
			})
			if _, err := s.downloadControllerArchive(t.Context(), release, a); err == nil {
				t.Fatal("accepted untrusted response")
			}
			if requests.Load() != 1 {
				t.Fatalf("retried permanent failure: %d", requests.Load())
			}
			if _, err := os.Stat(filepath.Join(s.config.WorkRoot, "controller-"+a.SHA256+".part")); !os.IsNotExist(err) {
				t.Fatalf("invalid partial retained: %v", err)
			}
		})
	}
}

func TestDownloadIgnoresMetadataTimeoutAndDetectsIdle(t *testing.T) {
	t.Run("slow-but-progressing", func(t *testing.T) {
		s, release, a := downloadFixture(t, func(w http.ResponseWriter, r *http.Request) {
			for _, b := range []byte("abcdefgh") {
				io.WriteString(w, string(b))
				w.(http.Flusher).Flush()
				time.Sleep(10 * time.Millisecond)
			}
		})
		s.config.HTTPClient.Timeout = time.Millisecond
		if _, err := s.downloadControllerArchive(t.Context(), release, a); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("idle", func(t *testing.T) {
		var requests atomic.Int32
		s, release, a := downloadFixture(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		s.config.DownloadIdleTimeout = 15 * time.Millisecond
		s.config.DownloadAttempts = 2
		_, err := s.downloadControllerArchive(t.Context(), release, a)
		if err == nil || !strings.Contains(err.Error(), "idle timeout") || requests.Load() != 2 {
			t.Fatalf("idle: %v requests=%d", err, requests.Load())
		}
	})
	t.Run("total-budget", func(t *testing.T) {
		s, release, a := downloadFixture(t, func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); <-r.Context().Done() })
		s.config.DownloadTimeout = 20 * time.Millisecond
		if _, err := s.downloadControllerArchive(t.Context(), release, a); err != context.DeadlineExceeded {
			t.Fatalf("total budget: %v", err)
		}
	})
}

func TestDownloadCancellationRetainsPartialAndResumesNextRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	s, release, a := downloadFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Length", "8")
			io.WriteString(w, "abcd")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.Header.Get("Range") != "bytes=4-" {
			t.Errorf("range=%s", r.Header.Get("Range"))
		}
		// A server may ignore Range. The client must truncate rather than append.
		io.WriteString(w, "abcdefgh")
	})
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); _, err := s.downloadControllerArchive(ctx, release, a); done <- err }()
	t.Cleanup(func() { cancel(); <-finished })
	deadline := time.After(time.Second)
	for {
		s.mu.Lock()
		d := s.status.Download
		received := d != nil && d.Bytes == 4
		s.mu.Unlock()
		if received {
			break
		}
		select {
		case <-deadline:
			t.Fatal("partial data not received")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatal("cancel retried")
	}
	if _, err := s.downloadControllerArchive(t.Context(), release, a); err != nil {
		t.Fatal(err)
	}
}

func TestDownloadCachedPartialIsBoundToDigest(t *testing.T) {
	var requestedRange string
	s, release, a := downloadFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requestedRange = r.Header.Get("Range")
		io.WriteString(w, "abcdefgh")
	})
	if err := os.WriteFile(filepath.Join(s.config.WorkRoot, "controller-"+strings.Repeat("0", 64)+".part"), []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.downloadControllerArchive(t.Context(), release, a); err != nil {
		t.Fatal(err)
	}
	if requestedRange != "" {
		t.Fatalf("used another artifact's partial: %s", requestedRange)
	}
	// A same-size corrupt cached artifact must never be accepted.
	if err := os.WriteFile(filepath.Join(s.config.WorkRoot, "controller-"+a.SHA256+".part"), []byte("corrupt!"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.downloadControllerArchive(t.Context(), release, a); err == nil {
		t.Fatal("accepted corrupt cache")
	}
}

func TestDownloadRetryAfterBounded(t *testing.T) {
	for _, value := range []string{"999999", "-1", "bad", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)} {
		if d := downloadRetryAfter(value); d < 0 || d > time.Minute {
			t.Fatal(fmt.Sprintf("retry-after %q: %s", value, d))
		}
	}
}
