package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

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
		"file:/absolute/path/oboard.sqlite?cache=shared",
		":memory:",
		"file::memory:",
	} {
		dsn, err := sqliteDSN(path, 5*time.Second)
		if err != nil {
			t.Fatalf("sqliteDSN(%q): %v", path, err)
		}
		if !strings.Contains(dsn, "_pragma=busy_timeout%285000%29") || !strings.Contains(dsn, "_pragma=foreign_keys%281%29") {
			t.Fatalf("sqliteDSN(%q) = %q, missing pragmas", path, dsn)
		}
		if strings.Contains(path, "cache=shared") && !strings.Contains(dsn, "cache=shared&") {
			t.Fatalf("sqliteDSN(%q) = %q, existing query was not preserved", path, dsn)
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
