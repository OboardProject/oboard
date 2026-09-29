package plugin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/store"
)

type fakeHost struct {
	mu            sync.Mutex
	servers       map[int64]ServerInfo
	diagnostics   []DiagnosticRequest
	notifications []string
	callerDenied  bool
}

func (h *fakeHost) Server(_ context.Context, id int64) (ServerInfo, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	server, ok := h.servers[id]
	return server, ok
}
func (h *fakeHost) ListServers(context.Context) []ServerInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []ServerInfo{}
	for _, server := range h.servers {
		out = append(out, server)
	}
	return out
}
func (h *fakeHost) ServerHealth(_ context.Context, id int64) (map[string]any, error) {
	return map[string]any{"server_id": strconv.FormatInt(id, 10)}, nil
}
func (h *fakeHost) ServerMetrics(_ context.Context, id int64) (map[string]any, error) {
	return map[string]any{"server_id": strconv.FormatInt(id, 10)}, nil
}
func (h *fakeHost) NotificationChannelExists(_ context.Context, id int64) bool { return id == 5 }
func (h *fakeHost) SendNotification(_ context.Context, id int64, title, body string) error {
	h.mu.Lock()
	h.notifications = append(h.notifications, title+"|"+body)
	h.mu.Unlock()
	return nil
}
func (h *fakeHost) RunNetworkDiagnostic(_ context.Context, request DiagnosticRequest) (json.RawMessage, error) {
	h.mu.Lock()
	h.diagnostics = append(h.diagnostics, request)
	h.mu.Unlock()
	var envelope model.NetworkDiagnosticEnvelope
	_ = json.Unmarshal(request.Payload, &envelope)
	return mustMarshal(model.NetworkTraceResult{OperationID: envelope.OperationID, Target: "example.com", ResolvedIP: "93.184.216.34", IPFamily: "ipv4", Mode: "icmp", Reached: true, Hops: []model.NetworkTraceHop{{Hop: 1, Addresses: []string{"93.184.216.34"}, Probes: []model.NetworkTraceProbe{{Address: "93.184.216.34", RTTMS: 1.5}}}}}), nil
}
func (h *fakeHost) HTTPDenied(context.Context, netip.Addr) bool { return false }
func (h *fakeHost) EncryptSecret(plain string) (string, error)  { return "enc:" + plain, nil }
func (h *fakeHost) DecryptSecret(encrypted string) (string, error) {
	return strings.TrimPrefix(encrypted, "enc:"), nil
}
func (h *fakeHost) ResolveCaller(_ context.Context, ref CallerRef) (application.Principal, error) {
	if h.callerDenied {
		return application.Principal{}, ErrPermissionDenied
	}
	caller := operatorPrincipal()
	caller.ID = ref.PrincipalID
	return caller, nil
}
func (h *fakeHost) RuntimeStatus() RuntimeHostStatus {
	return RuntimeHostStatus{Installed: true, WorkerConnected: true, IsolationAvailable: true}
}

func adminPrincipal() application.Principal {
	id := int64(1)
	return application.Principal{ID: "user:1", UserID: &id, Type: model.APIPrincipalOAuth, Role: model.RoleAdmin, Interactive: true, ClientName: "oboard-web", Scopes: []string{"*"}}
}

func operatorPrincipal() application.Principal {
	id := int64(2)
	return application.Principal{ID: "user:2", UserID: &id, Type: model.APIPrincipalOAuth, Role: model.RoleOperator, Interactive: true, ClientName: "oboard-web", Scopes: []string{"*"}}
}

const gatewayManifest = `{
  "id": "acme.gateway-test",
  "name": "Gateway test",
  "version": "1.0.0",
  "description": "",
  "runtime": "oboard-js",
  "entry": "main.js",
  "capabilities": ["servers.read", "network.trace", "state.read", "state.write", "secrets.use", "notifications.send"],
  "environment": [
    {"name": "SERVER", "type": "server", "label": "服务器", "required": true},
    {"name": "API_KEY", "type": "secret", "label": "密钥", "required": true}
  ],
  "triggers": {"schedule": true}
}`

