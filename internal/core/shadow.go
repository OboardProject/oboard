package core

import (
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// ShadowServerDivergence records one server whose authentication user set
// differs between the legacy runtime and the plan snapshot.
type ShadowServerDivergence struct {
	ServerID    int64    `json:"server_id"`
	ServerName  string   `json:"server_name"`
	LegacyUsers []string `json:"legacy_users"`
	PlanUsers   []string `json:"plan_users"`
}

// ShadowSSHDivergence records one SSH-rooted path whose authorized user set
// differs between the legacy runtime and the plan snapshot.
type ShadowSSHDivergence struct {
	PathID      int64    `json:"path_id"`
	PathName    string   `json:"path_name"`
	LegacyUsers []string `json:"legacy_users"`
	PlanUsers   []string `json:"plan_users"`
}

// ShadowPolicyDivergence records one user whose effective speed/traffic policy
// differs between the legacy group/user resolution and the plan snapshot.
type ShadowPolicyDivergence struct {
	UserID             int64  `json:"user_id"`
	Username           string `json:"username"`
	LegacySpeedMbps    int    `json:"legacy_speed_mbps"`
	PlanSpeedMbps      int    `json:"plan_speed_mbps"`
	LegacyTrafficBytes int64  `json:"legacy_traffic_bytes"`
	PlanTrafficBytes   int64  `json:"plan_traffic_bytes"`
	LegacyResetMode    string `json:"legacy_reset_mode"`
	PlanResetMode      string `json:"plan_reset_mode"`
	LegacyResetDay     int    `json:"legacy_reset_day"`
	PlanResetDay       int    `json:"plan_reset_day"`
}

// ConfiguredBranchesForInbound returns every configured branch for an inbound
// regardless of the requesting user. A standalone inbound is one with no
// configured branches.
func ConfiguredBranchesForInbound(inbound model.Inbound, paths []model.ProxyPath, steps []model.ProxyPathStep) []model.ProxyPath {
	return subscriptionBranchesForInbound(inbound, paths, steps, 0, nil)
}

func shadowSortedUsers(userIDs map[int64]bool, usernames map[int64]string) []string {
	out := make([]string, 0, len(userIDs))
	for id := range userIDs {
		name := usernames[id]
		if name == "" {
			name = "user:" + strconv.FormatInt(id, 10)
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func shadowActiveUserIDs(bindings []model.InboundUser, inboundID int64) map[int64]bool {
	out := map[int64]bool{}
	for _, b := range bindings {
		if b.Enabled && b.InboundID == inboundID {
			out[b.UserID] = true
		}
	}
	return out
}

func shadowActivePathUserIDs(bindings []model.ProxyPathUser, pathID int64) map[int64]bool {
	out := map[int64]bool{}
	for _, b := range bindings {
		if b.Enabled && b.ProxyPathID == pathID {
			out[b.UserID] = true
		}
	}
	return out
}

func shadowUserSetsEqual(a, b map[int64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

// CompareServerAuthUserSets compares, per server, the authentication user set
// produced by the legacy tables against the plan snapshot. A user counts for a
// server when they hold a binding on an enabled inbound owned by that server or
// on a path whose accounting server is that server. The result is bounded to
// maxSamples divergences.
func CompareServerAuthUserSets(servers []model.Server, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, usernames map[int64]string, legacyInboundUsers []model.InboundUser, legacyPathUsers []model.ProxyPathUser, planInboundUsers []model.InboundUser, planPathUsers []model.ProxyPathUser, maxSamples int) []ShadowServerDivergence {
	if maxSamples <= 0 {
		maxSamples = 20
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	pathsByServer := map[int64][]model.ProxyPath{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		serverID, ok := ProxyPathAccountingServerID(path, stepsByPath[path.ID], inbounds)
		if ok && serverID != 0 {
			pathsByServer[serverID] = append(pathsByServer[serverID], path)
		}
	}
	serverByName := map[int64]string{}
	for _, server := range servers {
		serverByName[server.ID] = server.Name
	}
	out := []ShadowServerDivergence{}
	for _, server := range servers {
		legacy := map[int64]bool{}
		plan := map[int64]bool{}
		for _, inbound := range inbounds {
			if inbound.ServerID != server.ID || !inbound.Enabled {
				continue
			}
			for id := range shadowActiveUserIDs(legacyInboundUsers, inbound.ID) {
				legacy[id] = true
			}
			for id := range shadowActiveUserIDs(planInboundUsers, inbound.ID) {
				plan[id] = true
			}
		}
		for _, path := range pathsByServer[server.ID] {
			for id := range shadowActivePathUserIDs(legacyPathUsers, path.ID) {
				legacy[id] = true
			}
			for id := range shadowActivePathUserIDs(planPathUsers, path.ID) {
				plan[id] = true
			}
		}
		if shadowUserSetsEqual(legacy, plan) {
			continue
		}
		out = append(out, ShadowServerDivergence{
			ServerID:    server.ID,
			ServerName:  serverByName[server.ID],
			LegacyUsers: shadowSortedUsers(legacy, usernames),
			PlanUsers:   shadowSortedUsers(plan, usernames),
		})
		if len(out) >= maxSamples {
			break
		}
	}
	return out
}

// CompareSSHUserSets compares the authorized user set per SSH-rooted path
// between the legacy tables and the plan snapshot. Public SSH inbounds are
// only meaningful as proxy-path roots, so the path user set is the SSH user
// set.
func CompareSSHUserSets(paths []model.ProxyPath, inbounds []model.Inbound, legacyPathUsers []model.ProxyPathUser, planPathUsers []model.ProxyPathUser, usernames map[int64]string, maxSamples int) []ShadowSSHDivergence {
	if maxSamples <= 0 {
		maxSamples = 20
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		if inbound.Protocol == model.ProtocolSSH {
			inboundByID[inbound.ID] = inbound
		}
	}
	out := []ShadowSSHDivergence{}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		if _, ok := inboundByID[path.InboundID]; !ok {
			continue
		}
		legacy := shadowActivePathUserIDs(legacyPathUsers, path.ID)
		plan := shadowActivePathUserIDs(planPathUsers, path.ID)
		if shadowUserSetsEqual(legacy, plan) {
			continue
		}
		out = append(out, ShadowSSHDivergence{
			PathID:      path.ID,
			PathName:    path.Name,
			LegacyUsers: shadowSortedUsers(legacy, usernames),
			PlanUsers:   shadowSortedUsers(plan, usernames),
		})
		if len(out) >= maxSamples {
			break
		}
	}
	return out
}

// CompareUserPolicies compares the effective speed/traffic policy per active
// user between the legacy group/user resolution and the plan snapshot. Users
// without a plan binding keep the snapshot default, so a divergence surfaces
// users that need a migration plan.
func CompareUserPolicies(users []model.User, groups []model.UserGroup, members []model.UserGroupMember, planPolicies map[int64]UserLimitPolicy, maxSamples int) []ShadowPolicyDivergence {
	if maxSamples <= 0 {
		maxSamples = 20
	}
	out := []ShadowPolicyDivergence{}
	for _, user := range users {
		if user.Status != "active" || strings.HasPrefix(user.Username, "__oboard_") {
			continue
		}
		legacy := EffectiveUserLimitPolicy(user, groups, members)
		plan := planPolicies[user.ID]
		if legacy == plan {
			continue
		}
		out = append(out, ShadowPolicyDivergence{
			UserID:             user.ID,
			Username:           user.Username,
			LegacySpeedMbps:    legacy.SpeedLimitMbps,
			PlanSpeedMbps:      plan.SpeedLimitMbps,
			LegacyTrafficBytes: legacy.TrafficLimitBytes,
			PlanTrafficBytes:   plan.TrafficLimitBytes,
			LegacyResetMode:    legacy.TrafficResetMode,
			PlanResetMode:      plan.TrafficResetMode,
			LegacyResetDay:     legacy.TrafficResetDay,
			PlanResetDay:       plan.TrafficResetDay,
		})
		if len(out) >= maxSamples {
			break
		}
	}
	return out
}
