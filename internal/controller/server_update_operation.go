package controller

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

type serverUpdateChanges struct {
	Name                     *string                     `json:"name,omitempty"`
	EntryAddress             *string                     `json:"entry_address,omitempty"`
	EntryIPMode              *model.EntryIPMode          `json:"entry_ip_mode,omitempty"`
	RegionMode               *string                     `json:"region_mode,omitempty"`
	RegionCode               *string                     `json:"region_code,omitempty"`
	ListenIP                 *string                     `json:"listen_ip,omitempty"`
	ListenMode               *model.ListenMode           `json:"listen_mode,omitempty"`
	IPStack                  *model.IPStack              `json:"ip_stack,omitempty"`
	UDPInboundMode           *model.UDPInboundMode       `json:"udp_inbound_mode,omitempty"`
	MTUMode                  *model.MTUMode              `json:"mtu_mode,omitempty"`
	MTUValue                 *int                        `json:"mtu_value,omitempty"`
	MTUProbeHost             *string                     `json:"mtu_probe_host,omitempty"`
	MTUProbePort             *int                        `json:"mtu_probe_port,omitempty"`
	MTUOverheadBytes         *int                        `json:"mtu_overhead_bytes,omitempty"`
	BBREnabled               *bool                       `json:"bbr_enabled,omitempty"`
	PortRangeStart           *int                        `json:"port_range_start,omitempty"`
	PortRangeEnd             *int                        `json:"port_range_end,omitempty"`
	InternalPortRangeStart   *int                        `json:"internal_port_range_start,omitempty"`
	InternalPortRangeEnd     *int                        `json:"internal_port_range_end,omitempty"`
	ConnectionAuditEnabled   *bool                       `json:"connection_audit_enabled,omitempty"`
	LatencyProbeEnabled      *bool                       `json:"latency_probe_enabled,omitempty"`
	LatencyProbeMode         *model.LatencyProbeMode     `json:"latency_probe_mode,omitempty"`
	LatencyProbePublicTarget *model.ConnectivityTarget   `json:"latency_probe_public_target,omitempty"`
	LatencyProbeInterval     *int                        `json:"latency_probe_interval_seconds,omitempty"`
	LatencyProbeSamples      *int                        `json:"latency_probe_sample_count,omitempty"`
	LatencyProbeRegions      *[]model.LatencyProbeRegion `json:"latency_probe_regions,omitempty"`
	LatencyProbeMaxTargets   *int                        `json:"latency_probe_max_targets,omitempty"`
	TimeCorrectionMode       *model.TimeCorrectionMode   `json:"time_correction_mode,omitempty"`
	OfflineNotifyEnabled     *bool                       `json:"offline_notify_enabled,omitempty"`
	OfflineAfterSeconds      *int                        `json:"offline_after_seconds,omitempty"`
}

type serverUpdateOperation struct {
	ServerID int64               `json:"server_id"`
	Changes  serverUpdateChanges `json:"changes"`
}

type serverPortMigrationRequiredError struct {
	Preview core.ServerPortPolicyChangePreview
}

func (e *serverPortMigrationRequiredError) Error() string {
	return serverPortPolicyConflictMessage(e.Preview)
}

func decodeServerUpdateOperation(input json.RawMessage) (serverUpdateOperation, error) {
	var request serverUpdateOperation
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	if request.ServerID <= 0 {
		return request, errors.New("positive server_id is required")
	}
	encoded, _ := json.Marshal(request.Changes)
	if string(encoded) == "{}" {
		return request, errors.New("at least one server setting change is required")
	}
	return request, nil
}

func (s *Server) validateServerUpdateOperation(ctx context.Context, principal application.Principal, request serverUpdateOperation) (*model.Server, []string, error) {
	if !principal.AllowsInt64("server_ids", request.ServerID) {
		return nil, nil, errors.New("authorized server_id is required")
	}
	current, err := s.store.GetServer(ctx, request.ServerID)
	if err != nil {
		return nil, nil, err
	}
	next := *current
	changed := applyServerUpdateChanges(&next, request.Changes)
	if err := s.validateServerUpdateCandidate(ctx, *current, &next); err != nil {
		return nil, nil, err
	}
	sort.Strings(changed)
	return &next, changed, nil
}

func (s *Server) validateServerUpdateCandidate(ctx context.Context, current model.Server, next *model.Server) error {
	if err := validateServer(next); err != nil {
		return err
	}
	if portPolicyChanged(current, *next) {
		allocations, err := s.store.ListProxyPathPortAllocations(ctx)
		if err != nil {
			return err
		}
		inbounds, err := s.store.ListInbounds(ctx)
		if err != nil {
			return err
		}
		preview := core.PreviewServerPortPolicyChange(current, *next, allocations, inbounds)
		if preview.RequiresMigration() {
			return &serverPortMigrationRequiredError{Preview: preview}
		}
	}
	return nil
}

