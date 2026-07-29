package core

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidDesiredState marks a failure the operator can fix by changing the
// stored configuration — a listener conflict, an unreachable address, an
// unsupported protocol field. Callers map it to a client error instead of
// reporting a server fault.
var ErrInvalidDesiredState = errors.New("invalid desired state")

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
		case "vless", "hysteria2", "anytls", "shadowsocks", "socks":
			v.validateRemoteAdapter(path, typ, outbound)
		default:
			v.addf("%s unsupported outbound type %q", path, typ)
		}
	}
	for i, outbound := range outbounds {
		if detour := stringFromAny(outbound["detour"]); detour != "" && !v.outboundTag[detour] {
			v.addf("outbounds[%d].detour references unknown outbound %q", i, detour)
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
		port := intFromAny(inbound["listen_port"])
		if !validPort(port) {
			v.addf("%s invalid listen_port %d", path, port)
		}
		resource := listenResource{address: listen, port: port, protocol: singBoxInboundListenTransport(inbound), owner: path}
		for _, previous := range v.listens {
			if resource.conflicts(previous) {
				v.addf("%s listen resource %s:%d (%s) conflicts with %s", path, listen, port, listenTransportName(resource.protocol&previous.protocol), previous.owner)
			}
		}
		v.listens = append(v.listens, resource)
		switch typ {
		case "vless":
			v.validateVLESSInbound(path, inbound)
		case "hysteria2", "anytls":
			v.validatePasswordUserInbound(path, typ, inbound)
		case "shadowsocks":
			v.validateShadowsocksInbound(path, inbound)
		default:
			v.addf("%s unsupported inbound type %q", path, typ)
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
	}
}

func (v *configValidator) validateRemoteAdapter(path, typ string, item map[string]any) {
	if server := strings.TrimSpace(stringFromAny(item["server"])); server == "" {
		v.addf("%s missing server", path)
	}
	if port := intFromAny(item["server_port"]); !validPort(port) {
		v.addf("%s invalid server_port %d", path, port)
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
	case "shadowsocks":
		method := stringFromAny(item["method"])
		if method == "" {
			v.addf("%s missing method", path)
		}
		if strings.TrimSpace(stringFromAny(item["password"])) == "" {
			v.addf("%s missing password", path)
		}
	case "socks":
		// Username/password are optional for unauthenticated third-party SOCKS5.
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