type harness struct {
	t       *testing.T
	service *Service
	store   *store.Store
	host    *fakeHost
	ctx     context.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "plugins.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	host := &fakeHost{servers: map[int64]ServerInfo{
		2: {ID: 2, Name: "Tokyo-01", Online: true, Enrolled: true, PluginsGate: true, Capabilities: []string{model.AgentCapabilityNetworkDiagnostics, model.AgentCapabilityNetworkTraceICMP}},
		3: {ID: 3, Name: "Osaka-01", Online: true, Enrolled: true, PluginsGate: true, Capabilities: []string{model.AgentCapabilityNetworkDiagnostics, model.AgentCapabilityNetworkTraceICMP}},
	}}
	service := NewService(db, capability.NewCatalog().RBAC(), host)
	ctx := context.Background()
	if err := db.SetSettings(ctx, map[string]string{model.PluginSettingEnabled: "true"}); err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, service: service, store: db, host: host, ctx: ctx}
}

func (h *harness) candidate(manifestJSON string, publisher string) PackageCandidate {
	manifest := mustManifest(h.t, manifestJSON)
	sum := sha256.Sum256([]byte(manifestJSON))
	return PackageCandidate{Manifest: manifest, Source: "function main(run) { return 1 }", SHA256: hex.EncodeToString(sum[:]), PublisherIdentity: publisher, SignatureState: model.PluginSignatureUnsigned, SourceKind: model.PluginSourceUpload}
}

// install sets up an enabled, configured and granted instance.
func (h *harness) install(grant Grant) (InstallResult, InstanceDetail) {
	h.t.Helper()
	candidate := h.candidate(gatewayManifest, model.PluginPublisherLocal)
	result, err := h.service.InstallPackage(h.ctx, adminPrincipal(), candidate, candidate.SHA256)
	if err != nil {
		h.t.Fatal(err)
	}
	instance, err := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID)
	if err != nil || instance.Enabled || instance.Grant != nil || instance.ConfigStatus != ConfigRequired {
		h.t.Fatalf("a fresh instance must be disabled, ungranted and unconfigured: %+v %v", instance, err)
	}
	if _, err := h.service.SaveEnvironment(h.ctx, operatorPrincipal(), result.InstanceID, instance.Revision, EnvironmentInput{Values: map[string]json.RawMessage{"SERVER": raw(`"2"`)}}); err != nil {
		h.t.Fatal(err)
	}
	if err := h.service.SetSecret(h.ctx, adminPrincipal(), result.InstanceID, "API_KEY", "s3cret-value"); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.service.SetGrant(h.ctx, adminPrincipal(), result.InstanceID, 0, grant); err != nil {
		h.t.Fatal(err)
	}
	if err := h.service.SetInstallationEnabled(h.ctx, adminPrincipal(), result.InstallationID, true); err != nil {
		h.t.Fatal(err)
	}
	enabled := true
	detail, err := h.service.UpdateInstance(h.ctx, adminPrincipal(), result.InstanceID, InstanceUpdate{Enabled: &enabled})
	if err != nil || detail.Status != StatusReady {
		h.t.Fatalf("instance not ready: %+v %v", detail, err)
	}
	return result, detail
}

func (h *harness) startRun(instanceID int64) *pluginrpc.RunLease {
	h.t.Helper()
	if _, err := h.service.RunManually(h.ctx, operatorPrincipal(), instanceID, "k-"+strconv.FormatInt(time.Now().UnixNano(), 10)); err != nil {
		h.t.Fatal(err)
	}
	lease, err := h.service.Lease(h.ctx, "worker-1")
	if err != nil || lease == nil {
		h.t.Fatalf("lease failed: %+v %v", lease, err)
	}
	return lease
}

func (h *harness) call(lease *pluginrpc.RunLease, method string, args any) pluginrpc.CallResponse {
	return h.service.Invoke(h.ctx, pluginrpc.CallRequest{WorkerID: "worker-1", RunUUID: lease.RunUUID, LeaseGeneration: lease.LeaseGeneration, Method: method, Arguments: mustMarshal(args)})
}

func fullGrant() Grant {
	return Grant{Capabilities: map[string]CapabilityGrant{
		CapServersRead: {Servers: []int64{2}}, CapNetworkTrace: {Servers: []int64{2}},
		CapStateRead: {}, CapStateWrite: {}, CapSecretsUse: {}, CapNotificationsSend: {Channels: []int64{5}},
	}}
}

func expectCode(t *testing.T, response pluginrpc.CallResponse, code string) {
	t.Helper()
	if response.OK || response.Code != code {
		t.Fatalf("expected %s, got ok=%v code=%s message=%s", code, response.OK, response.Code, response.Message)
	}
}

