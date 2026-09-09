package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func BenchmarkLatencyHistory(b *testing.B) {
	dimension := func(key string, fallback int) int {
		text := os.Getenv(key)
		if text == "" {
			return fallback
		}
		n, err := strconv.Atoi(text)
		if err != nil || n < 1 {
			b.Fatalf("invalid %s", key)
		}
		return n
	}
	servers := dimension("OBOARD_LATENCY_BENCH_SERVERS", 1)
	targets := dimension("OBOARD_LATENCY_BENCH_TARGETS", 8)
	days := dimension("OBOARD_LATENCY_BENCH_DAYS", 30)
	db, err := Open(filepath.Join(b.TempDir(), "latency.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	var selected int64
	for i := 0; i < servers; i++ {
		server := &model.Server{Name: fmt.Sprintf("latency-benchmark-%d", i)}
		if err := db.CreateServer(ctx, server); err != nil {
			b.Fatal(err)
		}
		selected = server.ID
		_, err := db.db.ExecContext(ctx, `with recursive samples(n) as (select 0 union all select n+1 from samples where n+1<?)
   insert into server_latency_probe_results(server_id,report_id,resource_version,probe_id,kind,task_id,task_name,mode,province,carrier,host,ip,port,available,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,checked_at,created_at)
   select ?,cast(n as text),'benchmark','target-'||(n%?),'custom',1+n%?,'target-'||(n%?),'tcp','','','example.test','',443,n%11<>0,case when n%11=0 then 0 else 10+n%120 end,0,0,0,3,case when n%11=0 then 0 else 3 end,'',strftime('%Y-%m-%dT%H:%M:%SZ','2026-08-01','+'||(n/?)||' minutes'),'2026-09-01T00:00:00Z' from samples`, targets*days*1440, server.ID, targets, targets, targets, targets)
		if err != nil {
			b.Fatal(err)
		}
	}
	b.Logf("fixture: servers=%d targets=%d days=%d rows=%d; returned rows are not scanned rows", servers, targets, days, servers*targets*days*1440)
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Duration(days) * 24 * time.Hour)
	bucket := time.Duration((days*1440+359)/360) * time.Minute

	rows, err := db.db.QueryContext(ctx, "explain query plan "+latencyBucketsSQL, from.Format(time.RFC3339Nano), int64(bucket/time.Second), selected, from.Format(time.RFC3339Nano), to.Format(time.RFC3339Nano))
	if err != nil {
		b.Fatal(err)
	}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			b.Fatal(err)
		}
		b.Logf("combined plan: %s", detail)
	}
	if err := rows.Err(); err != nil {
		b.Fatal(err)
	}
	rows.Close()
	originalPoints, start, err := db.benchmarkOriginalRegionalPoints(ctx, selected, from, to, bucket)
	if err != nil {
		b.Fatal(err)
	}
	originalStats, err := db.benchmarkOriginalTargetStats(ctx, selected, from, to, bucket)
	if err != nil {
		b.Fatal(err)
	}
	combined, err := db.QueryLatencyBuckets(ctx, selected, from, to, bucket)
	if err != nil {
		b.Fatal(err)
	}
	if !reflect.DeepEqual(originalPoints, combined.Points) || !reflect.DeepEqual(originalStats, combined.Stats) || !reflect.DeepEqual(start, combined.DataStart) {
		b.Fatal("combined query differs from original fixture results")
	}
	b.Logf("output: points=%d targets=%d", len(combined.Points), len(combined.Stats))
	b.Run("original", func(b *testing.B) {
		before := db.db.stmts.Load()
		b.ReportAllocs()
		for b.Loop() {
			if _, _, err := db.benchmarkOriginalRegionalPoints(ctx, selected, from, to, bucket); err != nil {
				b.Fatal(err)
			}
			if _, err := db.benchmarkOriginalTargetStats(ctx, selected, from, to, bucket); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(db.db.stmts.Load()-before)/float64(b.N), "statements/op")
	})
	b.Run("combined", func(b *testing.B) {
		before := db.db.stmts.Load()
		b.ReportAllocs()
		for b.Loop() {
			if _, err := db.QueryLatencyBuckets(ctx, selected, from, to, bucket); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(db.db.stmts.Load()-before)/float64(b.N), "statements/op")
	})
}

