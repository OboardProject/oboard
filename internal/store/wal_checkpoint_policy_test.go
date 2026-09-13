package store

import "testing"

// TestWALTruncatePolicyEscalatesPastAPinnedBackfill pins the rule that keeps a
// busy Controller's WAL bounded.
//
// A passive checkpoint only backfills frames older than the oldest live read
// snapshot and never resets the file. On a Controller that always has a reader,
// the backfill can stay at one frame while the log grows for hours, and every
// page read then searches a larger and larger WAL index. The policy therefore
// has to stop waiting for a quiet moment once the log is far past the point
// where the wait is cheaper than the amplification.
func TestWALTruncatePolicyEscalatesPastAPinnedBackfill(t *testing.T) {
	cases := []struct {
		name                    string
		busy, log, checkpointed int
		want                    bool
	}{
		{name: "no wal", want: false},
		{name: "small log stays untouched", log: maintenanceWALTruncateFrames, checkpointed: maintenanceWALTruncateFrames, want: false},
		{name: "drained log past the threshold truncates for free", log: maintenanceWALTruncateFrames + 1, checkpointed: maintenanceWALTruncateFrames + 1, want: true},
		{name: "a reader holding the backfill is waited out while the log is small", log: maintenanceWALTruncateFrames + 1, checkpointed: 0, want: false},
		{name: "busy checkpoint with a small log is left alone", busy: 1, log: maintenanceWALTruncateFrames + 1, checkpointed: maintenanceWALTruncateFrames + 1, want: false},
		// The production case: 318k frames with the backfill pinned at 183k.
		{name: "a pinned backfill far past the ceiling is truncated anyway", log: 318473, checkpointed: 183659, want: true},
		{name: "a busy checkpoint far past the ceiling is retried too", busy: 1, log: 318473, checkpointed: 183659, want: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldTruncateWAL(test.busy, test.log, test.checkpointed); got != test.want {
				t.Fatalf("shouldTruncateWAL(%d,%d,%d) = %v, want %v", test.busy, test.log, test.checkpointed, got, test.want)
			}
		})
	}
}

// TestWALStaysBoundedWhileAReaderHoldsASnapshot is the end-to-end shape: a read
// snapshot is open the whole time, writes keep arriving, and maintenance still
// has to leave the file bounded rather than letting it grow for as long as the
// reader lives.
func TestWALStaysBoundedWhileAReaderHoldsASnapshot(t *testing.T) {
	ctx := t.Context()
	db, err := Open(t.TempDir() + "/wal.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.ExecContext(ctx, `create table wal_probe(id integer primary key, payload text)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `insert into wal_probe(payload) values('seed')`); err != nil {
		t.Fatal(err)
	}
	// An unfinished read is what pins the backfill in production: the snapshot
	// stays open for as long as the rows are held, and the writer keeps
	// extending the log past it. Store transactions reserve the writer, so the
	// reader has to be a plain query rather than a transaction.
	reader, err := db.db.QueryContext(ctx, `select id, payload from wal_probe`)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if !reader.Next() {
		t.Fatal("the pinning reader returned no rows")
	}

	payload := string(make([]byte, 4096))
	for i := 0; i < 200; i++ {
		if _, err := db.db.ExecContext(ctx, `insert into wal_probe(payload) values(?)`, payload); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.CheckpointWAL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.WALLogFrames == 0 {
		t.Fatal("no WAL frames were reported for a database that was just written to")
	}
	// The reader is still open, so this pass cannot drain the log; what the
	// policy must not do is conclude that the file can never be truncated.
	if !shouldTruncateWAL(result.WALBusyFrames, maintenanceWALForceTruncateFrames+1, result.WALCheckpointedFrames) {
		t.Fatal("a log past the ceiling was left to keep growing behind the reader")
	}
}
