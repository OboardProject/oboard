package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

type SingBoxConfig struct {
	Log       map[string]any         `json:"log,omitempty"`
	DNS       map[string]any         `json:"dns,omitempty"`
	Endpoints []map[string]any       `json:"endpoints,omitempty"`
	Inbounds  []map[string]any       `json:"inbounds"`
	Outbounds []map[string]any       `json:"outbounds"`
	Route     map[string]any         `json:"route,omitempty"`
	OBoard    *OBoardRuntimeMetadata `json:"_oboard,omitempty"`
}

type OBoardRuntimeMetadata struct {
	RateLimits      OBoardRateLimits       `json:"rate_limits,omitempty"`
	ConnectionAudit *OBoardConnectionAudit `json:"connection_audit,omitempty"`
	TrustedForward  *OBoardTrustedForward  `json:"trusted_forward,omitempty"`
}

type OBoardTrustedForward struct {
	Receivers []OBoardTrustedForwardReceiver `json:"receivers,omitempty"`
}

type OBoardTrustedForwardReceiver struct {
	Version             int    `json:"version"`
	ID                  string `json:"id"`
	PathID              int64  `json:"path_id"`
	InboundTag          string `json:"inbound_tag"`
	Network             string `json:"network"`
	Listen              string `json:"listen"`
	ListenPort          int    `json:"listen_port"`
	Target              string `json:"target"`
	TargetPort          int    `json:"target_port"`
	Key                 string `json:"key"`
	MaxClockSkewSeconds int    `json:"max_clock_skew_seconds"`
}

type OBoardConnectionAudit struct {
	Enabled bool `json:"enabled"`
}

type OBoardRateLimits struct {
	Users    map[string]OBoardUserRuntimeLimit `json:"users,omitempty"`
	Inbounds map[string]OBoardUserRuntimeLimit `json:"inbounds,omitempty"`
}

type OBoardUserRuntimeLimit struct {
	UserID            int64  `json:"user_id,omitempty"`
	InboundID         int64  `json:"inbound_id,omitempty"`
	PathID            int64  `json:"path_id,omitempty"`
	Billable          bool   `json:"billable"`
	SpeedLimitMbps    int    `json:"speed_limit_mbps,omitempty"`
	TrafficLimitBytes int64  `json:"traffic_limit_bytes,omitempty"`
	UsedBaselineBytes int64  `json:"used_baseline_bytes,omitempty"`
	LeaseBytes        int64  `json:"lease_bytes,omitempty"`
	ResetLeaseBytes   int64  `json:"reset_lease_bytes,omitempty"`
	LeaseEnforced     bool   `json:"lease_enforced,omitempty"`
	PeriodKey         string `json:"period_key,omitempty"`
	PeriodStart       string `json:"period_start,omitempty"`
	PeriodEnd         string `json:"period_end,omitempty"`
	ResetMode         string `json:"reset_mode,omitempty"`
	ResetDay          int    `json:"reset_day,omitempty"`
	Timezone          string `json:"timezone,omitempty"`
	QuotaState        string `json:"quota_state,omitempty"`
	EnforcementMode   string `json:"enforcement_mode,omitempty"`
}

type Adapter interface {
	Protocol() model.Protocol
	ValidateInbound(model.Inbound) error
	ValidateOutbound(model.Outbound) error
	Inbound(model.Inbound, []model.User) (map[string]any, error)
	Outbound(model.Outbound, *model.User) (map[string]any, error)
	SubscriptionNode(model.User, model.Inbound, model.Server) (map[string]any, error)
}

type ConfigOptions struct {
	RoutingRules      []model.RoutingRule
	ExternalOutbounds []model.ExternalOutbound
	ProxyPaths        []model.ProxyPath
	ProxyPathSteps    []model.ProxyPathStep
	Servers           []model.Server
	Inbounds          []model.Inbound
	WARPProfiles      []model.WARPProfile
	InboundUsers      []model.InboundUser
	UserGroups        []model.UserGroup
	UserGroupMembers  []model.UserGroupMember
	TrafficPolicies   map[int64]model.TrafficRuntimePolicy
	// PortLedger supplies persisted generated-listener ports. When nil every
	// generated port is derived fresh, which keeps pure-Core callers and fixtures
	// working without a database.
	PortLedger *ProxyPathPortLedger
}

func AdapterFor(protocol model.Protocol) (Adapter, error) {
	switch protocol {
	case model.ProtocolVLESS:
		return vlessAdapter{}, nil
	case model.ProtocolHY2:
		return hy2Adapter{}, nil
	case model.ProtocolAnyTLS:
		return anyTLSAdapter{}, nil
	case model.ProtocolSS:
		return ssAdapter{}, nil
	case model.ProtocolMieru:
		return mieruAdapter{}, nil
	case model.ProtocolSocks:
		return nil, fmt.Errorf("socks is only supported as imported outbound")
	default:
		return nil, fmt.Errorf("unsupported protocol %q", protocol)
	}
}

func ValidatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	return nil
}

const MieruMaxPorts = 64

func MieruInboundPorts(inbound model.Inbound) ([]int, error) {
	if inbound.Protocol != model.ProtocolMieru {
		if err := ValidatePort(inbound.Port); err != nil {
			return nil, err
		}
		return []int{inbound.Port}, nil
	}
	return mieruPortsFromConfig(inbound.Port, inbound.ConfigJSON, "listen_ports")
}

func MieruOutboundPorts(port int, configJSON string) ([]int, error) {
	return mieruPortsFromConfig(port, configJSON, "server_ports")
}

func NormalizeMieruPortConfig(primary int, configJSON, rangeKey string) (int, string, error) {
	extra, err := decodeMieruConfig(configJSON)
	if err != nil {
		return 0, "", err
	}
	ports, err := mieruPortsFromValue(primary, extra[rangeKey])
	if err != nil {
		return 0, "", err
	}
	primary = ports[0]
	ranges := compressMieruPorts(ports[1:])
	if len(ranges) == 0 {
		delete(extra, rangeKey)
	} else {
		extra[rangeKey] = ranges
	}
	normalized, err := json.Marshal(extra)
	if err != nil {
		return 0, "", err
	}
	return primary, string(normalized), nil
}

func MieruInboundTransport(inbound model.Inbound) string {
	if inbound.Protocol != model.ProtocolMieru {
		return ""
	}
	return normalizeMieruTransport(stringValue(parseExtra(inbound.ConfigJSON), "transport", "TCP"))
}

func mieruPortsFromConfig(primary int, configJSON, rangeKey string) ([]int, error) {
	extra, err := decodeMieruConfig(configJSON)
	if err != nil {
		return nil, err
	}
	return mieruPortsFromValue(primary, extra[rangeKey])
}

func decodeMieruConfig(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return nil, err
	}
	if extra == nil {
		extra = map[string]any{}
	}
	return extra, nil
}

func mieruPortsFromValue(primary int, value any) ([]int, error) {
	ports := map[int]struct{}{}
	if primary != 0 {
		if err := ValidatePort(primary); err != nil {
			return nil, err
		}
		ports[primary] = struct{}{}
	}
	ranges, err := mieruPortRangeStrings(value)
	if err != nil {
		return nil, err
	}
	for _, value := range ranges {
		startText, endText, found := strings.Cut(value, "-")
		if !found || startText == "" || endText == "" || strings.Contains(endText, "-") || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("invalid mieru port range %q", value)
		}
		start, startErr := strconv.Atoi(startText)
		end, endErr := strconv.Atoi(endText)
		if startErr != nil || endErr != nil || ValidatePort(start) != nil || ValidatePort(end) != nil || start > end {
			return nil, fmt.Errorf("invalid mieru port range %q", value)
		}
		for port := start; port <= end; port++ {
			ports[port] = struct{}{}
			if len(ports) > MieruMaxPorts {
				return nil, fmt.Errorf("mieru supports at most %d unique ports", MieruMaxPorts)
			}
		}
	}
	if len(ports) == 0 {
		return nil, errors.New("mieru requires at least one port")
	}
	out := make([]int, 0, len(ports))
	for port := range ports {
		out = append(out, port)
	}
	sort.Ints(out)
	return out, nil
}

func mieruPortRangeStrings(value any) ([]string, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{typed}, nil
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, errors.New("mieru port ranges must contain only strings")
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, errors.New("mieru port ranges must be a string or string array")
	}
}

func compressMieruPorts(ports []int) []string {
	if len(ports) == 0 {
		return nil
	}
	out := make([]string, 0, len(ports))
	start := ports[0]
	end := start
	flush := func() {
		out = append(out, fmt.Sprintf("%d-%d", start, end))
	}
	for _, port := range ports[1:] {
		if port == end+1 {
			end = port
			continue
		}
		flush()
		start, end = port, port
	}
	flush()
	return out
}

func normalizeMieruTransport(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "UDP":
		return "UDP"
	default:
		return "TCP"
	}
}

func ValidateListenIP(ip string) error {
	if ip == "" || ip == "::" || ip == "0.0.0.0" {
		return nil
	}
	if net.ParseIP(ip) == nil {
		return fmt.Errorf("invalid listen ip %q", ip)
	}
	return nil
}

// EffectiveListenIP returns the address a server's sing-box listeners bind to.
// A stored empty or "0.0.0.0" value means "auto": when the server has a public
// IPv6 address, "::" is used because a wildcard IPv6 socket is dual stack on
// Linux and serves both families from one port; otherwise "0.0.0.0" keeps
// IPv4-only hosts working. Explicit "::" or specific addresses are preserved.
func EffectiveListenIP(server model.Server, stored string) string {
	value := strings.TrimSpace(stored)
	if value != "" && value != "0.0.0.0" {
		return value
	}
	if ip := net.ParseIP(strings.TrimSpace(server.PublicIPv6)); ip != nil && ip.To4() == nil {
		return "::"
	}
	return "0.0.0.0"
}

// ValidateSafeHost accepts an IP or DNS hostname suitable for network probes
// (ping/curl/ssh endpoint). It rejects option-like values and userinfo forms.
func ValidateSafeHost(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("host is required")
	}
	if len(host) > 253 {
		return errors.New("host is too long")
	}
	if strings.HasPrefix(host, "-") || strings.Contains(host, "://") || strings.Contains(host, "@") || strings.Contains(host, "/") {
		return errors.New("host contains unsafe characters")
	}
	for _, r := range host {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return errors.New("host contains unsafe characters")
		}
	}
	check := host
	if strings.HasPrefix(check, "[") {
		end := strings.Index(check, "]")
		if end <= 1 {
			return errors.New("invalid IPv6 host")
		}
		check = check[1:end]
	}
	if ip := net.ParseIP(check); ip != nil {
		return nil
	}
	for _, label := range strings.Split(check, ".") {
		if label == "" || len(label) > 63 {
			return errors.New("invalid hostname label")
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("invalid hostname label")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return errors.New("hostname contains invalid characters")
		}
	}
	return nil
}

func ValidateNetworkInterfaceName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if len(name) > 15 {
		return errors.New("interface name is too long")
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' || r == ':' {
			continue
		}
		return errors.New("interface name contains invalid characters")
	}
	return nil
}

// ValidateRoutingMatchJSON validates the structured destination-port fields
// exposed by OBoard while leaving the rest of sing-box's rule surface intact.
func ValidateRoutingMatchJSON(raw string) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var match map[string]any
	if err := decoder.Decode(&match); err != nil {
		return err
	}
	if value, ok := match["port"]; ok {
		if err := validateRoutingPorts(value); err != nil {
			return fmt.Errorf("port: %w", err)
		}
	}
	if value, ok := match["port_range"]; ok {
		if err := validateRoutingPortRanges(value); err != nil {
			return fmt.Errorf("port_range: %w", err)
		}
	}
	return nil
}

func validateRoutingPorts(value any) error {
	values := []any{value}
	if list, ok := value.([]any); ok {
		values = list
	}
	if len(values) == 0 {
		return errors.New("must not be empty")
	}
	for _, value := range values {
		number, ok := value.(json.Number)
		if !ok {
			return errors.New("must contain only integer ports")
		}
		port, err := strconv.Atoi(number.String())
		if err != nil || ValidatePort(port) != nil {
			return fmt.Errorf("invalid destination port %q", number.String())
		}
	}
	return nil
}

