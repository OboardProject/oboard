package core

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// ErrInvalidDesiredState marks a failure the operator can fix by changing the
// stored configuration — a listener conflict, an unreachable address, an
// unsupported protocol field. Callers map it to a client error instead of
// reporting a server fault.
var ErrInvalidDesiredState = errors.New("invalid desired state")

type invalidDesiredStateError struct {
	cause error
}

func (e invalidDesiredStateError) Error() string { return e.cause.Error() }
func (e invalidDesiredStateError) Unwrap() error { return e.cause }
func (e invalidDesiredStateError) Is(target error) bool {
	return target == ErrInvalidDesiredState
}

func markInvalidDesiredState(err error) error {
	if err == nil || errors.Is(err, ErrInvalidDesiredState) {
		return err
	}
	return invalidDesiredStateError{cause: err}
}

// ConfigFieldError identifies the exact JSON path rejected by the Controller.
// It is shared by REST, MCP, automation and reusable-template writes so every
// management surface reports the same actionable location before persistence.
type ConfigFieldError struct {
	Path    string
	Problem string
}

func (e *ConfigFieldError) Error() string {
	return e.Path + ": " + e.Problem
}

func (e *ConfigFieldError) ValidationPath() string { return e.Path }

func (e *ConfigFieldError) Is(target error) bool { return target == ErrInvalidDesiredState }

var inboundRealityFields = map[string]bool{
	"enabled":             true,
	"handshake":           true,
	"private_key":         true,
	"public_key":          true, // Controller-only derived client projection; stripped from the server config.
	"short_id":            true,
	"max_time_difference": true,
}

var inboundRealityHandshakeFields = map[string]bool{
	"server":      true,
	"server_port": true,
}

// ValidateInboundConfigJSON validates the protocol configuration document
// before it is stored. It deliberately owns an OBoard allowlist instead of
// importing sing-box: Controller and Agent remain separate projects, while a
// field that the pinned kernel would reject can never be persisted through a
// panel, REST, MCP or automation write.
func ValidateInboundConfigJSON(protocol model.Protocol, raw string) error {
	var config map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil || config == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return &ConfigFieldError{Path: "config_json", Problem: err.Error()}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &ConfigFieldError{Path: "config_json", Problem: "must contain exactly one JSON object"}
	}
	return ValidateInboundConfigObject(protocol, config)
}

// ValidatePersistedInboundConfigJSON validates the final document after the
// Controller has applied presets and generated credentials. Reusable presets
// intentionally use ValidateInboundConfigObject instead because they never
// contain per-inbound Reality keys.
func ValidatePersistedInboundConfigJSON(protocol model.Protocol, raw string) error {
	if err := ValidateInboundConfigJSON(protocol, raw); err != nil {
		return err
	}
	var config map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		return &ConfigFieldError{Path: "config_json", Problem: err.Error()}
	}
	tls, _ := config["tls"].(map[string]any)
	reality, _ := tls["reality"].(map[string]any)
	if !persistedRealityEnabled(reality) {
		return nil
	}
	if protocol != model.ProtocolVLESS {
		return &ConfigFieldError{Path: "config_json.tls.reality", Problem: fmt.Sprintf("is not supported for %s in OBoard", protocol)}
	}
	if enabled, _ := tls["enabled"].(bool); !enabled {
		return &ConfigFieldError{Path: "config_json.tls.enabled", Problem: "must be true for Reality"}
	}
	if enabled, _ := reality["enabled"].(bool); !enabled {
		return &ConfigFieldError{Path: "config_json.tls.reality.enabled", Problem: "must be true"}
	}
	if strings.TrimSpace(stringFromAny(tls["server_name"])) == "" {
		return &ConfigFieldError{Path: "config_json.tls.server_name", Problem: "is required for Reality"}
	}
	handshake, _ := reality["handshake"].(map[string]any)
	if handshake == nil {
		return &ConfigFieldError{Path: "config_json.tls.reality.handshake", Problem: "is required"}
	}
	if strings.TrimSpace(stringFromAny(handshake["server"])) == "" {
		return &ConfigFieldError{Path: "config_json.tls.reality.handshake.server", Problem: "is required"}
	}
	port, ok := exactJSONInt(handshake["server_port"])
	if !ok || !validPort(port) {
		return &ConfigFieldError{Path: "config_json.tls.reality.handshake.server_port", Problem: "must be an integer between 1 and 65535"}
	}
	if !validRealityPrivateKey(stringFromAny(reality["private_key"])) {
		return &ConfigFieldError{Path: "config_json.tls.reality.private_key", Problem: "must be a 32-byte base64url key"}
	}
	if !validRealityPrivateKey(stringFromAny(reality["public_key"])) {
		return &ConfigFieldError{Path: "config_json.tls.reality.public_key", Problem: "must be a 32-byte base64url key"}
	}
	shortID := strings.TrimSpace(stringFromAny(reality["short_id"]))
	if shortID == "" || !validRealityShortID(shortID) {
		return &ConfigFieldError{Path: "config_json.tls.reality.short_id", Problem: "must be an even-length hexadecimal string between 2 and 16 characters"}
	}
	return nil
}

