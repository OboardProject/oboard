package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// ServerRuntimeState is the health-report surface of a server: the fields a
// report can change plus the telemetry values rendered by the UI. It lets the
// hot path return a before/after pair so callers can publish a realtime patch
// without re-reading the server.
type ServerRuntimeState struct {
	ServerID              int64
	Status                model.ServerStatus
	PublicIPv4            string
	PublicIPv6            string
	InterfaceIPv6         string
	DetectedRegionCode    string
	OS                    string
	DistroID              string
	DistroVersion         string
	DistroName            string
	Libc                  string
	ServiceManager        string
	PackageManager        string
	Arch                  string
	Kernel                string
	CPU                   string
	MemoryBytes           uint64
	CPUUsagePercent       float64
	MemoryUsedBytes       uint64
	MemoryTotalBytes      uint64
	AgentMemoryBytes      uint64
	DiskBytes             uint64
	DiskTotalBytes        uint64
	TCPConnectionCount    uint64
	UDPConnectionCount    uint64
	ProcessCount          uint64
	AgentVersion          string
	AgentBuild            string
	SingBoxVersion        string
	KernelCapabilities    []string
	TCPFastOpenState      string
	TCPFastOpenValue      int
	NetworkUploadBPS      uint64
	NetworkDownloadBPS    uint64
	TrafficUploadBytes    uint64
	TrafficDownloadBytes  uint64
	ConnectivityStatus    string
	ConnectivityLatencyMS int64
	ConnectivityCheckedAt *time.Time
	ConnectivityError     string
	TelemetryUpdatedAt    *time.Time
	LastSeenAt            *time.Time
}

// HealthApplyResult reports what one health report changed. StatusChanged is
// true only when this call performed the status transition; a concurrent
// report already owning the transition yields StatusChanged=false so recovery
// handling stays idempotent.
type HealthApplyResult struct {
	OldStatus      model.ServerStatus
	NewStatus      model.ServerStatus
	StatusChanged  bool
	Prev           ServerRuntimeState
	Curr           ServerRuntimeState
	SampleInserted bool
	Coalesced      bool
}

