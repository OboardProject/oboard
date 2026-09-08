package controller

import (
	"expvar"
	"sync/atomic"
	"time"
)

// Hot-path observation counters. Labels stay coarse (stage name only) so cardinality
// cannot grow with user/report IDs. Exposed on the loopback pprof/expvar listener.
var (
	hotPathRequests      = expvar.NewMap("oboard_hot_path_requests")
	hotPathErrors        = expvar.NewMap("oboard_hot_path_errors")
	hotPathStageNanos    = expvar.NewMap("oboard_hot_path_stage_ns")
	hotPathCacheHits     = expvar.NewMap("oboard_hot_path_cache_hits")
	hotPathCacheMisses   = expvar.NewMap("oboard_hot_path_cache_misses")
	hotPathCacheBuilds   = expvar.NewMap("oboard_hot_path_cache_builds")
	hotPathSQLStatements = expvar.NewInt("oboard_hot_path_sql_statements")
	hotPathWriteTx       = expvar.NewInt("oboard_hot_path_write_transactions")
)

type hotPathSpan struct {
	name  string
	start time.Time
}

func startHotPath(name string) hotPathSpan {
	hotPathRequests.Add(name, 1)
	return hotPathSpan{name: name, start: time.Now()}
}

func (s hotPathSpan) stage(stage string, started time.Time) {
	hotPathStageNanos.Add(stage, time.Since(started).Nanoseconds())
}

func (s hotPathSpan) end(err error) {
	hotPathStageNanos.Add(s.name+"_total", time.Since(s.start).Nanoseconds())
	if err != nil {
		hotPathErrors.Add(s.name, 1)
	}
}

func noteCacheHit(name string)  { hotPathCacheHits.Add(name, 1) }
func noteCacheMiss(name string) { hotPathCacheMisses.Add(name, 1) }
func noteCacheBuild(name string) {
	hotPathCacheBuilds.Add(name, 1)
}

// Publish coalesce-cache counters periodically onto expvar maps without
// introducing per-user labels.
func publishCoalesceStats(name string, hits, misses, builds int64) {
	hotPathCacheHits.Set(name, newInt(hits))
	hotPathCacheMisses.Set(name, newInt(misses))
	hotPathCacheBuilds.Set(name, newInt(builds))
}

func newInt(v int64) expvar.Var {
	i := new(expvar.Int)
	i.Set(v)
	return i
}

// Ensure atomic stays available for future queue-depth gauges.
var hotPathQueueDepth atomic.Int64
