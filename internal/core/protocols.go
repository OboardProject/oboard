package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
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
	DeviceIDHash      string `json:"device_id_hash,omitempty"`
	CredentialEpoch   int64  `json:"credential_epoch,omitempty"`
	CredentialStatus  string `json:"credential_status,omitempty"`
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
	RoutingRuleSets   []model.RoutingRuleSet
	ExternalOutbounds []model.ExternalOutbound
	ProxyPaths        []model.ProxyPath
	ProxyPathSteps    []model.ProxyPathStep
	Servers           []model.Server
	Inbounds          []model.Inbound
	WARPProfiles      []model.WARPProfile
	InboundUsers      []model.InboundUser
	ProxyPathUsers    []model.ProxyPathUser
	// AccessSnapshot, when non-nil, is the plan authorization source. It
	// replaces InboundUsers/ProxyPathUsers as the user-node relation for this
	// configuration; config generation never reads authorization tables on its
	// own.
	AccessSnapshot *EffectiveAccessSnapshot
	// UserPolicies, when present, is the resolved speed/traffic policy per user
	// from the plan model. When absent the user's own limits apply.
	UserPolicies    map[int64]UserLimitPolicy
	TrafficPolicies map[int64]model.TrafficRuntimePolicy
	// UserDevices is the active device projection from the Controller routing
	// snapshot. A nil slice preserves the pure-Core user-level behaviour used by
	// isolated configuration tests and import tooling.
	UserDevices []model.UserDevice
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
	case model.ProtocolSnell:
		return snellAdapter{}, nil
	case model.ProtocolSocks:
		return socksAdapter{}, nil
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

func InboundAdvertisePort(inbound model.Inbound) int {
	if inbound.AdvertisePort > 0 {
		return inbound.AdvertisePort
	}
	return inbound.Port
}

func InboundHasAdvertisePort(inbound model.Inbound) bool {
	return inbound.AdvertisePort > 0 && inbound.AdvertisePort != inbound.Port
}

func InboundSubscriptionPort(inbound model.Inbound) int {
	return InboundAdvertisePort(inbound)
}

