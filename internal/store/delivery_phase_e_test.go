package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestServerDeliveryFlagsDefaultOnAndSurviveReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "delivery-flags.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "flags-node"}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	flags, err := s.ServerDeliveryFlags(ctx, server.ID)
	if err != nil || !flags.AuthorizationFastLane || !flags.RuntimeUsersEnabled {
		t.Fatalf("missing row must default both lanes on: %+v err=%v", flags, err)
	}
	if err := s.SetServerDeliveryFlags(ctx, ServerDeliveryFlags{ServerID: server.ID, AuthorizationFastLane: false, RuntimeUsersEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	flags, err = s.ServerDeliveryFlags(ctx, server.ID)
	if err != nil || flags.AuthorizationFastLane || !flags.RuntimeUsersEnabled {
		t.Fatalf("persisted flags after reopen: %+v err=%v", flags, err)
	}
}

func TestTrafficTailSnapshotSurvivesUserDelete(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "traffic-tail.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "tail-node"}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "tail-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `insert into traffic_counter_streams(server_id,user_id,counter_source,stream_id,counter_epoch,period_key,inbound_id,path_id,accepted_upload_bytes,accepted_download_bytes,status,first_seen_at,last_seen_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		server.ID, user.ID, "core", "stream-1", "epoch-1", "2026-09", 7, 0, 10, 20, "healthy", ts, ts, ts); err != nil {
		t.Fatal(err)
	}
	ok, err := s.HasTrafficCounterStream(ctx, server.ID, user.ID, "stream-1")
	if err != nil || !ok {
		t.Fatalf("checkpoint missing before delete: ok=%v err=%v", ok, err)
	}
	if err := s.Delete(ctx, "users", user.ID); err != nil {
		t.Fatal(err)
	}
	ok, err = s.HasTrafficCounterStream(ctx, server.ID, user.ID, "stream-1")
	if err != nil || ok {
		t.Fatalf("checkpoint should cascade away: ok=%v err=%v", ok, err)
	}
	ok, err = s.HasTrafficTailStream(ctx, server.ID, user.ID, "stream-1")
	if err != nil || !ok {
		t.Fatalf("delete must snapshot live streams: ok=%v err=%v", ok, err)
	}
	ok, err = s.HasTrafficTailStream(ctx, server.ID, user.ID, "other")
	if err != nil || ok {
		t.Fatalf("unrelated stream matched tail: ok=%v err=%v", ok, err)
	}
}

func TestTrafficTailAndDeliveryFlagsMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous-phase-e.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "legacy-phase-e"}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"drop trigger if exists traffic_tail_on_user_delete",
		"drop table if exists traffic_tail_streams",
		"drop table if exists server_delivery_flags",
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
	flags, err := s.ServerDeliveryFlags(ctx, server.ID)
	if err != nil || !flags.AuthorizationFastLane || !flags.RuntimeUsersEnabled {
		t.Fatalf("flags after migration: %+v err=%v", flags, err)
	}
	user := &model.User{Username: "migrated-tail", PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `insert into traffic_counter_streams(server_id,user_id,counter_source,stream_id,counter_epoch,period_key,inbound_id,path_id,accepted_upload_bytes,accepted_download_bytes,status,first_seen_at,last_seen_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		server.ID, user.ID, "core", "stream-m", "epoch-m", "2026-09", 1, 0, 1, 2, "healthy", ts, ts, ts); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "users", user.ID); err != nil {
		t.Fatal(err)
	}
	ok, err := s.HasTrafficTailStream(ctx, server.ID, user.ID, "stream-m")
	if err != nil || !ok {
		t.Fatalf("migrated trigger did not snapshot: ok=%v err=%v", ok, err)
	}
}
