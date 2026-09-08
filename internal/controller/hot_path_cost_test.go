package controller

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// hotPathFixture is one enrolled server with one authorized user, the shape
// every Agent hot path (traffic report, authorization poll, users snapshot)
// repeats several times a second across the fleet.
func hotPathFixture(t testing.TB) (*store.Store, *Server, *model.Server, *model.Inbound, *model.User) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := newTestServer(db, "hot-path-secret", "")
	server := &model.Server{
		Name: "hot-path-node", PublicIPv4: "203.0.113.31", AgentID: "hot-path-agent",
		AgentTokenHash: security.HashSecret("hot-path-token"), Status: model.ServerOnline,
		KernelCapabilities: []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, model.AgentCapabilityRuntimeUsersVLESS},
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "hot-path-account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111131", ProxyPassword: "password"}
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
	return db, srv, server, inbound, user
}

// Snapshot pulls and the recovery scan both regenerate the whole server
// configuration. Repeating the pull must reuse that work instead of paying for
// it again, while a routing change must still be visible on the next pull.
func TestRuntimeUserPackageIsReusedUntilRoutingChanges(t *testing.T) {
	ctx := context.Background()
	db, srv, server, _, user := hotPathFixture(t)
	first, firstRouting, err := srv.currentRuntimeUserPackage(ctx, *server, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Entries) != 1 {
		t.Fatalf("first package entries = %d, want 1", len(first.Entries))
	}
	before := db.SQLStatementCount()
	repeat, repeatRouting, err := srv.currentRuntimeUserPackage(ctx, *server, 1)
	if err != nil {
		t.Fatal(err)
	}
	reuseCost := db.SQLStatementCount() - before
	if len(repeat.Entries) != len(first.Entries) || repeat.UsersDigest != first.UsersDigest || repeatRouting != firstRouting {
		t.Fatalf("reused package diverged: first=%+v repeat=%+v", first, repeat)
	}
	if reuseCost > 8 {
		t.Fatalf("a reused package still cost %d statements", reuseCost)
	}

	second := &model.Inbound{ServerID: server.ID, Name: "vless-2", Protocol: model.ProtocolVLESS, Port: 444, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, second); err != nil {
		t.Fatal(err)
	}
	// The helper rebinds the user to a fresh single-node plan, so the package
	// must now authorize the new inbound and nothing else.
	grantTestPlanInboundNode(t, db, user.ID, second.ID)
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	rebuilt, rebuiltRouting, err := srv.currentRuntimeUserPackage(ctx, *server, 1)
	if err != nil {
		t.Fatal(err)
	}
	if rebuiltRouting == firstRouting {
		t.Fatal("routing revision did not advance across an inbound grant")
	}
	if len(rebuilt.Entries) != 1 || rebuilt.Entries[0].InboundTag != "in-"+strconv.FormatInt(second.ID, 10) {
		t.Fatalf("routing change was not visible in the package: %+v", rebuilt.Entries)
	}
}

// The audit console polls three overview endpoints that share one computation.
// Within the cache window they must answer from it, and a different reporting
// window must never be served from another window's entry.
func TestAuditOverviewIsSharedPerWindow(t *testing.T) {
	ctx := context.Background()
	db, srv, _, _, _ := hotPathFixture(t)
	if _, _, _, err := srv.auditOverviewData(ctx, 24); err != nil {
		t.Fatal(err)
	}
	before := db.SQLStatementCount()
	for range 3 {
		if _, _, _, err := srv.auditOverviewData(ctx, 24); err != nil {
			t.Fatal(err)
		}
	}
	shared := db.SQLStatementCount() - before
	if shared > 6 {
		t.Fatalf("repeated overviews cost %d statements", shared)
	}
	before = db.SQLStatementCount()
	if _, _, _, err := srv.auditOverviewData(ctx, 72); err != nil {
		t.Fatal(err)
	}
	if db.SQLStatementCount()-before <= shared {
		t.Fatal("a different reporting window was served from the cached one")
	}
}

// The authorization lease is renewed on every traffic report and every
// authorization poll. An unchanged grant set must cost no write transaction.
func TestAuthorizationLeaseRenewalOpensNoWriteTransaction(t *testing.T) {
	ctx := context.Background()
	db, srv, server, _, user := hotPathFixture(t)
	lease, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lease.Grants) != 1 {
		t.Fatalf("first lease grants = %d, want 1", len(lease.Grants))
	}
	before := db.SQLWriteTransactionCount()
	for range 5 {
		renewed, err := srv.currentAuthorizationLease(ctx, server.ID)
		if err != nil {
			t.Fatal(err)
		}
		if renewed.Sequence != lease.Sequence || renewed.Digest != lease.Digest {
			t.Fatalf("renewal re-issued the lease: first=%+v renewed=%+v", lease, renewed)
		}
	}
	if writes := db.SQLWriteTransactionCount() - before; writes != 0 {
		t.Fatalf("renewals opened %d write transactions", writes)
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID}}); err != nil {
		t.Fatal(err)
	}
	revoked, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked.Grants) != 0 || revoked.Revision != lease.Revision+1 {
		t.Fatalf("revocation was hidden by the renewal cache: %+v", revoked)
	}
}

// Concurrent package misses for one server must coalesce into a single build.
func TestRuntimeUserPackageConcurrentMissBuildsOnce(t *testing.T) {
	ctx := context.Background()
	_, srv, server, _, _ := hotPathFixture(t)
	before := srv.runtimeUserPackageBuildCount()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := srv.currentRuntimeUserPackage(ctx, *server, 1)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	builds := srv.runtimeUserPackageBuildCount() - before
	if builds != 1 {
		t.Fatalf("concurrent misses built %d packages, want 1", builds)
	}
}