func persistedRealityEnabled(reality map[string]any) bool {
	if reality == nil {
		return false
	}
	if enabled, _ := reality["enabled"].(bool); enabled {
		return true
	}
	return strings.TrimSpace(stringFromAny(reality["private_key"])) != "" ||
		strings.TrimSpace(stringFromAny(reality["public_key"])) != "" ||
		reality["handshake"] != nil
}

// ValidateInboundConfigObject is the object form used by node-preset
// normalization after its defaults and operator overrides have been merged.
func ValidateInboundConfigObject(protocol model.Protocol, config map[string]any) error {
	tlsValue, exists := config["tls"]
	if !exists || tlsValue == nil {
		return nil
	}
	tls, ok := tlsValue.(map[string]any)
	if !ok {
		return &ConfigFieldError{Path: "config_json.tls", Problem: "must be an object"}
	}
	realityValue, exists := tls["reality"]
	if !exists || realityValue == nil {
		return nil
	}
	if protocol != model.ProtocolVLESS {
		return &ConfigFieldError{Path: "config_json.tls.reality", Problem: fmt.Sprintf("is not supported for %s in OBoard", protocol)}
	}
	reality, ok := realityValue.(map[string]any)
	if !ok {
		return &ConfigFieldError{Path: "config_json.tls.reality", Problem: "must be an object"}
	}
	if field := firstUnsupportedField(reality, inboundRealityFields); field != "" {
		return &ConfigFieldError{Path: "config_json.tls.reality." + field, Problem: "unsupported field; use handshake.server and handshake.server_port for the Reality fallback target"}
	}
	if value, exists := reality["enabled"]; exists && value != nil {
		if _, ok := value.(bool); !ok {
			return &ConfigFieldError{Path: "config_json.tls.reality.enabled", Problem: "must be boolean"}
		}
	}
	for _, field := range []string{"private_key", "public_key", "short_id", "max_time_difference"} {
		if value, exists := reality[field]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return &ConfigFieldError{Path: "config_json.tls.reality." + field, Problem: "must be a string"}
			}
		}
	}
	handshakeValue, exists := reality["handshake"]
	if !exists || handshakeValue == nil {
		return nil
	}
	handshake, ok := handshakeValue.(map[string]any)
	if !ok {
		return &ConfigFieldError{Path: "config_json.tls.reality.handshake", Problem: "must be an object"}
	}
	if field := firstUnsupportedField(handshake, inboundRealityHandshakeFields); field != "" {
		return &ConfigFieldError{Path: "config_json.tls.reality.handshake." + field, Problem: "unsupported field"}
	}
	if value, exists := handshake["server"]; exists && value != nil {
		if _, ok := value.(string); !ok {
			return &ConfigFieldError{Path: "config_json.tls.reality.handshake.server", Problem: "must be a string"}
		}
	}
	if value, exists := handshake["server_port"]; exists && value != nil {
		port, ok := exactJSONInt(value)
		if !ok || !validPort(port) {
			return &ConfigFieldError{Path: "config_json.tls.reality.handshake.server_port", Problem: "must be an integer between 1 and 65535"}
		}
	}
	return nil
}