// ApplyHealthReport persists one Agent health report. Unlike
// UpsertHealthTransition it is keyed by server id, performs all SQL in a
// single write transaction, never loads telemetry or latency settings beyond
// the target server's own rows, never touches telemetry management settings
// (monitoring mode, traffic reset, time correction, offline policy), and uses
// an atomic conditional INSERT for the metric sample instead of a separate
// SELECT MAX(sampled_at).
func (s *Store) ApplyHealthReport(ctx context.Context, serverID int64, report model.HealthReport, window model.ServerTrafficWindow) (HealthApplyResult, error) {
	if serverID <= 0 {
		return HealthApplyResult{}, errors.New("health report requires a server id")
	}
	if window.Key == "" || window.Start.IsZero() || window.End.IsZero() {
		return HealthApplyResult{}, errors.New("invalid server telemetry report window")
	}
	tx, err := s.db.BeginCountedTx(ctx, nil)
	if err != nil {
		return HealthApplyResult{}, err
	}
	defer tx.Rollback()
	ts := report.Timestamp.UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	nowText := ts.Format(time.RFC3339Nano)
	// last_seen_at is wall-clock (offline detection and presence depend on the
	// Controller clock), while traffic accounting and samples use the report
	// timestamp, matching the pre-existing split.
	seenAt := time.Now().UTC()

	var oldStatus string
	var prev ServerRuntimeState
	var kernelCapabilitiesJSON string
	var lastSeen sql.NullString
	if err := tx.QueryRowContext(ctx, `select status,public_ipv4,public_ipv6,interface_ipv6,detected_region_code,os,distro_id,distro_version,distro_name,libc,service_manager,package_manager,arch,kernel,cpu,memory_bytes,cpu_usage_percent,memory_used_bytes,memory_total_bytes,agent_memory_bytes,disk_bytes,disk_total_bytes,tcp_connection_count,udp_connection_count,process_count,agent_version,agent_build,sing_box_version,kernel_capabilities_json,coalesce(tcp_fastopen_state,''),coalesce(tcp_fastopen_value,0),last_seen_at from servers where id=?`, serverID).Scan(&oldStatus, &prev.PublicIPv4, &prev.PublicIPv6, &prev.InterfaceIPv6, &prev.DetectedRegionCode, &prev.OS, &prev.DistroID, &prev.DistroVersion, &prev.DistroName, &prev.Libc, &prev.ServiceManager, &prev.PackageManager, &prev.Arch, &prev.Kernel, &prev.CPU, &prev.MemoryBytes, &prev.CPUUsagePercent, &prev.MemoryUsedBytes, &prev.MemoryTotalBytes, &prev.AgentMemoryBytes, &prev.DiskBytes, &prev.DiskTotalBytes, &prev.TCPConnectionCount, &prev.UDPConnectionCount, &prev.ProcessCount, &prev.AgentVersion, &prev.AgentBuild, &prev.SingBoxVersion, &kernelCapabilitiesJSON, &prev.TCPFastOpenState, &prev.TCPFastOpenValue, &lastSeen); err != nil {
		return HealthApplyResult{}, err
	}
	prev.ServerID = serverID
	prev.Status = model.ServerStatus(oldStatus)
	_ = json.Unmarshal([]byte(kernelCapabilitiesJSON), &prev.KernelCapabilities)
	if lastSeen.Valid {
		t := parseTime(lastSeen.String)
		prev.LastSeenAt = &t
	}
	server := &model.Server{Status: prev.Status, PublicIPv4: prev.PublicIPv4, PublicIPv6: prev.PublicIPv6, InterfaceIPv6: prev.InterfaceIPv6, DetectedRegionCode: prev.DetectedRegionCode}
	applyDetectedPublicIPs(server, report)
	if code := normalizeRegionCode(report.RegionCode); code != "" {
		server.DetectedRegionCode = code
	}
	newStatus := report.Status
	curr := prev
	curr.Status = newStatus
	curr.PublicIPv4 = server.PublicIPv4
	curr.PublicIPv6 = server.PublicIPv6
	curr.InterfaceIPv6 = server.InterfaceIPv6
	curr.DetectedRegionCode = server.DetectedRegionCode
	curr.OS = report.OS
	curr.DistroID = report.DistroID
	curr.DistroVersion = report.DistroVersion
	curr.DistroName = report.DistroName
	curr.Libc = report.Libc
	curr.ServiceManager = report.ServiceManager
	curr.PackageManager = report.PackageManager
	curr.Arch = report.Arch
	curr.Kernel = report.Kernel
	curr.CPU = report.CPU
	curr.MemoryBytes = report.MemoryBytes
	curr.CPUUsagePercent = report.CPUUsagePercent
	curr.MemoryUsedBytes = report.MemoryUsedBytes
	curr.MemoryTotalBytes = report.MemoryTotalBytes
	curr.AgentMemoryBytes = report.AgentMemoryBytes
	curr.DiskBytes = report.DiskBytes
	curr.DiskTotalBytes = report.DiskTotalBytes
	curr.TCPConnectionCount = report.TCPConnectionCount
	curr.UDPConnectionCount = report.UDPConnectionCount
	curr.ProcessCount = report.ProcessCount
	curr.AgentVersion = report.AgentVersion
	curr.AgentBuild = report.AgentBuild
	curr.SingBoxVersion = report.SingBoxVersion
	curr.KernelCapabilities = append([]string(nil), report.KernelCapabilities...)
	// An Agent that reports no TFO state keeps the last known one instead of
	// clearing a capability the host still has.
	if state := model.NormalizeTCPFastOpenState(report.TCPFastOpenState); state != model.TCPFastOpenStateUnknown {
		curr.TCPFastOpenState = state
		curr.TCPFastOpenValue = report.TCPFastOpenValue
	}
	curr.LastSeenAt = &seenAt

	coalesced := shouldCoalesceHealthRuntime(prev, curr, seenAt)
	if !coalesced {
		res, err := tx.ExecContext(ctx, `update servers set status=?,os=?,distro_id=?,distro_version=?,distro_name=?,libc=?,service_manager=?,package_manager=?,arch=?,kernel=?,cpu=?,memory_bytes=?,cpu_usage_percent=?,memory_used_bytes=?,memory_total_bytes=?,agent_memory_bytes=?,disk_bytes=?,disk_total_bytes=?,tcp_connection_count=?,udp_connection_count=?,process_count=?,agent_version=?,agent_build=?,sing_box_version=?,kernel_capabilities_json=?,tcp_fastopen_state=?,tcp_fastopen_value=?,public_ipv4=?,public_ipv6=?,interface_ipv6=?,detected_region_code=?,last_seen_at=? where id=? and status=?`,
			newStatus, report.OS, report.DistroID, report.DistroVersion, report.DistroName, report.Libc, report.ServiceManager, report.PackageManager, report.Arch, report.Kernel, report.CPU, report.MemoryBytes, report.CPUUsagePercent, report.MemoryUsedBytes, report.MemoryTotalBytes, report.AgentMemoryBytes, report.DiskBytes, report.DiskTotalBytes, report.TCPConnectionCount, report.UDPConnectionCount, report.ProcessCount, report.AgentVersion, report.AgentBuild, report.SingBoxVersion, stringSliceJSON(report.KernelCapabilities), curr.TCPFastOpenState, curr.TCPFastOpenValue, server.PublicIPv4, server.PublicIPv6, server.InterfaceIPv6, server.DetectedRegionCode, nilTime(&seenAt), serverID, oldStatus)
		if err != nil {
			return HealthApplyResult{}, err
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return HealthApplyResult{}, err
		}
		if affected != 1 {
			return HealthApplyResult{}, sql.ErrNoRows
		}
	} else {
		curr.LastSeenAt = prev.LastSeenAt
	}
	sampleInserted, err := s.updateServerTelemetryReportTx(ctx, tx, serverID, report, window, ts, nowText, &curr)
	if err != nil {
		return HealthApplyResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return HealthApplyResult{}, err
	}
	result := HealthApplyResult{OldStatus: prev.Status, NewStatus: newStatus, StatusChanged: !coalesced && prev.Status != newStatus, Prev: prev, Curr: curr, SampleInserted: sampleInserted, Coalesced: coalesced}
	return result, nil
}

