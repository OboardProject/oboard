package agentlink

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// ClientConfig configures one agent-side transport connection.
type ClientConfig struct {
	// Address is host:port of the Controller's dedicated listener.
	Address string
	// CertSHA256 pins the server certificate. An empty value disables
	// pinning (first-use bootstrap reads it from the enrollment response).
	CertSHA256 string
	// AgentID and Token authenticate the session.
	AgentID string
	Token   string
	// EnrollmentToken, when set, authenticates the very first connection of
	// a stealth install; the server returns the agent identity and the pin
	// is captured from this session.
	EnrollmentToken string
	// ServerCertOverride, when non-empty, is the pinned certificate in PEM
	// form for first-use validation without an out-of-band pin.
	ServerCertOverride string
}

// Dial establishes a session: TLS handshake with pinning, then the
// client-first auth frame, then the server's acceptance.
func Dial(ctx context.Context, cfg ClientConfig) (*Session, error) {
	d := &net.Dialer{Timeout: 15 * time.Second}
	tlsCfg := &tls.Config{
		// The dedicated listener serves no HTTP, so no ALPN is offered. The
		// handshake stays an ordinary TLS 1.3 exchange.
		NextProtos: nil,
		MinVersion: tls.VersionTLS13,
		ServerName: serverNameForAddress(cfg.Address),
	}
	if cfg.CertSHA256 != "" {
		expected := cfg.CertSHA256
		tlsCfg.InsecureSkipVerify = true
		tlsCfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("server presented no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			if hex.EncodeToString(sum[:]) != expected {
				return fmt.Errorf("server certificate pin mismatch")
			}
			return nil
		}
	} else if cfg.ServerCertOverride != "" {
		// First use without an out-of-band pin is only allowed when the
		// caller supplied the expected certificate itself (bootstrap).
		pool, err := certPoolFromPEM(cfg.ServerCertOverride)
		if err != nil {
			return nil, err
		}
		tlsCfg.RootCAs = pool
	} else {
		return nil, errors.New("agentlink requires a certificate pin")
	}
	raw, err := d.DialContext(ctx, "tcp", cfg.Address)
	if err != nil {
		return nil, err
	}
	conn := tls.Client(raw, tlsCfg)
	if err := conn.SetDeadline(time.Now().Add(helloTimeout)); err != nil {
		_ = raw.Close()
		return nil, err
	}
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	pin := cfg.CertSHA256
	if pin == "" {
		if peer := conn.ConnectionState().PeerCertificates; len(peer) > 0 {
			sum := sha256.Sum256(peer[0].Raw)
			pin = hex.EncodeToString(sum[:])
		}
	}
	session := &Session{
		conn:    conn,
		certPin: pin,
		pending: map[int64]chan *ResponseFrame{},
		closed:  make(chan struct{}),
	}
	auth := AuthRequest{AgentID: cfg.AgentID, Token: cfg.Token, EnrollmentToken: cfg.EnrollmentToken}
	payload, err := json.Marshal(auth)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := writeFrame(conn, frame{frameType: frameTypeAuth, flags: flagPayloadJSON, payload: payload}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	accept, err := readFrame(conn)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if accept.frameType == frameTypeError {
		_ = conn.Close()
		return nil, fmt.Errorf("agentlink auth rejected: %s", string(accept.payload))
	}
	if accept.frameType != frameTypeAuthOK {
		_ = conn.Close()
		return nil, fmt.Errorf("agentlink unexpected frame %#02x after auth", accept.frameType)
	}
	session.acceptPayload = accept.payload
	// The session is live; deadlines move to the read loop.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return session, nil
}

// CertPin returns the SHA-256 of the pinned server certificate observed on
// this session.
func (s *Session) CertPin() string { return s.certPin }

// AcceptPayload returns the server's auth-acceptance payload. For an
// enrollment session this is the AuthEnrollResponse JSON.
func (s *Session) AcceptPayload() []byte { return s.acceptPayload }

// RunClient starts the client read loop. onMessage receives every JSON
// message envelope exactly like the WebSocket channel delivered them. (The
// server side uses Run with an additional request callback.)
func (s *Session) RunClient(onMessage func(map[string]json.RawMessage)) error {
	s.msgHandler = onMessage
	for {
		_ = s.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		f, err := readFrame(s.conn)
		if err != nil {
			s.shutdown(err)
			return err
		}
		switch f.frameType {
		case frameTypePing:
			if err := s.WriteFrame(frame{frameType: frameTypePing}); err != nil {
				s.shutdown(err)
				return err
			}
		case frameTypeGoAway:
			s.shutdown(errors.New("server sent goaway"))
			return ErrClosed
		case frameTypeMessage:
			payload := f.payload
			if f.flags&flagPad != 0 {
				if payload, err = stripPadding(payload); err != nil {
					s.shutdown(err)
					return err
				}
			}
			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(payload, &envelope); err != nil {
				s.shutdown(err)
				return err
			}
			if s.msgHandler != nil {
				s.msgHandler(envelope)
			}
		case frameTypeResponse:
			responsePayload := f.payload
			if f.flags&flagPad != 0 {
				if responsePayload, err = stripPadding(responsePayload); err != nil {
					s.shutdown(err)
					return err
				}
			}
			var resp ResponseFrame
			if err := json.Unmarshal(responsePayload, &resp); err == nil {
				s.pendingMu.Lock()
				ch := s.pending[resp.ID]
				delete(s.pending, resp.ID)
				s.pendingMu.Unlock()
				if ch != nil {
					respCopy := resp
					ch <- &respCopy
				}
			}
		case frameTypeData:
			if err := s.deliverData(f); err != nil {
				s.shutdown(err)
				return err
			}
		default:
			// Unknown frame types are ignored: forward compatibility.
		}
	}
}

func serverNameForAddress(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return address
	}
	return host
}

// certPoolFromPEM builds a single-cert pool from PEM bytes.
func certPoolFromPEM(pem string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(pem)) {
		return nil, errors.New("agentlink server certificate override is not valid PEM")
	}
	return pool, nil
}
