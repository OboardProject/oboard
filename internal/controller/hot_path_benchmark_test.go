package controller

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/perfload"
)

// percentileNearestRank returns the nearest-rank percentile for a sorted sample.
func percentileNearestRank(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := int((p/100)*float64(len(sorted)-1) + 0.5)
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

type hotPathBenchmarkReport struct {
	Commit           string            `json:"commit"`
	Spec             perfload.Spec     `json:"spec"`
	SynchronousMode  string            `json:"synchronous_mode"`
	Samples          int               `json:"samples"`
	PackageHitP50MS  float64           `json:"package_hit_p50_ms"`
	PackageHitP95MS  float64           `json:"package_hit_p95_ms"`
	LeaseHitP50MS    float64           `json:"lease_hit_p50_ms"`
	LeaseHitP95MS    float64           `json:"lease_hit_p95_ms"`
	AuditHitP50MS    float64           `json:"audit_hit_p50_ms"`
	AuditHitP95MS    float64           `json:"audit_hit_p95_ms"`
	GeneratedAt      time.Time         `json:"generated_at"`
	Notes            []string          `json:"notes"`
}

// TestHotPathBenchmarkSmallSpec produces a machine-readable latency report for
// the canonical small Spec. It is a regression harness, not a fleet soak.
func TestHotPathBenchmarkSmallSpec(t *testing.T) {
	spec := perfload.SpecFor(perfload.ScaleSmall)
	ctx := t.Context()
	db, srv, server, _, _ := hotPathFixture(t)

	// Warm caches once so the measured loop is the hit path.
	if _, _, err := srv.currentRuntimeUserPackage(ctx, *server, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.currentAuthorizationLease(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := srv.auditOverviewData(ctx, 24); err != nil {
		t.Fatal(err)
	}

	const samples = 40
	packageSamples := make([]time.Duration, 0, samples)
	leaseSamples := make([]time.Duration, 0, samples)
	auditSamples := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		if _, _, err := srv.currentRuntimeUserPackage(ctx, *server, 1); err != nil {
			t.Fatal(err)
		}
		packageSamples = append(packageSamples, time.Since(start))

		start = time.Now()
		if _, err := srv.currentAuthorizationLease(ctx, server.ID); err != nil {
			t.Fatal(err)
		}
		leaseSamples = append(leaseSamples, time.Since(start))

		start = time.Now()
		if _, _, _, err := srv.auditOverviewData(ctx, 24); err != nil {
			t.Fatal(err)
		}
		auditSamples = append(auditSamples, time.Since(start))
	}
	sort.Slice(packageSamples, func(i, j int) bool { return packageSamples[i] < packageSamples[j] })
	sort.Slice(leaseSamples, func(i, j int) bool { return leaseSamples[i] < leaseSamples[j] })
	sort.Slice(auditSamples, func(i, j int) bool { return auditSamples[i] < auditSamples[j] })

	report := hotPathBenchmarkReport{
		Commit:          "local-test",
		Spec:            spec,
		SynchronousMode: "FULL",
		Samples:         samples,
		PackageHitP50MS: float64(percentileNearestRank(packageSamples, 50).Microseconds()) / 1000,
		PackageHitP95MS: float64(percentileNearestRank(packageSamples, 95).Microseconds()) / 1000,
		LeaseHitP50MS:   float64(percentileNearestRank(leaseSamples, 50).Microseconds()) / 1000,
		LeaseHitP95MS:   float64(percentileNearestRank(leaseSamples, 95).Microseconds()) / 1000,
		AuditHitP50MS:   float64(percentileNearestRank(auditSamples, 50).Microseconds()) / 1000,
		AuditHitP95MS:   float64(percentileNearestRank(auditSamples, 95).Microseconds()) / 1000,
		GeneratedAt:     time.Now().UTC(),
		Notes: []string{
			"Hit-path only after one warm build; cold builds are excluded.",
			"Fixture is one server/one user, not the full Spec population (population builders remain separate).",
			"Compare against NORMAL-mode historical numbers only when synchronous pragma is called out.",
			"SQL statements observed during reuse window: " + formatInt(int(db.SQLStatementCount())),
		},
	}
	outDir := filepath.Join("..", "..", "..", "dist", "test")
	_ = os.MkdirAll(outDir, 0o755)
	outPath := filepath.Join(outDir, "hot-path-benchmark-small.json")
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		t.Logf("could not write report to %s: %v", outPath, err)
	} else {
		t.Logf("wrote %s", outPath)
	}
	t.Logf("package_hit p50=%.2fms p95=%.2fms lease_hit p50=%.2fms p95=%.2fms audit_hit p50=%.2fms p95=%.2fms",
		report.PackageHitP50MS, report.PackageHitP95MS, report.LeaseHitP50MS, report.LeaseHitP95MS, report.AuditHitP50MS, report.AuditHitP95MS)

	// Soft CI gates for the tiny fixture: catch catastrophic regressions only.
	if report.PackageHitP95MS > 500 || report.LeaseHitP95MS > 500 || report.AuditHitP95MS > 1000 {
		t.Fatalf("hot-path p95 too high: %+v", report)
	}
}

func formatInt(v int) string {
	return strconvFormatInt(int64(v))
}

func strconvFormatInt(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	buf := make([]byte, 0, 20)
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
