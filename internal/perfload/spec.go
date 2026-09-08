package perfload

import (
	"fmt"
	"math/rand"
)

// Scale selects a reproducible fixture size for Controller load benchmarks.
type Scale string

const (
	ScaleSmall Scale = "small"
	ScaleMedium Scale = "medium"
	ScaleLarge Scale = "large"
)

// Spec is the fixed-seed population used by hot-path benchmarks and diagnostics.
type Spec struct {
	Name                string `json:"name"`
	Seed                int64  `json:"seed"`
	Servers             int    `json:"servers"`
	Users               int    `json:"users"`
	Plans               int    `json:"plans"`
	InboundsPerServer   int    `json:"inbounds_per_server"`
	PathsPerInbound     int    `json:"paths_per_inbound"`
	BindingsPerUser     int    `json:"bindings_per_user"`
	AuditReports        int    `json:"audit_reports"`
	AuditHotUserShare   float64 `json:"audit_hot_user_share"`
	TrafficBatchSize    int    `json:"traffic_batch_size"`
	AdminConcurrency    int    `json:"admin_concurrency"`
}

// SpecFor returns the canonical small/medium/large load shapes. Seeds are fixed
// so the same environment can reproduce machine-readable results.
func SpecFor(scale Scale) Spec {
	switch scale {
	case ScaleMedium:
		return Spec{
			Name: "medium", Seed: 20260908, Servers: 40, Users: 400, Plans: 8,
			InboundsPerServer: 3, PathsPerInbound: 2, BindingsPerUser: 2,
			AuditReports: 50_000, AuditHotUserShare: 0.15, TrafficBatchSize: 64, AdminConcurrency: 8,
		}
	case ScaleLarge:
		return Spec{
			Name: "large", Seed: 20260908, Servers: 120, Users: 2000, Plans: 16,
			InboundsPerServer: 4, PathsPerInbound: 3, BindingsPerUser: 3,
			AuditReports: 250_000, AuditHotUserShare: 0.10, TrafficBatchSize: 128, AdminConcurrency: 16,
		}
	default:
		return Spec{
			Name: "small", Seed: 20260908, Servers: 8, Users: 40, Plans: 3,
			InboundsPerServer: 2, PathsPerInbound: 1, BindingsPerUser: 1,
			AuditReports: 2_000, AuditHotUserShare: 0.20, TrafficBatchSize: 16, AdminConcurrency: 2,
		}
	}
}

// HotUserIDs returns the concentrated user set that owns most audit/traffic mass.
func (s Spec) HotUserIDs() []int64 {
	rng := rand.New(rand.NewSource(s.Seed))
	count := int(float64(s.Users) * s.AuditHotUserShare)
	if count < 1 {
		count = 1
	}
	ids := make([]int64, 0, count)
	seen := map[int64]struct{}{}
	for len(ids) < count {
		id := int64(1 + rng.Intn(s.Users))
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// Describe returns a short human-readable summary for reports.
func (s Spec) Describe() string {
	return fmt.Sprintf("%s seed=%d servers=%d users=%d audit_reports=%d batch=%d admin=%d",
		s.Name, s.Seed, s.Servers, s.Users, s.AuditReports, s.TrafficBatchSize, s.AdminConcurrency)
}
