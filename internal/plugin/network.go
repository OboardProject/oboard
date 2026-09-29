package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginhttp"
)

// agentStartWindow is how long a diagnostic may wait for its Agent before
// it is refused rather than queued behind other work.
const agentStartWindow = 15 * time.Second

type diagnosticCommon struct {
	ServerID string `json:"server_id"`
	IPFamily string `json:"ip_family"`
}

func normalizeFamily(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", model.NetworkFamilyAuto:
		return model.NetworkFamilyAuto, nil
	case model.NetworkFamilyIPv4, model.NetworkFamilyIPv6:
		return value, nil
	default:
		return "", Fail(CodeInvalidArgument, "ip_family must be auto, ipv4 or ipv6")
	}
}

// ValidateNetworkTarget accepts a public DNS name or a public IP literal.
// Private, loopback, link-local, ULA, multicast and metadata addresses are
// refused here; names are resolved and re-checked by the Agent.
func ValidateNetworkTarget(target string) (string, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" || len(target) > model.NetworkTargetMaxBytes {
		return "", Fail(CodeInvalidArgument, "target is required (at most 253 bytes)")
	}
	if ip, err := netip.ParseAddr(strings.Trim(target, "[]")); err == nil {
		if !pluginhttp.PublicIP(ip) {
			return "", Fail(CodeTargetNotAllowed, "private, loopback, link-local and metadata addresses are not allowed")
		}
		return ip.Unmap().String(), nil
	}
	if !ValidHostname(target) {
		return "", Fail(CodeTargetNotAllowed, "target must be a public DNS name or public IP address")
	}
	return target, nil
}

func intDefault(value *int, fallback, min, max int, field string) (int, error) {
	if value == nil {
		return fallback, nil
	}
	if *value < min || *value > max {
		return 0, Fail(CodeInvalidArgument, field+" must be between "+strconv.Itoa(min)+" and "+strconv.Itoa(max))
	}
	return *value, nil
}

func requiredAgentCapability(capability string, mode string) string {
	switch capability {
	case CapNetworkPing:
		return model.AgentCapabilityNetworkPing
	case CapNetworkTrace:
		switch mode {
		case model.TraceModeUDP:
			return model.AgentCapabilityNetworkTraceUDP
		case model.TraceModeTCP:
			return model.AgentCapabilityNetworkTraceTCP
		default:
			return model.AgentCapabilityNetworkTraceICMP
		}
	case CapNetworkTCPProbe:
		return model.AgentCapabilityNetworkTCPProbe
	case CapNetworkDNSLookup:
		return model.AgentCapabilityNetworkDNSLookup
	default:
		return model.AgentCapabilityNetworkHTTPProbe
	}
}

func hasString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// handleNetwork turns one structured SDK call into one structured, signed
// Agent task through the Controller. The plugin never sees task IDs, task
// types or the Agent control channel.
func (s *Service) handleNetwork(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	var common diagnosticCommon
	{
		var probe map[string]json.RawMessage
		if json.Unmarshal(bytes.TrimSpace(orEmptyObject(raw)), &probe) != nil {
			return nil, "", nil, Fail(CodeInvalidArgument, "arguments must be an object")
		}
		_ = json.Unmarshal(probe["server_id"], &common.ServerID)
		_ = json.Unmarshal(probe["ip_family"], &common.IPFamily)
	}
	serverID, err := parseServerArg(common.ServerID)
	if err != nil {
		return nil, "", nil, err
	}
	resource := "server:" + common.ServerID
	server, err := s.serverInScope(ctx, call, call.spec.Name, serverID)
	if err != nil {
		return nil, resource, nil, err
	}
	now := s.now()
	envelope := model.NetworkDiagnosticEnvelope{
		ProtocolVersion: model.NetworkDiagnosticProtocolVersion, OperationID: "pop_" + randomToken(12), Origin: model.NetworkDiagnosticOriginPlugin,
		RunID: call.run.UUID, PluginID: call.run.PluginKey, InstanceID: call.instance.ID, IssuedAt: now, ExpiresAt: now.Add(agentStartWindow),
	}
	payload, mode, detail, err := buildDiagnosticPayload(call.spec.Name, raw, envelope)
	if err != nil {
		return nil, resource, detail, err
	}
	if !server.Online {
		return nil, resource, detail, Fail(CodeServerOffline, "server "+common.ServerID+" is offline; retry on a later run")
	}
	if !hasString(server.Capabilities, model.AgentCapabilityNetworkDiagnostics) || !hasString(server.Capabilities, requiredAgentCapability(call.spec.Name, mode)) {
		return nil, resource, detail, Fail(CodeUnsupportedCapability, "the Agent on this server does not support this diagnostic; update the Agent")
	}
	if !server.PluginsGate {
		return nil, resource, detail, Fail(CodeAgentPolicyDenied, "the server's local Agent policy does not allow plugin operations (obag remote-access allow plugins)")
	}
	limits, _ := EffectiveLimits(call.loaded.manifest.Limits, s.Settings(ctx).MaxTimeout)
	allowed, err := s.store.ConsumePluginRunBudget(ctx, call.run.UUID, call.run.LeaseGeneration, "agent", limits.AgentOperations)
	if err != nil {
		return nil, resource, detail, Fail(CodeInternal, "internal error")
	}
	if !allowed {
		return nil, resource, detail, Fail(CodeLimitExceeded, "Agent operation budget for this run is exhausted")
	}
	deadline, _ := ctx.Deadline()
	result, err := s.host.RunNetworkDiagnostic(ctx, DiagnosticRequest{Capability: call.spec.Name, ServerID: serverID, RunUUID: call.run.UUID, PluginKey: call.run.PluginKey, InstanceID: call.instance.ID, Payload: payload, Deadline: deadline})
	if err != nil {
		return nil, resource, detail, err
	}
	normalized, summary, err := normalizeDiagnosticResult(call.spec.Name, envelope.OperationID, result)
	for key, value := range summary {
		detail[key] = value
	}
	return normalized, resource, detail, err
}