func validateRoutingPortRanges(value any) error {
	values := []any{value}
	if list, ok := value.([]any); ok {
		values = list
	}
	if len(values) == 0 {
		return errors.New("must not be empty")
	}
	for _, value := range values {
		raw, ok := value.(string)
		if !ok {
			return errors.New("must contain only start:end strings")
		}
		startRaw, endRaw, ok := strings.Cut(strings.TrimSpace(raw), ":")
		start, startErr := strconv.Atoi(startRaw)
		end, endErr := strconv.Atoi(endRaw)
		if !ok || startErr != nil || endErr != nil || ValidatePort(start) != nil || ValidatePort(end) != nil || start > end {
			return fmt.Errorf("invalid destination port range %q", raw)
		}
	}
	return nil
}

func ValidatePortRange(start, end int) error {
	if err := ValidatePort(start); err != nil {
		return err
	}
	if err := ValidatePort(end); err != nil {
		return err
	}
	if start > end {
		return errors.New("port range start must be <= end")
	}
	return nil
}

func GenerateServerConfigWithOptions(server model.Server, inbounds []model.Inbound, outbounds []model.Outbound, dnsState *DNSConfigState, users []model.User, opts ConfigOptions) (string, error) {
	pathInboundByID := map[int64]model.Inbound{}
	for _, inbound := range opts.Inbounds {
		pathInboundByID[inbound.ID] = inbound
	}
	pathStepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range opts.ProxyPathSteps {
		pathStepsByPath[step.PathID] = append(pathStepsByPath[step.PathID], step)
	}
	if err := validateProxyPathTransportSet(opts.ProxyPaths, pathStepsByPath, pathInboundByID); err != nil {
		return "", err
	}
	pathWARPServers, err := ProxyPathWARPServerIDs(opts.ProxyPaths, opts.ProxyPathSteps, opts.Inbounds)
	if err != nil {
		return "", err
	}
	warpReferenced := pathWARPServers[server.ID]
	config := SingBoxConfig{
		Log:       map[string]any{"level": "warn", "timestamp": true},
		Inbounds:  []map[string]any{},
		Outbounds: []map[string]any{{"type": "direct", "tag": "direct"}, {"type": "block", "tag": "block"}},
		Route:     map[string]any{"final": "direct"},
	}
	if server.ConnectionAuditEnabled {
		config.OBoard = &OBoardRuntimeMetadata{ConnectionAudit: &OBoardConnectionAudit{Enabled: true}}
	}
	dns, err := BuildDNSConfig(server, dnsState)
	if err != nil {
		return "", err
	}
	config.DNS = dns
	config.Route["default_domain_resolver"] = defaultDomainResolver(dns, server)
	for _, inbound := range inbounds {
		if inbound.ServerID != server.ID || !inbound.Enabled {
			continue
		}
		// SSH is a user-facing Agent service, not a sing-box listener. Its
		// dedicated plan is applied alongside this generated core config.
		if inbound.Protocol == model.ProtocolSSH {
			continue
		}
		if inboundUsesTransparentProcessing(inbound.ID, opts.ProxyPaths, opts.ProxyPathSteps) {
			continue
		}
		if err := validateServerUDPForInbound(server, inbound); err != nil {
			return "", err
		}
		adapter, err := AdapterFor(inbound.Protocol)
		if err != nil {
			return "", err
		}
		baseInboundUsers := usersForInbound(inbound, users, opts.InboundUsers)
		inboundUsers := baseInboundUsers
		if branchUsers := proxyPathBranchUsersForInbound(inbound, baseInboundUsers, opts.ProxyPaths, opts.ProxyPathSteps); len(branchUsers) > 0 {
			inboundUsers = branchUsers
		}
		addRuntimeLimitsForInbound(&config, inbound, inboundUsers, opts)
		inboundUsers = append(inboundUsers, pathLinkUsersForInbound(inbound, opts.ProxyPaths, opts.ProxyPathSteps)...)
		if len(inboundUsers) == 0 {
			placeholderUsers, err := placeholderUsersForInbound(inbound, server.ChainSecret)
			if err != nil {
				return "", err
			}
			inboundUsers = placeholderUsers
		}
		if !InboundSupportsMultipleUsers(inbound) && len(inboundUsers) > 1 {
			return "", fmt.Errorf("inbound %s supports only one user", inbound.Name)
		}
		item, err := adapter.Inbound(inbound, inboundUsers)
		if err != nil {
			return "", err
		}
		item["listen"] = EffectiveListenIP(server, inbound.ListenIP)
		applyServerNetworkPolicy(item, server, inbound.Protocol, true)
		config.Inbounds = append(config.Inbounds, item)
	}
	// Project the paths once and share the allocated synthetic listeners with both
	// the internal inbound builder and the outbound/rule builder. Two independent
	// derivations could pick different ports for the same hop.
	_, plannedPathInbounds, err := buildProxyPathPlansWithInbounds(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds, opts.PortLedger)
	if err != nil {
		return "", err
	}
	internalInbounds, err := buildProxyPathInternalInbounds(server, opts, users, &config, plannedPathInbounds)
	if err != nil {
		return "", err
	}
	config.Inbounds = append(config.Inbounds, internalInbounds...)
	for _, outbound := range outbounds {
		if outbound.ServerID != server.ID || !outbound.Enabled {
			continue
		}
		if err := ValidateAddressForIPStack(EffectiveIPStack(server), outbound.TargetAddress); err != nil {
			return "", markInvalidDesiredState(fmt.Errorf("outbound %s: %w", outbound.Name, err))
		}
		adapter, err := AdapterFor(outbound.Protocol)
		if err != nil {
			return "", err
		}
		item, err := adapter.Outbound(outbound, firstActiveUser(users))
		if err != nil {
			return "", err
		}
		applyServerNetworkPolicy(item, server, outbound.Protocol, false)
		config.Outbounds = append(config.Outbounds, item)
	}
	for _, external := range opts.ExternalOutbounds {
		if !external.Enabled || !externalUsableOnServer(external, server.ID) {
			continue
		}
		if err := ValidateAddressForIPStack(EffectiveIPStack(server), external.TargetAddress); err != nil {
			return "", markInvalidDesiredState(fmt.Errorf("external outbound %s: %w", external.Name, err))
		}
		item, err := externalOutboundToSingBox(external, server, firstActiveUser(users))
		if err != nil {
			return "", fmt.Errorf("external outbound %s: %w", external.Name, err)
		}
		config.Outbounds = append(config.Outbounds, item)
	}
	pathOutbounds, pathRules, err := buildProxyPathOutboundsAndRules(server, opts, users, plannedPathInbounds)
	if err != nil {
		return "", err
	}
	config.Outbounds = append(config.Outbounds, pathOutbounds...)
	for _, profile := range opts.WARPProfiles {
		if !warpReferenced || profile.ServerID != server.ID || !profile.Enabled {
			continue
		}
		if profile.Status != model.WARPStatusReady || strings.TrimSpace(profile.ConfigJSON) == "" || strings.TrimSpace(profile.ConfigJSON) == "{}" {
			config.Endpoints = append(config.Endpoints, map[string]any{"type": "wireguard", "tag": tag("warp", profile.ID), "_oboard_warp_pending": profile.ID})
			continue
		}
		item, err := warpProfileToSingBox(profile, server)
		if err != nil {
			return "", fmt.Errorf("warp profile %s: %w", profile.Name, err)
		}
		config.Endpoints = append(config.Endpoints, item)
	}
	rules, err := buildRouteRules(server, opts.RoutingRules, outbounds, opts.ExternalOutbounds)
	if err != nil {
		return "", err
	}
	if len(pathRules) > 0 {
		rules = append(pathRules, rules...)
	}
	if len(rules) > 0 {
		config.Route["rules"] = rules
	}
	if err := ValidateGeneratedSingBoxConfig(config); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func addRuntimeLimitsForInbound(config *SingBoxConfig, inbound model.Inbound, users []model.User, opts ConfigOptions) {
	addRuntimeLimitsForInboundTag(config, inbound, users, opts, tag("in", inbound.ID))
}

func addRuntimeLimitsForInboundTag(config *SingBoxConfig, inbound model.Inbound, users []model.User, opts ConfigOptions, inboundTag string) {
	if config == nil {
		return
	}
	limits := runtimeLimitsForUsers(users, opts)
	if inbound.Protocol == model.ProtocolMieru && len(limits) > 0 {
		aliases := make(map[string]OBoardUserRuntimeLimit, len(limits))
		for _, user := range users {
			if limit, ok := limits[user.Username]; ok {
				if limit.PathID == 0 {
					limit.PathID = runtimePathIDFromUsername(user.Username)
				}
				aliases[protocolAuthUsername(inbound.Protocol, user)] = limit
			}
		}
		limits = aliases
	}
	if len(limits) == 0 {
		return
	}
	if config.OBoard == nil {
		config.OBoard = &OBoardRuntimeMetadata{}
	}
	if config.OBoard.RateLimits.Users == nil {
		config.OBoard.RateLimits.Users = map[string]OBoardUserRuntimeLimit{}
	}
	for username, limit := range limits {
		limit.InboundID = inbound.ID
		if limit.PathID == 0 {
			limit.PathID = runtimePathIDFromUsername(username)
		}
		config.OBoard.RateLimits.Users[username] = limit
	}
	if len(limits) == 1 && !InboundSupportsMultipleUsers(inbound) {
		if config.OBoard.RateLimits.Inbounds == nil {
			config.OBoard.RateLimits.Inbounds = map[string]OBoardUserRuntimeLimit{}
		}
		for _, limit := range limits {
			config.OBoard.RateLimits.Inbounds[inboundTag] = limit
		}
	}
}

func runtimeLimitsForUsers(users []model.User, opts ConfigOptions) map[string]OBoardUserRuntimeLimit {
	out := map[string]OBoardUserRuntimeLimit{}
	for _, user := range users {
		if user.ID <= 0 || user.Status != "active" || strings.HasPrefix(user.Username, "__oboard_") {
			continue
		}
		policy := EffectiveUserLimitPolicy(user, opts.UserGroups, opts.UserGroupMembers)
		speed, traffic := policy.SpeedLimitMbps, policy.TrafficLimitBytes
		limit := OBoardUserRuntimeLimit{UserID: user.ID, Billable: true, ResetMode: policy.TrafficResetMode, ResetDay: policy.TrafficResetDay}
		if speed > 0 {
			limit.SpeedLimitMbps = speed
		}
		if traffic > 0 {
			limit.TrafficLimitBytes = traffic
		}
		if runtimePolicy, ok := opts.TrafficPolicies[user.ID]; ok {
			limit.UserID = runtimePolicy.UserID
			limit.Billable = runtimePolicy.Billable
			limit.UsedBaselineBytes = runtimePolicy.UsedBaselineBytes
			limit.LeaseBytes = runtimePolicy.LeaseBytes
			limit.ResetLeaseBytes = runtimePolicy.ResetLeaseBytes
			limit.LeaseEnforced = runtimePolicy.LeaseEnforced
			limit.PeriodKey = runtimePolicy.PeriodKey
			limit.PeriodStart = runtimePolicy.PeriodStart
			limit.PeriodEnd = runtimePolicy.PeriodEnd
			limit.ResetMode = runtimePolicy.ResetMode
			limit.ResetDay = runtimePolicy.ResetDay
			limit.Timezone = runtimePolicy.Timezone
			limit.QuotaState = runtimePolicy.QuotaState
			limit.EnforcementMode = runtimePolicy.EnforcementMode
			if runtimePolicy.TrafficLimitBytes > 0 {
				limit.TrafficLimitBytes = runtimePolicy.TrafficLimitBytes
			}
			if runtimePolicy.SpeedLimitMbps > 0 {
				limit.SpeedLimitMbps = runtimePolicy.SpeedLimitMbps
			}
		}
		out[user.Username] = limit
	}
	return out
}

func runtimePathIDFromUsername(username string) int64 {
	const marker = "__oboard_path_"
	idx := strings.LastIndex(username, marker)
	if idx < 0 {
		return 0
	}
	pathID, err := strconv.ParseInt(username[idx+len(marker):], 10, 64)
	if err != nil || pathID <= 0 {
		return 0
	}
	return pathID
}

func defaultDNS(final string) map[string]any {
	if final == "" {
		final = primaryRemoteDNSTag
	}
	return map[string]any{"servers": []map[string]any{{"type": "https", "tag": primaryRemoteDNSTag, "server": "cloudflare-dns.com", "server_port": 443, "path": "/dns-query", "tls": map[string]any{"server_name": "cloudflare-dns.com"}, "domain_resolver": primaryBootstrapDNSTag}, {"type": "udp", "tag": primaryBootstrapDNSTag, "server": "1.1.1.1", "server_port": 53}, {"type": "local", "tag": "local"}}, "final": final, "strategy": "prefer_ipv4"}
}

func validateServerUDPForInbound(server model.Server, inbound model.Inbound) error {
	if server.UDPInboundMode == model.UDPInboundAllow || server.UDPInboundMode == "" {
		return nil
	}
	if inbound.Protocol == model.ProtocolHY2 {
		return fmt.Errorf("server %s udp_inbound_mode=%s cannot host HY2 inbound %s because HY2 requires native UDP inbound", server.Name, server.UDPInboundMode, inbound.Name)
	}
	if inbound.Protocol == model.ProtocolMieru && MieruInboundTransport(inbound) == "UDP" {
		return fmt.Errorf("server %s udp_inbound_mode=%s cannot host UDP Mieru inbound %s", server.Name, server.UDPInboundMode, inbound.Name)
	}
	return nil
}

func applyServerNetworkPolicy(item map[string]any, server model.Server, protocol model.Protocol, inbound bool) {
	if !inbound {
		applyDialDomainResolver(item, normalizeDNSStrategy("", EffectiveIPStack(server)))
		return
	}
	if protocol == model.ProtocolSS && (server.UDPInboundMode == model.UDPInboundBlock || server.UDPInboundMode == model.UDPInboundUoT) {
		item["network"] = "tcp"
	}
}

func externalUsableOnServer(v model.ExternalOutbound, serverID int64) bool {
	if v.Scope == model.ExternalOutboundScopeServer {
		return v.ServerID != nil && *v.ServerID == serverID
	}
	if v.ServerID == nil || *v.ServerID == 0 {
		return true
	}
	return *v.ServerID == serverID
}

func externalOutboundToSingBox(v model.ExternalOutbound, server model.Server, user *model.User) (map[string]any, error) {
	return externalOutboundToSingBoxWithTag(v, server, user, tag("ext", v.ID))
}

func externalOutboundToSingBoxWithTag(v model.ExternalOutbound, server model.Server, user *model.User, outboundTag string) (map[string]any, error) {
	if strings.TrimSpace(v.ConfigJSON) != "" && strings.TrimSpace(v.ConfigJSON) != "{}" {
		var raw map[string]any
		if err := json.Unmarshal([]byte(v.ConfigJSON), &raw); err == nil && raw["type"] != nil {
			stripPrivateConfigFields(raw, v.Protocol)
			raw["tag"] = outboundTag
			if v.TargetAddress != "" {
				raw["server"] = v.TargetAddress
			}
			if v.TargetPort > 0 {
				raw["server_port"] = v.TargetPort
			}
			applyServerNetworkPolicy(raw, server, v.Protocol, false)
			return raw, nil
		}
	}
	if v.Protocol == model.ProtocolSocks {
		item := map[string]any{"type": "socks", "tag": outboundTag, "server": v.TargetAddress, "server_port": v.TargetPort}
		extra := parseExtra(v.ConfigJSON)
		applyAllowed(item, extra, "version", "username", "password", "network", "udp_over_tcp", "domain_resolver", "network_strategy", "fallback_delay")
		applyServerNetworkPolicy(item, server, v.Protocol, false)
		return item, nil
	}
	adapter, err := AdapterFor(v.Protocol)
	if err != nil {
		return nil, err
	}
	outbound := model.Outbound{ID: v.ID, ServerID: derefInt64(v.ServerID), Name: v.Name, Protocol: v.Protocol, TargetAddress: v.TargetAddress, TargetPort: v.TargetPort, ConfigJSON: v.ConfigJSON, Enabled: v.Enabled}
	item, err := adapter.Outbound(outbound, user)
	if err != nil {
		return nil, err
	}
	item["tag"] = outboundTag
	applyServerNetworkPolicy(item, server, v.Protocol, false)
	return item, nil
}

func stripPrivateConfigFields(raw map[string]any, protocol model.Protocol) {
	delete(raw, "_oboard")
	if protocol != model.ProtocolSocks && protocol != model.ProtocolMieru {
		delete(raw, "username")
	}
}

func buildProxyPathInternalInbounds(server model.Server, opts ConfigOptions, users []model.User, config *SingBoxConfig, plannedInbounds map[int64]model.Inbound) ([]map[string]any, error) {
	chainServices, err := buildProxyPathChainServices(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds, opts.PortLedger)
	if err != nil {
		return nil, err
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range opts.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	for id, inbound := range plannedInbounds {
		inboundByID[id] = inbound
	}
	serverByID := map[int64]model.Server{server.ID: server}
	for _, item := range opts.Servers {
		serverByID[item.ID] = item
	}
	serviceKeys := make([]proxyPathChainServiceKey, 0, len(chainServices))
	for key, service := range chainServices {
		inboundByID[service.Inbound.ID] = service.Inbound
		if key.ServerID == server.ID {
			serviceKeys = append(serviceKeys, key)
		}
	}
	sort.SliceStable(serviceKeys, func(i, j int) bool {
		if serviceKeys[i].Protocol == serviceKeys[j].Protocol {
			return serviceKeys[i].Profile < serviceKeys[j].Profile
		}
		return serviceKeys[i].Protocol < serviceKeys[j].Protocol
	})
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range opts.ProxyPathSteps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	paths := append([]model.ProxyPath(nil), opts.ProxyPaths...)
	sort.SliceStable(paths, func(i, j int) bool { return paths[i].ID < paths[j].ID })
	transparentGroups := buildTransparentProxyPathGroups(paths, stepsByPath)
	out := []map[string]any{}
	for _, serviceKey := range serviceKeys {
		service := chainServices[serviceKey]
		adapter, err := AdapterFor(service.Inbound.Protocol)
		if err != nil {
			return nil, err
		}
		item, err := adapter.Inbound(service.Inbound, service.Users)
		if err != nil {
			return nil, err
		}
		item["tag"] = service.Tag
		item["listen"] = EffectiveListenIP(server, service.Inbound.ListenIP)
		applyServerNetworkPolicy(item, server, service.Inbound.Protocol, true)
		out = append(out, item)
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		root, rootOK := inboundByID[path.InboundID]
		if !rootOK || !root.Enabled {
			continue
		}
		steps := append([]model.ProxyPathStep(nil), stepsByPath[path.ID]...)
		sort.SliceStable(steps, func(i, j int) bool {
			if steps[i].Position == steps[j].Position {
				return steps[i].ID < steps[j].ID
			}
			return steps[i].Position < steps[j].Position
		})
		for _, step := range steps {
			if step.NodeType != model.ProxyPathStepServerInbound || step.InboundID != nil {
				continue
			}
			if step.ServerID == nil || *step.ServerID != server.ID {
				continue
			}
			mode := step.TransportMode
			if mode == "" {
				mode = model.ProxyPathTransportSingBox
			}
			if mode != model.ProxyPathTransportPortForward {
				continue
			}
			group := transparentGroups[path.ID]
			key := proxyPathInternalInboundTag(path.ID, step.Position)
			if group != nil {
				key = proxyPathSharedTransparentInboundTag(group.InboundID, group.PrefixLength)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			// Reuse the port the deployment projection already allocated. Deriving
			// it again here would use a different occupancy set and could pick a
			// port the derived forward does not target.
			plannedID := proxyPathInternalOutboundID(path.ID, step.Position)
			if group != nil {
				plannedID = proxyPathSharedTransparentInboundID(group.InboundID, step.Position)
			}
			internal, planned := plannedInbounds[plannedID]
			if !planned {
				if group != nil {
					internal = proxyPathSharedTransparentInbound(group.InboundID, step, server, inboundByID, opts.PortLedger)
				} else {
					internal = proxyPathInternalInbound(path, step, server, inboundByID, opts.PortLedger)
				}
			}
			inboundByID[internal.ID] = internal
			if !step.ProcessingRole {
				// Transparent intermediate servers only own the managed
				// port-forward listener. Starting sing-box here would steal
				// that port and decrypt the user protocol too early.
				continue
			}
			outerInbound := internal
			outerReservation := outerInbound
			outerReservation.ID = internal.ID - (int64(1) << 44)
			inboundByID[outerReservation.ID] = outerReservation
			processingInbound := root
			processingInbound.ID = internal.ID
			processingInbound.ServerID = server.ID
			processingInbound.Name = fmt.Sprintf("%s / 处理加解密", firstNonEmpty(path.Name, root.Name))
			if group != nil {
				processingInbound.Name = fmt.Sprintf("%s / 共享处理加解密", firstNonEmpty(root.Name, fmt.Sprintf("入口 %d", root.ID)))
				processingInbound = proxyPathSharedTrustedInnerInbound(group.InboundID, group.PrefixLength, server, processingInbound, inboundByID, opts.PortLedger)
			} else {
				processingInbound = proxyPathTrustedInnerInbound(path, step, server, processingInbound, inboundByID, opts.PortLedger)
			}
			inboundByID[processingInbound.ID] = processingInbound
			baseUsers := usersForInbound(root, users, opts.InboundUsers)
			processingUsers := proxyPathBranchUsersForPath(path, root, baseUsers)
			if group != nil {
				processingUsers = nil
				for _, branch := range group.Paths {
					processingUsers = append(processingUsers, proxyPathBranchUsersForPath(branch, root, baseUsers)...)
				}
			}
			if len(processingUsers) == 0 {
				placeholderUsers, err := placeholderUsersForInbound(processingInbound, server.ChainSecret)
				if err != nil {
					return nil, err
				}
				processingUsers = placeholderUsers
			}
			if !InboundSupportsMultipleUsers(processingInbound) && len(processingUsers) > 1 {
				return nil, fmt.Errorf("processing inbound %s supports only one user", processingInbound.Name)
			}
			adapter, err := AdapterFor(processingInbound.Protocol)
			if err != nil {
				return nil, err
			}
			item, err := adapter.Inbound(processingInbound, processingUsers)
			if err != nil {
				return nil, err
			}
			item["tag"] = key
			item["listen"] = EffectiveListenIP(server, processingInbound.ListenIP)
			applyServerNetworkPolicy(item, server, processingInbound.Protocol, true)
			addRuntimeLimitsForInboundTag(config, root, processingUsers, opts, key)
			entryServer, ok := serverByID[root.ServerID]
			if !ok {
				return nil, fmt.Errorf("proxy path %s entry server %d does not exist", path.Name, root.ServerID)
			}
			if config.OBoard == nil {
				config.OBoard = &OBoardRuntimeMetadata{}
			}
			if config.OBoard.TrustedForward == nil {
				config.OBoard.TrustedForward = &OBoardTrustedForward{}
			}
			receiverID := proxyPathTrustedForwardReceiverID(path.ID, step.ID)
			receiverPathID := path.ID
			keyMaterial := proxyPathTrustedForwardKey(entryServer, server, path.ID, step.ID)
			if group != nil {
				receiverID = proxyPathSharedTrustedForwardReceiverID(group.InboundID, group.PrefixLength)
				receiverPathID = group.OwnerPathID
				keyMaterial = proxyPathSharedTrustedForwardKey(entryServer, server, group.InboundID, group.PrefixLength)
			}
			config.OBoard.TrustedForward.Receivers = append(config.OBoard.TrustedForward.Receivers, OBoardTrustedForwardReceiver{
				Version:             1,
				ID:                  receiverID,
				PathID:              receiverPathID,
				InboundTag:          key,
				Network:             string(transparentForwardProtocol(root)),
				Listen:              EffectiveListenIP(server, firstNonEmpty(outerInbound.ListenIP, server.ListenIP)),
				ListenPort:          outerInbound.Port,
				Target:              "127.0.0.1",
				TargetPort:          processingInbound.Port,
				Key:                 keyMaterial,
				MaxClockSkewSeconds: 120,
			})
			out = append(out, item)
		}
	}
	return out, nil
}

func buildProxyPathOutboundsAndRules(server model.Server, opts ConfigOptions, users []model.User, plannedInbounds map[int64]model.Inbound) ([]map[string]any, []map[string]any, error) {
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range opts.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	serverByID := map[int64]model.Server{}
	for _, item := range opts.Servers {
		serverByID[item.ID] = item
	}
	if _, ok := serverByID[server.ID]; !ok {
		serverByID[server.ID] = server
	}
	externalByID := map[int64]model.ExternalOutbound{}
	for _, item := range opts.ExternalOutbounds {
		externalByID[item.ID] = item
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range opts.ProxyPathSteps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	paths := append([]model.ProxyPath(nil), opts.ProxyPaths...)
	sort.SliceStable(paths, func(i, j int) bool { return paths[i].ID < paths[j].ID })
	transparentGroups := buildTransparentProxyPathGroups(paths, stepsByPath)
	chainServices, err := buildProxyPathChainServices(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds, opts.PortLedger)
	if err != nil {
		return nil, nil, err
	}
	pathPlans, err := BuildProxyPathPlans(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds)
	if err != nil {
		return nil, nil, err
	}
	// Adopt the projection's synthetic listeners so every hop dials the port the
	// deployment plan actually provisions.
	for id, inbound := range plannedInbounds {
		if _, ok := inboundByID[id]; !ok {
			inboundByID[id] = inbound
		}
	}
	tunnelByID := map[int64]model.Tunnel{}
	tunnelIDByStep := map[[2]int64]int64{}
	for _, plan := range pathPlans {
		for _, tunnel := range plan.Tunnels {
			tunnelByID[tunnel.ID] = tunnel
		}
		for _, step := range plan.Steps {
			if step.TunnelID != 0 {
				tunnelIDByStep[[2]int64{plan.PathID, step.ID}] = step.TunnelID
			}
		}
	}

	outbounds := []map[string]any{}
	rules := []map[string]any{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		root, ok := inboundByID[path.InboundID]
		if !ok || !root.Enabled {
			continue
		}
		steps := append([]model.ProxyPathStep(nil), stepsByPath[path.ID]...)
		sort.SliceStable(steps, func(i, j int) bool {
			if steps[i].Position == steps[j].Position {
				return steps[i].ID < steps[j].ID
			}
			return steps[i].Position < steps[j].Position
		})
		isDirect := path.Kind == model.ProxyPathKindDirect
		if !isDirect && len(steps) == 0 {
			continue
		}
		if len(steps) > 0 {
			if err := validateProxyPathForConfig(path, root, steps); err != nil {
				return nil, nil, err
			}
		}
		activeServerID := root.ServerID
		activeInboundTag := tag("in", root.ID)
		activeAuthUsers := proxyPathBranchUsernames(path, root, usersForInbound(root, users, opts.InboundUsers))
		previousTag := ""
		for _, step := range steps {
			if step.TransportMode == "" {
				step.TransportMode = model.ProxyPathTransportSingBox
			}
			if step.TransportMode == model.ProxyPathTransportPortForward {
				if targetServerID, _, ok := proxyPathStepTargetServer(step, inboundByID); ok {
					activeServerID = targetServerID
					activeInboundTag, activeAuthUsers = proxyPathStepInboundIdentity(path, step, root, targetServerID, inboundByID, users, opts, chainServices, transparentGroups[path.ID])
					previousTag = ""
				}
				continue
			}
			if step.NodeType == model.ProxyPathStepWARP {
				if activeServerID != server.ID {
					continue
				}
				profile, ok := warpProfileForServer(opts.WARPProfiles, server.ID)
				if !ok || !profile.Enabled {
					return nil, nil, fmt.Errorf("proxy path %s requires WARP on server %d", path.Name, server.ID)
				}
				if previousTag != "" {
					return nil, nil, fmt.Errorf("proxy path %s must connect WARP directly after a controlled server", path.Name)
				}
				previousTag = tag("warp", profile.ID)
				continue
			}
			if activeServerID != server.ID {
				if targetServerID, _, ok := proxyPathStepTargetServer(step, inboundByID); ok {
					activeServerID = targetServerID
					activeInboundTag, activeAuthUsers = proxyPathStepInboundIdentity(path, step, root, targetServerID, inboundByID, users, opts, chainServices, transparentGroups[path.ID])
					previousTag = ""
				}
				continue
			}
			stepTag := proxyPathStepTag(path.ID, step.Position)
			item, err := proxyPathStepOutbound(path, step, server, serverByID, inboundByID, externalByID, users, stepTag, chainServices, opts.PortLedger)
			if err != nil {
				return nil, nil, fmt.Errorf("proxy path %s step %d: %w", path.Name, step.Position, err)
			}
			if step.TransportMode == model.ProxyPathTransportTunnel {
				tunnel, ok := tunnelByID[tunnelIDByStep[[2]int64{path.ID, step.ID}]]
				if !ok {
					return nil, nil, fmt.Errorf("proxy path %s step %d: tunnel plan is missing", path.Name, step.Position)
				}
				tunnelAddress, tunnelPort, err := proxyPathTunnelDialTarget(tunnel)
				if err != nil {
					return nil, nil, fmt.Errorf("proxy path %s step %d: %w", path.Name, step.Position, err)
				}
				item["server"] = tunnelAddress
				if tunnelPort > 0 {
					item["server_port"] = tunnelPort
				}
			}
			delete(item, "detour")
			if previousTag != "" {
				item["detour"] = previousTag
			}
			outbounds = append(outbounds, item)
			previousTag = stepTag
			if step.NodeType == model.ProxyPathStepServerInbound {
				if previousTag != "" {
					rule := map[string]any{"inbound": []string{activeInboundTag}, "action": "route", "outbound": previousTag}
					if len(activeAuthUsers) > 0 {
						rule["auth_user"] = activeAuthUsers
					}
					rules = append(rules, rule)
				}
				if targetServerID, _, ok := proxyPathStepTargetServer(step, inboundByID); ok {
					activeServerID = targetServerID
					activeInboundTag, activeAuthUsers = proxyPathStepInboundIdentity(path, step, root, targetServerID, inboundByID, users, opts, chainServices, transparentGroups[path.ID])
					previousTag = ""
				}
			}
		}
		if isDirect {
			if previousTag != "" {
				return nil, nil, fmt.Errorf("直接出口分支 %s 必须结束于可控服务器", path.Name)
			}
			if activeServerID == server.ID {
				rule := map[string]any{"inbound": []string{activeInboundTag}, "action": "route", "outbound": "direct"}
				if len(activeAuthUsers) > 0 {
					rule["auth_user"] = activeAuthUsers
				}
				rules = append(rules, rule)
			}
			continue
		}
		if previousTag != "" && activeServerID == server.ID {
			rule := map[string]any{"inbound": []string{activeInboundTag}, "action": "route", "outbound": previousTag}
			if len(activeAuthUsers) > 0 {
				rule["auth_user"] = activeAuthUsers
			}
			rules = append(rules, rule)
		}
	}
	return outbounds, rules, nil
}

func validateProxyPathForConfig(path model.ProxyPath, root model.Inbound, steps []model.ProxyPathStep) error {
	processing := 0
	seenServers := map[int64]bool{root.ServerID: true}
	for _, step := range steps {
		if step.ProcessingRole {
			processing++
		}
		if step.NodeType == model.ProxyPathStepServerInbound && step.ServerID != nil && *step.ServerID != 0 {
			if seenServers[*step.ServerID] {
				return fmt.Errorf("proxy path %s contains a server loop", path.Name)
			}
			seenServers[*step.ServerID] = true
		}
	}
	if processing > 1 {
		return fmt.Errorf("proxy path %s has more than one processing node", path.Name)
	}
	return nil
}

func proxyPathStepInboundIdentity(path model.ProxyPath, step model.ProxyPathStep, root model.Inbound, targetServerID int64, inboundByID map[int64]model.Inbound, users []model.User, opts ConfigOptions, services map[proxyPathChainServiceKey]*proxyPathChainService, transparentGroup *transparentProxyPathGroup) (string, []string) {
	if transparentGroup != nil && step.Position == transparentGroup.PrefixLength {
		return proxyPathSharedTransparentInboundTag(transparentGroup.InboundID, transparentGroup.PrefixLength), proxyPathBranchUsernames(path, root, usersForInbound(root, users, opts.InboundUsers))
	}
	if step.InboundID != nil && *step.InboundID != 0 {
		inbound := inboundByID[*step.InboundID]
		user := proxyPathLinkUser(path, inbound)
		return tag("in", *step.InboundID), []string{protocolAuthUsername(inbound.Protocol, user)}
	}
	if service, ok := proxyPathChainServiceForStep(services, step, targetServerID); ok {
		user := proxyPathInternalUser(path, step)
		return service.Tag, []string{protocolAuthUsername(service.Inbound.Protocol, user)}
	}
	return proxyPathInternalInboundTag(path.ID, step.Position), proxyPathBranchUsernames(path, root, usersForInbound(root, users, opts.InboundUsers))
}

func proxyPathBranchUsernames(path model.ProxyPath, root model.Inbound, users []model.User) []string {
	branchUsers := proxyPathBranchUsersForPath(path, root, users)
	names := make([]string, 0, len(branchUsers))
	for _, user := range branchUsers {
		names = append(names, protocolAuthUsername(root.Protocol, user))
	}
	return names
}

func proxyPathInternalInboundTag(pathID int64, position int) string {
	return fmt.Sprintf("oboard-path-%d-step-%d-in", pathID, position)
}

// proxyPathInternalOutboundID derives the negative ID of a generated per-hop
// listener. Path ID and position use disjoint bit fields so a long chain cannot
// carry into the neighbouring path's range.
func proxyPathInternalOutboundID(pathID int64, position int) int64 {
	return -(int64(1)<<45 + (pathID&0xffffff)<<12 + int64(position)&0xfff)
}

func proxyPathInternalInbound(path model.ProxyPath, step model.ProxyPathStep, server model.Server, inboundByID map[int64]model.Inbound, ledger *ProxyPathPortLedger) model.Inbound {
	// The port is always allocated by Controller. Honoring a caller-supplied
	// internal_port would bypass the availability checks and let the derived plan
	// and the generated core config disagree about the processing listener.
	managedSSH := step.TransportMode == model.ProxyPathTransportTunnel && strings.EqualFold(stringValue(parseStepConfig(step.ConfigJSON), "type", ""), string(model.TunnelTypeSSH))
	port := ledger.resolve(model.ProxyPathPortKindInternal, fmt.Sprintf("%d:%d", path.ID, step.Position), server.ID, func() int {
		if managedSSH {
			return proxyPathAvailablePort(server, path.ID*149, step.Position*29, 23000, 29999, inboundByID)
		}
		return proxyPathInternalPort(server, path.ID, step.Position, inboundByID)
	})
	listenIP := EffectiveListenIP(server, server.ListenIP)
	if managedSSH {
		listenIP = "127.0.0.1"
	}
	return model.Inbound{ID: proxyPathInternalOutboundID(path.ID, step.Position), ServerID: server.ID, Name: fmt.Sprintf("%s / 第%d跳内部入口", firstNonEmpty(path.Name, "代理路径"), step.Position), Protocol: model.ProtocolVLESS, ListenIP: listenIP, Port: port, ConfigJSON: `{}`, Enabled: true}
}

func proxyPathSharedTransparentInboundID(inboundID int64, position int) int64 {
	return -(int64(1)<<43 + (inboundID&0xffffff)<<12 + int64(position)&0xfff)
}

func proxyPathSharedTransparentInboundTag(inboundID int64, position int) string {
	return fmt.Sprintf("oboard-inbound-%d-transparent-step-%d-in", inboundID, position)
}

func proxyPathSharedTransparentInbound(inboundID int64, step model.ProxyPathStep, server model.Server, inboundByID map[int64]model.Inbound, ledger *ProxyPathPortLedger) model.Inbound {
	scopeKey := fmt.Sprintf("inbound:%d:%d", inboundID, step.Position)
	port := ledger.resolve(model.ProxyPathPortKindInternal, scopeKey, server.ID, func() int {
		return proxyPathInternalPort(server, inboundID, step.Position, inboundByID)
	})
	return model.Inbound{ID: proxyPathSharedTransparentInboundID(inboundID, step.Position), ServerID: server.ID, Name: fmt.Sprintf("入口 %d / 透明第%d跳内部入口", inboundID, step.Position), Protocol: model.ProtocolVLESS, ListenIP: EffectiveListenIP(server, server.ListenIP), Port: port, ConfigJSON: `{}`, Enabled: true}
}

func proxyPathTrustedInnerInbound(path model.ProxyPath, step model.ProxyPathStep, server model.Server, outer model.Inbound, inboundByID map[int64]model.Inbound, ledger *ProxyPathPortLedger) model.Inbound {
	inner := outer
	inner.ListenIP = "127.0.0.1"
	inner.Port = ledger.resolve(model.ProxyPathPortKindTrustedInner, fmt.Sprintf("%d:%d", path.ID, step.Position), server.ID, func() int {
		return proxyPathAvailablePort(server, path.ID*193, step.Position*37, server.PortRangeStart, server.PortRangeEnd, inboundByID)
	})
	return inner
}

func proxyPathSharedTrustedInnerInbound(inboundID int64, position int, server model.Server, outer model.Inbound, inboundByID map[int64]model.Inbound, ledger *ProxyPathPortLedger) model.Inbound {
	inner := outer
	inner.ListenIP = "127.0.0.1"
	inner.Port = ledger.resolve(model.ProxyPathPortKindTrustedInner, fmt.Sprintf("inbound:%d:%d", inboundID, position), server.ID, func() int {
		return proxyPathAvailablePort(server, inboundID*193, position*37, server.PortRangeStart, server.PortRangeEnd, inboundByID)
	})
	return inner
}

func proxyPathInternalUser(path model.ProxyPath, step model.ProxyPathStep) model.User {
	seed := path.Secret
	if strings.TrimSpace(seed) == "" {
		seed = fmt.Sprintf("path:%d:step:%d", path.ID, step.Position)
	}
	return model.User{ID: proxyPathInternalOutboundID(path.ID, step.Position), Username: fmt.Sprintf("__oboard_path_%d_step_%d", path.ID, step.Position), Status: "active", ProxyUUID: deterministicUUID(fmt.Sprintf("%s:step:%d:uuid", seed, step.Position)), ProxyPassword: deterministicSecret(fmt.Sprintf("%s:step:%d:password", seed, step.Position))}
}

func proxyPathInternalPort(server model.Server, pathID int64, position int, inboundByID map[int64]model.Inbound) int {
	start, end := server.PortRangeStart, server.PortRangeEnd
	if start <= 0 || end <= 0 || start > end {
		start, end = 30000, 60000
	}
	return proxyPathAvailablePort(server, pathID*97, position*17, start, end, inboundByID)
}

func proxyPathStepOutbound(path model.ProxyPath, step model.ProxyPathStep, sourceServer model.Server, serverByID map[int64]model.Server, inboundByID map[int64]model.Inbound, externalByID map[int64]model.ExternalOutbound, users []model.User, outboundTag string, services map[proxyPathChainServiceKey]*proxyPathChainService, ledger *ProxyPathPortLedger) (map[string]any, error) {
	switch step.NodeType {
	case model.ProxyPathStepImported:
		if step.ExternalOutboundID == nil || *step.ExternalOutboundID == 0 {
			return nil, errors.New("external_outbound_id required")
		}
		external, ok := externalByID[*step.ExternalOutboundID]
		if !ok || !external.Enabled {
			return nil, fmt.Errorf("imported node %d not found or disabled", *step.ExternalOutboundID)
		}
		if !externalUsableOnServer(external, sourceServer.ID) {
			return nil, fmt.Errorf("imported node %s is not available on server %d", external.Name, sourceServer.ID)
		}
		if err := ValidateAddressForIPStack(EffectiveIPStack(sourceServer), external.TargetAddress); err != nil {
			return nil, markInvalidDesiredState(err)
		}
		return externalOutboundToSingBoxWithTag(external, sourceServer, firstActiveUser(users), outboundTag)
	case model.ProxyPathStepServerInbound:
		var inbound model.Inbound
		var targetServer model.Server
		var ok bool
		if step.InboundID != nil && *step.InboundID != 0 {
			inbound, ok = inboundByID[*step.InboundID]
			if !ok || !inbound.Enabled {
				return nil, fmt.Errorf("target inbound %d not found or disabled", *step.InboundID)
			}
			targetServer, ok = serverByID[inbound.ServerID]
		} else {
			if step.ServerID == nil || *step.ServerID == 0 {
				return nil, errors.New("server_id required")
			}
			targetServer, ok = serverByID[*step.ServerID]
			if ok {
				if service, serviceOK := proxyPathChainServiceForStep(services, step, targetServer.ID); serviceOK {
					inbound = service.Inbound
				} else if planned, plannedOK := inboundByID[proxyPathInternalOutboundID(path.ID, step.Position)]; plannedOK {
					// Prefer the listener the projection allocated for this hop.
					inbound = planned
				} else {
					inbound = proxyPathInternalInbound(path, step, targetServer, inboundByID, ledger)
				}
				inboundByID[inbound.ID] = inbound
			}
		}
		if !ok {
			return nil, fmt.Errorf("target server not found")
		}
		address, err := ResolveReachableEntryAddress(sourceServer, inbound, targetServer)
		if err != nil {
			return nil, err
		}
		targetServer.EntryAddress = address
		user := proxyPathLinkUser(path, inbound)
		if step.InboundID == nil || *step.InboundID == 0 {
			user = proxyPathInternalUser(path, step)
		}
		adapter, err := AdapterFor(inbound.Protocol)
		if err != nil {
			return nil, err
		}
		item, err := adapter.SubscriptionNode(user, inbound, targetServer)
		if err != nil {
			return nil, err
		}
		item["tag"] = outboundTag
		applyServerNetworkPolicy(item, sourceServer, inbound.Protocol, false)
		return item, nil
	default:
		return nil, fmt.Errorf("unsupported node_type %q", step.NodeType)
	}
}

func proxyPathStepTag(pathID int64, position int) string {
	return fmt.Sprintf("path-%d-step-%d", pathID, position)
}

func cloneNestedMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for k, v := range value {
		if child, ok := v.(map[string]any); ok {
			out[k] = cloneNestedMap(child)
			continue
		}
		out[k] = v
	}
	return out
}

func sanitizeInboundTLSForServer(value any) any {
	tls, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := cloneNestedMap(tls)
	if reality, ok := out["reality"].(map[string]any); ok {
		reality = cloneNestedMap(reality)
		delete(reality, "public_key")
		out["reality"] = reality
	}
	return out
}

func validateTLSPathFields(tls any, where string) error {
	m, ok := tls.(map[string]any)
	if !ok || m == nil {
		return nil
	}
	for _, key := range []string{"certificate_path", "key_path", "client_certificate_path"} {
		raw, _ := m[key].(string)
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if validManagedCertificatePath(raw, key) {
			continue
		}
		if !filepath.IsAbs(raw) || strings.Contains(filepath.Clean(raw), "..") {
			return fmt.Errorf("%s %s must be an absolute path without dot-dot segments", where, key)
		}
	}
	return nil
}

func validManagedCertificatePath(raw, key string) bool {
	const prefix = "oboard-asset://certificate/"
	if !strings.HasPrefix(raw, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(raw, prefix), "/")
	if len(parts) != 2 {
		return false
	}
	if _, err := strconv.ParseInt(parts[0], 10, 64); err != nil {
		return false
	}
	want := "fullchain.pem"
	if key == "key_path" {
		want = "privkey.pem"
	}
	return parts[1] == want
}

func sanitizeTLSForSubscription(value any) any {
	tls, ok := value.(map[string]any)
	if !ok {
		return value
	}
	out := cloneNestedMap(tls)
	for _, key := range []string{
		"certificate",
		"certificate_path",
		"key",
		"key_path",
		"acme",
		"client_certificate",
		"client_certificate_path",
		"client_certificate_public_key_sha256",
	} {
		delete(out, key)
	}
	if reality, ok := out["reality"].(map[string]any); ok {
		clientReality := map[string]any{}
		if enabled, ok := reality["enabled"].(bool); ok {
			clientReality["enabled"] = enabled
		}
		if publicKey := strings.TrimSpace(stringValue(reality, "public_key", "")); publicKey != "" {
			clientReality["enabled"] = true
			clientReality["public_key"] = publicKey
		}
		if shortID := strings.TrimSpace(stringValue(reality, "short_id", "")); shortID != "" {
			clientReality["short_id"] = shortID
		}
		if _, hasPublicKey := clientReality["public_key"]; hasPublicKey {
			out["reality"] = clientReality
			ensureClientUTLSFingerprint(out)
		} else {
			delete(out, "reality")
		}
	}
	return out
}

func subscriptionTLSForInbound(inbound model.Inbound, value any) any {
	tls := sanitizeTLSForSubscription(value)
	out, ok := tls.(map[string]any)
	if !ok || inbound.CertificateMode == "" || inbound.CertificateMode == model.CertificateModeExternal {
		return tls
	}
	serverName := strings.TrimSpace(inbound.CertificateDomain)
	if serverName == "" {
		serverName = strings.TrimSpace(inbound.DNSDomain)
	}
	if serverName != "" {
		out["server_name"] = serverName
	}
	return out
}

func ensureClientUTLSFingerprint(tls map[string]any) {
	utls, _ := tls["utls"].(map[string]any)
	if utls == nil {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		return
	}
	if _, ok := utls["enabled"]; !ok {
		utls["enabled"] = true
	}
	if strings.TrimSpace(stringFromAny(utls["fingerprint"])) == "" {
		utls["fingerprint"] = "chrome"
	}
	tls["utls"] = utls
}

func warpProfileToSingBox(v model.WARPProfile, server model.Server) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(v.ConfigJSON), &raw); err != nil {
		return nil, err
	}
	if raw["type"] == nil {
		raw["type"] = "wireguard"
	}
	raw["tag"] = tag("warp", v.ID)
	if fmt.Sprint(raw["type"]) == "wireguard" {
		normalizeWireGuardEndpoint(raw)
	}
	mtu := v.MTU
	if mtu <= 0 {
		mtu = server.MTUValue
	}
	if mtu <= 0 && EffectiveIPStack(server) == model.IPStackIPv6Only {
		mtu = 1280
	}
	if mtu > 0 {
		raw["mtu"] = mtu
	}
	applyManagedWARPDomainResolver(raw, normalizeDNSStrategy(v.DNSStrategy, EffectiveIPStack(server)))
	return raw, nil
}

func WARPOutboundTag(profileID int64) string {
	return tag("warp", profileID)
}

func warpProfileForServer(items []model.WARPProfile, serverID int64) (model.WARPProfile, bool) {
	for _, item := range items {
		if item.ServerID == serverID {
			return item, true
		}
	}
	return model.WARPProfile{}, false
}

func normalizeWireGuardEndpoint(raw map[string]any) {
	if raw == nil {
		return
	}
	if v, ok := raw["local_address"]; ok {
		raw["address"] = v
		delete(raw, "local_address")
	}
	if v, ok := raw["system_interface"]; ok {
		raw["system"] = v
		delete(raw, "system_interface")
	}
	if v, ok := raw["interface_name"]; ok {
		raw["name"] = v
		delete(raw, "interface_name")
	}
	peers := normalizeWireGuardPeers(raw)
	if len(peers) > 0 {
		if raw["reserved"] != nil && peers[0]["reserved"] == nil {
			peers[0]["reserved"] = raw["reserved"]
		}
		raw["peers"] = peers
	}
	for _, key := range []string{"server", "server_port", "peer_public_key", "pre_shared_key", "reserved", "allowed_ips", "gso", "network"} {
		delete(raw, key)
	}
}

func normalizeWireGuardPeers(raw map[string]any) []map[string]any {
	peers := make([]map[string]any, 0)
	if existing, ok := raw["peers"]; ok {
		switch values := existing.(type) {
		case []map[string]any:
			for _, peer := range values {
				peers = append(peers, normalizeWireGuardPeer(peer))
			}
		case []any:
			for _, value := range values {
				if peer, ok := value.(map[string]any); ok {
					peers = append(peers, normalizeWireGuardPeer(peer))
				}
			}
		}
	}
	if len(peers) == 0 && raw["server"] != nil {
		peer := map[string]any{}
		peer["address"] = raw["server"]
		if raw["server_port"] != nil {
			peer["port"] = raw["server_port"]
		}
		if raw["peer_public_key"] != nil {
			peer["public_key"] = raw["peer_public_key"]
		}
		for _, key := range []string{"pre_shared_key", "reserved", "allowed_ips"} {
			if raw[key] != nil {
				peer[key] = raw[key]
			}
		}
		peers = append(peers, normalizeWireGuardPeer(peer))
	}
	return peers
}

func normalizeWireGuardPeer(peer map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range peer {
		out[key] = value
	}
	if v, ok := out["server"]; ok {
		out["address"] = v
		delete(out, "server")
	}
	if v, ok := out["server_port"]; ok {
		out["port"] = v
		delete(out, "server_port")
	}
	if v, ok := out["peer_public_key"]; ok {
		out["public_key"] = v
		delete(out, "peer_public_key")
	}
	if _, ok := out["allowed_ips"]; !ok {
		out["allowed_ips"] = []string{"0.0.0.0/0", "::/0"}
	}
	return out
}

func defaultDomainResolver(dns map[string]any, server model.Server) any {
	resolver := preferredDNSResolverTag(dns)
	strategy := normalizeDNSStrategy("", EffectiveIPStack(server))
	if strategy == "" {
		return resolver
	}
	return map[string]any{"server": resolver, "strategy": strategy}
}

func preferredDNSResolverTag(dns map[string]any) string {
	fallback := "local"
	for _, server := range dnsServerItems(dns["servers"]) {
		tag, _ := server["tag"].(string)
		if tag == primaryBootstrapDNSTag {
			return tag
		}
		if tag != "" && fallback == "local" {
			fallback = tag
		}
	}
	return fallback
}

func dnsServerItems(value any) []map[string]any {
	switch servers := value.(type) {
	case []map[string]any:
		return servers
	case []any:
		out := make([]map[string]any, 0, len(servers))
		for _, value := range servers {
			if item, ok := value.(map[string]any); ok {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}

func applyDialDomainResolver(item map[string]any, defaultStrategy string) {
	if item == nil {
		return
	}
	if _, ok := item["domain_resolver"]; ok {
		return
	}
	if defaultStrategy == "" {
		return
	}
	item["domain_resolver"] = map[string]any{"server": primaryBootstrapDNSTag, "strategy": defaultStrategy}
}

func applyManagedWARPDomainResolver(item map[string]any, strategy string) {
	item["domain_resolver"] = map[string]any{"server": primaryBootstrapDNSTag, "strategy": strategy}
}

func buildRouteRules(server model.Server, rules []model.RoutingRule, outbounds []model.Outbound, external []model.ExternalOutbound) ([]map[string]any, error) {
	filtered := make([]model.RoutingRule, 0, len(rules))
	for _, rule := range rules {
		if rule.ServerID == server.ID && rule.Enabled {
			filtered = append(filtered, rule)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Priority == filtered[j].Priority {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].Priority < filtered[j].Priority
	})
	out := make([]map[string]any, 0, len(filtered))
	for _, rule := range filtered {
		item := map[string]any{}
		if strings.TrimSpace(rule.MatchJSON) != "" && strings.TrimSpace(rule.MatchJSON) != "{}" {
			if err := json.Unmarshal([]byte(rule.MatchJSON), &item); err != nil {
				return nil, fmt.Errorf("routing rule %s match_json: %w", rule.Name, err)
			}
		}
		if rule.Action == model.RouteActionInterface {
			item["action"] = "direct"
			item["bind_interface"] = rule.InterfaceName
			out = append(out, item)
			continue
		}
		tag, ok, err := routeRuleOutboundTag(rule, server, outbounds, external)
		if err != nil {
			return nil, fmt.Errorf("routing rule %s: %w", rule.Name, err)
		}
		if !ok {
			continue
		}
		item["action"] = "route"
		item["outbound"] = tag
		out = append(out, item)
	}
	return out, nil
}

func routeRuleOutboundTag(rule model.RoutingRule, server model.Server, outbounds []model.Outbound, external []model.ExternalOutbound) (string, bool, error) {
	switch rule.Action {
	case model.RouteActionDirect:
		return "direct", true, nil
	case model.RouteActionBlock:
		return "block", true, nil
	case model.RouteActionOutbound:
		if rule.OutboundID == nil {
			return "", false, errors.New("outbound_id required")
		}
		for _, outbound := range outbounds {
			if outbound.ID == *rule.OutboundID && outbound.Enabled && outbound.ServerID == server.ID {
				return tag("out", outbound.ID), true, nil
			}
		}
		return "", false, fmt.Errorf("outbound %d not found on server %d", *rule.OutboundID, server.ID)
	case model.RouteActionExternal:
		if rule.ExternalOutboundID == nil {
			return "", false, errors.New("external_outbound_id required")
		}
		for _, item := range external {
			if item.ID == *rule.ExternalOutboundID && item.Enabled && externalUsableOnServer(item, server.ID) {
				return tag("ext", item.ID), true, nil
			}
		}
		return "", false, fmt.Errorf("external outbound %d is not available on server %d", *rule.ExternalOutboundID, server.ID)
	default:
		return "", false, fmt.Errorf("unsupported route action %q", rule.Action)
	}
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func AddressFamily(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	address = strings.Trim(address, "[]")
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = strings.Trim(host, "[]")
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return "domain"
	}
	if ip.To4() != nil {
		return "ipv4"
	}
	return "ipv6"
}

func ValidateAddressForIPStack(stack model.IPStack, address string) error {
	family := AddressFamily(address)
	switch stack {
	case model.IPStackIPv4Only:
		if family == "ipv6" {
			return fmt.Errorf("ipv4_only server cannot use IPv6 literal %q", address)
		}
	case model.IPStackIPv6Only:
		if family == "ipv4" {
			return fmt.Errorf("ipv6_only server cannot use IPv4 literal %q", address)
		}
	}
	return nil
}

func ResolveReachableEntryAddress(source model.Server, inbound model.Inbound, target model.Server) (string, error) {
	if inbound.DNSSyncEnabled && strings.TrimSpace(inbound.DNSDomain) != "" {
		return strings.TrimSpace(inbound.DNSDomain), nil
	}
	mode := inbound.EntryIPMode
	if mode == "" || mode == model.EntryIPModeAuto {
		mode = target.EntryIPMode
	}
	if mode == "" || mode == model.EntryIPModeAuto {
		return resolveCompatibleServerAddress(source, target)
	}
	address := entryAddressForMode(mode, inbound.ExternalIP, target)
	return validateReachableServerAddress(source, target, address)
}

func ResolveReachableServerEntryAddress(source, target model.Server) (string, error) {
	mode := target.EntryIPMode
	if mode == "" || mode == model.EntryIPModeAuto {
		return resolveCompatibleServerAddress(source, target)
	}
	return validateReachableServerAddress(source, target, entryAddressForMode(mode, "", target))
}

func resolveCompatibleServerAddress(source, target model.Server) (string, error) {
	ipv4 := strings.TrimSpace(target.PublicIPv4)
	ipv6 := strings.TrimSpace(target.PublicIPv6)
	var candidates []string
	switch EffectiveIPStack(source) {
	case model.IPStackIPv6Only, model.IPStackPreferIPv6:
		candidates = []string{ipv6, ipv4}
	default:
		candidates = []string{ipv4, ipv6}
	}
	for _, address := range candidates {
		if address == "" {
			continue
		}
		if ValidateAddressForIPStack(EffectiveIPStack(source), address) == nil {
			return address, nil
		}
	}
	for _, address := range candidates {
		if address != "" {
			return validateReachableServerAddress(source, target, address)
		}
	}
	return "", markInvalidDesiredState(fmt.Errorf("目标服务器 %s 没有可供源服务器 %s 连接的公网入口地址", target.Name, source.Name))
}

func entryAddressForMode(mode model.EntryIPMode, externalIP string, server model.Server) string {
	switch mode {
	case model.EntryIPModeIPv4:
		return strings.TrimSpace(server.PublicIPv4)
	case model.EntryIPModeIPv6:
		return strings.TrimSpace(server.PublicIPv6)
	case model.EntryIPModeCustom:
		if strings.TrimSpace(externalIP) != "" {
			return strings.TrimSpace(externalIP)
		}
		return strings.TrimSpace(server.EntryAddress)
	default:
		return ""
	}
}

func validateReachableServerAddress(source, target model.Server, address string) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", markInvalidDesiredState(fmt.Errorf("目标服务器 %s 选择的入口地址不存在", target.Name))
	}
	if err := ValidateAddressForIPStack(EffectiveIPStack(source), address); err != nil {
		family := AddressFamily(address)
		if family == "ipv4" {
			family = "IPv4"
		} else if family == "ipv6" {
			family = "IPv6"
		}
		return "", markInvalidDesiredState(fmt.Errorf("源服务器 %s（%s）无法连接目标服务器 %s 的 %s 地址 %q；请调整目标入口地址或源服务器 IP 栈", source.Name, EffectiveIPStack(source), target.Name, family, address))
	}
	return address, nil
}

func ResolveEntryAddress(inbound model.Inbound, server model.Server) string {
	if inbound.DNSSyncEnabled && strings.TrimSpace(inbound.DNSDomain) != "" {
		return strings.TrimSpace(inbound.DNSDomain)
	}
	mode := inbound.EntryIPMode
	if mode == "" || mode == model.EntryIPModeAuto {
		mode = server.EntryIPMode
	}
	switch mode {
	case model.EntryIPModeIPv4:
		return strings.TrimSpace(server.PublicIPv4)
	case model.EntryIPModeIPv6:
		return strings.TrimSpace(server.PublicIPv6)
	case model.EntryIPModeCustom:
		if strings.TrimSpace(inbound.ExternalIP) != "" {
			return strings.TrimSpace(inbound.ExternalIP)
		}
		return strings.TrimSpace(server.EntryAddress)
	}
	return ResolveServerEntryAddress(server)
}

func ResolveServerEntryAddress(server model.Server) string {
	switch server.EntryIPMode {
	case model.EntryIPModeIPv4:
		return strings.TrimSpace(server.PublicIPv4)
	case model.EntryIPModeIPv6:
		return strings.TrimSpace(server.PublicIPv6)
	case model.EntryIPModeCustom:
		return strings.TrimSpace(server.EntryAddress)
	}
	if strings.TrimSpace(server.PublicIPv4) != "" {
		return strings.TrimSpace(server.PublicIPv4)
	}
	return strings.TrimSpace(server.PublicIPv6)
}

func activeUsers(users []model.User) []model.User {
	out := make([]model.User, 0, len(users))
	for _, user := range users {
		if user.Status == "active" {
			out = append(out, user)
		}
	}
	return out
}

func usersForInbound(inbound model.Inbound, users []model.User, bindings []model.InboundUser) []model.User {
	if bindings == nil {
		return activeUsers(users)
	}
	allowed := map[int64]bool{}
	for _, binding := range bindings {
		if binding.InboundID == inbound.ID && binding.Enabled {
			allowed[binding.UserID] = true
		}
	}
	out := make([]model.User, 0, len(allowed))
	for _, user := range users {
		if user.Status == "active" && allowed[user.ID] {
			out = append(out, user)
		}
	}
	return out
}

func placeholderUsersForInbound(inbound model.Inbound, serverSecret string) ([]model.User, error) {
	var uuid, password string
	if serverSecret = strings.TrimSpace(serverSecret); serverSecret != "" {
		seed := fmt.Sprintf("%s:placeholder:inbound:%d", serverSecret, inbound.ID)
		uuid = deterministicUUID(seed + ":uuid")
		password = deterministicSecret(seed + ":password")
	} else {
		var err error
		uuid, err = randomPlaceholderUUID()
		if err != nil {
			return nil, err
		}
		password, err = randomPlaceholderSecret(24)
		if err != nil {
			return nil, err
		}
	}
	return []model.User{{
		ID:            0,
		Username:      fmt.Sprintf("__oboard_placeholder_inbound_%d", inbound.ID),
		Status:        "active",
		ProxyUUID:     uuid,
		ProxyPassword: password,
	}}, nil
}

func pathLinkUsersForInbound(inbound model.Inbound, paths []model.ProxyPath, steps []model.ProxyPathStep) []model.User {
	pathByID := map[int64]model.ProxyPath{}
	for _, path := range paths {
		if path.Enabled {
			pathByID[path.ID] = path
		}
	}
	out := []model.User{}
	seen := map[int64]bool{}
	for _, step := range steps {
		if step.NodeType != model.ProxyPathStepServerInbound || step.InboundID == nil || *step.InboundID != inbound.ID {
			continue
		}
		path, ok := pathByID[step.PathID]
		if !ok || seen[path.ID] {
			continue
		}
		seen[path.ID] = true
		out = append(out, proxyPathLinkUser(path, inbound))
	}
	return out
}

func proxyPathBranchUsersForInbound(inbound model.Inbound, users []model.User, paths []model.ProxyPath, steps []model.ProxyPathStep) []model.User {
	if len(users) == 0 {
		return nil
	}
	pathByInbound := proxyPathsForRootInbound(inbound.ID, paths)
	if len(pathByInbound) == 0 {
		return nil
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	out := []model.User{}
	for _, path := range pathByInbound {
		if path.Kind != model.ProxyPathKindDirect && len(stepsByPath[path.ID]) == 0 {
			continue
		}
		out = append(out, proxyPathBranchUsersForPath(path, inbound, users)...)
	}
	return out
}

func proxyPathBranchUsersForPath(path model.ProxyPath, inbound model.Inbound, users []model.User) []model.User {
	if len(users) == 0 || !path.Enabled || path.InboundID != inbound.ID {
		return nil
	}
	out := make([]model.User, 0, len(users))
	for _, user := range users {
		if user.Status != "active" || strings.HasPrefix(user.Username, "__oboard_") {
			continue
		}
		out = append(out, proxyPathBranchUser(path, inbound, user))
	}
	return out
}

func proxyPathsForRootInbound(inboundID int64, paths []model.ProxyPath) []model.ProxyPath {
	out := []model.ProxyPath{}
	for _, path := range paths {
		if path.Enabled && path.InboundID == inboundID {
			out = append(out, path)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func proxyPathBranchUser(path model.ProxyPath, inbound model.Inbound, user model.User) model.User {
	seed := path.Secret
	if strings.TrimSpace(seed) == "" {
		seed = fmt.Sprintf("path:%d:inbound:%d:user:%d", path.ID, inbound.ID, user.ID)
	}
	out := user
	out.ID = user.ID
	out.Username = fmt.Sprintf("%s__oboard_path_%d", user.Username, path.ID)
	out.ProxyUUID = deterministicUUID(fmt.Sprintf("%s:branch:user:%d:uuid", seed, user.ID))
	out.ProxyPassword = deterministicSecret(fmt.Sprintf("%s:branch:user:%d:password", seed, user.ID))
	return out
}

func proxyPathLinkUser(path model.ProxyPath, inbound model.Inbound) model.User {
	seed := path.Secret
	if strings.TrimSpace(seed) == "" {
		seed = fmt.Sprintf("path:%d:inbound:%d", path.ID, inbound.ID)
	}
	uuid := deterministicUUID(seed + ":uuid")
	password := deterministicSecret(seed + ":password")
	return model.User{
		ID:            -path.ID,
		Username:      fmt.Sprintf("__oboard_path_%d_inbound_%d", path.ID, inbound.ID),
		Status:        "active",
		ProxyUUID:     uuid,
		ProxyPassword: password,
	}
}

func deterministicUUID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func deterministicSecret(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func randomPlaceholderUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func randomPlaceholderSecret(byteLen int) (string, error) {
	if byteLen <= 0 {
		byteLen = 24
	}
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func firstActiveUser(users []model.User) *model.User {
	for _, user := range users {
		if user.Status == "active" {
			u := user
			return &u
		}
	}
	return nil
}

func InboundSupportsMultipleUsers(inbound model.Inbound) bool {
	switch inbound.Protocol {
	case model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolMieru, model.ProtocolSSH:
		return true
	case model.ProtocolSS:
		method := stringValue(parseExtra(inbound.ConfigJSON), "method", "2022-blake3-aes-128-gcm")
		return shadowsocksMethodSupportsUsers(method)
	default:
		return false
	}
}

func shadowsocksMethodSupportsUsers(method string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(method)), "2022-")
}

func parseExtra(raw string) map[string]any {
	var m map[string]any
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return map[string]any{}
	}
	return m
}

func vlessUsers(users []model.User, flow string) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		if u.ProxyUUID != "" {
			item := map[string]any{"name": u.Username, "uuid": u.ProxyUUID}
			if flow != "" {
				item["flow"] = flow
			}
			out = append(out, item)
		}
	}
	return out
}

func passwordUsers(users []model.User) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		if u.ProxyPassword != "" {
			out = append(out, map[string]any{"name": u.Username, "password": u.ProxyPassword})
		}
	}
	return out
}

func mieruUsername(user model.User) string {
	if user.ID > 0 {
		if pathID := runtimePathIDFromUsername(user.Username); pathID > 0 {
			return fmt.Sprintf("oboard-u%d-p%d", user.ID, pathID)
		}
		return fmt.Sprintf("oboard-u%d", user.ID)
	}
	if user.ID < 0 {
		return fmt.Sprintf("oboard-i%x", -user.ID)
	}
	sum := sha256.Sum256([]byte(user.Username + "\x00" + user.ProxyPassword))
	return fmt.Sprintf("oboard-s%x", sum[:8])
}

func protocolAuthUsername(protocol model.Protocol, user model.User) string {
	if protocol == model.ProtocolMieru {
		return mieruUsername(user)
	}
	return user.Username
}

func mieruPasswordUsers(users []model.User) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		if user.ProxyPassword == "" {
			continue
		}
		out = append(out, map[string]any{"name": mieruUsername(user), "password": user.ProxyPassword})
	}
	return out
}

