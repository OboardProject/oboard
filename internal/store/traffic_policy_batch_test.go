package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func batchTrafficStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "batch.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func batchTrafficUser(t *testing.T, s *Store, username string) *model.User {
	t.Helper()
	user := &model.User{Username: username, PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	return user
}

// The batched period upsert replaces one statement pair per user. It has to
// land on exactly the row the per-user upsert would have written, including the
// cases where it skips the write: unchanged window, changed window, changed
// limit, and a limit change that flips the row into quota_exceeded.
func TestEnsureTrafficPeriodsMatchesPerUserUpsert(t *testing.T) {
	ctx := context.Background()
	s := batchTrafficStore(t)
	batched := batchTrafficUser(t, s, "batched")
	single := batchTrafficUser(t, s, "single")

	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	compare := func(stage, periodKey string, windowStart, windowEnd time.Time, limit int64) {
		t.Helper()
		batch, err := s.EnsureTrafficPeriods(ctx, []TrafficPeriodRequest{{UserID: batched.ID, PeriodKey: periodKey, StartedAt: windowStart, EndsAt: windowEnd, LimitBytes: limit}})
		if err != nil {
			t.Fatal(err)
		}
		want, err := s.EnsureTrafficPeriod(ctx, single.ID, periodKey, windowStart, windowEnd, limit)
		if err != nil {
			t.Fatal(err)
		}
		got := batch[batched.ID]
		if got.PeriodKey != want.PeriodKey || !got.StartedAt.Equal(want.StartedAt) || !got.EndsAt.Equal(want.EndsAt) ||
			got.Limit != want.Limit || got.State != want.State || got.Upload != want.Upload || got.Download != want.Download {
			t.Fatalf("%s: batched period %+v does not match per-user upsert %+v", stage, got, want)
		}
	}

	compare("create", "2026-09", start, end, 1000)
	compare("unchanged", "2026-09", start, end, 1000)
	compare("window moved", "2026-09", start.Add(time.Hour), end, 1000)
	compare("limit raised", "2026-09", start.Add(time.Hour), end, 5000)

	// Consume quota on both users, then lower the limit below what was used:
	// the upsert's conflict state must flip to quota_exceeded on both sides.
	for _, userID := range []int64{batched.ID, single.ID} {
		if _, err := s.db.ExecContext(ctx, `update traffic_periods set upload_bytes=600,download_bytes=600 where user_id=? and period_key=?`, userID, "2026-09"); err != nil {
			t.Fatal(err)
		}
	}
	compare("quota exceeded", "2026-09", start.Add(time.Hour), end, 900)
}

