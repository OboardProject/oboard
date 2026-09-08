package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// trafficPeriodRowState reads the columns a re-ensure would rewrite.
func trafficPeriodRowState(t *testing.T, s *Store, userID int64, periodKey string) (string, string) {
	t.Helper()
	var state, updated string
	if err := s.db.QueryRowContext(context.Background(), `select state,updated_at from traffic_periods where user_id=? and period_key=?`, userID, periodKey).Scan(&state, &updated); err != nil {
		t.Fatal(err)
	}
	return state, updated
}

func trafficLeaseRowState(t *testing.T, s *Store, serverID, userID int64, periodKey string) (int64, string) {
	t.Helper()
	var revision int64
	var updated string
	if err := s.db.QueryRowContext(context.Background(), `select lease_revision,updated_at from traffic_leases where server_id=? and user_id=? and period_key=?`, serverID, userID, periodKey).Scan(&revision, &updated); err != nil {
		t.Fatal(err)
	}
	return revision, updated
}

func trafficWriteFixture(t *testing.T) (*Store, *model.User, *model.Server) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	user := &model.User{Username: "amplification-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111119", ProxyPassword: "pass", SubscriptionToken: "amplification-sub"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "amplification-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	return s, user, server
}

// Every traffic report re-ensures the window of every accounting user, so an
// unchanged window must not produce a row write.
func TestEnsureTrafficPeriodOnlyWritesWhenTheWindowMoved(t *testing.T) {
	s, user, _ := trafficWriteFixture(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	end := start.Add(24 * time.Hour)
	if _, err := s.EnsureTrafficPeriod(ctx, user.ID, "2026-09", start, end, 1<<30); err != nil {
		t.Fatal(err)
	}
	_, first := trafficPeriodRowState(t, s, user.ID, "2026-09")
	for range 3 {
		time.Sleep(2 * time.Millisecond)
		if _, err := s.EnsureTrafficPeriod(ctx, user.ID, "2026-09", start, end, 1<<30); err != nil {
			t.Fatal(err)
		}
	}
	state, repeated := trafficPeriodRowState(t, s, user.ID, "2026-09")
	if repeated != first {
		t.Fatalf("unchanged window rewrote the period row: first=%s repeated=%s", first, repeated)
	}
	if state != "active" {
		t.Fatalf("period state = %q, want active", state)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := s.EnsureTrafficPeriod(ctx, user.ID, "2026-09", start, end, 2<<30); err != nil {
		t.Fatal(err)
	}
	if _, changed := trafficPeriodRowState(t, s, user.ID, "2026-09"); changed == first {
		t.Fatal("a changed traffic limit did not rewrite the period row")
	}
}

// A period whose counters already passed the incoming limit must still be
// moved to quota_exceeded even though the window itself did not move.
func TestEnsureTrafficPeriodStillAppliesQuotaState(t *testing.T) {
	s, user, server := trafficWriteFixture(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	end := start.Add(24 * time.Hour)
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-09", StartedAt: start, EndsAt: end, Limit: 1000}
	if _, err := s.EnsureTrafficPeriod(ctx, user.ID, period.PeriodKey, start, end, period.Limit); err != nil {
		t.Fatal(err)
	}
	report := model.TrafficReport{ReportID: "amplification-quota", ServerID: server.ID, UserID: user.ID, PeriodKey: period.PeriodKey, Upload: 900, StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}
	if accepted, err := s.AddTrafficReports(ctx, []model.TrafficReport{report}, period); err != nil || len(accepted) != 1 {
		t.Fatalf("add traffic report accepted=%v err=%v", accepted, err)
	}
	if _, err := s.EnsureTrafficPeriod(ctx, user.ID, period.PeriodKey, start, end, 500); err != nil {
		t.Fatal(err)
	}
	if state, _ := trafficPeriodRowState(t, s, user.ID, period.PeriodKey); state != "quota_exceeded" {
		t.Fatalf("period state = %q, want quota_exceeded", state)
	}
}

// A lease with more than half a chunk left needs no allocation, so repeating
// the policy sync must answer from the existing row without writing it.
func TestEnsureTrafficLeaseAllocationSkipsWritesWhileTheLeaseHolds(t *testing.T) {
	s, user, server := trafficWriteFixture(t)
	ctx := context.Background()
	const limit = 1 << 30
	first, err := s.EnsureTrafficLeaseAllocation(ctx, server.ID, user.ID, "2026-09", limit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.RemainingBytes <= 0 {
		t.Fatalf("first allocation = %#v, want a positive lease", first)
	}
	revision, updated := trafficLeaseRowState(t, s, server.ID, user.ID, "2026-09")
	for range 3 {
		time.Sleep(2 * time.Millisecond)
		repeat, err := s.EnsureTrafficLeaseAllocation(ctx, server.ID, user.ID, "2026-09", limit, 0)
		if err != nil {
			t.Fatal(err)
		}
		if repeat != first {
			t.Fatalf("repeat allocation = %#v, want %#v", repeat, first)
		}
	}
	if nextRevision, nextUpdated := trafficLeaseRowState(t, s, server.ID, user.ID, "2026-09"); nextRevision != revision || nextUpdated != updated {
		t.Fatalf("a holding lease was rewritten: revision %d->%d updated %s->%s", revision, nextRevision, updated, nextUpdated)
	}
}

// Once the lease is mostly consumed the sync must top it up again, so the
// read-only fast path may never hide a needed allocation.
func TestEnsureTrafficLeaseAllocationStillGrantsWhenTheLeaseRunsDown(t *testing.T) {
	s, user, server := trafficWriteFixture(t)
	ctx := context.Background()
	const limit = 1 << 30
	first, err := s.EnsureTrafficLeaseAllocation(ctx, server.ID, user.ID, "2026-09", limit, 0)
	if err != nil {
		t.Fatal(err)
	}
	consumed := first.ResetBytes - trafficLeaseChunk(limit)/4
	if _, err := s.db.ExecContext(ctx, `update traffic_leases set consumed_bytes=? where server_id=? and user_id=? and period_key=?`, consumed, server.ID, user.ID, "2026-09"); err != nil {
		t.Fatal(err)
	}
	revision, _ := trafficLeaseRowState(t, s, server.ID, user.ID, "2026-09")
	topUp, err := s.EnsureTrafficLeaseAllocation(ctx, server.ID, user.ID, "2026-09", limit, 0)
	if err != nil {
		t.Fatal(err)
	}
	if topUp.ResetBytes <= first.ResetBytes {
		t.Fatalf("lease was not topped up: first=%#v topUp=%#v", first, topUp)
	}
	if nextRevision, _ := trafficLeaseRowState(t, s, server.ID, user.ID, "2026-09"); nextRevision <= revision {
		t.Fatalf("lease revision did not advance: %d -> %d", revision, nextRevision)
	}
}

// An expired validity window must fall through to the write path so the lease
// is re-validated instead of being served forever from the fast path.
func TestEnsureTrafficLeaseAllocationRefreshesAnAgingValidityWindow(t *testing.T) {
	s, user, server := trafficWriteFixture(t)
	ctx := context.Background()
	const limit = 1 << 30
	if _, err := s.EnsureTrafficLeaseAllocation(ctx, server.ID, user.ID, "2026-09", limit, 0); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `update traffic_leases set valid_until=? where server_id=? and user_id=? and period_key=?`, stale, server.ID, user.ID, "2026-09"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureTrafficLeaseAllocation(ctx, server.ID, user.ID, "2026-09", limit, 0); err != nil {
		t.Fatal(err)
	}
	var refreshed string
	if err := s.db.QueryRowContext(ctx, `select valid_until from traffic_leases where server_id=? and user_id=? and period_key=?`, server.ID, user.ID, "2026-09").Scan(&refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed == stale {
		t.Fatal("an aging validity window was not refreshed")
	}
}
