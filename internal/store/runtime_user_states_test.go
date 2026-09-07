package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestRuntimeUserDesiredRevisionIsSemantic(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "runtime-users.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "users-ledger"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, 10, "digest-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed || first.State.DesiredRevision != 1 {
		t.Fatalf("first evaluation: %+v", first)
	}
	same, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, 11, "digest-a", now)
	if err != nil {
		t.Fatal(err)
	}
	if same.Changed || same.State.DesiredRevision != 1 || same.State.EvaluatedRoutingRevision != 11 {
		t.Fatalf("unchanged package advanced the revision: %+v", same)
	}
	next, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, 12, "digest-b", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !next.Changed || next.State.DesiredRevision != 2 {
		t.Fatalf("content change: %+v", next)
	}
	advanced, err := db.RecordRuntimeUsersConfirmation(ctx, server.ID, 1, "digest-a", "boot-1")
	if err != nil || !advanced {
		t.Fatalf("stale confirmation advanced=%v err=%v", advanced, err)
	}
	state, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Confirmed() {
		t.Fatalf("revision 1 acknowledgement confirmed revision 2: %+v", state)
	}
	if _, err := db.RecordRuntimeUsersConfirmation(ctx, server.ID, 2, "digest-b", "boot-1"); err != nil {
		t.Fatal(err)
	}
	state, _ = db.RuntimeUserState(ctx, server.ID)
	if !state.Confirmed() || state.PendingReason != "" {
		t.Fatalf("revision 2 not confirmed: %+v", state)
	}
}

func TestAuthorizationAndRuntimeUserTablesMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous-delivery.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "legacy-delivery", AgentID: "agent-1"}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"drop table if exists authorization_denials",
		"drop table if exists authorization_states",
		"drop table if exists runtime_user_states",
	} {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	auth, err := s.EvaluateAuthorizationDesired(ctx, server.ID, 1, "auth-a", []string{"k1"}, now)
	if err != nil || !auth.Changed {
		t.Fatalf("authorization evaluate after reopen: %+v err=%v", auth, err)
	}
	users, err := s.EvaluateRuntimeUsersDesired(ctx, server.ID, 1, "users-a", now)
	if err != nil || !users.Changed {
		t.Fatalf("runtime users evaluate after reopen: %+v err=%v", users, err)
	}
	listed, err := s.ListRuntimeUserStates(ctx)
	if err != nil || len(listed) != 1 || listed[0].ServerID != server.ID {
		t.Fatalf("list after migration: %+v err=%v", listed, err)
	}
}
