package controller

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/agentlink"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

// The stealth agent transport: a dedicated TLS listener that carries the same
// control-channel messages, agent callbacks, and release downloads as the
// standard WebSocket + HTTPS surfaces, with no HTTP anywhere. See
// internal/agentlink for the wire protocol.
//
// The listener is opt-in (OBOARD_STEALTH_ADDR). Its certificate is dedicated
// and self-signed with a random subject; agents pin its SHA-256 out of band
// through the panel-generated install command or the enrollment response.

// stealthTransport is the Controller-side listener state.
type stealthTransport struct {
	server   *agentlink.Server
	listener net.Listener
	listenAddr string
	done chan struct{}
	pin      string
	addr     string
}

// loadOrCreateStealthCert loads the dedicated listener certificate or
// generates it on first start. The key is written 0600 beside the database.
func loadOrCreateStealthCert(certPath, keyPath string) (tls.Certificate, string, error) {
	if pair, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		if len(pair.Certificate) > 0 {
			sum := agentlink.SHA256Hex(pair.Certificate[0])
			return pair, sum, nil
		}
	}
	pair, pin, err := agentlink.GenerateSelfSignedCert()
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPEM, keyPEM, err := agentlink.EncodeCertPEM(pair)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	return pair, pin, nil
}

// allowRateRaw is the store-backed rate limit without an HTTP response.
func (s *Server) allowRateRaw(key string, limit int, window time.Duration) bool {
	keyHash := security.HashSecret(key)
	allowed, err := s.store.AllowRate(context.Background(), keyHash, limit, window, 10_000)
	return err == nil && allowed
}

// StealthTransportInfo reports the listener state for settings and the panel.
func (s *Server) StealthTransportInfo() (addr, pin string, enabled bool) {
	current := s.stealthTransport.Load()
	if current == nil {
		return "", "", false
	}
	return current.addr, current.pin, true
}

// handleStealthConnection authenticates one inbound connection and runs the
// session. Unauthenticated connections get the zero-banner treatment: the
// server has said nothing since the handshake and closes on timeout or
// invalid auth.
func (s *Server) handleStealthConnection(session *agentlink.Session) {
	auth, err := session.ReadAuth()
	if err != nil {
		// No reply: a prober or a dead connection. Close quietly.
		_ = session.Close()
		return
	}
	remoteIP := stealthRemoteIP(session)
	if auth.EnrollmentToken != "" {
		s.handleStealthEnroll(session, auth, remoteIP)
		return
	}
	server, ok := s.authStealthAgent(session, auth, remoteIP)
	if !ok {
		return
	}
	s.runStealthSession(session, server, remoteIP)
}

// authStealthAgent mirrors authAgent's semantics over the binary protocol.
func (s *Server) authStealthAgent(session *agentlink.Session, auth *agentlink.AuthRequest, ip string) (*model.Server, bool) {
	agentID := strings.TrimSpace(auth.AgentID)
	if s.unknownAgentIdentity(agentID) || s.agentAuthBlocked(ip) || agentID == "" || strings.TrimSpace(auth.Token) == "" {
		s.noteAgentAuthFailure(ip)
		session.RejectAuth("invalid agent credentials")
		return nil, false
	}
	lookupCtx, cancel := context.WithTimeout(context.Background(), agentAuthLookupTimeout)
	defer cancel()
	server, err := s.store.GetServerByAgent(lookupCtx, agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.rememberUnknownAgent(agentID)
			s.noteAgentAuthFailure(ip)
		}
		session.RejectAuth("invalid agent credentials")
		return nil, false
	}
	if !hmac.Equal([]byte(server.AgentTokenHash), []byte(security.HashSecret(auth.Token))) {
		s.noteAgentAuthFailure(ip)
		session.RejectAuth("invalid agent credentials")
		return nil, false
	}
	s.forgetUnknownAgent(agentID)
	s.noteAgentAuthSuccess(ip)
	if err := session.AcceptAuth(nil); err != nil {
		return nil, false
	}
	return server, true
}

