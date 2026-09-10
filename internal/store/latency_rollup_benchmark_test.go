package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func BenchmarkLatencyRollupBatch(b *testing.B) {
	for _, servers := range []int{1, 100} {
		b.Run(fmt.Sprintf("servers_%d", servers), func(b *testing.B) {
			ctx := context.Background()
			db, err := Open(filepath.Join(b.TempDir(), "rollup.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			ids := make([]int64, servers)
			for i := range ids {
				node := &model.Server{Name: fmt.Sprint(i)}
				if err := db.CreateServer(ctx, node); err != nil {
					b.Fatal(err)
				}
				ids[i] = node.ID
			}
			if _, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500); err != nil {
				b.Fatal(err)
			}
			base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
			for i := 0; i < 500; i++ {
				report := model.LatencyProbeResultReport{ReportID: fmt.Sprint(i), ResourceVersion: "fixture", CheckedAt: base.Add(time.Duration(i/8) * time.Minute), Items: []model.LatencyProbeResult{{ProbeID: fmt.Sprint(i % 8), Kind: "custom", Mode: "tcp", Host: "example.net", Available: true, LatencyMS: 10, SampleCount: 3, SuccessCount: 3}}}
				if err := db.SaveLatencyProbeResults(ctx, ids[(i/8)%servers], report); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				if _, err := db.db.Exec(`delete from latency_rollup_buckets`); err != nil {
					b.Fatal(err)
				}
				if _, err := db.db.Exec(`update latency_rollup_state set live_cursor=0`); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				result, err := db.RunLatencyRollupBatch(ctx, time.Now(), 500)
				if err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(result.Processed), "rows/op")
				b.ReportMetric(float64(result.Buckets), "buckets/op")
				b.ReportMetric(float64(result.TransactionDuration.Nanoseconds()), "transaction_ns")
			}
		})
	}
}
