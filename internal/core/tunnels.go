package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"golang.org/x/crypto/ssh"
)

func ValidateTunnelType(v model.TunnelType) error {
	switch v {
	case model.TunnelTypeWireGuard, model.TunnelTypeSSH:
		return nil
	default:
		return fmt.Errorf("unsupported tunnel type %q", v)
	}
}

func ValidateTunnels(servers []model.Server, tunnels []model.Tunnel) error {
	known := map[int64]bool{}
	for _, s := range servers {
		known[s.ID] = true
	}
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}
		if strings.TrimSpace(t.Name) == "" {
			return errors.New("tunnel name required")
		}
		if t.SourceServerID == 0 || t.TargetServerID == 0 {
			return errors.New("source_server_id and target_server_id required")
		}
		if t.SourceServerID == t.TargetServerID {
			return errors.New("tunnel source and target must be different")
		}
		if !known[t.SourceServerID] {
			return fmt.Errorf("source server %d does not exist", t.SourceServerID)
		}
		if !known[t.TargetServerID] {
			return fmt.Errorf("target server %d does not exist", t.TargetServerID)
		}
		if err := ValidateTunnelType(t.Type); err != nil {
			return err
		}
		if err := ValidateTunnelConfig(t); err != nil {
			return err
		}
		if t.ListenPort != 0 {
			if err := ValidatePort(t.ListenPort); err != nil {
				return fmt.Errorf("listen_port: %w", err)
			}
		}
		if t.TargetPort != 0 {
			if err := ValidatePort(t.TargetPort); err != nil {
				return fmt.Errorf("target_port: %w", err)
			}
		}
	}
	return nil
}

func ValidateTunnelConfig(t model.Tunnel) error {
	if len(t.ConfigJSON) > 8192 {
		return errors.New("tunnel config_json is too large")
	}
	switch t.Type {
	case model.TunnelTypeWireGuard:
		return validateWireGuardTunnelConfig(t)
	case model.TunnelTypeSSH:
		return validateSSHTunnelConfig(t)
	default:
		return ValidateTunnelType(t.Type)
	}
}

func validateWireGuardTunnelConfig(t model.Tunnel) error {
	var marker struct {
		ManagedPair bool `json:"managed_pair"`
	}
	if err := json.Unmarshal([]byte(firstNonEmpty(t.ConfigJSON, "{}")), &marker); err == nil && marker.ManagedPair {
		var pair struct {
			ManagedPair         bool   `json:"managed_pair"`
			SourceAddress       string `json:"source_address"`
			TargetAddress       string `json:"target_address"`
			SourcePrivateKey    string `json:"source_private_key"`
			SourcePublicKey     string `json:"source_public_key"`
			TargetPrivateKey    string `json:"target_private_key"`
			TargetPublicKey     string `json:"target_public_key"`
			PersistentKeepalive int    `json:"persistent_keepalive"`
			ChainMethod         string `json:"chain_method"`
			ManagedBy           string `json:"managed_by"`
			PathID              int64  `json:"path_id"`
			StepID              int64  `json:"step_id"`
		}
		if err := strictJSON(t.ConfigJSON, &pair); err != nil {
			return fmt.Errorf("wireguard managed pair config_json: %w", err)
		}
		for _, key := range []string{pair.SourcePrivateKey, pair.SourcePublicKey, pair.TargetPrivateKey, pair.TargetPublicKey} {
			if unsafeScalar(key, 256) {
				return errors.New("wireguard managed pair keys must be non-empty safe strings")
			}
		}
		if err := validateIPPrefixList([]string{pair.SourceAddress, pair.TargetAddress}); err != nil {
			return fmt.Errorf("wireguard managed pair addresses: %w", err)
		}
		if pair.PersistentKeepalive < 0 || pair.PersistentKeepalive > 65535 {
			return errors.New("wireguard persistent_keepalive must be between 0 and 65535")
		}
		return nil
	}
	var cfg struct {
		PrivateKey          string   `json:"private_key"`
		PeerPublicKey       string   `json:"peer_public_key"`
		AllowedIPs          []string `json:"allowed_ips"`
		PersistentKeepalive int      `json:"persistent_keepalive"`
		ManagedBy           string   `json:"managed_by"`
		PathID              int64    `json:"path_id"`
		StepID              int64    `json:"step_id"`
	}
	if err := strictJSON(t.ConfigJSON, &cfg); err != nil {
		return fmt.Errorf("wireguard config_json: %w", err)
	}
	if unsafeScalar(cfg.PrivateKey, 256) || unsafeScalar(cfg.PeerPublicKey, 256) {
		return errors.New("wireguard keys must be non-empty strings without control characters and <= 256 bytes")
	}
	if cfg.PersistentKeepalive < 0 || cfg.PersistentKeepalive > 65535 {
		return errors.New("wireguard persistent_keepalive must be between 0 and 65535")
	}
	if t.LocalAddress != "" {
		if err := validateIPPrefixList([]string{t.LocalAddress}); err != nil {
			return fmt.Errorf("local_address: %w", err)
		}
	}
	if t.PeerAddress != "" {
		if err := validateIPPrefixList([]string{t.PeerAddress}); err != nil {
			return fmt.Errorf("peer_address: %w", err)
		}
	}
	if len(cfg.AllowedIPs) > 32 {
		return errors.New("wireguard allowed_ips supports at most 32 entries")
	}
	if len(cfg.AllowedIPs) > 0 {
		if err := validateIPPrefixList(cfg.AllowedIPs); err != nil {
			return fmt.Errorf("allowed_ips: %w", err)
		}
	}
	return nil
}

