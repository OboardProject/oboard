package agentlink

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

// ServerConfig configures the dedicated agent transport listener.
type ServerConfig struct {
	// Addr is the listen address (host:port).
	Addr string
	// Certificate is the TLS certificate the listener serves. It must be
	// dedicated: reusing the panel certificate would disclose the panel
	// domain to any probe that completes a handshake.
	Certificate tls.Certificate
}

// Server accepts agent transport connections on its own listener.
type Server struct {
	cfg ServerConfig
	tlsCfg *tls.Config

	mu       sync.Mutex
	sessions map[*Session]struct{}
	closed   chan struct{}
}

// NewServer prepares the listener configuration.
func NewServer(cfg ServerConfig) *Server {
	return &Server{
		cfg: cfg,
		tlsCfg: &tls.Config{
			Certificates: []tls.Certificate{cfg.Certificate},
			MinVersion:   tls.VersionTLS13,
			// No NextProtos: the listener serves no HTTP and offers no ALPN.
		},
		sessions: map[*Session]struct{}{},
		closed:   make(chan struct{}),
	}
}

// Listen starts the TCP listener.
func (s *Server) Listen() (net.Listener, error) {
	return tls.Listen("tcp", s.cfg.Addr, s.tlsCfg)
}

// Serve accepts connections until the listener closes. Each connection is
// handled in its own goroutine.
func (s *Server) Serve(ln net.Listener, handle func(*Session)) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
				return err
			}
		}
		go func() {
			// Zero banner: after the TLS handshake the server sends nothing
			// and waits for the client's auth frame. A probe that connects
			// and reads sees only the timeout.
			_ = conn.SetDeadline(time.Now().Add(helloTimeout))
			session := &Session{conn: conn, closed: make(chan struct{}), pending: map[int64]chan *ResponseFrame{}}
			s.mu.Lock()
			s.sessions[session] = struct{}{}
			s.mu.Unlock()
			defer func() {
				s.mu.Lock()
				delete(s.sessions, session)
				s.mu.Unlock()
				session.shutdown(errors.New("serve exit"))
			}()
			handle(session)
		}()
	}
}

// Close ends the listener and every live session.
func (s *Server) Close() {
	select {
	case <-s.closed:
		return
	default:
		close(s.closed)
	}
	s.mu.Lock()
	sessions := make([]*Session, 0, len(s.sessions))
	for session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.mu.Unlock()
	for _, session := range sessions {
		session.shutdown(errors.New("server closing"))
	}
}

// ServerSession is the server-side view of one agent connection. It mirrors
// the client Session's framing but is driven by the server's read loop.
type ServerSession struct {
	Session
}

// ReadAuth waits for the client's first frame and decodes it as an auth
// request. It is the only frame the server accepts before authentication.
func (s *Session) ReadAuth() (*AuthRequest, error) {
	_ = s.conn.SetReadDeadline(time.Now().Add(helloTimeout))
	f, err := readFrame(s.conn)
	if err != nil {
		return nil, err
	}
	if f.frameType != frameTypeAuth {
		return nil, fmt.Errorf("expected auth frame, got %#02x", f.frameType)
	}
	var auth AuthRequest
	if err := json.Unmarshal(f.payload, &auth); err != nil {
		return nil, err
	}
	return &auth, nil
}

// AcceptAuth replies with the acceptance payload (the enrollment response
// JSON, or nil for an ordinary session) and clears the handshake deadline.
func (s *Session) AcceptAuth(payload []byte) error {
	f := frame{frameType: frameTypeAuthOK}
	if payload != nil {
		f.payload = payload
	}
	if err := writeFrame(s.conn, f); err != nil {
		return err
	}
	return s.conn.SetDeadline(time.Time{})
}

// RejectAuth replies with an error frame and closes.
func (s *Session) RejectAuth(reason string) {
	_ = writeFrame(s.conn, frame{frameType: frameTypeError, payload: []byte(reason)})
	_ = s.conn.Close()
}

// Run drives the authenticated session. onMessage receives every JSON
// envelope; onRequest receives every RPC request. The session ends when
// either callback returns an error or the connection drops.
func (s *Session) Run(onMessage func(map[string]json.RawMessage) error, onRequest func(*RequestFrame) *ResponseFrame) error {
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
			s.shutdown(errors.New("client sent goaway"))
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
			if err := onMessage(envelope); err != nil {
				s.shutdown(err)
				return err
			}
		case frameTypeRequest:
			var req RequestFrame
			if err := json.Unmarshal(f.payload, &req); err == nil {
				var resp *ResponseFrame
				if onRequest != nil {
					resp = onRequest(&req)
				}
				if resp == nil {
					resp = &ResponseFrame{ID: req.ID, Status: 404, Error: "not handled"}
				}
				encoded, err := json.Marshal(resp)
				if err == nil {
					if err := s.writePadded(frame{frameType: frameTypeResponse, flags: flagPayloadJSON}, encoded); err != nil {
						s.shutdown(err)
						return err
					}
				}
			}
		case frameTypeData:
			// Data frames on the server side arrive in the download handler
			// goroutine; the read loop never sees them.
		default:
			// Unknown frame types are ignored for forward compatibility.
		}
	}
}

// GenerateSelfSignedCert creates the dedicated listener certificate: a
// random-subject self-signed cert whose only purpose is TLS 1.3 key
// transport. Agents pin its SHA-256; nothing about it identifies OBoard.
func GenerateSelfSignedCert() (tls.Certificate, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	name, err := RandomName()
	if err != nil {
		return tls.Certificate{}, "", err
	}
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(der)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return pair, hex.EncodeToString(sum[:]), nil
}

// FrameTypeData is the exported data-frame type for download streams.
const FrameTypeData = frameTypeData

// Frame is the exported frame for download streams.
type Frame = frame

// NewDataFrame builds a data frame with the given payload.
func NewDataFrame(payload []byte) Frame {
	return Frame{frameType: frameTypeData, payload: payload}
}

// SHA256Hex derives the hex pin for a DER certificate.
func SHA256Hex(der []byte) string {
	return sha256HexString(der)
}

// EncodeCertPEM returns the PEM encoding of a generated pair for persistence.
func EncodeCertPEM(pair tls.Certificate) (certPEM, keyPEM []byte, err error) {
	// The generator produced the pair from PEM; re-encode from the parsed
	// form so the persisted bytes always match what is served.
	certOut := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		return nil, nil, err
	}
	keyOut := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certOut, keyOut, nil
}

// RemoteAddr returns the remote address of the session connection.
func (s *Session) RemoteAddr() string {
	if s.conn == nil {
		return ""
	}
	return s.conn.RemoteAddr().String()
}
