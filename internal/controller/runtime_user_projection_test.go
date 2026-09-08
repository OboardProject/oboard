package controller

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRuntimeUserPackageSurvivesMissingDNSPolicy(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := newTestServer(db, "users-dns-secret", "")
	server := &model.Server{
		Name: "users-dns-node", PublicIPv4: "203.0.113.91", AgentID: "users-dns-agent",
		AgentTokenHash: security.HashSecret("users-dns-token"), Status: model.ServerOnline,
		KernelCapabilities: []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, model.AgentCapabilityRuntimeUsersVLESS},
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "users-dns-account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111191", ProxyPassword: "password"}
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
	if err := db.Delete(ctx, "server_dns_policies", server.ID); err != nil {
		t.Fatal(err)
	}
	srv.invalidateRoutingSnapshot()
	pkg, _, err := srv.currentRuntimeUserPackage(ctx, *server, 1)
	if err != nil {
		t.Fatalf("missing DNS policy must not block user projection: %v", err)
	}
	if len(pkg.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(pkg.Entries))
	}
}

func TestRuntimeUserPackageMatchesFullConfigProjection(t *testing.T) {
	ctx := context.Background()
	db, srv, server, _, _ := hotPathFixture(t)
	projected, err := srv.buildRuntimeUserPackage(ctx, *server, 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := db.FullRoutingConfigData(ctx)
	if err != nil {
		t.Fatal(err)
	}
	generated, err := srv.generateServerCoreConfigWithLedger(ctx, *server, data, nil)
	if err != nil {
		t.Fatal(err)
	}
	if generated.RuntimeUsers == nil {
		t.Fatal("full config produced no runtime users")
	}
	full := *generated.RuntimeUsers
	if len(projected.Entries) != len(full.Entries) {
		t.Fatalf("entry count projected=%d full=%d", len(projected.Entries), len(full.Entries))
	}
	if len(projected.Scope) != len(full.Scope) {
		t.Fatalf("scope projected=%v full=%v", projected.Scope, full.Scope)
	}
	for i := range projected.Entries {
		if projected.Entries[i].AuthUser != full.Entries[i].AuthUser ||
			projected.Entries[i].InboundTag != full.Entries[i].InboundTag ||
			projected.Entries[i].AuthorizationKey != full.Entries[i].AuthorizationKey {
			t.Fatalf("entry %d diverged: projected=%+v full=%+v", i, projected.Entries[i], full.Entries[i])
		}
	}
}
