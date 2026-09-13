package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

// TestEnrollmentClearsThePreviousIncarnationsLedger covers a reinstalled node:
// the Agent that confirmed the fast lanes no longer exists, and its watermark
// would otherwise tell the Controller that state is installed on a node that
// has nothing.
func TestEnrollmentClearsThePreviousIncarnationsLedger(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "incarnation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "node", AgentID: "agent-old", AgentTokenHash: security.HashSecret("old-token"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordAuthorizationConfirmation(ctx, server.ID, 7, 2, "auth-digest", "boot-old"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUserConfirmation(ctx, server.ID, 9, "users-digest", "boot-old"); err != nil {
		t.Fatal(err)
	}

	if err := db.SetServerEnrollmentHash(ctx, server.ID, security.HashSecret("enroll-token"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimServerEnrollment(ctx, security.HashSecret("enroll-token"), "agent-new", security.HashSecret("new-token")); err != nil {
		t.Fatal(err)
	}
	auth, err := db.AuthorizationState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if auth.ConfirmedRevision != 0 || auth.ConfirmedBootID != "" || auth.DeliveredRevision != 0 {
		t.Fatalf("the new node inherited its predecessor's authorization ledger: %+v", auth)
	}
	users, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if users.ConfirmedRevision != 0 || users.ConfirmedBootID != "" || users.DeliveredRevision != 0 {
		t.Fatalf("the new node inherited its predecessor's users ledger: %+v", users)
	}
}

// TestAppliedReportFromANewIncarnationReplacesTheWatermark separates the two
// kinds of report: an acknowledgement confirms one delivered message and may
// only move forward, while a node describing its own installed state may move
// the watermark back when it comes from a different incarnation.
func TestAppliedReportFromANewIncarnationReplacesTheWatermark(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "restate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "node", AgentID: "agent", Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUserConfirmation(ctx, server.ID, 9, "digest-9", "boot-a"); err != nil {
		t.Fatal(err)
	}
	// A late acknowledgement from the same incarnation cannot move it back.
	if advanced, err := db.RecordRuntimeUserConfirmation(ctx, server.ID, 4, "digest-4", "boot-a"); err != nil || advanced {
		t.Fatalf("stale acknowledgement moved the watermark: advanced=%v err=%v", advanced, err)
	}
	state, _ := db.RuntimeUserState(ctx, server.ID)
	if state.ConfirmedRevision != 9 {
		t.Fatalf("watermark = %d, want 9", state.ConfirmedRevision)
	}
	// The node that replaced it reports what it really has.
	advanced, err := db.RestateRuntimeUserConfirmation(ctx, server.ID, 4, "digest-4", "boot-b")
	if err != nil || !advanced {
		t.Fatalf("new incarnation report ignored: advanced=%v err=%v", advanced, err)
	}
	state, _ = db.RuntimeUserState(ctx, server.ID)
	if state.ConfirmedRevision != 4 || state.ConfirmedBootID != "boot-b" {
		t.Fatalf("watermark still describes the previous node: %+v", state)
	}

	if _, err := db.RecordAuthorizationConfirmation(ctx, server.ID, 9, 3, "digest-9", "boot-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RestateAuthorizationConfirmation(ctx, server.ID, 4, 1, "digest-4", "boot-b"); err != nil {
		t.Fatal(err)
	}
	auth, _ := db.AuthorizationState(ctx, server.ID)
	if auth.ConfirmedRevision != 4 || auth.ConfirmedBootID != "boot-b" {
		t.Fatalf("authorization watermark still describes the previous node: %+v", auth)
	}

	// An Agent old enough not to report a boot id keeps the strict forward-only
	// behaviour: without an incarnation there is nothing to tell apart, and
	// silently accepting a lower revision would let a late report undo a
	// confirmation.
	if advanced, err := db.RestateRuntimeUserConfirmation(ctx, server.ID, 2, "digest-2", ""); err != nil || advanced {
		t.Fatalf("report without an incarnation moved the watermark: advanced=%v err=%v", advanced, err)
	}
	state, _ = db.RuntimeUserState(ctx, server.ID)
	if state.ConfirmedRevision != 4 {
		t.Fatalf("watermark = %d, want 4", state.ConfirmedRevision)
	}
}
