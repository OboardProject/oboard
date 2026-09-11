package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

var ErrHistoryCoverage = errors.New("history_catching_up: historical summaries are not ready within the raw read budget")

type LatencySummaryReadOptions struct {
	TargetIDs []int64
	ForceRaw  bool
}

type LatencyReadCoverage struct {
	CatchingUp     bool
	Legacy         LatencyBucketResult
	Source         string
	LegacyRevision bool
	RevisionCount  int
}
type latencyReadGroup struct {
	delta     latencyRollupDelta
	revisions map[string]bool
}
type latencyReadAccumulator struct {
	overflow  bool
	from, to  time.Time
	interval  time.Duration
	groups    map[string]*latencyReadGroup
	observed  time.Time
	legacy    bool
	revisions map[string]bool
}

func newLatencyAccumulator(from, to time.Time, interval time.Duration) *latencyReadAccumulator {
	return &latencyReadAccumulator{from: from, to: to, interval: interval, groups: map[string]*latencyReadGroup{}, revisions: map[string]bool{}}
}
func (a *latencyReadAccumulator) add(d latencyRollupDelta, at time.Time) {
	start := at.UTC().Truncate(a.interval)
	if start.Before(a.from) {
		start = a.from
	}
	target := LatencyProbeTargetKey(d.kind, d.taskID, d.province, d.carrier)
	key := fmt.Sprintf("%s/%d", target, start.UnixNano())
	g := a.groups[key]
	if g == nil {
		if len(a.groups) >= 12000 {
			a.overflow = true
			return
		}
		g = &latencyReadGroup{delta: latencyRollupDelta{key: latencyRollupKey{start: start.Unix()}, first: d.first, last: d.last}, revisions: map[string]bool{}}
		a.groups[key] = g
	}
	x := &g.delta
	x.kind = max(x.kind, d.kind)
	x.taskID = max(x.taskID, d.taskID)
	x.taskName = max(x.taskName, d.taskName)
	x.mode = max(x.mode, d.mode)
	x.province = max(x.province, d.province)
	x.carrier = max(x.carrier, d.carrier)
	x.sum += d.sum
	x.count += d.count
	x.curveSum += d.curveSum
	x.curveCount += d.curveCount
	x.attempts += d.attempts
	x.successes += d.successes
	x.reports += d.reports
	x.available += d.available
	x.first = min(x.first, d.first)
	x.last = max(x.last, d.last)
	if d.minimum.Valid {
		addLatencyExtrema(&x.minimum, &x.maximum, &x.minAt, &x.maxAt, d.minimum.Int64, d.minAt.Int64)
		addLatencyExtrema(&x.minimum, &x.maximum, &x.minAt, &x.maxAt, d.maximum.Int64, d.maxAt.Int64)
	}
	if d.curveMin.Valid {
		addLatencyExtrema(&x.curveMin, &x.curveMax, &x.curveMinAt, &x.curveMaxAt, d.curveMin.Int64, d.curveMinAt.Int64)
		addLatencyExtrema(&x.curveMin, &x.curveMax, &x.curveMinAt, &x.curveMaxAt, d.curveMax.Int64, d.curveMaxAt.Int64)
	}
	g.revisions[d.key.revision] = true
	a.revisions[d.key.revision] = true
	if d.basis != "measurement_v1" {
		a.legacy = true
	}
	if last := time.Unix(0, d.last); last.After(a.observed) {
		a.observed = last
	}
}
func (a *latencyReadAccumulator) result() LatencyBucketResult {
	result := LatencyBucketResult{Points: []model.ServerRegionalLatencyPoint{}, Stats: []model.LatencyProbeTargetStat{}, ObservedThrough: a.observed}
	type total struct {
		revisions  map[string]bool
		stat       model.LatencyProbeTargetStat
		sum, count int64
	}
	totals := map[string]*total{}
	groups := make([]*latencyReadGroup, 0, len(a.groups))
	for _, g := range a.groups {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].delta.key.start < groups[j].delta.key.start })
	for _, g := range groups {
		d := g.delta
		key := LatencyProbeTargetKey(d.kind, d.taskID, d.province, d.carrier)
		t := totals[key]
		if t == nil {
			t = &total{revisions: map[string]bool{}, stat: model.LatencyProbeTargetStat{Key: key}}
			totals[key] = t
		}
		for revision := range g.revisions {
			t.revisions[revision] = true
		}
		s := &t.stat
		s.Kind = max(s.Kind, d.kind)
		s.TaskID = max(s.TaskID, d.taskID)
		s.TaskName = max(s.TaskName, d.taskName)
		s.Mode = max(s.Mode, d.mode)
		s.Province = max(s.Province, d.province)
		s.Carrier = max(s.Carrier, d.carrier)
		if d.kind == "public" {
			s.TaskName = "公网探测"
		} else if s.TaskName == "" {
			s.TaskName = key
		}
		t.sum += d.sum
		t.count += d.count
		s.SampleCount += d.attempts
		s.SuccessCount += d.successes
		s.ReportCount += d.reports
		s.AvailableCount += d.available
		if d.curveMin.Valid && (s.MinMS == nil || d.curveMin.Int64 < *s.MinMS) {
			v := d.curveMin.Int64
			s.MinMS = &v
		}
		if d.curveMax.Valid && (s.MaxMS == nil || d.curveMax.Int64 > *s.MaxMS) {
			v := d.curveMax.Int64
			s.MaxMS = &v
		}
		at := time.Unix(d.key.start, 0).UTC()
		first := time.Unix(0, d.first).UTC()
		if d.curveCount > 0 {
			peak := float64(d.curveSum) / float64(d.curveCount)
			if s.PeakLatencyMS == nil || peak > *s.PeakLatencyMS {
				s.PeakLatencyMS = &peak
				s.PeakLatencyAt = &first
			}
		}
		loss := latencyProbeLossPercent(d.attempts, d.successes, d.reports, d.available)
		if loss != nil && (s.PeakLossPercent == nil || *loss > *s.PeakLossPercent) {
			s.PeakLossPercent = loss
			s.PeakLossAt = &first
		}
		if d.kind == "public" {
			if d.curveCount > 0 {
				result.PublicPoints = append(result.PublicPoints, model.ServerRegionalLatencyPoint{Kind: "public", Available: true, CheckedAt: at, LatencyMS: float64(d.curveSum) / float64(d.curveCount), MinLatencyMS: d.curveMin.Int64, MaxLatencyMS: d.curveMax.Int64, Count: d.curveCount})
			}
			if d.reports > d.available {
				result.Failures = append(result.Failures, LatencyFailurePoint{At: at, Count: d.reports - d.available})
			}
		} else if d.count > 0 {
			result.Points = append(result.Points, model.ServerRegionalLatencyPoint{Kind: "regional", TaskID: d.taskID, TaskName: d.taskName, Province: d.province, Carrier: d.carrier, Available: true, CheckedAt: at, LatencyMS: float64(d.sum) / float64(d.count), MinLatencyMS: d.minimum.Int64, MaxLatencyMS: d.maximum.Int64, Count: d.count})
		}
	}
	for _, t := range totals {
		t.stat.MeasurementRevisionCount = len(t.revisions)
		if t.count > 0 {
			v := float64(t.sum) / float64(t.count)
			t.stat.AvgMS = &v
		}
		if t.stat.MinMS != nil && t.stat.MaxMS != nil {
			v := float64(*t.stat.MaxMS - *t.stat.MinMS)
			t.stat.JitterMS = &v
		}
		attachLatencyProbeTargetRates(&t.stat)
		result.Stats = append(result.Stats, t.stat)
	}
	sort.Slice(result.Stats, func(i, j int) bool {
		if (result.Stats[i].Kind == "public") != (result.Stats[j].Kind == "public") {
			return result.Stats[i].Kind == "public"
		}
		if result.Stats[i].TaskName != result.Stats[j].TaskName {
			return result.Stats[i].TaskName < result.Stats[j].TaskName
		}
		return result.Stats[i].Key < result.Stats[j].Key
	})
	sort.Slice(result.Points, func(i, j int) bool {
		a, b := result.Points[i], result.Points[j]
		if !a.CheckedAt.Equal(b.CheckedAt) {
			return a.CheckedAt.Before(b.CheckedAt)
		}
		if a.TaskName != b.TaskName {
			return a.TaskName < b.TaskName
		}
		return a.TaskID < b.TaskID
	})
	return result
}