func (s *Store) benchmarkOriginalRegionalPoints(ctx context.Context, serverID int64, from, to time.Time, bucket time.Duration) ([]model.ServerRegionalLatencyPoint, *time.Time, error) {
	from = from.UTC()
	to = to.UTC()
	if serverID <= 0 || from.IsZero() || to.IsZero() || !to.After(from) || bucket < time.Second {
		return nil, nil, errors.New("invalid regional latency point query")
	}
	bucketSeconds := int64(bucket / time.Second)
	bucketCount := int64((to.Sub(from) + bucket - 1) / bucket)
	if bucketSeconds <= 0 || bucketCount > maxRegionalLatencyPointBuckets {
		return nil, nil, errors.New("regional latency point query exceeds 360 buckets")
	}

	var dataStartText sql.NullString
	if err := s.db.QueryRowContext(ctx, `select min(checked_at) from (select min(checked_at) as checked_at from server_latency_probe_results where server_id=? and kind='regional' union all select min(checked_at) from server_latency_probe_results where server_id=? and kind='custom')`, serverID, serverID).Scan(&dataStartText); err != nil && err != sql.ErrNoRows {
		return nil, nil, err
	}
	var dataStart *time.Time
	if dataStartText.Valid && dataStartText.String != "" {
		parsed := parseTime(dataStartText.String)
		dataStart = &parsed
	}

	fromText := from.Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `
		with filtered as (
			select task_id,case when trim(task_name)<>'' then task_name else province||' · '||carrier end as task_name,province,carrier,cast((unixepoch(checked_at)-unixepoch(?))/? as integer) as bucket_index,latency_ms
			from server_latency_probe_results
			where server_id=? and kind in ('regional','custom') and available=1 and success_count>0 and latency_ms>0 and checked_at>=? and checked_at<?
		)
		select task_id,task_name,province,carrier,bucket_index,avg(latency_ms),min(latency_ms),max(latency_ms),count(*)
		from filtered
		where bucket_index>=0 and bucket_index<?
		group by task_id,task_name,bucket_index
		order by bucket_index,task_name,task_id`, fromText, bucketSeconds, serverID, fromText, to.Format(time.RFC3339Nano), bucketCount)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	points := make([]model.ServerRegionalLatencyPoint, 0)
	for rows.Next() {
		var point model.ServerRegionalLatencyPoint
		var bucketIndex int64
		if err := rows.Scan(&point.TaskID, &point.TaskName, &point.Province, &point.Carrier, &bucketIndex, &point.LatencyMS, &point.MinLatencyMS, &point.MaxLatencyMS, &point.Count); err != nil {
			return nil, nil, err
		}
		point.Kind = "regional"
		point.Available = true
		point.CheckedAt = from.Add(time.Duration(bucketIndex) * bucket)
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return points, dataStart, nil
}

