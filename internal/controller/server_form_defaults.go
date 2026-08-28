package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// panelServerFormDefaults is the single source of create-form defaults shared by
// the Web 添加服务器 dialog, REST POST /servers, Fast Path, and MCP form validation.
type panelServerFormDefaults struct {
	ListenIP                    string
	ListenMode                  model.ListenMode
	IPStack                     model.IPStack
	UDPInboundMode              model.UDPInboundMode
	EntryIPMode                 model.EntryIPMode
	RegionMode                  string
	MTUMode                     model.MTUMode
	MTUValue                    int
	MTUProbeHost                string
	MTUProbePort                int
	MTUOverheadBytes            int
	BBREnabled                  bool
	TimeCorrectionMode          model.TimeCorrectionMode
	PortRangeStart              int
	PortRangeEnd                int
	InternalPortRangeStart      int
	InternalPortRangeEnd        int
	MonitoringMode              string
	ResourceHistoryEnabled      bool
	LatencyProbeEnabled         bool
	LatencyProbeMode            model.LatencyProbeMode
	LatencyProbePublicTarget    model.ConnectivityTarget
	LatencyProbeIntervalSeconds int
	LatencyProbeSampleCount     int
	LatencyProbeMaxTargets      int
	ConnectionAuditEnabled      bool
	OfflineNotifyEnabled        bool
	OfflineAfterSeconds         int
	AutoRenewEnabled            bool
	RenewalCycle                model.ServerRenewalCycle
	ExpiryNotifyEnabled         bool
	IssueEnrollmentToken        bool
}

func (s *Server) panelServerFormDefaults(ctx context.Context) (panelServerFormDefaults, error) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return panelServerFormDefaults{}, err
	}
	mtuMode, bbrEnabled, timeMode := serverCreationDefaults(settings)
	return panelServerFormDefaults{
		ListenIP:                    "0.0.0.0",
		ListenMode:                  model.ListenModeAuto,
		IPStack:                     model.IPStackAuto,
		UDPInboundMode:              model.UDPInboundAllow,
		EntryIPMode:                 model.EntryIPModeAuto,
		RegionMode:                  "auto",
		MTUMode:                     mtuMode,
		MTUProbeHost:                "1.1.1.1",
		MTUProbePort:                443,
		BBREnabled:                  bbrEnabled,
		TimeCorrectionMode:          timeMode,
		PortRangeStart:              core.DefaultPublicPortRangeStart,
		PortRangeEnd:                core.DefaultPublicPortRangeEnd,
		InternalPortRangeStart:      core.DefaultInternalPortRangeStart,
		InternalPortRangeEnd:        core.DefaultInternalPortRangeEnd,
		MonitoringMode:              "lightweight",
		ResourceHistoryEnabled:      true,
		LatencyProbeEnabled:         true,
		LatencyProbeMode:            model.LatencyProbeModeTCP,
		LatencyProbePublicTarget:    model.ConnectivityProbeTargetAuto,
		LatencyProbeIntervalSeconds: 60,
		LatencyProbeSampleCount:     3,
		LatencyProbeMaxTargets:      64,
		ConnectionAuditEnabled:      settingBool(settings, settingConnectionAuditEnabled, true),
		OfflineNotifyEnabled:        true,
		RenewalCycle:                model.ServerRenewalCycleMonthly,
		ExpiryNotifyEnabled:         true,
		IssueEnrollmentToken:        true,
	}, nil
}

func (d panelServerFormDefaults) asMap() map[string]any {
	return map[string]any{
		"listen_ip":                       d.ListenIP,
		"listen_mode":                     string(d.ListenMode),
		"ip_stack":                        string(d.IPStack),
		"udp_inbound_mode":                string(d.UDPInboundMode),
		"entry_ip_mode":                   string(d.EntryIPMode),
		"region_mode":                     d.RegionMode,
		"mtu_mode":                        string(d.MTUMode),
		"mtu_value":                       d.MTUValue,
		"mtu_probe_host":                  d.MTUProbeHost,
		"mtu_probe_port":                  d.MTUProbePort,
		"mtu_overhead_bytes":              d.MTUOverheadBytes,
		"bbr_enabled":                     d.BBREnabled,
		"time_correction_mode":            string(d.TimeCorrectionMode),
		"port_range_start":                d.PortRangeStart,
		"port_range_end":                  d.PortRangeEnd,
		"internal_port_range_start":       d.InternalPortRangeStart,
		"internal_port_range_end":         d.InternalPortRangeEnd,
		"monitoring_mode":                 d.MonitoringMode,
		"resource_history_enabled":        d.ResourceHistoryEnabled,
		"latency_probe_enabled":           d.LatencyProbeEnabled,
		"latency_probe_mode":              string(d.LatencyProbeMode),
		"latency_probe_public_target":     string(d.LatencyProbePublicTarget),
		"latency_probe_interval_seconds": d.LatencyProbeIntervalSeconds,
		"latency_probe_sample_count":      d.LatencyProbeSampleCount,
		"latency_probe_max_targets":       d.LatencyProbeMaxTargets,
		"connection_audit_enabled":        d.ConnectionAuditEnabled,
		"offline_notify_enabled":          d.OfflineNotifyEnabled,
		"offline_after_seconds":           d.OfflineAfterSeconds,
		"auto_renew_enabled":              d.AutoRenewEnabled,
		"renewal_cycle":                   string(d.RenewalCycle),
		"expiry_notify_enabled":           d.ExpiryNotifyEnabled,
	}
}