// handleStealthEnroll performs enrollment over the transport so a stealth
// install never touches the HTTP surface.
func (s *Server) handleStealthEnroll(session *agentlink.Session, auth *agentlink.AuthRequest, ip string) {
	if !s.allowStealthRate("stealth-enroll:"+ip, 10, time.Minute) {
		session.RejectAuth("too many enrollment attempts")
		return
	}
	agentID, err := security.RandomToken(16)
	if err != nil {
		session.RejectAuth("enrollment failed")
		return
	}
	agentToken, err := security.RandomToken(32)
	if err != nil {
		session.RejectAuth("enrollment failed")
		return
	}
	hash := security.HashSecret(auth.EnrollmentToken)
	server, err := s.store.ClaimServerEnrollment(context.Background(), hash, agentID, security.HashSecret(agentToken))
	if err != nil {
		session.RejectAuth("invalid enrollment token")
		return
	}
	var health model.HealthReport
	if len(auth.Health) > 0 {
		_ = json.Unmarshal(auth.Health, &health)
	}
	if health.OS != "" {
		server.OS = health.OS
		server.DistroID = health.DistroID
		server.DistroVersion = health.DistroVersion
		server.DistroName = health.DistroName
		server.Libc = health.Libc
		server.ServiceManager = health.ServiceManager
		server.PackageManager = health.PackageManager
		server.Arch = health.Arch
		applyDetectedEntryIPs(server, health, ip)
		if code := normalizeControllerRegionCode(health.RegionCode); code != "" {
			server.DetectedRegionCode = code
		}
		server.SingBoxVersion = health.SingBoxVersion
		server.KernelCapabilities = normalizeKernelCapabilities(health.KernelCapabilities)
		server.CPU = health.CPU
		if health.CPUCores > 0 {
			server.CPUCores = health.CPUCores
		}
		server.MemoryBytes = health.MemoryBytes
		server.CPUUsagePercent = health.CPUUsagePercent
		server.MemoryUsedBytes = health.MemoryUsedBytes
		server.MemoryTotalBytes = health.MemoryTotalBytes
		server.AgentMemoryBytes = health.AgentMemoryBytes
		server.AgentVersion = health.AgentVersion
		server.AgentBuild = health.AgentBuild
		if err := s.store.UpdateServer(context.Background(), server); err != nil {
			session.RejectAuth("enrollment failed")
			return
		}
	}
	s.evictAgentSessions(server.ID)
	s.noteAgentAuthSuccess(ip)
	s.invalidateAuthorizationLease(server.ID)
	s.wakeAuthorizationSyncFor(accessSyncReasonReconnect, server.ID)
	s.wakeRuntimeUsersSyncFor(accessSyncReasonReconnect, server.ID)
	s.wakeRuntimeUsersSync()
	s.enqueueRecoveryDeployment(server.ID)
	_ = s.store.AddAudit(context.Background(), model.AuditLog{Action: "agent_enroll", Target: "server", Detail: server.Name, IP: ip})
	log.Printf("agent enrolled (stealth transport) server=%d(%s) agent_id=%s remote=%s", server.ID, safeLogField(server.Name), safeLogField(agentID), safeLogField(ip))
	response := agentlink.AuthEnrollResponse{
		ServerID:               server.ID,
		AgentID:                agentID,
		AgentToken:             agentToken,
		ConnectionAuditEnabled: s.effectiveConnectionAuditEnabled(context.Background(), server),
	}
	payload, err := json.Marshal(response)
	if err != nil {
		session.RejectAuth("enrollment failed")
		return
	}
	if err := session.AcceptAuth(payload); err != nil {
		return
	}
	s.runStealthSession(session, server, ip)
}

// runStealthSession bridges the binary transport onto the shared agent
// session loop: same hello, same task dispatch, same heartbeat.
func (s *Server) runStealthSession(session *agentlink.Session, server *model.Server, remoteIP string) {
	log.Printf("agent connected (stealth transport) server=%d(%s) agent_id=%s remote=%s", server.ID, safeLogField(server.Name), safeLogField(server.AgentID), safeLogField(remoteIP))
	connectedAt := time.Now()
	s.trackAgentConnection(context.Background(), server.ID, true, connectedAt.UTC())
	controlCh := make(chan any, 16)
	s.registerAgentLive(server.ID, controlCh)
	connectedAgentID := server.AgentID
	s.invalidateAuthorizationLease(server.ID)
	s.wakeAuthorizationSyncFor(accessSyncReasonReconnect, server.ID)
	s.wakeRuntimeUsersSyncFor(accessSyncReasonReconnect, server.ID)
	s.wakeRuntimeUsersSync()
	defer func() {
		s.unregisterAgentLive(server.ID, controlCh)
		s.trackAgentConnection(context.Background(), server.ID, false, time.Now().UTC())
		log.Printf("agent disconnected (stealth transport) server=%d(%s) connected_for=%s", server.ID, safeLogField(server.Name), time.Since(connectedAt).Round(time.Second))
	}()
	mode, _ := serverMonitoringPolicy(server)
	auditEnabled := s.effectiveConnectionAuditEnabled(context.Background(), server)
	s.syncConnectionAuditPresence(context.Background(), server, auditEnabled)
	hello := map[string]any{"type": "hello", "server_id": server.ID, "monitoring_mode": mode, "connection_audit_enabled": auditEnabled}
	for key, value := range s.configurationHeartbeatFields(context.Background(), server.ID) {
		hello[key] = value
	}
	if plan, err := s.cachedLatencyProbePlanForServer(context.Background(), *server); err == nil {
		hello["latency_probe_plan"] = plan
	}
	writeAgentJSON := func(payload any) error {
		return session.WriteMessage(s.withControllerTime(payload))
	}
	_ = writeAgentJSON(hello)
	reads := make(chan agentSocketRead, 8)
	sessionDone := make(chan error, 1)
	sessionDownload := func(req *agentlink.RequestFrame) *agentlink.ResponseFrame {
		if strings.TrimSpace(req.Path) == "/download" {
			return s.handleStealthDownloadRequest(server, session, req)
		}
		return s.handleStealthRequest(server, req)
	}
	go func() {
		sessionDone <- session.Run(func(envelope map[string]json.RawMessage) error {
			select {
			case reads <- agentSocketRead{message: envelope}:
				return nil
			case <-session.Closed():
				return agentlink.ErrClosed
			}
		}, sessionDownload)
	}()
	// RPC responses and data streams are written from the session goroutine;
	// the shared loop only sees message envelopes.
	go func() {
		<-session.Closed()
		// Unblock a pending write into the reads channel.
		select {
		case reads <- agentSocketRead{err: agentlink.ErrClosed}:
		default:
		}
	}()
	s.agentSessionLoop(context.Background(), server, connectedAgentID, remoteIP, controlCh, reads, writeAgentJSON, stealthTransportPing{session: session}, stealthKeepalivePingInterval)
}