func firstUnsupportedField(object map[string]any, allowed map[string]bool) string {
	fields := make([]string, 0)
	for field := range object {
		if !allowed[field] {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func ValidateGeneratedSingBoxConfig(config SingBoxConfig) error {
	v := &configValidator{
		tags:        map[string]string{},
		outboundTag: map[string]bool{},
		dnsTag:      map[string]bool{},
		routeTarget: map[string]bool{},
		listens:     []listenResource{},
	}
	v.validateDNS(config.DNS)
	v.validateOutbounds(config.Outbounds)
	v.validateEndpoints(config.Endpoints)
	v.validateInbounds(config.Inbounds)
	v.validateRoute(config.Route)
	if len(v.issues) > 0 {
		return fmt.Errorf("%w: generated sing-box config invalid: %s", ErrInvalidDesiredState, strings.Join(v.issues, "; "))
	}
	return nil
}

type configValidator struct {
	issues      []string
	tags        map[string]string
	outboundTag map[string]bool
	dnsTag      map[string]bool
	routeTarget map[string]bool
	listens     []listenResource
}

func (v *configValidator) addf(format string, args ...any) {
	v.issues = append(v.issues, fmt.Sprintf(format, args...))
}

func (v *configValidator) addTag(kind, tag string) {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		v.addf("%s missing tag", kind)
		return
	}
	if prev := v.tags[tag]; prev != "" {
		v.addf("duplicate tag %q used by %s and %s", tag, prev, kind)
		return
	}
	v.tags[tag] = kind
}

func (v *configValidator) validateDNS(dns map[string]any) {
	if dns == nil {
		v.addf("dns block missing")
		return
	}
	servers := mapList(dns["servers"])
	if len(servers) == 0 {
		v.addf("dns.servers missing")
	}
	for i, server := range servers {
		path := fmt.Sprintf("dns.servers[%d]", i)
		tag := stringFromAny(server["tag"])
		v.addTag(path, tag)
		if tag != "" {
			v.dnsTag[tag] = true
		}
		if stringFromAny(server["type"]) == "" {
			v.addf("%s missing type", path)
		}
		if detour := stringFromAny(server["detour"]); detour == "direct" {
			v.addf("%s detour=direct is invalid for OBoard's empty direct outbound", path)
		}
		if resolver := stringFromAny(server["domain_resolver"]); resolver != "" && !v.dnsTag[resolver] {
			// Defer cross-reference until all DNS tags are known.
			continue
		}
		if port := intFromAny(server["server_port"]); port != 0 && !validPort(port) {
			v.addf("%s invalid server_port %d", path, port)
		}
	}
	if final := stringFromAny(dns["final"]); final != "" && !v.dnsTag[final] {
		v.addf("dns.final references unknown dns server %q", final)
	}
	for i, server := range servers {
		if resolver := stringFromAny(server["domain_resolver"]); resolver != "" && !v.dnsTag[resolver] {
			v.addf("dns.servers[%d].domain_resolver references unknown dns server %q", i, resolver)
		}
	}
	for i, rule := range mapList(dns["rules"]) {
		if server := stringFromAny(rule["server"]); server != "" && !v.dnsTag[server] {
			v.addf("dns.rules[%d].server references unknown dns server %q", i, server)
		}
	}
}

func (v *configValidator) validateOutbounds(outbounds []map[string]any) {
	if len(outbounds) == 0 {
		v.addf("outbounds missing")
		return
	}
	for i, outbound := range outbounds {
		path := fmt.Sprintf("outbounds[%d]", i)
		tag := stringFromAny(outbound["tag"])
		v.addTag(path, tag)
		if tag != "" {
			v.outboundTag[tag] = true
			v.routeTarget[tag] = true
		}
		typ := stringFromAny(outbound["type"])
		if typ == "" {
			v.addf("%s missing type", path)
			continue
		}
		switch typ {
		case "direct", "block":
		case "source-prefix":
			prefix, err := netip.ParsePrefix(strings.TrimSpace(stringFromAny(outbound["prefix"])))
			if err != nil || !prefix.IsValid() {
				v.addf("%s missing prefix", path)
			}
		case "family-selector":
			ipv4Tag := strings.TrimSpace(stringFromAny(outbound["ipv4_outbound"]))
			ipv6Tag := strings.TrimSpace(stringFromAny(outbound["ipv6_outbound"]))
			if ipv4Tag == "" || ipv6Tag == "" || ipv4Tag == ipv6Tag {
				v.addf("%s requires distinct ipv4_outbound and ipv6_outbound", path)
			}
			strategy := strings.TrimSpace(stringFromAny(outbound["strategy"]))
			if strategy != "prefer_ipv4" && strategy != "prefer_ipv6" {
				v.addf("%s has unsupported strategy %q", path, strategy)
			}
			if fallback, ok := outbound["fallback"].(bool); !ok || !fallback {
				v.addf("%s must enable bounded family fallback", path)
			}
		case "vless", "hysteria2", "anytls", "shadowsocks", "mieru", "snell", "socks":
			v.validateRemoteAdapter(path, typ, outbound)
		default:
			v.addf("%s unsupported outbound type %q", path, typ)
		}
		v.validateDomainResolver(path, outbound["domain_resolver"])
	}
	for i, outbound := range outbounds {
		if detour := stringFromAny(outbound["detour"]); detour != "" && !v.outboundTag[detour] {
			v.addf("outbounds[%d].detour references unknown outbound %q", i, detour)
		}
		if stringFromAny(outbound["type"]) == "family-selector" {
			for _, field := range []string{"ipv4_outbound", "ipv6_outbound"} {
				target := strings.TrimSpace(stringFromAny(outbound[field]))
				if target != "" && !v.outboundTag[target] {
					v.addf("outbounds[%d].%s references unknown outbound %q", i, field, target)
				}
			}
		}
	}
}

func (v *configValidator) validateEndpoints(endpoints []map[string]any) {
	for i, endpoint := range endpoints {
		path := fmt.Sprintf("endpoints[%d]", i)
		tag := stringFromAny(endpoint["tag"])
		v.addTag(path, tag)
		if tag != "" {
			v.routeTarget[tag] = true
		}
		if stringFromAny(endpoint["type"]) == "" {
			v.addf("%s missing type", path)
		}
		if detour := stringFromAny(endpoint["detour"]); detour != "" && !v.outboundTag[detour] {
			v.addf("%s.detour references unknown outbound %q", path, detour)
		}
		v.validateDomainResolver(path, endpoint["domain_resolver"])
	}
}

func (v *configValidator) validateDomainResolver(path string, value any) {
	if value == nil {
		return
	}
	var tag string
	switch resolver := value.(type) {
	case string:
		tag = strings.TrimSpace(resolver)
	case map[string]any:
		tag = strings.TrimSpace(stringFromAny(resolver["server"]))
	default:
		v.addf("%s.domain_resolver has invalid shape", path)
		return
	}
	if tag == "" {
		v.addf("%s.domain_resolver missing server", path)
	} else if !v.dnsTag[tag] {
		v.addf("%s.domain_resolver references unknown dns server %q", path, tag)
	}
}

func (v *configValidator) validateInbounds(inbounds []map[string]any) {
	for i, inbound := range inbounds {
		path := fmt.Sprintf("inbounds[%d]", i)
		tag := stringFromAny(inbound["tag"])
		v.addTag(path, tag)
		typ := stringFromAny(inbound["type"])
		if typ == "" {
			v.addf("%s missing type", path)
			continue
		}
		listen := strings.TrimSpace(stringFromAny(inbound["listen"]))
		if listen == "" {
			listen = "0.0.0.0"
		}
		if err := ValidateListenIP(listen); err != nil {
			v.addf("%s invalid listen ip: %v", path, err)
		}
		ports := []int{intFromAny(inbound["listen_port"])}
		if typ == "mieru" {
			var err error
			ports, err = mieruPortsFromValue(ports[0], inbound["listen_ports"])
			if err != nil {
				v.addf("%s invalid mieru listen ports: %v", path, err)
				ports = nil
			}
		}
		for _, port := range ports {
			if !validPort(port) {
				v.addf("%s invalid listen_port %d", path, port)
				continue
			}
			resource := listenResource{address: listen, port: port, protocol: singBoxInboundListenTransport(inbound), owner: path}
			for _, previous := range v.listens {
				if resource.conflicts(previous) {
					v.addf("%s listen resource %s:%d (%s) conflicts with %s", path, listen, port, listenTransportName(resource.protocol&previous.protocol), previous.owner)
				}
			}
			v.listens = append(v.listens, resource)
		}
		switch typ {
		case "vless":
			v.validateVLESSInbound(path, inbound)
		case "hysteria2", "anytls":
			v.validatePasswordUserInbound(path, typ, inbound)
		case "shadowsocks":
			v.validateShadowsocksInbound(path, inbound)
		case "mieru":
			v.validateMieruInbound(path, inbound)
		case "snell":
			v.validateSnellInbound(path, inbound)
		case "socks":
			v.validateSocksInbound(path, inbound)
		default:
			v.addf("%s unsupported inbound type %q", path, typ)
		}
	}
}

func (v *configValidator) validateSocksInbound(path string, inbound map[string]any) {
	users := mapList(inbound["users"])
	if len(users) == 0 {
		v.addf("%s socks users missing", path)
	}
	seen := map[string]bool{}
	for i, user := range users {
		userPath := fmt.Sprintf("%s.users[%d]", path, i)
		username := strings.TrimSpace(stringFromAny(user["username"]))
		if username == "" {
			v.addf("%s missing username", userPath)
		} else if seen[username] {
			v.addf("%s duplicate username %q", userPath, username)
		}
		seen[username] = true
		if stringFromAny(user["password"]) == "" {
			v.addf("%s missing password", userPath)
		}
	}
}

func (v *configValidator) validateRoute(route map[string]any) {
	if route == nil {
		return
	}
	if final := stringFromAny(route["final"]); final != "" && !v.routeTarget[final] {
		v.addf("route.final references unknown outbound/endpoint %q", final)
	}
	if resolver, ok := route["default_domain_resolver"].(map[string]any); ok {
		if tag := stringFromAny(resolver["server"]); tag != "" && !v.dnsTag[tag] {
			v.addf("route.default_domain_resolver references unknown dns server %q", tag)
		}
	}
	for i, rule := range mapList(route["rules"]) {
		if action := stringFromAny(rule["action"]); action != "" && action != "route" {
			continue
		}
		if outbound := stringFromAny(rule["outbound"]); outbound != "" && !v.routeTarget[outbound] {
			v.addf("route.rules[%d].outbound references unknown outbound/endpoint %q", i, outbound)
		}
		if resolver := stringFromAny(rule["domain_resolver"]); resolver != "" && !v.dnsTag[resolver] {
			v.addf("route.rules[%d].domain_resolver references unknown dns server %q", i, resolver)
		}
	}
}

func (v *configValidator) validateRemoteAdapter(path, typ string, item map[string]any) {
	if server := strings.TrimSpace(stringFromAny(item["server"])); server == "" {
		v.addf("%s missing server", path)
	}
	port := intFromAny(item["server_port"])
	if !validPort(port) {
		v.addf("%s invalid server_port %d", path, port)
	}
	if typ == "mieru" {
		if _, err := mieruPortsFromValue(port, item["server_ports"]); err != nil {
			v.addf("%s invalid mieru server ports: %v", path, err)
		}
	}
	switch typ {
	case "vless":
		if strings.TrimSpace(stringFromAny(item["uuid"])) == "" {
			v.addf("%s missing uuid", path)
		}
		v.validateVLESSFlowAndTransport(path, item, false)
	case "hysteria2", "anytls":
		if strings.TrimSpace(stringFromAny(item["password"])) == "" {
			v.addf("%s missing password", path)
		}
		if typ == "anytls" && boolValue(item["tcp_fast_open"]) {
			v.addf("%s tcp_fast_open is not supported with anytls outbound", path)
		}
	case "shadowsocks":
		method := stringFromAny(item["method"])
		if method == "" {
			v.addf("%s missing method", path)
		}
		if strings.TrimSpace(stringFromAny(item["password"])) == "" {
			v.addf("%s missing password", path)
		}
	case "mieru":
		username := stringFromAny(item["username"])
		if strings.TrimSpace(username) == "" {
			v.addf("%s missing username", path)
		} else if len([]byte(username)) > 64 {
			v.addf("%s username exceeds 64 bytes", path)
		}
		password := stringFromAny(item["password"])
		if strings.TrimSpace(password) == "" {
			v.addf("%s missing password", path)
		} else if len([]byte(password)) > 64 {
			v.addf("%s password exceeds 64 bytes", path)
		}
		v.validateMieruTransport(path, item)
	case "snell":
		psk := stringFromAny(item["psk"])
		if strings.TrimSpace(psk) == "" {
			v.addf("%s missing psk", path)
		} else {
			version := intFromAny(item["version"])
			if version == 6 {
				if len([]byte(psk)) < 12 || len([]byte(psk)) > 255 {
					v.addf("%s snell v6 psk must be between 12 and 255 bytes", path)
				}
			} else if len(psk) < 8 {
				v.addf("%s psk too short", path)
			}
		}
		v.validateSnellVersionFields(path, item, false)
	case "socks":
		// Username/password are optional for unauthenticated third-party SOCKS5.
	}
}

func (v *configValidator) validateSnellVersionFields(path string, item map[string]any, inbound bool) {
	version := intFromAny(item["version"])
	switch version {
	case 4, 5, 6:
	default:
		v.addf("%s missing or unsupported snell version", path)
		return
	}
	if version == 6 {
		if obfs := strings.TrimSpace(stringFromAny(item["obfs_mode"])); obfs != "" {
			v.addf("%s snell v6 must not carry obfs_mode", path)
		}
		if mode := strings.ToLower(strings.TrimSpace(stringFromAny(item["mode"]))); mode != "" {
			switch mode {
			case "default", "unshaped", "unsafe-raw":
			default:
				v.addf("%s invalid snell v6 mode %q", path, mode)
			}
		}
		return
	}
	// v4 (outbound) and v5 (inbound) support http obfs only; tls obfs is not
	// exposed by OBoard, matching the sing-box 1.14 and Surge documentation.
	if obfs := strings.ToLower(strings.TrimSpace(stringFromAny(item["obfs_mode"]))); obfs != "" {
		switch obfs {
		case "none", "http":
		default:
			v.addf("%s invalid snell obfs_mode %q (only none or http)", path, obfs)
		}
	}
}

func (v *configValidator) validateSnellInbound(path string, inbound map[string]any) {
	psk := stringFromAny(inbound["psk"])
	if strings.TrimSpace(psk) == "" {
		v.addf("%s missing psk", path)
	} else {
		version := intFromAny(inbound["version"])
		if version == 6 {
			if len([]byte(psk)) < 12 || len([]byte(psk)) > 255 {
				v.addf("%s snell v6 psk must be between 12 and 255 bytes", path)
			}
		} else if len(psk) < 8 {
			v.addf("%s psk too short", path)
		}
	}
	v.validateSnellVersionFields(path, inbound, true)
}

func (v *configValidator) validateMieruInbound(path string, inbound map[string]any) {
	users := mapList(inbound["users"])
	if len(users) == 0 {
		v.addf("%s mieru users missing", path)
	}
	seen := map[string]bool{}
	for i, user := range users {
		userPath := fmt.Sprintf("%s.users[%d]", path, i)
		name := strings.TrimSpace(stringFromAny(user["name"]))
		if name == "" {
			v.addf("%s missing name", userPath)
		} else if len([]byte(name)) > 64 {
			v.addf("%s name exceeds 64 bytes", userPath)
		} else if seen[name] {
			v.addf("%s duplicate name %q", userPath, name)
		}
		seen[name] = true
		if strings.TrimSpace(stringFromAny(user["password"])) == "" {
			v.addf("%s missing password", userPath)
		} else if len([]byte(stringFromAny(user["password"]))) > 64 {
			v.addf("%s password exceeds 64 bytes", userPath)
		}
	}
	v.validateMieruTransport(path, inbound)
}

func (v *configValidator) validateMieruTransport(path string, item map[string]any) {
	switch strings.ToUpper(strings.TrimSpace(stringFromAny(item["transport"]))) {
	case "TCP", "UDP":
	default:
		v.addf("%s mieru transport must be TCP or UDP", path)
	}
}

func (v *configValidator) validateVLESSInbound(path string, inbound map[string]any) {
	users := mapList(inbound["users"])
	if len(users) == 0 {
		v.addf("%s vless users missing", path)
	}
	for i, user := range users {
		userPath := fmt.Sprintf("%s.users[%d]", path, i)
		if strings.TrimSpace(stringFromAny(user["name"])) == "" {
			v.addf("%s missing name", userPath)
		}
		if !validUUID(stringFromAny(user["uuid"])) {
			v.addf("%s invalid uuid", userPath)
		}
		if flow := stringFromAny(user["flow"]); flow != "" && flow != "xtls-rprx-vision" {
			v.addf("%s unsupported flow %q", userPath, flow)
		}
	}
	v.validateVLESSFlowAndTransport(path, inbound, true)
}

func (v *configValidator) validateVLESSFlowAndTransport(path string, item map[string]any, inbound bool) {
	flow := stringFromAny(item["flow"])
	for _, user := range mapList(item["users"]) {
		if userFlow := stringFromAny(user["flow"]); userFlow != "" {
			flow = userFlow
			break
		}
	}
	tlsEnabled, realityEnabled := v.validateAdapterTLS(path, "vless", item, inbound)
	transportType := transportType(item)
	if flow == "xtls-rprx-vision" {
		if !tlsEnabled {
			v.addf("%s Vision flow requires TLS or Reality", path)
		}
		if transportType != "" && transportType != "tcp" {
			v.addf("%s Vision flow requires TCP transport, got %q", path, transportType)
		}
	}
	if realityEnabled && transportType != "" && transportType != "tcp" {
		v.addf("%s Reality requires TCP transport, got %q", path, transportType)
	}
	if transportType == "ws" && strings.TrimSpace(stringFromAny(mapValue(item["transport"])["path"])) == "" {
		v.addf("%s WebSocket transport missing path", path)
	}
}

func (v *configValidator) validatePasswordUserInbound(path, typ string, inbound map[string]any) {
	users := mapList(inbound["users"])
	if len(users) == 0 {
		v.addf("%s %s users missing", path, typ)
	}
	for i, user := range users {
		userPath := fmt.Sprintf("%s.users[%d]", path, i)
		if strings.TrimSpace(stringFromAny(user["name"])) == "" {
			v.addf("%s missing name", userPath)
		}
		if strings.TrimSpace(stringFromAny(user["password"])) == "" {
			v.addf("%s missing password", userPath)
		}
	}
	v.validateAdapterTLS(path, typ, inbound, true)
}

func (v *configValidator) validateShadowsocksInbound(path string, inbound map[string]any) {
	if _, exists := inbound["udp_over_tcp"]; exists {
		v.addf("%s udp_over_tcp is outbound-only", path)
	}
	method := stringFromAny(inbound["method"])
	if !supportedShadowsocksMethod(method) {
		v.addf("%s unsupported shadowsocks method %q", path, method)
		return
	}
	password := stringFromAny(inbound["password"])
	if strings.TrimSpace(password) == "" {
		v.addf("%s missing password", path)
	}
	if shadowsocksMethodSupportsUsers(method) {
		if !validSS2022Key(password, method) {
			v.addf("%s invalid SS2022 server key length for %s", path, method)
		}
		users := mapList(inbound["users"])
		if len(users) == 0 {
			v.addf("%s SS2022 users missing", path)
		}
		for i, user := range users {
			if !validSS2022Key(stringFromAny(user["password"]), method) {
				v.addf("%s.users[%d] invalid SS2022 user key length for %s", path, i, method)
			}
		}
	} else if len(mapList(inbound["users"])) > 0 {
		v.addf("%s single-password SS must not include users list", path)
	}
}

func (v *configValidator) validateAdapterTLS(path, typ string, item map[string]any, inbound bool) (bool, bool) {
	tls := mapValue(item["tls"])
	if tls == nil {
		return false, false
	}
	enabled, _ := tls["enabled"].(bool)
	if !enabled {
		return false, false
	}
	reality := mapValue(tls["reality"])
	if rawReality, exists := tls["reality"]; exists && rawReality != nil {
		if reality == nil {
			v.addf("%s.tls.reality must be an object", path)
		} else {
			v.validateRealityFieldShape(path+".tls.reality", reality)
		}
	}
	realityEnabled := reality != nil && (boolValue(reality["enabled"]) || stringFromAny(reality["private_key"]) != "" || mapValue(reality["handshake"]) != nil)
	if !inbound {
		realityEnabled = reality != nil && (boolValue(reality["enabled"]) || stringFromAny(reality["public_key"]) != "")
		if realityEnabled {
			if typ != "vless" {
				v.addf("%s Reality is only exposed for VLESS in OBoard", path)
			}
			if stringFromAny(tls["server_name"]) == "" {
				v.addf("%s Reality TLS missing server_name/SNI", path)
			}
			if stringFromAny(reality["private_key"]) != "" || mapValue(reality["handshake"]) != nil {
				v.addf("%s Reality client config must not include private_key or handshake", path)
			}
			if strings.TrimSpace(stringFromAny(reality["public_key"])) == "" {
				v.addf("%s Reality public_key missing", path)
			}
			if !validRealityShortID(stringFromAny(reality["short_id"])) {
				v.addf("%s Reality short_id must be an even-length hex string up to 16 chars", path)
			}
		}
		return true, realityEnabled
	}
	if realityEnabled {
		if typ != "vless" {
			v.addf("%s Reality is only exposed for VLESS in OBoard", path)
		}
		if stringFromAny(tls["server_name"]) == "" {
			v.addf("%s Reality TLS missing server_name/SNI", path)
		}
		if stringFromAny(reality["public_key"]) != "" {
			v.addf("%s Reality server config must not include public_key", path)
		}
		if !validRealityPrivateKey(stringFromAny(reality["private_key"])) {
			v.addf("%s Reality private_key must be a 32-byte base64url key", path)
		}
		if !validRealityShortID(stringFromAny(reality["short_id"])) {
			v.addf("%s Reality short_id must be an even-length hex string up to 16 chars", path)
		}
		handshake := mapValue(reality["handshake"])
		if strings.TrimSpace(stringFromAny(handshake["server"])) == "" {
			v.addf("%s Reality handshake.server missing", path)
		}
		if port := intFromAny(handshake["server_port"]); !validPort(port) {
			v.addf("%s Reality handshake.server_port invalid", path)
		}
		if ech := mapValue(tls["ech"]); boolValue(ech["enabled"]) {
			v.addf("%s Reality conflicts with ECH", path)
		}
		return true, true
	}
	if !hasServerTLSKeyMaterial(tls) {
		v.addf("%s TLS enabled but certificate/key or ACME is missing", path)
	}
	return true, false
}

func (v *configValidator) validateRealityFieldShape(path string, reality map[string]any) {
	fields := make([]string, 0, len(reality))
	for field := range reality {
		if !inboundRealityFields[field] {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	for _, field := range fields {
		v.addf("%s.%s unsupported field; use %s.handshake.server and %s.handshake.server_port for the Reality fallback target", path, field, path, path)
	}
	rawHandshake, exists := reality["handshake"]
	if !exists || rawHandshake == nil {
		return
	}
	handshake := mapValue(rawHandshake)
	if handshake == nil {
		v.addf("%s.handshake must be an object", path)
		return
	}
	fields = fields[:0]
	for field := range handshake {
		if !inboundRealityHandshakeFields[field] {
			fields = append(fields, field)
		}
	}
	sort.Strings(fields)
	for _, field := range fields {
		v.addf("%s.handshake.%s unsupported field", path, field)
	}
}

func hasServerTLSKeyMaterial(tls map[string]any) bool {
	if strings.TrimSpace(stringFromAny(tls["certificate_path"])) != "" && strings.TrimSpace(stringFromAny(tls["key_path"])) != "" {
		return true
	}
	if strings.TrimSpace(stringFromAny(tls["certificate"])) != "" && strings.TrimSpace(stringFromAny(tls["key"])) != "" {
		return true
	}
	if acme := mapValue(tls["acme"]); acme != nil {
		if strings.TrimSpace(stringFromAny(acme["domain"])) != "" {
			return true
		}
		if len(stringList(acme["domain"])) > 0 {
			return true
		}
	}
	return false
}

func mapList(value any) []map[string]any {
	switch items := value.(type) {
	case []map[string]any:
		return items
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func mapValue(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func stringList(value any) []string {
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if s := strings.TrimSpace(stringFromAny(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}

func validUUID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return false
	}
	for i, ch := range value {
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
	}
	return true
}

func supportedShadowsocksMethod(method string) bool {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305", "2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return true
	default:
		return false
	}
}

func validSS2022Key(value, method string) bool {
	decoded, ok := decodeSS2022Key(value, ss2022KeyLength(method))
	return ok && len(decoded) == ss2022KeyLength(method)
}

func validRealityPrivateKey(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(value)
	}
	return err == nil && len(decoded) == 32
}

func validRealityShortID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if len(value) > 16 || len(value)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func transportType(item map[string]any) string {
	transport := mapValue(item["transport"])
	if transport == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(stringFromAny(transport["type"])))
}
