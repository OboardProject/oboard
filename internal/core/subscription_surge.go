package core

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

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
		if proxy.UserKey != "" {
			parts = append(parts, "userkey="+quoteConf(proxy.UserKey))
		}
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
	if subscriptionProxyAdvertisesTFO(proxy) && (format == model.SubscriptionFormatSurge || format == model.SubscriptionFormatSurgeMac) {
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
	return renderSurgeLine(proxy, model.SubscriptionFormatSurfboard)
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
	if subscriptionProxyAdvertisesTFO(proxy) {
		parts = append(parts, "fast-open=true")
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
	if subscriptionProxyAdvertisesTFO(proxy) {
		parts = append(parts, "fast-open=true")
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

func renderClientLines(proxies []subscriptionProxy, format model.SubscriptionFormat) (string, error) {
	lines := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		line, err := renderLineForFormat(proxy, format)
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

func renderLineForFormat(proxy subscriptionProxy, format model.SubscriptionFormat) (string, error) {
	switch format {
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac:
		return renderSurgeLine(proxy, format)
	case model.SubscriptionFormatSurfboard:
		return renderSurfboardLine(proxy)
	case model.SubscriptionFormatLoon:
		return renderLoonLine(proxy)
	case model.SubscriptionFormatQX:
		return renderQXLine(proxy)
	default:
		return "", fmt.Errorf("unsupported line subscription format %q", format)
	}
}