func ssPasswordUsers(users []model.User, method string) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, u := range users {
		if u.ProxyPassword == "" {
			continue
		}
		password := u.ProxyPassword
		if shadowsocksMethodSupportsUsers(method) {
			password = normalizeSS2022Key(password, method)
		}
		out = append(out, map[string]any{"name": u.Username, "password": password})
	}
	return out
}

func ss2022KeyLength(method string) int {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32
	default:
		return 16
	}
}

func normalizeSS2022Key(secret, method string) string {
	keyLen := ss2022KeyLength(method)
	if decoded, ok := decodeSS2022Key(secret, keyLen); ok {
		return base64.StdEncoding.EncodeToString(decoded)
	}
	sum := sha256.Sum256([]byte(secret))
	return base64.StdEncoding.EncodeToString(sum[:keyLen])
}

func decodeSS2022Key(secret string, keyLen int) ([]byte, bool) {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil, false
	}
	encodings := []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(secret)
		if err == nil && len(decoded) == keyLen {
			return decoded, true
		}
	}
	return nil, false
}

func applyAllowed(dst map[string]any, extra map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := extra[key]; ok && value != nil {
			dst[key] = value
		}
	}
}

var quicFieldKeys = []string{
	"init_stream_receive_window",
	"max_stream_receive_window",
	"init_conn_receive_window",
	"max_conn_receive_window",
	"max_idle_timeout",
	"max_incoming_streams",
	"disable_path_mtu_discovery",
}