const healthRuntimeCoalesceInterval = 45 * time.Second

func shouldCoalesceHealthRuntime(prev, curr ServerRuntimeState, seenAt time.Time) bool {
	if healthRuntimeCriticalChanged(prev, curr) {
		return false
	}
	if prev.LastSeenAt == nil || seenAt.Sub(*prev.LastSeenAt) >= healthRuntimeCoalesceInterval {
		return false
	}
	return !healthRuntimeVolatileChanged(prev, curr)
}

func healthRuntimeCriticalChanged(prev, curr ServerRuntimeState) bool {
	if prev.Status != curr.Status || prev.PublicIPv4 != curr.PublicIPv4 || prev.PublicIPv6 != curr.PublicIPv6 || prev.InterfaceIPv6 != curr.InterfaceIPv6 || prev.DetectedRegionCode != curr.DetectedRegionCode {
		return true
	}
	if prev.AgentBuild != curr.AgentBuild || prev.AgentVersion != curr.AgentVersion || prev.SingBoxVersion != curr.SingBoxVersion {
		return true
	}
	if prev.OS != curr.OS || prev.Arch != curr.Arch || prev.Kernel != curr.Kernel || prev.DistroID != curr.DistroID || prev.ServiceManager != curr.ServiceManager {
		return true
	}
	if prev.TCPFastOpenState != curr.TCPFastOpenState || prev.TCPFastOpenValue != curr.TCPFastOpenValue {
		return true
	}
	if len(prev.KernelCapabilities) != len(curr.KernelCapabilities) {
		return true
	}
	for i := range prev.KernelCapabilities {
		if prev.KernelCapabilities[i] != curr.KernelCapabilities[i] {
			return true
		}
	}
	return false
}

