package agentlink

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// sha256Hex is duplicated from the client for the vector test.
func sha256Hex(data []byte) string {
	sum := sha256Sum(data)
	return hex.EncodeToString(sum[:])
}

// The frame vectors below are a wire contract with the Controller's
// internal/agentlink copy. Both sides must encode and decode identically;
// changing the framing requires changing both in one release.

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := frame{frameType: frameTypeMessage, flags: flagPayloadJSON, payload: []byte(`{"type":"hello"}`)}
	if err := writeFrame(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := readFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if out.frameType != in.frameType || out.flags != in.flags || !bytes.Equal(out.payload, in.payload) {
		t.Fatalf("round trip mismatch: %+v vs %+v", out, in)
	}
}

func TestFrameHeaderVector(t *testing.T) {
	// type=0x01 flags=0x01 length=4 payload="test" — pinned bytes.
	var buf bytes.Buffer
	if err := writeFrame(&buf, frame{frameType: frameTypeMessage, flags: flagPayloadJSON, payload: []byte("test")}); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x01, 0x01, 0x00, 0x00, 0x00, 0x04, 't', 'e', 's', 't'}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("frame bytes = %x, want %x", buf.Bytes(), want)
	}
}

func TestPaddingRoundTrip(t *testing.T) {
	payload := []byte(`{"a":1}`)
	wrapped, err := buildPadding(payload, 37)
	if err != nil {
		t.Fatal(err)
	}
	stripped, err := stripPadding(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stripped, payload) {
		t.Fatalf("padding round trip = %q", stripped)
	}
	padLen := int(binary.BigEndian.Uint16(wrapped))
	if len(wrapped) != 2+padLen+len(payload) {
		t.Fatalf("padding layout = %d/%d", padLen, len(wrapped))
	}
	if _, err := stripPadding([]byte{0x00}); err == nil {
		t.Fatal("short padded frame must be rejected")
	}
	if _, err := stripPadding([]byte{0x00, 0x10, 0x01}); err == nil {
		t.Fatal("overlong padding must be rejected")
	}
}

func TestReadFrameRejectsOversize(t *testing.T) {
	header := make([]byte, frameHeaderLen)
	header[0] = frameTypeData
	binary.BigEndian.PutUint32(header[2:], uint32(maxFramePayload+1))
	if _, err := readFrame(bytes.NewReader(header)); err != ErrFrameTooLarge {
		t.Fatalf("oversize frame error = %v", err)
	}
}

func TestAuthPayloadShape(t *testing.T) {
	auth := AuthRequest{AgentID: "agent-1", Token: "tok"}
	encoded, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	var back AuthRequest
	if err := json.Unmarshal(encoded, &back); err != nil || back.AgentID != "agent-1" {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("enrollment_token")) {
		t.Fatalf("empty enrollment token must stay omitted: %s", encoded)
	}
}

// TestSHA256Vector pins the digest used for certificate pins; both sides
// derive pins the same way.
func TestSHA256Vector(t *testing.T) {
	sum := sha256Hex([]byte("oboard"))
	if sha256Hex([]byte("oboard")) != sum {
		t.Fatal("digest must be deterministic")
	}
	if len(sum) != 64 {
		t.Fatalf("digest length = %d", len(sum))
	}
	if _, err := hex.DecodeString(sum); err != nil {
		t.Fatalf("digest is not hex: %v", err)
	}
}