func applyQUICFields(dst map[string]any, extra map[string]any) {
	applyAllowed(dst, extra, quicFieldKeys...)
}

type vlessAdapter struct{}

func (vlessAdapter) Protocol() model.Protocol { return model.ProtocolVLESS }
func (vlessAdapter) ValidateInbound(v model.Inbound) error {
	if err := ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	return ValidatePort(v.Port)
}
func (vlessAdapter) ValidateOutbound(v model.Outbound) error {
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	return ValidatePort(v.TargetPort)
}
func (a vlessAdapter) Inbound(v model.Inbound, users []model.User) (map[string]any, error) {
	if err := a.ValidateInbound(v); err != nil {
		return nil, err
	}
	extra := parseExtra(v.ConfigJSON)
	item := map[string]any{"type": "vless", "tag": tag("in", v.ID), "listen": v.ListenIP, "listen_port": v.Port, "users": vlessUsers(users, stringValue(extra, "flow", ""))}
	if tls, ok := extra["tls"]; ok {
		if err := validateTLSPathFields(tls, "inbound tls"); err != nil {
			return nil, err
		}
		item["tls"] = sanitizeInboundTLSForServer(tls)
	}
	applyAllowed(item, extra, "multiplex", "transport", "network")
	return item, nil
}
func (a vlessAdapter) Outbound(v model.Outbound, user *model.User) (map[string]any, error) {
	if err := a.ValidateOutbound(v); err != nil {
		return nil, err
	}
	uuid := ""
	if user != nil {
		uuid = user.ProxyUUID
	}
	extra := parseExtra(v.ConfigJSON)
	item := map[string]any{"type": "vless", "tag": tag("out", v.ID), "server": v.TargetAddress, "server_port": v.TargetPort, "uuid": uuid}
	if flow := stringValue(extra, "flow", ""); flow != "" {
		item["flow"] = flow
	}
	applyAllowed(item, extra, "uuid", "tls", "network", "multiplex", "transport", "packet_encoding", "domain_resolver", "network_strategy", "fallback_delay")
	return item, nil
}
func (a vlessAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	node := map[string]any{"type": "vless", "tag": inbound.Name, "server": server.EntryAddress, "server_port": inbound.Port, "uuid": user.ProxyUUID, "packet_encoding": stringValue(extra, "packet_encoding", "xudp")}
	if flow := stringValue(extra, "flow", ""); flow != "" {
		node["flow"] = flow
	}
	if tls, ok := extra["tls"]; ok {
		node["tls"] = subscriptionTLSForInbound(inbound, tls)
	}
	applyAllowed(node, extra, "transport", "network", "multiplex")
	return node, nil
}

