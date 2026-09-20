package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
	"github.com/OboardProject/oboard/internal/model"
)

func accountAuditFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "audit.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	u := &model.User{Username: "audit-account", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return s, u.ID
}
func accountAuditSnapshot(id int64, minute int, score int) auditrisk.Snapshot {
	when := time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
	good := auditrisk.Dimension{State: auditrisk.Satisfied}
	return auditrisk.Snapshot{AccountID: id, AsOf: when, WindowEnd: when, WindowStart: when.Add(-30 * time.Minute), Activity: &auditrisk.Score{Lower: score, Upper: score, Status: "complete"}, Policy: auditrisk.DefaultPolicy(), Versions: auditrisk.Versions{Model: "v1", Source: "v1", Baseline: "v1"}, Quality: auditrisk.Quality{IdentityTrusted: good, SourceUsable: good, Deduplicated: good, MeasurementValid: good, CapabilitySupported: good, HistoryComplete: good, CoverageComplete: good, Freshness: good, TimeAligned: good, SourceSetComplete: good}}
}
func TestAccountAuditPendingPaginationAndIsolation(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	u := &model.User{Username: "second-account", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	p, err := s.ListAccountAuditSnapshots(ctx, AccountAuditQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	rows := p.Items.([]AccountAuditRow)
	if len(rows) != 1 || rows[0].UserID != id || rows[0].Snapshot != nil || rows[0].EvaluationStatus != "pending" || p.NextOffset == nil {
		t.Fatalf("bad pending page: %+v", p)
	}
	p, err = s.ListAccountAuditSnapshots(ctx, AccountAuditQuery{AllowedUserIDs: []int64{u.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if rows = p.Items.([]AccountAuditRow); len(rows) != 1 || rows[0].UserID != u.ID {
		t.Fatal("scope leaked")
	}
	p, err = s.ListAccountAuditSnapshots(ctx, AccountAuditQuery{AllowedUserIDs: []int64{}})
	if err != nil || len(p.Items.([]AccountAuditRow)) != 0 {
		t.Fatal("empty scope leaked")
	}
	if _, err = s.ListAccountAuditSnapshots(ctx, AccountAuditQuery{Limit: 101}); err == nil {
		t.Fatal("unbounded query accepted")
	}
}
func TestAccountAuditDebounceRecoveryAndReplay(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	rev := int64(0)
	save := func(minute, score int, complete bool) {
		t.Helper()
		rev++
		snap := accountAuditSnapshot(id, minute, score)
		if !complete {
			snap.Quality.CoverageComplete.State = auditrisk.Unknown
		}
		if err := s.SaveAccountAuditSnapshot(ctx, snap, rev); err != nil {
			t.Fatal(err)
		}
	}
	events := func() []AccountAuditEvent {
		t.Helper()
		p, err := s.ListAccountAuditEvents(ctx, AccountAuditQuery{})
		if err != nil {
			t.Fatal(err)
		}
		return p.Items.([]AccountAuditEvent)
	}
	save(1, 80, true)
	save(1, 80, true)
	if len(events()) != 0 {
		t.Fatal("same cycle triggered alert")
	}
	save(2, 80, true)
	if rows := events(); len(rows) != 1 || rows[0].Cycle != 1 || rows[0].Status != "pending" {
		t.Fatalf("missing event: %+v", rows)
	}
	for i := 3; i <= 15; i++ {
		save(i, 0, false)
	}
	if events()[0].Status != "pending" {
		t.Fatal("missing data recovered event")
	}
	for i := 16; i <= 24; i++ {
		save(i, 0, true)
	}
	if events()[0].Status != "pending" {
		t.Fatal("early recovery")
	}
	save(25, 0, true)
	if events()[0].Status != "recovered" {
		t.Fatal("no recovery")
	}
	save(26, 80, true)
	save(27, 80, true)
	rows := events()
	if len(rows) != 2 || rows[0].Cycle != 2 {
		t.Fatalf("new cycle absent: %+v", rows)
	}
	if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 27, 80), rev); err == nil {
		t.Fatal("stale revision accepted")
	}
}
func TestAccountAuditDirtyRevisionAndAtomicFailure(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	first, err := s.MarkAccountAuditDirty(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.MarkAccountAuditDirty(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 1, 80), first); err != nil {
		t.Fatal(err)
	}
	var got int64
	if err = s.db.QueryRowContext(ctx, `SELECT revision FROM account_audit_dirty WHERE user_id=?`, id).Scan(&got); err != nil || got != second {
		t.Fatal("concurrent dirty update lost", got, err)
	}
	if err = s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 2, 80), second); err != nil {
		t.Fatal(err)
	}
	third, err := s.MarkAccountAuditDirty(ctx, id)
	if err != nil || third <= second {
		t.Fatal("revision reset", third, err)
	}
	// Force the event-state write to fail after snapshot replacement. Both roll back.
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER fail_account_audit BEFORE UPDATE ON account_audit_event_state BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 3, 0), third); err == nil {
		t.Fatal("injection not reached")
	}
	var persisted int64
	if err = s.db.QueryRowContext(ctx, `SELECT revision FROM account_audit_snapshots WHERE user_id=?`, id).Scan(&persisted); err != nil || persisted != second {
		t.Fatal("partial snapshot committed", err)
	}
	if err = s.db.QueryRowContext(ctx, `SELECT revision FROM account_audit_dirty WHERE user_id=?`, id).Scan(&got); err != nil || got != third {
		t.Fatal("failed work lost", err)
	}
}
func TestAccountAuditRestartKeepsPendingWorkAndSnapshot(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	if err := s.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 1, 80), 1); err != nil {
		t.Fatal(err)
	}
	revision, err := s.MarkAccountAuditDirty(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	path := s.path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	work, err := reopened.ListAccountAuditWork(ctx, 0, 10)
	if err != nil || len(work) != 1 || work[0].Revision != revision {
		t.Fatalf("work not restored: %+v %v", work, err)
	}
	page, err := reopened.ListAccountAuditSnapshots(ctx, AccountAuditQuery{UserID: id})
	if err != nil {
		t.Fatal(err)
	}
	if rows := page.Items.([]AccountAuditRow); len(rows) != 1 || rows[0].Snapshot == nil {
		t.Fatal("snapshot lost after restart")
	}
	if err := reopened.SaveAccountAuditSnapshot(ctx, accountAuditSnapshot(id, 2, 80), revision); err != nil {
		t.Fatal(err)
	}
	page, err = reopened.ListAccountAuditEvents(ctx, AccountAuditQuery{})
	if err != nil || len(page.Items.([]AccountAuditEvent)) != 1 {
		t.Fatal("debounce state lost after restart", err)
	}
}

func TestAccountAuditPolicyChangeAndGapsResetDebounce(t *testing.T) {
	s, id := accountAuditFixture(t)
	ctx := context.Background()
	for i, minute := range []int{1, 3, 4} {
		snap := accountAuditSnapshot(id, minute, 90)
		if i == 2 {
			snap.Policy.Version = "v2"
		}
		if err := s.SaveAccountAuditSnapshot(ctx, snap, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.ListAccountAuditEvents(ctx, AccountAuditQuery{})
	if err != nil || len(p.Items.([]AccountAuditEvent)) != 0 {
		t.Fatal("gap or policy change counted as consecutive")
	}
}
