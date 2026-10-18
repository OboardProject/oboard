package agentlink

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type heartbeatConn struct {
	net.Conn
	input     bytes.Buffer
	output    bytes.Buffer
	deadlines []time.Time
}

func (c *heartbeatConn) Read(p []byte) (int, error)  { return c.input.Read(p) }
func (c *heartbeatConn) Write(p []byte) (int, error) { return c.output.Write(p) }
func (c *heartbeatConn) Close() error                { return nil }
func (c *heartbeatConn) SetReadDeadline(d time.Time) error {
	c.deadlines = append(c.deadlines, d)
	return nil
}

func testHeartbeat(t *testing.T, run func(*Session, func(map[string]json.RawMessage)) error) {
	t.Helper()
	for _, tc := range []struct {
		name     string
		incoming []byte
		outgoing []byte
	}{
		{"ping", []byte{0x08}, []byte{0x0a}},
		{"pong", []byte{0x0a}, nil},
		{"repeated", []byte{0x08, 0x0a, 0x0a, 0x08}, []byte{0x0a, 0x0a}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &heartbeatConn{}
			for _, kind := range tc.incoming {
				if err := writeFrame(&conn.input, frame{frameType: kind}); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeFrame(&conn.input, frame{frameType: frameTypeMessage, flags: flagPayloadJSON, payload: []byte(`{"type":"heartbeat_test"}`)}); err != nil {
				t.Fatal(err)
			}
			s := &Session{conn: conn, closed: make(chan struct{})}
			messages := 0
			started := time.Now()
			err := run(s, func(envelope map[string]json.RawMessage) {
				messages++
				if string(envelope["type"]) != `"heartbeat_test"` {
					t.Errorf("unexpected message: %v", envelope)
				}
			})
			if !errors.Is(err, io.EOF) {
				t.Fatalf("read loop error = %v, want EOF", err)
			}
			if messages != 1 {
				t.Fatalf("messages = %d, want 1", messages)
			}
			var want []byte
			for _, kind := range tc.outgoing {
				want = append(want, kind, 0, 0, 0, 0, 0)
			}
			if !bytes.Equal(conn.output.Bytes(), want) {
				t.Fatalf("heartbeat output = %x, want %x", conn.output.Bytes(), want)
			}
			if len(conn.deadlines) != len(tc.incoming)+2 {
				t.Fatalf("read deadline was not refreshed after every frame: %v", conn.deadlines)
			}
			for _, deadline := range conn.deadlines {
				if deadline.Before(started.Add(90 * time.Second)) {
					t.Fatalf("read deadline = %v, want renewed 90-second timeout", deadline)
				}
			}
		})
	}
}

func TestClientHeartbeatDoesNotReflectPong(t *testing.T) {
	testHeartbeat(t, func(s *Session, onMessage func(map[string]json.RawMessage)) error {
		return s.RunClient(onMessage)
	})
}

func TestPingWireVector(t *testing.T) {
	conn := &heartbeatConn{}
	s := &Session{conn: conn, closed: make(chan struct{})}
	if err := s.Ping(); err != nil {
		t.Fatal(err)
	}
	if want := []byte{0x08, 0, 0, 0, 0, 0}; !bytes.Equal(conn.output.Bytes(), want) {
		t.Fatalf("ping bytes = %x, want %x", conn.output.Bytes(), want)
	}
}
