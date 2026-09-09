package controller

import (
 "testing"
 "time"
)

func BenchmarkConnectivityBuckets(b *testing.B) {
 window, _ := parseConnectivityWindow("30d", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
 segments := make([]connectivitySegment, 43200)
 for i := range segments {
  start := window.From.Add(time.Duration(i)*time.Minute)
  segments[i] = connectivitySegment{start: start, end: start.Add(time.Minute), availability: connectivityAvailability(i%3)}
 }
 b.ReportAllocs()
 b.ResetTimer()
 for b.Loop() { buildConnectivityBuckets(window, segments, nil) }
}
