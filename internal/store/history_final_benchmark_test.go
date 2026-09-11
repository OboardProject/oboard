//go:build unix

package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func BenchmarkCompletedHistory(b *testing.B) {
	dimension := func(key string, fallback int) int {
		value := os.Getenv(key)
		if value == "" {
			return fallback
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			b.Fatal("invalid fixture dimension")
		}
		return n
	}
	servers, targets, days := dimension("OBOARD_LATENCY_BENCH_SERVERS", 1), dimension("OBOARD_LATENCY_BENCH_TARGETS", 8), dimension("OBOARD_LATENCY_BENCH_DAYS", 30)
	path := os.Getenv("OBOARD_HISTORY_BENCH_DB")
	if path == "" {
		path = filepath.Join(b.TempDir(), "completed.sqlite")
	} else {
		absolute, e := filepath.Abs(path)
		if e != nil || !strings.Contains(absolute, string(filepath.Separator)+".cache"+string(filepath.Separator)+"history-final"+string(filepath.Separator)) {
			b.Fatal("benchmark DB must be in the task cache")
		}
		path = absolute
	}
	db, err := Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.SetSetting(ctx, ServerMonitoringRetentionDaysSetting, "30"); err != nil {
		b.Fatal(err)
	}
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Duration(days) * 24 * time.Hour)
	var selected int64
	for server := 0; server < servers; server++ {
		node := &model.Server{Name: fmt.Sprintf("fixture-%d", server)}
		err := db.db.QueryRowContext(ctx, `select id from servers where name=?`, node.Name).Scan(&node.ID)
		if err == sql.ErrNoRows {
			err = db.CreateServer(ctx, node)
		}
		if err != nil {
			b.Fatal(err)
		}
		selected = node.ID
		var existing int
		if err := db.db.QueryRowContext(ctx, `select count(*) from server_latency_probe_results where server_id=?`, node.ID).Scan(&existing); err != nil {
			b.Fatal(err)
		}
		if existing == targets*days*1440 {
			continue
		}
		if existing != 0 {
			b.Fatal("fixture dimensions differ; use a new task database")
		}

		_, err = db.db.ExecContext(ctx, `with recursive samples(n) as (select 0 union all select n+1 from samples where n+1<?)
   insert into server_latency_probe_results(server_id,report_id,resource_version,probe_id,kind,task_id,task_name,mode,province,carrier,host,ip,port,available,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,checked_at,created_at)
   select ?,cast(n as text),'benchmark','target-'||(n%?),'custom',1+n%?,'target-'||(n%?),'tcp','','','example.test','',443,n%11<>0,case when n%11=0 then 0 else 10+n%120 end,0,0,0,3,case when n%11=0 then 0 else 3 end,'',strftime('%Y-%m-%dT%H:%M:%SZ','2026-08-01','+'||(n/?)||' minutes'),'2026-09-01T00:00:00Z' from samples`, targets*days*1440, node.ID, targets, targets, targets, targets)
		if err != nil {
			b.Fatal(err)
		}
		if (server+1)%10 == 0 {
			b.Logf("generated %d/%d servers", server+1, servers)
		}
	}
	if _, err := db.RunLatencyRollupBatch(ctx, to, 500); err != nil {
		b.Fatal(err)
	}
	batches := 0
	for {
		state, err := db.LatencyRollupState(ctx)
		if err != nil {
			b.Fatal(err)
		}
		if state.BackfillCursor == 0 {
			break
		}
		if _, err := db.RunLatencyBackfillBatch(ctx, to, 500); err != nil {
			b.Fatal(err)
		}
		batches++
	}
	for {
		count, err := db.RunLatencyLegacyBackfill(ctx, 500)
		if err != nil {
			b.Fatal(err)
		}
		if count == 0 {
			break
		}
	}
	b.Logf("fixture servers=%d targets=%d days=%d rows=%d backfill_batches=%d; setup excluded", servers, targets, days, servers*targets*days*1440, batches)
	for _, window := range []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour} {
		if window > to.Sub(from) {
			continue
		}
		start := to.Add(-window)
		resolution := 5 * time.Minute
		if window > 24*time.Hour {
			resolution = time.Hour
		}
		if window > 7*24*time.Hour {
			resolution = 2 * time.Hour
		}
		raw, err := db.QueryLatencyBuckets(ctx, selected, start, to, resolution)
		if err != nil {
			b.Fatal(err)
		}
		projected, coverage, err := db.QueryLatencySummaryChart(ctx, selected, start, to, resolution)
		if err != nil {
			b.Fatal(err)
		}
		for i := range projected.Stats {
			projected.Stats[i].MeasurementRevisionCount = 0
		}
		if !reflect.DeepEqual(raw.Points, projected.Points) || !reflect.DeepEqual(raw.Stats, projected.Stats) {
			b.Fatalf("statistics differ for %v: raw=%+v summary=%+v", window, raw.Stats, projected.Stats)
		}
		if coverage.Source != "summary" {
			b.Fatalf("not using completed summaries: %+v", coverage)
		}
		b.Run(fmt.Sprintf("%dh/raw", int(window.Hours())), func(b *testing.B) {
			b.ReportAllocs()
			cpu := historyProcessCPU()
			samples := []time.Duration{}
			for b.Loop() {
				started := time.Now()
				if _, err := db.QueryLatencyBuckets(ctx, selected, start, to, resolution); err != nil {
					b.Fatal(err)
				}
				samples = append(samples, time.Since(started))
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			b.ReportMetric(float64((historyProcessCPU()-cpu).Nanoseconds())/float64(b.N), "cpu_ns/op")
			b.ReportMetric(float64(samples[(len(samples)*95+99)/100-1].Nanoseconds()), "p95_ns")
		})
		b.Run(fmt.Sprintf("%dh/summary", int(window.Hours())), func(b *testing.B) {
			b.ReportAllocs()
			cpu := historyProcessCPU()
			samples := []time.Duration{}
			for b.Loop() {
				started := time.Now()
				if _, _, err := db.QueryLatencySummaryChart(ctx, selected, start, to, resolution); err != nil {
					b.Fatal(err)
				}
				samples = append(samples, time.Since(started))
			}
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			b.ReportMetric(float64((historyProcessCPU()-cpu).Nanoseconds())/float64(b.N), "cpu_ns/op")
			b.ReportMetric(float64(samples[(len(samples)*95+99)/100-1].Nanoseconds()), "p95_ns")
		})
	}
}