func (d panelServerFormDefaults) defaultOnBoolFields() map[string]bool {
	return map[string]bool{
		"bbr_enabled":              d.BBREnabled,
		"resource_history_enabled": d.ResourceHistoryEnabled,
		"latency_probe_enabled":    d.LatencyProbeEnabled,
		"connection_audit_enabled": d.ConnectionAuditEnabled,
		"offline_notify_enabled":   d.OfflineNotifyEnabled,
		"expiry_notify_enabled":    d.ExpiryNotifyEnabled,
		"issue_enrollment_token":   d.IssueEnrollmentToken,
	}
}

func jsonObjectKeys(raw json.RawMessage) map[string]json.RawMessage {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return map[string]json.RawMessage{}
	}
	return object
}

func applyDefaultString(present map[string]json.RawMessage, key string, dest *string, value string) {
	if _, ok := present[key]; !ok {
		*dest = value
	}
}

func applyDefaultBool(present map[string]json.RawMessage, key string, dest *bool, value bool) {
	if _, ok := present[key]; !ok {
		*dest = value
	}
}

func applyDefaultInt(present map[string]json.RawMessage, key string, dest *int, value int) {
	if _, ok := present[key]; !ok {
		*dest = value
	}
}

func fillServerMapDefaults(server map[string]any, defaults panelServerFormDefaults) []map[string]any {
	applied := make([]map[string]any, 0)
	for key, value := range defaults.asMap() {
		if _, ok := server[key]; ok {
			continue
		}
		server[key] = value
		applied = append(applied, map[string]any{"field": "server." + key, "value": value, "reason": "panel_create_default"})
	}
	return applied
}

func explicitFalseWarnings(present map[string]json.RawMessage, defaults panelServerFormDefaults) []string {
	warnings := make([]string, 0)
	for field, defaultOn := range defaults.defaultOnBoolFields() {
		if !defaultOn {
			continue
		}
		raw, ok := present[field]
		if !ok {
			continue
		}
		var value bool
		if json.Unmarshal(raw, &value) != nil || value {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("%s=false disables the panel create default (true). Omit the field to keep the default.", field))
	}
	return warnings
}

