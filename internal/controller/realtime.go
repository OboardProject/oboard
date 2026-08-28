package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

const realtimeProtocolVersion = 2

const uiEventPollTimeout = 25 * time.Second

const realtimePatchWindow = 500 * time.Millisecond

type realtimeMessage struct {
	Type            string                   `json:"type"`
	Protocol        int                      `json:"protocol,omitempty"`
	Sequence        uint64                   `json:"sequence"`
	Resources       []string                 `json:"resources,omitempty"`
	ServerSnapshots []realtimeServerSnapshot `json:"server_snapshots,omitempty"`
	ServerPatches   []realtimeServerPatch    `json:"server_patches,omitempty"`
}

type realtimeServerPatch struct {
	ServerID int64          `json:"server_id"`
	Fields   map[string]any `json:"fields"`
}

type realtimeServerSnapshot struct {
	ID                    int64              `json:"id"`
	Status                model.ServerStatus `json:"status"`
	CPUUsagePercent       float64            `json:"cpu_usage_percent"`
	MemoryUsedBytes       uint64             `json:"memory_used_bytes"`
	MemoryTotalBytes      uint64             `json:"memory_total_bytes"`
	AgentMemoryBytes      uint64             `json:"agent_memory_bytes"`
	DiskBytes             uint64             `json:"disk_bytes"`
	DiskTotalBytes        uint64             `json:"disk_total_bytes"`
	TCPConnectionCount    uint64             `json:"tcp_connection_count"`
	UDPConnectionCount    uint64             `json:"udp_connection_count"`
	ProcessCount          uint64             `json:"process_count"`
	NetworkUploadBPS      uint64             `json:"network_upload_bps"`
	NetworkDownloadBPS    uint64             `json:"network_download_bps"`
	TrafficUploadBytes    uint64             `json:"traffic_upload_bytes"`
	TrafficDownloadBytes  uint64             `json:"traffic_download_bytes"`
	ConnectivityStatus    string             `json:"connectivity_status"`
	ConnectivityLatencyMS int64              `json:"connectivity_latency_ms"`
	ConnectivityCheckedAt *time.Time         `json:"connectivity_checked_at,omitempty"`
	ConnectivityError     string             `json:"connectivity_error,omitempty"`
	TelemetryUpdatedAt    *time.Time         `json:"telemetry_updated_at,omitempty"`
	LastSeenAt            *time.Time         `json:"last_seen_at,omitempty"`
}

type realtimeBroker struct {
	mu                sync.Mutex
	clients           map[*realtimeClient]struct{}
	resourceSequences map[string]uint64
	sequence          atomic.Uint64
	telemetrySequence atomic.Uint64
	serverPatches     map[int64]map[string]any
	patchTimer        *time.Timer
	closed            bool
	counters          realtimeCounters
}

// realtimeCounters are process-local metrics used to assert that realtime
// broadcast cost stays independent of the number of connected UI clients:
// a full snapshot is built once and its serialized bytes are shared.
type realtimeCounters struct {
	snapshotBuilds atomic.Uint64
	snapshotRows   atomic.Uint64
	snapshotBytes  atomic.Uint64
	clients        atomic.Int64
}

func (c *realtimeCounters) snapshot() (builds, rows, bytes uint64, clients int64) {
	return c.snapshotBuilds.Load(), c.snapshotRows.Load(), c.snapshotBytes.Load(), c.clients.Load()
}

type realtimeClient struct {
	mu         sync.Mutex
	role       model.Role
	pending    map[string]struct{}
	sequence   uint64
	mergeCount int
	resync     bool
	mode       realtimeClientMode
	closed     bool
	wake       chan struct{}
	payloads   chan []byte
}

type realtimeClientMode uint8

const (
	realtimeClientAll realtimeClientMode = iota
	realtimeClientPolling
	realtimeClientLive
)

func newRealtimeBroker() *realtimeBroker {
	return &realtimeBroker{clients: make(map[*realtimeClient]struct{}), resourceSequences: make(map[string]uint64), serverPatches: make(map[int64]map[string]any)}
}