func TestLeaseCarriesOnlyTypedEnvironmentAndSecretReferences(t *testing.T) {
	h := newHarness(t)
	result, _ := h.install(fullGrant())
	lease := h.startRun(result.InstanceID)
	raw, _ := json.Marshal(lease)
	if strings.Contains(string(raw), "s3cret-value") {
		t.Fatal("secret plaintext reached the runner")
	}
	if lease.Environment["SERVER"].Raw != "2" || lease.Environment["API_KEY"].Raw != SecretRef(result.InstanceID, "API_KEY") {
		t.Fatalf("unexpected environment: %+v", lease.Environment)
	}
	if lease.Limits.TimeoutMS <= 0 || lease.Context.Trigger != model.PluginTriggerManual {
		t.Fatalf("unexpected lease: %+v", lease)
	}
}

func TestGatewayEnforcesManifestGrantAndResourceScope(t *testing.T) {
	h := newHarness(t)
	grant := fullGrant()
	delete(grant.Capabilities, CapStateWrite)
	result, _ := h.install(grant)
	lease := h.startRun(result.InstanceID)
	if response := h.call(lease, "servers.get", map[string]any{"server_id": "2"}); !response.OK {
		t.Fatalf("granted server read failed: %+v", response)
	}
	// Selecting a server in the environment is configuration, never a grant.
	expectCode(t, h.call(lease, "servers.get", map[string]any{"server_id": "3"}), CodeResourceDenied)
	expectCode(t, h.call(lease, "servers.get", map[string]any{"server_id": "Tokyo-01"}), CodeInvalidArgument)
	expectCode(t, h.call(lease, "http.request", map[string]any{"url": "https://api.example.com"}), CodeCapabilityDenied)
	expectCode(t, h.call(lease, "state.set", map[string]any{"key": "a", "value": 1}), CodeCapabilityDenied)
	expectCode(t, h.call(lease, "shell.exec", map[string]any{"command": "id"}), CodeUnsupportedCapability)
	expectCode(t, h.call(lease, "agent.task", map[string]any{"type": "remote_exec"}), CodeUnsupportedCapability)
	expectCode(t, h.call(lease, "servers.get", map[string]any{"server_id": "2", "extra": true}), CodeInvalidArgument)
	listing := h.call(lease, "servers.list", map[string]any{})
	if !listing.OK || strings.Contains(string(listing.Result), "Osaka") {
		t.Fatalf("servers.list must only return granted servers: %s", listing.Result)
	}
	// Revoking the grant takes effect on the next call of the running run.
	if err := h.service.RevokeGrant(h.ctx, adminPrincipal(), result.InstanceID); err != nil {
		t.Fatal(err)
	}
	expectCode(t, h.call(lease, "servers.get", map[string]any{"server_id": "2"}), CodeCapabilityDenied)
	// A stale lease generation or another worker is refused.
	bad := *lease
	bad.LeaseGeneration++
	expectCode(t, h.call(&bad, "state.get", map[string]any{"key": "a"}), CodeCancelled)
}

func TestGatewayRechecksCallerAndCancellation(t *testing.T) {
	h := newHarness(t)
	result, _ := h.install(fullGrant())
	lease := h.startRun(result.InstanceID)
	h.host.callerDenied = true
	expectCode(t, h.call(lease, "servers.get", map[string]any{"server_id": "2"}), CodeCapabilityDenied)
	h.host.callerDenied = false
	run, _ := h.store.GetPluginRunByUUID(h.ctx, lease.RunUUID)
	if _, err := h.service.CancelRun(h.ctx, operatorPrincipal(), run.ID); err != nil {
		t.Fatal(err)
	}
	expectCode(t, h.call(lease, "servers.get", map[string]any{"server_id": "2"}), CodeCancelled)
	if !h.service.CancelRequested(h.ctx, lease.RunUUID, lease.LeaseGeneration) {
		t.Fatal("worker cancel poll must report the cancellation")
	}
}

