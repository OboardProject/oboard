package core

import (
	"encoding/json"
	"strings"
)

func sanitizeSingBoxSubscriptionOutbound(raw map[string]any, proxy subscriptionProxy) map[string]any {
	// tcp_fast_open is a sing-box dial field, so it is allowed only for the
	// types whose data path is TCP and whose sing-box dialer accepts it.
	// QUIC-based hysteria2/tuic never carry it. AnyTLS listen may use TFO,
	// but the sing-box AnyTLS outbound rejects it.
	allowed := map[string][]string{
		"vless":     {"uuid", "flow", "packet_encoding", "tls", "transport", "network", "multiplex", "tcp_fast_open"},
		"vmess":     {"uuid", "security", "alter_id", "tls", "transport", "network", "multiplex", "tcp_fast_open"},
		"trojan":    {"password", "tls", "transport", "network", "multiplex", "tcp_fast_open"},
		"tuic":      {"uuid", "password", "congestion_control", "udp_relay_mode", "zero_rtt_handshake", "heartbeat", "tls"},
		"hysteria2": {"password", "tls", "server_ports", "hop_interval", "hop_interval_max", "up_mbps", "down_mbps", "obfs", "network"},
		"anytls":    {"password", "tls"},
		"ss":        {"method", "password", "plugin", "plugin_opts", "network", "udp_over_tcp", "multiplex", "tcp_fast_open"},
		"socks5":    {"version", "username", "password", "network", "udp_over_tcp"},
		"ssh":       {"password", "host_key"},
		"mieru":     {"server_ports", "transport", "username", "password", "multiplexing", "traffic_pattern", "tcp_fast_open"},
		"snell":     {"version", "psk", "userkey", "obfs_mode", "obfs_host", "mode", "reuse", "network", "tcp_fast_open"},
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
		if key == "tcp_fast_open" {
			if subscriptionProxyAdvertisesTFO(proxy) {
				out[key] = true
			}
			continue
		}
		value, ok := raw[key]
		if !ok || value == nil {
			continue
		}
		switch key {
		case "tls":
			value = sanitizeTLSForSubscription(value)
		case "transport":
			// An imported node carries whatever its source wrote. sing-box
			// decodes with unknown fields disallowed and knows no "tcp"
			// transport, so the transport object is rebuilt from the normalized
			// form instead of relayed verbatim.
			if singBoxUsesV2RayTransport(proxy.Type) {
				transport := singBoxTransportForSubscription(proxy.Transport)
				if transport == nil {
					continue
				}
				out[key] = transport
				continue
			}
		}
		out[key] = cloneSubscriptionValue(value)
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

func singBoxUsesV2RayTransport(proxyType string) bool {
	switch proxyType {
	case "vless", "vmess", "trojan":
		return true
	default:
		return false
	}
}

// singBoxTransportForSubscription renders the sing-box transport object for one
// normalized transport. sing-box accepts only http, ws, quic, grpc, and
// httpupgrade; plain TCP is the absence of the object, so any other type yields
// nil and the outbound omits it.
func singBoxTransportForSubscription(transport subscriptionTransport) map[string]any {
	out := map[string]any{"type": transport.Type}
	switch transport.Type {
	case "ws":
		setNonEmpty(out, "path", transport.Path)
		if host := strings.TrimSpace(transport.Host); host != "" {
			out["headers"] = map[string]any{"Host": host}
		}
	case "http":
		setNonEmpty(out, "path", transport.Path)
		setNonEmpty(out, "host", transport.Host)
	case "httpupgrade":
		setNonEmpty(out, "path", transport.Path)
		setNonEmpty(out, "host", transport.Host)
	case "grpc":
		setNonEmpty(out, "service_name", transport.ServiceName)
	case "quic":
	default:
		return nil
	}
	return out
}

func singBoxOutbounds(proxies []subscriptionProxy) []map[string]any {
	outbounds := []map[string]any{{"type": "direct", "tag": "direct"}}
	for _, proxy := range proxies {
		outbounds = append(outbounds, cloneSubscriptionValue(proxy.Native).(map[string]any))
	}
	return outbounds
}

func encodeSingBoxOutboundFragment(proxy subscriptionProxy) (string, error) {
	data, err := json.Marshal(cloneSubscriptionValue(proxy.Native))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func encodeSingBoxOutboundsJSON(proxies []subscriptionProxy) (string, error) {
	data, err := json.MarshalIndent(singBoxOutbounds(proxies), "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func encodeSingBoxRouteRulesJSON() string {
	return "[]"
}
