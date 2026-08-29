package core

import (
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"go.yaml.in/yaml/v3"
)

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
	return mihomoStyleProxyMap(proxy, format)
}

// mihomoStyleProxyMap encodes one normalized proxy as a Mihomo/Stash YAML map.
// Templates must consume this output; they must not rebuild SSH, Snell, or
// Mieru fields from subscriptionProxy.
func mihomoStyleProxyMap(proxy subscriptionProxy, format model.SubscriptionFormat) (map[string]any, error) {
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
		applyYAMLTransportMap(out, proxy.Transport)
		applyYAMLTLSMap(out, proxy.TLS)
	case "vmess":
		out["uuid"] = proxy.UUID
		out["alterId"] = proxy.AlterID
		out["cipher"] = defaultString(proxy.Security, "auto")
		applyYAMLTransportMap(out, proxy.Transport)
		applyYAMLTLSMap(out, proxy.TLS)
	case "trojan":
		out["password"] = proxy.Password
		out["udp"] = true
		applyYAMLTransportMap(out, proxy.Transport)
		applyYAMLTLSMap(out, proxy.TLS)
	case "tuic":
		out["uuid"] = proxy.UUID
		out["password"] = proxy.Password
		out["udp"] = true
		applyYAMLTLSMap(out, proxy.TLS)
		if value := stringFromAny(proxy.Native["congestion_control"]); value != "" {
			out["congestion-controller"] = value
		}
	case "hysteria2":
		if format == model.SubscriptionFormatStash {
			out["auth"] = proxy.Password
		} else {
			out["password"] = proxy.Password
		}
		applyYAMLTLSMap(out, proxy.TLS)
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
		applyYAMLTLSMap(out, proxy.TLS)
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
		out["userkey"] = proxy.UserKey
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
	if yamlFormatSupportsTFO(format) && subscriptionProxyAdvertisesTFO(proxy) {
		out["tfo"] = true
	}
	return out, nil
}

func yamlFormatSupportsTFO(format model.SubscriptionFormat) bool {
	return format == model.SubscriptionFormatMihomo || format == model.SubscriptionFormatStash
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

func applyYAMLTransportMap(out map[string]any, transport subscriptionTransport) {
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

func applyYAMLTLSMap(out map[string]any, tls subscriptionTLS) {
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