func (b *realtimeBroker) subscribe(role model.Role) (*realtimeClient, uint64, bool) {
	return b.subscribeMode(role, realtimeClientAll)
}

func (b *realtimeBroker) subscribePolling(role model.Role) (*realtimeClient, uint64, bool) {
	return b.subscribeMode(role, realtimeClientPolling)
}

func (b *realtimeBroker) subscribeLive(role model.Role) (*realtimeClient, uint64, bool) {
	return b.subscribeMode(role, realtimeClientLive)
}

func (b *realtimeBroker) subscribeMode(role model.Role, mode realtimeClientMode) (*realtimeClient, uint64, bool) {
	client := &realtimeClient{role: role, mode: mode, pending: make(map[string]struct{}), wake: make(chan struct{}, 1), payloads: make(chan []byte, 16)}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, b.sequence.Load(), false
	}
	b.clients[client] = struct{}{}
	b.counters.clients.Add(1)
	return client, b.sequence.Load(), true
}

func (b *realtimeBroker) publishServerPatch(patch realtimeServerPatch) {
	if patch.ServerID <= 0 || len(patch.Fields) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	merged := b.serverPatches[patch.ServerID]
	if merged == nil {
		merged = make(map[string]any, len(patch.Fields))
		b.serverPatches[patch.ServerID] = merged
	}
	for field, value := range patch.Fields {
		merged[field] = value
	}
	if b.patchTimer == nil {
		b.patchTimer = time.AfterFunc(realtimePatchWindow, b.flushServerPatches)
	}
}

func (b *realtimeBroker) flushServerPatches() {
	b.mu.Lock()
	if b.patchTimer != nil {
		b.patchTimer.Stop()
		b.patchTimer = nil
	}
	if b.closed || len(b.serverPatches) == 0 {
		b.mu.Unlock()
		return
	}
	patches := make([]realtimeServerPatch, 0, len(b.serverPatches))
	for serverID, fields := range b.serverPatches {
		patches = append(patches, realtimeServerPatch{ServerID: serverID, Fields: fields})
	}
	b.serverPatches = make(map[int64]map[string]any)
	b.mu.Unlock()

	sort.Slice(patches, func(i, j int) bool { return patches[i].ServerID < patches[j].ServerID })
	sequence := b.telemetrySequence.Add(1)
	payload, err := json.Marshal(realtimeMessage{Type: "server_patch", Sequence: sequence, ServerPatches: patches})
	if err != nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for client := range b.clients {
		if client.mode != realtimeClientLive || !roleAllows(client.role, model.RoleOperator) {
			continue
		}
		select {
		case client.payloads <- payload:
		default:
			client.close()
		}
	}
}

func (b *realtimeBroker) unsubscribe(client *realtimeClient) {
	if client == nil {
		return
	}
	b.mu.Lock()
	_, present := b.clients[client]
	delete(b.clients, client)
	if present {
		b.counters.clients.Add(-1)
	}
	b.mu.Unlock()
	client.close()
}

func (b *realtimeBroker) publish(resources ...string) {
	resources = normalizeRealtimeResources(resources)
	if len(resources) == 0 {
		return
	}
	sequence := b.sequence.Add(1)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for _, resource := range resources {
		b.resourceSequences[resource] = sequence
	}
	for client := range b.clients {
		client.enqueue(sequence, resources)
	}
}

func (b *realtimeBroker) changesSince(role model.Role, since uint64) realtimeMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	sequence := b.sequence.Load()
	if since > sequence {
		return realtimeMessage{Type: "resync_required", Sequence: sequence}
	}
	resources := make([]string, 0)
	for resource, changedAt := range b.resourceSequences {
		if changedAt > since && realtimeClientAllowsResource(role, realtimeClientPolling, resource) {
			resources = append(resources, resource)
		}
	}
	if len(resources) == 0 {
		return realtimeMessage{Type: "ready", Protocol: realtimeProtocolVersion, Sequence: sequence}
	}
	sort.Strings(resources)
	return realtimeMessage{Type: "invalidate", Sequence: sequence, Resources: resources}
}

