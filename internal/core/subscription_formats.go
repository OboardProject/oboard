package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"go.yaml.in/yaml/v3"
)

// subscriptionProxy is the target-neutral representation used by the local
// subscription converter. Native keeps the allowlisted sing-box fields; all
// other renderers read only the normalized fields below.
type subscriptionProxy struct {
	Name           string
	Group          string
	Type           string
	Server         string
	Port           int
	UUID           string
	AlterID        int
	Security       string
	Username       string
	Password       string
	HostKeys       []string
	Method         string
	Flow           string
	PacketEncoding string
	Network        string
	Transport      subscriptionTransport
	TLS            subscriptionTLS
	ServerPorts    []string
	HopInterval    string
	HopIntervalMax string
	UpMbps         int
	DownMbps       int
	ObfsType       string
	ObfsPassword   string
	UoT            bool
	Version        int
	PSK            string
	ObfsHost       string
	Mode           string
	Reuse          bool
	Multiplexing   string
	TrafficPattern string
	TCPFastOpen    bool
	Native         map[string]any
}

type subscriptionTransport struct {
	Type        string
	Path        string
	Host        string
	ServiceName string
}

type subscriptionTLS struct {
	Present          bool
	Enabled          bool
	ServerName       string
	Insecure         bool
	ALPN             []string
	Fingerprint      string
	RealityPublicKey string
	RealityShortID   string
}

func renderSubscriptionTarget(nodes []SubscriptionNode, format model.SubscriptionFormat) (string, error) {
	return renderSubscriptionTargetWithOptions(nodes, format, SubscriptionRenderOptions{})
}

func normalizeSubscriptionNodes(nodes []SubscriptionNode) ([]subscriptionProxy, error) {
	out := make([]subscriptionProxy, 0, len(nodes))
	for _, node := range nodes {
		proxy, err := normalizeSubscriptionNode(node)
		if err != nil {
			return nil, err
		}
		out = append(out, proxy)
	}
	return out, nil
}

func normalizeSubscriptionNode(node SubscriptionNode) (subscriptionProxy, error) {
	raw := node.Raw
	proxy := subscriptionProxy{
		Name:           node.Name,
		Group:          node.Group,
		Type:           normalizeSubscriptionProxyType(stringFromAny(raw["type"])),
		Server:         strings.TrimSpace(stringFromAny(raw["server"])),
		Port:           intFromAny(raw["server_port"]),
		UUID:           stringFromAny(raw["uuid"]),
		AlterID:        intFromAny(raw["alter_id"]),
		Security:       stringFromAny(raw["security"]),
		Username:       stringFromAny(raw["username"]),
		Password:       stringFromAny(raw["password"]),
		HostKeys:       stringListFromAny(raw["host_key"]),
		Method:         stringFromAny(raw["method"]),
		Flow:           stringFromAny(raw["flow"]),
		PacketEncoding: stringFromAny(raw["packet_encoding"]),
		Network:        strings.ToLower(strings.TrimSpace(stringFromAny(raw["network"]))),
		ServerPorts:    stringListFromAny(raw["server_ports"]),
		HopInterval:    scalarString(raw["hop_interval"]),
		HopIntervalMax: scalarString(raw["hop_interval_max"]),
		UpMbps:         intFromAny(raw["up_mbps"]),
		DownMbps:       intFromAny(raw["down_mbps"]),
		UoT:            udpOverTCPEnabled(raw["udp_over_tcp"]),
		Multiplexing:   stringFromAny(raw["multiplexing"]),
		TrafficPattern: stringFromAny(raw["traffic_pattern"]),
		TCPFastOpen:    boolValue(raw["tcp_fast_open"]),
	}
	if proxy.Name == "" {
		proxy.Name = stringFromAny(raw["tag"])
	}
	if proxy.Username == "" {
		proxy.Username = stringFromAny(raw["user"])
	}
	if proxy.Group == "" {
		proxy.Group = "default"
	}
	if proxy.Server == "" || proxy.Port <= 0 || proxy.Port > 65535 {
		return subscriptionProxy{}, fmt.Errorf("subscription node %s missing valid server/server_port", proxy.Name)
	}
	proxy.Transport = normalizeSubscriptionTransport(raw)
	if proxy.Type == "mieru" {
		proxy.Network = strings.ToLower(normalizeMieruTransport(stringFromAny(raw["transport"])))
	}
	if proxy.Network == "" {
		proxy.Network = proxy.Transport.Type
	}
	if proxy.Network == "" && (proxy.Type == "vless" || proxy.Type == "anytls") {
		proxy.Network = "tcp"
	}
	proxy.TLS = normalizeSubscriptionTLS(raw["tls"], proxy.Type)
	proxy.ObfsType, proxy.ObfsPassword = normalizeSubscriptionObfs(raw)
	proxy.ObfsType = strings.ToLower(proxy.ObfsType)
	proxy.Version = intFromAny(raw["version"])
	proxy.PSK = stringFromAny(raw["psk"])
	proxy.ObfsHost = stringFromAny(raw["obfs_host"])
	proxy.Mode = strings.ToLower(strings.TrimSpace(stringFromAny(raw["mode"])))
	proxy.Reuse = boolValue(raw["reuse"])
	if err := validateNormalizedSubscriptionProxy(proxy); err != nil {
		return subscriptionProxy{}, err
	}
	proxy.Native = sanitizeSingBoxSubscriptionOutbound(raw, proxy)
	return proxy, nil
}

func normalizeSubscriptionProxyType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "vless":
		return "vless"
	case "vmess":
		return "vmess"
	case "trojan":
		return "trojan"
	case "tuic":
		return "tuic"
	case "hysteria2", "hy2":
		return "hysteria2"
	case "anytls":
		return "anytls"
	case "shadowsocks", "ss":
		return "ss"
	case "socks", "socks5":
		return "socks5"
	case "ssh":
		return "ssh"
	case "mieru":
		return "mieru"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func validateNormalizedSubscriptionProxy(proxy subscriptionProxy) error {
	missing := func(field string) error {
		return fmt.Errorf("subscription node %s missing %s", proxy.Name, field)
	}
	switch proxy.Type {
	case "vless", "vmess":
		if proxy.UUID == "" {
			return missing("uuid")
		}
	case "trojan", "hysteria2", "anytls":
		if proxy.Password == "" {
			return missing("password")
		}
	case "tuic":
		if proxy.UUID == "" || proxy.Password == "" {
			return missing("tuic uuid/password")
		}
	case "ss":
		if proxy.Method == "" || proxy.Password == "" {
			return missing("shadowsocks method/password")
		}
	case "socks5":
		return nil
	case "ssh":
		if proxy.Username == "" || proxy.Password == "" {
			return missing("ssh username/password")
		}
	case "mieru":
		if proxy.Username == "" || proxy.Password == "" {
			return missing("mieru username/password")
		}
		if _, err := mieruPortsFromValue(proxy.Port, proxy.ServerPorts); err != nil {
			return fmt.Errorf("subscription node %s has invalid mieru ports: %w", proxy.Name, err)
		}
	case "snell":
		if proxy.PSK == "" {
			return missing("snell psk")
		}
		if proxy.Version != SnellVersionV4 && proxy.Version != SnellVersionV6 {
			return fmt.Errorf("subscription node %s has unsupported snell version %d", proxy.Name, proxy.Version)
		}
		if proxy.Version == SnellVersionV6 && proxy.ObfsType != "" {
			return fmt.Errorf("subscription node %s carries obfs on snell v6, which does not support obfs", proxy.Name)
		}
	default:
		return fmt.Errorf("unsupported subscription proxy type %q", proxy.Type)
	}
	return nil
}

