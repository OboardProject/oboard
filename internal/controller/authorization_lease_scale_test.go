package controller

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type authorizationScaleFixture struct {
	db      *store.Store
	srv     *Server
	servers []*model.Server
	users   []*model.User
	// inboundByServer is each server's single inbound.
	inboundByServer map[int64]*model.Inbound
}

// newAuthorizationScaleFixture builds a fleet where every server has one
// inbound and one user of its own. The global credential population therefore
// grows with the fleet while each server's own population stays at one, which
// is exactly the shape that exposes a per-server scan of global data.
func newAuthorizationScaleFixture(t *testing.T, servers int) *authorizationScaleFixture {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "authorization-scale.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := newTestServer(db, "authorization-scale-secret", "")
	fixture := &authorizationScaleFixture{db: db, srv: srv, inboundByServer: map[int64]*model.Inbound{}}
	for i := range servers {
		server := &model.Server{
			Name: "scale-node-" + strconvFormatInt(int64(i)), AgentID: "scale-agent-" + strconvFormatInt(int64(i)),
			AgentTokenHash: security.HashSecret("scale-token"), Status: model.ServerOnline,
			PublicIPv4: "203.0.113.1",
		}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		inbound := &model.Inbound{ServerID: server.ID, Name: "socks", Protocol: model.ProtocolSocks, Port: 10000 + i, Enabled: true, ConfigJSON: "{}"}
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
		user := &model.User{
			Username: "scale-account-" + strconvFormatInt(int64(i)), PasswordHash: "hash", Role: model.RoleViewer,
			Status: "active", ProxyUUID: "uuid-" + strconvFormatInt(int64(i)), ProxyPassword: "password",
		}
		if err := db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
		fixture.servers = append(fixture.servers, server)
		fixture.users = append(fixture.users, user)
		fixture.inboundByServer[server.ID] = inbound
	}
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// TestAuthorizationProjectionIndexesEntriesPerServer proves one server's grant
// computation touches only its own credentials rather than the whole
// population, and that the indexed result still equals the filtered one.
func TestAuthorizationProjectionIndexesEntriesPerServer(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 12)
	projection, err := fixture.srv.authorizationProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.entries) < len(fixture.servers) {
		t.Fatalf("expected at least one entry per server, got %d for %d servers", len(projection.entries), len(fixture.servers))
	}
	now := time.Now().UTC()
	for _, server := range fixture.servers {
		indexes := projection.byServer[server.ID]
		if len(indexes) != 1 {
			t.Fatalf("server %d indexes %d entries, want its own 1 out of %d", server.ID, len(indexes), len(projection.entries))
		}
		// The index must agree with a full filter over every entry.
		expected := make([]string, 0, 1)
		for _, entry := range projection.entries {
			if entry.servers[server.ID] {
				expected = append(expected, entry.key)
			}
		}
		grants := projection.grantsAt(server.ID, now)
		if len(grants) != len(expected) {
			t.Fatalf("server %d: indexed %d grants, filter says %d", server.ID, len(grants), len(expected))
		}
		for i := range grants {
			if grants[i].key != expected[i] {
				t.Fatalf("server %d: grant %d = %s, want %s", server.ID, i, grants[i].key, expected[i])
			}
		}
	}
}

// TestAuthorizationGrantsAreSortedWithoutPerCallSort proves grantsAt still
// returns credential keys in stable sorted order now that the per-call sort is
// gone, since the digest depends on that order.
func TestAuthorizationGrantsAreSortedWithoutPerCallSort(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 4)
	// Give one server several credentials so ordering is observable.
	server := fixture.servers[0]
	for i := range 5 {
		inbound := &model.Inbound{ServerID: server.ID, Name: "extra-socks-" + strconvFormatInt(int64(i)), Protocol: model.ProtocolSocks, Port: 20000 + i, Enabled: true, ConfigJSON: "{}"}
		if err := fixture.db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
		user := &model.User{
			Username: "extra-account-" + strconvFormatInt(int64(i)), PasswordHash: "hash", Role: model.RoleViewer,
			Status: "active", ProxyUUID: "extra-uuid-" + strconvFormatInt(int64(i)), ProxyPassword: "password",
		}
		if err := fixture.db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		grantTestPlanInboundNode(t, fixture.db, user.ID, inbound.ID)
	}
	if err := fixture.srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	projection, err := fixture.srv.authorizationProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	grants := projection.grantsAt(server.ID, time.Now().UTC())
	if len(grants) < 6 {
		t.Fatalf("expected the added credentials in the plan, got %d grants", len(grants))
	}
	for i := 1; i < len(grants); i++ {
		if grants[i-1].key >= grants[i].key {
			t.Fatalf("grants are not sorted at %d: %s >= %s", i, grants[i-1].key, grants[i].key)
		}
	}
}