func TestGatewayStateIsolationQuotaAndCAS(t *testing.T) {
	h := newHarness(t)
	result, _ := h.install(fullGrant())
	lease := h.startRun(result.InstanceID)
	if response := h.call(lease, "state.set", map[string]any{"key": "counter", "value": map[string]int{"n": 1}}); !response.OK {
		t.Fatalf("set failed: %+v", response)
	}
	got := h.call(lease, "state.get", map[string]any{"key": "counter"})
	if !got.OK || !strings.Contains(string(got.Result), `"n":1`) || !strings.Contains(string(got.Result), `"version":1`) {
		t.Fatalf("get failed: %s", got.Result)
	}
	expectCode(t, h.call(lease, "state.compareAndSwap", map[string]any{"key": "counter", "expected_version": 0, "value": 2}), CodeStateConflict)
	if response := h.call(lease, "state.compareAndSwap", map[string]any{"key": "counter", "expected_version": 1, "value": 2}); !response.OK {
		t.Fatalf("CAS failed: %+v", response)
	}
	expectCode(t, h.call(lease, "state.set", map[string]any{"key": "big", "value": strings.Repeat("x", MaxStateValueBytes)}), CodeStateQuotaExceeded)
	// A second instance of the same plugin cannot see the first one's state.
	other, err := h.service.CreateInstance(h.ctx, operatorPrincipal(), result.InstallationID, "second")
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := h.store.GetPluginState(h.ctx, other.ID, "counter"); err == nil {
		t.Fatalf("state leaked across instances: %+v", entry)
	}
	for i := 0; i < MaxStateKeys; i++ {
		_, _ = h.store.PutPluginState(h.ctx, result.InstanceID, "k"+strconv.Itoa(i), json.RawMessage(`1`), nil, MaxStateKeys, MaxStateTotalBytes)
	}
	if _, err := h.store.PutPluginState(h.ctx, result.InstanceID, "overflow", json.RawMessage(`1`), nil, MaxStateKeys, MaxStateTotalBytes); err != store.ErrPluginStateQuota {
		t.Fatalf("key quota not enforced: %v", err)
	}
}

func TestSecretsAreUsedNeverRead(t *testing.T) {
	h := newHarness(t)
	result, _ := h.install(fullGrant())
	lease := h.startRun(result.InstanceID)
	ref := SecretRef(result.InstanceID, "API_KEY")
	response := h.call(lease, "crypto.hmac", map[string]any{"algorithm": "sha256", "key": map[string]string{"$secret": ref}, "data": "payload"})
	mac := hmac.New(sha256.New, []byte("s3cret-value"))
	mac.Write([]byte("payload"))
	if !response.OK || !strings.Contains(string(response.Result), hex.EncodeToString(mac.Sum(nil))) || strings.Contains(string(response.Result), "s3cret") {
		t.Fatalf("hmac with secret ref failed: %+v", response)
	}
	expectCode(t, h.call(lease, "crypto.hmac", map[string]any{"algorithm": "sha256", "key": map[string]string{"$secret": SecretRef(result.InstanceID+1, "API_KEY")}, "data": "x"}), CodeSecretNotConfigured)
	expectCode(t, h.call(lease, "crypto.hmac", map[string]any{"algorithm": "sha256", "key": map[string]string{"$secret": SecretRef(result.InstanceID, "OTHER")}, "data": "x"}), CodeSecretNotConfigured)
	detail, _ := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID)
	encoded, _ := json.Marshal(detail)
	if strings.Contains(string(encoded), "s3cret") || !detail.Secrets["API_KEY"].Configured {
		t.Fatalf("instance view must report configured without the value: %s", encoded)
	}
	// Logs, results and errors are redacted before storage.
	err := h.service.Complete(h.ctx, pluginrpc.CompleteRequest{WorkerID: "worker-1", RunUUID: lease.RunUUID, LeaseGeneration: lease.LeaseGeneration, Status: "failed", ErrorCode: "SCRIPT_ERROR", ErrorMessage: "bad key s3cret-value", Result: json.RawMessage(`{"echo":"czNjcmV0LXZhbHVl"}`), Logs: []pluginrpc.LogLine{{Seq: 1, Level: "info", Message: "token=s3cret-value"}}})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := h.store.GetPluginRunByUUID(h.ctx, lease.RunUUID)
	logs, _ := h.store.ListPluginRunLogs(h.ctx, run.ID, 0, 10)
	stored, _ := json.Marshal([]any{run, logs})
	if strings.Contains(string(stored), "s3cret-value") || strings.Contains(string(stored), "czNjcmV0LXZhbHVl") {
		t.Fatalf("secret leaked into run record: %s", stored)
	}
}

