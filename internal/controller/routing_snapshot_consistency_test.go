package controller

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRoutingSnapshotRebuildsAfterConcurrentRevocation(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "routing.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "snapshot-secret", "")
	user := &model.User{Username: "snapshot-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	revision, err := db.RoutingCacheRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loads := 0
	entry, err := loadConsistentRoutingSnapshot(ctx, revision, db.RoutingCacheRevision, func(ctx context.Context, revision uint64) (*routingSnapshot, error) {
		entry, err := srv.readRoutingSnapshot(ctx, revision)
		if err != nil {
			return nil, err
		}
		loads++
		if loads == 1 {
			user.Status = "disabled"
			if err := db.UpdateUser(ctx, user); err != nil {
				return nil, err
			}
		}
		return entry, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if loads != 2 || entry.usersByID[user.ID].Status != "disabled" {
		t.Fatalf("old data was relabeled with newer revision: loads=%d status=%s", loads, entry.usersByID[user.ID].Status)
	}
	// A waiter arriving after a newer commit cannot use an older completed
	// build; it rebuilds instead of returning the superseded authorization.
	user.Status = "active"
	if err := db.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	srv.routingSnapshotInflight = &routingSnapshotBuild{done: done, entry: entry}
	rebuilt, err := srv.routingSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt == entry || rebuilt.usersByID[user.ID].Status != "active" {
		t.Fatalf("stale joined build was reused: status=%s", rebuilt.usersByID[user.ID].Status)
	}
	if srv.routingSnapshotInflight != nil {
		t.Fatal("rebuild left an in-flight build behind")
	}
}

func TestRoutingSnapshotContinuousChangesAreBounded(t *testing.T) {
	ctx := context.Background()
	var revision uint64 = 1
	loads := 0
	entry, err := loadConsistentRoutingSnapshot(ctx, revision, func(context.Context) (uint64, error) { revision++; return revision, nil }, func(context.Context, uint64) (*routingSnapshot, error) { loads++; return &routingSnapshot{}, nil })
	if entry != nil || !errors.Is(err, errRoutingSnapshotChanged) || loads != 3 {
		t.Fatalf("entry=%v err=%v loads=%d", entry, err, loads)
	}
}
