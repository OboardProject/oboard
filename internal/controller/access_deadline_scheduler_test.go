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

func TestAccessServersFromProjectionsBeforeAfterUnion(t *testing.T) {
	before := core.AccessProjection{
		InboundUsers: map[int64][]int64{1: {9}},
	}
	after := core.AccessProjection{
		InboundUsers: map[int64][]int64{2: {9}},
	}
	data := store.FullRoutingConfig{
		Servers:  []model.Server{{ID: 10, Status: model.ServerOnline}, {ID: 20, Status: model.ServerOnline}},
		Inbounds: []model.Inbound{{ID: 1, ServerID: 10, Enabled: true}, {ID: 2, ServerID: 20, Enabled: true}},
	}
	got := accessServersFromProjections(before, after, data)
	if len(got) != 2 || got[0] != 10 || got[1] != 20 {
		t.Fatalf("servers=%v want [10 20]", got)
	}
}

func TestFireAccessDeadlinesMarksEvaluatedStale(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "deadline-fire.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := New(db, "secret", t.TempDir(), "/", nil)

	server := &model.Server{Name: "n1", AgentID: "agent-1", AgentTokenHash: "hash", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	eval, err := db.EvaluateAuthorizationDesired(ctx, server.ID, 3, "d", []string{"k"}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordAuthorizationConfirmation(ctx, server.ID, eval.State.DesiredRevision, 1, "d", "boot"); err != nil {
		t.Fatal(err)
	}
	usersEval, err := db.EvaluateRuntimeUsersDesired(ctx, server.ID, 3, "u", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordRuntimeUserConfirmation(ctx, server.ID, usersEval.State.DesiredRevision, "u", "boot"); err != nil {
		t.Fatal(err)
	}

	srv.fireAccessDeadlines(ctx, time.Now().UTC())

	auth, err := db.AuthorizationState(ctx, server.ID)
	if err != nil || auth.EvaluatedRoutingRevision != 0 {
		t.Fatalf("auth evaluated=%d err=%v", auth.EvaluatedRoutingRevision, err)
	}
	users, err := db.RuntimeUserState(ctx, server.ID)
	if err != nil || users.EvaluatedRoutingRevision != 0 {
		t.Fatalf("users evaluated=%d err=%v", users.EvaluatedRoutingRevision, err)
	}
}

func TestInvalidateAccessServersIsPrecise(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "precise.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := New(db, "secret", t.TempDir(), "/", nil)

	keep := &model.Server{Name: "keep", AgentID: "a1", AgentTokenHash: "h1", Status: model.ServerOnline}
	touch := &model.Server{Name: "touch", AgentID: "a2", AgentTokenHash: "h2", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, keep); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, touch); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{keep.ID, touch.ID} {
		eval, err := db.EvaluateAuthorizationDesired(ctx, id, 5, "d", []string{"k"}, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.RecordAuthorizationConfirmation(ctx, id, eval.State.DesiredRevision, 1, "d", "boot"); err != nil {
			t.Fatal(err)
		}
		usersEval, err := db.EvaluateRuntimeUsersDesired(ctx, id, 5, "u", time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.RecordRuntimeUserConfirmation(ctx, id, usersEval.State.DesiredRevision, "u", "boot"); err != nil {
			t.Fatal(err)
		}
	}

	srv.invalidateAccessServers(ctx, []int64{touch.ID})

	keepAuth, _ := db.AuthorizationState(ctx, keep.ID)
	touchAuth, _ := db.AuthorizationState(ctx, touch.ID)
	if keepAuth.EvaluatedRoutingRevision != 5 {
		t.Fatalf("keep auth evaluated=%d want 5", keepAuth.EvaluatedRoutingRevision)
	}
	if touchAuth.EvaluatedRoutingRevision != 0 {
		t.Fatalf("touch auth evaluated=%d want 0", touchAuth.EvaluatedRoutingRevision)
	}
	stale, err := db.StaleRuntimeUserServerIDs(ctx, 5)
	if err != nil || len(stale) != 1 || stale[0] != touch.ID {
		t.Fatalf("stale users=%v err=%v", stale, err)
	}
}