func applyServerUpdateChanges(next *model.Server, changes serverUpdateChanges) []string {
	changed := []string{}
	set := func(name string, present bool, apply func()) {
		if present {
			apply()
			changed = append(changed, name)
		}
	}
	set("name", changes.Name != nil, func() { next.Name = *changes.Name })
	set("entry_address", changes.EntryAddress != nil, func() { next.EntryAddress = *changes.EntryAddress })
	set("entry_ip_mode", changes.EntryIPMode != nil, func() { next.EntryIPMode = *changes.EntryIPMode })
	set("region_mode", changes.RegionMode != nil, func() { next.RegionMode = *changes.RegionMode })
	set("region_code", changes.RegionCode != nil, func() { next.RegionCode = *changes.RegionCode })
	set("listen_ip", changes.ListenIP != nil, func() { next.ListenIP = *changes.ListenIP })
	set("listen_mode", changes.ListenMode != nil, func() { next.ListenMode = *changes.ListenMode })
	set("ip_stack", changes.IPStack != nil, func() { next.IPStack = *changes.IPStack })
	set("udp_inbound_mode", changes.UDPInboundMode != nil, func() { next.UDPInboundMode = *changes.UDPInboundMode })
	set("mtu_mode", changes.MTUMode != nil, func() { next.MTUMode = *changes.MTUMode })
	set("mtu_value", changes.MTUValue != nil, func() { next.MTUValue = *changes.MTUValue })
	set("mtu_probe_host", changes.MTUProbeHost != nil, func() { next.MTUProbeHost = *changes.MTUProbeHost })
	set("mtu_probe_port", changes.MTUProbePort != nil, func() { next.MTUProbePort = *changes.MTUProbePort })
	set("mtu_overhead_bytes", changes.MTUOverheadBytes != nil, func() { next.MTUOverheadBytes = *changes.MTUOverheadBytes })
	set("bbr_enabled", changes.BBREnabled != nil, func() { next.BBREnabled = *changes.BBREnabled })
	set("port_range_start", changes.PortRangeStart != nil, func() { next.PortRangeStart = *changes.PortRangeStart })
	set("port_range_end", changes.PortRangeEnd != nil, func() { next.PortRangeEnd = *changes.PortRangeEnd })
	set("internal_port_range_start", changes.InternalPortRangeStart != nil, func() { next.InternalPortRangeStart = *changes.InternalPortRangeStart })
	set("internal_port_range_end", changes.InternalPortRangeEnd != nil, func() { next.InternalPortRangeEnd = *changes.InternalPortRangeEnd })
	set("connection_audit_enabled", changes.ConnectionAuditEnabled != nil, func() { next.ConnectionAuditEnabled = *changes.ConnectionAuditEnabled })
	set("latency_probe_enabled", changes.LatencyProbeEnabled != nil, func() { next.LatencyProbeEnabled = *changes.LatencyProbeEnabled })
	set("latency_probe_mode", changes.LatencyProbeMode != nil, func() { next.LatencyProbeMode = *changes.LatencyProbeMode })
	set("latency_probe_public_target", changes.LatencyProbePublicTarget != nil, func() { next.LatencyProbePublicTarget = *changes.LatencyProbePublicTarget })
	set("latency_probe_interval_seconds", changes.LatencyProbeInterval != nil, func() { next.LatencyProbeIntervalSeconds = *changes.LatencyProbeInterval })
	set("latency_probe_sample_count", changes.LatencyProbeSamples != nil, func() { next.LatencyProbeSampleCount = *changes.LatencyProbeSamples })
	set("latency_probe_regions", changes.LatencyProbeRegions != nil, func() { next.LatencyProbeRegions = *changes.LatencyProbeRegions })
	set("latency_probe_max_targets", changes.LatencyProbeMaxTargets != nil, func() { next.LatencyProbeMaxTargets = *changes.LatencyProbeMaxTargets })
	set("time_correction_mode", changes.TimeCorrectionMode != nil, func() { next.TimeCorrectionMode = *changes.TimeCorrectionMode })
	set("offline_notify_enabled", changes.OfflineNotifyEnabled != nil, func() { next.OfflineNotifyEnabled = *changes.OfflineNotifyEnabled })
	set("offline_after_seconds", changes.OfflineAfterSeconds != nil, func() { next.OfflineAfterSeconds = *changes.OfflineAfterSeconds })
	return changed
}

func (s *Server) registerServerUpdateOperation() {
	s.automation.RegisterValidator("servers.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerUpdateOperation(input)
		if err != nil {
			return nil, err
		}
		next, changed, err := s.validateServerUpdateOperation(ctx, principal, request)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server_id": next.ID, "changed_fields": changed}, nil
	})
	s.automation.RegisterRevisionResolver("servers.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		request, err := decodeServerUpdateOperation(input)
		if err != nil || !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server_id is required")
		}
		server, err := s.store.GetServer(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(server.ID, 10): server.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeServerUpdateOperation(input)
		if err != nil {
			return nil, err
		}
		current, err := s.store.GetServer(ctx, request.ServerID)
		if err != nil {
			return nil, err
		}
		next, changed, err := s.validateServerUpdateOperation(ctx, principal, request)
		if err != nil {
			return nil, err
		}
		if err := s.store.UpdateServer(ctx, next); err != nil {
			return nil, err
		}
		if current.TimeCorrectionMode != next.TimeCorrectionMode {
			if err := s.store.ResetServerTimeCheck(ctx, next.ID); err != nil {
				return nil, err
			}
			if next.AgentID != "" && next.Status != model.ServerOffline {
				_, _ = s.queueTimeCheck(ctx, *next, true)
			}
		}
		updated, err := s.store.GetServer(ctx, next.ID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server_id": updated.ID, "revision": updated.UpdatedAt.UTC().Format(time.RFC3339Nano), "changed_fields": changed}, nil
	})
}