func TestNetworkCapabilityChecksAndAudit(t *testing.T) {
	h := newHarness(t)
	grant := fullGrant()
	grant.Capabilities[CapNetworkTrace] = CapabilityGrant{Servers: []int64{2, 3}}
	result, _ := h.install(grant)
	lease := h.startRun(result.InstanceID)
	expectCode(t, h.call(lease, "network.trace", map[string]any{"server_id": "2", "target": "10.0.0.1"}), CodeTargetNotAllowed)
	expectCode(t, h.call(lease, "network.trace", map[string]any{"server_id": "2", "target": "169.254.169.254"}), CodeTargetNotAllowed)
	expectCode(t, h.call(lease, "network.trace", map[string]any{"server_id": "2", "target": "example.com", "max_hops": 99}), CodeInvalidArgument)
	expectCode(t, h.call(lease, "network.trace", map[string]any{"server_id": "2", "target": "example.com", "command": "traceroute"}), CodeInvalidArgument)
	h.host.servers[3] = ServerInfo{ID: 3, Online: false, PluginsGate: true, Capabilities: h.host.servers[2].Capabilities}
	expectCode(t, h.call(lease, "network.trace", map[string]any{"server_id": "3", "target": "example.com"}), CodeServerOffline)
	h.host.servers[3] = ServerInfo{ID: 3, Online: true, PluginsGate: false, Capabilities: h.host.servers[2].Capabilities}
	expectCode(t, h.call(lease, "network.trace", map[string]any{"server_id": "3", "target": "example.com"}), CodeAgentPolicyDenied)
	h.host.servers[3] = ServerInfo{ID: 3, Online: true, PluginsGate: true}
	expectCode(t, h.call(lease, "network.trace", map[string]any{"server_id": "3", "target": "example.com"}), CodeUnsupportedCapability)
	if len(h.host.diagnostics) != 0 {
		t.Fatal("refused diagnostics must never reach the Agent")
	}
	response := h.call(lease, "network.trace", map[string]any{"server_id": "2", "target": "example.com", "max_hops": 12})
	if !response.OK || !strings.Contains(string(response.Result), `"reached":true`) {
		t.Fatalf("trace failed: %+v", response)
	}
	var payload model.NetworkTraceTaskPayload
	if err := json.Unmarshal(h.host.diagnostics[0].Payload, &payload); err != nil || payload.Origin != model.NetworkDiagnosticOriginPlugin || payload.MaxHops != 12 || payload.Mode != "icmp" || payload.Target != "example.com" {
		t.Fatalf("unexpected structured payload: %+v %v", payload, err)
	}
	events, _ := h.store.ListPluginAuditEvents(h.ctx, result.InstanceID, 0, 50)
	if len(events) == 0 || events[0].Capability != CapNetworkTrace || events[0].Result != "succeeded" {
		t.Fatalf("trace call not audited: %+v", events)
	}
}

func TestHTTPGatewayRefusesUngrantedAndPrivateTargets(t *testing.T) {
	h := newHarness(t)
	manifest := withField(t, gatewayManifest, "capabilities", []string{"http.request", "secrets.use", "servers.read"})
	manifest = withField(t, manifest, "http", map[string]any{"hosts": []string{"api.example.com", "*.aliyuncs.com"}, "methods": []string{"GET"}})
	candidate := h.candidate(manifest, model.PluginPublisherLocal)
	result, err := h.service.InstallPackage(h.ctx, adminPrincipal(), candidate, candidate.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	instance, _ := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID)
	_, _ = h.service.SaveEnvironment(h.ctx, adminPrincipal(), result.InstanceID, instance.Revision, EnvironmentInput{Values: map[string]json.RawMessage{"SERVER": raw(`"2"`)}})
	_ = h.service.SetSecret(h.ctx, adminPrincipal(), result.InstanceID, "API_KEY", "k")
	if _, err := h.service.SetGrant(h.ctx, adminPrincipal(), result.InstanceID, 0, Grant{Capabilities: map[string]CapabilityGrant{CapHTTPRequest: {Hosts: []string{"*.aliyuncs.com"}}, CapServersRead: {Servers: []int64{2}}}}); err != nil {
		t.Fatal(err)
	}
	_ = h.service.SetInstallationEnabled(h.ctx, adminPrincipal(), result.InstallationID, true)
	enabled := true
	_, _ = h.service.UpdateInstance(h.ctx, adminPrincipal(), result.InstanceID, InstanceUpdate{Enabled: &enabled})
	lease := h.startRun(result.InstanceID)
	expectCode(t, h.call(lease, "http.request", map[string]any{"url": "https://api.example.com/x"}), CodeHTTPHostDenied)
	expectCode(t, h.call(lease, "http.request", map[string]any{"url": "https://127.0.0.1/x"}), CodeHTTPPrivateAddressDenied)
	expectCode(t, h.call(lease, "http.request", map[string]any{"url": "https://169.254.169.254/latest/meta-data"}), CodeHTTPPrivateAddressDenied)
	expectCode(t, h.call(lease, "http.request", map[string]any{"url": "http://ecs.aliyuncs.com/"}), CodeInvalidArgument)
	expectCode(t, h.call(lease, "http.request", map[string]any{"url": "file:///etc/passwd"}), CodeInvalidArgument)
	expectCode(t, h.call(lease, "http.request", map[string]any{"url": "https://ecs.aliyuncs.com/", "method": "DELETE"}), CodeHTTPHostDenied)
	// secrets.use is declared but not granted here, so a secret header is refused.
	expectCode(t, h.call(lease, "http.request", map[string]any{"url": "https://ecs.aliyuncs.com/", "auth": map[string]any{"type": "bearer", "secret": map[string]string{"$secret": SecretRef(result.InstanceID, "API_KEY")}}}), CodeCapabilityDenied)
}