func (b *realtimeBroker) close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	if b.patchTimer != nil {
		b.patchTimer.Stop()
		b.patchTimer = nil
	}
	clients := make([]*realtimeClient, 0, len(b.clients))
	for client := range b.clients {
		clients = append(clients, client)
	}
	b.clients = make(map[*realtimeClient]struct{})
	b.mu.Unlock()
	for _, client := range clients {
		client.close()
	}
}

func (c *realtimeClient) enqueue(sequence uint64, resources []string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	allowed := make([]string, 0, len(resources))
	for _, resource := range resources {
		if realtimeClientAllowsResource(c.role, c.mode, resource) {
			allowed = append(allowed, resource)
		}
	}
	if len(allowed) == 0 {
		c.mu.Unlock()
		return
	}
	c.sequence = sequence
	c.mergeCount++
	if c.mergeCount > 64 {
		c.resync = true
		clear(c.pending)
	} else if !c.resync {
		for _, resource := range allowed {
			c.pending[resource] = struct{}{}
		}
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func realtimeClientAllowsResource(role model.Role, mode realtimeClientMode, resource string) bool {
	if !roleAllows(role, realtimeResourceRole(resource)) {
		return false
	}
	live := realtimeLiveResource(resource)
	switch mode {
	case realtimeClientPolling:
		return !live
	case realtimeClientLive:
		return live
	default:
		return true
	}
}

func realtimeLiveResource(resource string) bool {
	return false
}

func (c *realtimeClient) drain() (realtimeMessage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return realtimeMessage{}, false
	}
	sequence := c.sequence
	if c.resync {
		c.resync = false
		c.mergeCount = 0
		return realtimeMessage{Type: "resync_required", Sequence: sequence}, true
	}
	if len(c.pending) == 0 {
		c.mergeCount = 0
		return realtimeMessage{}, false
	}
	resources := make([]string, 0, len(c.pending))
	for resource := range c.pending {
		resources = append(resources, resource)
	}
	sort.Strings(resources)
	clear(c.pending)
	c.mergeCount = 0
	return realtimeMessage{Type: "invalidate", Sequence: sequence, Resources: resources}, true
}

func (c *realtimeClient) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func normalizeRealtimeResources(resources []string) []string {
	seen := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		resource = strings.TrimSpace(resource)
		if resource != "" {
			seen[resource] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for resource := range seen {
		out = append(out, resource)
	}
	sort.Strings(out)
	return out
}

func realtimeResourceRole(resource string) model.Role {
	switch resource {
	case "user_overview":
		return model.RoleNone
	case "all", "account", "notifications", "subscriptions", "traffic":
		return model.RoleViewer
	case "servers", "server_runtime", "server_metrics", "tasks", "deployments", "configuration", "probes", "topology", "audit", "mtu", "port_forwards", "tunnels":
		return model.RoleOperator
	default:
		return model.RoleAdmin
	}
}

func (s *Server) publishRealtime(resources ...string) {
	if s.realtime != nil {
		s.realtime.publish(resources...)
	}
}

func (s *Server) publishServerPatch(result store.HealthApplyResult) {
	if s.realtime != nil {
		s.realtime.publishServerPatch(healthRealtimePatch(result))
	}
}

func healthRealtimePatch(result store.HealthApplyResult) realtimeServerPatch {
	previous, current := result.Prev, result.Curr
	fields := make(map[string]any)
	set := func(name string, changed bool, value any) {
		if changed {
			fields[name] = value
		}
	}
	set("status", previous.Status != current.Status, current.Status)
	set("public_ipv4", previous.PublicIPv4 != current.PublicIPv4, current.PublicIPv4)
	set("public_ipv6", previous.PublicIPv6 != current.PublicIPv6, current.PublicIPv6)
	set("interface_ipv6", previous.InterfaceIPv6 != current.InterfaceIPv6, current.InterfaceIPv6)
	set("detected_region_code", previous.DetectedRegionCode != current.DetectedRegionCode, current.DetectedRegionCode)
	set("os", previous.OS != current.OS, current.OS)
	set("distro_id", previous.DistroID != current.DistroID, current.DistroID)
	set("distro_version", previous.DistroVersion != current.DistroVersion, current.DistroVersion)
	set("distro_name", previous.DistroName != current.DistroName, current.DistroName)
	set("libc", previous.Libc != current.Libc, current.Libc)
	set("service_manager", previous.ServiceManager != current.ServiceManager, current.ServiceManager)
	set("package_manager", previous.PackageManager != current.PackageManager, current.PackageManager)
	set("arch", previous.Arch != current.Arch, current.Arch)
	set("kernel", previous.Kernel != current.Kernel, current.Kernel)
	set("cpu", previous.CPU != current.CPU, current.CPU)
	set("memory_bytes", previous.MemoryBytes != current.MemoryBytes, current.MemoryBytes)
	set("cpu_usage_percent", previous.CPUUsagePercent != current.CPUUsagePercent, current.CPUUsagePercent)
	set("memory_used_bytes", previous.MemoryUsedBytes != current.MemoryUsedBytes, current.MemoryUsedBytes)
	set("memory_total_bytes", previous.MemoryTotalBytes != current.MemoryTotalBytes, current.MemoryTotalBytes)
	set("agent_memory_bytes", previous.AgentMemoryBytes != current.AgentMemoryBytes, current.AgentMemoryBytes)
	set("disk_bytes", previous.DiskBytes != current.DiskBytes, current.DiskBytes)
	set("disk_total_bytes", previous.DiskTotalBytes != current.DiskTotalBytes, current.DiskTotalBytes)
	set("tcp_connection_count", previous.TCPConnectionCount != current.TCPConnectionCount, current.TCPConnectionCount)
	set("udp_connection_count", previous.UDPConnectionCount != current.UDPConnectionCount, current.UDPConnectionCount)
	set("process_count", previous.ProcessCount != current.ProcessCount, current.ProcessCount)
	set("agent_version", previous.AgentVersion != current.AgentVersion, current.AgentVersion)
	set("agent_build", previous.AgentBuild != current.AgentBuild, current.AgentBuild)
	set("sing_box_version", previous.SingBoxVersion != current.SingBoxVersion, current.SingBoxVersion)
	set("tcp_fastopen_state", previous.TCPFastOpenState != current.TCPFastOpenState, current.TCPFastOpenState)
	set("tcp_fastopen_value", previous.TCPFastOpenValue != current.TCPFastOpenValue, current.TCPFastOpenValue)
	set("network_upload_bps", previous.NetworkUploadBPS != current.NetworkUploadBPS, current.NetworkUploadBPS)
	set("network_download_bps", previous.NetworkDownloadBPS != current.NetworkDownloadBPS, current.NetworkDownloadBPS)
	set("traffic_upload_bytes", previous.TrafficUploadBytes != current.TrafficUploadBytes, current.TrafficUploadBytes)
	set("traffic_download_bytes", previous.TrafficDownloadBytes != current.TrafficDownloadBytes, current.TrafficDownloadBytes)
	set("connectivity_status", previous.ConnectivityStatus != current.ConnectivityStatus, current.ConnectivityStatus)
	set("connectivity_latency_ms", previous.ConnectivityLatencyMS != current.ConnectivityLatencyMS, current.ConnectivityLatencyMS)
	set("connectivity_checked_at", !timePointersEqual(previous.ConnectivityCheckedAt, current.ConnectivityCheckedAt), current.ConnectivityCheckedAt)
	set("connectivity_error", previous.ConnectivityError != current.ConnectivityError, current.ConnectivityError)
	set("telemetry_updated_at", !timePointersEqual(previous.TelemetryUpdatedAt, current.TelemetryUpdatedAt), current.TelemetryUpdatedAt)
	set("last_seen_at", !timePointersEqual(previous.LastSeenAt, current.LastSeenAt), current.LastSeenAt)
	return realtimeServerPatch{ServerID: current.ServerID, Fields: fields}
}

func timePointersEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

func realtimeResourcesForTask(taskType string) []string {
	resources := []string{"tasks"}
	switch taskType {
	case model.AgentTaskTypeApplyDeployment:
		resources = append(resources, "deployments", "servers", "topology", "probes", "subscriptions", "user_overview")
	case model.AgentTaskTypeApplyCoreConfig:
		resources = append(resources, "deployments", "servers", "dns")
	case model.AgentTaskTypeApplyTrafficPolicy:
		resources = append(resources, "tasks")
	case model.AgentTaskTypeUpdateAgent:
		resources = append(resources, "server_runtime")
	case model.AgentTaskTypeUninstallAgent:
		resources = append(resources, "server_runtime", "servers")
	case model.AgentTaskTypeUpdateAgentConfig:
		resources = append(resources, "settings", "server_runtime")
	case model.AgentTaskTypeProbeInbounds, model.AgentTaskTypeProbeInboundsExternal, model.AgentTaskTypeProbeExternalEgress, model.AgentTaskTypeProbeLatencyTargets:
		resources = append(resources, "probes", "topology")
	case model.AgentTaskTypeProbePortForwards:
		resources = append(resources, "probes", "port_forwards")
	case model.AgentTaskTypeDetectMTU:
		resources = append(resources, "mtu", "servers")
	case model.AgentTaskTypeBenchmarkDNS:
		resources = append(resources, "dns", "probes", "servers")
	case model.AgentTaskTypeCheckTime:
		resources = append(resources, "servers")
	case model.AgentTaskTypeIssueCertificateHTTP:
		resources = append(resources, "settings", "topology")
	}
	return normalizeRealtimeResources(resources)
}

func realtimeResourcesForTasks(tasks []model.AgentTask) []string {
	resources := make([]string, 0, len(tasks)+1)
	for _, task := range tasks {
		resources = append(resources, realtimeResourcesForTask(task.Type)...)
	}
	return normalizeRealtimeResources(resources)
}

func (s *Server) uiEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" || cookie.Value != currentSessionToken(r) {
		fail(w, errors.New("cookie session required"), http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Has("token") || r.URL.Query().Has("access_token") {
		fail(w, errors.New("query credentials are not supported"), http.StatusBadRequest)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	role := currentRole(r)
	client, _, ok := s.realtime.subscribeLive(role)
	if !ok {
		fail(w, errors.New("realtime service unavailable"), http.StatusServiceUnavailable)
		return
	}
	defer s.realtime.unsubscribe(client)

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(1024)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})
	ready, err := s.realtimeReadyMessage(r.Context(), role)
	if err != nil {
		return
	}
	readyPayload, err := json.Marshal(ready)
	if err != nil {
		return
	}
	if len(ready.ServerSnapshots) != 0 {
		s.realtime.counters.snapshotBytes.Add(uint64(len(readyPayload)))
	}
	if err := writeRealtimePayload(conn, readyPayload); err != nil {
		return
	}

	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-readDone:
			return
		case payload := <-client.payloads:
			if err := writeRealtimePayload(conn, payload); err != nil {
				return
			}
		case <-client.wake:
			return
		case <-ticker.C:
			valid, currentRole := s.realtimeSessionValid(r.Context(), cookie.Value, user.ID, user.SessionVersion)
			if !valid || currentRole != role {
				_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "session changed"), time.Now().Add(5*time.Second))
				return
			}
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		}
	}
}