type hy2Adapter struct{}

func (hy2Adapter) Protocol() model.Protocol { return model.ProtocolHY2 }
func (hy2Adapter) ValidateInbound(v model.Inbound) error {
	if err := ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if err := ValidatePort(v.Port); err != nil {
		return err
	}
	return requireInboundTLS(v.ConfigJSON, "hysteria2")
}
func (hy2Adapter) ValidateOutbound(v model.Outbound) error {
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	return ValidatePort(v.TargetPort)
}
func (a hy2Adapter) Inbound(v model.Inbound, users []model.User) (map[string]any, error) {
	if err := a.ValidateInbound(v); err != nil {
		return nil, err
	}
	extra := parseExtra(v.ConfigJSON)
	item := map[string]any{"type": "hysteria2", "tag": tag("in", v.ID), "listen": v.ListenIP, "listen_port": v.Port, "users": passwordUsers(users)}
	if err := validateTLSPathFields(extra["tls"], "inbound tls"); err != nil {
		return nil, err
	}
	applyAllowed(item, extra, "tls", "up_mbps", "down_mbps", "obfs", "ignore_client_bandwidth", "masquerade", "bbr_profile", "brutal_debug", "realm")
	applyQUICFields(item, extra)
	return item, nil
}
func (a hy2Adapter) Outbound(v model.Outbound, user *model.User) (map[string]any, error) {
	if err := a.ValidateOutbound(v); err != nil {
		return nil, err
	}
	pass := ""
	if user != nil {
		pass = user.ProxyPassword
	}
	extra := parseExtra(v.ConfigJSON)
	item := map[string]any{"type": "hysteria2", "tag": tag("out", v.ID), "server": v.TargetAddress, "server_port": v.TargetPort, "password": pass, "tls": defaultOutboundTLS(v.TargetAddress)}
	applyAllowed(item, extra, "password", "tls", "server_ports", "hop_interval", "hop_interval_max", "up_mbps", "down_mbps", "obfs", "network", "bbr_profile", "brutal_debug", "realm", "domain_resolver", "network_strategy", "fallback_delay")
	if err := rejectDisabledTLS(item, "hysteria2"); err != nil {
		return nil, err
	}
	applyQUICFields(item, extra)
	return item, nil
}
func (a hy2Adapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	node := map[string]any{"type": "hysteria2", "tag": inbound.Name, "server": server.EntryAddress, "server_port": inbound.Port, "password": user.ProxyPassword}
	if tls, ok := extra["tls"]; ok {
		node["tls"] = subscriptionTLSForInbound(inbound, tls)
	}
	applyAllowed(node, extra, "obfs", "up_mbps", "down_mbps", "network", "server_ports", "hop_interval", "hop_interval_max")
	return node, nil
}