func TestUpdateRequiresReviewForNewPermissionsAndKeepsPublisher(t *testing.T) {
	h := newHarness(t)
	result, _ := h.install(fullGrant())
	expanded := withField(t, gatewayManifest, "version", "1.1.0")
	expanded = withField(t, expanded, "capabilities", []string{"servers.read", "network.trace", "network.ping", "state.read", "state.write", "secrets.use", "notifications.send"})
	other := h.candidate(expanded, "ed25519:abc")
	preview, err := h.service.PreviewPackage(h.ctx, adminPrincipal(), other)
	if err != nil || preview.Blocked == "" {
		t.Fatalf("publisher change must be blocked in preview: %+v %v", preview, err)
	}
	if _, err := h.service.InstallPackage(h.ctx, adminPrincipal(), other, other.SHA256); CodeOf(err) != CodePublisherChanged {
		t.Fatalf("publisher change must be rejected: %v", err)
	}
	update := h.candidate(expanded, model.PluginPublisherLocal)
	updated, err := h.service.InstallPackage(h.ctx, adminPrincipal(), update, update.SHA256)
	if err != nil || !updated.ReviewRequired || !updated.Diff.Expanded {
		t.Fatalf("expanded update must require review: %+v %v", updated, err)
	}
	detail, _ := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID)
	if detail.Status != StatusPermissionReviewRequired || detail.Grant == nil || detail.Grant.Capabilities[CapNetworkPing].Servers != nil {
		t.Fatalf("new capability must not be granted automatically: %+v", detail)
	}
	if _, err := h.service.RunManually(h.ctx, operatorPrincipal(), result.InstanceID, "blocked"); CodeOf(err) != CodePermissionReviewRequired {
		t.Fatalf("runs must wait for review: %v", err)
	}
	if _, err := h.service.SetGrant(h.ctx, adminPrincipal(), result.InstanceID, detail.Grant.Revision, fullGrant()); err != nil {
		t.Fatal(err)
	}
	if detail, _ := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID); detail.Status != StatusReady {
		t.Fatalf("review not cleared: %+v", detail.Status)
	}
	// A new required variable puts the instance into configuration_required.
	required := withField(t, expanded, "version", "1.2.0")
	required = withField(t, required, "environment", []map[string]any{{"name": "SERVER", "type": "server", "label": "s", "required": true}, {"name": "API_KEY", "type": "secret", "label": "k", "required": true}, {"name": "REGION", "type": "string", "label": "r", "required": true}})
	next := h.candidate(required, model.PluginPublisherLocal)
	if _, err := h.service.InstallPackage(h.ctx, adminPrincipal(), next, next.SHA256); err != nil {
		t.Fatal(err)
	}
	if detail, _ := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID); detail.Status != ConfigRequired {
		t.Fatalf("new required variable must require configuration: %s", detail.Status)
	}
}

