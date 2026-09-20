package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
)

// AccountActivityBatch is authenticated, ownership-checked and source-normalized
// by the receiver. Counters are cumulative within boot/stream/minute/identity.
type AccountActivityBatch struct {
	// ReceiptDigest binds the authenticated wire report independently of source policy.
	ReceiptDigest        [32]byte                   `json:"-"`
	ServerID             int64                      `json:"server_id"`
	CollectorBootID      string                     `json:"collector_boot_id"`
	CollectorStartedAt   int64                      `json:"collector_started_at"`
	StreamType           string                     `json:"stream_type"`
	Sequence             int64                      `json:"sequence"`
	MinuteUnix           int64                      `json:"minute_unix"`
	SourceVersion        string                     `json:"source_version"`
	ClockState           string                     `json:"clock_state"`
	Complete             bool                       `json:"complete"`
	DroppedUpdates       uint64                     `json:"dropped_updates"`
	UnknownSourceUpdates uint64                     `json:"unknown_source_updates"`
	Items                []AccountActivityBatchItem `json:"items"`
}
type AccountActivityBatchItem struct {
	AccountID     int64  `json:"account_id"`
	InboundID     int64  `json:"inbound_id"`
	PathID        int64  `json:"path_id"`
	SourceGroup   string `json:"source_group"`
	CounterSource string `json:"counter_source,omitempty"`
	ActivityBits  uint16 `json:"activity_bits"`
	UploadBytes   uint64 `json:"upload_bytes"`
	DownloadBytes uint64 `json:"download_bytes"`
}

func (r AccountActivityBatchItem) counterSource() string {
	if r.CounterSource != "" {
		return r.CounterSource
	}
	return r.SourceGroup
}

type AccountActivityBatchReceipt struct {
	Accepted  bool  `json:"accepted"`
	Duplicate bool  `json:"duplicate"`
	Sequence  int64 `json:"sequence"`
}
type AccountActivityExpected struct {
	AccountID           int64  `json:"account_id"`
	ServerID            int64  `json:"server_id"`
	StreamType          string `json:"stream_type"`
	MinuteUnix          int64  `json:"minute_unix"`
	CapabilitySupported bool   `json:"capability_supported"`
}

func (s *Store) InitAccountActivityPipelineSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS account_activity_v1_boot_generation(server INTEGER NOT NULL,stream TEXT NOT NULL,started_at INTEGER NOT NULL,PRIMARY KEY(server,stream));
 CREATE TABLE IF NOT EXISTS account_activity_v1_checkpoint(server INTEGER NOT NULL,stream TEXT NOT NULL,boot TEXT NOT NULL,minute INTEGER NOT NULL,account INTEGER NOT NULL,inbound INTEGER NOT NULL,path INTEGER NOT NULL,source TEXT NOT NULL,version TEXT NOT NULL,seq INTEGER NOT NULL,up INTEGER NOT NULL,down INTEGER NOT NULL,bits INTEGER NOT NULL,PRIMARY KEY(server,stream,boot,minute,account,inbound,path,source,version,seq));
 CREATE INDEX IF NOT EXISTS account_activity_v1_checkpoint_minute ON account_activity_v1_checkpoint(minute);
 CREATE TABLE IF NOT EXISTS account_activity_v1_clock(id INTEGER PRIMARY KEY CHECK(id=1), now_seconds INTEGER NOT NULL);
 INSERT OR IGNORE INTO account_activity_v1_clock VALUES(1,0);
 CREATE TABLE IF NOT EXISTS account_activity_v1_stream(server INTEGER NOT NULL,stream TEXT NOT NULL,boot TEXT NOT NULL,retired BLOB NOT NULL,high INTEGER NOT NULL,floor INTEGER NOT NULL,PRIMARY KEY(server,stream));
 CREATE TABLE IF NOT EXISTS account_activity_v1_inbox(server INTEGER NOT NULL,stream TEXT NOT NULL,boot TEXT NOT NULL,seq INTEGER NOT NULL,minute INTEGER NOT NULL,digest BLOB NOT NULL,payload BLOB,gap INTEGER NOT NULL,PRIMARY KEY(server,stream,boot,seq));
 CREATE INDEX IF NOT EXISTS account_activity_v1_pending ON account_activity_v1_inbox(minute) WHERE payload IS NOT NULL;
 CREATE TABLE IF NOT EXISTS account_activity_v1_counter(server INTEGER NOT NULL,stream TEXT NOT NULL,boot TEXT NOT NULL,minute INTEGER NOT NULL,account INTEGER NOT NULL,inbound INTEGER NOT NULL,path INTEGER NOT NULL,source TEXT NOT NULL,version TEXT NOT NULL,seq INTEGER NOT NULL,up INTEGER NOT NULL,down INTEGER NOT NULL,bits INTEGER NOT NULL,PRIMARY KEY(server,stream,boot,minute,account,inbound,path,source,version));
 CREATE TABLE IF NOT EXISTS account_activity_v1_source(account INTEGER NOT NULL,minute INTEGER NOT NULL,version TEXT NOT NULL,source TEXT NOT NULL,bytes INTEGER NOT NULL,bits INTEGER NOT NULL,PRIMARY KEY(account,minute,version,source));
 CREATE TABLE IF NOT EXISTS account_activity_v1_expected(account INTEGER NOT NULL,server INTEGER NOT NULL,stream TEXT NOT NULL,minute INTEGER NOT NULL,capable INTEGER NOT NULL,PRIMARY KEY(account,server,stream,minute));
 CREATE TABLE IF NOT EXISTS account_activity_v1_coverage(server INTEGER NOT NULL,stream TEXT NOT NULL,minute INTEGER NOT NULL,version TEXT NOT NULL,complete INTEGER NOT NULL,aligned INTEGER NOT NULL,PRIMARY KEY(server,stream,minute,version));
 CREATE TABLE IF NOT EXISTS account_activity_v1_quality(account INTEGER NOT NULL,minute INTEGER NOT NULL,overflow INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(account,minute));
 CREATE TABLE IF NOT EXISTS account_activity_v1_dirty(account INTEGER PRIMARY KEY,revision INTEGER NOT NULL,evaluated INTEGER NOT NULL DEFAULT 0);
 CREATE TABLE IF NOT EXISTS account_activity_v1_baseline(account INTEGER NOT NULL,day INTEGER NOT NULL,policy TEXT NOT NULL,version TEXT NOT NULL,histogram BLOB NOT NULL,last_minute INTEGER NOT NULL,PRIMARY KEY(account,day,policy,version));
 CREATE TABLE IF NOT EXISTS account_activity_v1_baseline_exclusion(account INTEGER PRIMARY KEY,until_minute INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS account_activity_v1_counter_minute ON account_activity_v1_counter(minute);
 CREATE INDEX IF NOT EXISTS account_activity_v1_source_minute ON account_activity_v1_source(minute);
 CREATE INDEX IF NOT EXISTS account_activity_v1_expected_minute ON account_activity_v1_expected(minute);
 CREATE INDEX IF NOT EXISTS account_activity_v1_expected_account_minute ON account_activity_v1_expected(account,minute);
 CREATE INDEX IF NOT EXISTS account_activity_v1_expected_stream ON account_activity_v1_expected(server,stream,minute);
 CREATE INDEX IF NOT EXISTS account_activity_v1_coverage_minute ON account_activity_v1_coverage(minute);
 CREATE INDEX IF NOT EXISTS account_activity_v1_quality_minute ON account_activity_v1_quality(minute);
 `)
	return err
}

// A bounded retired-boot Bloom filter fails closed on collisions. Unlike an
// expiring boot cache it cannot resurrect a replay after its tombstone expires.
func activityRetired(filter []byte, boot string, add bool) bool {
	h := sha256.Sum256([]byte(boot))
	found := true
	for i := 0; i < 4; i++ {
		n := (int(h[2*i])<<8 | int(h[2*i+1])) % (len(filter) * 8)
		if filter[n/8]&(1<<uint(n%8)) == 0 {
			found = false
		}
		if add {
			filter[n/8] |= 1 << uint(n%8)
		}
	}
	return found
}
func validateActivityBatch(b *AccountActivityBatch) error {
	if b.CollectorStartedAt <= 0 || b.ServerID <= 0 || !activityHex(b.CollectorBootID, 16) || (b.StreamType != "kernel" && b.StreamType != "ssh") || b.Sequence <= 0 || b.MinuteUnix <= 0 || b.MinuteUnix%60 != 0 || b.SourceVersion == "" || len(b.SourceVersion) > 128 || (b.ClockState != "aligned" && b.ClockState != "unaligned" && b.ClockState != "unknown") || len(b.Items) > 4096 {
		return ErrAccountActivityInvalid
	}
	sort.Slice(b.Items, func(i, j int) bool {
		a, c := b.Items[i], b.Items[j]
		if a.AccountID != c.AccountID {
			return a.AccountID < c.AccountID
		}
		if a.InboundID != c.InboundID {
			return a.InboundID < c.InboundID
		}
		if a.PathID != c.PathID {
			return a.PathID < c.PathID
		}
		return a.counterSource() < c.counterSource()
	})
	for i, r := range b.Items {
		if r.AccountID <= 0 || r.InboundID <= 0 || r.PathID < 0 || !activityHex(r.SourceGroup, 32) || !activityHex(r.counterSource(), 32) || r.ActivityBits > 4095 || r.UploadBytes > math.MaxInt64 || r.DownloadBytes > math.MaxInt64-r.UploadBytes || ((r.UploadBytes+r.DownloadBytes == 0) != (r.ActivityBits == 0)) {
			return ErrAccountActivityInvalid
		}
		if i > 0 {
			p := b.Items[i-1]
			if p.AccountID == r.AccountID && p.InboundID == r.InboundID && p.PathID == r.PathID && p.counterSource() == r.counterSource() {
				return ErrAccountActivityInvalid
			}
		}
	}
	return nil
}
func (s *Store) ReceiveAccountActivityBatch(ctx context.Context, batch AccountActivityBatch, now time.Time) (AccountActivityBatchReceipt, error) {
	receipt := AccountActivityBatchReceipt{Sequence: batch.Sequence}
	batch.Items = append([]AccountActivityBatchItem(nil), batch.Items...)
	if err := validateActivityBatch(&batch); err != nil {
		return receipt, err
	}
	if batch.CollectorStartedAt > now.Add(30*time.Second).UnixNano() {
		return receipt, ErrAccountActivityInvalid
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		return receipt, err
	}
	if len(payload) > 1<<20 {
		return receipt, ErrAccountActivityCapacity
	}
	digest := batch.ReceiptDigest
	if digest == ([32]byte{}) {
		digest = sha256.Sum256(payload)
	}
	tx, err := s.db.BeginCountedTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE account_activity_v1_clock SET now_seconds=MAX(now_seconds,?) WHERE id=1`, now.Unix()); err != nil {
		return receipt, err
	}
	var clock int64
	if err = tx.QueryRowContext(ctx, `SELECT now_seconds FROM account_activity_v1_clock WHERE id=1`).Scan(&clock); err != nil {
		return receipt, err
	}
	var old []byte
	err = tx.QueryRowContext(ctx, `SELECT digest FROM account_activity_v1_inbox WHERE server=? AND stream=? AND boot=? AND seq=?`, batch.ServerID, batch.StreamType, batch.CollectorBootID, batch.Sequence).Scan(&old)
	if err == nil {
		if string(old) != string(digest[:]) {
			return receipt, ErrAccountActivityConflict
		}
		receipt.Accepted = true
		receipt.Duplicate = true
		return receipt, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return receipt, err
	}
	if batch.MinuteUnix > clock-60 || batch.MinuteUnix < clock-180 {
		return receipt, ErrAccountActivityExpired
	}
	var count, node, bytes int64
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(server=?),0),COALESCE(SUM(length(payload)),0) FROM account_activity_v1_inbox WHERE payload IS NOT NULL`, batch.ServerID).Scan(&count, &node, &bytes); err != nil {
		return receipt, err
	}
	if count >= 4096 || node >= 256 || bytes+int64(len(payload)) > 64<<20 {
		return receipt, ErrAccountActivityCapacity
	}
	var boot string
	var filter []byte
	var high, floor int64
	err = tx.QueryRowContext(ctx, `SELECT boot,retired,high,floor FROM account_activity_v1_stream WHERE server=? AND stream=?`, batch.ServerID, batch.StreamType).Scan(&boot, &filter, &high, &floor)
	changed := false
	if errors.Is(err, sql.ErrNoRows) {
		var n int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_activity_v1_stream`).Scan(&n); err != nil {
			return receipt, err
		}
		if n >= 8192 {
			return receipt, ErrAccountActivityCapacity
		}
		filter = make([]byte, 1024)
		boot = batch.CollectorBootID
		changed = true
	} else if err != nil {
		return receipt, err
	} else {
		var generation int64
		if err = tx.QueryRowContext(ctx, `SELECT started_at FROM account_activity_v1_boot_generation WHERE server=? AND stream=?`, batch.ServerID, batch.StreamType).Scan(&generation); err != nil {
			return receipt, err
		}
		if (boot == batch.CollectorBootID && batch.CollectorStartedAt != generation) || (boot != batch.CollectorBootID && batch.CollectorStartedAt <= generation) {
			return receipt, ErrAccountActivityConflict
		}
	}
	if boot != batch.CollectorBootID {
		if activityRetired(filter, batch.CollectorBootID, false) {
			return receipt, ErrAccountActivityConflict
		}
		activityRetired(filter, boot, true)
		boot = batch.CollectorBootID
		high = 0
		floor = 0
		changed = true
	}
	if batch.Sequence <= floor {
		return receipt, ErrAccountActivityExpired
	}
	if err = reserveActivityCapacity(ctx, tx.Tx, batch, clock); err != nil {
		return receipt, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_boot_generation VALUES(?,?,?) ON CONFLICT(server,stream) DO UPDATE SET started_at=excluded.started_at`, batch.ServerID, batch.StreamType, batch.CollectorStartedAt); err != nil {
		return receipt, err
	}
	gap := changed || batch.Sequence > high+1
	high = max(high, batch.Sequence)
	floor = max(floor, high-4096)
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_stream VALUES(?,?,?,?,?,?) ON CONFLICT(server,stream) DO UPDATE SET boot=excluded.boot,retired=excluded.retired,high=excluded.high,floor=excluded.floor`, batch.ServerID, batch.StreamType, boot, filter, high, floor); err != nil {
		return receipt, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_activity_v1_inbox WHERE server=? AND stream=? AND payload IS NULL AND (boot<>? OR seq<=?)`, batch.ServerID, batch.StreamType, boot, floor); err != nil {
		return receipt, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_inbox VALUES(?,?,?,?,?,?,?,?)`, batch.ServerID, batch.StreamType, boot, batch.Sequence, batch.MinuteUnix, digest[:], payload, gap); err != nil {
		return receipt, err
	}
	receipt.Accepted = true
	return receipt, tx.Commit()
}