func healthRuntimeVolatileChanged(prev, curr ServerRuntimeState) bool {
	if absFloat(prev.CPUUsagePercent-curr.CPUUsagePercent) >= 2 {
		return true
	}
	if absUint64Diff(prev.MemoryUsedBytes, curr.MemoryUsedBytes) >= 16<<20 {
		return true
	}
	if absUint64Diff(prev.DiskBytes, curr.DiskBytes) >= 64<<20 {
		return true
	}
	if absUint64Diff(prev.TCPConnectionCount, curr.TCPConnectionCount) >= 50 || absUint64Diff(prev.UDPConnectionCount, curr.UDPConnectionCount) >= 50 {
		return true
	}
	return absUint64Diff(prev.ProcessCount, curr.ProcessCount) >= 10
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func absUint64Diff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// updateServerTelemetryReportTx performs the traffic-accounting and metric
// sample writes inside the health report transaction. It mirrors
// UpdateServerTelemetryReport semantics: period rollover, delta-based traffic
// accumulation with a 10-minute window, BPS tolerance, connectivity
// preservation, and the rate-limited metric sample. It returns whether a
// metric sample was inserted.
func (s *Store) updateServerTelemetryReportTx(ctx context.Context, tx *countingTx, serverID int64, report model.HealthReport, window model.ServerTrafficWindow, ts time.Time, nowText string, curr *ServerRuntimeState) (bool, error) {
	var periodKey string
	var periodUp, periodDown, previousUp, previousDown uint64
	var lastRawAt sql.NullString
	var resourceHistoryEnabled, connectivityAvailable int
	var connectivityLatency int64
	var connectivityChecked sql.NullString
	var connectivityError string
	if err := tx.QueryRowContext(ctx, `select period_key,traffic_upload_bytes,traffic_download_bytes,raw_upload_bytes,raw_download_bytes,last_reported_at,resource_history_enabled,connectivity_available,connectivity_latency_ms,connectivity_checked_at,connectivity_error from server_telemetry where server_id=?`, serverID).Scan(&periodKey, &periodUp, &periodDown, &previousUp, &previousDown, &lastRawAt, &resourceHistoryEnabled, &connectivityAvailable, &connectivityLatency, &connectivityChecked, &connectivityError); err != nil {
		return false, err
	}
	periodChanged := periodKey != window.Key
	hasBaseline := lastRawAt.Valid && !periodChanged
	if periodChanged {
		periodUp, periodDown = 0, 0
	}
	var uploadDelta, downloadDelta uint64
	var elapsed float64
	if hasBaseline {
		last := parseTime(lastRawAt.String)
		elapsed = ts.Sub(last).Seconds()
		if elapsed > 0 && elapsed <= 10*60 {
			if report.NetworkTotalUploadBytes >= previousUp {
				uploadDelta = report.NetworkTotalUploadBytes - previousUp
			}
			if report.NetworkTotalDownloadBytes >= previousDown {
				downloadDelta = report.NetworkTotalDownloadBytes - previousDown
			}
			maxDelta := uint64(elapsed * float64(uint64(100<<30)))
			if uploadDelta > maxDelta {
				uploadDelta = 0
			}
			if downloadDelta > maxDelta {
				downloadDelta = 0
			}
		}
	}
	periodUp += uploadDelta
	periodDown += downloadDelta
	uploadBPS, downloadBPS := report.NetworkUploadBPS, report.NetworkDownloadBPS
	if elapsed > 0 {
		calculatedUp := uint64(float64(uploadDelta) / elapsed)
		calculatedDown := uint64(float64(downloadDelta) / elapsed)
		if !rateWithinProbeTolerance(uploadBPS, calculatedUp) {
			uploadBPS = calculatedUp
		}
		if !rateWithinProbeTolerance(downloadBPS, calculatedDown) {
			downloadBPS = calculatedDown
		}
	}
	if _, err := tx.ExecContext(ctx, `update server_telemetry set period_key=?,period_start=?,period_end=?,traffic_upload_bytes=?,traffic_download_bytes=?,raw_upload_bytes=?,raw_download_bytes=?,network_upload_bps=?,network_download_bps=?,last_reported_at=?,updated_at=? where server_id=?`, window.Key, window.Start.UTC().Format(time.RFC3339Nano), window.End.UTC().Format(time.RFC3339Nano), periodUp, periodDown, report.NetworkTotalUploadBytes, report.NetworkTotalDownloadBytes, uploadBPS, downloadBPS, nowText, nowText, serverID); err != nil {
		return false, err
	}
	curr.NetworkUploadBPS = uploadBPS
	curr.NetworkDownloadBPS = downloadBPS
	curr.TrafficUploadBytes = periodUp
	curr.TrafficDownloadBytes = periodDown
	curr.TelemetryUpdatedAt = &ts
	curr.ConnectivityLatencyMS = connectivityLatency
	curr.ConnectivityError = connectivityError
	if connectivityAvailable == 1 {
		curr.ConnectivityStatus = "available"
	} else if connectivityAvailable == 0 {
		curr.ConnectivityStatus = "unavailable"
	} else {
		curr.ConnectivityStatus = "pending"
	}
	if connectivityChecked.Valid {
		checkedAt := parseTime(connectivityChecked.String)
		curr.ConnectivityCheckedAt = &checkedAt
	}
	cpuUsage, memoryUsed, memoryTotal := report.CPUUsagePercent, report.MemoryUsedBytes, report.MemoryTotalBytes
	diskUsed, diskTotal := report.DiskBytes, report.DiskTotalBytes
	tcpConnections, udpConnections, processes := report.TCPConnectionCount, report.UDPConnectionCount, report.ProcessCount
	if resourceHistoryEnabled == 0 {
		cpuUsage, memoryUsed, memoryTotal = 0, 0, 0
		diskUsed, diskTotal = 0, 0
		tcpConnections, udpConnections, processes = 0, 0, 0
	}
	interval := s.metricSampleMinInterval
	if interval <= 0 {
		interval = defaultMetricSampleMinInterval
	}
	// Atomic conditional INSERT: the sample is rate-limited by the newest
	// existing sample instead of a separate SELECT MAX(sampled_at) + INSERT.
	res, err := tx.ExecContext(ctx, `insert into server_metric_samples(server_id,cpu_usage_percent,memory_used_bytes,memory_total_bytes,disk_used_bytes,disk_total_bytes,tcp_connection_count,udp_connection_count,process_count,resource_recorded,network_upload_bps,network_download_bps,traffic_upload_bytes,traffic_download_bytes,connectivity_available,connectivity_latency_ms,sampled_at) select ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,? where not exists(select 1 from server_metric_samples where server_id=? and sampled_at>?)`, serverID, cpuUsage, memoryUsed, memoryTotal, diskUsed, diskTotal, tcpConnections, udpConnections, processes, resourceHistoryEnabled, uploadBPS, downloadBPS, periodUp, periodDown, connectivityAvailable, connectivityLatency, nowText, serverID, ts.Add(-interval).Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return inserted == 1, nil
}