func normalizeSubscriptionTransport(raw map[string]any) subscriptionTransport {
	transportRaw, _ := raw["transport"].(map[string]any)
	transport := subscriptionTransport{Type: strings.ToLower(strings.TrimSpace(stringFromAny(transportRaw["type"])))}
	transport.Path = stringFromAny(transportRaw["path"])
	transport.ServiceName = stringFromAny(transportRaw["service_name"])
	if headers, ok := transportRaw["headers"].(map[string]any); ok {
		transport.Host = stringFromAny(headers["Host"])
		if transport.Host == "" {
			transport.Host = stringFromAny(headers["host"])
		}
	}
	if transport.Host == "" {
		transport.Host = stringFromAny(transportRaw["host"])
	}
	return transport
}

func normalizeSubscriptionTLS(value any, proxyType string) subscriptionTLS {
	tlsRaw, ok := value.(map[string]any)
	tls := subscriptionTLS{Present: ok, Enabled: proxyType == "hysteria2" || proxyType == "anytls"}
	if !ok {
		return tls
	}
	if enabled, ok := tlsRaw["enabled"].(bool); ok {
		tls.Enabled = enabled
	}
	tls.ServerName = stringFromAny(tlsRaw["server_name"])
	tls.Insecure, _ = tlsRaw["insecure"].(bool)
	tls.ALPN = stringListFromAny(tlsRaw["alpn"])
	if utls, ok := tlsRaw["utls"].(map[string]any); ok {
		tls.Fingerprint = stringFromAny(utls["fingerprint"])
	}
	if tls.Fingerprint == "" {
		tls.Fingerprint = stringFromAny(tlsRaw["fingerprint"])
	}
	if reality, ok := tlsRaw["reality"].(map[string]any); ok {
		tls.RealityPublicKey = stringFromAny(reality["public_key"])
		tls.RealityShortID = stringFromAny(reality["short_id"])
		if tls.RealityPublicKey != "" && tls.Fingerprint == "" {
			tls.Fingerprint = "chrome"
		}
	}
	return tls
}

func normalizeSubscriptionObfs(raw map[string]any) (string, string) {
	if mode := strings.ToLower(strings.TrimSpace(stringFromAny(raw["obfs_mode"]))); mode != "" && mode != "none" {
		return mode, ""
	}
	switch value := raw["obfs"].(type) {
	case map[string]any:
		return stringFromAny(value["type"]), stringFromAny(value["password"])
	case string:
		password := stringFromAny(raw["obfs_password"])
		if password == "" {
			password = stringFromAny(raw["obfs-password"])
		}
		return value, password
	default:
		return "", ""
	}
}

func sanitizeSingBoxSubscriptionOutbound(raw map[string]any, proxy subscriptionProxy) map[string]any {
	// tcp_fast_open is a sing-box dial field, so it is allowed only for the
	// types whose data path is TCP; QUIC-based hysteria2/tuic never carry it.
	allowed := map[string][]string{
		"vless":     {"uuid", "flow", "packet_encoding", "tls", "transport", "network", "multiplex", "tcp_fast_open"},
		"vmess":     {"uuid", "security", "alter_id", "tls", "transport", "network", "multiplex", "tcp_fast_open"},
		"trojan":    {"password", "tls", "transport", "network", "multiplex", "tcp_fast_open"},
		"tuic":      {"uuid", "password", "congestion_control", "udp_relay_mode", "zero_rtt_handshake", "heartbeat", "tls"},
		"hysteria2": {"password", "tls", "server_ports", "hop_interval", "hop_interval_max", "up_mbps", "down_mbps", "obfs", "network"},
		"anytls":    {"password", "tls", "tcp_fast_open"},
		"ss":        {"method", "password", "plugin", "plugin_opts", "network", "udp_over_tcp", "multiplex", "tcp_fast_open"},
		"socks5":    {"version", "username", "password", "network", "udp_over_tcp", "tcp_fast_open"},
		"ssh":       {"password", "host_key"},
		"mieru":     {"server_ports", "transport", "username", "password", "multiplexing", "traffic_pattern", "tcp_fast_open"},
		"snell":     {"version", "psk", "obfs_mode", "obfs_host", "mode", "reuse", "network", "tcp_fast_open"},
	}
	typeName := map[string]string{"ss": "shadowsocks", "socks5": "socks"}[proxy.Type]
	if typeName == "" {
		typeName = proxy.Type
	}
	out := map[string]any{
		"type":        typeName,
		"tag":         proxy.Name,
		"server":      proxy.Server,
		"server_port": proxy.Port,
	}
	for _, key := range allowed[proxy.Type] {
		if value, ok := raw[key]; ok && value != nil {
			if key == "tls" {
				value = sanitizeTLSForSubscription(value)
			}
			out[key] = cloneSubscriptionValue(value)
		}
	}
	if proxy.Type == "ss" {
		forceShadowsocksUoTVersion(out)
	}
	if proxy.Type == "ssh" {
		out["user"] = proxy.Username
		out["password"] = proxy.Password
	}
	return out
}

func cloneSubscriptionValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = cloneSubscriptionValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneSubscriptionValue(item)
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func subscriptionTargetSupports(format model.SubscriptionFormat, proxy subscriptionProxy) bool {
	if format == model.SubscriptionFormatSurgeMac {
		return surgeMacSupports(proxy, defaultSurgeMacOptions())
	}
	if proxy.Type == "vmess" || proxy.Type == "trojan" || proxy.Type == "tuic" {
		switch format {
		case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru,
			model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo,
			model.SubscriptionFormatStash, model.SubscriptionFormatShadowrocket,
			model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
			return true
		default:
			return false
		}
	}
	if format == model.SubscriptionFormatSingBoxMieru {
		return proxy.Type != "ssh"
	}
	if format == model.SubscriptionFormatMieru {
		return proxy.Type == "mieru"
	}
	if proxy.Type == "mieru" {
		switch format {
		case model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo, model.SubscriptionFormatShadowrocket:
			return true
		default:
			return false
		}
	}
	if proxy.Type == "ssh" {
		switch format {
		case model.SubscriptionFormatSingBox, model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo, model.SubscriptionFormatShadowrocket, model.SubscriptionFormatStash, model.SubscriptionFormatEgern, model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
			return true
		default:
			return false
		}
	}
	if proxy.Type == "snell" {
		return snellFormatSupports(format, proxy)
	}
	switch format {
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo, model.SubscriptionFormatShadowrocket:
		return true
	case model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
		return proxy.Type != "ssh"
	case model.SubscriptionFormatStash:
		return stashSupports(proxy)
	case model.SubscriptionFormatEgern:
		return egernSupports(proxy)
	case model.SubscriptionFormatLoon:
		return loonSupports(proxy)
	case model.SubscriptionFormatQX:
		return qxSupports(proxy)
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatSurfboard:
		return surgeStyleSupports(format, proxy)
	case model.SubscriptionFormatClash:
		switch proxy.Type {
		case "vless":
			return proxy.Flow == "" && proxy.TLS.RealityPublicKey == ""
		case "ss":
			return classicClashCipher(proxy.Method)
		case "socks5":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func stashSupports(proxy subscriptionProxy) bool {
	if proxy.Type == "vless" && proxy.TLS.RealityPublicKey != "" && proxy.Network != "" && proxy.Network != "tcp" {
		return false
	}
	if proxy.Type == "anytls" {
		return (proxy.Network == "" || proxy.Network == "tcp") && proxy.TLS.RealityPublicKey == ""
	}
	if proxy.Type == "ss" {
		return stashCipher(proxy.Method)
	}
	return true
}

func egernSupports(proxy subscriptionProxy) bool {
	switch proxy.Type {
	case "vless":
		return stringSetContains([]string{"tcp", "ws", "http", "h2", "grpc"}, proxy.Network) && (proxy.Flow == "" || proxy.Flow == "xtls-rprx-vision")
	case "anytls":
		return proxy.Network == "" || proxy.Network == "tcp"
	case "ss":
		return stashCipher(proxy.Method)
	default:
		return true
	}
}

func loonSupports(proxy subscriptionProxy) bool {
	if proxy.Type == "vless" {
		return proxy.Network == "" || proxy.Network == "tcp" || proxy.Network == "ws" || proxy.Network == "http"
	}
	if proxy.Type == "anytls" {
		return proxy.Network == "" || proxy.Network == "tcp"
	}
	if proxy.Type == "ss" {
		return stashCipher(proxy.Method)
	}
	if proxy.Type == "hysteria2" && proxy.ObfsPassword != "" {
		return proxy.ObfsType == "salamander"
	}
	return true
}

func qxSupports(proxy subscriptionProxy) bool {
	switch proxy.Type {
	case "hysteria2":
		return false
	case "vless":
		return stringSetContains([]string{"tcp", "ws", "http"}, proxy.Network) && (proxy.Flow == "" || proxy.Flow == "xtls-rprx-vision")
	case "anytls":
		return proxy.Network == "" || proxy.Network == "tcp"
	case "ss":
		return stashCipher(proxy.Method)
	default:
		return true
	}
}

// snellFormatSupports mirrors the Sub-Store capability matrix for Snell
// nodes (checked against v4/v6 nodes OBoard emits):
//
//   - Surge and Surge Mac: native Snell v1-v6, both v4 and v6 nodes.
//   - Surfboard: Snell v1-v5 only, so v6 nodes are filtered.
//   - Clash Meta / mihomo: Snell v1-v5 only, v6 filtered.
//   - sing-box: v4 and v6 outbounds, both rendered.
//   - Stash: Sub-Store filters Snell v4+; OBoard only emits v4/v6, so all
//     Snell nodes are filtered for Stash (same silent-filter convention as
//     Mieru).
//   - Egern: Snell v1-v5, v6 filtered.
//   - Shadowrocket: Sub-Store accepts Snell v1-v6 but emits a Clash-style
//     YAML body, while OBoard renders the shadowrocket format as a line URI
//     list; Snell has no standard URI scheme, so nodes are filtered and the
//     capability is documented in docs/SUBSCRIPTION_CONVERSION.md.
//   - Loon, Quantumult X, classic Clash, and V2Ray URI formats: no Snell.
func snellFormatSupports(format model.SubscriptionFormat, proxy subscriptionProxy) bool {
	if proxy.Version != SnellVersionV4 && proxy.Version != SnellVersionV6 {
		return false
	}
	switch format {
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac:
		return true
	case model.SubscriptionFormatSurfboard:
		return proxy.Version == SnellVersionV4
	case model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo:
		return proxy.Version == SnellVersionV4
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru:
		return true
	case model.SubscriptionFormatEgern:
		return proxy.Version == SnellVersionV4
	case model.SubscriptionFormatLoon, model.SubscriptionFormatQX, model.SubscriptionFormatClash,
		model.SubscriptionFormatStash, model.SubscriptionFormatShadowrocket,
		model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
		return false
	default:
		return false
	}
}

func surgeStyleSupports(format model.SubscriptionFormat, proxy subscriptionProxy) bool {
	switch proxy.Type {
	case "vless":
		return false
	case "anytls":
		return (proxy.Network == "" || proxy.Network == "tcp") && proxy.TLS.RealityPublicKey == ""
	case "ss":
		return stashCipher(proxy.Method)
	case "hysteria2":
		if proxy.ObfsType == "" {
			return true
		}
		if format == model.SubscriptionFormatSurfboard {
			return proxy.ObfsType == "salamander"
		}
		return proxy.ObfsType == "salamander" || proxy.ObfsType == "gecko"
	case "socks5":
		return true
	case "ssh":
		return format == model.SubscriptionFormatSurge || format == model.SubscriptionFormatSurgeMac
	default:
		return false
	}
}

func classicClashCipher(method string) bool {
	return stringSetContains([]string{
		"aes-128-gcm", "aes-192-gcm", "aes-256-gcm", "aes-128-cfb", "aes-192-cfb", "aes-256-cfb",
		"aes-128-ctr", "aes-192-ctr", "aes-256-ctr", "rc4-md5", "chacha20-ietf", "xchacha20",
		"chacha20-ietf-poly1305", "xchacha20-ietf-poly1305",
	}, method)
}

func stashCipher(method string) bool {
	return classicClashCipher(method) || stringSetContains([]string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"}, method)
}

func stringSetContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func renderSingBoxTarget(proxies []subscriptionProxy) (string, error) {
	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	for _, proxy := range proxies {
		outbounds = append(outbounds, cloneSubscriptionValue(proxy.Native).(map[string]any))
	}
	config := SingBoxConfig{
		Log:       map[string]any{"level": "warn"},
		DNS:       defaultDNS(primaryRemoteDNSTag),
		Inbounds:  []map[string]any{},
		Outbounds: outbounds,
		Route:     map[string]any{"final": "direct"},
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func renderMieruTarget(proxies []subscriptionProxy) (string, error) {
	lines := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		query := url.Values{}
		query.Set("profile", proxy.Name)
		query.Add("port", strconv.Itoa(proxy.Port))
		query.Add("protocol", strings.ToUpper(proxy.Network))
		for _, portRange := range proxy.ServerPorts {
			query.Add("port", portRange)
			query.Add("protocol", strings.ToUpper(proxy.Network))
		}
		setQueryIfNotEmpty(query, "multiplexing", proxy.Multiplexing)
		setQueryIfNotEmpty(query, "traffic-pattern", proxy.TrafficPattern)
		shareURL := &url.URL{
			Scheme:   "mierus",
			User:     url.UserPassword(proxy.Username, proxy.Password),
			Host:     subscriptionEndpoint(proxy.Server, 0),
			RawQuery: encodeURIQuery(query),
		}
		lines = append(lines, shareURL.String())
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

type clashSubscriptionDocument struct {
	Proxies     []map[string]any         `yaml:"proxies"`
	ProxyGroups []clashSubscriptionGroup `yaml:"proxy-groups"`
	Rules       []string                 `yaml:"rules"`
}

type clashSubscriptionGroup struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Proxies []string `yaml:"proxies"`
}

type proxyListDocument struct {
	Proxies []map[string]any `yaml:"proxies"`
}

func renderClashTarget(proxies []subscriptionProxy, format model.SubscriptionFormat) (string, error) {
	document := clashSubscriptionDocument{Proxies: []map[string]any{}, ProxyGroups: []clashSubscriptionGroup{}, Rules: []string{"MATCH,DIRECT"}}
	groups := map[string][]string{}
	for _, proxy := range proxies {
		item, err := proxyMapForYAML(proxy, format)
		if err != nil {
			return "", err
		}
		document.Proxies = append(document.Proxies, item)
		groups[proxy.Group] = append(groups[proxy.Group], proxy.Name)
	}
	groupNames := make([]string, 0, len(groups))
	for name := range groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)
	for _, name := range groupNames {
		members := append([]string(nil), groups[name]...)
		members = append(members, "DIRECT")
		document.ProxyGroups = append(document.ProxyGroups, clashSubscriptionGroup{Name: name, Type: "select", Proxies: members})
	}
	if len(groupNames) > 0 {
		document.Rules = []string{"MATCH," + groupNames[0]}
	}
	return marshalSubscriptionYAML(document)
}

func renderProxyListYAML(proxies []subscriptionProxy, format model.SubscriptionFormat) (string, error) {
	document := proxyListDocument{Proxies: []map[string]any{}}
	for _, proxy := range proxies {
		item, err := proxyMapForYAML(proxy, format)
		if err != nil {
			return "", err
		}
		document.Proxies = append(document.Proxies, item)
	}
	return marshalSubscriptionYAML(document)
}

func marshalSubscriptionYAML(value any) (string, error) {
	data, err := yaml.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func proxyMapForYAML(proxy subscriptionProxy, format model.SubscriptionFormat) (map[string]any, error) {
	if format == model.SubscriptionFormatEgern {
		return egernProxyMap(proxy), nil
	}
	return clashStyleProxyMap(proxy, format)
}

func clashStyleProxyMap(proxy subscriptionProxy, format model.SubscriptionFormat) (map[string]any, error) {
	typeName := proxy.Type
	if typeName == "socks5" {
		typeName = "socks5"
	}
	out := map[string]any{"name": proxy.Name, "type": typeName, "server": proxy.Server, "port": proxy.Port}
	switch proxy.Type {
	case "vless":
		out["uuid"] = proxy.UUID
		setNonEmpty(out, "flow", proxy.Flow)
		setNonEmpty(out, "packet-encoding", proxy.PacketEncoding)
		applyClashTransportMap(out, proxy.Transport)
		applyClashTLSMap(out, proxy.TLS)
	case "vmess":
		out["uuid"] = proxy.UUID
		out["alterId"] = proxy.AlterID
		out["cipher"] = defaultString(proxy.Security, "auto")
		applyClashTransportMap(out, proxy.Transport)
		applyClashTLSMap(out, proxy.TLS)
	case "trojan":
		out["password"] = proxy.Password
		out["udp"] = true
		applyClashTransportMap(out, proxy.Transport)
		applyClashTLSMap(out, proxy.TLS)
	case "tuic":
		out["uuid"] = proxy.UUID
		out["password"] = proxy.Password
		out["udp"] = true
		applyClashTLSMap(out, proxy.TLS)
		if value := stringFromAny(proxy.Native["congestion_control"]); value != "" {
			out["congestion-controller"] = value
		}
	case "hysteria2":
		if format == model.SubscriptionFormatStash {
			out["auth"] = proxy.Password
		} else {
			out["password"] = proxy.Password
		}
		applyClashTLSMap(out, proxy.TLS)
		out["udp"] = true
		setNonEmpty(out, "ports", hoppingPorts(proxy))
		setNonEmpty(out, "hop-interval", proxy.HopInterval)
		setNonEmpty(out, "obfs", proxy.ObfsType)
		setNonEmpty(out, "obfs-password", proxy.ObfsPassword)
		if format == model.SubscriptionFormatStash {
			if proxy.UpMbps > 0 {
				out["up-speed"] = proxy.UpMbps
			}
			if proxy.DownMbps > 0 {
				out["down-speed"] = proxy.DownMbps
			}
		} else {
			if proxy.UpMbps > 0 {
				out["up"] = proxy.UpMbps
			}
			if proxy.DownMbps > 0 {
				out["down"] = proxy.DownMbps
			}
		}
	case "anytls":
		out["password"] = proxy.Password
		out["udp"] = true
		applyClashTLSMap(out, proxy.TLS)
	case "ss":
		out["type"] = "ss"
		out["cipher"] = proxy.Method
		out["password"] = proxy.Password
		out["udp"] = true
		if proxy.UoT {
			out["udp-over-tcp"] = true
			out["udp-over-tcp-version"] = shadowsocksUoTVersion
		}
	case "socks5":
		setNonEmpty(out, "username", proxy.Username)
		setNonEmpty(out, "password", proxy.Password)
		out["udp"] = true
	case "ssh":
		if format == model.SubscriptionFormatStash {
			out["user"] = proxy.Username
		} else {
			out["username"] = proxy.Username
		}
		out["password"] = proxy.Password
		out["host-key"] = append([]string(nil), proxy.HostKeys...)
	case "mieru":
		ports, err := mieruPortsFromValue(proxy.Port, proxy.ServerPorts)
		if err != nil {
			return nil, err
		}
		delete(out, "port")
		if portRange, ok := contiguousMieruPortRange(ports); ok {
			out["port-range"] = portRange
		} else {
			out["port"] = ports[0]
		}
		out["transport"] = strings.ToUpper(proxy.Network)
		out["username"] = proxy.Username
		out["password"] = proxy.Password
		out["udp"] = true
		if format == model.SubscriptionFormatShadowrocket {
			out["user-hint-is-mandatory"] = true
		}
		if proxy.Multiplexing != "MULTIPLEXING_DEFAULT" {
			setNonEmpty(out, "multiplexing", proxy.Multiplexing)
		}
		setNonEmpty(out, "traffic-pattern", proxy.TrafficPattern)
	case "snell":
		out["type"] = "snell"
		out["psk"] = proxy.PSK
		out["version"] = proxy.Version
		out["udp"] = true
		if proxy.Reuse {
			out["reuse"] = true
		}
		if proxy.ObfsType != "" {
			opts := map[string]any{"mode": proxy.ObfsType}
			if proxy.ObfsHost != "" {
				opts["host"] = proxy.ObfsHost
			}
			out["obfs-opts"] = opts
		}
	}
	if proxy.TCPFastOpen && clashFormatSupportsTFO(format) && clashTypeSupportsTFO(proxy.Type) {
		out["tfo"] = true
	}
	return out, nil
}

// clashFormatSupportsTFO limits the `tfo` key to the mihomo engine, which
// documents it as a shared proxy option.
func clashFormatSupportsTFO(format model.SubscriptionFormat) bool {
	return format == model.SubscriptionFormatMihomo || format == model.SubscriptionFormatClashMeta
}

func clashTypeSupportsTFO(proxyType string) bool {
	switch proxyType {
	case "vless", "vmess", "trojan", "ss", "socks5", "snell", "anytls":
		return true
	default:
		return false
	}
}

func contiguousMieruPortRange(ports []int) (string, bool) {
	if len(ports) < 2 {
		return "", false
	}
	for index := 1; index < len(ports); index++ {
		if ports[index] != ports[index-1]+1 {
			return "", false
		}
	}
	return fmt.Sprintf("%d-%d", ports[0], ports[len(ports)-1]), true
}

func applyClashTransportMap(out map[string]any, transport subscriptionTransport) {
	if transport.Type == "" || transport.Type == "tcp" {
		return
	}
	out["network"] = transport.Type
	switch transport.Type {
	case "ws":
		opts := map[string]any{}
		setNonEmpty(opts, "path", defaultString(transport.Path, "/"))
		if transport.Host != "" {
			opts["headers"] = map[string]any{"Host": transport.Host}
		}
		out["ws-opts"] = opts
	case "grpc":
		opts := map[string]any{}
		setNonEmpty(opts, "grpc-service-name", transport.ServiceName)
		out["grpc-opts"] = opts
	case "http":
		opts := map[string]any{}
		if transport.Path != "" {
			opts["path"] = []string{transport.Path}
		}
		if transport.Host != "" {
			opts["headers"] = map[string]any{"Host": []string{transport.Host}}
		}
		out["http-opts"] = opts
	}
}

func applyClashTLSMap(out map[string]any, tls subscriptionTLS) {
	if tls.Present || tls.Enabled {
		out["tls"] = tls.Enabled
	}
	setNonEmpty(out, "servername", tls.ServerName)
	if tls.Insecure {
		out["skip-cert-verify"] = true
	}
	if len(tls.ALPN) > 0 {
		out["alpn"] = append([]string(nil), tls.ALPN...)
	}
	if tls.RealityPublicKey != "" {
		out["client-fingerprint"] = defaultString(tls.Fingerprint, "chrome")
		reality := map[string]any{"public-key": tls.RealityPublicKey}
		setNonEmpty(reality, "short-id", tls.RealityShortID)
		out["reality-opts"] = reality
	}
}

func egernProxyMap(proxy subscriptionProxy) map[string]any {
	out := map[string]any{"name": proxy.Name, "server": proxy.Server, "port": proxy.Port}
	typeName := proxy.Type
	switch proxy.Type {
	case "vless":
		out["user_id"] = proxy.UUID
		out["udp_relay"] = true
		setNonEmpty(out, "flow", proxy.Flow)
		out["transport"] = egernTransportMap(proxy)
	case "hysteria2":
		out["auth"] = proxy.Password
		out["udp_relay"] = true
		setNonEmpty(out, "sni", proxy.TLS.ServerName)
		if proxy.TLS.Insecure {
			out["skip_tls_verify"] = true
		}
		setNonEmpty(out, "port_hopping", hoppingPorts(proxy))
		setNonEmpty(out, "port_hopping_interval", proxy.HopInterval)
		setNonEmpty(out, "obfs", proxy.ObfsType)
		setNonEmpty(out, "obfs_password", proxy.ObfsPassword)
		if proxy.UpMbps > 0 {
			out["bandwidth"] = proxy.UpMbps
		}
	case "anytls":
		out["password"] = proxy.Password
		out["udp_relay"] = true
		setNonEmpty(out, "sni", proxy.TLS.ServerName)
		if proxy.TLS.Insecure {
			out["skip_tls_verify"] = true
		}
		if reality := egernRealityMap(proxy.TLS); reality != nil {
			out["reality"] = reality
		}
	case "ss":
		typeName = "shadowsocks"
		method := proxy.Method
		if method == "chacha20-ietf-poly1305" {
			method = "chacha20-poly1305"
		}
		out["method"] = method
		out["password"] = proxy.Password
		out["udp_relay"] = !proxy.UoT
	case "socks5":
		setNonEmpty(out, "username", proxy.Username)
		setNonEmpty(out, "password", proxy.Password)
		out["udp_relay"] = true
	case "ssh":
		out["username"] = proxy.Username
		out["password"] = proxy.Password
		out["host_keys"] = append([]string(nil), proxy.HostKeys...)
	case "snell":
		out["psk"] = proxy.PSK
		out["version"] = proxy.Version
		out["udp_relay"] = true
		if proxy.Reuse {
			out["reuse"] = true
		}
		setNonEmpty(out, "obfs", proxy.ObfsType)
		setNonEmpty(out, "obfs_host", proxy.ObfsHost)
	}
	return map[string]any{typeName: out}
}

func egernTransportMap(proxy subscriptionProxy) map[string]any {
	transportType := proxy.Transport.Type
	if transportType == "" {
		transportType = proxy.Network
	}
	switch transportType {
	case "ws":
		key := "ws"
		if proxy.TLS.Enabled {
			key = "wss"
		}
		value := map[string]any{"path": defaultString(proxy.Transport.Path, "/")}
		if proxy.Transport.Host != "" {
			value["headers"] = map[string]any{"Host": proxy.Transport.Host}
		}
		setNonEmpty(value, "sni", proxy.TLS.ServerName)
		if proxy.TLS.Insecure {
			value["skip_tls_verify"] = true
		}
		return map[string]any{key: value}
	case "grpc":
		value := map[string]any{}
		setNonEmpty(value, "service_name", proxy.Transport.ServiceName)
		setNonEmpty(value, "sni", proxy.TLS.ServerName)
		if proxy.TLS.Insecure {
			value["skip_tls_verify"] = true
		}
		return map[string]any{"grpc": value}
	case "http", "h2":
		value := map[string]any{}
		setNonEmpty(value, "path", proxy.Transport.Path)
		if proxy.Transport.Host != "" {
			value["headers"] = map[string]any{"Host": proxy.Transport.Host}
		}
		setNonEmpty(value, "sni", proxy.TLS.ServerName)
		if proxy.TLS.Insecure {
			value["skip_tls_verify"] = true
		}
		return map[string]any{"http": value}
	default:
		key := "tcp"
		if proxy.TLS.Enabled {
			key = "tls"
		}
		value := map[string]any{}
		setNonEmpty(value, "sni", proxy.TLS.ServerName)
		if proxy.TLS.Insecure {
			value["skip_tls_verify"] = true
		}
		if reality := egernRealityMap(proxy.TLS); reality != nil {
			value["reality"] = reality
		}
		return map[string]any{key: value}
	}
}

func egernRealityMap(tls subscriptionTLS) map[string]any {
	if tls.RealityPublicKey == "" {
		return nil
	}
	out := map[string]any{"public_key": tls.RealityPublicKey}
	setNonEmpty(out, "short_id", tls.RealityShortID)
	return out
}

func setNonEmpty(target map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		target[key] = value
	}
}

func setQueryIfNotEmpty(query url.Values, key, value string) {
	if strings.TrimSpace(value) != "" {
		query.Set(key, value)
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func stringListFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := scalarString(item); text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{typed}
	default:
		return nil
	}
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func hoppingPorts(proxy subscriptionProxy) string {
	if len(proxy.ServerPorts) == 0 {
		return ""
	}
	values := make([]string, 0, len(proxy.ServerPorts)+1)
	values = append(values, strconv.Itoa(proxy.Port))
	values = append(values, proxy.ServerPorts...)
	return strings.Join(values, ",")
}

func subscriptionEndpoint(host string, port int) string {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if port > 0 {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		return "[" + host + "]"
	}
	return host
}

func renderClientLines(proxies []subscriptionProxy, format model.SubscriptionFormat) (string, error) {
	lines := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		var line string
		var err error
		switch format {
		case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac:
			line, err = renderSurgeLine(proxy, format)
		case model.SubscriptionFormatSurfboard:
			line, err = renderSurfboardLine(proxy)
		case model.SubscriptionFormatLoon:
			line, err = renderLoonLine(proxy)
		case model.SubscriptionFormatQX:
			line, err = renderQXLine(proxy)
		default:
			err = fmt.Errorf("unsupported line subscription format %q", format)
		}
		if err != nil {
			return "", err
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func renderSurgeLine(proxy subscriptionProxy, format model.SubscriptionFormat) (string, error) {
	name := sanitizeConfName(proxy.Name)
	host := subscriptionEndpoint(proxy.Server, 0)
	parts := []string{}
	switch proxy.Type {
	case "ss":
		parts = append(parts, fmt.Sprintf("%s=ss,%s,%d", name, host, proxy.Port), "encrypt-method="+proxy.Method, "password="+quoteConf(proxy.Password))
		parts = append(parts, "udp-relay="+strconv.FormatBool(!proxy.UoT))
	case "socks5":
		parts = append(parts, fmt.Sprintf("%s=socks5,%s,%d", name, host, proxy.Port))
		if proxy.Username != "" {
			parts = append(parts, "username="+quoteConf(proxy.Username))
		}
		if proxy.Password != "" {
			parts = append(parts, "password="+quoteConf(proxy.Password))
		}
		parts = append(parts, "udp-relay=true")
	case "hysteria2":
		parts = append(parts, fmt.Sprintf("%s=hysteria2,%s,%d", name, host, proxy.Port), "password="+quoteConf(proxy.Password))
		appendHoppingLineFields(&parts, proxy, ";")
		if proxy.ObfsPassword != "" {
			field := "salamander-password"
			if proxy.ObfsType == "gecko" {
				field = "gecko-password"
			}
			parts = append(parts, field+"="+quoteConf(proxy.ObfsPassword))
		}
		appendSurgeTLSFields(&parts, proxy.TLS)
		if proxy.DownMbps > 0 {
			parts = append(parts, "download-bandwidth="+strconv.Itoa(proxy.DownMbps))
		}
		parts = append(parts, "udp-relay=true")
	case "anytls":
		parts = append(parts, fmt.Sprintf("%s=anytls,%s,%d", name, host, proxy.Port), "password="+quoteConf(proxy.Password))
		appendSurgeTLSFields(&parts, proxy.TLS)
		parts = append(parts, "udp-relay=true")
	case "ssh":
		parts = append(parts, fmt.Sprintf("%s=ssh,%s,%d", name, host, proxy.Port), "username="+quoteConf(proxy.Username), "password="+quoteConf(proxy.Password), "server-fingerprint="+quoteConf(strings.Join(proxy.HostKeys, ",")))
	case "snell":
		parts = append(parts, fmt.Sprintf("%s=snell,%s,%d", name, host, proxy.Port), "version="+strconv.Itoa(proxy.Version), "psk="+quoteConf(proxy.PSK))
		if proxy.Version == SnellVersionV6 {
			if proxy.Mode != "" && proxy.Mode != "default" {
				parts = append(parts, "mode="+proxy.Mode)
			}
			parts = append(parts, "udp-relay=true")
		} else {
			if proxy.ObfsType != "" {
				parts = append(parts, "obfs="+proxy.ObfsType)
				if proxy.ObfsHost != "" {
					parts = append(parts, "obfs-host="+quoteConf(proxy.ObfsHost))
				}
			}
			parts = append(parts, "udp-relay=true")
		}
	default:
		return "", fmt.Errorf("Surge does not support subscription proxy type %q", proxy.Type)
	}
	// `tfo` is a documented Surge proxy parameter; Surfboard has no equivalent,
	// so it stays out of the Surfboard line.
	if proxy.TCPFastOpen && proxy.Type != "ssh" && (format == model.SubscriptionFormatSurge || format == model.SubscriptionFormatSurgeMac) {
		parts = append(parts, "tfo=true")
	}
	return strings.Join(parts, ","), nil
}

func renderSurfboardLine(proxy subscriptionProxy) (string, error) {
	if proxy.Type == "socks5" {
		parts := []string{fmt.Sprintf("%s=socks5,%s,%d", sanitizeConfName(proxy.Name), subscriptionEndpoint(proxy.Server, 0), proxy.Port)}
		if proxy.Username != "" {
			parts = append(parts, quoteConf(proxy.Username))
		}
		if proxy.Password != "" {
			parts = append(parts, quoteConf(proxy.Password))
		}
		parts = append(parts, "udp-relay=true")
		return strings.Join(parts, ","), nil
	}
	line, err := renderSurgeLine(proxy, model.SubscriptionFormatSurfboard)
	if err != nil {
		return "", err
	}
	return line, nil
}

func renderLoonLine(proxy subscriptionProxy) (string, error) {
	name := sanitizeConfName(proxy.Name)
	host := subscriptionEndpoint(proxy.Server, 0)
	parts := []string{}
	switch proxy.Type {
	case "vless":
		parts = append(parts, fmt.Sprintf("%s=vless,%s,%d,%s", name, host, proxy.Port, quoteConf(proxy.UUID)))
		appendLoonTransport(&parts, proxy)
		appendLoonTLS(&parts, proxy)
		if proxy.Flow != "" {
			parts = append(parts, "flow="+proxy.Flow)
		}
		parts = append(parts, "udp=true")
	case "hysteria2":
		parts = append(parts, fmt.Sprintf("%s=Hysteria2,%s,%d,%s", name, host, proxy.Port, quoteConf(proxy.Password)))
		if ports := hoppingPorts(proxy); ports != "" {
			parts = append(parts, "server-ports="+quoteConf(ports))
		}
		if proxy.HopInterval != "" {
			parts = append(parts, "hop-interval="+proxy.HopInterval)
		}
		appendLoonTLS(&parts, proxy)
		if proxy.ObfsPassword != "" {
			parts = append(parts, "salamander-password="+quoteConf(proxy.ObfsPassword))
		}
		parts = append(parts, "udp=true")
	case "anytls":
		parts = append(parts, fmt.Sprintf("%s=anytls,%s,%d,%s", name, host, proxy.Port, quoteConf(proxy.Password)))
		appendLoonTLS(&parts, proxy)
		parts = append(parts, "udp=true")
	case "ss":
		parts = append(parts, fmt.Sprintf("%s=shadowsocks,%s,%d,%s,%s", name, host, proxy.Port, proxy.Method, quoteConf(proxy.Password)))
		if proxy.UoT {
			parts = append(parts, "udp-over-tcp=true")
		} else {
			parts = append(parts, "udp=true")
		}
	case "socks5":
		parts = append(parts, fmt.Sprintf("%s=socks5,%s,%d", name, host, proxy.Port))
		if proxy.Username != "" {
			parts = append(parts, quoteConf(proxy.Username))
		}
		if proxy.Password != "" {
			parts = append(parts, quoteConf(proxy.Password))
		}
	default:
		return "", fmt.Errorf("Loon does not support subscription proxy type %q", proxy.Type)
	}
	return strings.Join(parts, ","), nil
}

func appendLoonTransport(parts *[]string, proxy subscriptionProxy) {
	transportType := proxy.Network
	if transportType == "" {
		transportType = "tcp"
	}
	*parts = append(*parts, "transport="+transportType)
	if proxy.Transport.Path != "" {
		*parts = append(*parts, "path="+quoteConf(proxy.Transport.Path))
	}
	if proxy.Transport.Host != "" {
		*parts = append(*parts, "host="+quoteConf(proxy.Transport.Host))
	}
}

func appendLoonTLS(parts *[]string, proxy subscriptionProxy) {
	if proxy.TLS.Present || proxy.TLS.Enabled {
		*parts = append(*parts, "over-tls="+strconv.FormatBool(proxy.TLS.Enabled))
	}
	if proxy.TLS.Insecure {
		*parts = append(*parts, "skip-cert-verify=true")
	}
	if len(proxy.TLS.ALPN) > 0 {
		*parts = append(*parts, "alpn="+quoteConf(strings.Join(proxy.TLS.ALPN, ",")))
	}
	if proxy.TLS.RealityPublicKey != "" {
		*parts = append(*parts, "public-key="+quoteConf(proxy.TLS.RealityPublicKey))
		if proxy.TLS.RealityShortID != "" {
			*parts = append(*parts, "short-id="+proxy.TLS.RealityShortID)
		}
		*parts = append(*parts, "tls-profile="+loonTLSProfile(proxy.TLS.Fingerprint))
	} else if proxy.TLS.ServerName != "" {
		*parts = append(*parts, "tls-name="+quoteConf(proxy.TLS.ServerName))
	}
}

func loonTLSProfile(fingerprint string) string {
	switch strings.ToLower(strings.TrimSpace(fingerprint)) {
	case "safari", "ios":
		return "iOS"
	default:
		return "Chrome"
	}
}

func renderQXLine(proxy subscriptionProxy) (string, error) {
	hostPort := subscriptionEndpoint(proxy.Server, proxy.Port)
	parts := []string{}
	switch proxy.Type {
	case "vless":
		parts = append(parts, "vless="+hostPort, "method=none", "password="+escapeConf(proxy.UUID))
		appendQXTransport(&parts, proxy)
		if proxy.Flow != "" {
			parts = append(parts, "vless-flow="+proxy.Flow)
		}
		appendQXTLS(&parts, proxy.TLS)
		parts = append(parts, "udp-relay=true")
	case "anytls":
		parts = append(parts, "anytls="+hostPort, "password="+escapeConf(proxy.Password), "over-tls=true")
		appendQXTLS(&parts, proxy.TLS)
		parts = append(parts, "udp-relay=true")
	case "ss":
		parts = append(parts, "shadowsocks="+hostPort, "method="+proxy.Method, "password="+escapeConf(proxy.Password), "udp-relay=true")
		if proxy.UoT {
			parts = append(parts, "udp-over-tcp=sp.v"+strconv.Itoa(shadowsocksUoTVersion))
		}
	case "socks5":
		parts = append(parts, "socks5="+hostPort)
		if proxy.Username != "" {
			parts = append(parts, "username="+escapeConf(proxy.Username))
		}
		if proxy.Password != "" {
			parts = append(parts, "password="+escapeConf(proxy.Password))
		}
		parts = append(parts, "udp-relay=true")
	default:
		return "", fmt.Errorf("Quantumult X does not support subscription proxy type %q", proxy.Type)
	}
	parts = append(parts, "tag="+escapeConf(proxy.Name))
	return strings.Join(parts, ","), nil
}

func appendQXTransport(parts *[]string, proxy subscriptionProxy) {
	switch proxy.Network {
	case "ws":
		if proxy.TLS.Enabled {
			*parts = append(*parts, "obfs=wss")
		} else {
			*parts = append(*parts, "obfs=ws")
		}
	case "http":
		*parts = append(*parts, "obfs=http")
	case "tcp", "":
		if proxy.TLS.Enabled {
			*parts = append(*parts, "obfs=over-tls")
		}
	}
	if proxy.Transport.Path != "" {
		*parts = append(*parts, "obfs-uri="+escapeConf(proxy.Transport.Path))
	}
	if proxy.Transport.Host != "" {
		*parts = append(*parts, "obfs-host="+escapeConf(proxy.Transport.Host))
	}
}

func appendQXTLS(parts *[]string, tls subscriptionTLS) {
	if tls.Insecure {
		*parts = append(*parts, "tls-verification=false")
	}
	if tls.ServerName != "" {
		*parts = append(*parts, "tls-host="+escapeConf(tls.ServerName))
	}
	if len(tls.ALPN) > 0 {
		*parts = append(*parts, "tls-alpn="+escapeConf(strings.Join(tls.ALPN, ",")))
	}
	if tls.RealityPublicKey != "" {
		*parts = append(*parts, "reality-base64-pubkey="+escapeConf(tls.RealityPublicKey))
		if tls.RealityShortID != "" {
			*parts = append(*parts, "reality-hex-shortid="+escapeConf(tls.RealityShortID))
		}
	}
}

func appendHoppingLineFields(parts *[]string, proxy subscriptionProxy, separator string) {
	if ports := hoppingPorts(proxy); ports != "" {
		*parts = append(*parts, "port-hopping="+quoteConf(strings.ReplaceAll(ports, ",", separator)))
	}
	if proxy.HopInterval != "" {
		*parts = append(*parts, "port-hopping-interval="+proxy.HopInterval)
	}
}

func appendSurgeTLSFields(parts *[]string, tls subscriptionTLS) {
	if tls.ServerName != "" {
		*parts = append(*parts, "sni="+quoteConf(tls.ServerName))
	}
	if tls.Insecure {
		*parts = append(*parts, "skip-cert-verify=true")
	}
	if len(tls.ALPN) > 0 {
		*parts = append(*parts, "alpn="+quoteConf(strings.Join(tls.ALPN, ",")))
	}
}

func sanitizeConfName(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "=", "", ",", "").Replace(value))
}

func quoteConf(value string) string {
	return strconv.Quote(value)
}

func escapeConf(value string) string {
	return strings.NewReplacer("\\", "\\\\", ",", "\\,", "\r", "", "\n", " ").Replace(value)
}

func renderCanonicalURIList(proxies []subscriptionProxy) (string, error) {
	lines := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		line, err := canonicalShareURI(proxy)
		if err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func canonicalShareURI(proxy subscriptionProxy) (string, error) {
	endpoint := subscriptionEndpoint(proxy.Server, proxy.Port)
	fragment := escapeURIComponent(proxy.Name)
	switch proxy.Type {
	case "vless":
		query := url.Values{}
		query.Set("encryption", "none")
		appendURITransport(query, proxy)
		setQueryIfNotEmpty(query, "flow", proxy.Flow)
		setQueryIfNotEmpty(query, "packetEncoding", proxy.PacketEncoding)
		appendURITLS(query, proxy.TLS)
		if proxy.TLS.Insecure {
			query.Del("insecure")
			query.Set("allowInsecure", "1")
		}
		return "vless://" + escapeURIComponent(proxy.UUID) + "@" + endpoint + "?" + query.Encode() + "#" + fragment, nil
	case "vmess":
		transportType := defaultString(proxy.Network, "tcp")
		payload := map[string]any{
			"v": "2", "ps": proxy.Name, "add": proxy.Server, "port": strconv.Itoa(proxy.Port),
			"id": proxy.UUID, "aid": proxy.AlterID, "scy": defaultString(proxy.Security, "auto"),
			"net": transportType, "type": "none", "host": proxy.Transport.Host,
			"path": proxy.Transport.Path, "tls": map[bool]string{true: "tls", false: ""}[proxy.TLS.Enabled],
			"sni": proxy.TLS.ServerName,
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		return "vmess://" + base64.RawStdEncoding.EncodeToString(encoded), nil
	case "trojan":
		query := url.Values{}
		appendURITransport(query, proxy)
		appendURITLS(query, proxy.TLS)
		return "trojan://" + escapeURIComponent(proxy.Password) + "@" + endpoint + querySuffix(query) + "#" + fragment, nil
	case "tuic":
		query := url.Values{}
		setQueryIfNotEmpty(query, "sni", proxy.TLS.ServerName)
		setQueryIfNotEmpty(query, "congestion_control", stringFromAny(proxy.Native["congestion_control"]))
		if proxy.TLS.Insecure {
			query.Set("allow_insecure", "1")
		}
		return "tuic://" + escapeURIComponent(proxy.UUID) + ":" + escapeURIComponent(proxy.Password) + "@" + endpoint + querySuffix(query) + "#" + fragment, nil
	case "hysteria2":
		query := url.Values{}
		if proxy.TLS.Insecure {
			query.Set("insecure", "1")
		}
		setQueryIfNotEmpty(query, "sni", proxy.TLS.ServerName)
		setQueryIfNotEmpty(query, "mport", hoppingPorts(proxy))
		setQueryIfNotEmpty(query, "hop-interval", proxy.HopInterval)
		setQueryIfNotEmpty(query, "obfs", proxy.ObfsType)
		setQueryIfNotEmpty(query, "obfs-password", proxy.ObfsPassword)
		return "hysteria2://" + escapeURIComponent(proxy.Password) + "@" + endpoint + querySuffix(query) + "#" + fragment, nil
	case "anytls":
		query := url.Values{}
		query.Set("encryption", "none")
		appendURITransport(query, proxy)
		appendURITLS(query, proxy.TLS)
		return "anytls://" + escapeURIComponent(proxy.Password) + "@" + endpoint + querySuffix(query) + "#" + fragment, nil
	case "ss":
		var userInfo string
		if strings.HasPrefix(proxy.Method, "2022-blake3-") {
			userInfo = escapeURIComponent(proxy.Method) + ":" + escapeURIComponent(proxy.Password)
		} else {
			userInfo = base64.RawURLEncoding.EncodeToString([]byte(proxy.Method + ":" + proxy.Password))
		}
		query := url.Values{}
		if proxy.UoT {
			query.Set("uot", "1")
		}
		return "ss://" + userInfo + "@" + endpoint + querySuffix(query) + "#" + fragment, nil
	case "socks5":
		credentials := base64.StdEncoding.EncodeToString([]byte(proxy.Username + ":" + proxy.Password))
		return "socks://" + escapeURIComponent(credentials) + "@" + endpoint + "#" + fragment, nil
	case "ssh":
		shareURL := &url.URL{Scheme: "ssh", User: url.UserPassword(proxy.Username, proxy.Password), Host: endpoint, Fragment: proxy.Name}
		return shareURL.String(), nil
	case "mieru":
		query := url.Values{}
		query.Set("profile", proxy.Name)
		query.Add("port", strconv.Itoa(proxy.Port))
		query.Add("protocol", strings.ToUpper(proxy.Network))
		for _, portRange := range proxy.ServerPorts {
			query.Add("port", portRange)
			query.Add("protocol", strings.ToUpper(proxy.Network))
		}
		query.Set("user-hint-is-mandatory", "true")
		setQueryIfNotEmpty(query, "multiplexing", proxy.Multiplexing)
		setQueryIfNotEmpty(query, "traffic-pattern", proxy.TrafficPattern)
		return (&url.URL{Scheme: "mierus", User: url.UserPassword(proxy.Username, proxy.Password), Host: subscriptionEndpoint(proxy.Server, 0), RawQuery: encodeURIQuery(query)}).String(), nil
	default:
		return "", fmt.Errorf("URI subscriptions do not support proxy type %q", proxy.Type)
	}
}

func escapeURIComponent(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func encodeURIQuery(query url.Values) string {
	return strings.ReplaceAll(query.Encode(), "+", "%20")
}

func appendURITransport(query url.Values, proxy subscriptionProxy) {
	network := proxy.Network
	if network == "" {
		network = "tcp"
	}
	query.Set("type", network)
	switch network {
	case "ws", "http":
		setQueryIfNotEmpty(query, "path", proxy.Transport.Path)
		setQueryIfNotEmpty(query, "host", proxy.Transport.Host)
	case "grpc":
		setQueryIfNotEmpty(query, "serviceName", proxy.Transport.ServiceName)
	}
}

func appendURITLS(query url.Values, tls subscriptionTLS) {
	if tls.RealityPublicKey != "" {
		query.Set("security", "reality")
		query.Set("pbk", tls.RealityPublicKey)
		setQueryIfNotEmpty(query, "sid", tls.RealityShortID)
		query.Set("fp", defaultString(tls.Fingerprint, "chrome"))
	} else if tls.Enabled {
		query.Set("security", "tls")
	}
	setQueryIfNotEmpty(query, "sni", tls.ServerName)
	if tls.Insecure {
		query.Set("insecure", "1")
	}
	if len(tls.ALPN) > 0 {
		query.Set("alpn", strings.Join(tls.ALPN, ","))
	}
}