// TestAuthorizationLeaseHitDoesNoWork proves a renewal inside the reuse window
// computes no grants, no digest and writes nothing.
func TestAuthorizationLeaseHitDoesNoWork(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 6)
	for _, server := range fixture.servers {
		if _, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err != nil {
			t.Fatal(err)
		}
	}
	statements := fixture.db.SQLStatementCount()
	transactions := fixture.db.SQLWriteTransactionCount()
	reused := fixture.srv.hotPath.authorizationLeaseReused.Load()
	for range 20 {
		for _, server := range fixture.servers {
			if _, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	renewals := int64(20 * len(fixture.servers))
	if got := fixture.srv.hotPath.authorizationLeaseReused.Load() - reused; got != renewals {
		t.Fatalf("reused %d of %d renewals", got, renewals)
	}
	if writes := fixture.db.SQLWriteTransactionCount() - transactions; writes != 0 {
		t.Fatalf("reused renewals opened %d write transactions", writes)
	}
	// The only statements a hit may cost are the routing revision reads that
	// decide whether the projection is still current.
	if read := fixture.db.SQLStatementCount() - statements; read > renewals {
		t.Fatalf("reused renewals issued %d statements for %d renewals", read, renewals)
	}
}

// TestAuthorizationLeaseReuseDoesNotRewriteValidity proves reuse never extends
// a lease: a reused lease is returned exactly as it was issued.
func TestAuthorizationLeaseReuseDoesNotRewriteValidity(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 1)
	server := fixture.servers[0]
	first, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reused.IssuedAt != first.IssuedAt || reused.ExpiresAt != first.ExpiresAt {
		t.Fatalf("reuse rewrote validity: %s/%s -> %s/%s", first.IssuedAt, first.ExpiresAt, reused.IssuedAt, reused.ExpiresAt)
	}
	if reused.Sequence != first.Sequence || reused.Revision != first.Revision {
		t.Fatalf("reuse allocated a new sequence: %+v -> %+v", first, reused)
	}
	issued, err := time.Parse(time.RFC3339Nano, first.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339Nano, first.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if expires.Sub(issued) > authorizationLeaseDuration {
		t.Fatalf("lease lifetime %s exceeds the five minute maximum", expires.Sub(issued))
	}
}

// TestAuthorizationLeaseRevokeIsNotHiddenByTheCache proves a revoke that lands
// while a lease is inside its reuse window is still reflected immediately.
func TestAuthorizationLeaseRevokeIsNotHiddenByTheCache(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 3)
	server := fixture.servers[0]
	before, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Grants) == 0 {
		t.Fatal("expected a granted credential before the revoke")
	}
	// A reuse hit right now, well inside the window.
	if reused, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err != nil {
		t.Fatal(err)
	} else if reused.Sequence != before.Sequence {
		t.Fatalf("expected reuse, got sequence %d after %d", reused.Sequence, before.Sequence)
	}
	if err := fixture.db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: fixture.users[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Grants) != 0 {
		t.Fatalf("revoke was hidden by the reuse window: %+v", after)
	}
	if after.Revision <= before.Revision {
		t.Fatalf("revoke did not advance the desired revision: %d -> %d", before.Revision, after.Revision)
	}
	if len(after.Denied) == 0 {
		t.Fatalf("revoke did not carry an unconfirmed denial: %+v", after)
	}
}

// TestAuthorizationLeaseReuseEndsAtBusinessBoundary proves the reuse window is
// cut short by a scheduled boundary, including the start of a grant the server
// is not yet authorized for.
func TestAuthorizationLeaseReuseEndsAtBusinessBoundary(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 1)
	server := fixture.servers[0]
	projection, err := fixture.srv.authorizationProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	state := fixture.srv.serverAuthorizationLeaseState(server.ID)
	now := time.Now().UTC()

	state.mu.Lock()
	state.nextBoundary = now.Add(2 * time.Second)
	reusableBefore := state.reusable(projection, now)
	reusableAfter := state.reusable(projection, now.Add(3*time.Second))
	state.mu.Unlock()
	if !reusableBefore {
		t.Fatal("lease should be reusable before its next boundary")
	}
	if reusableAfter {
		t.Fatal("lease must not be reused across a business boundary")
	}
}