func (s *Server) realtimeReadyMessage(ctx context.Context, role model.Role) (realtimeMessage, error) {
	ready := realtimeMessage{Type: "ready", Protocol: realtimeProtocolVersion}
	if !roleAllows(role, model.RoleOperator) {
		ready.Sequence = s.realtime.telemetrySequence.Load()
		return ready, nil
	}
	for {
		sequence := s.realtime.telemetrySequence.Load()
		snapshots, err := s.realtimeServerSnapshots(ctx)
		if err != nil {
			return realtimeMessage{}, err
		}
		if sequence == s.realtime.telemetrySequence.Load() {
			ready.Sequence = sequence
			ready.ServerSnapshots = snapshots
			return ready, nil
		}
	}
}

func (s *Server) realtimeServerSnapshots(ctx context.Context) ([]realtimeServerSnapshot, error) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	s.realtime.counters.snapshotBuilds.Add(1)
	s.realtime.counters.snapshotRows.Add(uint64(len(servers)))
	snapshots := make([]realtimeServerSnapshot, 0, len(servers))
	for _, server := range servers {
		snapshots = append(snapshots, realtimeServerSnapshot{
			ID:                    server.ID,
			Status:                server.Status,
			CPUUsagePercent:       server.CPUUsagePercent,
			MemoryUsedBytes:       server.MemoryUsedBytes,
			MemoryTotalBytes:      server.MemoryTotalBytes,
			AgentMemoryBytes:      server.AgentMemoryBytes,
			DiskBytes:             server.DiskBytes,
			DiskTotalBytes:        server.DiskTotalBytes,
			TCPConnectionCount:    server.TCPConnectionCount,
			UDPConnectionCount:    server.UDPConnectionCount,
			ProcessCount:          server.ProcessCount,
			NetworkUploadBPS:      server.NetworkUploadBPS,
			NetworkDownloadBPS:    server.NetworkDownloadBPS,
			TrafficUploadBytes:    server.TrafficUploadBytes,
			TrafficDownloadBytes:  server.TrafficDownloadBytes,
			ConnectivityStatus:    server.ConnectivityStatus,
			ConnectivityLatencyMS: server.ConnectivityLatencyMS,
			ConnectivityCheckedAt: server.ConnectivityCheckedAt,
			ConnectivityError:     server.ConnectivityError,
			TelemetryUpdatedAt:    server.TelemetryUpdatedAt,
			LastSeenAt:            server.LastSeenAt,
		})
	}
	return snapshots, nil
}

