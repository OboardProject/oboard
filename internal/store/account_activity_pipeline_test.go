package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

func activityPipelineStore(t testing.TB, path string) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: newCountingDB(db)}
	if err := s.InitAccountActivityPipelineSchema(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return s
}
func activityBatchFixture() (AccountActivityBatch, time.Time) {
	now := time.Unix(1800000060, 0).UTC()
	return AccountActivityBatch{ServerID: 1, CollectorStartedAt: now.Add(-2 * time.Minute).UnixNano(), CollectorBootID: "0123456789abcdef0123456789abcdef", StreamType: "kernel", Sequence: 1, MinuteUnix: now.Unix() - 60, SourceVersion: "v1", ClockState: "aligned", Complete: true, Items: []AccountActivityBatchItem{{AccountID: 7, InboundID: 2, SourceGroup: fmt.Sprintf("%064x", 1), ActivityBits: 7, UploadBytes: 16384}}}, now
}
func TestAccountActivityPipelineRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pipeline.sqlite")
	s := activityPipelineStore(t, path)
	b, now := activityBatchFixture()
	receipt, err := s.ReceiveAccountActivityBatch(ctx, b, now)
	if err != nil || !receipt.Accepted || receipt.Duplicate {
		t.Fatalf("receive: %+v %v", receipt, err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER pipeline_crash BEFORE UPDATE OF payload ON account_activity_v1_inbox BEGIN SELECT RAISE(ABORT,'crash before checkpoint commit'); END`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ApplyPendingAccountActivity(ctx, 10, now); err == nil || n != 0 {
		t.Fatalf("crash: %d %v", n, err)
	}
	var n int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM account_activity_v1_source`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial source persisted: %d %v", n, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER pipeline_crash`); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	s = activityPipelineStore(t, path)
	receipt, err = s.ReceiveAccountActivityBatch(ctx, b, now)
	if err != nil || !receipt.Duplicate {
		t.Fatalf("restart receipt: %+v %v", receipt, err)
	}
	if n, err := s.ApplyPendingAccountActivity(ctx, 10, now); err != nil || n != 1 {
		t.Fatalf("apply: %d %v", n, err)
	}
	if n, err := s.ApplyPendingAccountActivity(ctx, 10, now); err != nil || n != 0 {
		t.Fatalf("reapply: %d %v", n, err)
	}
	altered := b
	altered.Complete = false
	if _, err = s.ReceiveAccountActivityBatch(ctx, altered, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("same identity different payload: %v", err)
	}
	var bytes int64
	if err = s.db.QueryRow(`SELECT bytes FROM account_activity_v1_source`).Scan(&bytes); err != nil || bytes != 16384 {
		t.Fatalf("counter: %d %v", bytes, err)
	}
}
func TestAccountActivityPipelineCumulativeAndNodeUnion(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	b, now := activityBatchFixture()
	for _, seq := range []int64{3, 1, 2} {
		r := b
		r.Sequence = seq
		r.Items = append([]AccountActivityBatchItem(nil), b.Items...)
		r.Items[0].UploadBytes = uint64(seq) * 16384
		r.Items[0].ActivityBits = uint16(1<<uint(seq+2)) - 1
		if _, err := s.ReceiveAccountActivityBatch(ctx, r, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
			t.Fatal(err)
		}
	}
	b.ServerID = 2
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	var count int
	var bytes, bits int64
	if err := s.db.QueryRow(`SELECT COUNT(*),SUM(bytes),MAX(bits) FROM account_activity_v1_source`).Scan(&count, &bytes, &bits); err != nil || count != 1 || bytes != 65536 || bits != 31 {
		t.Fatalf("merged: %d %d %d %v", count, bytes, bits, err)
	}
	f, q, err := s.LoadAccountAuditFeatures(ctx, 7, now, auditrisk.DefaultPolicy(), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if f.Activity[29].Sources.Lower != 1 || f.Activity[29].Sources.Upper != 8 || q.CoverageComplete.State != auditrisk.Unknown {
		t.Fatalf("missing expectations manufactured completeness: %+v %+v", f.Activity[29], q)
	}
}
func TestAccountActivityPipelineCoverageBootOverflowAndDirty(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	b, now := activityBatchFixture()
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	// A later minute of an established stream can prove an observed zero.
	now = now.Add(time.Minute)
	b.MinuteUnix += 60
	b.Sequence++
	b.Items = nil
	if err := s.SaveAccountActivityExpected(ctx, []AccountActivityExpected{{AccountID: 7, ServerID: 1, StreamType: "kernel", MinuteUnix: b.MinuteUnix, CapabilitySupported: true}}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	f, _, err := s.LoadAccountAuditFeatures(ctx, 7, now, auditrisk.DefaultPolicy(), "v1")
	if err != nil || f.Activity[29].Sources.Upper != 0 {
		t.Fatalf("complete empty: %+v %v", f.Activity[29], err)
	}
	dirty, err := s.ListAccountActivityDirty(ctx, 10)
	if err != nil || len(dirty) != 1 {
		t.Fatalf("dirty: %+v %v", dirty, err)
	}
	b.Sequence = 1
	b.MinuteUnix += 60
	now = now.Add(time.Minute)
	b.CollectorBootID = "1123456789abcdef0123456789abcdef"
	b.CollectorStartedAt++
	for i := 0; i < 33; i++ {
		b.Items = append(b.Items, AccountActivityBatchItem{AccountID: 7, InboundID: 2, SourceGroup: fmt.Sprintf("%064x", i+1), ActivityBits: 7, UploadBytes: 16384})
	}
	if _, err = s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkAccountActivityPipelineEvaluated(ctx, 7, dirty[0].Revision); err != nil {
		t.Fatal(err)
	}
	dirty, err = s.ListAccountActivityDirty(ctx, 10)
	if err != nil || len(dirty) != 1 {
		t.Fatalf("racing update lost: %+v %v", dirty, err)
	}
	f, q, err := s.LoadAccountAuditFeatures(ctx, 7, now, auditrisk.DefaultPolicy(), "v1")
	if err != nil || f.Activity[29].Sources.Lower != 32 || q.SourceSetComplete.State != auditrisk.Unknown {
		t.Fatalf("overflow: %+v %+v %v", f.Activity[29], q, err)
	}
	b.CollectorBootID = "0123456789abcdef0123456789abcdef"
	b.Sequence++
	if _, err = s.ReceiveAccountActivityBatch(ctx, b, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("retired boot replay: %v", err)
	}
}
func TestAccountActivityPipelinePendingBudgetRecycles(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	b, now := activityBatchFixture()
	for i := 1; i <= 256; i++ {
		b.Sequence = int64(i)
		if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
			t.Fatal(err)
		}
	}
	b.Sequence = 257
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); !errors.Is(err, ErrAccountActivityCapacity) {
		t.Fatalf("node budget: %v", err)
	}
	if n, err := s.ApplyPendingAccountActivity(ctx, 256, now); err != nil || n != 256 {
		t.Fatalf("apply budget: %d %v", n, err)
	}
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
		t.Fatalf("applied payload blocked receive: %v", err)
	}
}
func BenchmarkAccountActivityPipeline(b *testing.B) {
	s := activityPipelineStore(b, ":memory:")
	r, now := activityBatchFixture()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Sequence = int64(i + 1)
		if _, err := s.ReceiveAccountActivityBatch(ctx, r, now); err != nil {
			b.Fatal(err)
		}
		if _, err := s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
			b.Fatal(err)
		}
	}
}