// TestAuthorizationLeaseRejectsBackwardClock proves a Controller clock that
// steps backwards never turns into an extended reuse window.
func TestAuthorizationLeaseRejectsBackwardClock(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 1)
	server := fixture.servers[0]
	projection, err := fixture.srv.authorizationProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	state := fixture.srv.serverAuthorizationLeaseState(server.ID)
	state.mu.Lock()
	issued := state.issuedAt
	backwards := state.reusable(projection, issued.Add(-time.Minute))
	state.mu.Unlock()
	if backwards {
		t.Fatal("a lease issued in the future must not be reused")
	}
}

// TestAuthorizationLeaseInvalidatedOnAgentIdentityChange proves a re-enrolled or
// reconnecting Agent is answered with a freshly issued lease rather than the one
// its predecessor already holds.
func TestAuthorizationLeaseInvalidatedOnAgentIdentityChange(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 2)
	server := fixture.servers[0]
	first, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.srv.invalidateAuthorizationLease(server.ID)
	reissued, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reissued.Sequence <= first.Sequence {
		t.Fatalf("agent identity change did not reissue: %d -> %d", first.Sequence, reissued.Sequence)
	}
	if reissued.Digest != first.Digest {
		t.Fatalf("an unchanged grant set changed digest: %s -> %s", first.Digest, reissued.Digest)
	}

	// Another server's cached lease is untouched by that invalidation.
	other := fixture.servers[1]
	otherFirst, err := fixture.srv.currentAuthorizationLease(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixture.srv.invalidateAuthorizationLease(server.ID)
	otherReused, err := fixture.srv.currentAuthorizationLease(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherReused.Sequence != otherFirst.Sequence {
		t.Fatalf("invalidating one server reissued another: %d -> %d", otherFirst.Sequence, otherReused.Sequence)
	}

	// A deleted server leaves no lease state or lock behind.
	fixture.srv.forgetAuthorizationLease(server.ID)
	fixture.srv.authorizationLeases.mu.Lock()
	_, present := fixture.srv.authorizationLeases.servers[server.ID]
	fixture.srv.authorizationLeases.mu.Unlock()
	if present {
		t.Fatal("a forgotten server left authorization lease state behind")
	}
}

// TestAuthorizationLeaseConcurrentRenewalIssuesOnce proves simultaneous
// renewals for one server produce exactly one issuance, and that renewals for
// different servers are not serialized behind a single fleet-wide lock.
func TestAuthorizationLeaseConcurrentRenewalIssuesOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 4)
	// Warm the projection so the measured window is issuance only.
	if _, err := fixture.srv.authorizationProjection(ctx); err != nil {
		t.Fatal(err)
	}
	server := fixture.servers[0]
	issuedBefore := fixture.srv.hotPath.authorizationLeaseIssued.Load()

	const callers = 16
	leases := make([]*model.AuthorizationLease, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			lease, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
			if err != nil {
				t.Error(err)
				return
			}
			leases[index] = lease
		}(i)
	}
	wg.Wait()
	if issued := fixture.srv.hotPath.authorizationLeaseIssued.Load() - issuedBefore; issued != 1 {
		t.Fatalf("%d concurrent renewals produced %d issuances, want 1", callers, issued)
	}
	for i, lease := range leases {
		if lease == nil {
			t.Fatalf("caller %d got no lease", i)
		}
		if lease.Sequence != leases[0].Sequence || lease.Digest != leases[0].Digest {
			t.Fatalf("caller %d saw a different lease: %+v vs %+v", i, lease, leases[0])
		}
	}

	// Each server holds its own lock: a lease held for one does not block another.
	held := fixture.srv.serverAuthorizationLeaseState(server.ID)
	held.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, other := range fixture.servers[1:] {
			if _, err := fixture.srv.currentAuthorizationLease(ctx, other.ID); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		held.mu.Unlock()
		t.Fatal("renewals for other servers blocked behind one server's issuance lock")
	}
	held.mu.Unlock()
}