func readRawLatency(ctx context.Context, conn *sql.Conn, a *latencyReadAccumulator, query string, args []any, budget *int) error {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		*budget--
		if *budget < 0 {
			return ErrHistoryCoverage
		}
		var d latencyRollupDelta
		var available, latency int64
		var checked, host string
		var port int64
		if err := rows.Scan(&d.key.serverID, &d.kind, &d.taskID, &d.taskName, &d.probeID, &d.mode, &d.province, &d.carrier, &available, &latency, &d.attempts, &d.successes, &checked, &d.key.revision, &host, &port); err != nil {
			return err
		}
		at, err := time.Parse(time.RFC3339Nano, checked)
		if err != nil {
			return err
		}
		if at.Before(a.from) || !at.Before(a.to) {
			continue
		}
		d.first, d.last = at.UnixNano(), at.UnixNano()
		d.reports = 1
		d.available = available
		d.basis = "measurement_v1"
		if d.key.revision == "" {
			d.basis = "reported_endpoint_v1"
			semantics, _ := json.Marshal([]any{"reported_endpoint_v1", d.mode, strings.TrimSpace(host), port})
			d.key.revision = fmt.Sprintf("%x", sha256.Sum256(semantics))
		}
		if available == 1 && latency > 0 {
			d.curveSum = latency
			d.curveCount = 1
			addLatencyExtrema(&d.curveMin, &d.curveMax, &d.curveMinAt, &d.curveMaxAt, latency, at.UnixNano())
			if d.successes > 0 {
				d.sum = latency
				d.count = 1
				addLatencyExtrema(&d.minimum, &d.maximum, &d.minAt, &d.maxAt, latency, at.UnixNano())
			}
		}
		a.add(d, at)
		if a.overflow {
			return ErrLatencyPointBudget
		}
	}
	return rows.Err()
}

