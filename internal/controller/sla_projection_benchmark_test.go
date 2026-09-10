package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func BenchmarkSLAProjectionBatch(b *testing.B) {
	for _, samples := range []int{0, 120} {
		b.Run(fmt.Sprintf("events_per_hour_%d", samples), func(b *testing.B) {
			ctx := context.Background()
			db, err := store.Open(filepath.Join(b.TempDir(), "sla.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			node := &model.Server{Name: "benchmark"}
			if err := db.CreateServer(ctx, node); err != nil {
				b.Fatal(err)
			}
			base := time.Now().UTC().Truncate(5 * time.Minute).Add(5 * time.Minute)
			if err := db.RecordControllerConnectionEvent(ctx, node.ID, true, base.Add(-time.Minute)); err != nil {
				b.Fatal(err)
			}
			for hour := 0; hour < 12; hour++ {
				for i := 0; i < samples; i++ {
					report := model.LatencyProbeResultReport{ReportID: fmt.Sprintf("%d-%d", hour, i), ResourceVersion: "test", CheckedAt: base.Add(time.Duration(hour)*time.Hour + time.Duration(i)*30*time.Second + time.Second), Items: []model.LatencyProbeResult{{ProbeID: "public", Kind: "public", Mode: "tcp", Host: "example.net", Available: i%20 != 0, LatencyMS: 20, SampleCount: 3, SuccessCount: 3}}}
					if err := db.SaveLatencyProbeResults(ctx, node.ID, report); err != nil {
						b.Fatal(err)
					}
				}
			}
			if _, err := db.RunSLAProjectionBatch(ctx, base, 500, buildSLAProjection); err != nil {
				b.Fatal(err)
			}
			at := base
			var totalEvents, totalBuckets int
			b.ReportAllocs()
			for b.Loop() {
				at = at.Add(time.Hour)
				result, err := db.RunSLAProjectionBatch(ctx, at, 500, buildSLAProjection)
				if err != nil {
					b.Fatal(err)
				}
				totalEvents += result.Events
				totalBuckets += result.Buckets
			}
			b.ReportMetric(float64(totalEvents)/float64(b.N), "events/op")
			b.ReportMetric(float64(totalBuckets)/float64(b.N), "buckets/op")
		})
	}
}
