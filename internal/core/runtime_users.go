package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

type RuntimeUserPackage struct {
	Scope         []string                  `json:"scope"`
	UsersRevision int64                     `json:"users_revision"`
	UsersDigest   string                    `json:"users_digest"`
	Mode          string                    `json:"mode"`
	BaseRevision  int64                     `json:"base_revision,omitempty"`
	Chunk         *model.UsersInstallChunk  `json:"chunk,omitempty"`
	Entries       []model.UsersInstallEntry `json:"entries"`

	// ContentDigest is the cached UsersContentDigest of Scope+Entries. It is
	// Controller-local bookkeeping so a cached package does not re-sort, re-encode
	// and re-hash every entry on each Agent pull; Request() never carries it.
	ContentDigest string `json:"-"`
}

func ServerSupportsRuntimeUsers(server model.Server) bool {
	return strings.TrimSpace(server.AgentID) != "" &&
		stringSliceContains(server.KernelCapabilities, model.AgentCapabilityRuntimeUsers) &&
		stringSliceContains(server.KernelCapabilities, model.KernelCapabilityRuntimeUsers)
}

func UsersDigest(revision int64, scope []string, entries []model.UsersInstallEntry) (string, error) {
	return UsersSnapshotDigest(revision, scope, entries)
}

// UsersContentDigest is the users-lane desired-state gate. It deliberately
// ignores the lease-accounting counters the traffic lane owns.
//
// Those three fields move on every accepted traffic report. Hashing them made
// each report advance the server's desired users revision, which forced a
// redelivery, left the Agent's acknowledgement pointing at an already-superseded
// revision, and woke the sync worker to rebuild the whole fleet again — a loop
// paced by the report rate rather than by any timer. The Agent already receives
// the current quota numbers in every traffic-report response and through
// apply_traffic_policy, so the users lane only has to react to identity,
// credential, route, and policy-configuration changes.
//
// The delivered payload is unchanged: it still carries the values current at
// delivery time. Only the change-detection gate is narrower.
func UsersContentDigest(scope []string, entries []model.UsersInstallEntry) (string, error) {
	stable := make([]model.UsersInstallEntry, len(entries))
	for i, entry := range entries {
		entry.Policy.UsedBaselineBytes = 0
		entry.Policy.LeaseBytes = 0
		entry.Policy.ResetLeaseBytes = 0
		stable[i] = entry
	}
	return UsersSnapshotDigest(0, scope, stable)
}

func ProtocolSupportsRuntimeUsers(protocol model.Protocol, inbound model.Inbound) bool {
	switch protocol {
	case model.ProtocolVLESS, model.ProtocolHY2:
		return true
	case model.ProtocolSS:
		return inboundAuthorizedUserCount(inbound) > 1
	default:
		return false
	}
}

func inboundAuthorizedUserCount(inbound model.Inbound) int {
	raw := strings.TrimSpace(inbound.ConfigJSON)
	if raw == "" || raw == "{}" {
		return 0
	}
	var extra map[string]any
	if err := json.Unmarshal([]byte(raw), &extra); err != nil {
		return 0
	}
	switch users := extra["users"].(type) {
	case []any:
		return len(users)
	case []map[string]any:
		return len(users)
	default:
		return 0
	}
}

func RuntimeUserPackageFromConfig(config SingBoxConfig, revision int64) (RuntimeUserPackage, error) {
	managed := map[string]struct{}{}
	if config.OBoard != nil && config.OBoard.RuntimeUsers != nil {
		for _, tag := range config.OBoard.RuntimeUsers.Inbounds {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				managed[tag] = struct{}{}
			}
		}
	}
	if len(managed) == 0 {
		return RuntimeUserPackage{UsersRevision: revision, Mode: "full"}, nil
	}
	pkg := collectRuntimeUserPackage(&config, managed)
	if pkg == nil {
		return RuntimeUserPackage{UsersRevision: revision, Mode: "full"}, nil
	}
	pkg.UsersRevision = revision
	digest, err := UsersDigest(revision, pkg.Scope, pkg.Entries)
	if err != nil {
		return RuntimeUserPackage{}, err
	}
	pkg.UsersDigest = digest
	pkg.Mode = "full"
	return *pkg, nil
}

