package core

import (
	"fmt"
	"sort"

	"github.com/OboardProject/oboard/internal/model"
)

func EffectiveInboundUsers(inbounds []model.Inbound, users []model.User, direct []model.InboundUser, groups []model.UserGroup, members []model.UserGroupMember, grants []model.InboundAccessGrant) []model.InboundUser {
	activeUsers := map[int64]bool{}
	for _, user := range users {
		if user.Status == "active" {
			activeUsers[user.ID] = true
		}
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	groupEnabled := map[int64]bool{}
	for _, group := range groups {
		if group.Enabled {
			groupEnabled[group.ID] = true
		}
	}
	groupUsers := map[int64][]int64{}
	for _, member := range members {
		if member.Enabled && groupEnabled[member.GroupID] && activeUsers[member.UserID] {
			groupUsers[member.GroupID] = append(groupUsers[member.GroupID], member.UserID)
		}
	}
	pairs := map[string]model.InboundUser{}
	add := func(inboundID, userID int64) {
		if !activeUsers[userID] {
			return
		}
		if _, ok := inboundByID[inboundID]; !ok {
			return
		}
		key := fmt.Sprintf("%d:%d", inboundID, userID)
		pairs[key] = model.InboundUser{InboundID: inboundID, UserID: userID, Enabled: true}
	}
	for _, binding := range direct {
		if binding.Enabled {
			add(binding.InboundID, binding.UserID)
		}
	}
	for _, grant := range grants {
		if !grant.Enabled {
			continue
		}
		for _, inbound := range inbounds {
			if !grantAppliesToInbound(grant, inbound) {
				continue
			}
			switch grant.SubjectType {
			case model.AccessSubjectUser:
				add(inbound.ID, grant.SubjectID)
			case model.AccessSubjectGroup:
				for _, userID := range groupUsers[grant.SubjectID] {
					add(inbound.ID, userID)
				}
			}
		}
	}
	out := make([]model.InboundUser, 0, len(pairs))
	for _, item := range pairs {
		out = append(out, item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].InboundID == out[j].InboundID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].InboundID < out[j].InboundID
	})
	return out
}

func ValidateInboundAccessCapacity(inbounds []model.Inbound, users []model.User, direct []model.InboundUser, groups []model.UserGroup, members []model.UserGroupMember, grants []model.InboundAccessGrant) error {
	effective := EffectiveInboundUsers(inbounds, users, direct, groups, members, grants)
	counts := map[int64]int{}
	for _, binding := range effective {
		if binding.Enabled {
			counts[binding.InboundID]++
		}
	}
	for _, inbound := range inbounds {
		if !InboundSupportsMultipleUsers(inbound) && counts[inbound.ID] > 1 {
			return fmt.Errorf("inbound %s supports only one user", inbound.Name)
		}
	}
	return nil
}

type UserLimitPolicy struct {
	SpeedLimitMbps    int
	TrafficLimitBytes int64
	TrafficResetMode  string
	TrafficResetDay   int
}

func EffectiveUserLimitPolicy(user model.User, groups []model.UserGroup, members []model.UserGroupMember) UserLimitPolicy {
	speedLimitMbps := user.SpeedLimitMbps
	trafficLimitBytes := user.TrafficLimitBytes
	resetMode := user.TrafficResetMode
	resetDay := user.TrafficResetDay
	if resetMode == "" {
		resetMode = "monthly"
	}
	if resetDay <= 0 {
		resetDay = 1
	}
	inheritSpeed := speedLimitMbps == 0
	inheritTraffic := trafficLimitBytes == 0
	if !inheritSpeed && !inheritTraffic {
		return UserLimitPolicy{SpeedLimitMbps: speedLimitMbps, TrafficLimitBytes: trafficLimitBytes, TrafficResetMode: resetMode, TrafficResetDay: resetDay}
	}
	groupByID := map[int64]model.UserGroup{}
	for _, group := range groups {
		if group.Enabled {
			groupByID[group.ID] = group
		}
	}
	for _, member := range members {
		if !member.Enabled || member.UserID != user.ID {
			continue
		}
		group, ok := groupByID[member.GroupID]
		if !ok {
			continue
		}
		if inheritSpeed && group.SpeedLimitMbps > 0 && (speedLimitMbps == 0 || group.SpeedLimitMbps < speedLimitMbps) {
			speedLimitMbps = group.SpeedLimitMbps
		}
		if inheritTraffic && group.TrafficLimitBytes > 0 && (trafficLimitBytes == 0 || group.TrafficLimitBytes < trafficLimitBytes) {
			trafficLimitBytes = group.TrafficLimitBytes
			resetMode = group.TrafficResetMode
			resetDay = group.TrafficResetDay
		}
	}
	if resetMode == "" {
		resetMode = "monthly"
	}
	if resetDay <= 0 {
		resetDay = 1
	}
	return UserLimitPolicy{SpeedLimitMbps: speedLimitMbps, TrafficLimitBytes: trafficLimitBytes, TrafficResetMode: resetMode, TrafficResetDay: resetDay}
}

func grantAppliesToInbound(grant model.InboundAccessGrant, inbound model.Inbound) bool {
	switch grant.ScopeType {
	case model.AccessScopeGlobal:
		return true
	case model.AccessScopeServer:
		return grant.ServerID != nil && *grant.ServerID == inbound.ServerID
	case model.AccessScopeInbound:
		return grant.InboundID != nil && *grant.InboundID == inbound.ID
	default:
		return false
	}
}
