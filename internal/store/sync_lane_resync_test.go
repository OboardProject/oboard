package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// A node that refuses the revision it already holds can only be moved by a new
// revision, and the content has not changed, so nothing in the normal flow
// produces one. This is the way out.
func TestForceRuntimeUsersResyncAllocatesANewRevisionForTheSameContent(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "users-resync.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "users-resync"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, 10, "content-a", now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUsersConfirmation(ctx, server.ID, 1, "content-a", "boot-1"); err != nil {
		t.Fatal(err)
	}
	// Without the resync the same content answers with the same revision, which
	// is exactly what leaves a refusing node stuck.
	repeat, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, 10, "content-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Changed || repeat.State.DesiredRevision != 1 {
		t.Fatalf("unchanged content allocated a revision: %+v", repeat)
	}

	if err := db.ForceRuntimeUsersResync(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	state, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConfirmedRevision != 0 || state.ConfirmedDigest != "" {
		t.Fatalf("a stale confirmation would let the lane skip the redelivery: %+v", state)
	}
	after, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, 10, "content-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Changed || after.State.DesiredRevision != 2 {
		t.Fatalf("resync did not allocate a new revision: %+v", after)
	}
	if after.State.DesiredDigest != "content-a" {
		t.Fatalf("resync must not change the content: %+v", after.State)
	}
}

// The delivered payload carries lease counters that move between two deliveries
// of one revision, so the node re-applies the revision it already holds. The
// lane has to record that, or its stored identity keeps describing a package the
// node no longer runs.
func TestRuntimeUsersConfirmationRefreshesIdentityAtTheSameRevision(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "users-confirm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "users-confirm"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUsersConfirmation(ctx, server.ID, 4, "content-a", "boot-1"); err != nil {
		t.Fatal(err)
	}
	advanced, err := db.RecordRuntimeUsersConfirmation(ctx, server.ID, 4, "content-b", "boot-2")
	if err != nil {
		t.Fatal(err)
	}
	if !advanced {
		t.Fatal("a re-application at the same revision was dropped")
	}
	state, _ := db.RuntimeUserState(ctx, server.ID)
	if state.ConfirmedDigest != "content-b" || state.ConfirmedBootID != "boot-2" {
		t.Fatalf("identity not refreshed: %+v", state)
	}
	// An identical report is still a no-op, so a steady node does not write on
	// every heartbeat.
	if advanced, err := db.RecordRuntimeUsersConfirmation(ctx, server.ID, 4, "content-b", "boot-2"); err != nil || advanced {
		t.Fatalf("identical confirmation wrote again: advanced=%v err=%v", advanced, err)
	}
	// The watermark still only moves forward.
	if advanced, _ := db.RecordRuntimeUsersConfirmation(ctx, server.ID, 3, "content-c", "boot-2"); advanced {
		t.Fatal("an older revision moved the watermark backwards")
	}
}

// The probe version is answered from the stored digest, so a node that refuses
// the current version is sent it again forever. Rebinding is what produces a
// version it can accept.
func TestRebindLatencyProbePlanVersionIssuesANewVersion(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "probe-rebind.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "probe-rebind"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	first, err := db.LatencyProbePlanVersion(ctx, server.ID, "plan-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := db.LatencyProbePlanVersion(ctx, server.ID, "plan-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if repeat != first {
		t.Fatalf("unchanged plan reissued a version: %d -> %d", first, repeat)
	}

	if err := db.RebindLatencyProbePlanVersion(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	rebound, err := db.LatencyProbePlanVersion(ctx, server.ID, "plan-a", 0)
	if err != nil {
		t.Fatal(err)
	}
	if rebound <= first {
		t.Fatalf("rebind did not move the version forward: %d -> %d", first, rebound)
	}
	bindings, err := db.ListLatencyProbePlanBindings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].ServerID != server.ID || bindings[0].PlanVersion != rebound || bindings[0].PlanDigest != "plan-a" {
		t.Fatalf("binding not readable for comparison: %+v", bindings)
	}
}
