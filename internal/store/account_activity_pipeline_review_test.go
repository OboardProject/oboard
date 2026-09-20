package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/auditactivity"
)

func TestAccountActivityPipelineDelayedBootBarrier(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	b, now := activityBatchFixture()
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
		t.Fatal(err)
	}
	old := b
	old.CollectorBootID = "1123456789abcdef0123456789abcdef"
	old.MinuteUnix -= 60
	if _, err := s.ReceiveAccountActivityBatch(ctx, old, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("delayed unseen boot: %v", err)
	}
	old.MinuteUnix = b.MinuteUnix
	if _, err := s.ReceiveAccountActivityBatch(ctx, old, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("same-minute ambiguous boot: %v", err)
	}
	old.CollectorStartedAt = b.CollectorStartedAt - 1
	old.MinuteUnix += 60
	now = now.Add(time.Minute)
	old.Sequence = 2
	if _, err := s.ReceiveAccountActivityBatch(ctx, old, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("older boot with newer minute: %v", err)
	}
	old.MinuteUnix = b.MinuteUnix
	old.CollectorStartedAt = b.CollectorStartedAt + 1
	if _, err := s.ReceiveAccountActivityBatch(ctx, old, now); err != nil {
		t.Fatal(err)
	}
	delayed := old
	delayed.CollectorBootID = "3123456789abcdef0123456789abcdef"
	delayed.CollectorStartedAt = b.CollectorStartedAt - 1
	delayed.MinuteUnix += 60
	if _, err := s.ReceiveAccountActivityBatch(ctx, delayed, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("delayed unseen older boot after restart: %v", err)
	}
	var persisted int64
	if err := s.db.QueryRowContext(ctx, `SELECT started_at FROM account_activity_v1_boot_generation WHERE server=1 AND stream='kernel'`).Scan(&persisted); err != nil || persisted != old.CollectorStartedAt {
		t.Fatalf("persisted generation: %d %v", persisted, err)
	}
	changed := old
	changed.Sequence++
	changed.CollectorStartedAt++
	if _, err := s.ReceiveAccountActivityBatch(ctx, changed, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("same boot changed generation: %v", err)
	}
	future := old
	future.CollectorBootID = "2123456789abcdef0123456789abcdef"
	future.CollectorStartedAt = now.Add(30*time.Second).UnixNano() + 1
	if _, err := s.ReceiveAccountActivityBatch(ctx, future, now); !errors.Is(err, ErrAccountActivityInvalid) {
		t.Fatalf("future boot: %v", err)
	}
	future.CollectorStartedAt = 0
	if _, err := s.ReceiveAccountActivityBatch(ctx, future, now); !errors.Is(err, ErrAccountActivityInvalid) {
		t.Fatalf("missing generation: %v", err)
	}
	b.Sequence = 2
	b.MinuteUnix = old.MinuteUnix + 60
	now = now.Add(time.Minute)
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); !errors.Is(err, ErrAccountActivityConflict) {
		t.Fatalf("retired boot revived: %v", err)
	}
}

func TestAccountActivityPipelineAppliedPredecessorConstraint(t *testing.T) {
	for _, apply := range []bool{false, true} {
		s := activityPipelineStore(t, ":memory:")
		ctx := context.Background()
		b, now := activityBatchFixture()
		for _, seq := range []int64{1, 3} {
			b.Sequence = seq
			b.Items[0].UploadBytes = uint64(seq * 100)
			if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
				t.Fatal(err)
			}
			if apply {
				if _, err := s.ApplyPendingAccountActivity(ctx, 1, now); err != nil {
					t.Fatal(err)
				}
			}
		}
		b.Sequence = 2
		b.Items[0].UploadBytes = 50
		if r, err := s.ReceiveAccountActivityBatch(ctx, b, now); r.Accepted || !errors.Is(err, ErrAccountActivityConflict) {
			t.Fatalf("apply=%v lower predecessor accepted: %+v %v", apply, r, err)
		}
		b.Items[0].UploadBytes = 350
		if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); !errors.Is(err, ErrAccountActivityConflict) {
			t.Fatalf("successor constraint: %v", err)
		}
		b.Items[0].UploadBytes = 200
		if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApplyPendingAccountActivity(ctx, 10, now); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAccountActivityPipelineDirtyAdmissionAndReclamation(t *testing.T) {
	ctx := context.Background()
	s := activityPipelineStore(t, ":memory:")
	b, now := activityBatchFixture()
	if _, err := s.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(100) UNION ALL SELECT x+1 FROM n WHERE x<65635) INSERT INTO account_activity_v1_dirty SELECT x,1,0 FROM n`); err != nil {
		t.Fatal(err)
	}
	if r, err := s.ReceiveAccountActivityBatch(ctx, b, now); r.Accepted || !errors.Is(err, ErrAccountActivityCapacity) {
		t.Fatalf("unreserved ACK: %+v %v", r, err)
	}
	if _, err := s.db.Exec(`UPDATE account_activity_v1_dirty SET evaluated=revision`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveAccountActivityBatch(ctx, b, now); err != nil {
		t.Fatalf("inactive rows not reclaimed: %v", err)
	}
	if n, err := s.ApplyPendingAccountActivity(ctx, 1, now); n != 1 || err != nil {
		t.Fatalf("accepted head blocked: %d %v", n, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM account_activity_v1_dirty`).Scan(&count); err != nil || count <= 1 || count >= 65536 {
		t.Fatalf("bounded dirty retention: %d %v", count, err)
	}
	before := count
	if err := s.CleanupAccountActivityPipeline(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM account_activity_v1_dirty`).Scan(&count); err != nil || count != before-500 {
		t.Fatalf("maintenance batch: %d -> %d, %v", before, count, err)
	}
}

func TestAccountActivityPipelineRetryRefreshesLastSeen(t *testing.T) {
	var s auditactivity.SubscriptionState
	p := auditactivity.DefaultSubscriptionPolicy()
	o := auditactivity.SubscriptionObservation{At: time.Unix(1800000000, 0), Source: "source", TokenVersion: "token", SourceVersion: "v1", Success: true, IdentityTrusted: true, SourceUsable: true, HistoryComplete: true}
	if _, err := s.Observe(o, p); err != nil {
		t.Fatal(err)
	}
	first := s.Seen[o.Source]
	o.At = o.At.Add(30 * time.Second)
	r, err := s.Observe(o, p)
	seen := s.Seen[o.Source]
	if err != nil || r.Logical || r.Reason != "retry_merged" || s.LogicalUpdates != 1 || !seen.LastSeen.Equal(o.At) || !seen.FirstSeen.Equal(first.FirstSeen) || !seen.SecondUpdate.IsZero() {
		t.Fatalf("retry: %+v %+v %v", r, seen, err)
	}
}
