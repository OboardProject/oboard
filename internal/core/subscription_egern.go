package core

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
		out["userkey"] = proxy.UserKey
		out["version"] = proxy.Version
		out["udp_relay"] = true
		if proxy.Reuse {
			out["reuse"] = true
		}
		setNonEmpty(out, "obfs", proxy.ObfsType)
		setNonEmpty(out, "obfs_host", proxy.ObfsHost)
	}
	if subscriptionProxyAdvertisesTFO(proxy) {
		out["tfo"] = true
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