// applyServerOnboardingDefaults keeps automation/MCP onboarding aligned with
// the panel create form. A missing field uses the current Controller default;
// an explicitly supplied false/zero value remains authoritative.
func (s *Server) applyServerOnboardingDefaults(ctx context.Context, input json.RawMessage, request *serverOnboardingOperation) error {
	defaults, err := s.panelServerFormDefaults(ctx)
	if err != nil {
		return err
	}
	envelope := jsonObjectKeys(input)
	serverKeys := jsonObjectKeys(envelope["server"])
	applyDefaultString(serverKeys, "listen_ip", &request.Server.ListenIP, defaults.ListenIP)
	if _, ok := serverKeys["listen_mode"]; !ok {
		request.Server.ListenMode = defaults.ListenMode
	}
	if _, ok := serverKeys["ip_stack"]; !ok {
		request.Server.IPStack = defaults.IPStack
	}
	if _, ok := serverKeys["udp_inbound_mode"]; !ok {
		request.Server.UDPInboundMode = defaults.UDPInboundMode
	}
	if _, ok := serverKeys["entry_ip_mode"]; !ok {
		request.Server.EntryIPMode = defaults.EntryIPMode
	}
	applyDefaultString(serverKeys, "region_mode", &request.Server.RegionMode, defaults.RegionMode)
	if _, ok := serverKeys["mtu_mode"]; !ok {
		request.Server.MTUMode = defaults.MTUMode
	}
	applyDefaultString(serverKeys, "mtu_probe_host", &request.Server.MTUProbeHost, defaults.MTUProbeHost)
	applyDefaultInt(serverKeys, "mtu_probe_port", &request.Server.MTUProbePort, defaults.MTUProbePort)
	applyDefaultInt(serverKeys, "mtu_overhead_bytes", &request.Server.MTUOverheadBytes, defaults.MTUOverheadBytes)
	applyDefaultBool(serverKeys, "bbr_enabled", &request.Server.BBREnabled, defaults.BBREnabled)
	if _, ok := serverKeys["time_correction_mode"]; !ok {
		request.Server.TimeCorrectionMode = defaults.TimeCorrectionMode
	}
	applyDefaultInt(serverKeys, "port_range_start", &request.Server.PortRangeStart, defaults.PortRangeStart)
	applyDefaultInt(serverKeys, "port_range_end", &request.Server.PortRangeEnd, defaults.PortRangeEnd)
	applyDefaultInt(serverKeys, "internal_port_range_start", &request.Server.InternalPortRangeStart, defaults.InternalPortRangeStart)
	applyDefaultInt(serverKeys, "internal_port_range_end", &request.Server.InternalPortRangeEnd, defaults.InternalPortRangeEnd)
	applyDefaultString(serverKeys, "monitoring_mode", &request.Server.MonitoringMode, defaults.MonitoringMode)
	applyDefaultBool(serverKeys, "resource_history_enabled", &request.Server.ResourceHistoryEnabled, defaults.ResourceHistoryEnabled)
	request.Server.ResourceHistoryConfigured = true
	applyDefaultBool(serverKeys, "latency_probe_enabled", &request.Server.LatencyProbeEnabled, defaults.LatencyProbeEnabled)
	if _, ok := serverKeys["latency_probe_mode"]; !ok {
		request.Server.LatencyProbeMode = defaults.LatencyProbeMode
	}
	if _, ok := serverKeys["latency_probe_public_target"]; !ok {
		request.Server.LatencyProbePublicTarget = defaults.LatencyProbePublicTarget
	}
	applyDefaultInt(serverKeys, "latency_probe_interval_seconds", &request.Server.LatencyProbeIntervalSeconds, defaults.LatencyProbeIntervalSeconds)
	applyDefaultInt(serverKeys, "latency_probe_sample_count", &request.Server.LatencyProbeSampleCount, defaults.LatencyProbeSampleCount)
	applyDefaultInt(serverKeys, "latency_probe_max_targets", &request.Server.LatencyProbeMaxTargets, defaults.LatencyProbeMaxTargets)
	applyDefaultBool(serverKeys, "connection_audit_enabled", &request.Server.ConnectionAuditEnabled, defaults.ConnectionAuditEnabled)
	applyDefaultBool(serverKeys, "offline_notify_enabled", &request.Server.OfflineNotifyEnabled, defaults.OfflineNotifyEnabled)
	applyDefaultBool(serverKeys, "expiry_notify_enabled", &request.Server.ExpiryNotifyEnabled, defaults.ExpiryNotifyEnabled)
	if _, ok := serverKeys["renewal_cycle"]; !ok {
		request.Server.RenewalCycle = defaults.RenewalCycle
	}
	if _, ok := envelope["issue_enrollment_token"]; !ok {
		request.IssueEnrollmentToken = defaults.IssueEnrollmentToken
	}
	_, hasMode := serverKeys["traffic_reset_mode"]
	_, hasDay := serverKeys["traffic_reset_day"]
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return err
	}
	if !hasMode && !hasDay {
		if derivedMode, derivedDay, ok := deriveServerTrafficReset(nil, nil, request.Server.ServiceStartAt, request.Server.ExpiresAt, trafficLocation(settings)); ok {
			request.Server.TrafficResetMode = derivedMode
			request.Server.TrafficResetDay = derivedDay
		} else {
			request.Server.TrafficResetMode = "monthly"
			request.Server.TrafficResetDay = 1
		}
	} else {
		if !hasMode {
			request.Server.TrafficResetMode = "monthly"
		} else {
			request.Server.TrafficResetMode = normalizeControllerTrafficResetMode(request.Server.TrafficResetMode)
		}
		if !hasDay {
			request.Server.TrafficResetDay = 1
		} else {
			request.Server.TrafficResetDay = normalizeControllerTrafficResetDay(request.Server.TrafficResetDay)
		}
	}
	return nil
}