func (s *Store) benchmarkOriginalTargetStats(ctx context.Context, serverID int64, from, to time.Time, bucket time.Duration) ([]model.LatencyProbeTargetStat, error) {
	from = from.UTC()
	to = to.UTC()
	if serverID <= 0 || from.IsZero() || to.IsZero() || !to.After(from) || bucket < time.Second {
		return nil, errors.New("invalid latency probe target stat query")
	}
	bucketSeconds := int64(bucket / time.Second)
	bucketCount := int64((to.Sub(from) + bucket - 1) / bucket)
	if bucketSeconds <= 0 || bucketCount > maxRegionalLatencyPointBuckets {
		return nil, errors.New("latency probe target stat query exceeds 360 buckets")
	}

	fromText := from.Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `
		select
			case when kind='public' then 'public' when task_id>0 then 'task_'||task_id else trim(province)||' · '||trim(carrier) end as target_key,
			cast((unixepoch(checked_at)-unixepoch(?))/? as integer) as bucket_index,
			max(kind), max(task_id),
			max(case when kind='public' then '公网探测' when trim(task_name)<>'' then task_name else trim(province)||' · '||trim(carrier) end),
			max(mode), max(province), max(carrier),
			sum(case when available=1 and success_count>0 and latency_ms>0 then latency_ms else 0 end),
			count(case when available=1 and success_count>0 and latency_ms>0 then 1 end),
			min(case when available=1 and latency_ms>0 then latency_ms end),
			max(case when available=1 and latency_ms>0 then latency_ms end),
			sum(sample_count), sum(success_count), count(*),
			sum(case when available=1 then 1 else 0 end),
			avg(case when available=1 and latency_ms>0 then latency_ms end), min(checked_at)
		from server_latency_probe_results
		where server_id=? and checked_at>=? and checked_at<? and kind in ('public','regional','custom')
		group by target_key, bucket_index
		order by target_key, bucket_index`, fromText, bucketSeconds, serverID, fromText, to.Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type targetTotal struct {
		stat                     model.LatencyProbeTargetStat
		latencySum, latencyCount int64
	}
	byKey := make(map[string]*targetTotal)
	for rows.Next() {
		var stat model.LatencyProbeTargetStat
		var bucketIndex, latencySum, latencyCount int64
		var minMS, maxMS sql.NullInt64
		var peakMS sql.NullFloat64
		var checkedAt string
		if err := rows.Scan(&stat.Key, &bucketIndex, &stat.Kind, &stat.TaskID, &stat.TaskName, &stat.Mode, &stat.Province, &stat.Carrier,
			&latencySum, &latencyCount, &minMS, &maxMS, &stat.SampleCount, &stat.SuccessCount, &stat.ReportCount, &stat.AvailableCount, &peakMS, &checkedAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(stat.Key) == "" || stat.Key == " · " {
			continue
		}
		total := byKey[stat.Key]
		if total == nil {
			total = &targetTotal{stat: model.LatencyProbeTargetStat{Key: stat.Key}}
			byKey[stat.Key] = total
		}
		item := &total.stat
		item.Kind = max(item.Kind, stat.Kind)
		item.TaskID = max(item.TaskID, stat.TaskID)
		item.TaskName = max(item.TaskName, stat.TaskName)
		item.Mode = max(item.Mode, stat.Mode)
		item.Province = max(item.Province, stat.Province)
		item.Carrier = max(item.Carrier, stat.Carrier)
		total.latencySum += latencySum
		total.latencyCount += latencyCount
		item.SampleCount += stat.SampleCount
		item.SuccessCount += stat.SuccessCount
		item.ReportCount += stat.ReportCount
		item.AvailableCount += stat.AvailableCount
		if minMS.Valid && (item.MinMS == nil || minMS.Int64 < *item.MinMS) {
			value := minMS.Int64
			item.MinMS = &value
		}
		if maxMS.Valid && (item.MaxMS == nil || maxMS.Int64 > *item.MaxMS) {
			value := maxMS.Int64
			item.MaxMS = &value
		}
		if bucketIndex < 0 || bucketIndex >= bucketCount {
			continue
		}
		at := parseTime(checkedAt)
		if peakMS.Valid && (item.PeakLatencyMS == nil || peakMS.Float64 > *item.PeakLatencyMS) {
			value := peakMS.Float64
			item.PeakLatencyMS = &value
			item.PeakLatencyAt = &at
		}
		loss := latencyProbeLossPercent(stat.SampleCount, stat.SuccessCount, stat.ReportCount, stat.AvailableCount)
		if loss != nil && (item.PeakLossPercent == nil || *loss > *item.PeakLossPercent) {
			item.PeakLossPercent = loss
			item.PeakLossAt = &at
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stats := make([]model.LatencyProbeTargetStat, 0, len(byKey))
	for _, total := range byKey {
		stat := total.stat
		if total.latencyCount > 0 {
			average := float64(total.latencySum) / float64(total.latencyCount)
			stat.AvgMS = &average
		}
		if stat.MinMS != nil && stat.MaxMS != nil {
			jitter := float64(*stat.MaxMS - *stat.MinMS)
			stat.JitterMS = &jitter
		}
		attachLatencyProbeTargetRates(&stat)
		stats = append(stats, stat)
	}
	sort.Slice(stats, func(i, j int) bool {
		if (stats[i].Kind == "public") != (stats[j].Kind == "public") {
			return stats[i].Kind == "public"
		}
		if stats[i].TaskName != stats[j].TaskName {
			return stats[i].TaskName < stats[j].TaskName
		}
		return stats[i].Key < stats[j].Key
	})
	return stats, nil
}
