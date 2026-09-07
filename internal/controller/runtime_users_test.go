package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRuntimeUsersFastLaneEnvelopeAndAck(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "users-test-secret", "")
	server := &model.Server{
		Name: "users-node", PublicIPv4: "203.0.113.19", AgentID: "users-agent",
		AgentTokenHash: security.HashSecret("users-token"), Status: model.ServerOnline,
		KernelCapabilities: []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, model.AgentCapabilityRuntimeUsersVLESS},
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "vless", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}

	controlCh := make(chan any, 4)
	srv.registerAgentLive(server.ID, controlCh)
	defer srv.unregisterAgentLive(server.ID, controlCh)
	srv.reconcileRuntimeUsersSync(ctx, false)
	var envelope model.UsersEnvelope
	select {
	case payload := <-controlCh:
		var ok bool
		envelope, ok = payload.(model.UsersEnvelope)
		if !ok {
			t.Fatalf("unexpected control payload %T", payload)
		}
	default:
		t.Fatal("worker did not push a users_update on the live socket")
	}
	if envelope.Type != model.AgentControlUsersUpdate || envelope.ServerID != server.ID || envelope.MessageID == "" {
		t.Fatalf("envelope shape: %+v", envelope)
	}
	var req model.UsersInstallRequest
	if err := json.Unmarshal([]byte(envelope.UsersJSON), &req); err != nil {
		t.Fatal(err)
	}
	fields := security.UsersEnvelopeFields{ServerID: envelope.ServerID, MessageID: envelope.MessageID, Revision: req.UsersRevision, Digest: req.UsersDigest, UsersJSON: envelope.UsersJSON}
	if !security.VerifyUsersEnvelope(server.AgentTokenHash, fields, envelope.Signature) {
		t.Fatal("envelope signature does not verify with the Agent token hash")
	}
	if security.VerifyUsersEnvelope(security.HashSecret("other"), fields, envelope.Signature) {
		t.Fatal("envelope signature verified with a foreign key")
	}
	if req.Mode != "full" || req.UsersRevision != 1 || len(req.Entries) != 1 || req.Entries[0].InboundTag != "in-"+strconv.FormatInt(inbound.ID, 10) || req.Entries[0].AuthUser == "" || req.Entries[0].AuthorizationKey == "" {
		t.Fatalf("unexpected users package %+v", req)
	}
	state, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.DeliveredRevision != 1 || state.DeliveredMessageID != envelope.MessageID || state.Confirmed() {
		t.Fatalf("delivery not recorded or confirmed prematurely: %+v", state)
	}
	srv.reconcileRuntimeUsersSync(ctx, false)
	select {
	case payload := <-controlCh:
		t.Fatalf("duplicate push while unconfirmed: %+v", payload)
	default:
	}

	ackRaw, _ := json.Marshal(map[string]any{"type": model.AgentControlUsersAck, "message_id": envelope.MessageID, "revision": req.UsersRevision, "digest": req.UsersDigest, "confirmed": true, "boot_id": "boot-1"})
	var ackMessage map[string]json.RawMessage
	_ = json.Unmarshal(ackRaw, &ackMessage)
	srv.handleUsersAck(ctx, server, ackMessage)
	state, _ = db.RuntimeUserState(ctx, server.ID)
	if !state.Confirmed() || state.ConfirmedBootID != "boot-1" || state.ConfirmedDigest != req.UsersDigest {
		t.Fatalf("ack did not confirm: %+v", state)
	}

	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.reconcileProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	srv.wakeRuntimeUsersSync()
	srv.reconcileRuntimeUsersSync(ctx, false)
	var revoke model.UsersEnvelope
	select {
	case payload := <-controlCh:
		revoke = payload.(model.UsersEnvelope)
	default:
		t.Fatal("users revoke was not pushed")
	}
	var revokeReq model.UsersInstallRequest
	_ = json.Unmarshal([]byte(revoke.UsersJSON), &revokeReq)
	if revokeReq.UsersRevision != 2 || len(revokeReq.Entries) != 0 {
		t.Fatalf("revoke package: %+v", revokeReq)
	}
	srv.recordUsersAck(ctx, server, model.UsersAck{Revision: 1, Confirmed: true, BootID: "boot-1"})
	state, _ = db.RuntimeUserState(ctx, server.ID)
	if state.Confirmed() {
		t.Fatalf("stale ack confirmed revision 2: %+v", state)
	}

	handler := srv.Handler()
	httpReq := httptest.NewRequest(http.MethodGet, "/api/v1/agent/users-snapshot", nil)
	httpReq.Header.Set("X-Agent-ID", server.AgentID)
	httpReq.Header.Set("Authorization", "Bearer users-token")
	httpReq.Header.Set(headerUsersAppliedRevision, strconv.FormatInt(revokeReq.UsersRevision, 10))
	httpReq.Header.Set(headerUsersAppliedDigest, revokeReq.UsersDigest)
	httpReq.Header.Set(headerUsersBootID, "boot-2")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httpReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull status %d: %s", rec.Code, rec.Body.String())
	}
	var pulled model.UsersEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &pulled); err != nil {
		t.Fatal(err)
	}
	var pulledReq model.UsersInstallRequest
	_ = json.Unmarshal([]byte(pulled.UsersJSON), &pulledReq)
	pulledFields := security.UsersEnvelopeFields{ServerID: pulled.ServerID, MessageID: pulled.MessageID, Revision: pulledReq.UsersRevision, Digest: pulledReq.UsersDigest, UsersJSON: pulled.UsersJSON}
	if !security.VerifyUsersEnvelope(server.AgentTokenHash, pulledFields, pulled.Signature) || pulledReq.UsersRevision != 2 {
		t.Fatalf("pulled envelope invalid: %+v", pulledReq)
	}
	state, _ = db.RuntimeUserState(ctx, server.ID)
	if !state.Confirmed() || state.ConfirmedBootID != "boot-2" {
		t.Fatalf("pull headers did not confirm: %+v", state)
	}
	anon := httptest.NewRequest(http.MethodGet, "/api/v1/agent/users-snapshot", nil)
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code == http.StatusOK {
		t.Fatal("unauthenticated pull succeeded")
	}
}