func MieruInboundSubscriptionPorts(inbound model.Inbound) ([]int, error) {
	ports, err := MieruInboundPorts(inbound)
	if err != nil {
		return nil, err
	}
	if len(ports) == 0 {
		return ports, nil
	}
	if inbound.AdvertisePort > 0 && inbound.AdvertisePort != inbound.Port {
		ports[0] = inbound.AdvertisePort
		if len(ports) > 1 {
			return nil, fmt.Errorf("NAT port mapping does not support multi-port Mieru")
		}
	}
	return ports, nil
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
// A stored empty or "0.0.0.0" value falls through to the server listen_mode:
// "dual" always binds "::", "ipv4_only" always binds "0.0.0.0", and "auto"
// binds "::" when the server has IPv6 inbound capability (a public or
// interface global IPv6 address) because a wildcard IPv6 socket is dual stack
// on Linux and serves both families from one port; otherwise "0.0.0.0" keeps
// IPv4-only hosts working. Explicit "::" or specific addresses are preserved.
func EffectiveListenIP(server model.Server, stored string) string {
	value := strings.TrimSpace(stored)
	if value != "" && value != "0.0.0.0" {
		return value
	}
	switch server.ListenMode {
	case model.ListenModeDual:
		return "::"
	case model.ListenModeIPv4Only:
		return "0.0.0.0"
	}
	if ServerHasIPv6Inbound(server) {
		return "::"
	}
	return "0.0.0.0"
}

// ServerHasIPv6Inbound reports whether a server can accept IPv6 connections:
// either egress-detected public IPv6 or a global IPv6 address assigned to a
// local interface (covers inbound-only IPv6 hosts whose egress probe fails).
func ServerHasIPv6Inbound(server model.Server) bool {
	return strings.TrimSpace(server.PublicIPv6) != "" || strings.TrimSpace(server.InterfaceIPv6) != ""
}

// ServerEntryIPv6 returns the IPv6 address advertised for a server: the
// egress-detected public address when available, otherwise the interface
// global address (inbound-only IPv6 hosts).
func ServerEntryIPv6(server model.Server) string {
	return firstNonEmpty(strings.TrimSpace(server.PublicIPv6), strings.TrimSpace(server.InterfaceIPv6))
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

func ValidateRoutingRuleDNSResolver(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	switch value {
	case primaryRemoteDNSTag, "remote-secondary", primaryBootstrapDNSTag, "bootstrap-secondary", "local":
		return nil
	default:
		return fmt.Errorf("unsupported dns_resolver %q", value)
	}
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
	if opts.AccessSnapshot != nil {
		opts.InboundUsers = opts.AccessSnapshot.InboundUserBindings()
		opts.ProxyPathUsers = opts.AccessSnapshot.ProxyPathUserBindings()
	}
	opts.ProxyPathSteps = resolveImplicitProxyPathInboundBindings(opts.ProxyPaths, opts.ProxyPathSteps, opts.Inbounds)
	users = ExpandDeviceUsers(users, opts.UserDevices)
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
	if dnsRules, err := buildDNSRules(server, opts.RoutingRules, opts.RoutingRuleSets); err != nil {
		return "", err
	} else if len(dnsRules) > 0 {
		dns["rules"] = dnsRules
	}
	config.Route["default_domain_resolver"] = defaultDomainResolver(dns, server)
	// Snell fans out into one single-user listener per identity, so its ports
	// must be claimed before anything else derives a generated listener:
	// proxy path hops allocate from the same server range and only see
	// conflicts through the inbound set they are handed.
	snellPlan, snellReservations, err := planSnellUserListeners(configProjectionInbounds(inbounds, opts.Inbounds), configProjectionServers(server, opts.Servers), users, opts)
	if err != nil {
		return "", err
	}
	if len(snellReservations) > 0 {
		opts.Inbounds = append(append([]model.Inbound{}, opts.Inbounds...), snellReservations...)
	}
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
		accountedUsers, inboundUsers, err := resolveInboundUsers(inbound, users, opts, server.ChainSecret)
		if err != nil {
			return "", err
		}
		// Snell does not have one listener with a user table: each identity
		// owns a dedicated single-user listener rendered from the plan above,
		// and each carries its own runtime limit keyed by its own tag.
		if inbound.Protocol == model.ProtocolSnell {
			for _, listener := range snellPlan[inbound.ID] {
				item, err := snellListenerInbound(inbound, listener)
				if err != nil {
					return "", err
				}
				item["listen"] = EffectiveListenIP(server, inbound.ListenIP)
				applyServerNetworkPolicy(item, server, inbound.Protocol, true)
				addRuntimeLimitsForInboundTag(&config, inbound, []model.User{listener.User}, opts, listener.Tag)
				config.Inbounds = append(config.Inbounds, item)
			}
			continue
		}
		addRuntimeLimitsForInbound(&config, inbound, accountedUsers, opts)
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
	sourcePrefixOutbounds, err := buildSourcePrefixOutbounds(server, opts.RoutingRules)
	if err != nil {
		return "", err
	}
	config.Outbounds = append(config.Outbounds, sourcePrefixOutbounds...)
	interfaceOutbounds, err := buildRoutingRuleInterfaceOutbounds(server, opts.RoutingRules, opts.ProxyPaths, opts.ProxyPathSteps, opts.WARPProfiles)
	if err != nil {
		return "", err
	}
	config.Outbounds = append(config.Outbounds, interfaceOutbounds...)
	pathOutbounds, pathRules, err := buildProxyPathOutboundsAndRules(server, outbounds, opts, users, plannedPathInbounds)
	if err != nil {
		return "", err
	}
	config.Outbounds = append(config.Outbounds, pathOutbounds...)
	inheritedFamilyDNSStrategy, _ := dns["strategy"].(string)
	familySplitOutbounds, err := buildRoutingRuleFamilySplitOutbounds(server, opts, pathOutbounds, plannedPathInbounds, defaultDomainResolver(dns, server), inheritedFamilyDNSStrategy)
	if err != nil {
		return "", err
	}
	config.Outbounds = append(config.Outbounds, familySplitOutbounds...)
	omitUnsupportedDialTCPFastOpenAll(config.Outbounds)
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
	if err := applyRoutingRuleWARPEndpointBindings(server, opts.RoutingRules, opts.ProxyPaths, opts.ProxyPathSteps, opts.WARPProfiles, &config.Endpoints); err != nil {
		return "", err
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
	if ruleSets := buildRouteRuleSets(server, opts.RoutingRules, opts.RoutingRuleSets); len(ruleSets) > 0 {
		config.Route["rule_set"] = ruleSets
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
		policy, okPolicy := opts.UserPolicies[user.ID]
		if !okPolicy {
			policy = UserLimitPolicy{
				SpeedLimitMbps:    user.SpeedLimitMbps,
				TrafficLimitBytes: user.TrafficLimitBytes,
				TrafficResetMode:  user.TrafficResetMode,
				TrafficResetDay:   user.TrafficResetDay,
			}
			if strings.TrimSpace(policy.TrafficResetMode) == "" {
				policy.TrafficResetMode = "monthly"
			}
			if policy.TrafficResetDay <= 0 {
				policy.TrafficResetDay = 1
			}
		}
		speed, traffic := policy.SpeedLimitMbps, policy.TrafficLimitBytes
		limit := OBoardUserRuntimeLimit{
			UserID:           user.ID,
			DeviceIDHash:     user.DeviceIDHash,
			CredentialEpoch:  user.CredentialEpoch,
			CredentialStatus: user.CredentialStatus,
			Billable:         true,
			ResetMode:        policy.TrafficResetMode,
			ResetDay:         policy.TrafficResetDay,
		}
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
		if user.DeviceIDHash != "" {
			limit.DeviceIDHash = user.DeviceIDHash
			limit.CredentialEpoch = user.CredentialEpoch
			limit.CredentialStatus = user.CredentialStatus
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
	if inbound.Protocol == model.ProtocolSS && server.UDPInboundMode == model.UDPInboundUoT && genericMuxEnabled(parseExtra(inbound.ConfigJSON)["multiplex"]) {
		return fmt.Errorf("server %s udp_inbound_mode=uot conflicts with multiplex on Shadowsocks inbound %s: a client cannot use udp_over_tcp and multiplex together", server.Name, inbound.Name)
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
			omitUnsupportedDialTCPFastOpen(raw)
			return raw, nil
		}
	}
	if v.Protocol == model.ProtocolSocks {
		item := map[string]any{"type": "socks", "tag": outboundTag, "server": v.TargetAddress, "server_port": v.TargetPort}
		extra := parseExtra(v.ConfigJSON)
		applyAllowed(item, extra, "version", "username", "password", "network", "udp_over_tcp", "tcp_fast_open", "domain_resolver", "network_strategy", "fallback_delay")
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
	omitUnsupportedDialTCPFastOpen(item)
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
			processingUsers := proxyPathBranchUsersForPath(path, root, usersForProxyPath(path, root, users, opts.InboundUsers, opts.ProxyPathUsers))
			if group != nil {
				processingUsers = nil
				for _, branch := range group.Paths {
					processingUsers = append(processingUsers, proxyPathBranchUsersForPath(branch, root, usersForProxyPath(branch, root, users, opts.InboundUsers, opts.ProxyPathUsers))...)
				}
			}
			if len(processingUsers) == 0 {
				placeholderUsers, err := placeholderUsersForInbound(processingInbound, server.ChainSecret)
				if err != nil {
					return nil, err
				}
				processingUsers = placeholderUsers
			}
			processingUsers = credentialUsersForInbound(processingUsers, processingInbound)
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

func buildProxyPathOutboundsAndRules(server model.Server, outboundsInput []model.Outbound, opts ConfigOptions, users []model.User, plannedInbounds map[int64]model.Inbound) ([]map[string]any, []map[string]any, error) {
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
	// Project the paths through the same ledger the caller already used. A
	// second derivation without the ledger would re-roll the seeds and could
	// pick different ports for the same hop now that allocations carry pool and
	// network metadata; the caller's planned listeners and this outbound/rule
	// table must agree on every port.
	pathPlans, _, err := buildProxyPathPlansWithInbounds(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds, opts.PortLedger)
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
		steps := OrderedFamilyBranchSteps(stepsByPath[path.ID])
		isDirect := path.Kind == model.ProxyPathKindDirect
		var root model.Inbound
		activeServerID := int64(0)
		var activeInboundTags []string
		var activeAuthUsers []string
		var activeStageStepID *int64
		previousTag := ""
		if IsFamilyBranch(path) {
			if len(steps) == 0 {
				continue
			}
			if err := validateProxyPathForConfig(path, model.Inbound{}, steps); err != nil {
				return nil, nil, err
			}
			first := steps[0]
			if first.NodeType != model.ProxyPathStepServerInbound {
				continue
			}
			targetServerID, _, ok := proxyPathStepTargetServer(first, inboundByID)
			if !ok {
				continue
			}
			activeServerID = targetServerID
			activeInboundTags, activeAuthUsers = proxyPathStepInboundIdentity(path, first, model.Inbound{}, targetServerID, inboundByID, users, opts, chainServices, transparentGroups[path.ID])
			stepID := first.ID
			activeStageStepID = &stepID
			steps = steps[1:]
		} else {
			var ok bool
			root, ok = inboundByID[path.InboundID]
			if !ok || !root.Enabled {
				continue
			}
			if !isDirect && len(steps) == 0 {
				continue
			}
			if len(steps) > 0 {
				if err := validateProxyPathForConfig(path, root, steps); err != nil {
					return nil, nil, err
				}
			}
			activeServerID = root.ServerID
			activeInboundTags = rootInboundRoutingTags(root, path, users, opts)
			activeAuthUsers = routingAuthUsersForProtocol(root.Protocol, proxyPathBranchUsernames(path, root, usersForProxyPath(path, root, users, opts.InboundUsers, opts.ProxyPathUsers)))
		}
		for _, step := range steps {
			if step.TransportMode == "" {
				step.TransportMode = model.ProxyPathTransportSingBox
			}
			if step.TransportMode == model.ProxyPathTransportPortForward {
				if targetServerID, _, ok := proxyPathStepTargetServer(step, inboundByID); ok {
					activeServerID = targetServerID
					activeInboundTags, activeAuthUsers = proxyPathStepInboundIdentity(path, step, root, targetServerID, inboundByID, users, opts, chainServices, transparentGroups[path.ID])
					stepID := step.ID
					activeStageStepID = &stepID
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
					activeInboundTags, activeAuthUsers = proxyPathStepInboundIdentity(path, step, root, targetServerID, inboundByID, users, opts, chainServices, transparentGroups[path.ID])
					stepID := step.ID
					activeStageStepID = &stepID
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
					stageRules, err := buildPathStageRules(path, activeStageStepID, server, activeInboundTags, activeAuthUsers, opts.RoutingRules, outboundsInput, opts.ExternalOutbounds, paths, opts.ProxyPathSteps, opts.WARPProfiles)
					if err != nil {
						return nil, nil, err
					}
					rules = append(rules, stageRules...)
					rules = appendPathRoutingRule(rules, activeInboundTags, activeAuthUsers, previousTag)
				}
				if targetServerID, _, ok := proxyPathStepTargetServer(step, inboundByID); ok {
					activeServerID = targetServerID
					activeInboundTags, activeAuthUsers = proxyPathStepInboundIdentity(path, step, root, targetServerID, inboundByID, users, opts, chainServices, transparentGroups[path.ID])
					stepID := step.ID
					activeStageStepID = &stepID
					previousTag = ""
				}
			}
		}
		if isDirect {
			if previousTag != "" {
				return nil, nil, fmt.Errorf("直接出口分支 %s 必须结束于可控服务器", path.Name)
			}
			if activeServerID == server.ID {
				stageRules, err := buildPathStageRules(path, activeStageStepID, server, activeInboundTags, activeAuthUsers, opts.RoutingRules, outboundsInput, opts.ExternalOutbounds, paths, opts.ProxyPathSteps, opts.WARPProfiles)
				if err != nil {
					return nil, nil, err
				}
				rules = append(rules, stageRules...)
				rules = appendPathRoutingRule(rules, activeInboundTags, activeAuthUsers, "direct")
			}
			continue
		}
		if previousTag != "" && activeServerID == server.ID {
			stageRules, err := buildPathStageRules(path, activeStageStepID, server, activeInboundTags, activeAuthUsers, opts.RoutingRules, outboundsInput, opts.ExternalOutbounds, paths, opts.ProxyPathSteps, opts.WARPProfiles)
			if err != nil {
				return nil, nil, err
			}
			rules = append(rules, stageRules...)
			rules = appendPathRoutingRule(rules, activeInboundTags, activeAuthUsers, previousTag)
		} else if previousTag == "" && activeStageStepID != nil && activeServerID == server.ID {
			stageRules, err := buildPathStageRules(path, activeStageStepID, server, activeInboundTags, activeAuthUsers, opts.RoutingRules, outboundsInput, opts.ExternalOutbounds, paths, opts.ProxyPathSteps, opts.WARPProfiles)
			if err != nil {
				return nil, nil, err
			}
			rules = append(rules, stageRules...)
		}
	}
	if err := applyRoutingRuleProxyPathBindings(server, opts.RoutingRules, paths, opts.ProxyPathSteps, opts.WARPProfiles, &outbounds); err != nil {
		return nil, nil, err
	}
	return outbounds, rules, nil
}

func ProxyPathEntryRoute(path model.ProxyPath, steps []model.ProxyPathStep, root model.Inbound, warpProfiles []model.WARPProfile) (string, string, error) {
	if !path.Enabled || path.InboundID != root.ID || root.Protocol != model.ProtocolSSH {
		return "", "", errors.New("SSH 入口路径无效")
	}
	ordered := orderedProxyPathSteps(steps)
	for _, step := range ordered {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode == model.ProxyPathTransportPortForward {
			return "", "", errors.New("SSH 入口不能使用透明端口转发")
		}
	}
	if len(ordered) == 0 {
		if path.Kind == model.ProxyPathKindDirect {
			return "direct", "", nil
		}
		return "", "", errors.New("SSH 普通代理路径至少需要一个步骤")
	}
	first := ordered[0]
	if first.NodeType == model.ProxyPathStepWARP {
		for _, profile := range warpProfiles {
			if profile.ServerID == root.ServerID && profile.Enabled {
				return "outbound", tag("warp", profile.ID), nil
			}
		}
		return "", "", errors.New("SSH 入口路径需要可用的 WARP")
	}
	return "outbound", proxyPathStepTag(path.ID, first.Position), nil
}

func ProxyPathEntryRoutingIdentity(path model.ProxyPath, root model.Inbound, user model.User) (string, string, error) {
	if !path.Enabled || path.InboundID != root.ID || root.Protocol != model.ProtocolSSH || user.ID <= 0 {
		return "", "", errors.New("SSH entry routing identity is invalid")
	}
	branchUser := proxyPathBranchUser(path, root, user)
	return tag("in", root.ID), protocolAuthUsername(root.Protocol, branchUser), nil
}

func validateProxyPathForConfig(path model.ProxyPath, root model.Inbound, steps []model.ProxyPathStep) error {
	processing := 0
	seenServers := map[int64]bool{}
	if !IsFamilyBranch(path) {
		seenServers[root.ServerID] = true
	}
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

func proxyPathStepInboundIdentity(path model.ProxyPath, step model.ProxyPathStep, root model.Inbound, targetServerID int64, inboundByID map[int64]model.Inbound, users []model.User, opts ConfigOptions, services map[proxyPathChainServiceKey]*proxyPathChainService, transparentGroup *transparentProxyPathGroup) ([]string, []string) {
	if transparentGroup != nil && step.Position == transparentGroup.PrefixLength {
		return []string{proxyPathSharedTransparentInboundTag(transparentGroup.InboundID, transparentGroup.PrefixLength)}, proxyPathBranchUsernames(path, root, usersForProxyPath(path, root, users, opts.InboundUsers, opts.ProxyPathUsers))
	}
	if step.InboundID != nil && *step.InboundID != 0 {
		inbound := inboundByID[*step.InboundID]
		user := proxyPathLinkUser(path, inbound)
		return stepInboundRoutingTags(inbound, user), routingAuthUsersForProtocol(inbound.Protocol, []string{protocolAuthUsername(inbound.Protocol, user)})
	}
	if service, ok := proxyPathChainServiceForStep(services, step, targetServerID); ok {
		user := proxyPathInternalUser(path, step)
		return []string{service.Tag}, []string{protocolAuthUsername(service.Inbound.Protocol, user)}
	}
	return []string{proxyPathInternalInboundTag(path.ID, step.Position)}, proxyPathBranchUsernames(path, root, usersForProxyPath(path, root, users, opts.InboundUsers, opts.ProxyPathUsers))
}

// appendPathRoutingRule adds one proxy path routing rule. An empty inbound tag
// set means the stage has no listener on this server — a Snell branch with no
// authorized users, for instance — and must not produce a rule: sing-box would
// read the empty `inbound` list as "match anything".
func appendPathRoutingRule(rules []map[string]any, inboundTags, authUsers []string, outbound string) []map[string]any {
	if len(inboundTags) == 0 {
		return rules
	}
	rule := map[string]any{"inbound": inboundTags, "action": "route", "outbound": outbound}
	if len(authUsers) > 0 {
		rule["auth_user"] = authUsers
	}
	return append(rules, rule)
}

// rootInboundRoutingTags names the listeners a branch's traffic arrives on.
// Most protocols share one listener and separate users with auth_user. Snell
// gives every identity its own single-user listener, so the branch is
// identified by the set of those tags and there is no auth_user to match.
func rootInboundRoutingTags(root model.Inbound, path model.ProxyPath, users []model.User, opts ConfigOptions) []string {
	if root.Protocol != model.ProtocolSnell {
		return []string{tag("in", root.ID)}
	}
	branchUsers := proxyPathBranchUsersForPath(path, root, usersForProxyPath(path, root, users, opts.InboundUsers, opts.ProxyPathUsers))
	out := make([]string, 0, len(branchUsers))
	for _, user := range branchUsers {
		out = append(out, snellUserInboundTag(root.ID, user.ID, runtimePathIDFromUsername(user.Username)))
	}
	return out
}

// stepInboundRoutingTags names the listeners a chain hop lands on when the hop
// targets a real inbound. A Snell target accepts the hop on the dedicated
// listener of the path's link identity.
func stepInboundRoutingTags(inbound model.Inbound, linkUser model.User) []string {
	if inbound.Protocol != model.ProtocolSnell {
		return []string{tag("in", inbound.ID)}
	}
	return []string{snellUserInboundTag(inbound.ID, linkUser.ID, runtimePathIDFromUsername(linkUser.Username))}
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
	listenIP := EffectiveListenIP(server, server.ListenIP)
	pool := model.PortPoolPublic
	if managedSSH {
		pool = model.PortPoolInternal
		listenIP = "127.0.0.1"
	}
	port := ledger.resolve(PortRequirement{
		Kind:           model.ProxyPathPortKindInternal,
		ScopeKey:       fmt.Sprintf("%d:%d", path.ID, step.Position),
		ServerID:       server.ID,
		Pool:           pool,
		ListenIP:       listenIP,
		Network:        model.ForwardProtocolTCP,
		PolicyRevision: serverPortPolicyRevision(server),
		Allocate: func() int {
			if managedSSH {
				start, end := proxyPathInternalPortRange(server)
				return proxyPathAvailablePort(server, path.ID*149, step.Position*29, start, end, "127.0.0.1", inboundByID)
			}
			return proxyPathInternalPort(server, path.ID, step.Position, listenIP, inboundByID)
		},
	})
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
	port := ledger.resolve(PortRequirement{
		Kind:           model.ProxyPathPortKindInternal,
		ScopeKey:       scopeKey,
		ServerID:       server.ID,
		Pool:           model.PortPoolPublic,
		ListenIP:       EffectiveListenIP(server, server.ListenIP),
		Network:        model.ForwardProtocolTCP,
		PolicyRevision: serverPortPolicyRevision(server),
		Allocate: func() int {
			return proxyPathInternalPort(server, inboundID, step.Position, EffectiveListenIP(server, server.ListenIP), inboundByID)
		},
	})
	return model.Inbound{ID: proxyPathSharedTransparentInboundID(inboundID, step.Position), ServerID: server.ID, Name: fmt.Sprintf("入口 %d / 透明第%d跳内部入口", inboundID, step.Position), Protocol: model.ProtocolVLESS, ListenIP: EffectiveListenIP(server, server.ListenIP), Port: port, ConfigJSON: `{}`, Enabled: true}
}

func proxyPathTrustedInnerInbound(path model.ProxyPath, step model.ProxyPathStep, server model.Server, outer model.Inbound, inboundByID map[int64]model.Inbound, ledger *ProxyPathPortLedger) model.Inbound {
	inner := outer
	inner.ListenIP = "127.0.0.1"
	inner.Port = ledger.resolve(PortRequirement{
		Kind:           model.ProxyPathPortKindTrustedInner,
		ScopeKey:       fmt.Sprintf("%d:%d", path.ID, step.Position),
		ServerID:       server.ID,
		Pool:           model.PortPoolInternal,
		ListenIP:       "127.0.0.1",
		Network:        model.ForwardProtocolTCP,
		PolicyRevision: serverPortPolicyRevision(server),
		Allocate: func() int {
			start, end := proxyPathInternalPortRange(server)
			return proxyPathAvailablePort(server, path.ID*193, step.Position*37, start, end, "127.0.0.1", inboundByID)
		},
	})
	return inner
}

func proxyPathSharedTrustedInnerInbound(inboundID int64, position int, server model.Server, outer model.Inbound, inboundByID map[int64]model.Inbound, ledger *ProxyPathPortLedger) model.Inbound {
	inner := outer
	inner.ListenIP = "127.0.0.1"
	inner.Port = ledger.resolve(PortRequirement{
		Kind:           model.ProxyPathPortKindTrustedInner,
		ScopeKey:       fmt.Sprintf("inbound:%d:%d", inboundID, position),
		ServerID:       server.ID,
		Pool:           model.PortPoolInternal,
		ListenIP:       "127.0.0.1",
		Network:        model.ForwardProtocolTCP,
		PolicyRevision: serverPortPolicyRevision(server),
		Allocate: func() int {
			start, end := proxyPathInternalPortRange(server)
			return proxyPathAvailablePort(server, inboundID*193, position*37, start, end, "127.0.0.1", inboundByID)
		},
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

func proxyPathInternalPort(server model.Server, pathID int64, position int, listenIP string, inboundByID map[int64]model.Inbound) int {
	start, end := proxyPathServerPortRange(server)
	return proxyPathAvailablePort(server, pathID*97, position*17, start, end, listenIP, inboundByID)
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
		var item map[string]any
		if inbound.Protocol == model.ProtocolSnell {
			// The hop dials the dedicated single-user listener of this path's
			// link identity, whose port the Snell projection already recorded
			// in the ledger earlier in this run.
			pathID := runtimePathIDFromUsername(user.Username)
			node, ok, err := SnellSubscriptionNode(ledger, user, inbound, targetServer, pathID)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, markInvalidDesiredState(fmt.Errorf("代理链目标入口 %s 是 Snell，但尚未为该链路分配监听端口，请先部署该入口", inbound.Name))
			}
			item = node
		} else {
			adapter, err := AdapterFor(inbound.Protocol)
			if err != nil {
				return nil, err
			}
			item, err = adapter.SubscriptionNode(user, inbound, targetServer)
			if err != nil {
				return nil, err
			}
		}
		item["tag"] = outboundTag
		applyServerNetworkPolicy(item, sourceServer, inbound.Protocol, false)
		omitUnsupportedDialTCPFastOpen(item)
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

// WarpTunnelMTU is the fixed WireGuard tunnel MTU for WARP egress. WARP must
// never inherit the server's main-network MTU: its encrypted outer packets add
// WireGuard/IP/UDP headers, and oversized datagrams get fragmented and dropped
// on typical paths (including Cloudflare anycast), which stalls page loads
// while small control packets still pass. 1280 is Cloudflare's standard WARP
// value and is safe on any path that supports IPv6 minimum MTU.
const WarpTunnelMTU = 1280

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
	raw["mtu"] = WarpTunnelMTU
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
		if rule.ServerID == server.ID && rule.Enabled && (rule.Scope == "" || rule.Scope == model.RoutingRuleScopeServer) {
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
			item["action"] = "route"
			item["outbound"] = routingRuleInterfaceOutboundTag(rule.ID)
			out = append(out, item)
			continue
		}
		if rule.Action == model.RouteActionSourcePrefix {
			item["action"] = "route"
			item["outbound"] = sourcePrefixOutboundTag(rule.SourcePrefix)
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

func buildPathStageRules(path model.ProxyPath, stageStepID *int64, server model.Server, inboundTags []string, authUsers []string, rules []model.RoutingRule, outbounds []model.Outbound, external []model.ExternalOutbound, paths []model.ProxyPath, steps []model.ProxyPathStep, warpProfiles []model.WARPProfile) ([]map[string]any, error) {
	// No listener on this server means no stage to attach rules to. Emitting
	// them with an empty inbound list would make sing-box match every inbound.
	if len(inboundTags) == 0 {
		return nil, nil
	}
	filtered := make([]model.RoutingRule, 0)
	for _, rule := range rules {
		if !rule.Enabled || rule.Scope != model.RoutingRuleScopePathStage || rule.ProxyPathID == nil || *rule.ProxyPathID != path.ID || rule.ServerID != server.ID || !sameOptionalID(rule.StageStepID, stageStepID) {
			continue
		}
		filtered = append(filtered, rule)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].SortPosition == filtered[j].SortPosition {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].SortPosition < filtered[j].SortPosition
	})
	result := make([]map[string]any, 0, len(filtered))
	for _, rule := range filtered {
		item := map[string]any{}
		if rule.MatchSource == model.RoutingMatchSourceRuleSet {
			if rule.RuleSetID == nil {
				return nil, fmt.Errorf("routing rule %s: rule_set_id required", rule.Name)
			}
			item["rule_set"] = []string{routingRuleSetTag(*rule.RuleSetID)}
		} else if strings.TrimSpace(rule.MatchJSON) != "" && strings.TrimSpace(rule.MatchJSON) != "{}" {
			if err := json.Unmarshal([]byte(rule.MatchJSON), &item); err != nil {
				return nil, fmt.Errorf("routing rule %s match_json: %w", rule.Name, err)
			}
		}
		item["inbound"] = append([]string(nil), inboundTags...)
		if len(authUsers) > 0 {
			item["auth_user"] = append([]string(nil), authUsers...)
		} else {
			delete(item, "auth_user")
		}
		if rule.Action == model.RouteActionInterface {
			item["action"] = "route"
			if continuation, err := routingRuleSamePathContinuationTag(rule, server, paths, steps, warpProfiles); err == nil && continuation != "" {
				item["outbound"] = routingRuleBoundOutboundTag(rule.ID, continuation)
			} else {
				item["outbound"] = routingRuleInterfaceOutboundTag(rule.ID)
			}
			result = append(result, item)
			continue
		}
		if rule.Action == model.RouteActionSourcePrefix {
			item["action"] = "route"
			item["outbound"] = sourcePrefixOutboundTag(rule.SourcePrefix)
			result = append(result, item)
			continue
		}
		var outboundTag string
		var ok bool
		var err error
		if rule.Action == model.RouteActionProxyPath {
			outboundTag, err = routingRuleProxyPathOutboundTag(rule, server, paths, steps, warpProfiles)
			if err == nil && routingRuleHasProxyPathBinding(rule) {
				outboundTag = routingRuleBoundOutboundTag(rule.ID, outboundTag)
			}
			ok = err == nil && outboundTag != ""
		} else if rule.Action == model.RouteActionFamilySplit {
			outboundTag = routingRuleFamilySelectorTag(rule.ID)
			ok = true
		} else {
			outboundTag, ok, err = routeRuleOutboundTag(rule, server, outbounds, external)
		}
		if err != nil {
			return nil, fmt.Errorf("routing rule %s: %w", rule.Name, err)
		}
		if !ok {
			continue
		}
		item["action"] = "route"
		item["outbound"] = outboundTag
		result = append(result, item)
	}
	return result, nil
}

func routingRuleProxyPathOutboundTag(rule model.RoutingRule, server model.Server, paths []model.ProxyPath, steps []model.ProxyPathStep, warpProfiles []model.WARPProfile) (string, error) {
	if rule.ProxyPathID == nil || rule.TargetProxyPathID == nil {
		return "", errors.New("source and target proxy paths are required")
	}
	return routingRuleTargetProxyPathOutboundTag(rule, *rule.TargetProxyPathID, server, paths, steps, warpProfiles)
}

func routingRuleSamePathContinuationTag(rule model.RoutingRule, server model.Server, paths []model.ProxyPath, steps []model.ProxyPathStep, warpProfiles []model.WARPProfile) (string, error) {
	if rule.ProxyPathID == nil {
		return "", errors.New("source proxy path is required")
	}
	return routingRuleTargetProxyPathOutboundTag(rule, *rule.ProxyPathID, server, paths, steps, warpProfiles)
}

func routingRuleTargetProxyPathOutboundTag(rule model.RoutingRule, targetPathID int64, server model.Server, paths []model.ProxyPath, steps []model.ProxyPathStep, warpProfiles []model.WARPProfile) (string, error) {
	if rule.ProxyPathID == nil {
		return "", errors.New("source proxy path is required")
	}
	var target model.ProxyPath
	found := false
	for _, path := range paths {
		if path.ID == targetPathID && path.Enabled {
			target, found = path, true
			break
		}
	}
	if !found {
		return "", fmt.Errorf("target proxy path %d is unavailable", targetPathID)
	}
	stagePosition := 0
	if rule.StageStepID != nil {
		for _, step := range steps {
			if step.PathID == *rule.ProxyPathID && step.ID == *rule.StageStepID {
				stagePosition = step.Position
				break
			}
		}
		if stagePosition == 0 {
			return "", errors.New("routing stage is unavailable")
		}
	}
	targetSteps := make([]model.ProxyPathStep, 0)
	for _, step := range steps {
		if step.PathID == target.ID && step.Position > stagePosition {
			targetSteps = append(targetSteps, step)
		}
	}
	sort.SliceStable(targetSteps, func(i, j int) bool { return targetSteps[i].Position < targetSteps[j].Position })
	if len(targetSteps) == 0 {
		return "", errors.New("target proxy path does not continue after the routing stage")
	}
	previousTag := ""
	for _, step := range targetSteps {
		mode := step.TransportMode
		if mode == "" {
			mode = model.ProxyPathTransportSingBox
		}
		if mode == model.ProxyPathTransportPortForward {
			return "", errors.New("rule-specific path cannot enter a transparent forwarding hop")
		}
		if step.NodeType == model.ProxyPathStepWARP {
			if previousTag != "" {
				return "", errors.New("WARP must be the first local hop after the routing stage")
			}
			profile, ok := warpProfileForServer(warpProfiles, server.ID)
			if !ok || !profile.Enabled {
				return "", fmt.Errorf("target proxy path requires WARP on server %d", server.ID)
			}
			previousTag = tag("warp", profile.ID)
			continue
		}
		previousTag = proxyPathStepTag(target.ID, step.Position)
		if step.NodeType == model.ProxyPathStepServerInbound {
			return previousTag, nil
		}
	}
	if previousTag == "" {
		return "", errors.New("target proxy path has no routable outbound")
	}
	return previousTag, nil
}

func routingRuleHasProxyPathBinding(rule model.RoutingRule) bool {
	return strings.TrimSpace(rule.InterfaceName) != "" || strings.TrimSpace(rule.SourcePrefix) != ""
}

func routingRuleShouldBindInterface(rule model.RoutingRule, dest string) bool {
	if strings.TrimSpace(rule.InterfaceName) == "" {
		return false
	}
	if !rule.InterfaceBindKnown {
		return true
	}
	switch AddressFamily(dest) {
	case "ipv4":
		return rule.InterfaceHasGlobalIPv4
	case "ipv6":
		return rule.InterfaceHasGlobalIPv6
	default:
		return rule.InterfaceHasGlobalIPv4 || rule.InterfaceHasGlobalIPv6
	}
}

func routingRuleBoundOutboundTag(ruleID int64, outboundTag string) string {
	return fmt.Sprintf("routing-rule-%d-%s", ruleID, outboundTag)
}

func routingRuleInterfaceOutboundTag(ruleID int64) string {
	return fmt.Sprintf("routing-rule-%d-interface", ruleID)
}

func routingRuleFamilySelectorTag(ruleID int64) string {
	return fmt.Sprintf("routing-rule-%d-family", ruleID)
}

func routingRuleFamilyBranchTag(ruleID int64, family, outboundTag string) string {
	return fmt.Sprintf("routing-rule-%d-%s-%s", ruleID, family, outboundTag)
}

func buildRoutingRuleInterfaceOutbounds(server model.Server, rules []model.RoutingRule, paths []model.ProxyPath, steps []model.ProxyPathStep, warpProfiles []model.WARPProfile) ([]map[string]any, error) {
	result := make([]map[string]any, 0)
	for _, rule := range rules {
		if !rule.Enabled || rule.ServerID != server.ID || rule.Action != model.RouteActionInterface {
			continue
		}
		if _, err := routingRuleSamePathContinuationTag(rule, server, paths, steps, warpProfiles); err == nil {
			continue
		}
		interfaceName := strings.TrimSpace(rule.InterfaceName)
		if err := ValidateNetworkInterfaceName(interfaceName); err != nil {
			return nil, fmt.Errorf("routing rule %s interface_name: %w", rule.Name, err)
		}
		if interfaceName == "" {
			return nil, fmt.Errorf("routing rule %s interface_name is required", rule.Name)
		}
		result = append(result, map[string]any{
			"type":           "direct",
			"tag":            routingRuleInterfaceOutboundTag(rule.ID),
			"bind_interface": interfaceName,
		})
	}
	return result, nil
}

func applyRoutingRuleProxyPathBindings(server model.Server, rules []model.RoutingRule, paths []model.ProxyPath, steps []model.ProxyPathStep, warpProfiles []model.WARPProfile, outbounds *[]map[string]any) error {
	if outbounds == nil {
		return nil
	}
	byTag := make(map[string]map[string]any, len(*outbounds))
	for _, outbound := range *outbounds {
		if outboundTag, _ := outbound["tag"].(string); outboundTag != "" {
			byTag[outboundTag] = outbound
		}
	}
	for _, rule := range rules {
		if !rule.Enabled || rule.Scope != model.RoutingRuleScopePathStage || rule.ServerID != server.ID {
			continue
		}
		var baseTag string
		var err error
		switch {
		case rule.Action == model.RouteActionProxyPath && routingRuleHasProxyPathBinding(rule):
			if strings.TrimSpace(rule.InterfaceName) != "" && strings.TrimSpace(rule.SourcePrefix) != "" {
				return fmt.Errorf("routing rule %s cannot bind both an interface and a source prefix", rule.Name)
			}
			baseTag, err = routingRuleProxyPathOutboundTag(rule, server, paths, steps, warpProfiles)
			if err != nil {
				return fmt.Errorf("routing rule %s: %w", rule.Name, err)
			}
		case rule.Action == model.RouteActionInterface && strings.TrimSpace(rule.InterfaceName) != "":
			baseTag, err = routingRuleSamePathContinuationTag(rule, server, paths, steps, warpProfiles)
			if err != nil {
				continue
			}
		default:
			continue
		}
		base, ok := byTag[baseTag]
		if !ok {
			if _, isWARP := routingRuleWARPProfileForTag(server.ID, baseTag, warpProfiles); isWARP {
				continue
			}
			return fmt.Errorf("routing rule %s cannot bind non-outbound target %q", rule.Name, baseTag)
		}
		chain := []map[string]any{base}
		seen := map[string]bool{baseTag: true}
		for {
			detour, _ := chain[len(chain)-1]["detour"].(string)
			if detour == "" {
				break
			}
			if seen[detour] {
				return fmt.Errorf("routing rule %s target outbound detour cycle at %q", rule.Name, detour)
			}
			next, ok := byTag[detour]
			if !ok {
				return fmt.Errorf("routing rule %s target outbound detour %q is unavailable", rule.Name, detour)
			}
			seen[detour] = true
			chain = append(chain, next)
		}
		for index := len(chain) - 1; index >= 0; index-- {
			bound := cloneNestedMap(chain[index])
			originalTag, _ := bound["tag"].(string)
			boundTag := routingRuleBoundOutboundTag(rule.ID, originalTag)
			bound["tag"] = boundTag
			if index == len(chain)-1 {
				if interfaceName := strings.TrimSpace(rule.InterfaceName); interfaceName != "" {
					if err := ValidateNetworkInterfaceName(interfaceName); err != nil {
						return fmt.Errorf("routing rule %s interface_name: %w", rule.Name, err)
					}
					if dest, _ := bound["server"].(string); routingRuleShouldBindInterface(rule, dest) {
						bound["bind_interface"] = interfaceName
					}
				} else {
					prefix, err := netip.ParsePrefix(strings.TrimSpace(rule.SourcePrefix))
					if err != nil {
						return fmt.Errorf("routing rule %s source_prefix: %w", rule.Name, err)
					}
					bound["detour"] = sourcePrefixOutboundTag(prefix.Masked().String())
				}
			} else if detour, _ := bound["detour"].(string); detour != "" {
				bound["detour"] = routingRuleBoundOutboundTag(rule.ID, detour)
			}
			*outbounds = append(*outbounds, bound)
			byTag[boundTag] = bound
		}
	}
	return nil
}

func buildRoutingRuleFamilySplitOutbounds(server model.Server, opts ConfigOptions, pathOutbounds []map[string]any, plannedInbounds map[int64]model.Inbound, defaultResolver any, inheritedDNSStrategy string) ([]map[string]any, error) {
	inboundByID := make(map[int64]model.Inbound, len(opts.Inbounds)+len(plannedInbounds))
	for _, inbound := range opts.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	for id, inbound := range plannedInbounds {
		inboundByID[id] = inbound
	}
	serverByID := make(map[int64]model.Server, len(opts.Servers)+1)
	for _, item := range opts.Servers {
		serverByID[item.ID] = item
	}
	serverByID[server.ID] = server
	externalByID := make(map[int64]model.ExternalOutbound, len(opts.ExternalOutbounds))
	for _, item := range opts.ExternalOutbounds {
		externalByID[item.ID] = item
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range opts.ProxyPathSteps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	chainServices, err := buildProxyPathChainServices(opts.ProxyPaths, opts.ProxyPathSteps, opts.Servers, opts.Inbounds, opts.PortLedger)
	if err != nil {
		return nil, err
	}
	workingOutbounds := append([]map[string]any(nil), pathOutbounds...)
	result := make([]map[string]any, 0)
	for _, rule := range opts.RoutingRules {
		if !rule.Enabled || rule.Action != model.RouteActionFamilySplit || rule.Scope != model.RoutingRuleScopePathStage || rule.ServerID != server.ID {
			continue
		}
		if strings.TrimSpace(server.AgentID) != "" && !stringSliceContains(server.KernelCapabilities, "family_selector_v1") {
			return nil, markInvalidDesiredState(fmt.Errorf("服务器 %s 的内核缺少 family_selector_v1 能力；请先更新 Agent/内核", server.Name))
		}
		if rule.FamilySplitTemplateID == nil || *rule.FamilySplitTemplateID <= 0 {
			return nil, fmt.Errorf("routing rule %s: family_split_template_id required", rule.Name)
		}
		ipv4Path, ipv6Path, err := FamilySplitTemplatePaths(opts.ProxyPaths, *rule.FamilySplitTemplateID)
		if err != nil {
			return nil, markInvalidDesiredState(fmt.Errorf("routing rule %s: %w", rule.Name, err))
		}
		if !ipv4Path.Enabled || !ipv6Path.Enabled {
			return nil, markInvalidDesiredState(fmt.Errorf("routing rule %s: family split template branches are not enabled", rule.Name))
		}
		strategy, err := normalizedFamilyDNSStrategy(rule.FamilyDNSStrategy, server, inheritedDNSStrategy)
		if err != nil {
			return nil, markInvalidDesiredState(fmt.Errorf("routing rule %s: %w", rule.Name, err))
		}
		selector := map[string]any{
			"type":     "family-selector",
			"tag":      routingRuleFamilySelectorTag(rule.ID),
			"strategy": strategy,
			"fallback": true,
		}
		for _, branch := range []struct {
			family  string
			path    model.ProxyPath
			jsonKey string
		}{
			{family: "ipv4", path: ipv4Path, jsonKey: "ipv4_outbound"},
			{family: "ipv6", path: ipv6Path, jsonKey: "ipv6_outbound"},
		} {
			branchTag, clones, generated, err := buildFamilySplitBranchOutbound(rule, branch.family, branch.path, server, opts, inboundByID, serverByID, externalByID, stepsByPath, chainServices, workingOutbounds, defaultResolver)
			if err != nil {
				return nil, markInvalidDesiredState(fmt.Errorf("routing rule %s %s branch: %w", rule.Name, branch.family, err))
			}
			workingOutbounds = append(workingOutbounds, generated...)
			result = append(result, clones...)
			selector[branch.jsonKey] = branchTag
		}
		resolver := domainResolverMap(defaultResolver)
		if strings.TrimSpace(rule.DNSResolver) != "" {
			resolver["server"] = strings.TrimSpace(rule.DNSResolver)
		}
		resolver["strategy"] = strategy
		selector["domain_resolver"] = resolver
		result = append(result, selector)
	}
	return result, nil
}

func buildFamilySplitBranchOutbound(rule model.RoutingRule, family string, path model.ProxyPath, server model.Server, opts ConfigOptions, inboundByID map[int64]model.Inbound, serverByID map[int64]model.Server, externalByID map[int64]model.ExternalOutbound, stepsByPath map[int64][]model.ProxyPathStep, chainServices map[proxyPathChainServiceKey]*proxyPathChainService, pathOutbounds []map[string]any, defaultResolver any) (string, []map[string]any, []map[string]any, error) {
	steps := OrderedFamilyBranchSteps(stepsByPath[path.ID])
	if err := ValidateFamilyBranchTransport(steps); err != nil {
		return "", nil, nil, err
	}
	binding, err := FamilyBranchLastBinding(steps)
	if err != nil {
		return "", nil, nil, err
	}
	remaining := CollapseFamilyBranchSteps(server.ID, steps, inboundByID)
	if len(remaining) == 0 {
		if binding.InterfaceName == "" && binding.SourcePrefix == "" {
			return "direct", nil, nil, nil
		}
		tag := FamilySplitBoundExitTag(rule.ID, family)
		item := map[string]any{"type": "direct", "tag": tag}
		if err := applyFamilyBranchExitBinding(item, binding); err != nil {
			return "", nil, nil, err
		}
		return tag, []map[string]any{item}, nil, nil
	}
	first := remaining[0]
	generated := []map[string]any{}
	if first.NodeType == model.ProxyPathStepWARP {
		profile, ok := warpProfileForServer(opts.WARPProfiles, server.ID)
		if !ok || !profile.Enabled {
			return "", nil, nil, fmt.Errorf("family branch requires WARP on server %d", server.ID)
		}
		baseTag := tag("warp", profile.ID)
		if binding.InterfaceName == "" && binding.SourcePrefix == "" {
			return baseTag, nil, nil, nil
		}
		cloneTag := routingRuleFamilyBranchTag(rule.ID, family, baseTag)
		item := map[string]any{"type": "direct", "tag": cloneTag, "detour": baseTag}
		if err := applyFamilyBranchExitBinding(item, binding); err != nil {
			return "", nil, nil, err
		}
		return cloneTag, []map[string]any{item}, nil, nil
	}
	baseTag := proxyPathStepTag(path.ID, first.Position)
	if !outboundTagExists(pathOutbounds, baseTag) {
		item, err := proxyPathStepOutbound(path, first, server, serverByID, inboundByID, externalByID, nil, baseTag, chainServices, opts.PortLedger)
		if err != nil {
			return "", nil, nil, err
		}
		generated = append(generated, item)
		pathOutbounds = append(pathOutbounds, item)
	}
	entryAddress := ""
	if first.NodeType == model.ProxyPathStepServerInbound {
		entryInbound, targetServer, err := familyBranchFirstHopEntry(first, family, inboundByID, serverByID)
		if err != nil {
			return "", nil, nil, err
		}
		entryAddress, err = ResolveReachableEntryAddressForFamily(server, entryInbound, targetServer, family)
		if err != nil {
			return "", nil, nil, err
		}
	}
	clones, branchTag, err := cloneRoutingRuleFamilyBranch(rule.ID, family, baseTag, entryAddress, pathOutbounds, defaultResolver)
	if err != nil {
		return "", nil, nil, err
	}
	if len(remaining) > 0 && remaining[len(remaining)-1].ID == steps[len(steps)-1].ID && (binding.InterfaceName != "" || binding.SourcePrefix != "") {
		if err := applyFamilyBranchExitBinding(clones[len(clones)-1], binding); err != nil {
			return "", nil, nil, err
		}
	}
	return branchTag, clones, generated, nil
}

func outboundTagExists(outbounds []map[string]any, wanted string) bool {
	for _, outbound := range outbounds {
		if tag, _ := outbound["tag"].(string); tag == wanted {
			return true
		}
	}
	return false
}

func familyBranchFirstHopEntry(step model.ProxyPathStep, family string, inboundByID map[int64]model.Inbound, serverByID map[int64]model.Server) (model.Inbound, model.Server, error) {
	targetServerID, inbound, ok := proxyPathStepTargetServer(step, inboundByID)
	if !ok {
		return model.Inbound{}, model.Server{}, errors.New("family branch first hop target is missing")
	}
	targetServer, ok := serverByID[targetServerID]
	if !ok {
		return model.Inbound{}, model.Server{}, errors.New("family branch first hop server is missing")
	}
	if targetServer.Status != model.ServerOnline {
		return model.Inbound{}, model.Server{}, fmt.Errorf("target server %s is offline", targetServer.Name)
	}
	if inbound == nil {
		synthetic := model.Inbound{ServerID: targetServer.ID, ListenIP: targetServer.ListenIP, EntryIPMode: model.EntryIPModeAuto, Enabled: true}
		return synthetic, targetServer, nil
	}
	if !inbound.Enabled {
		return model.Inbound{}, model.Server{}, errors.New("target inbound is disabled")
	}
	_ = family
	return *inbound, targetServer, nil
}

func normalizedFamilyDNSStrategy(strategy model.FamilyDNSStrategy, server model.Server, inherited string) (string, error) {
	switch strategy {
	case "", model.FamilyDNSStrategyAuto:
		if strings.TrimSpace(inherited) != "" {
			return normalizeDNSStrategy(inherited, EffectiveIPStack(server)), nil
		}
		return normalizeDNSStrategy("auto", EffectiveIPStack(server)), nil
	case model.FamilyDNSStrategyPreferIPv4, model.FamilyDNSStrategyPreferIPv6:
		return string(strategy), nil
	default:
		return "", fmt.Errorf("unsupported family_dns_strategy %q", strategy)
	}
}

func routingRuleFamilyTargetEntry(rule model.RoutingRule, targetPathID int64, family string, inboundByID map[int64]model.Inbound, serverByID map[int64]model.Server, steps []model.ProxyPathStep) (model.Inbound, model.Server, error) {
	stagePosition := 0
	if rule.StageStepID != nil {
		for _, step := range steps {
			if rule.ProxyPathID != nil && step.PathID == *rule.ProxyPathID && step.ID == *rule.StageStepID {
				stagePosition = step.Position
				break
			}
		}
		if stagePosition == 0 {
			return model.Inbound{}, model.Server{}, errors.New("routing stage is unavailable")
		}
	}
	var candidates []model.ProxyPathStep
	for _, step := range steps {
		if step.PathID == targetPathID && step.Position > stagePosition {
			candidates = append(candidates, step)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Position < candidates[j].Position })
	if len(candidates) == 0 {
		return model.Inbound{}, model.Server{}, errors.New("target path does not continue after the family split stage")
	}
	next := candidates[0]
	mode := next.TransportMode
	if mode == "" {
		mode = model.ProxyPathTransportSingBox
	}
	if mode != model.ProxyPathTransportSingBox || next.NodeType != model.ProxyPathStepServerInbound {
		return model.Inbound{}, model.Server{}, errors.New("family branch must enter a controlled server through a sing-box hop")
	}
	var inbound model.Inbound
	if next.InboundID != nil && *next.InboundID > 0 {
		var ok bool
		inbound, ok = inboundByID[*next.InboundID]
		if !ok || !inbound.Enabled {
			return model.Inbound{}, model.Server{}, fmt.Errorf("target inbound %d is unavailable", *next.InboundID)
		}
	} else {
		if next.ServerID == nil || *next.ServerID <= 0 {
			return model.Inbound{}, model.Server{}, errors.New("generated target server is unavailable")
		}
		inbound = model.Inbound{ServerID: *next.ServerID, EntryIPMode: model.EntryIPModeAuto, Enabled: true}
	}
	targetServer, ok := serverByID[inbound.ServerID]
	if !ok {
		return model.Inbound{}, model.Server{}, fmt.Errorf("target server %d is unavailable", inbound.ServerID)
	}
	if targetServer.Status != model.ServerOnline {
		return model.Inbound{}, model.Server{}, fmt.Errorf("target server %s is offline", targetServer.Name)
	}
	return inbound, targetServer, nil
}

func cloneRoutingRuleFamilyBranch(ruleID int64, family, baseTag, entryAddress string, outbounds []map[string]any, defaultResolver any) ([]map[string]any, string, error) {
	byTag := make(map[string]map[string]any, len(outbounds))
	for _, outbound := range outbounds {
		if outboundTag, _ := outbound["tag"].(string); outboundTag != "" {
			byTag[outboundTag] = outbound
		}
	}
	base, ok := byTag[baseTag]
	if !ok {
		return nil, "", fmt.Errorf("target outbound %q is unavailable", baseTag)
	}
	chain := []map[string]any{base}
	seen := map[string]bool{baseTag: true}
	for {
		detour, _ := chain[len(chain)-1]["detour"].(string)
		if detour == "" {
			break
		}
		if seen[detour] {
			return nil, "", fmt.Errorf("target outbound detour cycle at %q", detour)
		}
		next, ok := byTag[detour]
		if !ok {
			return nil, "", fmt.Errorf("target outbound detour %q is unavailable", detour)
		}
		seen[detour] = true
		chain = append(chain, next)
	}
	clones := make([]map[string]any, 0, len(chain))
	for index, original := range chain {
		clone := cloneNestedMap(original)
		originalTag, _ := original["tag"].(string)
		clone["tag"] = routingRuleFamilyBranchTag(ruleID, family, originalTag)
		if detour, _ := original["detour"].(string); detour != "" {
			clone["detour"] = routingRuleFamilyBranchTag(ruleID, family, detour)
		}
		if index == 0 && strings.TrimSpace(entryAddress) != "" {
			clone["server"] = entryAddress
			if AddressFamily(entryAddress) == "domain" {
				resolver := domainResolverMap(firstNonNil(clone["domain_resolver"], defaultResolver))
				resolver["strategy"] = family + "_only"
				clone["domain_resolver"] = resolver
			}
		}
		clones = append(clones, clone)
	}
	return clones, routingRuleFamilyBranchTag(ruleID, family, baseTag), nil
}

func domainResolverMap(value any) map[string]any {
	switch resolver := value.(type) {
	case map[string]any:
		return cloneNestedMap(resolver)
	case string:
		return map[string]any{"server": strings.TrimSpace(resolver)}
	default:
		return map[string]any{"server": primaryBootstrapDNSTag}
	}
}

func stringSliceContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func routingRuleWARPBindingDNSStrategy(rule model.RoutingRule) (string, error) {
	if strings.TrimSpace(rule.InterfaceName) != "" {
		switch rule.InterfaceIPStack {
		case model.IPStackIPv4Only:
			return "ipv4_only", nil
		case model.IPStackIPv6Only:
			return "ipv6_only", nil
		case model.IPStackDualStack:
			return "", nil
		default:
			return "", markInvalidDesiredState(fmt.Errorf("分流规则 %s 无法确认网卡 %s 的地址族；请重新读取 Agent 网卡后再下发", rule.Name, rule.InterfaceName))
		}
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(rule.SourcePrefix))
	if err != nil {
		return "", fmt.Errorf("routing rule %s source_prefix: %w", rule.Name, err)
	}
	if prefix.Addr().Is4() {
		return "ipv4_only", nil
	}
	return "ipv6_only", nil
}

func applyRoutingRuleWARPEndpointBindings(server model.Server, rules []model.RoutingRule, paths []model.ProxyPath, steps []model.ProxyPathStep, warpProfiles []model.WARPProfile, endpoints *[]map[string]any) error {
	if endpoints == nil {
		return nil
	}
	byTag := make(map[string]map[string]any, len(*endpoints))
	for _, endpoint := range *endpoints {
		if endpointTag, _ := endpoint["tag"].(string); endpointTag != "" {
			byTag[endpointTag] = endpoint
		}
	}
	for _, rule := range rules {
		if !rule.Enabled || rule.Scope != model.RoutingRuleScopePathStage || rule.ServerID != server.ID {
			continue
		}
		var baseTag string
		var err error
		switch {
		case rule.Action == model.RouteActionProxyPath && routingRuleHasProxyPathBinding(rule):
			baseTag, err = routingRuleProxyPathOutboundTag(rule, server, paths, steps, warpProfiles)
			if err != nil {
				return fmt.Errorf("routing rule %s: %w", rule.Name, err)
			}
		case rule.Action == model.RouteActionInterface && strings.TrimSpace(rule.InterfaceName) != "":
			baseTag, err = routingRuleSamePathContinuationTag(rule, server, paths, steps, warpProfiles)
			if err != nil {
				continue
			}
		default:
			continue
		}
		if _, isWARP := routingRuleWARPProfileForTag(server.ID, baseTag, warpProfiles); !isWARP {
			continue
		}
		base, ok := byTag[baseTag]
		if !ok {
			return fmt.Errorf("routing rule %s WARP endpoint %q is unavailable", rule.Name, baseTag)
		}
		bound := cloneNestedMap(base)
		boundTag := routingRuleBoundOutboundTag(rule.ID, baseTag)
		bound["tag"] = boundTag
		strategy, err := routingRuleWARPBindingDNSStrategy(rule)
		if err != nil {
			return err
		}
		if strategy != "" {
			applyManagedWARPDomainResolver(bound, strategy)
		}
		if interfaceName := strings.TrimSpace(rule.InterfaceName); interfaceName != "" {
			if err := ValidateNetworkInterfaceName(interfaceName); err != nil {
				return fmt.Errorf("routing rule %s interface_name: %w", rule.Name, err)
			}
			delete(bound, "detour")
			bound["bind_interface"] = interfaceName
		} else {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(rule.SourcePrefix))
			if err != nil {
				return fmt.Errorf("routing rule %s source_prefix: %w", rule.Name, err)
			}
			delete(bound, "bind_interface")
			bound["detour"] = sourcePrefixOutboundTag(prefix.Masked().String())
		}
		*endpoints = append(*endpoints, bound)
		byTag[boundTag] = bound
	}
	return nil
}

func routingRuleWARPProfileForTag(serverID int64, outboundTag string, profiles []model.WARPProfile) (model.WARPProfile, bool) {
	for _, profile := range profiles {
		if profile.ServerID == serverID && profile.Enabled && tag("warp", profile.ID) == outboundTag {
			return profile, true
		}
	}
	return model.WARPProfile{}, false
}

func sameOptionalID(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func routingRuleSetTag(id int64) string { return fmt.Sprintf("routing-rule-set-%d", id) }

func buildSourcePrefixOutbounds(server model.Server, rules []model.RoutingRule) ([]map[string]any, error) {
	seen := map[string]bool{}
	result := make([]map[string]any, 0)
	for _, rule := range rules {
		if !rule.Enabled || rule.ServerID != server.ID || (rule.Action != model.RouteActionSourcePrefix && !(rule.Action == model.RouteActionProxyPath && strings.TrimSpace(rule.SourcePrefix) != "")) {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(rule.SourcePrefix))
		if err != nil {
			return nil, fmt.Errorf("routing rule %s source_prefix: %w", rule.Name, err)
		}
		canonical := prefix.Masked().String()
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		item := map[string]any{
			"type":   "source-prefix",
			"tag":    sourcePrefixOutboundTag(canonical),
			"prefix": canonical,
		}
		strategy := "ipv4_only"
		if prefix.Addr().Is6() {
			strategy = "ipv6_only"
		}
		applyDialDomainResolver(item, strategy)
		result = append(result, item)
	}
	return result, nil
}

func sourcePrefixOutboundTag(prefix string) string {
	canonical := strings.TrimSpace(prefix)
	if parsed, err := netip.ParsePrefix(canonical); err == nil {
		canonical = parsed.Masked().String()
	}
	sum := sha256.Sum256([]byte(canonical))
	return "source-prefix-" + hex.EncodeToString(sum[:6])
}

func buildRouteRuleSets(server model.Server, rules []model.RoutingRule, sets []model.RoutingRuleSet) []map[string]any {
	wanted := map[int64]bool{}
	for _, rule := range rules {
		if rule.Enabled && rule.ServerID == server.ID && rule.MatchSource == model.RoutingMatchSourceRuleSet && rule.RuleSetID != nil {
			wanted[*rule.RuleSetID] = true
		}
	}
	result := make([]map[string]any, 0, len(wanted))
	for _, set := range sets {
		if !wanted[set.ID] || set.Revision == "" {
			continue
		}
		format := "source"
		filename := "rules.json"
		if set.Format == model.RoutingRuleSetFormatSingBoxBinary {
			format, filename = "binary", "rules.srs"
		}
		result = append(result, map[string]any{"type": "local", "tag": routingRuleSetTag(set.ID), "format": format, "path": fmt.Sprintf("oboard-asset://routing-rule-set/%d/%s", set.ID, filename)})
	}
	sort.SliceStable(result, func(i, j int) bool { return fmt.Sprint(result[i]["tag"]) < fmt.Sprint(result[j]["tag"]) })
	return result
}

func buildDNSRules(server model.Server, rules []model.RoutingRule, sets []model.RoutingRuleSet) ([]map[string]any, error) {
	filtered := make([]model.RoutingRule, 0)
	for _, rule := range rules {
		if rule.ServerID == server.ID && rule.Enabled && strings.TrimSpace(rule.DNSResolver) != "" {
			filtered = append(filtered, rule)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Priority == filtered[j].Priority {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].Priority < filtered[j].Priority
	})
	result := make([]map[string]any, 0, len(filtered))
	for _, rule := range filtered {
		item := map[string]any{}
		if rule.MatchSource == model.RoutingMatchSourceRuleSet {
			if rule.RuleSetID == nil {
				return nil, fmt.Errorf("routing rule %s: rule_set_id required", rule.Name)
			}
			found := false
			for _, set := range sets {
				if set.ID == *rule.RuleSetID && set.Revision != "" {
					item["rule_set"] = []string{routingRuleSetTag(set.ID)}
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("routing rule %s: rule set %d has no successful snapshot", rule.Name, *rule.RuleSetID)
			}
		} else if strings.TrimSpace(rule.MatchJSON) != "" && strings.TrimSpace(rule.MatchJSON) != "{}" {
			var match map[string]any
			if err := json.Unmarshal([]byte(rule.MatchJSON), &match); err != nil {
				return nil, fmt.Errorf("routing rule %s match_json: %w", rule.Name, err)
			}
			for _, key := range []string{"domain", "domain_suffix", "domain_keyword", "domain_regex", "geosite", "geoip", "ip_cidr"} {
				if value, ok := match[key]; ok {
					item[key] = value
				}
			}
		}
		if len(item) == 0 {
			continue
		}
		item["server"] = strings.TrimSpace(rule.DNSResolver)
		result = append(result, item)
	}
	return result, nil
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

func ResolveReachableEntryAddressForFamily(source model.Server, inbound model.Inbound, target model.Server, family string) (string, error) {
	family = strings.ToLower(strings.TrimSpace(family))
	if family != "ipv4" && family != "ipv6" {
		return "", markInvalidDesiredState(fmt.Errorf("unsupported entry address family %q", family))
	}
	if err := validateSourceFamilyReachability(source, family); err != nil {
		return "", err
	}
	if !ListenIPSupportsFamily(EffectiveListenIP(target, inbound.ListenIP), family) {
		return "", markInvalidDesiredState(fmt.Errorf("目标服务器 %s 的监听地址 %q 不接受 %s 入口连接", target.Name, EffectiveListenIP(target, inbound.ListenIP), strings.ToUpper(family)))
	}
	mode := inbound.EntryIPMode
	if mode == "" || mode == model.EntryIPModeAuto {
		mode = target.EntryIPMode
	}
	if mode == model.EntryIPModeIPv4 && family != "ipv4" {
		return "", markInvalidDesiredState(fmt.Errorf("目标服务器 %s 的入口策略固定为 IPv4，不能用于 IPv6 家族分支", target.Name))
	}
	if mode == model.EntryIPModeIPv6 && family != "ipv6" {
		return "", markInvalidDesiredState(fmt.Errorf("目标服务器 %s 的入口策略固定为 IPv6，不能用于 IPv4 家族分支", target.Name))
	}
	familyAddress := strings.TrimSpace(target.PublicIPv4)
	if family == "ipv6" {
		familyAddress = ServerEntryIPv6(target)
	}
	if familyAddress == "" {
		return "", markInvalidDesiredState(fmt.Errorf("目标服务器 %s 缺少 %s 入口地址", target.Name, strings.ToUpper(family)))
	}
	if inbound.DNSSyncEnabled && strings.TrimSpace(inbound.DNSDomain) != "" {
		return strings.TrimSpace(inbound.DNSDomain), nil
	}
	if mode == model.EntryIPModeCustom {
		address := firstNonEmpty(strings.TrimSpace(inbound.ExternalIP), strings.TrimSpace(target.EntryAddress))
		if address == "" {
			return "", markInvalidDesiredState(fmt.Errorf("目标服务器 %s 的自定义入口地址不存在", target.Name))
		}
		addressFamily := AddressFamily(address)
		if addressFamily != "domain" && addressFamily != family {
			return "", markInvalidDesiredState(fmt.Errorf("目标服务器 %s 的自定义入口地址 %q 不属于 %s", target.Name, address, strings.ToUpper(family)))
		}
		return address, nil
	}
	return familyAddress, nil
}

func validateSourceFamilyReachability(source model.Server, family string) error {
	stack := EffectiveIPStack(source)
	if stack == model.IPStackAuto || family == "ipv4" && stack == model.IPStackIPv6Only || family == "ipv6" && stack == model.IPStackIPv4Only {
		return markInvalidDesiredState(fmt.Errorf("源服务器 %s（%s）无法连接 %s 入口", source.Name, stack, strings.ToUpper(family)))
	}
	return nil
}

func ListenIPSupportsFamily(listenIP, family string) bool {
	listenIP = strings.TrimSpace(strings.Trim(listenIP, "[]"))
	if listenIP == "::" {
		return true
	}
	address, err := netip.ParseAddr(listenIP)
	if err != nil {
		return false
	}
	if family == "ipv4" {
		return address.Is4()
	}
	return address.Is6()
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
	ipv6 := ServerEntryIPv6(target)
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
		return ServerEntryIPv6(server)
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

func ResolveLiteralEntryAddress(inbound model.Inbound, server model.Server) string {
	mode := inbound.EntryIPMode
	if mode == "" || mode == model.EntryIPModeAuto {
		mode = server.EntryIPMode
	}
	switch mode {
	case model.EntryIPModeIPv4:
		return strings.TrimSpace(server.PublicIPv4)
	case model.EntryIPModeIPv6:
		return ServerEntryIPv6(server)
	case model.EntryIPModeCustom:
		if strings.TrimSpace(inbound.ExternalIP) != "" {
			return strings.TrimSpace(inbound.ExternalIP)
		}
		return strings.TrimSpace(server.EntryAddress)
	}
	return ResolveServerEntryAddress(server)
}

// ResolveDNSPreferredEntryAddress returns the managed DNS name when sync is on.
// Agent-side identities that must not rebuild when the client Host policy
// changes (restricted SSH inbound plans) use this instead of the subscription host.
func ResolveDNSPreferredEntryAddress(inbound model.Inbound, server model.Server) string {
	if inbound.DNSSyncEnabled && strings.TrimSpace(inbound.DNSDomain) != "" {
		return strings.TrimSpace(inbound.DNSDomain)
	}
	return ResolveLiteralEntryAddress(inbound, server)
}

func inboundClientHostUsesDomain(inbound model.Inbound, server model.Server, alwaysUseDomain bool) bool {
	domain := strings.TrimSpace(inbound.DNSDomain)
	if !inbound.DNSSyncEnabled || domain == "" {
		return false
	}
	if alwaysUseDomain || inbound.DDNSEnabled {
		return true
	}
	literal := ResolveLiteralEntryAddress(inbound, server)
	switch AddressFamily(literal) {
	case "domain", "":
		return true
	}
	switch inbound.EntryIPMode {
	case model.EntryIPModeIPv4, model.EntryIPModeIPv6, model.EntryIPModeCustom:
		return false
	}
	if strings.EqualFold(strings.TrimSpace(inbound.DNSRecordTypes), "both") {
		return true
	}
	return false
}

// ResolveEntryAddressHost is the client-facing subscription Host.
// TLS SNI stays the certificate/DNS domain. Static single-stack inbounds
// may advertise a public IP to skip a client lookup; dns_record_types=both
// always advertises the managed name so dual-stack A+AAAA can take effect
// even before both families are detected. DDNS, custom domain targets, and
// alwaysUseDomain also keep the managed name.
func ResolveEntryAddressHost(inbound model.Inbound, server model.Server, alwaysUseDomain bool) string {
	if inboundClientHostUsesDomain(inbound, server, alwaysUseDomain) {
		return strings.TrimSpace(inbound.DNSDomain)
	}
	if literal := ResolveLiteralEntryAddress(inbound, server); literal != "" {
		return literal
	}
	return strings.TrimSpace(inbound.DNSDomain)
}

func ResolveEntryAddress(inbound model.Inbound, server model.Server) string {
	return ResolveEntryAddressHost(inbound, server, false)
}

func ResolveServerEntryAddress(server model.Server) string {
	switch server.EntryIPMode {
	case model.EntryIPModeIPv4:
		return strings.TrimSpace(server.PublicIPv4)
	case model.EntryIPModeIPv6:
		return ServerEntryIPv6(server)
	case model.EntryIPModeCustom:
		return strings.TrimSpace(server.EntryAddress)
	}
	if strings.TrimSpace(server.PublicIPv4) != "" {
		return strings.TrimSpace(server.PublicIPv4)
	}
	return ServerEntryIPv6(server)
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

// resolveInboundUsers derives the identities one inbound serves. It returns
// the accounted users — the authorized users whose traffic and limits belong to
// this inbound — and the full listener set, which additionally carries the
// proxy path link identities and falls back to a placeholder when nothing is
// authorized. Both the generated core config and the Snell listener projection
// call it so the two can never disagree about who an inbound serves.
func resolveInboundUsers(inbound model.Inbound, users []model.User, opts ConfigOptions, serverSecret string) ([]model.User, []model.User, error) {
	accounted := usersForInbound(inbound, users, opts.InboundUsers)
	if branchUsers := proxyPathBranchUsersForInbound(inbound, users, opts.InboundUsers, opts.ProxyPathUsers, opts.ProxyPaths, opts.ProxyPathSteps); len(branchUsers) > 0 {
		accounted = branchUsers
	}
	accounted = credentialUsersForInbound(accounted, inbound)
	listeners := append(append([]model.User{}, accounted...), pathLinkUsersForInbound(inbound, opts.ProxyPaths, opts.ProxyPathSteps)...)
	if len(listeners) == 0 {
		placeholderUsers, err := placeholderUsersForInbound(inbound, serverSecret)
		if err != nil {
			return nil, nil, err
		}
		listeners = placeholderUsers
	}
	return accounted, listeners, nil
}

// configProjectionInbounds unions the inbounds a generation call was given with
// the full topology in ConfigOptions. Callers that only pass one of the two
// (fixtures pass the positional list, Controller passes both) must still see
// every inbound when a projection has to reason across servers.
func configProjectionInbounds(inbounds []model.Inbound, optsInbounds []model.Inbound) []model.Inbound {
	seen := map[int64]bool{}
	out := make([]model.Inbound, 0, len(inbounds)+len(optsInbounds))
	for _, group := range [][]model.Inbound{inbounds, optsInbounds} {
		for _, inbound := range group {
			if seen[inbound.ID] {
				continue
			}
			seen[inbound.ID] = true
			out = append(out, inbound)
		}
	}
	return out
}

// configProjectionServers guarantees the server being generated is present even
// when a fixture supplies no server topology.
func configProjectionServers(server model.Server, servers []model.Server) []model.Server {
	for _, item := range servers {
		if item.ID == server.ID {
			return servers
		}
	}
	return append(append([]model.Server{}, servers...), server)
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

func proxyPathBranchUsersForInbound(inbound model.Inbound, users []model.User, inboundUsers []model.InboundUser, pathUsers []model.ProxyPathUser, paths []model.ProxyPath, steps []model.ProxyPathStep) []model.User {
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
		out = append(out, proxyPathBranchUsersForPath(path, inbound, usersForProxyPath(path, inbound, users, inboundUsers, pathUsers))...)
	}
	return out
}

func usersForProxyPath(path model.ProxyPath, inbound model.Inbound, users []model.User, inboundUsers []model.InboundUser, pathUsers []model.ProxyPathUser) []model.User {
	if pathUsers == nil {
		return usersForInbound(inbound, users, inboundUsers)
	}
	allowed := map[int64]bool{}
	for _, binding := range pathUsers {
		if binding.Enabled && binding.ProxyPathID == path.ID {
			allowed[binding.UserID] = true
		}
	}
	out := make([]model.User, 0, len(allowed))
	for _, user := range users {
		if allowed[user.ID] {
			out = append(out, user)
		}
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
	if out.DeviceIDHash != "" {
		out = credentialUser(out, inbound.ID, path.ID, string(inbound.Protocol))
	}
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

// InboundAuthCapabilities is the unified per-protocol auth model. It replaces the
// scattered InboundSupportsMultipleUsers / protocolHasRoutingAuthUser helpers so
// a newly added protocol capability cannot be missed in one path.
type InboundAuthCapabilities struct {
	MultiUser           bool
	RoutingAuthUser     bool
	HasServerCredential bool
	HasUserCredential   bool
}

// AuthCapabilities returns the authoritative auth model for an inbound. All
// proxy-path, rate-limit, audit and subscription paths must read this record
// instead of guessing by protocol string.
func AuthCapabilities(inbound model.Inbound) InboundAuthCapabilities {
	switch inbound.Protocol {
	case model.ProtocolVLESS:
		return InboundAuthCapabilities{MultiUser: true, RoutingAuthUser: true, HasServerCredential: false, HasUserCredential: true}
	case model.ProtocolHY2:
		return InboundAuthCapabilities{MultiUser: true, RoutingAuthUser: true, HasServerCredential: false, HasUserCredential: true}
	case model.ProtocolAnyTLS:
		return InboundAuthCapabilities{MultiUser: true, RoutingAuthUser: true, HasServerCredential: false, HasUserCredential: true}
	case model.ProtocolMieru:
		return InboundAuthCapabilities{MultiUser: true, RoutingAuthUser: true, HasServerCredential: false, HasUserCredential: true}
	case model.ProtocolSnell:
		// Snell has no cross-client multi-user mode, so one panel inbound is
		// projected into one single-user listener per identity. Each listener
		// authenticates with its own PSK and carries no user table, which also
		// means route rules match it by inbound tag instead of auth_user.
		return InboundAuthCapabilities{MultiUser: false, RoutingAuthUser: false, HasServerCredential: false, HasUserCredential: true}
	case model.ProtocolSocks:
		return InboundAuthCapabilities{MultiUser: true, RoutingAuthUser: true, HasServerCredential: false, HasUserCredential: true}
	case model.ProtocolSSH:
		return InboundAuthCapabilities{MultiUser: true, RoutingAuthUser: true, HasServerCredential: false, HasUserCredential: true}
	case model.ProtocolSS:
		method := stringValue(parseExtra(inbound.ConfigJSON), "method", "2022-blake3-aes-128-gcm")
		multi := shadowsocksMethodSupportsUsers(method)
		return InboundAuthCapabilities{MultiUser: multi, RoutingAuthUser: multi, HasServerCredential: multi, HasUserCredential: true}
	default:
		return InboundAuthCapabilities{}
	}
}

func InboundSupportsMultipleUsers(inbound model.Inbound) bool {
	return AuthCapabilities(inbound).MultiUser
}

// protocolHasRoutingAuthUser reports whether sing-box route rules can match
// connections on this inbound by auth_user. Snell now participates as a normal
// multi-user protocol with per-user userkey authentication.
func protocolHasRoutingAuthUser(protocol model.Protocol) bool {
	return AuthCapabilities(model.Inbound{Protocol: protocol}).RoutingAuthUser
}

func routingAuthUsersForProtocol(protocol model.Protocol, users []string) []string {
	if !protocolHasRoutingAuthUser(protocol) {
		return nil
	}
	return users
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
		deviceSuffix := ""
		if value := strings.ToLower(strings.TrimSpace(user.DeviceIDHash)); value != "" {
			if len(value) > 12 {
				value = value[:12]
			}
			deviceSuffix = "-d" + value
		}
		if pathID := runtimePathIDFromUsername(user.Username); pathID > 0 {
			return fmt.Sprintf("oboard-u%d%s-p%d", user.ID, deviceSuffix, pathID)
		}
		return fmt.Sprintf("oboard-u%d%s", user.ID, deviceSuffix)
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

func socksPasswordUsers(users []model.User) []map[string]any {
	out := make([]map[string]any, 0, len(users))
	for _, user := range users {
		if strings.TrimSpace(user.Username) == "" || user.ProxyPassword == "" {
			continue
		}
		out = append(out, map[string]any{"username": user.Username, "password": user.ProxyPassword})
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

func omitUnsupportedDialTCPFastOpenAll(outbounds []map[string]any) {
	for _, item := range outbounds {
		omitUnsupportedDialTCPFastOpen(item)
	}
}

func omitUnsupportedDialTCPFastOpen(item map[string]any) {
	if item == nil {
		return
	}
	protocol := protocolFromKernelOutboundType(stringFromAny(item["type"]))
	if protocol == "" {
		return
	}
	encoded, err := json.Marshal(item)
	configJSON := ""
	if err == nil {
		configJSON = string(encoded)
	}
	if !OutboundSupportsTCPFastOpen(protocol, configJSON) {
		delete(item, "tcp_fast_open")
	}
}

func protocolFromKernelOutboundType(typ string) model.Protocol {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "vless":
		return model.ProtocolVLESS
	case "hysteria2":
		return model.ProtocolHY2
	case "anytls":
		return model.ProtocolAnyTLS
	case "shadowsocks":
		return model.ProtocolSS
	case "mieru":
		return model.ProtocolMieru
	case "snell":
		return model.ProtocolSnell
	case "socks":
		return model.ProtocolSocks
	default:
		return ""
	}
}

func applyAllowed(dst map[string]any, extra map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := extra[key]; ok && value != nil {
			dst[key] = value
		}
	}
}

func validateHY2Obfs(extra map[string]any) error {
	raw, exists := extra["obfs"]
	if !exists || raw == nil {
		return nil
	}
	obfs, ok := raw.(map[string]any)
	if !ok {
		return &ConfigFieldError{Path: "config_json.obfs", Problem: "must be an object"}
	}
	typ := strings.ToLower(strings.TrimSpace(stringValue(obfs, "type", "")))
	if typ != "salamander" {
		return fmt.Errorf("unsupported hysteria2 obfs type %q (only salamander)", typ)
	}
	if strings.TrimSpace(stringValue(obfs, "password", "")) == "" {
		return errors.New("hysteria2 salamander obfs requires password")
	}
	return nil
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
	if err := ValidatePort(v.Port); err != nil {
		return err
	}
	return validateTransportOptions(model.ProtocolVLESS, parseExtra(v.ConfigJSON), transportSideInbound)
}
func (vlessAdapter) ValidateOutbound(v model.Outbound) error {
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	if err := ValidatePort(v.TargetPort); err != nil {
		return err
	}
	return validateTransportOptions(model.ProtocolVLESS, parseExtra(v.ConfigJSON), transportSideOutbound)
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
	applyAllowed(item, extra, "multiplex", "transport", "network", "tcp_fast_open")
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
	applyAllowed(item, extra, "uuid", "tls", "network", "multiplex", "transport", "packet_encoding", "tcp_fast_open", "domain_resolver", "network_strategy", "fallback_delay")
	return item, nil
}
func (a vlessAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	node := map[string]any{"type": "vless", "tag": inbound.Name, "server": server.EntryAddress, "server_port": InboundSubscriptionPort(inbound), "uuid": user.ProxyUUID, "packet_encoding": stringValue(extra, "packet_encoding", "xudp")}
	if flow := stringValue(extra, "flow", ""); flow != "" {
		node["flow"] = flow
	}
	if tls, ok := extra["tls"]; ok {
		node["tls"] = subscriptionTLSForInbound(inbound, tls)
	}
	applyAllowed(node, extra, "transport", "network", "multiplex", "tcp_fast_open")
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
	extra := parseExtra(v.ConfigJSON)
	if err := validateTransportOptions(model.ProtocolHY2, extra, transportSideInbound); err != nil {
		return err
	}
	if err := validateHY2Obfs(extra); err != nil {
		return err
	}
	return requireInboundTLS(v.ConfigJSON, "hysteria2")
}
func (hy2Adapter) ValidateOutbound(v model.Outbound) error {
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	if err := ValidatePort(v.TargetPort); err != nil {
		return err
	}
	extra := parseExtra(v.ConfigJSON)
	if err := validateTransportOptions(model.ProtocolHY2, extra, transportSideOutbound); err != nil {
		return err
	}
	return validateHY2Obfs(extra)
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
	node := map[string]any{"type": "hysteria2", "tag": inbound.Name, "server": server.EntryAddress, "server_port": InboundSubscriptionPort(inbound), "password": user.ProxyPassword}
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
	extra := parseExtra(v.ConfigJSON)
	if err := ValidateAnyTLSPaddingScheme(extra["padding_scheme"]); err != nil {
		return err
	}
	if err := validateTransportOptions(model.ProtocolAnyTLS, extra, transportSideInbound); err != nil {
		return err
	}
	return requireInboundTLS(v.ConfigJSON, "anytls")
}
func (anyTLSAdapter) ValidateOutbound(v model.Outbound) error {
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	if err := ValidatePort(v.TargetPort); err != nil {
		return err
	}
	return validateTransportOptions(model.ProtocolAnyTLS, parseExtra(v.ConfigJSON), transportSideOutbound)
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
	applyAllowed(item, extra, "tls", "padding_scheme", "tcp_fast_open")
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
	omitUnsupportedDialTCPFastOpen(item)
	return item, nil
}
func (a anyTLSAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	node := map[string]any{"type": "anytls", "tag": inbound.Name, "server": server.EntryAddress, "server_port": InboundSubscriptionPort(inbound), "password": user.ProxyPassword}
	if tls, ok := extra["tls"]; ok {
		node["tls"] = subscriptionTLSForInbound(inbound, tls)
	}
	applyAllowed(node, extra, "tcp_fast_open")
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
	applyAllowed(item, extra, "traffic_pattern", "tcp_fast_open")
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
	applyAllowed(item, extra, "multiplexing", "traffic_pattern", "tcp_fast_open", "domain_resolver", "network_strategy", "fallback_delay")
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
	ports, _ := MieruInboundSubscriptionPorts(inbound)
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
	applyAllowed(node, extra, "multiplexing", "traffic_pattern", "tcp_fast_open")
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
	side := transportSideInbound
	if outbound {
		side = transportSideOutbound
	}
	return validateTransportOptions(model.ProtocolMieru, extra, side)
}

func boolValueWithDefault(value any, fallback bool) bool {
	if typed, ok := value.(bool); ok {
		return typed
	}
	return fallback
}

type socksAdapter struct{}

func (socksAdapter) Protocol() model.Protocol { return model.ProtocolSocks }
func (socksAdapter) ValidateInbound(v model.Inbound) error {
	if err := ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if err := ValidatePort(v.Port); err != nil {
		return err
	}
	return validateTransportOptions(model.ProtocolSocks, parseExtra(v.ConfigJSON), transportSideInbound)
}
func (socksAdapter) ValidateOutbound(v model.Outbound) error {
	if strings.TrimSpace(v.TargetAddress) == "" {
		return errors.New("target_address required")
	}
	if err := ValidatePort(v.TargetPort); err != nil {
		return err
	}
	return validateTransportOptions(model.ProtocolSocks, parseExtra(v.ConfigJSON), transportSideOutbound)
}
func (a socksAdapter) Inbound(v model.Inbound, users []model.User) (map[string]any, error) {
	if err := a.ValidateInbound(v); err != nil {
		return nil, err
	}
	extra := parseExtra(v.ConfigJSON)
	item := map[string]any{"type": "socks", "tag": tag("in", v.ID), "listen": v.ListenIP, "listen_port": v.Port, "users": socksPasswordUsers(users)}
	applyAllowed(item, extra, "udp_timeout", "tcp_fast_open")
	return item, nil
}
func (a socksAdapter) Outbound(v model.Outbound, user *model.User) (map[string]any, error) {
	if err := a.ValidateOutbound(v); err != nil {
		return nil, err
	}
	extra := parseExtra(v.ConfigJSON)
	item := map[string]any{"type": "socks", "tag": tag("out", v.ID), "server": v.TargetAddress, "server_port": v.TargetPort, "version": "5"}
	if user != nil {
		item["username"] = user.Username
		item["password"] = user.ProxyPassword
	}
	// sing-box SOCKSOutboundOptions has no `multiplex` field: SOCKS has no
	// connection reuse layer, so a multiplex object would be silently dropped.
	applyAllowed(item, extra, "version", "username", "password", "network", "udp_over_tcp", "tcp_fast_open", "domain_resolver", "network_strategy", "fallback_delay")
	return item, nil
}
func (a socksAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	if err := a.ValidateInbound(inbound); err != nil {
		return nil, err
	}
	extra := parseExtra(inbound.ConfigJSON)
	node := map[string]any{"type": "socks", "tag": inbound.Name, "server": server.EntryAddress, "server_port": InboundSubscriptionPort(inbound), "version": "5", "username": user.Username, "password": user.ProxyPassword}
	applyAllowed(node, extra, "network", "udp_over_tcp", "tcp_fast_open")
	if server.UDPInboundMode == model.UDPInboundBlock || server.UDPInboundMode == model.UDPInboundUoT {
		node["network"] = "tcp"
		delete(node, "udp_over_tcp")
	}
	return node, nil
}

// Snell panel version semantics:
//
//   - The panel exposes v4 and v6. sing-box upstream implements Snell v4
//     outbounds and v5/v6 inbounds; the v5 inbound accepts v4 clients
//     (Surge documents Snell v5 as backward compatible with v4 clients).
//   - A panel v4 server entry therefore maps to the sing-box v5 inbound and
//     advertises v4 to subscription clients. A panel v6 server entry maps to
//     the sing-box v6 inbound and advertises v6.
//   - Version 5 is never emitted to clients: sing-box upstream does not
//     implement a v5 outbound, so v5 nodes would not be usable.
//
// Snell is a single-user protocol at the client boundary. sing-box does accept
// a `users[]` table, but the AEAD key still derives from the server PSK alone
// and the per-user `userkey` is only an identity tag; worse, no client except
// sing-box has a userkey field, so a multi-user listener rejects Surge, Mihomo,
// Egern, Shadowrocket and Surfboard outright. One panel Snell inbound is
// therefore projected into one single-user listener per identity, each on its
// own port with its own PSK derived from `config_json.psk`. See
// snell_listeners.go for the projection; the adapter below only renders the
// non-user-specific shape and the client-facing node.
//
// UDP relay rides on the established TCP stream, not on a native UDP
// listener, so Snell inbounds remain TCP-only listeners and stay valid
// under every `udp_inbound_mode`.

const (
	SnellVersionV4 = 4
	SnellVersionV6 = 6
)

// SnellServerVersion maps a panel-level Snell version to the sing-box inbound
// version: v4 panel entries use the v5 inbound (compatible with v4 clients),
// v6 panel entries use the v6 inbound.
func SnellServerVersion(panelVersion int) (int, error) {
	switch panelVersion {
	case SnellVersionV4:
		return 5, nil
	case SnellVersionV6:
		return 6, nil
	default:
		return 0, fmt.Errorf("unsupported snell version %d", panelVersion)
	}
}

// SnellClientVersion maps a panel-level Snell version to the version
// advertised to subscription clients. v5 is never emitted because sing-box
// upstream has no v5 outbound.
func SnellClientVersion(panelVersion int) (int, error) {
	switch panelVersion {
	case SnellVersionV4, SnellVersionV6:
		return panelVersion, nil
	default:
		return 0, fmt.Errorf("unsupported snell version %d", panelVersion)
	}
}

func normalizeSnellObfsMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "none":
		return "none", nil
	case "http":
		return "http", nil
	default:
		// sing-box and Surge document Snell v4/v5 obfs as none/http only;
		// TLS obfs (v1-3 era) is intentionally not exposed by the panel.
		return "", fmt.Errorf("unsupported snell obfs_mode %q (only none or http)", mode)
	}
}

func normalizeSnellV6Mode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "default":
		return "default", nil
	case "unshaped":
		return "unshaped", nil
	case "unsafe-raw":
		return "unsafe-raw", nil
	default:
		return "", fmt.Errorf("unsupported snell v6 mode %q", mode)
	}
}

func snellPanelVersion(extra map[string]any) (int, error) {
	raw, ok := extra["version"]
	if !ok || raw == nil {
		return SnellVersionV4, nil
	}
	switch typed := raw.(type) {
	case float64:
		return int(typed), nil
	case json.Number:
		n, err := typed.Int64()
		if err != nil {
			return 0, errors.New("snell version must be an integer")
		}
		return int(n), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, errors.New("snell version must be an integer")
		}
		return n, nil
	default:
		return 0, errors.New("snell version must be an integer")
	}
}

// snellServerPSK resolves the stable server PSK for an inbound. It is strictly
// the inbound's config_json.psk and never falls back to a user password. The
// PSK is expected to be persisted at creation time and remain unchanged when
// users are added, removed, or rotate their passwords.
func snellServerPSK(extra map[string]any) (string, error) {
	psk := strings.TrimSpace(stringValue(extra, "psk", ""))
	if psk == "" {
		return "", errors.New("snell psk required (config_json.psk)")
	}
	version, err := snellPanelVersion(extra)
	if err != nil {
		return "", err
	}
	if err := validateSnellPSKLength(psk, version); err != nil {
		return "", err
	}
	return psk, nil
}

// snellPSK is kept for migration only: it mirrors the old fallback (first bound
// user password). New code must use snellServerPSK. Do not add new callers.
func snellPSK(extra map[string]any, users []model.User) (string, error) {
	psk := strings.TrimSpace(stringValue(extra, "psk", ""))
	if psk == "" {
		for _, user := range users {
			if candidate := strings.TrimSpace(user.ProxyPassword); candidate != "" {
				psk = candidate
				break
			}
		}
	}
	if psk == "" {
		return "", errors.New("snell psk required (config_json.psk or a bound user password)")
	}
	version, err := snellPanelVersion(extra)
	if err != nil {
		return "", err
	}
	if err := validateSnellPSKLength(psk, version); err != nil {
		return "", err
	}
	return psk, nil
}

// snellUserKey is the userkey an OBoard outbound presents when dialing a
// third-party Snell server that runs in multi-user mode. OBoard's own Snell
// inbounds never run that way — see snell_listeners.go — so nothing on the
// inbound side calls this.
func snellUserKey(user model.User) string {
	return strings.TrimSpace(user.ProxyPassword)
}

// ValidateSnellPSKForVersion is the exported variant used by store migrations
// and tests to validate a PSK against a panel version.
func ValidateSnellPSKForVersion(psk string, version int) error {
	return validateSnellPSKLength(psk, version)
}

// validateSnellPSKLength enforces the sing-box 1.14 Snell contract: v6 PSKs
// must be 12-255 bytes (sing-snell v6 server rejects anything else); v4 keeps
// an 8-byte floor consistent with panel user password strength.
func validateSnellPSKLength(psk string, version int) error {
	length := len([]byte(psk))
	if version == SnellVersionV6 {
		if length < 12 || length > 255 {
			return fmt.Errorf("snell v6 psk must be between 12 and 255 bytes")
		}
		return nil
	}
	if length < 8 {
		return errors.New("snell psk must be at least 8 characters")
	}
	return nil
}

func snellReuse(extra map[string]any) bool {
	return boolValueWithDefault(extra["reuse"], false)
}

func validateSnellOptions(extra map[string]any, side transportSide) error {
	version, err := snellPanelVersion(extra)
	if err != nil {
		return err
	}
	if version != SnellVersionV4 && version != SnellVersionV6 {
		return fmt.Errorf("unsupported snell version %d", version)
	}
	obfs, err := normalizeSnellObfsMode(stringValue(extra, "obfs_mode", "none"))
	if err != nil {
		return err
	}
	if version == SnellVersionV6 && obfs != "none" {
		return errors.New("snell v6 does not support obfs_mode")
	}
	// obfs_host is optional: sing-box defaults it to bing.com for http obfs.
	if _, err := normalizeSnellV6Mode(stringValue(extra, "mode", "default")); err != nil {
		return err
	}
	if raw, exists := extra["reuse"]; exists {
		if _, ok := raw.(bool); !ok {
			return errors.New("snell reuse must be boolean")
		}
	}
	if _, exists := extra["userkey"]; exists {
		return errors.New("snell userkey is derived from user proxy credentials (managed_field)")
	}
	if _, exists := extra["user_key"]; exists {
		return errors.New("snell userkey is derived from user proxy credentials (managed_field)")
	}
	if psk := strings.TrimSpace(stringValue(extra, "psk", "")); psk != "" {
		if err := validateSnellPSKLength(psk, version); err != nil {
			return err
		}
	}
	return validateTransportOptions(model.ProtocolSnell, extra, side)
}

type snellAdapter struct{}

func (snellAdapter) Protocol() model.Protocol { return model.ProtocolSnell }
func (snellAdapter) ValidateInbound(v model.Inbound) error {
	if err := ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if err := ValidatePort(v.Port); err != nil {
		return err
	}
	return validateSnellOptions(parseExtra(v.ConfigJSON), transportSideInbound)
}
func (snellAdapter) ValidateOutbound(v model.Outbound) error {
	if strings.TrimSpace(v.TargetAddress) == "" {
		return errors.New("target_address required")
	}
	if err := ValidatePort(v.TargetPort); err != nil {
		return err
	}
	return validateSnellOptions(parseExtra(v.ConfigJSON), transportSideOutbound)
}

// Inbound is not how Snell listeners are produced. A Snell inbound expands
// into one single-user listener per identity, each with its own port and PSK,
// which needs the port ledger and therefore lives in planSnellUserListeners.
// Returning an error here keeps any other generated-listener path — proxy path
// transparent processing is the one that can reach it — from silently
// producing a multi-user listener no client can connect to.
func (a snellAdapter) Inbound(v model.Inbound, users []model.User) (map[string]any, error) {
	if err := a.ValidateInbound(v); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("入口 %s 是 Snell：每个用户使用独立端口和独立 PSK，无法在此处生成共享监听；Snell 暂不支持代理链的透明处理节点，请改用 VLESS/HY2 等协议承载该链路", v.Name)
}
func (a snellAdapter) Outbound(v model.Outbound, user *model.User) (map[string]any, error) {
	if err := a.ValidateOutbound(v); err != nil {
		return nil, err
	}
	extra := parseExtra(v.ConfigJSON)
	panelVersion, err := snellPanelVersion(extra)
	if err != nil {
		return nil, err
	}
	clientVersion, err := SnellClientVersion(panelVersion)
	if err != nil {
		return nil, err
	}
	psk, err := snellServerPSK(extra)
	if err != nil {
		if user != nil {
			psk = strings.TrimSpace(user.ProxyPassword)
			if psk != "" {
				if verr := validateSnellPSKLength(psk, panelVersion); verr != nil {
					return nil, verr
				}
			}
		}
		if psk == "" {
			return nil, errors.New("snell psk required")
		}
	}
	if clientVersion == SnellVersionV6 {
		if err := validateSnellPSKLength(psk, SnellVersionV6); err != nil {
			return nil, err
		}
	}
	userkey := ""
	if user != nil {
		userkey = snellUserKey(*user)
	} else {
		userkey = strings.TrimSpace(stringValue(extra, "userkey", ""))
		if userkey == "" {
			userkey = strings.TrimSpace(stringValue(extra, "user_key", ""))
		}
	}
	item := map[string]any{"type": "snell", "tag": tag("out", v.ID), "server": v.TargetAddress, "server_port": v.TargetPort, "version": clientVersion, "psk": psk}
	if userkey != "" {
		item["userkey"] = userkey
	}
	if snellReuse(extra) {
		item["reuse"] = true
	}
	if clientVersion == SnellVersionV4 {
		if obfs, err := normalizeSnellObfsMode(stringValue(extra, "obfs_mode", "none")); err != nil {
			return nil, err
		} else if obfs != "none" {
			item["obfs_mode"] = obfs
			if host := strings.TrimSpace(stringValue(extra, "obfs_host", "")); host != "" {
				item["obfs_host"] = host
			}
		}
	} else {
		if mode, err := normalizeSnellV6Mode(stringValue(extra, "mode", "default")); err != nil {
			return nil, err
		} else if mode != "default" {
			item["mode"] = mode
		}
	}
	applyAllowed(item, extra, "network", "tcp_fast_open", "domain_resolver", "network_strategy", "fallback_delay")
	return item, nil
}

// SubscriptionNode is not how Snell nodes are rendered. Each identity connects
// to its own listener on its own port with its own derived PSK, which requires
// the port ledger, so subscription rendering goes through
// SnellSubscriptionNode instead. Failing loudly here keeps a caller from
// emitting a node pointing at the inbound's declared port, which nothing
// listens on.
func (a snellAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	if err := a.ValidateInbound(inbound); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("入口 %s 是 Snell：订阅节点需要按用户解析端口与 PSK，请使用 SnellSubscriptionNode", inbound.Name)
}

type ssAdapter struct{}

const shadowsocksUoTVersion = 2

func forceShadowsocksUoTVersion(item map[string]any) {
	if !udpOverTCPEnabled(item["udp_over_tcp"]) {
		return
	}
	options, ok := item["udp_over_tcp"].(map[string]any)
	if !ok {
		options = map[string]any{"enabled": true}
	}
	options["version"] = shadowsocksUoTVersion
	item["udp_over_tcp"] = options
}

func (ssAdapter) Protocol() model.Protocol { return model.ProtocolSS }
func (ssAdapter) ValidateInbound(v model.Inbound) error {
	if err := ValidateListenIP(v.ListenIP); err != nil {
		return err
	}
	if err := ValidatePort(v.Port); err != nil {
		return err
	}
	return validateTransportOptions(model.ProtocolSS, parseExtra(v.ConfigJSON), transportSideInbound)
}
func (ssAdapter) ValidateOutbound(v model.Outbound) error {
	if v.TargetAddress == "" {
		return errors.New("target_address required")
	}
	if err := ValidatePort(v.TargetPort); err != nil {
		return err
	}
	extra := parseExtra(v.ConfigJSON)
	if err := validateTransportOptions(model.ProtocolSS, extra, transportSideOutbound); err != nil {
		return err
	}
	return validateShadowsocksUoTMultiplex(extra)
}

// validateShadowsocksUoTMultiplex rejects the documented Shadowsocks conflict:
// with udp_over_tcp enabled sing-box never builds the multiplex dialer, so a
// configuration carrying both would silently lose multiplexing.
func validateShadowsocksUoTMultiplex(extra map[string]any) error {
	if udpOverTCPEnabled(extra["udp_over_tcp"]) && genericMuxEnabled(extra["multiplex"]) {
		return errors.New("shadowsocks udp_over_tcp conflicts with multiplex: enable only one")
	}
	return nil
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
	applyAllowed(item, extra, "network", "multiplex", "managed", "destinations", "tcp_fast_open")
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
	applyAllowed(item, extra, "plugin", "plugin_opts", "network", "udp_over_tcp", "multiplex", "tcp_fast_open", "domain_resolver", "network_strategy", "fallback_delay")
	forceShadowsocksUoTVersion(item)
	return item, nil
}
func (a ssAdapter) SubscriptionNode(user model.User, inbound model.Inbound, server model.Server) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	method := stringValue(extra, "method", "2022-blake3-aes-128-gcm")
	password := user.ProxyPassword
	if serverPassword := stringValue(extra, "password", ""); serverPassword != "" && shadowsocksMethodSupportsUsers(method) {
		password = normalizeSS2022Key(serverPassword, method) + ":" + normalizeSS2022Key(user.ProxyPassword, method)
	}
	node := map[string]any{"type": "shadowsocks", "tag": inbound.Name, "server": server.EntryAddress, "server_port": InboundSubscriptionPort(inbound), "method": method, "password": password}
	applyAllowed(node, extra, "tcp_fast_open")
	if server.UDPInboundMode == model.UDPInboundUoT {
		// UoT wins over multiplexing on the client side, and the inbound is
		// rejected earlier if it tries to combine both.
		node["udp_over_tcp"] = map[string]any{"enabled": true, "version": shadowsocksUoTVersion}
		return node, nil
	}
	applyAllowed(node, extra, "multiplex")
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
