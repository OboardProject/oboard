package controller

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func compressed(t *testing.T, handler http.Handler, target string, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func jsonHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

// The two heaviest panel responses were about a megabyte of repetitive JSON,
// sent in the clear on every poll.
func TestPanelJSONIsCompressed(t *testing.T) {
	body := `{"servers":[` + strings.Repeat(`{"id":1,"name":"node","status":"online"},`, 500) + `{}]}`
	srv := &Server{}
	rr := compressed(t, srv.withCompression(jsonHandler(body)), "/api/v1/ui/servers", "gzip")

	if got := rr.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rr.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want it removed for a compressed body", got)
	}
	if !strings.Contains(rr.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("Vary = %q, want Accept-Encoding", rr.Header().Get("Vary"))
	}
	reader, err := gzip.NewReader(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != body {
		t.Fatal("decompressed body does not match what the handler wrote")
	}
	if rr.Body.Len() >= len(body)/4 {
		t.Fatalf("compressed to %d bytes from %d, expected far better on repetitive JSON", rr.Body.Len(), len(body))
	}
}

// A client that did not ask for gzip must get exactly what it would have got
// before.
func TestClientWithoutGzipGetsIdentity(t *testing.T) {
	body := strings.Repeat("x", 4096)
	srv := &Server{}
	rr := compressed(t, srv.withCompression(jsonHandler(body)), "/api/v1/ui/servers", "")
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatal("a client that did not offer gzip received a compressed body")
	}
	if rr.Body.String() != body {
		t.Fatal("identity body was altered")
	}
}

// The Agent wire and subscriptions are contracts with separate implementations
// and third-party clients. Compressing them is not a panel optimisation.
func TestWireSurfacesAreNotCompressed(t *testing.T) {
	body := strings.Repeat(`{"a":1},`, 500)
	srv := &Server{}
	for _, path := range []string{"/api/v1/agent/users-snapshot", "/api/v1/subscriptions/abc", "/s/abc", "/api/v1/subscription-relay/heartbeat"} {
		rr := compressed(t, srv.withCompression(jsonHandler(body)), path, "gzip")
		if rr.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s was compressed", path)
		}
		if rr.Body.String() != body {
			t.Fatalf("%s body was altered", path)
		}
	}
}

// Below roughly a packet there is nothing to win, and gzip's own header would
// make some responses larger.
func TestSmallResponsesArePassedThrough(t *testing.T) {
	body := `{"version":"dev"}`
	srv := &Server{}
	rr := compressed(t, srv.withCompression(jsonHandler(body)), "/api/v1/ui/version", "gzip")
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatal("a tiny response was compressed")
	}
	if rr.Body.String() != body {
		t.Fatal("small body was altered")
	}
}

// A type gzip cannot help is left alone.
func TestAlreadyCompressedContentIsNotRecompressed(t *testing.T) {
	srv := &Server{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(bytes.Repeat([]byte{0x89, 0x50, 0x4e, 0x47}, 2048))
	})
	rr := compressed(t, srv.withCompression(handler), "/assets/logo.png", "gzip")
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatal("a PNG was gzipped")
	}
}

// A Range request must still receive the range it asked for.
func TestRangeRequestsAreNotCompressed(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/assets/index.js", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-99")
	rr := httptest.NewRecorder()
	srv.withCompression(jsonHandler(strings.Repeat("y", 8192))).ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatal("a range request was compressed")
	}
}

// The status a handler chose must survive the deferred header write.
func TestStatusIsPreservedThroughCompression(t *testing.T) {
	srv := &Server{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, strings.Repeat(`{"error":"conflict"},`, 200))
	})
	rr := compressed(t, srv.withCompression(handler), "/api/v1/ui/servers", "gzip")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("error body was not compressed")
	}
}

// A handler that writes no body must not gain headers it never set.
func TestEmptyResponseIsUntouched(t *testing.T) {
	srv := &Server{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	rr := compressed(t, srv.withCompression(handler), "/api/v1/ui/servers", "gzip")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if rr.Header().Get("Content-Encoding") != "" {
		t.Fatal("an empty response was given a Content-Encoding")
	}
}
