package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type sqliteCodeTestError int

func (e sqliteCodeTestError) Error() string { return "sqlite test error" }
func (e sqliteCodeTestError) Code() int     { return int(e) }

func TestSQLiteOpenEnablesWAL(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mode string
	if err := s.db.QueryRow(`pragma journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestSQLiteDSNPreservesSupportedPathsAndQueries(t *testing.T) {
	for _, path := range []string{
		"./data/oboard.sqlite",
		filepath.Join(t.TempDir(), "oboard.sqlite"),
		"file:./data/oboard.sqlite",
		"file:/absolute/path/oboard.sqlite?cache=shared&_txlock=deferred",
		":memory:",
		"file::memory:",
	} {
		dsn, err := sqliteDSN(path, 5*time.Second, defaultSQLiteCacheKB, false)
		if err != nil {
			t.Fatalf("sqliteDSN(%q): %v", path, err)
		}
		if !strings.Contains(dsn, "_pragma=busy_timeout%285000%29") || !strings.Contains(dsn, "_pragma=foreign_keys%281%29") || !strings.Contains(dsn, "_txlock=immediate") {
			t.Fatalf("sqliteDSN(%q) = %q, missing pragmas", path, dsn)
		}
		// WAL commits stop fsyncing individually, and the page cache is sized
		// for an installation that records traffic, metrics and audits.
		if !strings.Contains(dsn, "_pragma=synchronous%281%29") || !strings.Contains(dsn, "_pragma=cache_size%28-16384%29") {
			t.Fatalf("sqliteDSN(%q) = %q, missing storage tuning pragmas", path, dsn)
		}
		// Restore runs in DELETE journal mode, where NORMAL does not carry the
		// same guarantee, so it keeps the FULL default.
		restoreDSN, err := sqliteDSN(path, 5*time.Second, defaultSQLiteCacheKB, true)
		if err != nil {
			t.Fatalf("sqliteDSN(%q, restore): %v", path, err)
		}
		if strings.Contains(restoreDSN, "synchronous") {
			t.Fatalf("sqliteDSN(%q, restore) = %q, relaxed durability during restore", path, restoreDSN)
		}
		if strings.Contains(dsn, "_txlock=deferred") {
			t.Fatalf("sqliteDSN(%q) = %q, deferred transaction override survived", path, dsn)
		}
		if strings.Contains(path, "cache=shared") {
			parsed, err := url.Parse(dsn)
			if err != nil || parsed.Query().Get("cache") != "shared" {
				t.Fatalf("sqliteDSN(%q) = %q, existing query was not preserved", path, dsn)
			}
		}
	}
}

func TestIsSQLiteBusyRecognizesExtendedResultCodes(t *testing.T) {
	for _, code := range []int{5, 517, 773} {
		if !IsSQLiteBusy(fmt.Errorf("wrapped: %w", sqliteCodeTestError(code))) {
			t.Fatalf("SQLite result code %d was not recognized as busy", code)
		}
	}
	for _, err := range []error{errors.New("database is locked (5) (SQLITE_BUSY)"), sqliteCodeTestError(6)} {
		if IsSQLiteBusy(err) {
			t.Fatalf("error %v was incorrectly recognized as SQLite busy", err)
		}
	}
}

func TestSQLiteMemoryDatabaseUsesSingleConnection(t *testing.T) {
	for _, dsn := range []string{":memory:", "file::memory:"} {
		t.Run(dsn, func(t *testing.T) {
			s, err := OpenWithOptions(dsn, SQLiteOptions{MaxOpenConns: 8, MaxIdleConns: 8, BusyTimeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if got := s.DBStats().MaxOpenConnections; got != 1 {
				t.Fatalf("MaxOpenConnections = %d, want 1", got)
			}
		})
	}
}

func TestSQLitePragmasApplyToEveryConnection(t *testing.T) {
	const connections = 4
	s, err := OpenWithOptions(filepath.Join(t.TempDir(), "oboard.sqlite"), SQLiteOptions{
		MaxOpenConns: connections,
		MaxIdleConns: connections,
		BusyTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	conns := make([]interface{ Close() error }, 0, connections)
	for i := 0; i < connections; i++ {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		var foreignKeys, busyTimeout int
		if err := conn.QueryRowContext(ctx, `pragma foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(ctx, `pragma busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || busyTimeout != 5000 {
			t.Fatalf("connection %d pragmas = foreign_keys:%d busy_timeout:%d", i, foreignKeys, busyTimeout)
		}
	}
	for _, conn := range conns {
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLiteConcurrentReadersAndWriter(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.SetSetting(ctx, "concurrent", "0"); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 9)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 200; i++ {
			if err := s.SetSetting(ctx, "concurrent", fmt.Sprint(i)); err != nil {
				errs <- err
				return
			}
		}
	}()
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				settings, err := s.ListSettings(ctx)
				if err != nil {
					errs <- err
					return
				}
				if _, ok := settings["concurrent"]; !ok {
					errs <- fmt.Errorf("concurrent setting is missing")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := s.DBStats().MaxOpenConnections; got != DefaultSQLiteOptions().MaxOpenConns {
		t.Fatalf("MaxOpenConnections = %d, want %d", got, DefaultSQLiteOptions().MaxOpenConns)
	}
}

func TestSQLiteAuditIndexes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, name := range []string{
		"idx_connection_audit_user_started",
		"idx_subscription_audit_user_risk_time",
		"idx_subscription_audit_route_risk_time",
		"idx_connection_probe_episodes_time",
		"idx_subscription_rate_buckets_updated",
	} {
		var exists int
		if err := s.db.QueryRow(`select count(*) from sqlite_master where type='index' and name=?`, name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != 1 {
			t.Fatalf("index %s does not exist", name)
		}
	}
}