// The batched lease read is a pure fast path: it may only report a lease the
// serialized allocator would also have accepted without refreshing it.
func TestCurrentTrafficLeaseAllocationsMatchesSingleRowFastPath(t *testing.T) {
	ctx := context.Background()
	s := batchTrafficStore(t)
	const limit = int64(10 << 30)
	node := &model.Server{Name: "lease-node", PublicIPv4: "203.0.113.9"}
	if err := s.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	serverID := node.ID

	healthy := batchTrafficUser(t, s, "healthy")
	expired := batchTrafficUser(t, s, "expired")
	drained := batchTrafficUser(t, s, "drained")
	absent := batchTrafficUser(t, s, "absent")

	now := time.Now().UTC()
	rows := []struct {
		userID     int64
		lease      int64
		consumed   int64
		state      string
		validUntil time.Time
	}{
		{healthy.ID, trafficLeaseChunk(limit), 0, trafficLeaseActive, now.Add(24 * time.Hour)},
		{expired.ID, trafficLeaseChunk(limit), 0, trafficLeaseActive, now.Add(-time.Hour)},
		{drained.ID, trafficLeaseChunk(limit), trafficLeaseChunk(limit), trafficLeaseActive, now.Add(24 * time.Hour)},
	}
	for _, row := range rows {
		if _, err := s.db.ExecContext(ctx, `insert into traffic_leases(server_id,user_id,period_key,lease_bytes,consumed_bytes,lease_revision,state,issued_at,last_synced_at,valid_until,updated_at) values(?,?,?,?,?,1,?,?,?,?,?)`,
			serverID, row.userID, "2026-09", row.lease, row.consumed, row.state,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), row.validUntil.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}

	probes := []TrafficLeaseProbe{
		{UserID: healthy.ID, PeriodKey: "2026-09", LimitBytes: limit},
		{UserID: expired.ID, PeriodKey: "2026-09", LimitBytes: limit},
		{UserID: drained.ID, PeriodKey: "2026-09", LimitBytes: limit},
		{UserID: absent.ID, PeriodKey: "2026-09", LimitBytes: limit},
	}
	got, err := s.CurrentTrafficLeaseAllocations(ctx, serverID, probes, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range probes {
		want, wantOK := s.currentTrafficLeaseAllocation(ctx, serverID, probe.UserID, probe.PeriodKey, probe.LimitBytes, now)
		have, haveOK := got[probe.UserID]
		if wantOK != haveOK {
			t.Fatalf("user %d: batched healthy=%v single healthy=%v", probe.UserID, haveOK, wantOK)
		}
		if wantOK && have != want {
			t.Fatalf("user %d: batched %+v single %+v", probe.UserID, have, want)
		}
	}
	if _, ok := got[healthy.ID]; !ok {
		t.Fatal("expected the healthy lease to be served from the batched read")
	}

	// A lease recorded under a different window must never satisfy a probe for
	// the current one.
	stale, err := s.CurrentTrafficLeaseAllocations(ctx, serverID, []TrafficLeaseProbe{{UserID: healthy.ID, PeriodKey: "2026-10", LimitBytes: limit}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 0 {
		t.Fatalf("expected no allocation for a different period key, got %+v", stale)
	}
}

// The preloaded transition set has to answer exactly like the per-user queries
// it replaces, in both directions.
func TestTrafficPeriodTransitionsMatchPerUserLookups(t *testing.T) {
	ctx := context.Background()
	s := batchTrafficStore(t)
	user := batchTrafficUser(t, s, "migrated")

	base := time.Now().UTC()
	chain := []struct {
		source, target string
		createdAt      time.Time
	}{
		{"2026-08", "2026-09#migration-1", base},
		{"2026-09#migration-1", "2026-09#migration-2", base.Add(time.Minute)},
		{"2026-07", "2026-09#migration-2", base.Add(2 * time.Minute)},
	}
	for _, link := range chain {
		if _, err := s.db.ExecContext(ctx, `insert into traffic_period_transitions(user_id,source_period_key,target_period_key,created_at) values(?,?,?,?)`,
			user.ID, link.source, link.target, link.createdAt.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}

	sets, err := s.TrafficPeriodTransitions(ctx, []int64{user.ID})
	if err != nil {
		t.Fatal(err)
	}
	set := sets[user.ID]
	for _, key := range []string{"2026-08", "2026-09#migration-1", "2026-07", "2026-09#migration-2", "2026-10"} {
		wantKey, wantChanged, wantErr := s.ResolveTrafficPeriodKey(ctx, user.ID, key)
		gotKey, gotChanged, gotErr := set.Resolve(key)
		if wantKey != gotKey || wantChanged != gotChanged || (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("resolve %q: batched (%q,%v,%v) single (%q,%v,%v)", key, gotKey, gotChanged, gotErr, wantKey, wantChanged, wantErr)
		}
		wantSource, wantOK := s.PreviousTrafficPeriodKey(ctx, user.ID, key)
		gotSource, gotOK := set.Previous(key)
		if wantSource != gotSource || wantOK != gotOK {
			t.Fatalf("previous %q: batched (%q,%v) single (%q,%v)", key, gotSource, gotOK, wantSource, wantOK)
		}
	}
}