type anyTLSAdapter struct{}

func (anyTLSAdapter) Protocol() model.Protocol { return model.ProtocolAnyTLS }
func (anyTLSAdapter) ValidateInbound(v model.Inbound) error {
	if err := ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if err := ValidatePort(v.Port); err != nil {
		return err
	}
	return requireInboundTLS(v.ConfigJSON, "anytls")
}
func (anyTLSAdapter) ValidateOutbound(v model.Outbound) error {
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	return ValidatePort(v.TargetPort)
}
func (a anyTLSAdapter) Inbound(v model.Inbound, users []model.User) (map[string]any, error) {
	if err := a.ValidateInbound(v); err != nil {
		return nil, err
	}
	extra := parseExtra(v.ConfigJSON)
	item := map[string]any{"type": "anytls", "tag": tag("in", v.ID), "listen": v.ListenIP, "listen_port": v.Port, "users": passwordUsers(users)}
	if err := validateTLSPathFields(extra["tls"], "inbound tls"); err != nil {
		return nil, err
	}
	applyAllowed(item, extra, "tls", "padding_scheme")
	return item, nil
}
func (a anyTLSAdapter) Outbound(v model.Outbound, user *model.User) (map[string]any, error) {
	if err := a.ValidateOutbound(v); err != nil {
		return nil, err
	}
	pass := ""
	if user != nil {
		pass = user.ProxyPassword
	}
	extra := parseExtra(v.ConfigJSON)
	item := map[string]any{"type": "anytls", "tag": tag("out", v.ID), "server": v.TargetAddress, "server_port": v.TargetPort, "password": pass, "tls": defaultOutboundTLS(v.TargetAddress)}
	applyAllowed(item, extra, "password", "tls", "idle_session_check_interval", "idle_session_timeout", "min_idle_session", "domain_resolver", "network_strategy", "fallback_delay")
	if err := rejectDisabledTLS(item, "anytls"); err != nil {
		return nil, err
	}
	return item, nil
}
func (a anyTLSAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	node := map[string]any{"type": "anytls", "tag": inbound.Name, "server": server.EntryAddress, "server_port": inbound.Port, "password": user.ProxyPassword}
	if tls, ok := extra["tls"]; ok {
		node["tls"] = subscriptionTLSForInbound(inbound, tls)
	}
	applyAllowed(node, extra, "padding_scheme")
	return node, nil
}