func ServerSupportsRuntimeUserProtocol(server model.Server, protocol model.Protocol) bool {
	if !ServerSupportsRuntimeUsers(server) {
		return false
	}
	switch protocol {
	case model.ProtocolVLESS:
		return stringSliceContains(server.KernelCapabilities, model.AgentCapabilityRuntimeUsersVLESS)
	case model.ProtocolHY2:
		return stringSliceContains(server.KernelCapabilities, model.AgentCapabilityRuntimeUsersHysteria2)
	case model.ProtocolSS:
		return stringSliceContains(server.KernelCapabilities, model.AgentCapabilityRuntimeUsersShadowsocks)
	default:
		return false
	}
}

func UsersSnapshotDigest(revision int64, scope []string, entries []model.UsersInstallEntry) (string, error) {
	sortedScope := uniqueSortedStrings(scope)
	sortedEntries := append([]model.UsersInstallEntry(nil), entries...)
	sort.Slice(sortedEntries, func(i, j int) bool {
		if sortedEntries[i].InboundTag != sortedEntries[j].InboundTag {
			return sortedEntries[i].InboundTag < sortedEntries[j].InboundTag
		}
		return sortedEntries[i].AuthUser < sortedEntries[j].AuthUser
	})
	canonical, err := json.Marshal(struct {
		Revision int64                     `json:"users_revision"`
		Scope    []string                  `json:"scope"`
		Entries  []model.UsersInstallEntry `json:"entries"`
	}{Revision: revision, Scope: sortedScope, Entries: sortedEntries})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func uniqueSortedStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func applyRuntimeUserStructure(config *SingBoxConfig, server model.Server) *RuntimeUserPackage {
	if config == nil || !ServerSupportsRuntimeUsers(server) {
		return nil
	}
	limits := map[string]OBoardUserRuntimeLimit{}
	if config.OBoard != nil {
		limits = config.OBoard.RateLimits.Users
	}
	managed := map[string]struct{}{}
	for _, item := range config.Inbounds {
		if !inboundIsRuntimeManaged(server, item, limits) {
			continue
		}
		tag, _ := item["tag"].(string)
		if tag != "" {
			managed[tag] = struct{}{}
		}
	}
	if len(managed) == 0 {
		return nil
	}
	pkg := collectRuntimeUserPackage(config, managed)
	rewriteRuntimeUserStructure(config, managed, pkg)
	if config.OBoard == nil {
		config.OBoard = &OBoardRuntimeMetadata{}
	}
	config.OBoard.RuntimeUsers = &OBoardRuntimeUsersMeta{Inbounds: pkg.Scope}
	return pkg
}

func inboundIsRuntimeManaged(server model.Server, item map[string]any, limits map[string]OBoardUserRuntimeLimit) bool {
	kind, _ := item["type"].(string)
	switch kind {
	case "vless":
		if !ServerSupportsRuntimeUserProtocol(server, model.ProtocolVLESS) {
			return false
		}
	case "hysteria2":
		if !ServerSupportsRuntimeUserProtocol(server, model.ProtocolHY2) {
			return false
		}
	case "shadowsocks":
		if !ServerSupportsRuntimeUserProtocol(server, model.ProtocolSS) {
			return false
		}
		if _, ok := item["users"]; !ok {
			return false
		}
	default:
		return false
	}
	return !inboundHasUnkeyedUser(item, limits)
}

// inboundHasUnkeyedUser reports whether an inbound carries an identity that the
// runtime lane cannot install. Every installed entry needs an authorization key,
// and only billable panel users get one, so internal identities (proxy-path
// chain services) have none. Stripping such an inbound into an empty
// user-selector would make the kernel reject the whole snapshot and drop every
// connection on it, so the inbound keeps its static users instead.
func inboundHasUnkeyedUser(item map[string]any, limits map[string]OBoardUserRuntimeLimit) bool {
	for _, user := range inboundUserObjects(item) {
		name, _ := user["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, "__oboard_placeholder_") {
			continue
		}
		if strings.TrimSpace(limits[name].AuthorizationKey) == "" {
			return true
		}
	}
	return false
}

func collectRuntimeUserPackage(config *SingBoxConfig, managed map[string]struct{}) *RuntimeUserPackage {
	scope := make([]string, 0, len(managed))
	for tag := range managed {
		scope = append(scope, tag)
	}
	sort.Strings(scope)
	routes := collectRuntimeUserRoutes(config, managed)
	limits := map[string]OBoardUserRuntimeLimit{}
	if config.OBoard != nil {
		limits = config.OBoard.RateLimits.Users
	}
	entries := []model.UsersInstallEntry{}
	for _, item := range config.Inbounds {
		tag, _ := item["tag"].(string)
		if _, ok := managed[tag]; !ok {
			continue
		}
		for _, user := range inboundUserObjects(item) {
			name, _ := user["name"].(string)
			name = strings.TrimSpace(name)
			if name == "" || strings.Contains(name, "__oboard_placeholder_") {
				continue
			}
			entry := model.UsersInstallEntry{
				InboundTag:    tag,
				AuthUser:      name,
				Credential:    credentialFromUserObject(user),
				RouteOutbound: routes[tag+"\x00"+name],
			}
			if entry.RouteOutbound == "" {
				entry.RouteOutbound = "direct"
			}
			if limit, ok := limits[name]; ok {
				entry.AuthorizationKey = limit.AuthorizationKey
				entry.Identity = model.UsersIdentity{
					UserID: limit.UserID, InboundID: limit.InboundID, PathID: limit.PathID,
					DeviceIDHash: limit.DeviceIDHash, CredentialEpoch: limit.CredentialEpoch, CredentialStatus: limit.CredentialStatus,
				}
				entry.Policy = model.UsersRuntimePolicy{
					AuthorizationKey: limit.AuthorizationKey, UserID: limit.UserID, InboundID: limit.InboundID, PathID: limit.PathID,
					DeviceIDHash: limit.DeviceIDHash, CredentialEpoch: limit.CredentialEpoch, CredentialStatus: limit.CredentialStatus,
					Billable: limit.Billable, SpeedLimitMbps: limit.SpeedLimitMbps, TrafficLimitBytes: limit.TrafficLimitBytes,
					UsedBaselineBytes: limit.UsedBaselineBytes, LeaseBytes: limit.LeaseBytes, ResetLeaseBytes: limit.ResetLeaseBytes,
					LeaseEnforced: limit.LeaseEnforced, PeriodKey: limit.PeriodKey, PeriodStart: limit.PeriodStart, PeriodEnd: limit.PeriodEnd,
					ResetMode: limit.ResetMode, ResetDay: limit.ResetDay, Timezone: limit.Timezone, QuotaState: limit.QuotaState, EnforcementMode: limit.EnforcementMode,
				}
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].InboundTag != entries[j].InboundTag {
			return entries[i].InboundTag < entries[j].InboundTag
		}
		return entries[i].AuthUser < entries[j].AuthUser
	})
	pkg := &RuntimeUserPackage{Scope: scope, Mode: "full", Entries: entries}
	digest, err := UsersSnapshotDigest(0, scope, entries)
	if err == nil {
		pkg.UsersDigest = digest
	}
	return pkg
}

func rewriteRuntimeUserStructure(config *SingBoxConfig, managed map[string]struct{}, pkg *RuntimeUserPackage) {
	for _, item := range config.Inbounds {
		tag, _ := item["tag"].(string)
		if _, ok := managed[tag]; !ok {
			continue
		}
		if item["type"] == "shadowsocks" {
			item["managed"] = true
			delete(item, "users")
			continue
		}
		item["users"] = []any{}
	}
	if config.OBoard != nil && config.OBoard.RateLimits.Users != nil && pkg != nil {
		for _, entry := range pkg.Entries {
			delete(config.OBoard.RateLimits.Users, entry.AuthUser)
		}
		if len(config.OBoard.RateLimits.Users) == 0 {
			config.OBoard.RateLimits.Users = nil
		}
	}
	rules, _ := config.Route["rules"].([]map[string]any)
	if rules == nil {
		if raw, ok := config.Route["rules"].([]any); ok {
			for _, item := range raw {
				if rule, ok := item.(map[string]any); ok {
					rules = append(rules, rule)
				}
			}
		}
	}
	present := map[string]struct{}{}
	for _, outbound := range config.Outbounds {
		tag, _ := outbound["tag"].(string)
		if strings.HasPrefix(tag, "userselector-") {
			present[strings.TrimPrefix(tag, "userselector-")] = struct{}{}
		}
	}
	needed := make([]string, 0, len(managed))
	for tag := range managed {
		if _, ok := present[tag]; !ok {
			needed = append(needed, tag)
		}
	}
	sort.Strings(needed)
	remaining := make([]map[string]any, 0, len(rules)+len(needed))
	for _, rule := range rules {
		if isPureAuthRoute(rule) {
			hit := false
			for _, inboundTag := range stringList(rule["inbound"]) {
				if _, ok := managed[inboundTag]; ok {
					hit = true
					break
				}
			}
			if hit {
				continue
			}
		}
		remaining = append(remaining, rule)
	}
	for _, tag := range needed {
		remaining = append(remaining, map[string]any{"inbound": []string{tag}, "action": "route", "outbound": "userselector-" + tag})
		config.Outbounds = append(config.Outbounds, map[string]any{"type": "user-selector", "tag": "userselector-" + tag, "users": map[string]string{}})
	}
	if config.Route == nil {
		config.Route = map[string]any{}
	}
	if len(remaining) > 0 {
		config.Route["rules"] = remaining
	}
}

func collectRuntimeUserRoutes(config *SingBoxConfig, managed map[string]struct{}) map[string]string {
	out := map[string]string{}
	rules, _ := config.Route["rules"].([]map[string]any)
	if rules == nil {
		if raw, ok := config.Route["rules"].([]any); ok {
			for _, item := range raw {
				if rule, ok := item.(map[string]any); ok {
					rules = append(rules, rule)
				}
			}
		}
	}
	for _, rule := range rules {
		if !isPureAuthRoute(rule) {
			continue
		}
		outbound, _ := rule["outbound"].(string)
		if outbound == "" {
			continue
		}
		users := stringList(rule["auth_user"])
		for _, inboundTag := range stringList(rule["inbound"]) {
			if _, ok := managed[inboundTag]; !ok {
				continue
			}
			for _, user := range users {
				out[inboundTag+"\x00"+user] = outbound
			}
		}
	}
	return out
}

func isPureAuthRoute(rule map[string]any) bool {
	action, _ := rule["action"].(string)
	if action != "" && action != "route" {
		return false
	}
	for key := range rule {
		switch key {
		case "inbound", "auth_user", "action", "outbound":
		default:
			return false
		}
	}
	return true
}

func inboundUserObjects(item map[string]any) []map[string]any {
	raw, ok := item["users"].([]any)
	if !ok {
		if typed, ok := item["users"].([]map[string]any); ok {
			return typed
		}
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		if user, ok := value.(map[string]any); ok {
			out = append(out, user)
		}
	}
	return out
}

func credentialFromUserObject(user map[string]any) model.UsersCredential {
	uuid, _ := user["uuid"].(string)
	password, _ := user["password"].(string)
	userKey, _ := user["userkey"].(string)
	flow, _ := user["flow"].(string)
	return model.UsersCredential{UUID: uuid, Password: password, UserKey: userKey, Flow: flow}
}

func (p *RuntimeUserPackage) Request() model.UsersInstallRequest {
	if p == nil {
		return model.UsersInstallRequest{Mode: "full"}
	}
	return model.UsersInstallRequest{
		Scope:         append([]string(nil), p.Scope...),
		UsersRevision: p.UsersRevision,
		UsersDigest:   p.UsersDigest,
		Mode:          p.Mode,
		BaseRevision:  p.BaseRevision,
		Chunk:         p.Chunk,
		Entries:       append([]model.UsersInstallEntry(nil), p.Entries...),
	}
}
