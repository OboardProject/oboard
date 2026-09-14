package controller

import (
	"context"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// A node that holds the desired revision with different content is a state
// neither side can leave: the node refuses the revision it already has, and the
// Controller re-derives that same revision because the content did not change.
// The lane meanwhile reports a confirmed watermark for a payload the node never
// installed, so nothing retries and the node's authorization stops being
// renewed. Allocating a new revision for the same content is the way out.
func TestDivergedUsersRevisionIsRepairedByAllocatingANewOne(t *testing.T) {
	ctx := context.Background()
	db, srv, server, _, _ := hotPathFixture(t)

	pkg, routingRevision, err := srv.currentRuntimeUserPackage(ctx, *server, 1)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest, err := core.UsersContentDigest(pkg.Scope, pkg.Entries)
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, routingRevision, contentDigest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.State.DesiredRevision <= 0 {
		t.Fatalf("no desired revision to diverge from: %+v", first)
	}

	// The node reports it runs that revision with different content.
	srv.recordUsersApplied(ctx, server, &model.UsersAppliedSnapshot{
		Revision: first.State.DesiredRevision, ContentDigest: "divergent-content", BootID: "boot-1",
	})
	state, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConfirmedRevision != 0 || state.DesiredDigest != "" {
		t.Fatalf("divergence did not release the binding: %+v", state)
	}

	// The same content now answers with a higher revision, which is the one
	// thing the node's gate always accepts.
	repaired, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, routingRevision, contentDigest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !repaired.Changed || repaired.State.DesiredRevision != first.State.DesiredRevision+1 {
		t.Fatalf("repair did not allocate a new revision: %+v", repaired)
	}
	if repaired.State.DesiredDigest != contentDigest {
		t.Fatalf("repair changed the content it delivers: %+v", repaired.State)
	}

	// A repair that does not settle must not allocate a revision per heartbeat.
	srv.recordUsersApplied(ctx, server, &model.UsersAppliedSnapshot{
		Revision: repaired.State.DesiredRevision, ContentDigest: "divergent-content", BootID: "boot-1",
	})
	again, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, routingRevision, contentDigest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if again.State.DesiredRevision != repaired.State.DesiredRevision {
		t.Fatalf("a second divergence inside the interval allocated again: %+v", again)
	}
}

// A node that agrees with the binding, and one that reports no content identity
// at all, must both be left alone.
func TestConvergedUsersReportDoesNotAllocateARevision(t *testing.T) {
	ctx := context.Background()
	db, srv, server, _, _ := hotPathFixture(t)

	pkg, routingRevision, err := srv.currentRuntimeUserPackage(ctx, *server, 1)
	if err != nil {
		t.Fatal(err)
	}
	contentDigest, err := core.UsersContentDigest(pkg.Scope, pkg.Entries)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, routingRevision, contentDigest, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for name, applied := range map[string]*model.UsersAppliedSnapshot{
		"converged":   {Revision: desired.State.DesiredRevision, ContentDigest: contentDigest, BootID: "boot-1"},
		"no identity": {Revision: desired.State.DesiredRevision, Digest: "snapshot", BootID: "boot-1"},
	} {
		srv.recordUsersApplied(ctx, server, applied)
		state, err := db.RuntimeUserState(ctx, server.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.DesiredRevision != desired.State.DesiredRevision || state.DesiredDigest != contentDigest {
			t.Fatalf("%s report released the binding: %+v", name, state)
		}
	}
}

// A refusal that does reach the Controller as an acknowledgement is recorded as
// its own state. Retrying cannot resolve it, so it must not sit in a retryable
// runtime failure that the lane keeps pushing at.
func TestUsersRevisionConflictAckIsNotRetryable(t *testing.T) {
	ctx := context.Background()
	db, srv, server, _, _ := hotPathFixture(t)
	srv.recordUsersAck(ctx, server, model.UsersAck{
		Revision: 4,
		Digest:   "snapshot",
		Error:    "users revision 4 already applied with a different content digest",
	})
	state, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingReason != store.RuntimeUsersPendingRevisionConflict || state.Retryable {
		t.Fatalf("conflict recorded as an ordinary runtime failure: %+v", state)
	}
}