func TestAccountActivityPipelineReceiptAtomicAndCounterConflict(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	b, now := activityBatchFixture()
	if _, err := s.db.Exec(`CREATE TRIGGER receipt_crash BEFORE INSERT ON account_activity_v1_inbox BEGIN SELECT RAISE(ABORT,'receipt crash'); END`); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ReceiveAccountActivityBatch(ctx, b, now); err == nil || r.Accepted {
		t.Fatalf("false receipt: %+v %v", r, err)
	}
	var streams int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM account_activity_v1_stream`).Scan(&streams); err != nil || streams != 0 {
		t.Fatalf("partial stream commit: %d %v", streams, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER receipt_crash`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
		t.Fatal(err)
	}
	b.Sequence++
	b.Items[0].UploadBytes--
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("pending cumulative rollback accepted: %v", err)
	}
	if _, err := s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("applied cumulative rollback accepted: %v", err)
	}
	b.MinuteUnix -= 180
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); !errors.Is(err, ErrAccountActivityExpired) {
		t.Fatalf("late minute reattributed: %v", err)
	}
}
func TestAccountActivityPipelineBaselineBoundedAndExcluded(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	_, now := activityBatchFixture()
	p := auditrisk.DefaultPolicy()
	yes := auditrisk.Dimension{State: auditrisk.Satisfied}
	snapshot := auditrisk.Snapshot{AccountID: 7, Policy: p, Versions: auditrisk.Versions{Source: "v1"}, Activity: &auditrisk.Score{Lower: 0, Upper: 0}, Attention: &auditrisk.Score{Lower: 0, Upper: 0}, Quality: auditrisk.Quality{TimeAligned: yes, SourceSetComplete: yes, CoverageComplete: yes}}
	for d := 0; d < 15; d++ {
		at := now.Add(time.Duration(d) * 24 * time.Hour)
		for i := range snapshot.Features.Activity {
			snapshot.Features.Activity[i] = auditrisk.Minute{Start: at.Add(time.Duration(i-32) * time.Minute), Sources: auditrisk.CountRange{Lower: 2, Upper: 2}}
		}
		if err := s.RecordAccountActivityBaseline(ctx, snapshot, at); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordAccountActivityBaseline(ctx, snapshot, at); err != nil {
			t.Fatal(err)
		}
	}
	at := now.Add(14 * 24 * time.Hour)
	base, err := s.LoadAccountActivityBaseline(ctx, 7, at, p.Version, "v1")
	if err != nil || !base.Ready || base.ValidDays != 14 || base.ActiveMinutes != 420 || base.Q95 != 2 {
		t.Fatalf("baseline duplicate/retention: %+v %v", base, err)
	}
	if err := s.ExcludeAccountActivityBaseline(ctx, 7, at.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for i := range snapshot.Features.Activity {
		snapshot.Features.Activity[i].Start = snapshot.Features.Activity[i].Start.Add(time.Hour)
	}
	if err := s.RecordAccountActivityBaseline(ctx, snapshot, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	base, err = s.LoadAccountActivityBaseline(ctx, 7, at.Add(time.Hour), p.Version, "v1")
	if err != nil || base.ActiveMinutes != 420 {
		t.Fatalf("investigation admitted: %+v %v", base, err)
	}
	changed, err := s.LoadAccountActivityBaseline(ctx, 7, at, p.Version, "v2")
	if err != nil || changed.Ready {
		t.Fatalf("epoch reused baseline: %+v %v", changed, err)
	}
}
func BenchmarkAccountActivityFeatureRead(b *testing.B) {
	s := activityPipelineStore(b, ":memory:")
	r, now := activityBatchFixture()
	ctx := context.Background()
	if _, err := s.ReceiveAccountActivityBatch(ctx, r, now); err != nil {
		b.Fatal(err)
	}
	if _, err := s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := s.LoadAccountAuditFeatures(ctx, 7, now, auditrisk.DefaultPolicy(), "v1"); err != nil {
			b.Fatal(err)
		}
	}
}

func TestAccountActivityPipelineConcurrentReceipt(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	batch, now := activityBatchFixture()
	results := make(chan error, 64)
	for i := 1; i <= 64; i++ {
		go func(seq int) {
			b := batch
			b.Sequence = int64(seq)
			_, err := s.ReceiveAccountActivityBatch(ctx, b, now)
			results <- err
		}(i)
	}
	for i := 0; i < 64; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.ApplyPendingAccountActivity(ctx, 64, now); err != nil || n != 64 {
		t.Fatalf("apply concurrent receipts: %d %v", n, err)
	}
	var bytes int64
	if err := s.db.QueryRow(`SELECT bytes FROM account_activity_v1_source`).Scan(&bytes); err != nil || bytes != 16384 {
		t.Fatalf("duplicate cumulative bytes: %d %v", bytes, err)
	}
}
