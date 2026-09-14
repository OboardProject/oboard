package agentlink

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The integration test runs the real server and client stacks against each
// other over a loopback TLS listener: certificate pinning, zero-banner auth,
// message envelopes, RPC round-trips, and data streaming.

func startTestServer(t *testing.T, handle func(*Session)) (addr string, pin string) {
	t.Helper()
	pair, pin, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(ServerConfig{Addr: "127.0.0.1:0", Certificate: pair})
	ln, err := server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		server.Close()
	})
	go func() { _ = server.Serve(ln, handle) }()
	return ln.Addr().String(), pin
}

func TestTransportEndToEndAuthAndMessages(t *testing.T) {
	type sessionState struct {
		auth   chan *AuthRequest
		wrote  chan map[string]json.RawMessage
	}
	state := &sessionState{auth: make(chan *AuthRequest, 1), wrote: make(chan map[string]json.RawMessage, 8)}
	addr, pin := startTestServer(t, func(session *Session) {
		auth, err := session.ReadAuth()
		if err != nil {
			_ = session.Close()
			return
		}
		select {
		case state.auth <- auth:
		default:
		}
		if auth.Token != "correct-token" {
			session.RejectAuth("invalid agent credentials")
			return
		}
		if err := session.AcceptAuth(nil); err != nil {
			return
		}
		_ = session.Run(func(envelope map[string]json.RawMessage) error {
			state.wrote <- envelope
			return nil
		}, func(req *RequestFrame) *ResponseFrame {
			return &ResponseFrame{ID: req.ID, Status: 200, Body: []byte(`{"ok":true}`)}
		})
	})

	// Wrong token is rejected.
	bad, err := Dial(context.Background(), ClientConfig{Address: addr, CertSHA256: pin, AgentID: "agent-1", Token: "wrong"})
	if err == nil || bad != nil {
		t.Fatalf("wrong token must be rejected: %v", err)
	}

	// Correct token authenticates; messages flow both ways; RPC round-trips.
	session, err := Dial(context.Background(), ClientConfig{Address: addr, CertSHA256: pin, AgentID: "agent-1", Token: "correct-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	received := make(chan map[string]json.RawMessage, 4)
	go func() { _ = session.RunClient(func(envelope map[string]json.RawMessage) { received <- envelope }) }()
	if err := session.WriteMessage(map[string]any{"type": "health_report", "health_report": map[string]any{"agent_id": "agent-1"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case envelope := <-state.wrote:
		if string(envelope["type"]) != `"health_report"` {
			t.Fatalf("server envelope = %v", envelope)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never received the message")
	}
	resp, err := session.Request(context.Background(), "/task-results", map[string]any{"task_id": 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || string(resp.Body) != `{"ok":true}` {
		t.Fatalf("rpc response = %+v", resp)
	}
}

func TestTransportPinMismatchIsRejected(t *testing.T) {
	addr, _ := startTestServer(t, func(session *Session) { _ = session.Close() })
	wrongPin := string(bytes.Repeat([]byte("a"), 64))
	if _, err := Dial(context.Background(), ClientConfig{Address: addr, CertSHA256: wrongPin, AgentID: "a", Token: "t"}); err == nil {
		t.Fatal("certificate pin mismatch must fail the handshake")
	}
}

func TestTransportDownloadStream(t *testing.T) {
	dir := t.TempDir()
	artifact := filepath.Join(dir, "release-manifest.json")
	payload := []byte(`{"repo":"OboardProject/oboard-agent","build":"abc123"}`)
	if err := os.WriteFile(artifact, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	addr, pin := startTestServer(t, func(session *Session) {
		auth, err := session.ReadAuth()
		if err != nil {
			_ = session.Close()
			return
		}
		_ = auth
		if err := session.AcceptAuth(nil); err != nil {
			return
		}
		_ = session.Run(func(map[string]json.RawMessage) error { return nil }, func(req *RequestFrame) *ResponseFrame {
			if req.Path != "/download" {
				return &ResponseFrame{ID: req.ID, Status: 404, Error: "not found"}
			}
			var request struct {
				Stream string `json:"stream"`
				Offset int64  `json:"offset"`
				Length int64  `json:"length"`
			}
			if err := json.Unmarshal(req.Body, &request); err != nil {
				return &ResponseFrame{ID: req.ID, Status: 400, Error: "bad request"}
			}
			if request.Stream != "release-manifest.json" {
				return &ResponseFrame{ID: req.ID, Status: 404, Error: "unknown artifact"}
			}
			data, err := os.ReadFile(artifact)
			if err != nil {
				return &ResponseFrame{ID: req.ID, Status: 404, Error: "unavailable"}
			}
			end := request.Offset + request.Length
			if end > int64(len(data)) {
				end = int64(len(data))
			}
			if request.Offset >= int64(len(data)) {
				return &ResponseFrame{ID: req.ID, Status: 200, Body: []byte("null")}
			}
			encoded, _ := json.Marshal(data[request.Offset:end])
			return &ResponseFrame{ID: req.ID, Status: 200, Body: encoded}
		})
	})
	session, err := Dial(context.Background(), ClientConfig{Address: addr, CertSHA256: pin, AgentID: "a", Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = session.RunClient(func(map[string]json.RawMessage) {}) }()
	resp, err := session.Request(context.Background(), "/download", map[string]any{"stream": "release-manifest.json", "offset": 0, "length": 4096}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("download response = %+v", resp)
	}
	var chunk []byte
	if err := json.Unmarshal(resp.Body, &chunk); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(chunk, payload) {
		t.Fatalf("downloaded chunk = %q", chunk)
	}
}

func TestGenerateSelfSignedCertShape(t *testing.T) {
	pair, pin, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatal(err)
	}
	if len(pin) != 64 {
		t.Fatalf("pin length = %d", len(pin))
	}
	if len(pair.Certificate) == 0 {
		t.Fatal("certificate is empty")
	}
	// Regenerating must produce a different pin (random subject/serial).
	if _, second, err := GenerateSelfSignedCert(); err != nil || second == pin {
		t.Fatalf("pins must be unique per generation: %s vs %s (%v)", pin, second, err)
	}
}