func orEmptyObject(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func buildDiagnosticPayload(capability string, raw json.RawMessage, envelope model.NetworkDiagnosticEnvelope) (json.RawMessage, string, map[string]any, error) {
	detail := map[string]any{}
	switch capability {
	case CapNetworkPing:
		var args struct {
			diagnosticCommon
			Target     string `json:"target"`
			Count      *int   `json:"count"`
			IntervalMS *int   `json:"interval_ms"`
			TimeoutMS  *int   `json:"timeout_ms"`
			PacketSize *int   `json:"packet_size"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, "", detail, err
		}
		target, err := ValidateNetworkTarget(args.Target)
		if err != nil {
			return nil, "", detail, err
		}
		detail["target"] = target
		family, err := normalizeFamily(args.IPFamily)
		if err != nil {
			return nil, "", detail, err
		}
		count, err := intDefault(args.Count, 4, 1, model.PingMaxCount, "count")
		if err != nil {
			return nil, "", detail, err
		}
		interval, err := intDefault(args.IntervalMS, 1000, model.PingMinIntervalMS, model.PingMaxIntervalMS, "interval_ms")
		if err != nil {
			return nil, "", detail, err
		}
		timeout, err := intDefault(args.TimeoutMS, 1000, model.PingMinTimeoutMS, model.PingMaxTimeoutMS, "timeout_ms")
		if err != nil {
			return nil, "", detail, err
		}
		size, err := intDefault(args.PacketSize, 56, 0, model.PingMaxPacketSize, "packet_size")
		if err != nil {
			return nil, "", detail, err
		}
		if count*interval+timeout > model.NetworkDiagnosticMaxDurationMS-5000 {
			return nil, "", detail, Fail(CodeInvalidArgument, "count × interval_ms + timeout_ms exceeds the 30 s ping budget")
		}
		return mustMarshal(model.NetworkPingTaskPayload{NetworkDiagnosticEnvelope: envelope, Target: target, IPFamily: family, Count: count, IntervalMS: interval, TimeoutMS: timeout, PacketSize: size}), "", detail, nil
	case CapNetworkTrace:
		var args struct {
			diagnosticCommon
			Target          string `json:"target"`
			Mode            string `json:"mode"`
			Port            *int   `json:"port"`
			MaxHops         *int   `json:"max_hops"`
			QueriesPerHop   *int   `json:"queries_per_hop"`
			PerHopTimeoutMS *int   `json:"per_hop_timeout_ms"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, "", detail, err
		}
		target, err := ValidateNetworkTarget(args.Target)
		if err != nil {
			return nil, "", detail, err
		}
		detail["target"] = target
		family, err := normalizeFamily(args.IPFamily)
		if err != nil {
			return nil, "", detail, err
		}
		mode := strings.TrimSpace(args.Mode)
		if mode == "" {
			mode = model.TraceModeICMP
		}
		port := 0
		switch mode {
		case model.TraceModeICMP:
			if args.Port != nil {
				return nil, mode, detail, Fail(CodeInvalidArgument, "port is only valid for udp and tcp traces")
			}
		case model.TraceModeUDP:
			if port, err = intDefault(args.Port, 33434, 1, 65535, "port"); err != nil {
				return nil, mode, detail, err
			}
		case model.TraceModeTCP:
			if port, err = intDefault(args.Port, 443, 1, 65535, "port"); err != nil {
				return nil, mode, detail, err
			}
		default:
			return nil, mode, detail, Fail(CodeInvalidArgument, "mode must be icmp, udp or tcp")
		}
		detail["mode"] = mode
		hops, err := intDefault(args.MaxHops, 20, 1, model.TraceMaxHops, "max_hops")
		if err != nil {
			return nil, mode, detail, err
		}
		queries, err := intDefault(args.QueriesPerHop, 2, 1, model.TraceMaxQueriesPerHop, "queries_per_hop")
		if err != nil {
			return nil, mode, detail, err
		}
		perHop, err := intDefault(args.PerHopTimeoutMS, 1000, model.TraceMinHopTimeoutMS, model.TraceMaxHopTimeoutMS, "per_hop_timeout_ms")
		if err != nil {
			return nil, mode, detail, err
		}
		return mustMarshal(model.NetworkTraceTaskPayload{NetworkDiagnosticEnvelope: envelope, Target: target, IPFamily: family, Mode: mode, Port: port, MaxHops: hops, QueriesPerHop: queries, PerHopTimeoutMS: perHop}), mode, detail, nil
	case CapNetworkTCPProbe:
		var args struct {
			diagnosticCommon
			Host      string `json:"host"`
			Port      int    `json:"port"`
			TimeoutMS *int   `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, "", detail, err
		}
		host, err := ValidateNetworkTarget(args.Host)
		if err != nil {
			return nil, "", detail, err
		}
		detail["target"] = host
		if args.Port < 1 || args.Port > 65535 {
			return nil, "", detail, Fail(CodeInvalidArgument, "port must be between 1 and 65535")
		}
		detail["port"] = args.Port
		family, err := normalizeFamily(args.IPFamily)
		if err != nil {
			return nil, "", detail, err
		}
		timeout, err := intDefault(args.TimeoutMS, 3000, 100, model.TCPProbeMaxTimeoutMS, "timeout_ms")
		if err != nil {
			return nil, "", detail, err
		}
		return mustMarshal(model.NetworkTCPProbeTaskPayload{NetworkDiagnosticEnvelope: envelope, Host: host, Port: args.Port, IPFamily: family, TimeoutMS: timeout}), "", detail, nil
	case CapNetworkDNSLookup:
		var args struct {
			ServerID    string   `json:"server_id"`
			Name        string   `json:"name"`
			RecordTypes []string `json:"record_types"`
			TimeoutMS   *int     `json:"timeout_ms"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, "", detail, err
		}
		name := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(args.Name), "."))
		if !ValidHostname(name) {
			return nil, "", detail, Fail(CodeTargetNotAllowed, "name must be a public DNS name")
		}
		detail["target"] = name
		types := []string{}
		seen := map[string]bool{}
		for _, item := range args.RecordTypes {
			item = strings.ToUpper(strings.TrimSpace(item))
			if item != "A" && item != "AAAA" {
				return nil, "", detail, Fail(CodeInvalidArgument, "record_types may only contain A and AAAA")
			}
			if !seen[item] {
				seen[item] = true
				types = append(types, item)
			}
		}
		if len(types) == 0 {
			types = []string{"A", "AAAA"}
		}
		timeout, err := intDefault(args.TimeoutMS, 3000, 100, model.DNSLookupMaxTimeoutMS, "timeout_ms")
		if err != nil {
			return nil, "", detail, err
		}
		return mustMarshal(model.NetworkDNSLookupTaskPayload{NetworkDiagnosticEnvelope: envelope, Name: name, RecordTypes: types, TimeoutMS: timeout}), "", detail, nil
	default:
		var args struct {
			diagnosticCommon
			URL             string `json:"url"`
			Method          string `json:"method"`
			TimeoutMS       *int   `json:"timeout_ms"`
			FollowRedirects bool   `json:"follow_redirects"`
		}
		if err := decodeArgs(raw, &args); err != nil {
			return nil, "", detail, err
		}
		target, err := ValidateProbeURL(args.URL)
		if err != nil {
			return nil, "", detail, err
		}
		detail["target"] = target.Host
		method := strings.ToUpper(strings.TrimSpace(args.Method))
		if method == "" {
			method = "GET"
		}
		if method != "GET" && method != "HEAD" {
			return nil, "", detail, Fail(CodeInvalidArgument, "httpProbe only supports GET and HEAD")
		}
		family, err := normalizeFamily(args.IPFamily)
		if err != nil {
			return nil, "", detail, err
		}
		timeout, err := intDefault(args.TimeoutMS, 10000, 500, model.HTTPProbeMaxTimeoutMS, "timeout_ms")
		if err != nil {
			return nil, "", detail, err
		}
		return mustMarshal(model.NetworkHTTPProbeTaskPayload{NetworkDiagnosticEnvelope: envelope, URL: target.String(), Method: method, IPFamily: family, TimeoutMS: timeout, FollowRedirects: args.FollowRedirects}), "", detail, nil
	}
}