func (s *Store) SaveAccountActivityExpected(ctx context.Context, expected []AccountActivityExpected, now time.Time) error {
	if len(expected) > 16384 {
		return ErrAccountActivityCapacity
	}
	tx, err := s.db.BeginCountedTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE account_activity_v1_clock SET now_seconds=MAX(now_seconds,?) WHERE id=1`, now.Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM account_activity_v1_expected WHERE minute<?`, now.UTC().Truncate(time.Minute).Unix()-1920); err != nil {
		return err
	}
	if err = reclaimActivityDirty(ctx, tx.Tx, now.Unix()); err != nil {
		return err
	}
	for _, e := range expected {
		if e.AccountID <= 0 || e.ServerID <= 0 || (e.StreamType != "kernel" && e.StreamType != "ssh") || e.MinuteUnix%60 != 0 || e.MinuteUnix < now.Unix()-1920 || e.MinuteUnix > now.Unix() {
			return ErrAccountActivityInvalid
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_expected VALUES(?,?,?,?,?) ON CONFLICT(account,server,stream,minute) DO UPDATE SET capable=MIN(capable,excluded.capable)`, e.AccountID, e.ServerID, e.StreamType, e.MinuteUnix, e.CapabilitySupported); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_dirty(account,revision) VALUES(?,1) ON CONFLICT(account) DO UPDATE SET revision=revision+1`, e.AccountID); err != nil {
			return err
		}
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_activity_v1_expected`).Scan(&n); err != nil {
		return err
	}
	if n > 1048576 {
		return ErrAccountActivityCapacity
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_activity_v1_dirty`).Scan(&n); err != nil {
		return err
	}
	if n > 65536 {
		return ErrAccountActivityCapacity
	}
	return tx.Commit()
}