type mieruAdapter struct{}

func (mieruAdapter) Protocol() model.Protocol { return model.ProtocolMieru }
func (mieruAdapter) ValidateInbound(v model.Inbound) error {
	if err := ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if _, err := MieruInboundPorts(v); err != nil {
		return err
	}
	extra, err := decodeMieruConfig(v.ConfigJSON)
	if err != nil {
		return err
	}
	return validateMieruOptions(extra, false)
}
func (mieruAdapter) ValidateOutbound(v model.Outbound) error {
	if strings.TrimSpace(v.TargetAddress) == "" {
		return errors.New("target_address required")
	}
	if _, err := MieruOutboundPorts(v.TargetPort, v.ConfigJSON); err != nil {
		return err
	}
	extra, err := decodeMieruConfig(v.ConfigJSON)
	if err != nil {
		return err
	}
	username := stringValue(extra, "username", "")
	if strings.TrimSpace(username) == "" {
		return errors.New("mieru username required")
	}
	if len([]byte(username)) > 64 {
		return errors.New("mieru username exceeds 64 bytes")
	}
	password := stringValue(extra, "password", "")
	if password == "" {
		return errors.New("mieru password required")
	}
	if len([]byte(password)) > 64 {
		return errors.New("mieru password exceeds 64 bytes")
	}
	return validateMieruOptions(extra, true)
}
func (a mieruAdapter) Inbound(v model.Inbound, users []model.User) (map[string]any, error) {
	if err := a.ValidateInbound(v); err != nil {
		return nil, err
	}
	for _, user := range users {
		if len([]byte(user.ProxyPassword)) > 64 {
			return nil, fmt.Errorf("mieru password for user %d exceeds 64 bytes", user.ID)
		}
	}
	extra := parseExtra(v.ConfigJSON)
	ports, _ := MieruInboundPorts(v)
	item := map[string]any{
		"type":                   "mieru",
		"tag":                    tag("in", v.ID),
		"listen":                 v.ListenIP,
		"listen_port":            ports[0],
		"transport":              normalizeMieruTransport(stringValue(extra, "transport", "TCP")),
		"users":                  mieruPasswordUsers(users),
		"user_hint_is_mandatory": boolValueWithDefault(extra["user_hint_is_mandatory"], true),
	}
	if ranges := compressMieruPorts(ports[1:]); len(ranges) > 0 {
		item["listen_ports"] = ranges
	}
	applyAllowed(item, extra, "traffic_pattern")
	return item, nil
}
func (a mieruAdapter) Outbound(v model.Outbound, user *model.User) (map[string]any, error) {
	if err := a.ValidateOutbound(v); err != nil {
		return nil, err
	}
	extra := parseExtra(v.ConfigJSON)
	ports, _ := MieruOutboundPorts(v.TargetPort, v.ConfigJSON)
	username := stringValue(extra, "username", "")
	password := stringValue(extra, "password", "")
	if user != nil {
		if username == "" {
			username = mieruUsername(*user)
		}
		if password == "" {
			password = user.ProxyPassword
		}
	}
	item := map[string]any{
		"type":        "mieru",
		"tag":         tag("out", v.ID),
		"server":      v.TargetAddress,
		"server_port": ports[0],
		"transport":   normalizeMieruTransport(stringValue(extra, "transport", "TCP")),
		"username":    username,
		"password":    password,
	}
	if ranges := compressMieruPorts(ports[1:]); len(ranges) > 0 {
		item["server_ports"] = ranges
	}
	applyAllowed(item, extra, "multiplexing", "traffic_pattern", "domain_resolver", "network_strategy", "fallback_delay")
	return item, nil
}
func (a mieruAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	if err := a.ValidateInbound(inbound); err != nil {
		return nil, err
	}
	if len([]byte(user.ProxyPassword)) > 64 {
		return nil, fmt.Errorf("mieru password for user %d exceeds 64 bytes", user.ID)
	}
	extra := parseExtra(inbound.ConfigJSON)
	ports, _ := MieruInboundPorts(inbound)
	node := map[string]any{
		"type":        "mieru",
		"tag":         inbound.Name,
		"server":      server.EntryAddress,
		"server_port": ports[0],
		"transport":   normalizeMieruTransport(stringValue(extra, "transport", "TCP")),
		"username":    mieruUsername(user),
		"password":    user.ProxyPassword,
	}
	if ranges := compressMieruPorts(ports[1:]); len(ranges) > 0 {
		node["server_ports"] = ranges
	}
	applyAllowed(node, extra, "multiplexing", "traffic_pattern")
	return node, nil
}

