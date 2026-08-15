package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// TestSQLStatementCounter verifies that the per-Store SQL statement counter
// advances for query, exec, and write-transaction statements so hot-path tests
// can rely on it to bound per-report SQL work.
func TestSQLStatementCounter(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	server := &model.Server{Name: "counter", AgentID: "counter-agent", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	before := s.SQLStatementCount()
	beforeTx := s.SQLWriteTransactionCount()
	if _, err := s.GetServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.SQLStatementCount() - before; got < 1 {
		t.Fatalf("statement counter did not advance for GetServer: delta=%d", got)
	}
	report := model.HealthReport{AgentID: server.AgentID, Status: model.ServerOnline, Timestamp: time.Now().UTC()}
	window := model.ServerTrafficWindow{Key: "2026-08-01", Start: time.Now().UTC(), End: time.Now().UTC().Add(time.Hour)}
	if _, _, err := s.UpsertHealthTransition(ctx, report, window); err != nil {
		t.Fatal(err)
	}
	if got := s.SQLWriteTransactionCount() - beforeTx; got < 1 {
		t.Fatalf("write transaction counter did not advance: delta=%d", got)
	}
	if s.SQLStatementCount() <= before {
		t.Fatalf("statement counter did not advance after health transition: %d", s.SQLStatementCount())
	}
}