func (s *Server) uiPollEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	since := uint64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("since")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			fail(w, errors.New("invalid event sequence"), http.StatusBadRequest)
			return
		}
		since = value
	}
	role := currentRole(r)
	client, sequence, ok := s.realtime.subscribePolling(role)
	if !ok {
		fail(w, errors.New("event polling unavailable"), http.StatusServiceUnavailable)
		return
	}
	defer s.realtime.unsubscribe(client)
	if sequence != since {
		write(w, http.StatusOK, s.realtime.changesSince(role, since))
		return
	}
	timer := time.NewTimer(uiEventPollTimeout)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-client.wake:
		message, available := client.drain()
		if !available {
			fail(w, errors.New("event polling unavailable"), http.StatusServiceUnavailable)
			return
		}
		write(w, http.StatusOK, message)
	case <-timer.C:
		write(w, http.StatusOK, s.realtime.changesSince(role, since))
	}
}

func writeRealtimeMessage(conn *websocket.Conn, message realtimeMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return writeRealtimePayload(conn, payload)
}

func writeRealtimePayload(conn *websocket.Conn, payload []byte) error {
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteMessage(websocket.TextMessage, payload)
}

func (s *Server) realtimeSessionValid(ctx context.Context, token string, userID, sessionVersion int64) (bool, model.Role) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	claims, err := security.VerifySession(s.sessionSecret, token)
	if err != nil || claims.Subject != userID || claims.SessionVersion != sessionVersion {
		return false, ""
	}
	revoked, err := s.store.UserSessionRevoked(ctx, security.HashSecret(token))
	if err != nil || revoked {
		return false, ""
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil || user.Status != "active" || user.SessionVersion != sessionVersion {
		return false, ""
	}
	role, err := s.store.EffectiveUserRole(ctx, *user)
	return err == nil, role
}

