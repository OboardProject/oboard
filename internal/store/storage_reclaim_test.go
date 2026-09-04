package store

import (
	"context"
	"database/sql"
	"os"
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
	if got := sqlitePragmaInt(t, s, "synchronous"); got != 1 {
		t.Fatalf("synchronous = %d, want NORMAL (1) in WAL mode", got)
	}
	if got := sqlitePragmaInt(t, s, "cache_size"); got != -int64(defaultSQLiteCacheKB) {
		t.Fatalf("cache_size = %d, want -%d", got, defaultSQLiteCacheKB)
	}
}

// An installation created before incremental auto-vacuum existed opens with
// auto_vacuum=NONE and a file full of free pages. Opening it must convert the
// mode once and reclaim the space, without losing data.
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
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := sqlitePragmaInt(t, s, "auto_vacuum"); got != sqliteAutoVacuumIncremental {
		t.Fatalf("auto_vacuum = %d after open, want %d", got, sqliteAutoVacuumIncremental)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("database did not shrink: %d -> %d bytes", before.Size(), after.Size())
	}
	var rows int
	if err := s.db.QueryRowContext(ctx, `select count(*) from bulk`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 20 {
		t.Fatalf("compaction changed the data: %d rows, want 20", rows)
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
