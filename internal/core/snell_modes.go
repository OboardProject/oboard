package core

import (
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
)

const (
	SnellListenerShared      = "shared_port"
	SnellListenerPerIdentity = "per_identity_port"
	SnellCredentialLimit     = 64
)

var (
	ErrSnellUnsupported        = errors.New("snell_multi_psk_unsupported")
	ErrSnellDuplicatePSK       = errors.New("snell_duplicate_psk")
	ErrSnellCredentialLimit    = errors.New("snell_credential_limit_exceeded")
	ErrSnellUnsafeMode         = errors.New("snell_unsafe_mode_not_allowed")
	ErrSnellModeChange         = errors.New("snell_listener_mode_change_required")
	ErrSnellRuntimeUnconfirmed = errors.New("snell_runtime_not_confirmed")
)

func SnellListenerMode(inbound model.Inbound) string {
	return stringValue(parseExtra(inbound.ConfigJSON), "listener_mode", SnellListenerPerIdentity)
}
func SnellSharedPort(inbound model.Inbound) bool {
	return inbound.Protocol == model.ProtocolSnell && SnellListenerMode(inbound) == SnellListenerShared
}
func ValidateSnellListenerMode(inbound model.Inbound) error {
	if inbound.Protocol != model.ProtocolSnell {
		return nil
	}
	switch SnellListenerMode(inbound) {
	case SnellListenerPerIdentity:
		return nil
	case SnellListenerShared:
	default:
		return fmt.Errorf("invalid snell listener_mode")
	}
	if stringValue(parseExtra(inbound.ConfigJSON), "mode", "default") == "unsafe-raw" {
		return ErrSnellUnsafeMode
	}
	return nil
}
func ServerSupportsSnellShared(server model.Server, inbound model.Inbound) bool {
	if !SnellSharedPort(inbound) {
		return true
	}
	version, err := snellPanelVersion(parseExtra(inbound.ConfigJSON))
	if err != nil {
		return false
	}
	cap := "snell_multi_psk_v4_v1"
	if version == 6 {
		cap = "snell_multi_psk_v6_v1"
	}
	return ServerSupportsRuntimeUsers(server) && stringSliceContains(server.KernelCapabilities, cap) && stringSliceContains(server.KernelCapabilities, "authorization_lease_v1") && stringSliceContains(server.KernelCapabilities, "runtime_users_snell_psk_v1") && stringSliceContains(server.KernelCapabilities, "runtime_users_snell_psk_control_v1")
}
func snellSharedInbound(inbound model.Inbound, users []model.User) (map[string]any, error) {
	if err := ValidateSnellListenerMode(inbound); err != nil {
		return nil, err
	}
	// Public PSKs and internal link identities never share a user snapshot.
	filtered := make([]model.User, 0, len(users))
	for _, u := range users {
		if u.ID > 0 {
			filtered = append(filtered, u)
		} else if u.ID < 0 {
			return nil, fmt.Errorf("shared Snell requires a separate internal link listener")
		}
	}
	if len(filtered) > SnellCredentialLimit {
		return nil, ErrSnellCredentialLimit
	}
	item, err := snellListenerInbound(inbound, snellUserListener{Tag: tag("in", inbound.ID), Port: inbound.Port})
	if err != nil {
		return nil, err
	}
	delete(item, "psk")
	item["auth_mode"] = "multi_psk"
	entries := make([]map[string]any, 0, len(filtered))
	seen := map[string]bool{}
	version, _ := snellPanelVersion(parseExtra(inbound.ConfigJSON))
	for _, u := range filtered {
		if u.AuthorizationKey == "" || u.ProxyPassword == "" {
			return nil, fmt.Errorf("snell credential missing")
		}
		if err := validateSnellPSKLength(u.ProxyPassword, version); err != nil {
			return nil, err
		}
		if seen[u.ProxyPassword] {
			return nil, ErrSnellDuplicatePSK
		}
		seen[u.ProxyPassword] = true
		entries = append(entries, map[string]any{"name": protocolAuthUsername(model.ProtocolSnell, u), "psk": u.ProxyPassword})
	}
	item["users"] = entries
	return item, nil
}
