package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

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
		appendURITFO(query, proxy)
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
		appendURITFO(query, proxy)
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
		appendURITFO(query, proxy)
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
		appendURITFO(query, proxy)
		return "ss://" + userInfo + "@" + endpoint + querySuffix(query) + "#" + fragment, nil
	case "socks5":
		credentials := base64.StdEncoding.EncodeToString([]byte(proxy.Username + ":" + proxy.Password))
		query := url.Values{}
		appendURITFO(query, proxy)
		return "socks://" + escapeURIComponent(credentials) + "@" + endpoint + querySuffix(query) + "#" + fragment, nil
	case "ssh":
		shareURL := &url.URL{Scheme: "ssh", User: url.UserPassword(proxy.Username, proxy.Password), Host: endpoint, Fragment: proxy.Name}
		return shareURL.String(), nil
	case "mieru":
		return renderMieruShareURI(proxy, true)
	case "snell":
		encoded := base64.RawStdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:" + proxy.PSK + "@" + endpoint))
		params := []string{"version=" + strconv.Itoa(proxy.Version)}
		if proxy.UserKey != "" {
			params = append(params, "userkey="+escapeURIComponent(proxy.UserKey))
		}
		if proxy.Reuse {
			params = append(params, "reuse=1")
		}
		params = append(params, "udp=1")
		if proxy.ObfsType != "" {
			params = append(params, "obfs="+escapeURIComponent(proxy.ObfsType))
		}
		if proxy.ObfsHost != "" {
			params = append(params, "obfsParam="+escapeURIComponent(proxy.ObfsHost))
		}
		if proxy.Mode != "" && proxy.Mode != "default" {
			params = append(params, "mode="+escapeURIComponent(proxy.Mode))
		}
		if subscriptionProxyAdvertisesTFO(proxy) {
			params = append(params, "tfo=1")
		}
		return "snell://" + encoded + "?" + strings.Join(params, "&") + "#" + fragment, nil
	default:
		return "", fmt.Errorf("URI subscriptions do not support proxy type %q", proxy.Type)
	}
}

func renderMieruShareURI(proxy subscriptionProxy, shadowrocketHint bool) (string, error) {
	query := url.Values{}
	query.Set("profile", proxy.Name)
	query.Add("port", strconv.Itoa(proxy.Port))
	query.Add("protocol", strings.ToUpper(proxy.Network))
	for _, portRange := range proxy.ServerPorts {
		query.Add("port", portRange)
		query.Add("protocol", strings.ToUpper(proxy.Network))
	}
	if shadowrocketHint {
		query.Set("user-hint-is-mandatory", "true")
	}
	setQueryIfNotEmpty(query, "multiplexing", proxy.Multiplexing)
	setQueryIfNotEmpty(query, "traffic-pattern", proxy.TrafficPattern)
	return (&url.URL{
		Scheme:   "mierus",
		User:     url.UserPassword(proxy.Username, proxy.Password),
		Host:     subscriptionEndpoint(proxy.Server, 0),
		RawQuery: encodeURIQuery(query),
	}).String(), nil
}

func escapeURIComponent(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func encodeURIQuery(query url.Values) string {
	return strings.ReplaceAll(query.Encode(), "+", "%20")
}

func setQueryIfNotEmpty(query url.Values, key, value string) {
	if strings.TrimSpace(value) != "" {
		query.Set(key, value)
	}
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

func appendURITFO(query url.Values, proxy subscriptionProxy) {
	if !subscriptionProxyAdvertisesTFO(proxy) || proxy.Type == "mieru" {
		return
	}
	query.Set("tfo", "1")
}
