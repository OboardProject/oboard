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

func TestSQLiteWriterQueueDoesNotBlockAgentLookup(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server := &model.Server{Name: "isolation", AgentID: "isolation-agent"}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := s.db.BeginTx(ctx, nil)
			if err == nil {
				_ = tx.Rollback()
			}
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for s.db.Stats().WaitCount < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.db.Stats().WaitCount < 1 {
		t.Fatal("writers did not queue")
	}
	readCtx, readCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer readCancel()
	got, err := s.GetServerByAgent(readCtx, server.AgentID)
	if err != nil {
		t.Fatalf("agent lookup blocked behind writers: %v", err)
	}
	readTx, err := s.db.BeginTx(readCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("read snapshot blocked behind writers: %v", err)
	}
	var count int
	err = readTx.QueryRowContext(readCtx, "select count(*) from servers").Scan(&count)
	_ = readTx.Rollback()
	if err != nil || count != 1 {
		t.Fatalf("snapshot count=%d err=%v", count, err)
	}
	if got.ID != server.ID {
		t.Fatalf("wrong server: %d", got.ID)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteReaderRejectsWritesAndWriterCancellationRecovers(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.db.readDB().ExecContext(ctx, "create table forbidden(id integer)"); err == nil {
		t.Fatal("read pool accepted write")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := s.SetSetting(short, "queue", "canceled"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("write cancellation: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "queue", "recovered"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSetting(ctx, "queue"); err != nil || got != "recovered" {
		t.Fatalf("value=%q err=%v", got, err)
	}
}

func TestTrafficLeaseSweepFailureRollsBack(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(ctx, "alter table traffic_leases rename to unavailable_traffic_leases"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureTrafficLeaseAllocation(ctx, 1, 1, "period", 100, 0); err == nil {
		t.Fatal("expected sweep failure")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("sweep returned an open transaction to the pool: %v", err)
	}
	_ = tx.Rollback()
}

func TestSQLiteQueuedWritesOutliveBusyTimeout(t *testing.T) {
	opts := DefaultSQLiteOptions()
	opts.BusyTimeout = 50 * time.Millisecond
	s, err := OpenWithOptions(filepath.Join(t.TempDir(), "controller.sqlite"), opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	results := make(chan error, 12)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.SetSetting(ctx, "queued", "saved") }()
	}
	time.Sleep(200 * time.Millisecond)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		if err := <-results; err != nil {
			t.Errorf("queued write failed: %v", err)
		}
	}
}
