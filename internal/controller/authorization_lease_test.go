package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAuthorizationLeaseRevokesWithoutRenderingOrCredentialAllocation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "authorization-test-secret", "")
	server := &model.Server{Name: "lease-node", PublicIPv4: "203.0.113.1"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "old-uuid", ProxyPassword: "old-password"}
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
	users, err := db.LoadProxyCredentials(ctx, srv.sessionSecret, []model.User{*user})
	if err != nil {
		t.Fatal(err)
	}
	credential := core.UserCredentialForRoute(users[0], inbound.ID, 0, inbound.Protocol)
	if credential.AuthorizationKey == "" || credential.ProxyUsername == user.Username || credential.ProxyPassword == user.ProxyPassword {
		t.Fatal("random persisted credential missing")
	}
	before, _ := db.ConfigurationRevision(ctx)
	lease, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.Grants) != 1 || lease.Grants[credential.AuthorizationKey] == "" {
		t.Fatalf("grant missing: %+v", lease)
	}
	issued, _ := time.Parse(time.RFC3339Nano, lease.IssuedAt)
	end, _ := time.Parse(time.RFC3339Nano, lease.Grants[credential.AuthorizationKey])
	if end.Sub(issued) > 5*time.Minute {
		t.Fatal("unbounded lease")
	}
	after, _ := db.ConfigurationRevision(ctx)
	if before != after {
		t.Fatal("renewal mutated configuration")
	}
	if lease.Revision != 1 || lease.Sequence != 1 || lease.Digest == "" || lease.ExpiresAt == "" {
		t.Fatalf("first lease is not revision 1 / sequence 1 with digest and expiry: %+v", lease)
	}
	renewed, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	// An unchanged grant set is answered from the issued lease, so a fleet
	// renewing on every traffic report and authorization poll allocates no new
	// sequence and writes no ledger row.
	if renewed.Revision != lease.Revision || renewed.Sequence != lease.Sequence || renewed.Digest != lease.Digest || renewed.IssuedAt != lease.IssuedAt {
		t.Fatalf("renewal changed the semantic revision: first=%+v renewed=%+v", lease, renewed)
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID}}); err != nil {
		t.Fatal(err)
	}
	revoked, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked.Grants) != 0 || revoked.Revision != lease.Revision+1 {
		t.Fatalf("revocation did not advance authorization by exactly one: %+v", revoked)
	}
	if len(revoked.Denied) != 1 || revoked.Denied[0] != credential.AuthorizationKey {
		t.Fatalf("revoked key missing from deny watermark: %+v", revoked.Denied)
	}
	state, err := db.AuthorizationState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.DesiredRevision != revoked.Revision || state.Confirmed() {
		t.Fatalf("ledger not tracking the unconfirmed revoke: %+v", state)
	}
	if err := srv.reconcileProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListProxyCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != "revoked" {
		t.Fatal("credential tombstone missing")
	}
}

// Removing one of two plan bindings that both grant the same inbound must not
// drop the grant: revocation follows the effective-authorization difference,
// never the deletion of a single binding row.
func TestAuthorizationLeaseKeepsGrantWhileAnotherSourceStillAuthorizes(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "authorization-test-secret", "")
	server := &model.Server{Name: "overlap-node", PublicIPv4: "203.0.113.2"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "overlap", PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "socks", Protocol: model.ProtocolSocks, Port: 10444, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	planA := &model.SubscriptionPlan{Name: "plan-a", Enabled: true}
	planB := &model.SubscriptionPlan{Name: "plan-b", Enabled: true}
	for _, plan := range []*model.SubscriptionPlan{planA, planB} {
		if err := db.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: inbound.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: planA.ID}, {UserID: user.ID, PlanID: planB.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	lease, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.Grants) != 1 {
		t.Fatalf("expected one grant, got %+v", lease.Grants)
	}
	var key string
	for key = range lease.Grants {
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: planA.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := srv.reconcileProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Grants[key] == "" {
		t.Fatalf("overlapping authorization was revoked: %+v", after.Grants)
	}
	rows, err := db.ListProxyCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == key && row.Status != "active" {
			t.Fatalf("credential %s was tombstoned while still authorized", key)
		}
	}
}

func TestAuthorizationLeaseClampsPlanExpiry(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(40 * time.Second)
	user := model.User{ID: 1, Status: "active", LegacyProxyEnabled: true}
	data := store.FullRoutingConfig{Users: []model.User{user}, UserDevices: []model.UserDevice{}, Servers: []model.Server{{ID: 1}}, Inbounds: []model.Inbound{{ID: 2, ServerID: 1, Protocol: model.ProtocolSocks, Enabled: true}}, SubscriptionPlans: []model.SubscriptionPlan{{ID: 3, Enabled: true}}, PlanBindings: []model.UserPlanBinding{{UserID: 1, PlanID: 3, Enabled: true, ExpiresAt: &expiry}}, ActivePlanNodes: []model.SubscriptionPlanNode{{PlanID: 3, NodeType: model.AssignableNodeInbound, NodeID: 2, Enabled: true}}}
	credential := model.ProxyCredential{ID: "key", UserID: 1, InboundID: 2, Protocol: model.ProtocolSocks, Status: "active"}
	lease := buildAuthorizationLease(1, now, 1, data, []model.ProxyCredential{credential})
	if lease.Grants["key"] != expiry.Format(time.RFC3339Nano) {
		t.Fatalf("deadline=%q", lease.Grants["key"])
	}
	later := buildAuthorizationLease(1, expiry, 1, data, []model.ProxyCredential{credential})
	if len(later.Grants) != 0 {
		t.Fatal("expired binding renewed")
	}
}
