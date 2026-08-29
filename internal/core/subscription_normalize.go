package core

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// subscriptionProxy is the target-neutral representation used by the local
// subscription converter. Native keeps the allowlisted sing-box fields; all
// other renderers read only the normalized fields below.
//
// Protocol field conversion stays here and in the per-client encoders.
// Client templates must never read these fields to reconstruct SSH, Snell,
// Mieru, or any other proxy.
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
	UserKey        string
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
	proxy.UserKey = stringFromAny(raw["userkey"])
	if proxy.UserKey == "" {
		proxy.UserKey = stringFromAny(raw["user_key"])
	}
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
		if proxy.UserKey == "" {
			return missing("snell userkey")
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

func subscriptionProxyAdvertisesTFO(proxy subscriptionProxy) bool {
	if !proxy.TCPFastOpen {
		return false
	}
	if proxy.Type == "mieru" {
		return strings.EqualFold(proxy.Network, "tcp")
	}
	if proxy.Type == "vless" && proxy.Network == "quic" {
		return false
	}
	return subscriptionTypeSupportsTFO(proxy.Type)
}

func subscriptionTypeSupportsTFO(proxyType string) bool {
	switch proxyType {
	case "vless", "vmess", "trojan", "ss", "socks5", "snell", "anytls":
		return true
	default:
		return false
	}
}

func setNonEmpty(target map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		target[key] = value
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

func stringSetContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func subscriptionConcreteFormatName(format model.SubscriptionFormat) string {
	return string(normalizeSubscriptionFormat(format))
}
