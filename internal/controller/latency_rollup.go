package controller

import (
	"context"
	"errors"
	"log"
	"os"
	"runtime/metrics"
	"time"

	"github.com/OboardProject/oboard/internal/store"
)

type latencyRollupSchedule struct {
	latencyEnabled, slaEnabled, slaTurn bool
	rows                                int
	delay                               time.Duration
	wait                                time.Duration
	cpu                                 float64
	sampledAt, lastLog                  time.Time
}

func newLatencyRollupSchedule() *latencyRollupSchedule {
	latency, sla := os.Getenv("OBOARD_LATENCY_ROLLUP_WRITE") == "1", os.Getenv("OBOARD_SLA_PROJECTION_WRITE") == "1"
	if !latency && !sla {
		return nil
	}
	return &latencyRollupSchedule{latencyEnabled: latency, slaEnabled: sla, rows: store.LatencyRollupBatchLimit, delay: 2 * time.Second}
}

func (state *latencyRollupSchedule) next(processed int, duration time.Duration, err error) time.Duration {
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			state.rows = max(1, state.rows/2)
		}
		state.delay = min(5*time.Minute, max(5*time.Second, state.delay*2))
	} else if processed == 0 {
		state.delay = min(30*time.Second, max(2*time.Second, state.delay*2))
	} else {
		// Wall time is a conservative upper bound for this one sequential worker's
		// CPU time. Leave at least fifty times its work duration between batches.
		state.delay = max(2*time.Second, duration*50)
	}
	return state.delay
}

func (s *Server) runLatencyRollup(ctx context.Context, state *latencyRollupSchedule) time.Duration {
	at := time.Now()
	stats := s.store.DBStats()
	waited := stats.WaitDuration > state.wait
	state.wait = stats.WaitDuration
	samples := []metrics.Sample{{Name: "/cpu/classes/user:cpu-seconds"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}}
	metrics.Read(samples)
	cpu := 0.0
	for _, sample := range samples {
		if sample.Value.Kind() == metrics.KindFloat64 {
			cpu += sample.Value.Float64()
		}
	}
	cpuBusy := !state.sampledAt.IsZero() && cpu >= state.cpu && (cpu-state.cpu)/at.Sub(state.sampledAt).Seconds() > 0.8
	state.cpu, state.sampledAt = cpu, at
	if stats.InUse > 0 || waited || cpuBusy {
		state.delay = max(state.delay, 30*time.Second)
		return state.delay
	}
	batchCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	busy, err := s.store.LatencyRollupShouldYield(batchCtx)
	if err != nil {
		return state.next(0, time.Since(at), err)
	}
	if busy {
		state.delay = max(state.delay, 30*time.Second)
		return state.delay
	}
	useSLA := state.slaEnabled && (!state.latencyEnabled || state.slaTurn)
	state.slaTurn = !state.slaTurn
	if useSLA {
		result, err := s.store.RunSLAProjectionBatch(batchCtx, at, state.rows, buildSLAProjection)
		if errors.Is(err, store.ErrSLAProjectionDensity) && state.rows < store.LatencyRollupBatchLimit {
			state.rows = min(store.LatencyRollupBatchLimit, state.rows*2)
		}

		if ctx.Err() == nil && (err != nil || result.Events > 0 || result.Buckets > 0) && at.Sub(state.lastLog) >= time.Minute {
			log.Printf("SLA projection: events=%d buckets=%d server=%d phase=%s duration=%s error=%v", result.Events, result.Buckets, result.ServerID, result.Phase, time.Since(at), err)
			state.lastLog = at
		}
		return state.next(result.Events+result.Buckets, time.Since(at), err)
	}
	result, err := s.store.RunLatencyRollupBatch(batchCtx, at, state.rows)
	if ctx.Err() == nil && (err != nil || result.Processed > 0) && at.Sub(state.lastLog) >= time.Minute {
		log.Printf("latency rollup: processed=%d buckets=%d expired=%d pending_id_span=%d cursor=%d duration=%s transaction=%s error=%v", result.Processed, result.Buckets, result.Expired, result.PendingIDSpan, result.Cursor, result.Duration, result.TransactionDuration, err)
		state.lastLog = at
	}
	return state.next(result.Processed, time.Since(at), err)
}