func (s *Server) materializeServerOnboardForm(ctx context.Context, input json.RawMessage) (map[string]any, []map[string]any, []string, error) {
	defaults, err := s.panelServerFormDefaults(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	request, err := decodeServerOnboardingOperation(input)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := s.applyServerOnboardingDefaults(ctx, input, &request); err != nil {
		return nil, nil, nil, err
	}
	if err := validateServer(&request.Server); err != nil {
		return nil, nil, nil, err
	}
	normalized, err := json.Marshal(request)
	if err != nil {
		return nil, nil, nil, err
	}
	var output map[string]any
	if err := json.Unmarshal(normalized, &output); err != nil {
		return nil, nil, nil, err
	}
	envelope := jsonObjectKeys(input)
	serverKeys := jsonObjectKeys(envelope["server"])
	applied := make([]map[string]any, 0)
	for key, value := range defaults.asMap() {
		if _, ok := serverKeys[key]; ok {
			continue
		}
		applied = append(applied, map[string]any{"field": "server." + key, "value": value, "reason": "panel_create_default"})
	}
	if _, ok := envelope["issue_enrollment_token"]; !ok {
		applied = append(applied, map[string]any{"field": "issue_enrollment_token", "value": defaults.IssueEnrollmentToken, "reason": "panel_create_default"})
	}
	if _, hasMode := serverKeys["traffic_reset_mode"]; !hasMode {
		if _, hasDay := serverKeys["traffic_reset_day"]; !hasDay {
			applied = append(applied, map[string]any{"field": "server.traffic_reset_mode", "value": request.Server.TrafficResetMode, "reason": "panel_create_default"})
			applied = append(applied, map[string]any{"field": "server.traffic_reset_day", "value": request.Server.TrafficResetDay, "reason": "panel_create_default"})
		}
	}
	presentForWarnings := map[string]json.RawMessage{}
	for key, raw := range serverKeys {
		presentForWarnings[key] = raw
	}
	if raw, ok := envelope["issue_enrollment_token"]; ok {
		presentForWarnings["issue_enrollment_token"] = raw
	}
	return output, applied, explicitFalseWarnings(presentForWarnings, defaults), nil
}

func panelServerFormResource(defaults panelServerFormDefaults) map[string]any {
	return map[string]any{
		"name": "OBoard server create form",
		"summary": "The panel 添加服务器 dialog is the source of create defaults. MCP and automation must omit unspecified fields instead of sending false or zero. Controller fills the same defaults before validate and apply.",
		"rule":    "Omitted fields use these defaults. Explicit values win, including explicit false. Sending false for an unspecified default-on switch disables a feature the panel would leave on.",
		"required": []string{"server.name"},
		"defaults": defaults.asMap(),
		"default_on_switches": []string{
			"bbr_enabled", "resource_history_enabled", "latency_probe_enabled",
			"connection_audit_enabled", "offline_notify_enabled", "expiry_notify_enabled", "issue_enrollment_token",
		},
		"tabs": []map[string]any{
			{"id": "basic", "label": "基础", "fields": []string{"name", "region_mode", "region_code", "entry_ip_mode", "entry_address", "listen_mode", "listen_ip"}},
			{"id": "billing", "label": "到期", "fields": []string{"service_start_at", "expires_at", "auto_renew_enabled", "renewal_cycle", "expiry_notify_enabled", "traffic_reset_mode", "traffic_reset_day", "traffic_limit_bytes", "traffic_used_bytes"}},
			{"id": "network", "label": "网络", "fields": []string{"ip_stack", "udp_inbound_mode", "bbr_enabled", "port_range_start", "port_range_end", "internal_port_range_start", "internal_port_range_end", "mtu_mode", "mtu_value", "mtu_probe_host", "mtu_probe_port", "mtu_overhead_bytes"}},
			{"id": "monitor", "label": "监控", "fields": []string{"monitoring_mode", "resource_history_enabled", "latency_probe_enabled", "latency_probe_mode", "latency_probe_public_target", "connection_audit_enabled", "offline_notify_enabled", "offline_after_seconds"}},
			{"id": "system", "label": "系统", "fields": []string{"time_correction_mode"}},
		},
		"validate_with": "oboard_validate_form",
		"create_capability": "servers.onboard",
		"notes": []string{
			"Prefer oboard_task intent server.onboard with only the fields the user specified.",
			"Call oboard_validate_form before a fallback servers.onboard submit so newly added default-on fields are filled instead of JSON false.",
			"servers.update is a patch: omitted fields stay unchanged and must not be filled with create defaults.",
			"traffic_reset_mode/day are derived from service_start_at then expires_at when omitted.",
		},
	}
}

func normalizeValidateFormCapability(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
