package model

import "time"

type ServerMonitoringSample struct {
	Available    bool      `json:"available"`
	LatencyMS    *float64  `json:"latency_ms"`
	SampleCount  int64     `json:"sample_count"`
	SuccessCount int64     `json:"success_count"`
	CheckedAt    time.Time `json:"checked_at"`
}

type ServerMonitoringDisplay struct {
	Name    string                   `json:"name"`
	Enabled bool                     `json:"enabled"`
	Samples []ServerMonitoringSample `json:"samples"`
}