// stealthTransportPing adapts the binary session to the loop's keepalive.
type stealthTransportPing struct {
	session *agentlink.Session
}

func (p stealthTransportPing) ping() error { return p.session.Ping() }

const stealthKeepalivePingInterval = 30 * time.Second

// stealthAgentRoutes maps transport RPC paths to the existing HTTP handlers.
// The handlers stay unchanged; the bridge replays each request through them
// with a recorder, so auth semantics, validation, rate limits, and response
// shapes cannot drift between the two surfaces.
func (s *Server) stealthAgentRoutes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/api/v1/agent/assets":             s.agentManagedAssets,
		"/api/v1/agent/authorization":      s.agentAuthorization,
		"/api/v1/agent/users-snapshot":     s.agentUsersSnapshot,
		"/api/v1/agent/task-results":       s.agentTaskResults,
		"/api/v1/agent/traffic-reports":    s.agentTrafficReports,
		"/api/v1/agent/connection-reports": s.agentConnectionReports,
		"/api/v1/agent/inbound-probes":     s.agentInboundProbes,
		"/api/v1/agent/port-forward-probes": s.agentPortForwardProbes,
		"/api/v1/agent/dns-benchmarks":     s.agentDNSBenchmarks,
		"/api/v1/agent/mtu-detections":     s.agentMTUDetections,
		"/api/v1/agent/certificate-issues": s.agentCertificateIssues,
	}
}

// handleStealthDownloadRequest serves one download chunk over the transport.
// The request body is {stream, offset, length}; the response body is the
// base64-less raw chunk JSON (the agent decodes []byte), and the data frames
// stream from the session goroutine.
func (s *Server) handleStealthDownloadRequest(server *model.Server, session *agentlink.Session, req *agentlink.RequestFrame) *agentlink.ResponseFrame {
	var request struct {
		Stream string `json:"stream"`
		Offset int64  `json:"offset"`
		Length int64  `json:"length"`
	}
	if len(req.Body) > 0 {
		if err := json.Unmarshal(req.Body, &request); err != nil {
			return &agentlink.ResponseFrame{ID: req.ID, Status: 400, Error: "invalid download request"}
		}
	}
	if request.Length <= 0 || request.Length > 1<<20 {
		return &agentlink.ResponseFrame{ID: req.ID, Status: 400, Error: "invalid chunk length"}
	}
	root, err := filepath.Abs(downloadsDir(s.staticDir))
	if err != nil {
		return &agentlink.ResponseFrame{ID: req.ID, Status: 500, Error: err.Error()}
	}
	name := filepath.Base(strings.TrimSpace(request.Stream))
	switch name {
	case "oboard-agent-linux-amd64", "oboard-agent-linux-arm64", "oboard-sb-linux-amd64", "oboard-sb-linux-arm64", "oboard-realm-linux-amd64", "oboard-realm-linux-arm64", "release-manifest.json", "release-manifest.json.sig":
	default:
		return &agentlink.ResponseFrame{ID: req.ID, Status: 404, Error: "unknown artifact"}
	}
	file, err := os.Open(filepath.Join(root, name)) // #nosec G304 -- name is allowlisted above.
	if err != nil {
		return &agentlink.ResponseFrame{ID: req.ID, Status: 404, Error: "artifact unavailable"}
	}
	defer file.Close()
	if request.Offset > 0 {
		if _, err := file.Seek(request.Offset, io.SeekStart); err != nil {
			return &agentlink.ResponseFrame{ID: req.ID, Status: 500, Error: err.Error()}
		}
	}
	buf := make([]byte, request.Length)
	n, readErr := file.Read(buf)
	if n == 0 && readErr != nil {
		return &agentlink.ResponseFrame{ID: req.ID, Status: 200, Body: []byte("null")}
	}
	chunk := buf[:n]
	if request.Offset == 0 && n == int(request.Length) {
		// First chunk of a full read: stream the remainder as data frames so
		// the agent reuses one session for the whole artifact.
		go s.ServeStealthDownload(server, session, name, request.Offset+int64(n))
	}
	encoded, err := json.Marshal(chunk)
	if err != nil {
		return &agentlink.ResponseFrame{ID: req.ID, Status: 500, Error: err.Error()}
	}
	return &agentlink.ResponseFrame{ID: req.ID, Status: 200, Body: encoded}
}