// ValidateProbeURL accepts http(s) URLs to a public DNS name or public IP
// without credentials or fragments.
func ValidateProbeURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > model.NetworkURLMaxBytes || strings.ContainsAny(raw, "#\r\n\t ") {
		return nil, Fail(CodeInvalidArgument, "url is required (at most 2048 bytes, no fragment)")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.Opaque != "" {
		return nil, Fail(CodeInvalidArgument, "url must be an absolute http(s) URL without credentials")
	}
	if _, err := ValidateNetworkTarget(u.Hostname()); err != nil {
		return nil, err
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, Fail(CodeInvalidArgument, "url port is invalid")
		}
	}
	return u, nil
}

// normalizeDiagnosticResult strictly decodes the Agent result into the
// fixed result model; anything else is refused rather than passed through.
func normalizeDiagnosticResult(capability, operationID string, raw json.RawMessage) (any, map[string]any, error) {
	decode := func(target any) error {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if len(raw) > 256<<10 || dec.Decode(target) != nil {
			return Fail(CodeOperationFailed, "the Agent returned an invalid diagnostic result")
		}
		return nil
	}
	summary := map[string]any{}
	switch capability {
	case CapNetworkPing:
		var result model.NetworkPingResult
		if err := decode(&result); err != nil || result.OperationID != operationID || len(result.Samples) > model.PingMaxCount {
			return nil, summary, Fail(CodeOperationFailed, "the Agent returned an invalid ping result")
		}
		summary["received"], summary["sent"] = result.Received, result.Sent
		return result, summary, nil
	case CapNetworkTrace:
		var result model.NetworkTraceResult
		if err := decode(&result); err != nil || result.OperationID != operationID || len(result.Hops) > model.TraceMaxHops {
			return nil, summary, Fail(CodeOperationFailed, "the Agent returned an invalid trace result")
		}
		for _, hop := range result.Hops {
			if len(hop.Probes) > model.TraceMaxQueriesPerHop || len(hop.Addresses) > model.TraceMaxQueriesPerHop {
				return nil, summary, Fail(CodeOperationFailed, "the Agent returned an invalid trace result")
			}
		}
		summary["reached"], summary["hops"] = result.Reached, len(result.Hops)
		return result, summary, nil
	case CapNetworkTCPProbe:
		var result model.NetworkTCPProbeResult
		if err := decode(&result); err != nil || result.OperationID != operationID {
			return nil, summary, Fail(CodeOperationFailed, "the Agent returned an invalid TCP probe result")
		}
		summary["connected"] = result.Connected
		return result, summary, nil
	case CapNetworkDNSLookup:
		var result model.NetworkDNSLookupResult
		if err := decode(&result); err != nil || result.OperationID != operationID || len(result.Records) > 64 {
			return nil, summary, Fail(CodeOperationFailed, "the Agent returned an invalid DNS result")
		}
		summary["records"] = len(result.Records)
		return result, summary, nil
	default:
		var result model.NetworkHTTPProbeResult
		if err := decode(&result); err != nil || result.OperationID != operationID {
			return nil, summary, Fail(CodeOperationFailed, "the Agent returned an invalid HTTP probe result")
		}
		summary["status"] = result.StatusCode
		return result, summary, nil
	}
}
