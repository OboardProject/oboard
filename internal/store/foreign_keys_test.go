package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// openPooledStore opens a store whose pool holds several connections so a
// per-connection PRAGMA can be observed leaking between them.
func openPooledStore(t *testing.T) *Store {
	t.Helper()
	opts := DefaultSQLiteOptions()
	opts.MaxOpenConns = 4
	opts.MaxIdleConns = 4
	db, err := OpenWithOptions(filepath.Join(t.TempDir(), "pool.sqlite"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// assertEveryPooledConnectionEnforcesForeignKeys occupies the whole pool at
// once so no connection can answer twice, and checks each one.
func assertEveryPooledConnectionEnforcesForeignKeys(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	conns := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		var enforced int
		if err := conn.QueryRowContext(ctx, `pragma foreign_keys`).Scan(&enforced); err != nil {
			t.Fatal(err)
		}
		if enforced != 1 {
			t.Fatalf("pooled connection %d no longer enforces foreign keys, so deletes on it skip cascades", i)
		}
	}
}

func TestForeignKeysRestoredForEveryPooledConnection(t *testing.T) {
	ctx := context.Background()
	db := openPooledStore(t)
	if err := db.withForeignKeysDisabled(ctx, func(ctx context.Context, conn *sql.Conn) error {
		var enforced int
		if err := conn.QueryRowContext(ctx, `pragma foreign_keys`).Scan(&enforced); err != nil {
			return err
		}
		if enforced != 0 {
			t.Fatal("the rebuild connection is still enforcing foreign keys")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertEveryPooledConnectionEnforcesForeignKeys(t, db)

	failure := errors.New("rebuild failed")
	if err := db.withForeignKeysDisabled(ctx, func(context.Context, *sql.Conn) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("err=%v", err)
	}
	assertEveryPooledConnectionEnforcesForeignKeys(t, db)

	// A cancelled caller must not leave the connection unenforced either.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_ = db.withForeignKeysDisabled(cancelled, func(context.Context, *sql.Conn) error { return nil })
	assertEveryPooledConnectionEnforcesForeignKeys(t, db)
}

// TestDeletedServerCannotLeaveOrphanedRows is the behaviour the restore
// protects: whichever pooled connection serves the delete, dependent rows go
// with it instead of surviving as references to a server that no longer exists.
func TestDeletedServerCannotLeaveOrphanedRows(t *testing.T) {
	ctx := context.Background()
	db := openPooledStore(t)
	if err := db.withForeignKeysDisabled(ctx, func(context.Context, *sql.Conn) error { return nil }); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "orphan-check", EntryAddress: "198.51.100.7", Status: "online"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `insert into server_connectivity_events(server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at) values(?,'server_offline',0,0,'','test',?,'k',?)`, server.ID, now(), now()); err != nil {
		t.Fatal(err)
	}
	// Spread the delete and the check over concurrent pool users so the work
	// does not stay on one connection by accident.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var count int
			_ = db.db.QueryRowContext(ctx, `select count(*) from servers`).Scan(&count)
			time.Sleep(time.Millisecond)
		}()
	}
	if err := db.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	var orphans int
	if err := db.db.QueryRowContext(ctx, `select count(*) from server_connectivity_events where server_id=?`, server.ID).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Fatalf("deleted server left %d connectivity rows behind", orphans)
	}
}