func validateSSHTunnelConfig(t model.Tunnel) error {
	var cfg struct {
		ManagedPair      bool     `json:"managed_pair"`
		Role             string   `json:"role"`
		User             string   `json:"user"`
		KeyPath          string   `json:"key_path"`
		ClientPrivateKey string   `json:"client_private_key"`
		ClientPublicKey  string   `json:"client_public_key"`
		AuthorizedKey    string   `json:"authorized_key"`
		PermitOpen       string   `json:"permit_open"`
		ServerPort       int      `json:"server_port"`
		LocalForward     string   `json:"local_forward"`
		RemoteForward    string   `json:"remote_forward"`
		ExtraArgs        []string `json:"extra_args"`
		ManagedBy        string   `json:"managed_by"`
		ChainMethod      string   `json:"chain_method"`
		PathID           int64    `json:"path_id"`
		StepID           int64    `json:"step_id"`
	}
	if err := strictJSON(t.ConfigJSON, &cfg); err != nil {
		return fmt.Errorf("ssh config_json: %w", err)
	}
	if !safeSSHUser(cfg.User) {
		return errors.New("ssh user must match [A-Za-z0-9._-]{1,64}")
	}
	if cfg.ManagedPair {
		switch cfg.Role {
		case "":
			if err := validateSSHPrivateKey(cfg.ClientPrivateKey); err != nil {
				return fmt.Errorf("ssh managed client_private_key: %w", err)
			}
			if err := validateSSHPublicKey(cfg.ClientPublicKey); err != nil {
				return fmt.Errorf("ssh managed client_public_key: %w", err)
			}
		case "client":
			if err := validateSSHPrivateKey(cfg.ClientPrivateKey); err != nil {
				return fmt.Errorf("ssh managed client_private_key: %w", err)
			}
		case "server":
			if err := validateSSHPublicKey(cfg.AuthorizedKey); err != nil {
				return fmt.Errorf("ssh managed authorized_key: %w", err)
			}
			if err := validateSSHPermitOpen(cfg.PermitOpen); err != nil {
				return fmt.Errorf("ssh managed permit_open: %w", err)
			}
			if err := ValidatePort(cfg.ServerPort); err != nil {
				return fmt.Errorf("ssh managed server_port: %w", err)
			}
		default:
			return errors.New("ssh managed role must be client or server")
		}
	}
	if cfg.KeyPath != "" {
		if unsafeScalar(cfg.KeyPath, 512) || !filepath.IsAbs(cfg.KeyPath) || strings.Contains(filepath.Clean(cfg.KeyPath), "..") {
			return errors.New("ssh key_path must be an absolute safe path")
		}
	}
	if len(cfg.ExtraArgs) > 0 {
		return errors.New("ssh extra_args is disabled; use first-class tunnel fields instead")
	}
	if err := validateSSHForward(cfg.LocalForward); err != nil {
		return fmt.Errorf("local_forward: %w", err)
	}
	if err := validateSSHForward(cfg.RemoteForward); err != nil {
		return fmt.Errorf("remote_forward: %w", err)
	}
	return nil
}