type realtimeStatusWriter struct {
	http.ResponseWriter
	status   int
	buffered bool
	body     bytes.Buffer
}

func (w *realtimeStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *realtimeStatusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if !w.buffered {
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *realtimeStatusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.buffered {
		return w.body.Write(body)
	}
	return w.ResponseWriter.Write(body)
}

func (w *realtimeStatusWriter) flush(body []byte) {
	w.Header().Del("Content-Length")
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(status)
	_, _ = w.ResponseWriter.Write(body)
}

func (s *Server) realtimeInvalidation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		managedAPI := strings.HasPrefix(r.URL.Path, "/api/v1/ui/") || strings.HasPrefix(r.URL.Path, "/api/v1/") || strings.HasPrefix(r.URL.Path, "/api/v1/agent/")
		readOnlyPost := r.URL.Path == "/api/v1/query"
		if !managedAPI || readOnlyPost || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions || websocket.IsWebSocketUpgrade(r) {
			next.ServeHTTP(w, r)
			return
		}
		configurationMutation := configurationMutationPath(r.URL.Path, r.Method)
		beforeRevision := uint64(0)
		var affectedServerIDs []int64
		if configurationMutation && s.store != nil {
			beforeRevision, _ = s.store.ConfigurationRevision(r.Context())
			affectedServerIDs = s.configurationMutationServerIDs(r.Context(), r.URL.Path, r.Method)
		}
		recorder := &realtimeStatusWriter{ResponseWriter: w, buffered: configurationMutation}
		next.ServeHTTP(recorder, r)
		responseBody := recorder.body.Bytes()
		if recorder.status >= 200 && recorder.status < 300 {
			s.publishRealtime(realtimeResourcesForRequest(r.URL.Path)...)
			if configurationMutation && s.store != nil {
				if afterRevision, err := s.store.ConfigurationRevision(r.Context()); err == nil && afterRevision > beforeRevision {
					if len(affectedServerIDs) == 0 {
						if responseServerIDs, resolved := s.configurationMutationResponseServerIDs(r.Context(), r.URL.Path, responseBody); resolved {
							affectedServerIDs = responseServerIDs
						}
					}
					s.markConfigurationRevision(r.Context(), afterRevision, affectedServerIDs)
					responseBody = s.configurationMutationResponse(r.Context(), responseBody, afterRevision, affectedServerIDs)
				}
			}
		}
		if configurationMutation {
			recorder.flush(responseBody)
		}
	})
}

