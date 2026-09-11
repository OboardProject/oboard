package core

import (
	"encoding/json"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
)

type SnellListenerPreview struct {
	InboundID                   int64  `json:"inbound_id"`
	ServerID                    int64  `json:"server_id"`
	CurrentMode                 string `json:"current_mode"`
	TargetMode                  string `json:"target_mode"`
	ActiveMode                  string `json:"active_mode"`
	CurrentPorts                []int  `json:"current_ports"`
	TargetPorts                 []int  `json:"target_ports"`
	AdvertisePort               int    `json:"advertise_port"`
	CredentialCount             int    `json:"credential_count"`
	CredentialLimit             int    `json:"credential_limit"`
	CapabilityReady             bool   `json:"capability_ready"`
	UsersConfirmed              bool   `json:"users_confirmed"`
	AuthorizationConfirmed      bool   `json:"authorization_confirmed"`
	RequiresRestart             bool   `json:"requires_restart"`
	SubscriptionRefreshRequired bool   `json:"subscription_refresh_required"`
	PreviewDigest               string `json:"preview_digest"`
}

func PreviewSnellListener(inbound model.Inbound, server model.Server, target string, users []model.User, opts ConfigOptions) (SnellListenerPreview, error) {
	out := SnellListenerPreview{InboundID: inbound.ID, ServerID: inbound.ServerID, CurrentMode: SnellListenerMode(inbound), TargetMode: target, ActiveMode: inbound.SnellActiveMode, AdvertisePort: inbound.AdvertisePort, CredentialLimit: SnellCredentialLimit, UsersConfirmed: server.UsersConfirmed, AuthorizationConfirmed: server.AuthorizationConfirmed, RequiresRestart: target != SnellListenerMode(inbound), SubscriptionRefreshRequired: target != SnellListenerMode(inbound)}
	if inbound.Protocol != model.ProtocolSnell {
		return out, fmt.Errorf("listener mode requires Snell")
	}
	out.CurrentPorts = SnellRuntimeProbePorts(opts.PortLedger, inbound, false)
	extra := parseExtra(inbound.ConfigJSON)
	extra["listener_mode"] = target
	raw, _ := json.Marshal(extra)
	inbound.ConfigJSON = string(raw)
	if err := ValidateSnellListenerMode(inbound); err != nil {
		return out, err
	}
	_, identities, err := resolveInboundUsers(inbound, users, opts, server.ChainSecret)
	if err != nil {
		return out, err
	}
	for _, u := range identities {
		if u.ID > 0 {
			out.CredentialCount++
		}
	}
	out.CapabilityReady = ServerSupportsSnellShared(server, inbound)
	if SnellSharedPort(inbound) {
		if _, err = snellSharedInbound(inbound, identities); err != nil {
			return out, err
		}
		out.TargetPorts = []int{inbound.Port}
	} else {
		all := append([]model.Inbound(nil), opts.Inbounds...)
		for i := range all {
			if all[i].ID == inbound.ID {
				all[i] = inbound
			}
		}
		if _, _, err = planSnellUserListeners(all, opts.Servers, users, opts); err != nil {
			return out, err
		}
		out.TargetPorts = SnellRuntimeProbePorts(opts.PortLedger, inbound, true)
	}
	return out, nil
}
