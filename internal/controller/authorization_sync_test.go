package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// The fast lane must deliver one signed envelope per transport, confirm the
// desired revision only from an Agent acknowledgement, and re-arm delivery when
// the acknowledgement names an older revision.
func TestAuthorizationFastLaneEnvelopeAndAck(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "authorization-test-secret", "")
	server := &model.Server{Name: "lane-node", PublicIPv4: "203.0.113.9", AgentID: "lane-agent", AgentTokenHash: security.HashSecret("lane-token"), Status: model.ServerOnline, KernelCapabilities: []string{model.AgentCapabilityAuthorizationLease, model.AgentCapabilityAuthorizationControl}}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "socks", Protocol: model.ProtocolSocks, Port: 10443, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}

	// Control-channel delivery: the worker pushes exactly one envelope on the
	// live socket and records it as delivered but unconfirmed.
	controlCh := make(chan any, 4)
	srv.registerAgentLive(server.ID, controlCh)
	defer srv.unregisterAgentLive(server.ID, controlCh)
	srv.reconcileAuthorizationSync(ctx, false)
	var envelope model.AuthorizationEnvelope
	select {
	case payload := <-controlCh:
		var ok bool
		envelope, ok = payload.(model.AuthorizationEnvelope)
		if !ok {
			t.Fatalf("unexpected control payload %T", payload)
		}
	default:
		t.Fatal("worker did not push an authorization_update on the live socket")
	}
	if envelope.Type != model.AgentControlAuthorizationUpdate || envelope.ServerID != server.ID || envelope.MessageID == "" {
		t.Fatalf("envelope shape: %+v", envelope)
	}
	var lease model.AuthorizationLease
	if err := json.Unmarshal([]byte(envelope.LeaseJSON), &lease); err != nil {
		t.Fatal(err)
	}
	fields := security.AuthorizationEnvelopeFields{ServerID: envelope.ServerID, MessageID: envelope.MessageID, Revision: lease.Revision, Sequence: lease.Sequence, IssuedAt: lease.IssuedAt, ExpiresAt: lease.ExpiresAt, LeaseJSON: envelope.LeaseJSON}
	if !security.VerifyAuthorizationEnvelope(server.AgentTokenHash, fields, envelope.Signature) {
		t.Fatal("envelope signature does not verify with the Agent token hash")
	}
	if security.VerifyAuthorizationEnvelope(security.HashSecret("other"), fields, envelope.Signature) {
		t.Fatal("envelope signature verified with a foreign key")
	}
	if len(lease.Grants) != 1 || lease.Revision != 1 {
		t.Fatalf("unexpected lease %+v", lease)
	}
	state, err := db.AuthorizationState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.DeliveredRevision != 1 || state.DeliveredMessageID != envelope.MessageID || state.Confirmed() {
		t.Fatalf("delivery not recorded or confirmed prematurely: %+v", state)
	}
	// A second pass must not push a duplicate while the first is in flight.
	srv.reconcileAuthorizationSync(ctx, false)
	select {
	case payload := <-controlCh:
		t.Fatalf("duplicate push while unconfirmed: %+v", payload)
	default:
	}

	// Acknowledgement confirms the ledger.
	ackRaw, _ := json.Marshal(map[string]any{"type": model.AgentControlAuthorizationAck, "message_id": envelope.MessageID, "revision": lease.Revision, "sequence": lease.Sequence, "digest": lease.Digest, "confirmed": true, "boot_id": "boot-1"})
	var ackMessage map[string]json.RawMessage
	_ = json.Unmarshal(ackRaw, &ackMessage)
	srv.handleAuthorizationAck(ctx, server, ackMessage)
	state, _ = db.AuthorizationState(ctx, server.ID)
	if !state.Confirmed() || state.ConfirmedBootID != "boot-1" || state.ConfirmedDigest != lease.Digest {
		t.Fatalf("ack did not confirm: %+v", state)
	}

	// Revoking advances the semantic revision and pushes again.
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.reconcileProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	srv.reconcileAuthorizationSync(ctx, false)
	var revoke model.AuthorizationEnvelope
	select {
	case payload := <-controlCh:
		revoke = payload.(model.AuthorizationEnvelope)
	default:
		t.Fatal("revoke was not pushed")
	}
	var revokeLease model.AuthorizationLease
	_ = json.Unmarshal([]byte(revoke.LeaseJSON), &revokeLease)
	if revokeLease.Revision != 2 || len(revokeLease.Grants) != 0 || len(revokeLease.Denied) != 1 {
		t.Fatalf("revoke lease: %+v", revokeLease)
	}
	// A stale acknowledgement for revision 1 keeps the ledger unconfirmed.
	srv.recordAuthorizationAck(ctx, server, model.AuthorizationAck{Revision: 1, Sequence: 9, Confirmed: true, BootID: "boot-1"})
	state, _ = db.AuthorizationState(ctx, server.ID)
	if state.Confirmed() {
		t.Fatalf("stale ack confirmed revision 2: %+v", state)
	}

	// HTTP pull returns the same signed envelope and records the applied
	// identity carried in the request headers as a confirmation.
	handler := srv.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/agent/authorization", nil)
	req.Header.Set("X-Agent-ID", server.AgentID)
	req.Header.Set("Authorization", "Bearer lane-token")
	req.Header.Set(headerAuthorizationAppliedRevision, strconv.FormatInt(revokeLease.Revision, 10))
	req.Header.Set(headerAuthorizationAppliedSequence, strconv.FormatInt(revokeLease.Sequence, 10))
	req.Header.Set(headerAuthorizationAppliedDigest, revokeLease.Digest)
	req.Header.Set(headerAuthorizationBootID, "boot-2")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pull status %d: %s", rec.Code, rec.Body.String())
	}
	var pulled model.AuthorizationEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &pulled); err != nil {
		t.Fatal(err)
	}
	var pulledLease model.AuthorizationLease
	_ = json.Unmarshal([]byte(pulled.LeaseJSON), &pulledLease)
	pulledFields := security.AuthorizationEnvelopeFields{ServerID: pulled.ServerID, MessageID: pulled.MessageID, Revision: pulledLease.Revision, Sequence: pulledLease.Sequence, IssuedAt: pulledLease.IssuedAt, ExpiresAt: pulledLease.ExpiresAt, LeaseJSON: pulled.LeaseJSON}
	if !security.VerifyAuthorizationEnvelope(server.AgentTokenHash, pulledFields, pulled.Signature) || pulledLease.Revision != 2 {
		t.Fatalf("pulled envelope invalid: %+v", pulledLease)
	}
	state, _ = db.AuthorizationState(ctx, server.ID)
	if !state.Confirmed() || state.ConfirmedBootID != "boot-2" {
		t.Fatalf("pull headers did not confirm: %+v", state)
	}
	denials, _ := db.ListAuthorizationDenials(ctx, server.ID)
	for _, denial := range denials {
		if denial.ConfirmedAt == nil {
			t.Fatalf("denial not confirmed after revision 2 ack: %+v", denial)
		}
	}
	// Unauthenticated pulls are rejected.
	anon := httptest.NewRequest(http.MethodGet, "/api/v1/agent/authorization", nil)
	anonRec := httptest.NewRecorder()
	handler.ServeHTTP(anonRec, anon)
	if anonRec.Code == http.StatusOK {
		t.Fatal("unauthenticated pull succeeded")
	}
}