func TestRuntimeUsersLaneMarksAgentUpgradeWithoutCaps(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "users-test-secret", "")
	server := &model.Server{Name: "old-node", PublicIPv4: "203.0.113.20", AgentID: "old-agent", AgentTokenHash: security.HashSecret("old-token"), Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "vless", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	controlCh := make(chan any, 4)
	srv.registerAgentLive(server.ID, controlCh)
	defer srv.unregisterAgentLive(server.ID, controlCh)
	srv.reconcileRuntimeUsersSync(ctx, true)
	select {
	case payload := <-controlCh:
		t.Fatalf("incapable agent received users_update: %+v", payload)
	default:
	}
	state, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingReason != store.RuntimeUsersPendingAgentUpgrade {
		t.Fatalf("pending reason = %q", state.PendingReason)
	}
}

func TestSubscriptionDeliveryHidesUnconfirmedNewGrant(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-time.Hour)
	auth := store.AuthorizationState{DesiredRevision: 2, ConfirmedRevision: 1, ConfirmedAt: &old}
	users := store.RuntimeUserState{DesiredRevision: 2, ConfirmedRevision: 1, ConfirmedAt: &old}
	if !subscriptionDeliveryAllowsNode(auth, users, old.Add(-time.Minute)) {
		t.Fatal("existing grant should stay advertised")
	}
	if subscriptionDeliveryAllowsNode(auth, users, now) {
		t.Fatal("new grant advertised before confirmation")
	}
	never := store.AuthorizationState{DesiredRevision: 1}
	if subscriptionDeliveryAllowsNode(never, store.RuntimeUserState{}, now) {
		t.Fatal("never-confirmed server advertised a node")
	}
	confirmed := store.AuthorizationState{DesiredRevision: 1, ConfirmedRevision: 1}
	if !subscriptionDeliveryAllowsNode(confirmed, store.RuntimeUserState{DesiredRevision: 1, ConfirmedRevision: 1}, now) {
		t.Fatal("confirmed delivery hid a node")
	}
}

// A server whose inbounds are all outside the runtime lane has an empty scope,
// and the kernel rejects an install without one. The pull endpoint used to sign
// such a package anyway, so the Agent retried a 400 every 30 seconds forever.
func TestRuntimeUsersPullSkipsEmptyScope(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "users-test-secret", "")
	server := &model.Server{
		Name: "ss-node", PublicIPv4: "203.0.113.21", AgentID: "ss-agent",
		AgentTokenHash: security.HashSecret("ss-token"), Status: model.ServerOnline,
		KernelCapabilities: []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, model.AgentCapabilityRuntimeUsersShadowsocks},
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "ss", Protocol: model.ProtocolSS, Port: 8388, Enabled: true, ConfigJSON: `{"method":"aes-128-gcm"}`}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/users-snapshot", nil)
	req.Header.Set("X-Agent-ID", server.AgentID)
	req.Header.Set("Authorization", "Bearer ss-token")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull status %d: %s", rec.Code, rec.Body.String())
	}
	var envelope model.UsersEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.UsersJSON != "" {
		t.Fatalf("pull returned an installable package for an empty scope: %s", envelope.UsersJSON)
	}
	state, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Confirmed() {
		t.Fatalf("empty scope left the users lane pending: %+v", state)
	}
}
