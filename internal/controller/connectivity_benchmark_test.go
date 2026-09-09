package controller

import (
	"testing"
	"time"
)

func BenchmarkConnectivityBuckets(b *testing.B) {
	window, _ := parseConnectivityWindow("30d", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	segments := make([]connectivitySegment, 43200)
	for i := range segments {
		start := window.From.Add(time.Duration(i) * time.Minute)
		segments[i] = connectivitySegment{start: start, end: start.Add(time.Minute), availability: connectivityAvailability(i % 3)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		buildConnectivityBuckets(window, segments, nil)
	}
}

func TestConnectivityBucketsMatchIntervalIntegration(t *testing.T) {
	window, _ := parseConnectivityWindow("24h", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	for _, step := range []time.Duration{17 * time.Second, 47 * time.Minute, 7 * time.Hour} {
		var segments []connectivitySegment
		for at, i := window.From.Add(-time.Hour), 0; at.Before(window.To.Add(time.Hour)); at, i = at.Add(step), i+1 {
			segments = append(segments, connectivitySegment{start: at, end: at.Add(step), availability: connectivityAvailability(i % 3)})
		}
		buckets := buildConnectivityBuckets(window, segments, nil)
		for _, bucket := range buckets {
			a, u, unknown := connectivityDurations(segments, bucket.StartAt, bucket.EndAt)
			if bucket.AvailableSeconds != a.Seconds() || bucket.UnavailableSeconds != u.Seconds() || bucket.UnknownSeconds != unknown.Seconds() {
				t.Fatalf("step %s bucket %s: %+v expected %s/%s/%s", step, bucket.StartAt, bucket, a, u, unknown)
			}
		}
	}
}
