package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func sqlitePragmaInt(t *testing.T, s *Store, pragma string) int64 {
	t.Helper()
	var value int64
	if err := s.db.QueryRowContext(context.Background(), `pragma `+pragma).Scan(&value); err != nil {
		t.Fatalf("read pragma %s: %v", pragma, err)
	}
	return value
}

// Retention has always deleted old reporting rows, but with the SQLite default
// auto_vacuum=NONE those pages only moved onto the free list: the file never
// shrank below its historical high-water mark.
func TestNewDatabaseUsesIncrementalAutoVacuum(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := sqlitePragmaInt(t, s, "auto_vacuum"); got != sqliteAutoVacuumIncremental {
		t.Fatalf("auto_vacuum = %d, want %d", got, sqliteAutoVacuumIncremental)
	}
	if got := sqlitePragmaInt(t, s, "synchronous"); got != 2 {
		t.Fatalf("synchronous = %d, want FULL (2) for accounting durability", got)
	}
	if got := sqlitePragmaInt(t, s, "cache_size"); got != -int64(defaultSQLiteCacheKB) {
		t.Fatalf("cache_size = %d, want -%d", got, defaultSQLiteCacheKB)
	}
	if got := sqlitePragmaInt(t, s, "journal_size_limit"); got != sqliteJournalSizeLimitBytes {
		t.Fatalf("journal_size_limit = %d, want %d", got, sqliteJournalSizeLimitBytes)
	}
}

// An installation created before incremental auto-vacuum existed opens with
// auto_vacuum=NONE and a file full of free pages. Opening it must not block on
// a full rewrite; conversion is deferred to RunIncrementalVacuumConversion.
func TestExistingDatabaseIsConvertedAndCompactedOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, statement := range []string{
		`pragma auto_vacuum=none`,
		`pragma journal_mode=WAL`,
		`create table bulk(id integer primary key, blob text)`,
	} {
		if _, err := legacy.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = 'x'
	}
	for i := 0; i < 4000; i++ {
		if _, err := legacy.ExecContext(ctx, `insert into bulk(blob) values(?)`, string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.ExecContext(ctx, `delete from bulk where id > 20`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pending, err := s.incrementalVacuumPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("expected incremental vacuum conversion to be deferred on open")
	}
	if got := sqlitePragmaInt(t, s, "auto_vacuum"); got == sqliteAutoVacuumIncremental {
		t.Fatal("open must not rewrite an existing database into incremental vacuum")
	}
	if err := s.RunIncrementalVacuumConversion(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sqlitePragmaInt(t, s, "auto_vacuum"); got != sqliteAutoVacuumIncremental {
		t.Fatalf("auto_vacuum = %d after explicit conversion, want %d", got, sqliteAutoVacuumIncremental)
	}
	pending, err = s.incrementalVacuumPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("pending flag should clear after explicit conversion")
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `select count(*) from bulk`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 20 {
		t.Fatalf("compaction changed the data: %d rows, want 20", rows)
	}
	// Open already ran migrate, which can reuse the legacy free list before the
	// deferred rewrite. Prove incremental reclaim works after conversion by
	// deleting a large temporary table and asking maintenance to return pages.
	if _, err := s.db.ExecContext(ctx, `create table reclaim_probe(id integer primary key, blob text)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 800; i++ {
		if _, err := s.db.ExecContext(ctx, `insert into reclaim_probe(blob) values(?)`, string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `drop table reclaim_probe`); err != nil {
		t.Fatal(err)
	}
	beforeReclaim := sqlitePragmaInt(t, s, "freelist_count")
	if beforeReclaim == 0 {
		t.Fatal("expected free pages after drop")
	}
	reclaimed, err := s.reclaimFreePages(ctx, maintenanceReclaimPages)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed <= 0 {
		t.Fatalf("incremental vacuum reclaimed %d pages, want > 0 (freelist was %d)", reclaimed, beforeReclaim)
	}
}

// Maintenance returns the pages the retention deletes freed instead of leaving
// them on the free list until the next full rewrite.
func TestMaintenanceReclaimsFreePages(t *testing.T) {
	s, _, _ := newMaintenanceTestStore(t)
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `create table bulk(id integer primary key, blob text)`); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = 'x'
	}
	for i := 0; i < 2000; i++ {
		if _, err := s.db.ExecContext(ctx, `insert into bulk(blob) values(?)`, string(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `delete from bulk`); err != nil {
		t.Fatal(err)
	}
	var freeBefore int64
	if err := s.db.QueryRowContext(ctx, `pragma freelist_count`).Scan(&freeBefore); err != nil {
		t.Fatal(err)
	}
	if freeBefore == 0 {
		t.Fatal("expected free pages after the delete")
	}
	result, err := s.RunMaintenance(ctx, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if result.FreePagesReclaimed <= 0 {
		t.Fatalf("reclaimed %d pages, want the freed pages returned", result.FreePagesReclaimed)
	}
	var freeAfter int64
	if err := s.db.QueryRowContext(ctx, `pragma freelist_count`).Scan(&freeAfter); err != nil {
		t.Fatal(err)
	}
	if freeAfter >= freeBefore {
		t.Fatalf("free list did not shrink: %d -> %d", freeBefore, freeAfter)
	}
}