var mieruMultiplexingLevels = map[string]bool{
	"MULTIPLEXING_DEFAULT": true,
	"MULTIPLEXING_OFF":     true,
	"MULTIPLEXING_LOW":     true,
	"MULTIPLEXING_MIDDLE":  true,
	"MULTIPLEXING_HIGH":    true,
}

func validateMieruOptions(extra map[string]any, outbound bool) error {
	if transportValue, exists := extra["transport"]; exists {
		transport, ok := transportValue.(string)
		if !ok || (strings.ToUpper(strings.TrimSpace(transport)) != "TCP" && strings.ToUpper(strings.TrimSpace(transport)) != "UDP") {
			return errors.New("mieru transport must be TCP or UDP")
		}
	}
	if multiplexingValue, exists := extra["multiplexing"]; exists {
		multiplexing, ok := multiplexingValue.(string)
		if !ok || !mieruMultiplexingLevels[multiplexing] {
			return errors.New("invalid mieru multiplexing level")
		}
	}
	if patternValue, exists := extra["traffic_pattern"]; exists {
		pattern, ok := patternValue.(string)
		if !ok {
			return errors.New("mieru traffic_pattern must be a base64 string")
		}
		if pattern != "" {
			if _, err := base64.StdEncoding.DecodeString(pattern); err != nil {
				return errors.New("mieru traffic_pattern must be valid base64")
			}
		}
	}
	if !outbound {
		if mandatory, exists := extra["user_hint_is_mandatory"]; exists {
			if _, ok := mandatory.(bool); !ok {
				return errors.New("mieru user_hint_is_mandatory must be boolean")
			}
		}
	}
	return nil
}

func boolValueWithDefault(value any, fallback bool) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}

type ssAdapter struct{}

func (ssAdapter) Protocol() model.Protocol { return model.ProtocolSS }
func (ssAdapter) ValidateInbound(v model.Inbound) error {
	if err := ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	return ValidatePort(v.Port)
}
func (ssAdapter) ValidateOutbound(v model.Outbound) error {
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	return ValidatePort(v.TargetPort)
}
func (a ssAdapter) Inbound(v model.Inbound, users []model.User) (map[string]any, error) {
	if err := a.ValidateInbound(v); err != nil {
		return nil, err
	}
	extra := parseExtra(v.ConfigJSON)
	method := stringValue(extra, "method", "2022-blake3-aes-128-gcm")
	supportsUsers := shadowsocksMethodSupportsUsers(method)
	pass := stringValue(extra, "password", "")
	if supportsUsers {
		if pass != "" {
			pass = normalizeSS2022Key(pass, method)
		} else if len(users) > 0 {
			pass = normalizeSS2022Key(users[0].ProxyPassword, method)
		}
	} else if len(users) > 0 && pass == "" {
		pass = users[0].ProxyPassword
	}
	item := map[string]any{"type": "shadowsocks", "tag": tag("in", v.ID), "listen": v.ListenIP, "listen_port": v.Port, "method": method, "password": pass, "users": ssPasswordUsers(users, method)}
	applyAllowed(item, extra, "network", "multiplex", "managed", "destinations")
	if !supportsUsers {
		delete(item, "users")
	}
	return item, nil
}
func (a ssAdapter) Outbound(v model.Outbound, user *model.User) (map[string]any, error) {
	if err := a.ValidateOutbound(v); err != nil {
		return nil, err
	}
	pass := ""
	if user != nil {
		pass = user.ProxyPassword
	}
	extra := parseExtra(v.ConfigJSON)
	method := stringValue(extra, "method", "2022-blake3-aes-128-gcm")
	if override := stringValue(extra, "password", ""); override != "" {
		pass = override
	}
	if shadowsocksMethodSupportsUsers(method) && pass != "" {
		pass = normalizeSS2022Key(pass, method)
	}
	item := map[string]any{"type": "shadowsocks", "tag": tag("out", v.ID), "server": v.TargetAddress, "server_port": v.TargetPort, "method": method, "password": pass}
	applyAllowed(item, extra, "plugin", "plugin_opts", "network", "udp_over_tcp", "multiplex", "domain_resolver", "network_strategy", "fallback_delay")
	return item, nil
}
func (a ssAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	method := stringValue(extra, "method", "2022-blake3-aes-128-gcm")
	password := user.ProxyPassword
	if serverPassword := stringValue(extra, "password", ""); serverPassword != "" && shadowsocksMethodSupportsUsers(method) {
		password = normalizeSS2022Key(serverPassword, method) + ":" + normalizeSS2022Key(user.ProxyPassword, method)
	}
	node := map[string]any{"type": "shadowsocks", "tag": inbound.Name, "server": server.EntryAddress, "server_port": inbound.Port, "method": method, "password": password}
	if server.UDPInboundMode == model.UDPInboundUoT {
		node["udp_over_tcp"] = map[string]any{"enabled": true}
	}
	return node, nil
}

func tag(prefix string, id int64) string { return prefix + "-" + strconv.FormatInt(id, 10) }
func stringValue(m map[string]any, key, fallback string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func defaultOutboundTLS(server string) map[string]any {
	tls := map[string]any{"enabled": true}
	if hostNeedsResolver(server) {
		tls["server_name"] = server
	}
	return tls
}

func requireInboundTLS(raw string, protocol string) error {
	extra := parseExtra(raw)
	tls, ok := extra["tls"]
	if !ok || tls == nil {
		return fmt.Errorf("%s inbound requires tls config_json", protocol)
	}
	item := map[string]any{"tls": tls}
	return rejectDisabledTLS(item, protocol)
}

func rejectDisabledTLS(item map[string]any, protocol string) error {
	tls, ok := item["tls"]
	if !ok || tls == nil {
		return fmt.Errorf("%s requires tls", protocol)
	}
	if tlsMap, ok := tls.(map[string]any); ok {
		if enabled, ok := tlsMap["enabled"].(bool); ok && !enabled {
			return fmt.Errorf("%s requires tls.enabled=true", protocol)
		}
	}
	return nil
}
