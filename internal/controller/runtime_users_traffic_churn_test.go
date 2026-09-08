package controller

import (
	"context"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// Traffic accounting must not advance the users lane. The delivered payload
// carries the current quota numbers, but the change gate ignores them: the
// traffic-report response and apply_traffic_policy are the lane that renews a
// lease, and hashing those counters here turned every accepted report into a
// users redelivery, an unconfirmed acknowledgement, and a fleet-wide rebuild.
func TestUsersContentDigestIgnoresLeaseAccounting(t *testing.T) {
	scope := []string{"vless-in"}
	base := []model.UsersInstallEntry{{
		InboundTag: "vless-in", AuthUser: "u1", AuthorizationKey: "key-1",
		Identity: model.UsersIdentity{UserID: 1, InboundID: 2},
		Policy: model.UsersRuntimePolicy{
			AuthorizationKey: "key-1", UserID: 1, InboundID: 2, Billable: true,
			TrafficLimitBytes: 1 << 30, UsedBaselineBytes: 100, LeaseBytes: 900, ResetLeaseBytes: 1000,
			LeaseEnforced: true, PeriodKey: "2026-09", QuotaState: "active",
		},
	}}
	accounted := []model.UsersInstallEntry{base[0]}
	accounted[0].Policy.UsedBaselineBytes = 5 << 20
	accounted[0].Policy.LeaseBytes = 512
	accounted[0].Policy.ResetLeaseBytes = 4096

	before, err := core.UsersContentDigest(scope, base)
	if err != nil {
		t.Fatal(err)
	}
	after, err := core.UsersContentDigest(scope, accounted)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("lease accounting changed the users-lane gate: %s -> %s", before, after)
	}

	// The full snapshot digest, which is what the Agent verifies, still covers
	// those fields.
	fullBefore, err := core.UsersDigest(7, scope, base)
	if err != nil {
		t.Fatal(err)
	}
	fullAfter, err := core.UsersDigest(7, scope, accounted)
	if err != nil {
		t.Fatal(err)
	}
	if fullBefore == fullAfter {
		t.Fatal("the delivered users digest must still cover the quota numbers it carries")
	}

	// Everything that is not lease accounting must still move the gate.
	for name, mutate := range map[string]func(*model.UsersInstallEntry){
		"credential":   func(e *model.UsersInstallEntry) { e.Credential.UUID = "changed" },
		"route":        func(e *model.UsersInstallEntry) { e.RouteOutbound = "path-9-step-1" },
		"speed limit":  func(e *model.UsersInstallEntry) { e.Policy.SpeedLimitMbps = 50 },
		"quota limit":  func(e *model.UsersInstallEntry) { e.Policy.TrafficLimitBytes = 2 << 30 },
		"quota state":  func(e *model.UsersInstallEntry) { e.Policy.QuotaState = "quota_exceeded" },
		"period":       func(e *model.UsersInstallEntry) { e.Policy.PeriodKey = "2026-10" },
		"credential ep": func(e *model.UsersInstallEntry) { e.Identity.CredentialEpoch = 3 },
	} {
		changed := []model.UsersInstallEntry{base[0]}
		mutate(&changed[0])
		digest, err := core.UsersContentDigest(scope, changed)
		if err != nil {
			t.Fatal(err)
		}
		if digest == before {
			t.Fatalf("%s must advance the users-lane gate", name)
		}
	}
}

// A traffic report advances traffic_policy_revision. That must not make the
// next users sync decide the server needs a new desired revision.
func TestTrafficReportDoesNotAdvanceUsersDesiredRevision(t *testing.T) {
	ctx := context.Background()
	db, srv, server, _, user := hotPathFixture(t)

	pkg, routingRevision, err := srv.currentRuntimeUserPackage(ctx, *server, 1)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := core.UsersContentDigest(pkg.Scope, pkg.Entries)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, routingRevision, digest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.BumpTrafficPolicyRevision(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnsureTrafficPeriod(ctx, user.ID, "2026-09", time.Now().UTC(), time.Now().UTC().Add(24*time.Hour), 1<<30); err != nil {
		t.Fatal(err)
	}
	srv.bumpRuntimeUserPackageGeneration()

	pkg, routingRevision, err = srv.currentRuntimeUserPackage(ctx, *server, first.State.DesiredRevision)
	if err != nil {
		t.Fatal(err)
	}
	digest, err = core.UsersContentDigest(pkg.Scope, pkg.Entries)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, routingRevision, digest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed || second.State.DesiredRevision != first.State.DesiredRevision {
		t.Fatalf("traffic accounting advanced the users desired revision: %d -> %d (changed=%v)",
			first.State.DesiredRevision, second.State.DesiredRevision, second.Changed)
	}
}