func realtimeResourcesForRequest(path string) []string {
	for _, prefix := range []string{"/api/v1/ui/", "/api/v1/agent/", "/api/v1/"} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
			break
		}
	}
	segment := strings.Split(strings.Trim(path, "/"), "/")[0]
	switch segment {
	case "servers":
		return []string{"servers", "topology", "subscriptions"}
	case "agents":
		return []string{"server_runtime"}
	case "agent-tasks":
		return []string{"tasks"}
	case "task-results":
		return nil
	case "configuration-sync":
		return []string{"configuration", "tasks", "deployments", "servers", "topology"}
	case "deployments":
		return []string{"tasks", "deployments"}
	case "traffic-reports":
		return []string{"traffic", "user_overview"}
	case "connection-reports":
		return []string{"audit", "user_overview"}
	case "dns-benchmarks", "dns-lists":
		return []string{"dns", "servers", "tasks", "probes"}
	case "mtu-detections":
		return []string{"mtu", "servers", "tasks", "probes"}
	case "port-forward-probes", "inbound-probes":
		return []string{"probes", "port_forwards", "topology", "tasks"}
	case "users", "user-groups", "user-group-members":
		return []string{"users", "subscriptions", "account", "topology", "user_overview"}
	case "subscriptions":
		return []string{"subscriptions", "account", "user_overview"}
	case "inbounds", "outbounds", "routing-rules", "external-outbounds", "proxy-paths", "proxy-path-steps", "warp-profiles":
		return []string{"topology", "subscriptions", "servers", "deployments", "user_overview"}
	case "port-forwards":
		return []string{"port_forwards", "topology", "deployments"}
	case "tunnels":
		return []string{"tunnels", "topology", "deployments"}
	case "dns-credentials", "dns-records", "dns-sync", "google-eab-credentials", "certificates":
		return []string{"dns", "settings", "topology"}
	case "notification-channels", "notification-announcements":
		return []string{"notifications"}
	case "settings", "controller-update", "agent-updates":
		return []string{"settings", "controller_update", "agent_updates", "servers", "user_overview"}
	case "backups":
		return []string{"backups", "settings"}
	case "changesets":
		return []string{"all"}
	case "api-principals", "oauth-clients", "ai", "approval-policies", "tool-audits":
		return []string{"automation"}
	case "auth", "me":
		return []string{"account", "user_overview"}
	default:
		return []string{"all"}
	}
}