func TestDeletedServerAndFailureStreak(t *testing.T) {
	h := newHarness(t)
	result, _ := h.install(fullGrant())
	for i := 0; i < AutoPauseFailureStreak; i++ {
		lease := h.startRun(result.InstanceID)
		if err := h.service.Complete(h.ctx, pluginrpc.CompleteRequest{WorkerID: "worker-1", RunUUID: lease.RunUUID, LeaseGeneration: lease.LeaseGeneration, Status: "failed", ErrorCode: "SCRIPT_ERROR"}); err != nil {
			t.Fatal(err)
		}
	}
	detail, _ := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID)
	if !detail.AutoPaused || detail.Status != StatusAutoPaused || detail.FailureStreak != AutoPauseFailureStreak {
		t.Fatalf("instance must auto pause: %+v", detail.InstanceSummary)
	}
	resumed, err := h.service.UpdateInstance(h.ctx, operatorPrincipal(), result.InstanceID, InstanceUpdate{Resume: true})
	if err != nil || resumed.AutoPaused || resumed.Status != StatusReady {
		t.Fatalf("resume failed: %+v %v", resumed.InstanceSummary, err)
	}
	delete(h.host.servers, 2)
	if _, err := h.service.RunManually(h.ctx, operatorPrincipal(), result.InstanceID, "gone"); CodeOf(err) != CodeServerNotFound {
		t.Fatalf("deleted server must stop runs: %v", err)
	}
}

func TestSchedulesCoalesceAndEventsStayInScope(t *testing.T) {
	h := newHarness(t)
	manifest := withField(t, gatewayManifest, "capabilities", []string{"servers.read", "events.server_status", "secrets.use"})
	manifest = withField(t, manifest, "triggers", map[string]any{"schedule": true, "events": []string{"server.offline"}})
	candidate := h.candidate(manifest, model.PluginPublisherLocal)
	result, _ := h.service.InstallPackage(h.ctx, adminPrincipal(), candidate, candidate.SHA256)
	instance, _ := h.service.GetInstance(h.ctx, adminPrincipal(), result.InstanceID)
	_, _ = h.service.SaveEnvironment(h.ctx, adminPrincipal(), result.InstanceID, instance.Revision, EnvironmentInput{Values: map[string]json.RawMessage{"SERVER": raw(`"2"`)}})
	_ = h.service.SetSecret(h.ctx, adminPrincipal(), result.InstanceID, "API_KEY", "k")
	if _, err := h.service.SetGrant(h.ctx, adminPrincipal(), result.InstanceID, 0, Grant{Capabilities: map[string]CapabilityGrant{CapServersRead: {Servers: []int64{2}}, CapEventsServerStatus: {Servers: []int64{2}}}}); err != nil {
		t.Fatal(err)
	}
	_ = h.service.SetInstallationEnabled(h.ctx, adminPrincipal(), result.InstallationID, true)
	enabled := true
	_, _ = h.service.UpdateInstance(h.ctx, adminPrincipal(), result.InstanceID, InstanceUpdate{Enabled: &enabled})
	if _, err := h.service.CreateSchedule(h.ctx, operatorPrincipal(), result.InstanceID, ScheduleInput{Kind: "interval", Interval: "30s", Enabled: true}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("interval below one minute accepted: %v", err)
	}
	if _, err := h.service.CreateSchedule(h.ctx, operatorPrincipal(), result.InstanceID, ScheduleInput{Kind: "event", Event: "server.online", Enabled: true}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("undeclared event accepted: %v", err)
	}
	schedule, err := h.service.CreateSchedule(h.ctx, operatorPrincipal(), result.InstanceID, ScheduleInput{Kind: "interval", Interval: "1m", Enabled: true})
	if err != nil || schedule.NextDueAt == nil {
		t.Fatalf("schedule not created: %+v %v", schedule, err)
	}
	fire := func() {
		current, _ := h.store.GetPluginSchedule(h.ctx, schedule.ID)
		due := current.NextDueAt.Add(time.Second)
		h.service.now = func() time.Time { return due }
		h.service.tickSchedules(h.ctx)
	}
	fire()
	fire()
	runs, _ := h.store.ListPluginRuns(h.ctx, store.PluginRunFilter{InstanceID: result.InstanceID})
	stored, _ := h.store.GetPluginSchedule(h.ctx, schedule.ID)
	if len(runs) != 1 || stored.LastSkipReason == "" {
		t.Fatalf("overlapping slots must coalesce: runs=%d skip=%q", len(runs), stored.LastSkipReason)
	}
}
