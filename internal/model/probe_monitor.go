package model

import "encoding/json"

type ProbeMonitor struct {
	ID              int64           `json:"id"`
	ServerID        int64           `json:"server_id"`
	Name            string          `json:"name"`
	Kind            string          `json:"kind"`
	Target          string          `json:"target"`
	Mode            string          `json:"mode"`
	Port            int             `json:"port"`
	IntervalSeconds int             `json:"interval_seconds"`
	Enabled         bool            `json:"enabled"`
	Revision        int64           `json:"revision"`
	CheckedAt       string          `json:"checked_at"`
	Status          string          `json:"status"`
	Error           string          `json:"error"`
	Result          json.RawMessage `json:"result"`
}

type ProbeRouteVersion struct {
	ID         int64              `json:"id"`
	CheckedAt  string             `json:"checked_at"`
	LastSeenAt string             `json:"last_seen_at"`
	ASNPath    []string           `json:"asn_path"`
	Trace      NetworkTraceResult `json:"trace"`
}
