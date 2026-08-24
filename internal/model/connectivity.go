package model

import "time"

type ConnectivityEventKind string

const (
	ConnectivityEventSourceAgentSocket      = "agent_socket"
	ConnectivityEventSourceControllerUpdate = "controller_update"

	ConnectivityEventProbeResult            ConnectivityEventKind = "probe_result"
	ConnectivityEventServerOffline          ConnectivityEventKind = "server_offline"
	ConnectivityEventProbeEnabled           ConnectivityEventKind = "probe_enabled"
	ConnectivityEventProbeDisabled          ConnectivityEventKind = "probe_disabled"
	ConnectivityEventProbeTargetChanged     ConnectivityEventKind = "probe_target_changed"
	ConnectivityEventControllerConnected    ConnectivityEventKind = "controller_connected"
	ConnectivityEventControllerDisconnected ConnectivityEventKind = "controller_disconnected"
)

type ServerConnectivityEvent struct {
	ID          int64                 `json:"id"`
	ServerID    int64                 `json:"server_id"`
	Kind        ConnectivityEventKind `json:"kind"`
	Available   *bool                 `json:"available"`
	LatencyMS   int                   `json:"latency_ms"`
	Error       string                `json:"error"`
	Source      string                `json:"source"`
	EffectiveAt time.Time             `json:"effective_at"`
	EventKey    string                `json:"event_key"`
	CreatedAt   time.Time             `json:"created_at"`
}

type ServerConnectivityHistory struct {
	Baseline  []ServerConnectivityEvent
	Events    []ServerConnectivityEvent
	DataStart *time.Time
}
