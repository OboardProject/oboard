// Package agentlink implements the binary transport used between stealth
// Agents and the Controller's dedicated listener: TLS 1.3 with a pinned
// self-signed certificate, a zero-banner handshake where the client speaks
// first, and length-prefixed JSON frames that carry the same message
// envelopes as the standard WebSocket channel.
//
// The wire format is deliberately minimal and carries no product marker: no
// magic, no version banner, no ALPN. An observer sees an ordinary TLS
// session whose server certificate has a random subject and whose payload
// frames are indistinguishable padding-wise from any other binary protocol.
//
// This is the Controller-side copy. The agent has its own copy in
// internal/agentlink; the two must stay byte-compatible. The pinned test
// vectors in frame_test.go exist in both packages and must be changed
// together.
package agentlink

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// randRead is a seam for tests.
var randRead = rand.Read

const (
	// MaxMessageBytes is the reassembled application-message limit on both
	// sides of the transport. It matches the WebSocket channel's 16 MiB.
	MaxMessageBytes = 16 << 20
	// maxFramePayload bounds one frame's payload. Data-transfer frames
	// (update chunks) are the only payloads that approach this.
	maxFramePayload = 1 << 20
	// frameHeaderLen is the fixed header: type(1) flags(1) length(4).
	frameHeaderLen = 6
	// MinPaddedFrameBytes is the minimum on-wire size of a padded frame.
	// Small control messages are padded to this size so frame lengths do not
	// disclose message types.
	MinPaddedFrameBytes = 128
	// helloTimeout bounds how long the server waits for the client's first
	// frame after the TLS handshake before dropping the connection. A probe
	// that connects and waits sees nothing but a closed connection.
	helloTimeout = 10 * time.Second
)

// Frame types. The values are wire-stable; do not renumber.
const (
	frameTypeMessage  = 0x01 // JSON message envelope (same shape as the WS channel)
	frameTypeAuth     = 0x02 // client-first authentication
	frameTypeAuthOK   = 0x03 // server acceptance (empty)
	frameTypeError    = 0x04 // terminal error, connection closes after send
	frameTypeRequest  = 0x05 // request/response RPC (path + JSON body)
	frameTypeResponse = 0x06 // RPC response (status + JSON body)
	frameTypeData     = 0x07 // binary data chunk for transfer streams
	frameTypePing     = 0x08 // keepalive; peer answers with frameTypePing
	frameTypeGoAway   = 0x09 // graceful shutdown notice before close
)

// ErrFrameTooLarge is returned when a frame header announces a payload the
// receiver refuses to buffer.
var ErrFrameTooLarge = errors.New("agentlink frame exceeds the payload limit")

// ErrMessageTooLarge is returned when a reassembled message exceeds
// MaxMessageBytes.
var ErrMessageTooLarge = errors.New("agentlink message exceeds the reassembly limit")

type frame struct {
	frameType byte
	flags     byte
	payload   []byte
}

// flagPayloadJSON marks a message frame whose payload is JSON text.
const flagPayloadJSON = 0x01

// flagPad marks a frame whose payload carries random padding that the
// receiver strips. The padding layout is: uint16 pad length, pad bytes,
// real payload.
const flagPad = 0x02

// writeFrame writes one frame with a length-prefixed header.
func writeFrame(w io.Writer, f frame) error {
	header := make([]byte, frameHeaderLen)
	header[0] = f.frameType
	header[1] = f.flags
	binary.BigEndian.PutUint32(header[2:], uint32(len(f.payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(f.payload)
	return err
}

// readFrame reads one frame. It refuses to allocate more than
// maxFramePayload.
func readFrame(r io.Reader) (frame, error) {
	header := make([]byte, frameHeaderLen)
	if _, err := io.ReadFull(r, header); err != nil {
		return frame{}, err
	}
	length := binary.BigEndian.Uint32(header[2:])
	if length > maxFramePayload {
		return frame{}, ErrFrameTooLarge
	}
	f := frame{frameType: header[0], flags: header[1]}
	if length > 0 {
		f.payload = make([]byte, length)
		if _, err := io.ReadFull(r, f.payload); err != nil {
			return frame{}, err
		}
	}
	return f, nil
}

// stripPadding removes the flagPad layout from a payload.
func stripPadding(payload []byte) ([]byte, error) {
	if len(payload) < 2 {
		return nil, fmt.Errorf("padded frame is too short")
	}
	padLen := int(binary.BigEndian.Uint16(payload))
	if 2+padLen > len(payload) {
		return nil, fmt.Errorf("padded frame declares more padding than payload")
	}
	return payload[2+padLen:], nil
}

// buildPadding wraps payload in the flagPad layout with the given pad length.
func buildPadding(payload []byte, padLen int) []byte {
	out := make([]byte, 0, 2+padLen+len(payload))
	var padHeader [2]byte
	binary.BigEndian.PutUint16(padHeader[:], uint16(padLen))
	out = append(out, padHeader[:]...)
	out = append(out, make([]byte, padLen)...)
	return append(out, payload...)
}

// AuthRequest is the frameTypeAuth payload.
type AuthRequest struct {
	AgentID string `json:"agent_id"`
	Token   string `json:"token"`
	// EnrollmentToken is set only by a bootstrapping agent that has no
	// identity yet; the server answers with an AuthEnrollResponse.
	EnrollmentToken string `json:"enrollment_token,omitempty"`
	// Health carries the first probe report during enrollment, mirroring the
	// HTTP enroll request body. It is typed as the same HealthReport model on
	// both sides; the agent package cannot be imported here, so the JSON
	// shape is the contract.
	Health json.RawMessage `json:"health,omitempty"`
}

// AuthEnrollResponse is the frameTypeAuthOK payload when the auth request
// carried an enrollment token.
type AuthEnrollResponse struct {
	ServerID               int64  `json:"server_id"`
	AgentID                string `json:"agent_id"`
	AgentToken             string `json:"agent_token"`
	ConnectionAuditEnabled bool   `json:"connection_audit_enabled"`
}

// RequestFrame is the frameTypeRequest payload: an RPC over the transport.
// Path matches the standard HTTP callback route (without the /api/v1/agent
// prefix) so the server can bridge to the existing handlers unchanged.
type RequestFrame struct {
	ID   int64          `json:"id"`
	Path string        `json:"path"`
	Body json.RawMessage `json:"body,omitempty"`
}

// ResponseFrame is the frameTypeResponse payload.
type ResponseFrame struct {
	ID     int64           `json:"id"`
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// DataHeader prefixes a frameTypeData payload: stream identity and offset.
type DataHeader struct {
	Stream string `json:"stream"`
	Offset int64  `json:"offset"`
}

// RandomName returns a random [a-z][a-z0-9]{9} identifier used for the
// self-signed certificate subject.
func RandomName() (string, error) {
	const digits = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, 0, 10)
	var seed [8]byte
	if _, err := randRead(seed[:]); err != nil {
		return "", err
	}
	v := binary.BigEndian.Uint64(seed[:])
	out = append(out, byte('a'+int(v%26)))
	v /= 26
	for i := 0; i < 9; i++ {
		out = append(out, digits[int(v%36)])
		v /= 36
	}
	return string(out), nil
}

// sha256Sum is a small helper shared by the package (server pin derivation
// and tests).
func sha256Sum(data []byte) [32]byte {
	return sha256.Sum256(data)
}