// ApplyPendingAccountActivity commits each receipt and all its effects together.
// Out-of-order cumulative snapshots use the sequence of each individual counter,
// not a stream-wide high-water mark, so gaps do not discard unrelated identities.
func (s *Store) ApplyPendingAccountActivity(ctx context.Context, limit int, now time.Time) (int, error) {
	if limit < 1 || limit > 256 {
		return 0, ErrAccountActivityInvalid
	}
	n := 0
	for n < limit {
		ok, err := s.applyAccountActivityBatch(ctx, now)
		if err != nil {
			return n, err
		}
		if !ok {
			break
		}
		n++
	}
	return n, nil
}
func (s *Store) applyAccountActivityBatch(ctx context.Context, now time.Time) (bool, error) {
	tx, err := s.db.BeginCountedTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE account_activity_v1_clock SET now_seconds=MAX(now_seconds,?) WHERE id=1`, now.Unix()); err != nil {
		return false, err
	}
	var data []byte
	var gap bool
	err = tx.QueryRowContext(ctx, `SELECT payload,gap FROM account_activity_v1_inbox WHERE payload IS NOT NULL ORDER BY minute,server,stream,seq LIMIT 1`).Scan(&data, &gap)
	if errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	var b AccountActivityBatch
	if err = json.Unmarshal(data, &b); err != nil {
		return false, err
	}
	aligned := b.ClockState == "aligned"
	complete := b.Complete && aligned && !gap && b.DroppedUpdates == 0 && b.UnknownSourceUpdates == 0
	for _, r := range b.Items {
		var seq, up, down, bits int64
		err = tx.QueryRowContext(ctx, `SELECT seq,up,down,bits FROM account_activity_v1_counter WHERE server=? AND stream=? AND boot=? AND minute=? AND account=? AND inbound=? AND path=? AND source=? AND version=?`, b.ServerID, b.StreamType, b.CollectorBootID, b.MinuteUnix, r.AccountID, r.InboundID, r.PathID, r.counterSource(), b.SourceVersion).Scan(&seq, &up, &down, &bits)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if seq > b.Sequence {
			if int64(r.UploadBytes) > up || int64(r.DownloadBytes) > down || int64(r.ActivityBits)&bits != int64(r.ActivityBits) {
				return false, ErrAccountActivityConflict
			}
			continue
		}
		if int64(r.UploadBytes) < up || int64(r.DownloadBytes) < down || int64(r.ActivityBits)&bits != bits {
			return false, ErrAccountActivityConflict
		}
		delta := int64(r.UploadBytes) - up + int64(r.DownloadBytes) - down
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_counter VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(server,stream,boot,minute,account,inbound,path,source,version) DO UPDATE SET seq=excluded.seq,up=excluded.up,down=excluded.down,bits=excluded.bits`, b.ServerID, b.StreamType, b.CollectorBootID, b.MinuteUnix, r.AccountID, r.InboundID, r.PathID, r.counterSource(), b.SourceVersion, b.Sequence, r.UploadBytes, r.DownloadBytes, r.ActivityBits); err != nil {
			return false, err
		}
		if aligned {
			var size int
			var saved int64
			err = tx.QueryRowContext(ctx, `SELECT bytes FROM account_activity_v1_source WHERE account=? AND minute=? AND version=? AND source=?`, r.AccountID, b.MinuteUnix, b.SourceVersion, r.SourceGroup).Scan(&saved)
			exists := err == nil
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return false, err
			}
			if !exists {
				if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_activity_v1_source WHERE account=? AND minute=?`, r.AccountID, b.MinuteUnix).Scan(&size); err != nil {
					return false, err
				}
			}
			if (!exists && size >= 32) || saved > math.MaxInt64-delta {
				if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_quality VALUES(?,?,1) ON CONFLICT(account,minute) DO UPDATE SET overflow=1`, r.AccountID, b.MinuteUnix); err != nil {
					return false, err
				}
			} else if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_source VALUES(?,?,?,?,?,?) ON CONFLICT(account,minute,version,source) DO UPDATE SET bytes=bytes+excluded.bytes,bits=bits|excluded.bits`, r.AccountID, b.MinuteUnix, b.SourceVersion, r.SourceGroup, delta, r.ActivityBits); err != nil {
				return false, err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_dirty(account,revision) VALUES(?,1) ON CONFLICT(account) DO UPDATE SET revision=revision+1`, r.AccountID); err != nil {
			return false, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_coverage VALUES(?,?,?,?,?,?) ON CONFLICT(server,stream,minute,version) DO UPDATE SET complete=MIN(complete,excluded.complete),aligned=MIN(aligned,excluded.aligned)`, b.ServerID, b.StreamType, b.MinuteUnix, b.SourceVersion, complete, aligned); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO account_activity_v1_dirty(account,revision) SELECT DISTINCT account,1 FROM account_activity_v1_expected WHERE server=? AND stream=? AND minute=? ON CONFLICT(account) DO UPDATE SET revision=revision+1`, b.ServerID, b.StreamType, b.MinuteUnix); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE account_activity_v1_inbox SET payload=NULL WHERE server=? AND stream=? AND boot=? AND seq=?`, b.ServerID, b.StreamType, b.CollectorBootID, b.Sequence); err != nil {
		return false, err
	}
	// Counter and source state are bounded to the scoring window plus lateness.
	for _, table := range []string{"source", "expected", "coverage", "quality"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM account_activity_v1_`+table+` WHERE rowid IN (SELECT rowid FROM account_activity_v1_`+table+` WHERE minute<? LIMIT 500)`, now.UTC().Truncate(time.Minute).Unix()-1920); err != nil {
			return false, err
		}
	}
	if err = reclaimActivityDirty(ctx, tx.Tx, now.Unix()); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) LoadAccountAuditFeatures(ctx context.Context, userID int64, asOf time.Time, policy auditrisk.Policy, sourceVersion string) (auditrisk.Features, auditrisk.Quality, error) {
	f := auditrisk.Features{AccountID: userID, EvidenceCutoff: asOf.UTC().Truncate(time.Minute)}
	yes := auditrisk.Dimension{State: auditrisk.Satisfied, ReasonCode: "validated_report"}
	unknown := auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "not_observed"}
	q := auditrisk.Quality{IdentityTrusted: yes, SourceUsable: yes, Deduplicated: yes, MeasurementValid: yes, CoverageComplete: yes, TimeAligned: yes, SourceSetComplete: yes, BaselineReady: unknown, HistoryComplete: unknown, Freshness: yes, CapabilitySupported: yes}
	if userID <= 0 || sourceVersion == "" {
		return f, q, ErrAccountActivityInvalid
	}
	if err := policy.Validate(); err != nil {
		return f, q, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return f, q, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT revision FROM account_activity_v1_dirty WHERE account=?`, userID).Scan(&f.DataRevision)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return f, q, err
	}
	for i := 0; i < auditrisk.WindowMinutes; i++ {
		minute := f.EvidenceCutoff.Add(time.Duration(i-auditrisk.WindowMinutes) * time.Minute)
		m := minute.Unix()
		var expected, covered, capable, aligned int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(c.complete=1),0),COALESCE(SUM(e.capable),0),COALESCE(SUM(c.aligned=1),0) FROM account_activity_v1_expected e LEFT JOIN account_activity_v1_coverage c ON c.server=e.server AND c.stream=e.stream AND c.minute=e.minute AND c.version=? WHERE e.account=? AND e.minute=?`, sourceVersion, userID, m).Scan(&expected, &covered, &capable, &aligned); err != nil {
			return f, q, err
		}
		complete := expected > 0 && covered == expected && capable == expected
		if !complete {
			q.CoverageComplete = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "expected_stream_missing"}
		}
		if capable != expected || expected == 0 {
			q.CapabilitySupported = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "collector_capability_missing"}
		}
		// Missing streams affect coverage, not the time mapping of evidence
		// already received. An explicit unaligned report is a different condition.
		var clockErrors int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_activity_v1_expected e JOIN account_activity_v1_coverage c ON c.server=e.server AND c.stream=e.stream AND c.minute=e.minute AND c.version=? WHERE e.account=? AND e.minute=? AND c.aligned=0`, sourceVersion, userID, m).Scan(&clockErrors); err != nil {
			return f, q, err
		}
		if clockErrors > 0 {
			q.TimeAligned = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "stream_clock_unaligned"}
		}
		var overflow int
		err = tx.QueryRowContext(ctx, `SELECT overflow FROM account_activity_v1_quality WHERE account=? AND minute=?`, userID, m).Scan(&overflow)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return f, q, err
		}
		if overflow != 0 {
			complete = false
			q.SourceSetComplete = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "source_capacity"}
		}
		rows, e := tx.QueryContext(ctx, `SELECT source,bits,bytes FROM account_activity_v1_source WHERE account=? AND minute=? AND version=? ORDER BY source LIMIT 32`, userID, m, sourceVersion)
		if e != nil {
			return f, q, e
		}
		var sources []auditrisk.SourceActivity
		for rows.Next() {
			var r auditrisk.SourceActivity
			if e = rows.Scan(&r.SourceGroup, &r.Bitmap, &r.Bytes); e != nil {
				rows.Close()
				return f, q, e
			}
			sources = append(sources, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return f, q, e
		}
		a, e := auditrisk.AggregateMinute(sources, complete, policy)
		if e != nil {
			return f, q, e
		}
		if a.Overflow || a.CounterOverflow {
			q.SourceSetComplete = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "source_capacity"}
		}
		f.Activity[i] = auditrisk.Minute{Start: minute, Sources: a.Sources}
		f.ExposureActivity[i] = auditrisk.Minute{Start: minute, Sources: auditrisk.CountRange{Upper: policy.ActivitySources.Full}}
	}
	f.NovelRepeatedSources = auditrisk.CountRange{Upper: policy.ExposureSources.Full}
	var latest sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MAX(c.minute) FROM account_activity_v1_expected e JOIN account_activity_v1_coverage c ON c.server=e.server AND c.stream=e.stream AND c.minute=e.minute AND c.version=? WHERE e.account=?`, sourceVersion, userID).Scan(&latest); err != nil {
		return f, q, err
	}
	if !latest.Valid || latest.Int64 < f.EvidenceCutoff.Unix()-180 {
		q.Freshness = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "activity_reports_stale"}
	}
	rows, err := tx.QueryContext(ctx, `SELECT histogram FROM account_activity_v1_baseline WHERE account=? AND day>=? AND day<=? AND policy=? AND version=? ORDER BY day LIMIT 14`, userID, asOf.UTC().Truncate(24*time.Hour).Unix()-13*86400, asOf.Unix(), policy.Version, sourceVersion)
	if err != nil {
		return f, q, err
	}
	var days []auditrisk.BaselineDay
	for rows.Next() {
		var data []byte
		var day auditrisk.BaselineDay
		if err = rows.Scan(&data); err != nil {
			rows.Close()
			return f, q, err
		}
		if err = json.Unmarshal(data, &day); err != nil {
			rows.Close()
			return f, q, err
		}
		days = append(days, day)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return f, q, err
	}
	baseline, err := auditrisk.SummarizeBaseline(days)
	if err != nil {
		return f, q, err
	}
	if baseline.Ready {
		q.BaselineReady = yes
	} else {
		q.BaselineReady = auditrisk.Dimension{State: auditrisk.Unknown, ReasonCode: "baseline_not_ready"}
	}
	return f, q, tx.Commit()
}

// Dirty revisions are acknowledged conditionally so updates racing evaluation
// remain queued. Pagination is stable and does not clear state on reads.
type AccountActivityDirty struct {
	AccountID int64
	Revision  uint64
}

func (s *Store) ListAccountActivityDirty(ctx context.Context, limit int) ([]AccountActivityDirty, error) {
	if limit < 1 || limit > 500 {
		return nil, ErrAccountActivityInvalid
	}
	rows, err := s.db.QueryContext(ctx, `SELECT account,revision FROM account_activity_v1_dirty WHERE revision>evaluated ORDER BY account LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountActivityDirty
	for rows.Next() {
		var d AccountActivityDirty
		if err = rows.Scan(&d.AccountID, &d.Revision); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) MarkAccountActivityPipelineEvaluated(ctx context.Context, userID int64, revision uint64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE account_activity_v1_dirty SET evaluated=? WHERE account=? AND revision=?`, revision, userID, revision)
	return err
}
