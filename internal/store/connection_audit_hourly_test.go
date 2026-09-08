package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConnectionAuditHourlyMatchesRawRobustZ(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hourly.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user := &model.User{Username: "hourly-user", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "hourly-node", PublicIPv4: "203.0.113.50", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	reports := []model.ConnectionAuditReport{}
	for week := 0; week < 4; week++ {
		started := base.Add(-time.Duration(week*7*24) * time.Hour)
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: "r-" + started.Format("2006010215"), ServerID: server.ID, UserID: user.ID,
			SourceIP: "203.0.113.10", Network: "tcp", ConnectionCount: int64(10 + week),
			BucketCapacity: 1, CollectionStartedAt: started, CollectionEndedAt: started.Add(time.Minute),
			StartedAt: started, EndedAt: started.Add(time.Minute), CreatedAt: started,
		})
	}
	if _, err := s.AddConnectionAuditReportsResult(ctx, reports); err != nil {
		t.Fatal(err)
	}
	dirty, err := s.ListConnectionAuditHourlyDirty(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) == 0 {
		t.Fatal("expected dirty hours after insert")
	}
	for _, item := range dirty {
		if err := s.RecomputeConnectionAuditHour(ctx, item.UserID, item.UTCHour, item.DirtyAt); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetAuditRollupState(ctx, AuditRollupState{BackfillComplete: true, ReadPath: "hourly", AlgorithmVersion: connectionAuditHourlyAlgorithm}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.batchConnectionAuditRobustZBucketsRaw(ctx, []int64{user.ID}, base)
	if err != nil {
		t.Fatal(err)
	}
	hourly, err := s.batchConnectionAuditRobustZBuckets(ctx, []int64{user.ID}, base)
	if err != nil {
		t.Fatal(err)
	}
	want := computeConnectionAuditRobustZ(raw[user.ID], base)
	got := computeConnectionAuditRobustZ(hourly[user.ID], base)
	if got != want {
		t.Fatalf("robustZ hourly=%v raw=%v", got, want)
	}
}

func TestConnectionAuditHourlyDirtySurvivesConcurrentRefresh(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hourly-dirty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user := &model.User{Username: "dirty-user", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "dirty-node", PublicIPv4: "203.0.113.51", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	report := model.ConnectionAuditReport{
		ReportID: "dirty-1", ServerID: server.ID, UserID: user.ID, SourceIP: "203.0.113.11", Network: "tcp",
		ConnectionCount: 5, BucketCapacity: 1, CollectionStartedAt: started, CollectionEndedAt: started.Add(time.Minute),
		StartedAt: started, EndedAt: started.Add(time.Minute), CreatedAt: started,
	}
	if _, err := s.AddConnectionAuditReportsResult(ctx, []model.ConnectionAuditReport{report}); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListConnectionAuditHourlyDirty(ctx, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("dirty=%v err=%v", items, err)
	}
	oldDirty := items[0]
	time.Sleep(2 * time.Millisecond)
	if err := s.MarkConnectionAuditHoursDirty(ctx, map[int64][]time.Time{user.ID: {started}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecomputeConnectionAuditHour(ctx, oldDirty.UserID, oldDirty.UTCHour, oldDirty.DirtyAt); err != nil {
		t.Fatal(err)
	}
	remaining, err := s.ListConnectionAuditHourlyDirty(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("newer dirty mark was cleared: %#v", remaining)
	}
}

func TestConnectionAuditHourlyBackfillPages(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "hourly-backfill.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	user := &model.User{Username: "backfill-user", PasswordHash: "h", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "backfill-node", PublicIPv4: "203.0.113.52", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	for i := 0; i < 5; i++ {
		started := base.Add(time.Duration(i) * time.Hour)
		_, err := s.AddConnectionAuditReportsResult(ctx, []model.ConnectionAuditReport{{
			ReportID: "bf-" + started.Format("2006010215"), ServerID: server.ID, UserID: user.ID,
			SourceIP: "203.0.113.12", Network: "tcp", ConnectionCount: 3, BucketCapacity: 1,
			CollectionStartedAt: started, CollectionEndedAt: started.Add(time.Minute),
			StartedAt: started, EndedAt: started.Add(time.Minute), CreatedAt: started,
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for {
		n, done, err := s.BackfillConnectionAuditHourlyPages(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		if n == 0 {
			t.Fatal("backfill stalled")
		}
	}
	state, err := s.GetAuditRollupState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !state.BackfillComplete || state.ReadPath != "hourly" {
		t.Fatalf("state=%+v", state)
	}
}
