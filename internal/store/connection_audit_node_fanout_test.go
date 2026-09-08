package store

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func connectionAuditNodeFanoutQuadratic(reports []model.ConnectionAuditReport) int {
	byIdentity := map[string][]model.ConnectionAuditReport{}
	for _, report := range reports {
		if report.InternalProbe || report.ProbeState == "confirmed" || report.ProbeState == "candidate" || report.ConnectionCount <= 0 {
			continue
		}
		identity := connectionAuditReportIdentity(report)
		byIdentity[identity.key()] = append(byIdentity[identity.key()], report)
	}
	maximum := 0
	for _, items := range byIdentity {
		sort.SliceStable(items, func(i, j int) bool { return items[i].StartedAt.Before(items[j].StartedAt) })
		for left, right := 0, 0; right < len(items); right++ {
			for left <= right && items[right].StartedAt.Sub(items[left].StartedAt) > 10*time.Second {
				left++
			}
			nodes := map[string]struct{}{}
			for index := left; index <= right; index++ {
				nodes[connectionAuditNode(items[index])] = struct{}{}
			}
			maximum = max(maximum, len(nodes))
		}
	}
	return maximum
}

func TestConnectionAuditNodeFanoutMatchesQuadraticOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for round := 0; round < 64; round++ {
		n := 20 + rng.Intn(80)
		reports := make([]model.ConnectionAuditReport, 0, n)
		for i := 0; i < n; i++ {
			report := model.ConnectionAuditReport{
				UserID:          1,
				ServerID:        int64(1 + rng.Intn(12)),
				OutboundTag:     fmt.Sprintf("out-%d", rng.Intn(8)),
				StartedAt:       base.Add(time.Duration(rng.Intn(30_000)) * time.Millisecond),
				ConnectionCount: int64(1 + rng.Intn(3)),
				DeviceIDHash:    fmt.Sprintf("dev-%d", rng.Intn(4)),
				SourceIP:        fmt.Sprintf("203.0.113.%d", 10+rng.Intn(20)),
			}
			if rng.Intn(10) == 0 {
				report.ProbeState = "candidate"
			}
			if rng.Intn(20) == 0 {
				report.InternalProbe = true
			}
			if rng.Intn(15) == 0 {
				inboundID := int64(1 + rng.Intn(5))
				report.InboundID = &inboundID
			}
			reports = append(reports, report)
		}
		got := connectionAuditNodeFanout(reports)
		want := connectionAuditNodeFanoutQuadratic(reports)
		if got != want {
			t.Fatalf("round %d: fanout=%d quadratic=%d", round, got, want)
		}
	}
}

func TestConnectionAuditNodeFanoutTenSecondBoundary(t *testing.T) {
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inbound := int64(1)
	reports := []model.ConnectionAuditReport{
		{UserID: 1, ServerID: 1, InboundID: &inbound, OutboundTag: "a", StartedAt: base, ConnectionCount: 1, DeviceIDHash: "d1"},
		{UserID: 1, ServerID: 2, InboundID: &inbound, OutboundTag: "b", StartedAt: base.Add(10 * time.Second), ConnectionCount: 1, DeviceIDHash: "d1"},
		{UserID: 1, ServerID: 3, InboundID: &inbound, OutboundTag: "c", StartedAt: base.Add(10*time.Second + time.Nanosecond), ConnectionCount: 1, DeviceIDHash: "d1"},
	}
	if got := connectionAuditNodeFanout(reports[:2]); got != 2 {
		t.Fatalf("exact 10s boundary fanout=%d, want 2", got)
	}
	if got := connectionAuditNodeFanout(reports); got != 2 {
		t.Fatalf("10s+1ns boundary fanout=%d, want 2 (first report slid out)", got)
	}
}

func TestConnectionAuditNodeFanoutDenseComplexityTrend(t *testing.T) {
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	n := 1000
	quadraticScans := n * (n + 1) / 2
	linearTouches := 2 * n
	if linearTouches*10 > quadraticScans {
		t.Fatalf("complexity fixture invalid: linear=%d quadratic=%d", linearTouches, quadraticScans)
	}
	reports := make([]model.ConnectionAuditReport, n)
	for j := 0; j < n; j++ {
		inbound := int64(j%7 + 1)
		reports[j] = model.ConnectionAuditReport{
			UserID: 1, ServerID: int64(j%11 + 1), InboundID: &inbound,
			OutboundTag: fmt.Sprintf("o-%d", j%13), StartedAt: base.Add(time.Duration(j) * time.Millisecond),
			ConnectionCount: 1, DeviceIDHash: "dense",
		}
	}
	got := connectionAuditNodeFanout(reports)
	want := connectionAuditNodeFanoutQuadratic(reports)
	if got != want {
		t.Fatalf("dense fanout=%d quadratic=%d", got, want)
	}
}
