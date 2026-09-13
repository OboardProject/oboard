package controller

import (
	"context"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// The fleet resource history the panel charts.
//
// Two endpoints serve it - the servers list with include_metrics and the
// servers page's page-data - so one panel load fetches the same hour twice, and
// an open panel re-fetches it on every poll. Building it is not cheap: it reads
// the latest samples for every server and then overlays public latency for the
// ones whose connectivity was not recorded in-band, and on a fleet-sized
// database those two queries were the largest single consumer of Controller CPU
// while a panel was open.
//
// Agents sample once a minute, so the answer changes at most that often no
// matter how many tabs are polling. The cache below collapses the repeats
// without changing what any caller receives.

// fleetMetricsWindow is the number of samples per server the panel charts. It
// is the value both endpoints already requested.
const fleetMetricsWindow = 60

// fleetMetricsTTL is well under the one-minute sampling interval, so a poll can
// never show a chart that is a sample behind what the database holds.
const fleetMetricsTTL = 15 * time.Second

type fleetMetricsEntry struct {
	builtAt time.Time
	samples []model.ServerMetricSample
}

type fleetMetricsBuild struct {
	done    chan struct{}
	samples []model.ServerMetricSample
	err     error
}

// fleetMetrics returns the panel's fleet resource history, rebuilding it only
// when the cached copy has aged past fleetMetricsTTL.
//
// Concurrent misses coalesce into one build: a panel load asks for this twice
// and several tabs poll independently, so without coalescing a cache miss would
// still run the query once per caller.
//
// The returned slice is shared and must be treated as read-only by callers;
// both current callers marshal it straight into a response.
func (s *Server) fleetMetrics(ctx context.Context) ([]model.ServerMetricSample, error) {
	if current := s.fleetMetricsCache.Load(); current != nil && time.Since(current.builtAt) < fleetMetricsTTL {
		return current.samples, nil
	}
	s.fleetMetricsMu.Lock()
	if current := s.fleetMetricsCache.Load(); current != nil && time.Since(current.builtAt) < fleetMetricsTTL {
		s.fleetMetricsMu.Unlock()
		return current.samples, nil
	}
	if build := s.fleetMetricsInflight; build != nil {
		s.fleetMetricsMu.Unlock()
		select {
		case <-build.done:
			return build.samples, build.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	build := &fleetMetricsBuild{done: make(chan struct{})}
	s.fleetMetricsInflight = build
	s.fleetMetricsMu.Unlock()

	samples, err := s.store.ListServerMetricSamples(ctx, 0, fleetMetricsWindow)

	s.fleetMetricsMu.Lock()
	if err == nil {
		s.fleetMetricsCache.Store(&fleetMetricsEntry{builtAt: time.Now(), samples: samples})
	}
	build.samples = samples
	build.err = err
	s.fleetMetricsInflight = nil
	close(build.done)
	s.fleetMetricsMu.Unlock()
	return samples, err
}