const rawLatencyReadColumns = `server_id,kind,task_id,task_name,probe_id,mode,province,carrier,available,latency_ms,sample_count,success_count,checked_at,measurement_revision,host,port`

// A manual deferred transaction bypasses the Store's IMMEDIATE write default.
// All summaries, watermarks and bounded supplements belong to this read snapshot.
func (s *Store) QueryLatencySummaryChart(ctx context.Context, serverID int64, from, to time.Time, interval time.Duration, options ...LatencySummaryReadOptions) (LatencyBucketResult, LatencyReadCoverage, error) {
	coverage := LatencyReadCoverage{Source: "summary"}
	opts := LatencySummaryReadOptions{}
	if len(options) > 0 {
		opts = options[0]
	}
	filter := ""
	filterArgs := []any{}
	if len(opts.TargetIDs) > 64 {
		return LatencyBucketResult{}, coverage, errors.New("invalid target filter")
	}
	if len(opts.TargetIDs) > 0 {
		for _, id := range opts.TargetIDs {
			if id <= 0 {
				return LatencyBucketResult{}, coverage, errors.New("invalid target filter")
			}
			filterArgs = append(filterArgs, id)
		}
		filter = " and (kind='public' or task_id in (" + strings.TrimSuffix(strings.Repeat("?,", len(filterArgs)), ",") + "))"
	}

	empty := LatencyBucketResult{}
	if interval < time.Minute || from.Nanosecond() != 0 || to.Nanosecond() != 0 || !to.After(from) || (to.Sub(from)+interval-1)/interval > 360 {
		return empty, coverage, errors.New("invalid summary chart window")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return empty, coverage, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return empty, coverage, err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	var live, backfill, floor int64
	var version int
	if err := conn.QueryRowContext(ctx, `select live_cursor,backfill_cursor,retention_floor,schema_version from latency_rollup_state where id=1`).Scan(&live, &backfill, &floor, &version); err != nil {
		return empty, coverage, err
	}
	a := newLatencyAccumulator(from, to, interval)
	budget := 50000
	raw := func(start, end time.Time, predicate string, extra ...any) error {
		args := []any{serverID, connectivityTimeBound(start), connectivityTimeBound(end)}
		args = append(args, extra...)
		args = append(args, filterArgs...)
		args = append(args, budget+1)
		return readRawLatency(ctx, conn, a, `select `+rawLatencyReadColumns+` from server_latency_probe_results where server_id=? and checked_at>=? and checked_at<? and kind in ('public','regional','custom') `+predicate+filter+` order by checked_at limit ?`, args, &budget)
	}
	if opts.ForceRaw || live < 0 || backfill > 0 || version != latencyRollupSchemaVersion || interval < 5*time.Minute {
		coverage.Source = "raw_bounded"
		if opts.ForceRaw || interval < 5*time.Minute {
			coverage.Source = "raw"
		} else {
			coverage.CatchingUp = true
		}
		if err := raw(from, to, ""); err != nil {
			return empty, coverage, err
		}
	} else {
		type span struct {
			from, to   int64
			resolution int64
		}
		spans := []span{}
		for at := from.Unix(); at < to.Unix(); {
			resolution := int64(0)
			end := min(to.Unix(), (at/300+1)*300)
			if at >= floor && at%3600 == 0 && to.Unix()-at >= 3600 && interval >= time.Hour && interval%time.Hour == 0 {
				resolution = 3600
				end = at + 3600
			} else if at >= floor && at%300 == 0 && to.Unix()-at >= 300 {
				resolution = 300
				end = at + 300
			}
			if len(spans) > 0 && spans[len(spans)-1].resolution == resolution && spans[len(spans)-1].to == at {
				spans[len(spans)-1].to = end
			} else {
				spans = append(spans, span{at, end, resolution})
			}
			at = end
		}
		for _, span := range spans {
			if span.resolution == 0 {
				coverage.Source = "mixed"
				if err := raw(time.Unix(span.from, 0), time.Unix(span.to, 0), "and id<=?", live); err != nil {
					return empty, coverage, err
				}
				continue
			}
			summaryArgs := []any{serverID, span.resolution, span.from, span.to, latencyRollupSchemaVersion}
			summaryArgs = append(summaryArgs, filterArgs...)
			rows, err := conn.QueryContext(ctx, `select server_id,series_key,measurement_revision,revision_basis,kind,task_id,probe_id,mode,task_name,province,carrier,bucket_start,latency_sum,latency_value_count,latency_min,latency_max,latency_min_at,latency_max_at,curve_sum,curve_value_count,curve_min,curve_max,curve_min_at,curve_max_at,attempt_count,success_count,report_count,available_report_count,first_sample_at,last_sample_at from latency_rollup_buckets indexed by sqlite_autoindex_latency_rollup_buckets_1 where server_id=? and resolution_seconds=? and bucket_start>=? and bucket_start<? and summary_schema_version=?`+filter+` order by bucket_start limit 12001`, summaryArgs...)
			if err != nil {
				return empty, coverage, err
			}
			count := 0
			for rows.Next() {
				count++
				if count > 12000 {
					rows.Close()
					return empty, coverage, ErrLatencyPointBudget
				}
				var d latencyRollupDelta
				if err := rows.Scan(&d.key.serverID, &d.key.series, &d.key.revision, &d.basis, &d.kind, &d.taskID, &d.probeID, &d.mode, &d.taskName, &d.province, &d.carrier, &d.key.start, &d.sum, &d.count, &d.minimum, &d.maximum, &d.minAt, &d.maxAt, &d.curveSum, &d.curveCount, &d.curveMin, &d.curveMax, &d.curveMinAt, &d.curveMaxAt, &d.attempts, &d.successes, &d.reports, &d.available, &d.first, &d.last); err != nil {
					rows.Close()
					return empty, coverage, err
				}
				a.add(d, time.Unix(d.key.start, 0))
				if a.overflow {
					rows.Close()
					return empty, coverage, ErrLatencyPointBudget
				}
			}
			readErr := rows.Err()
			rows.Close()
			if readErr != nil {
				return empty, coverage, readErr
			}
		}
		// Bound pending work globally before applying the server/window filter.
		var pending int
		if err := conn.QueryRowContext(ctx, `select count(*) from (select id from server_latency_probe_results where id>? order by id limit 5001)`, live).Scan(&pending); err != nil {
			return empty, coverage, err
		}
		if pending > 5000 {
			return empty, coverage, ErrHistoryCoverage
		}
		query := `select ` + rawLatencyReadColumns + ` from (select * from server_latency_probe_results where id>? order by id limit 5000) where server_id=? and checked_at>=? and checked_at<? and kind in ('public','regional','custom')` + filter
		pendingArgs := []any{live, serverID, connectivityTimeBound(from), connectivityTimeBound(to)}
		pendingArgs = append(pendingArgs, filterArgs...)
		beforePending := budget
		if err := readRawLatency(ctx, conn, a, query, pendingArgs, &budget); err != nil {
			return empty, coverage, err
		}
		if budget < beforePending {
			coverage.Source = "mixed"
		}
	}
	legacy, err := s.queryLegacyLatencyBuckets(ctx, conn, serverID, from, to, interval, true)
	if err != nil {
		return empty, coverage, err
	}
	coverage.Legacy = legacy
	var legacyCursor int64
	if err := conn.QueryRowContext(ctx, `select cursor from latency_legacy_progress where id=1`).Scan(&legacyCursor); err != nil {
		return empty, coverage, err
	}
	if legacyCursor != 0 && !opts.ForceRaw && interval >= 5*time.Minute {
		coverage.CatchingUp = true
		if coverage.Source == "summary" {
			coverage.Source = "mixed"
		}
	}
	result := a.result()
	coverage.LegacyRevision = a.legacy || len(legacy.PublicPoints)+len(legacy.Failures) > 0
	coverage.RevisionCount = len(a.revisions)
	var first sql.NullString
	if err := conn.QueryRowContext(ctx, `select min(checked_at) from (select min(checked_at) checked_at from server_latency_probe_results where server_id=? and kind='regional' union all select min(checked_at) from server_latency_probe_results where server_id=? and kind='custom')`, serverID, serverID).Scan(&first); err != nil {
		return empty, coverage, err
	}
	if first.Valid {
		at := parseTime(first.String)
		result.DataStart = &at
	}
	return result, coverage, nil
}
