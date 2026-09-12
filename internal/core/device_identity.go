package core

import (
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// ExpandDeviceUsers projects accounts into eligible data-plane identities.
// Device-specific subscriptions are not issued: leftover device rows never
// become additional proxy identities.
func ExpandDeviceUsers(users []model.User, devices []model.UserDevice) []model.User {
	if devices == nil {
		return append([]model.User(nil), users...)
	}
	out := make([]model.User, 0, len(users))
	for _, user := range users {
		if user.Status != "active" {
			continue
		}
		out = append(out, user)
	}
	return out
}

func UserForDevice(user model.User, device model.UserDevice) model.User {
	out := user
	out.Username = deviceAuthUsername(user.ID, device.DeviceIDHash)
	out.DeviceIDHash = device.DeviceIDHash
	out.CredentialEpoch = device.CredentialEpoch
	out.CredentialSeed, out.SSHRandomID = "", ""
	out.ProxyUsername, out.ProxyPassword, out.ProxyUUID, out.AuthorizationKey = "", "", "", ""
	out.CredentialStatus = device.ProxyAccessState
	if out.CredentialStatus == "" {
		out.CredentialStatus = "active"
	}
	return out
}

// ConsistentDeviceCredentialIdentity reports whether a device identity is
// internally coherent: a device-bound identity carries both a hash and a
// positive epoch, and a legacy account-level identity carries neither. The
// Agent rejects an SSH inbound user that violates this, failing the whole
// deployment for that server, so a violating identity must never reach a
// deployment payload in the first place.
func ConsistentDeviceCredentialIdentity(deviceIDHash string, credentialEpoch int64) bool {
	if strings.TrimSpace(deviceIDHash) == "" {
		return credentialEpoch == 0
	}
	return credentialEpoch > 0
}

// UserCredentialForRoute selects persisted secret material. An account without
// an exact active scope never falls back to account or derived credentials.
func UserCredentialForRoute(user model.User, inboundID, pathID int64, protocol model.Protocol) model.User {
	if user.ID <= 0 {
		return user
	} // Controller-owned managed hop/placeholder.
	user.ProxyUsername, user.ProxyPassword, user.ProxyUUID, user.AuthorizationKey = "", "", "", ""
	if user.Status != "active" || user.CredentialStatus == "revoked" || user.CredentialStatus == "disabled" {
		return user
	}
	// Issuance already refuses an incoherent identity, so a stored credential
	// that still matches one is stale material from before that rule. Treat the
	// identity as credential-less here too: the account drops out of this route
	// exactly like one that owns no credential, instead of projecting secrets
	// the Agent contract rejects.
	if !ConsistentDeviceCredentialIdentity(user.DeviceIDHash, user.CredentialEpoch) {
		return user
	}
	for _, c := range user.ProxyCredentials {
		if c.Status == "active" && c.UserID == user.ID && c.InboundID == inboundID && c.PathID == pathID && c.Protocol == protocol && c.DeviceIDHash == user.DeviceIDHash && c.CredentialEpoch == user.CredentialEpoch && c.ID != "" && c.Username != "" && c.Password != "" && c.UUID != "" {
			user.ProxyUsername, user.ProxyPassword, user.ProxyUUID, user.AuthorizationKey = c.Username, c.Password, c.UUID, c.ID
			return user
		}
	}
	return user
}

func credentialUsersForInbound(users []model.User, inbound model.Inbound) []model.User {
	out := make([]model.User, 0, len(users))
	for _, user := range users {
		user = UserCredentialForRoute(user, inbound.ID, runtimePathIDFromUsername(user.Username), inbound.Protocol)
		if user.ID <= 0 || user.AuthorizationKey != "" {
			out = append(out, user)
		}
	}
	return out
}

// ProxyCredentialScopes enumerates authorization only: no secrets, configuration
// generation or port allocation. Bindings must describe the complete authorized
// snapshot; absent bindings do not grant everyone access.
func ProxyCredentialScopes(users []model.User, devices []model.UserDevice, inbounds []model.Inbound, opts ConfigOptions) []model.ProxyCredential {
	if opts.AccessSnapshot != nil {
		opts.InboundUsers = opts.AccessSnapshot.InboundUserBindings()
		opts.ProxyPathUsers = opts.AccessSnapshot.ProxyPathUserBindings()
	}
	if opts.InboundUsers == nil {
		opts.InboundUsers = []model.InboundUser{}
	}
	if opts.ProxyPathUsers == nil {
		opts.ProxyPathUsers = []model.ProxyPathUser{}
	}
	users = ExpandDeviceUsers(users, devices)
	steps := make(map[int64]bool)
	for _, step := range opts.ProxyPathSteps {
		steps[step.PathID] = true
	}
	seen := make(map[model.ProxyCredential]bool)
	out := []model.ProxyCredential{}
	appendScope := func(user model.User, inbound model.Inbound, pathID int64) {
		if user.ID <= 0 || user.Status != "active" || user.CredentialStatus == "revoked" || user.CredentialStatus == "disabled" {
			return
		}
		if !ConsistentDeviceCredentialIdentity(user.DeviceIDHash, user.CredentialEpoch) {
			return
		}
		c := model.ProxyCredential{UserID: user.ID, InboundID: inbound.ID, PathID: pathID, DeviceIDHash: user.DeviceIDHash, CredentialEpoch: user.CredentialEpoch, Protocol: inbound.Protocol}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, inbound := range inbounds {
		if !inbound.Enabled || inbound.ID <= 0 {
			continue
		}
		paths := proxyPathsForRootInbound(inbound.ID, opts.ProxyPaths)
		if len(paths) == 0 {
			pathID := int64(0)
			if inbound.Protocol == model.ProtocolSSH {
				pathID = SSHDirectBranchPathID(inbound.ID)
			}
			for _, user := range usersForInbound(inbound, users, opts.InboundUsers) {
				appendScope(user, inbound, pathID)
			}
			continue
		}
		for _, path := range paths {
			if path.Kind != model.ProxyPathKindDirect && !steps[path.ID] {
				continue
			}
			for _, user := range usersForProxyPath(path, inbound, users, opts.InboundUsers, opts.ProxyPathUsers) {
				appendScope(user, inbound, path.ID)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.InboundID != b.InboundID {
			return a.InboundID < b.InboundID
		}
		if a.PathID != b.PathID {
			return a.PathID < b.PathID
		}
		if a.DeviceIDHash != b.DeviceIDHash {
			return a.DeviceIDHash < b.DeviceIDHash
		}
		if a.CredentialEpoch != b.CredentialEpoch {
			return a.CredentialEpoch < b.CredentialEpoch
		}
		return a.Protocol < b.Protocol
	})
	return out
}

func deviceAuthUsername(userID int64, deviceIDHash string) string {
	return fmt.Sprintf("u%d__oboard_device_%s", userID, strings.ToLower(strings.TrimSpace(deviceIDHash)))
}