// handleStealthRequest dispatches one RPC over the transport by replaying it
// through the standard HTTP handler with the session's agent identity
// pre-authenticated.
func (s *Server) handleStealthRequest(server *model.Server, req *agentlink.RequestFrame) *agentlink.ResponseFrame {
	handler, ok := s.stealthAgentRoutes()[strings.TrimSpace(req.Path)]
	if !ok {
		return &agentlink.ResponseFrame{ID: req.ID, Status: 404, Error: "not found"}
	}
	method := http.MethodPost
	if strings.Contains(strings.TrimSpace(req.Path), "authorization") || strings.Contains(strings.TrimSpace(req.Path), "users-snapshot") {
		method = http.MethodGet
	}
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(context.Background(), method, "http://agentlink"+strings.TrimSpace(req.Path), body)
	if err != nil {
		return &agentlink.ResponseFrame{ID: req.ID, Status: 500, Error: err.Error()}
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	// The session authenticated once at connection setup; the handlers'
	// authAgent call resolves the identity from the request context.
	httpReq = withAuthenticatedAgent(httpReq, server)
	recorder := newStealthRecorder()
	handler(recorder, httpReq)
	return &agentlink.ResponseFrame{ID: req.ID, Status: recorder.status, Body: recorder.body.Bytes()}
}

// stealthRecorder is a minimal ResponseWriter capturing status and body.
type stealthRecorder struct {
	status int
	body   bytes.Buffer
	errText string
	header http.Header
}

func newStealthRecorder() *stealthRecorder {
	return &stealthRecorder{status: 200, header: http.Header{}}
}

func (r *stealthRecorder) Header() http.Header { return r.header }
func (r *stealthRecorder) WriteHeader(status int) { r.status = status }
func (r *stealthRecorder) Write(b []byte) (int, error) { return r.body.Write(b) }

func stealthJSONResponse(id int64, status int, body any) *agentlink.ResponseFrame {
	encoded, err := json.Marshal(body)
	if err != nil {
		return &agentlink.ResponseFrame{ID: id, Status: 500, Error: err.Error()}
	}
	return &agentlink.ResponseFrame{ID: id, Status: status, Body: encoded}
}

// ServeStealthDownload streams one release artifact to the agent. The agent
// requests chunks; this reader side runs in the session goroutine.
func (s *Server) ServeStealthDownload(server *model.Server, session *agentlink.Session, name string, offset int64) error {
	root, err := filepath.Abs(downloadsDir(s.staticDir))
	if err != nil {
		return err
	}
	switch filepath.Base(name) {
	case "oboard-agent-linux-amd64", "oboard-agent-linux-arm64", "oboard-sb-linux-amd64", "oboard-sb-linux-arm64", "oboard-realm-linux-amd64", "oboard-realm-linux-arm64", "release-manifest.json", "release-manifest.json.sig":
	default:
		return fmt.Errorf("unknown artifact")
	}
	path := filepath.Join(root, filepath.Base(name))
	file, err := os.Open(path) // #nosec G304 -- name is allowlisted above.
	if err != nil {
		return err
	}
	defer file.Close()
	if offset > 0 {
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}
	buf := make([]byte, 512<<10)
	for {
		n, readErr := file.Read(buf)
		if n > 0 {
			header, _ := json.Marshal(agentlink.DataHeader{Stream: name, Offset: offset})
			if err := session.WriteFrame(agentlink.NewDataFrame(append(header, buf[:n]...))); err != nil {
				return err
			}
			offset += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

func stealthRemoteIP(session *agentlink.Session) string {
	if addr := session.RemoteAddr(); addr != "" {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			return host
		}
		return addr
	}
	return "unknown"
}

func (s *Server) allowStealthRate(key string, limit int, window time.Duration) bool {
	return s.allowRateRaw(key, limit, window)
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