// TestAuthorizationProjectionBuildsOnceUnderConcurrency proves a cold cache
// with many simultaneous readers performs one shared build, and that a caller
// giving up does not abort it for the others.
func TestAuthorizationProjectionBuildsOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 8)
	fixture.srv.invalidateAuthorizationProjection()
	built := fixture.srv.hotPath.authorizationProjectionBuilt.Load()

	const callers = 24
	results := make([]*authorizationProjection, callers)
	var wg sync.WaitGroup
	// One caller cancels immediately; the shared build must survive it.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = fixture.srv.authorizationProjection(cancelled)
	}()
	for i := range callers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			projection, err := fixture.srv.authorizationProjection(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			results[index] = projection
		}(i)
	}
	wg.Wait()
	if builds := fixture.srv.hotPath.authorizationProjectionBuilt.Load() - built; builds != 1 {
		t.Fatalf("%d concurrent readers performed %d builds, want 1", callers, builds)
	}
	for i, projection := range results {
		if projection == nil {
			t.Fatalf("caller %d got no projection", i)
		}
		if projection != results[0] {
			t.Fatalf("caller %d got a different projection instance", i)
		}
	}
}

// TestAuthorizationProjectionDiscardsBuildRetiredMidFlight proves a build that
// finishes after an invalidation is discarded rather than resurrecting the
// state that invalidation retired.
func TestAuthorizationProjectionDiscardsBuildRetiredMidFlight(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 2)
	if _, err := fixture.srv.authorizationProjection(ctx); err != nil {
		t.Fatal(err)
	}
	fixture.srv.authorizationProjections.mu.Lock()
	generation := fixture.srv.authorizationProjections.generation
	fixture.srv.authorizationProjections.mu.Unlock()

	revision, err := fixture.db.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Retire the round the build below believes it belongs to.
	fixture.srv.invalidateAuthorizationProjection()
	discarded := fixture.srv.hotPath.authorizationProjectionDiscarded.Load()
	projection, err := fixture.srv.buildSharedAuthorizationProjection(ctx, revision, generation)
	if err != nil {
		t.Fatal(err)
	}
	if projection != nil {
		t.Fatal("a build retired mid-flight was published")
	}
	if got := fixture.srv.hotPath.authorizationProjectionDiscarded.Load() - discarded; got != 1 {
		t.Fatalf("discarded %d retired builds, want 1", got)
	}
	fixture.srv.authorizationProjections.mu.Lock()
	current := fixture.srv.authorizationProjections.current
	fixture.srv.authorizationProjections.mu.Unlock()
	if current != nil {
		t.Fatal("the retired build resurrected the invalidated projection")
	}
}

// TestAuthorizationLeaseFailureDoesNotAdvanceTheCache proves a failed issuance
// leaves the recorded lease untouched, so the next attempt retries instead of
// remembering a lease that was never produced.
func TestAuthorizationLeaseFailureDoesNotAdvanceTheCache(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 1)
	server := fixture.servers[0]
	issued, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := fixture.srv.serverAuthorizationLeaseState(server.ID)
	state.mu.Lock()
	state.issuedAt = time.Now().UTC().Add(-time.Minute)
	aged := state.issuedAt
	state.mu.Unlock()

	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err == nil {
		t.Fatal("expected the issuance to fail once the store is unavailable")
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.lease == nil || state.lease.Sequence != issued.Sequence || !state.issuedAt.Equal(aged) {
		t.Fatalf("a failed issuance rewrote the recorded lease: %+v at %s", state.lease, state.issuedAt)
	}
}

// TestAuthorizationDenialSurvivesReissue proves an unconfirmed denial is carried
// by every lease until the Agent acknowledges it, including across the reuse
// window boundary.
func TestAuthorizationDenialSurvivesReissue(t *testing.T) {
	ctx := context.Background()
	fixture := newAuthorizationScaleFixture(t, 1)
	server := fixture.servers[0]
	if _, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: fixture.users[0].ID}}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	revoked, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(revoked.Denied) == 0 {
		t.Fatalf("revoke produced no denial: %+v", revoked)
	}
	// Age past the reuse window: the reissued lease must still deny.
	state := fixture.srv.serverAuthorizationLeaseState(server.ID)
	state.mu.Lock()
	state.issuedAt = time.Now().UTC().Add(-time.Minute)
	state.mu.Unlock()
	reissued, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reissued.Sequence <= revoked.Sequence {
		t.Fatalf("expected a reissue after the window, sequence %d -> %d", revoked.Sequence, reissued.Sequence)
	}
	if len(reissued.Denied) != len(revoked.Denied) {
		t.Fatalf("unconfirmed denial was dropped on reissue: %v -> %v", revoked.Denied, reissued.Denied)
	}
}
