package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAuthorizationDesiredRevisionIsSemantic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "auth-ledger"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 10, "digest-a", []string{"k1", "k2"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.State.DesiredRevision != 1 || len(first.DeniedKeys) != 0 {
		t.Fatalf("first evaluation: %+v", first)
	}
	sequence, err := db.IssueAuthorizationSequence(ctx, server.ID, 1, now.Add(5*time.Minute))
	if err != nil || sequence != 1 {
		t.Fatalf("sequence=%d err=%v", sequence, err)
	}
	// Unrelated routing writes re-evaluate with the same digest: no new revision.
	same, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 11, "digest-a", []string{"k2", "k1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if same.Changed || same.State.DesiredRevision != 1 || same.State.EvaluatedRoutingRevision != 11 {
		t.Fatalf("unchanged grant set advanced the revision: %+v", same)
	}
	// Removing k2 is a revoke: revision 2 and a denial bounded by the issued lease.
	revoke, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 12, "digest-b", []string{"k1"}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !revoke.Changed || revoke.State.DesiredRevision != 2 || len(revoke.DeniedKeys) != 1 || revoke.DeniedKeys[0] != "k2" {
		t.Fatalf("revoke evaluation: %+v", revoke)
	}
	denials, err := db.PendingAuthorizationDenials(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(denials) != 1 || denials[0].DenyRevision != 2 || denials[0].LeaseBoundUntil.Before(now.Add(5*time.Minute).Add(-time.Second)) {
		t.Fatalf("denial not bound to issued lease: %+v", denials)
	}
	// A stale acknowledgement of revision 1 must not confirm revision 2.
	advanced, err := db.RecordAuthorizationConfirmation(ctx, server.ID, 1, 1, "digest-a", "boot-1")
	if err != nil || !advanced {
		t.Fatalf("first confirmation advanced=%v err=%v", advanced, err)
	}
	state, err := db.AuthorizationState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Confirmed() {
		t.Fatalf("revision 1 acknowledgement confirmed revision 2: %+v", state)
	}
	if _, err := db.RecordAuthorizationConfirmation(ctx, server.ID, 2, 1, "digest-b", "boot-1"); err != nil {
		t.Fatal(err)
	}
	state, _ = db.AuthorizationState(ctx, server.ID)
	if !state.Confirmed() || state.PendingReason != "" {
		t.Fatalf("revision 2 not confirmed: %+v", state)
	}
	pending, _ := db.PendingAuthorizationDenials(ctx, server.ID)
	if len(pending) != 0 {
		t.Fatalf("confirmed denial still pending: %+v", pending)
	}
	// Late, lower acknowledgement is ignored.
	advanced, err = db.RecordAuthorizationConfirmation(ctx, server.ID, 1, 5, "digest-a", "boot-0")
	if err != nil || advanced {
		t.Fatalf("stale ack moved the ledger: advanced=%v err=%v", advanced, err)
	}
	if n, err := db.PruneAuthorizationDenials(ctx, now.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("denial pruned before its lease bound passed: n=%d err=%v", n, err)
	}
	if n, err := db.PruneAuthorizationDenials(ctx, now.Add(6*time.Minute)); err != nil || n != 1 {
		t.Fatalf("expired confirmed denial not pruned: n=%d err=%v", n, err)
	}
	stale, err := db.StaleAuthorizationServerIDs(ctx, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("confirmed server listed as stale (server has no agent): %v", stale)
	}
}