func validateSSHPrivateKey(raw string) error {
	if len(raw) == 0 || len(raw) > 4096 {
		return errors.New("must be a non-empty OpenSSH private key")
	}
	if _, err := ssh.ParseRawPrivateKey([]byte(raw)); err != nil {
		return errors.New("must be a valid OpenSSH private key")
	}
	return nil
}

func validateSSHPublicKey(raw string) error {
	if len(raw) == 0 || len(raw) > 1024 || strings.ContainsAny(raw, "\r\n") {
		return errors.New("must be a non-empty SSH public key")
	}
	if _, _, _, _, err := ssh.ParseAuthorizedKey([]byte(raw)); err != nil {
		return errors.New("must be a valid SSH public key")
	}
	return nil
}

func validateSSHPermitOpen(raw string) error {
	host, portText, err := net.SplitHostPort(raw)
	if err != nil {
		return errors.New("must be host:port")
	}
	if err := ValidateSafeHost(host); err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return errors.New("port must be numeric")
	}
	return ValidatePort(port)
}

func strictJSON(raw string, v any) error {
	if strings.TrimSpace(raw) == "" {
		raw = "{}"
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func unsafeScalar(v string, max int) bool {
	if strings.TrimSpace(v) == "" || len(v) > max {
		return true
	}
	for _, r := range v {
		if r == 0 || r == '\n' || r == '\r' {
			return true
		}
	}
	return false
}

func validateIPPrefixList(items []string) error {
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return errors.New("empty IP prefix")
		}
		if _, err := netip.ParsePrefix(item); err == nil {
			continue
		}
		if _, err := netip.ParseAddr(item); err == nil {
			continue
		}
		return fmt.Errorf("%q is not an IP/CIDR prefix", item)
	}
	return nil
}

func safeSSHUser(user string) bool {
	if user == "" || len(user) > 64 {
		return false
	}
	for _, r := range user {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validateSSHForward(v string) error {
	if v == "" {
		return nil
	}
	if unsafeScalar(v, 512) || strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t") {
		return errors.New("forward spec contains unsafe characters")
	}
	parts := strings.Split(v, ":")
	if len(parts) < 3 || len(parts) > 4 {
		return errors.New("forward spec must be [bind:]port:host:hostport")
	}
	return nil
}

func ValidateTunnelEndpoint(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if unsafeScalar(raw, 253) || strings.HasPrefix(raw, "-") {
		return errors.New("endpoint contains unsafe characters")
	}
	for _, r := range raw {
		if r == ' ' || r == '\t' || r == '/' {
			return errors.New("endpoint contains unsafe characters")
		}
	}
	if strings.Contains(raw, "://") || strings.Contains(raw, "@") {
		return errors.New("endpoint must be a host or IP, not a URL/user@host")
	}
	return ValidateSafeHost(raw)
}

func BuildTunnelPlan(version int64, server model.Server, servers []model.Server, tunnels []model.Tunnel) (model.TunnelPlan, error) {
	byID := map[int64]model.Server{}
	for _, item := range servers {
		byID[item.ID] = item
	}
	out := make([]model.Tunnel, 0)
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}
		if t.Type == model.TunnelTypeWireGuard {
			if projected, ok, err := projectManagedWireGuardPair(t, server.ID); err != nil {
				return model.TunnelPlan{}, err
			} else if ok {
				if err := ValidateTunnelConfig(projected); err != nil {
					return model.TunnelPlan{}, err
				}
				out = append(out, projected)
				continue
			}
		}
		if t.Type == model.TunnelTypeSSH {
			if projected, ok, err := projectManagedSSHPair(t, server.ID); err != nil {
				return model.TunnelPlan{}, err
			} else if ok {
				if err := ValidateTunnelConfig(projected); err != nil {
					return model.TunnelPlan{}, err
				}
				out = append(out, projected)
				continue
			}
		}
		if t.SourceServerID != server.ID {
			continue
		}
		if err := ValidateTunnelType(t.Type); err != nil {
			return model.TunnelPlan{}, err
		}
		if strings.TrimSpace(t.TargetEndpoint) == "" {
			target, ok := byID[t.TargetServerID]
			if !ok {
				return model.TunnelPlan{}, fmt.Errorf("target server %d does not exist", t.TargetServerID)
			}
			t.TargetEndpoint = firstNonEmpty(ResolveServerEntryAddress(target), target.ListenIP)
		}
		out = append(out, t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority == out[j].Priority {
			return out[i].ID < out[j].ID
		}
		return out[i].Priority < out[j].Priority
	})
	return model.TunnelPlan{Version: version, Tunnels: out}, nil
}

