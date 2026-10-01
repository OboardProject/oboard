package core

import "github.com/OboardProject/oboard/internal/model"

// snellListenerInbound renders the shared listener transport options.
func snellListenerInbound(inbound model.Inbound) (map[string]any, error) {
	extra := parseExtra(inbound.ConfigJSON)
	panelVersion, err := snellPanelVersion(extra)
	if err != nil {
		return nil, err
	}
	serverVersion, err := SnellServerVersion(panelVersion)
	if err != nil {
		return nil, err
	}
	item := map[string]any{
		"type":        "snell",
		"tag":         tag("in", inbound.ID),
		"listen":      inbound.ListenIP,
		"listen_port": inbound.Port,
		"version":     serverVersion,
	}
	if panelVersion == SnellVersionV4 {
		obfs, err := normalizeSnellObfsMode(stringValue(extra, "obfs_mode", "none"))
		if err != nil {
			return nil, err
		}
		if obfs != "none" {
			item["obfs_mode"] = obfs
		}
	} else {
		mode, err := normalizeSnellV6Mode(stringValue(extra, "mode", "default"))
		if err != nil {
			return nil, err
		}
		if mode != "default" {
			item["mode"] = mode
		}
	}
	applyAllowed(item, extra, "tcp_fast_open")
	return item, nil
}

// SnellRuntimeProbePorts uses the configured port for deployment and the verified active port for diagnostics.
func SnellRuntimeProbePorts(inbound model.Inbound, projectedOnly bool) []int {
	if inbound.Protocol != model.ProtocolSnell {
		return nil
	}
	if projectedOnly {
		return []int{inbound.Port}
	}
	if inbound.SnellActiveMode == SnellListenerShared && inbound.SnellActivePort > 0 {
		return []int{inbound.SnellActivePort}
	}
	return nil
}

// SnellSubscriptionNode renders the shared endpoint with the identity-scoped standard PSK.
func SnellSubscriptionNode(ledger *ProxyPathPortLedger, user model.User, inbound model.Inbound, server model.Server, pathID int64) (map[string]any, bool, error) {
	extra := parseExtra(inbound.ConfigJSON)
	panelVersion, err := snellPanelVersion(extra)
	if err != nil {
		return nil, false, err
	}
	clientVersion, err := SnellClientVersion(panelVersion)
	if err != nil {
		return nil, false, err
	}
	if user.ID <= 0 || user.AuthorizationKey == "" || user.ProxyPassword == "" {
		return nil, false, nil
	}
	runtimePort := inbound.Port
	serverPort := runtimePort
	if inbound.AdvertisePort > 0 {
		serverPort = inbound.AdvertisePort
	}
	node := map[string]any{
		"type":        "snell",
		"tag":         inbound.Name,
		"server":      server.EntryAddress,
		"server_port": serverPort,
		"version":     clientVersion,
		"psk":         user.ProxyPassword,
	}
	if clientVersion == SnellVersionV4 {
		obfs, err := normalizeSnellObfsMode(stringValue(extra, "obfs_mode", "none"))
		if err != nil {
			return nil, false, err
		}
		if obfs != "none" {
			node["obfs_mode"] = obfs
			if host := stringValue(extra, "obfs_host", ""); host != "" {
				node["obfs_host"] = host
			}
		}
	} else {
		mode, err := normalizeSnellV6Mode(stringValue(extra, "mode", "default"))
		if err != nil {
			return nil, false, err
		}
		if mode != "default" {
			node["mode"] = mode
		}
	}
	if snellReuse(extra) {
		node["reuse"] = true
	}
	applyAllowed(node, extra, "tcp_fast_open")
	return node, true, nil
}