func projectManagedSSHPair(t model.Tunnel, serverID int64) (model.Tunnel, bool, error) {
	var pair struct {
		ManagedPair      bool   `json:"managed_pair"`
		ClientPrivateKey string `json:"client_private_key"`
		ClientPublicKey  string `json:"client_public_key"`
		User             string `json:"user"`
		LocalForward     string `json:"local_forward"`
		PermitOpen       string `json:"permit_open"`
		ServerPort       int    `json:"server_port"`
		ManagedBy        string `json:"managed_by"`
		PathID           int64  `json:"path_id"`
		StepID           int64  `json:"step_id"`
	}
	if err := json.Unmarshal([]byte(firstNonEmpty(t.ConfigJSON, "{}")), &pair); err != nil || !pair.ManagedPair {
		return model.Tunnel{}, false, nil
	}
	if serverID != t.SourceServerID && serverID != t.TargetServerID {
		return model.Tunnel{}, false, nil
	}
	projected := t
	cfg := map[string]any{
		"managed_pair": true,
		"user":         pair.User,
		"managed_by":   pair.ManagedBy,
		"path_id":      pair.PathID,
		"step_id":      pair.StepID,
	}
	if serverID == t.SourceServerID {
		cfg["role"] = "client"
		cfg["client_private_key"] = pair.ClientPrivateKey
		cfg["local_forward"] = pair.LocalForward
	} else {
		serverPort := projected.TargetPort
		projected.SourceServerID = serverID
		projected.TargetServerID = t.SourceServerID
		projected.ListenPort = 0
		projected.TargetEndpoint = ""
		projected.TargetPort = 0
		cfg["role"] = "server"
		cfg["authorized_key"] = pair.ClientPublicKey
		cfg["permit_open"] = pair.PermitOpen
		cfg["server_port"] = serverPort
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return model.Tunnel{}, false, err
	}
	projected.ConfigJSON = string(b)
	return projected, true, nil
}

func projectManagedWireGuardPair(t model.Tunnel, serverID int64) (model.Tunnel, bool, error) {
	var pair struct {
		ManagedPair         bool   `json:"managed_pair"`
		SourceAddress       string `json:"source_address"`
		TargetAddress       string `json:"target_address"`
		SourcePrivateKey    string `json:"source_private_key"`
		SourcePublicKey     string `json:"source_public_key"`
		TargetPrivateKey    string `json:"target_private_key"`
		TargetPublicKey     string `json:"target_public_key"`
		PersistentKeepalive int    `json:"persistent_keepalive"`
	}
	if err := json.Unmarshal([]byte(firstNonEmpty(t.ConfigJSON, "{}")), &pair); err != nil || !pair.ManagedPair {
		return model.Tunnel{}, false, nil
	}
	if serverID != t.SourceServerID && serverID != t.TargetServerID {
		return model.Tunnel{}, false, nil
	}
	projected := t
	cfg := map[string]any{"persistent_keepalive": pair.PersistentKeepalive}
	if serverID == t.SourceServerID {
		projected.LocalAddress = pair.SourceAddress
		projected.PeerAddress = prefixHost(pair.TargetAddress) + "/32"
		projected.ListenPort = 0
		cfg["private_key"] = pair.SourcePrivateKey
		cfg["peer_public_key"] = pair.TargetPublicKey
		cfg["allowed_ips"] = []string{projected.PeerAddress}
	} else {
		projected.SourceServerID = serverID
		projected.TargetServerID = t.SourceServerID
		projected.LocalAddress = pair.TargetAddress
		projected.PeerAddress = prefixHost(pair.SourceAddress) + "/32"
		projected.ListenPort = t.TargetPort
		projected.TargetEndpoint = ""
		projected.TargetPort = 0
		cfg["private_key"] = pair.TargetPrivateKey
		cfg["peer_public_key"] = pair.SourcePublicKey
		cfg["allowed_ips"] = []string{projected.PeerAddress}
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return model.Tunnel{}, false, err
	}
	projected.ConfigJSON = string(b)
	return projected, true, nil
}
