package core

import (
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// EffectiveUserPolicy is the resolved speed/traffic policy for one user in the
// plan authorization model. Precedence: explicit user override > active plan
// limits > system defaults.
type EffectiveUserPolicy struct {
	SpeedLimitMbps    int
	TrafficLimitBytes int64
	TrafficResetMode  string
	TrafficResetDay   int
	Source            string // user_override | plan | user | default
}

// EffectiveNodeGrant explains why one user can use one node in the plan model.
type EffectiveNodeGrant struct {
	NodeType     model.AssignableNodeType
	NodeID       int64
	DisplayGroup string
	Source       string // plan | exception_allow
	PlanID       int64
	PlanName     string
	Exception    *model.UserNodeException
}

// EffectiveAccessSnapshot is the single authorization source for the runtime
// chain once the plan model is active. Every consumer (subscription
// generation, Agent config generation, SSH inbound plans, connection
// presence/audit gates, traffic policy) must read this snapshot instead of the
// legacy tables, so every surface agrees on the same user-node relation.
type EffectiveAccessSnapshot struct {
	At                    time.Time
	Users                 map[int64]model.User
	UserNodes             map[int64]map[string]EffectiveNodeGrant
	InboundUsers          map[int64][]int64
	ProxyPathUsers        map[int64][]int64
	ExternalOutboundUsers map[int64][]int64
	NodeUsers             map[string][]int64
	UserPolicies          map[int64]EffectiveUserPolicy
	PathInboundIDs        map[int64]int64
}

// EffectiveAccessInput carries the plan authorization data one snapshot is
// derived from. Bindings and plan nodes must already be the time-effective
// active sets; the resolver re-checks time windows defensively.
type EffectiveAccessInput struct {
	Users             []model.User
	Bindings          []model.UserPlanBinding
	Plans             []model.SubscriptionPlan
	PlanNodes         []model.SubscriptionPlanNode
	Exceptions        []model.UserNodeException
	Paths             []model.ProxyPath
	Steps             []model.ProxyPathStep
	Inbounds          []model.Inbound
	ExternalOutbounds []model.ExternalOutbound
	Now               time.Time
}

// BuildEffectiveAccessSnapshot resolves every active user's node set with the
// fixed priority deny exception > allow exception > active plan > default deny,
// then projects the per-inbound, per-path, per-external and per-node views.
func BuildEffectiveAccessSnapshot(input EffectiveAccessInput) *EffectiveAccessSnapshot {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	snap := &EffectiveAccessSnapshot{
		At:                    now,
		Users:                 map[int64]model.User{},
		UserNodes:             map[int64]map[string]EffectiveNodeGrant{},
		InboundUsers:          map[int64][]int64{},
		ProxyPathUsers:        map[int64][]int64{},
		ExternalOutboundUsers: map[int64][]int64{},
		NodeUsers:             map[string][]int64{},
		UserPolicies:          map[int64]EffectiveUserPolicy{},
		PathInboundIDs:        map[int64]int64{},
	}
	active := map[int64]bool{}
	for _, user := range input.Users {
		if user.Status == "active" {
			active[user.ID] = true
			snap.Users[user.ID] = user
		}
	}
	planByID := map[int64]*model.SubscriptionPlan{}
	for i := range input.Plans {
		plan := input.Plans[i]
		planByID[plan.ID] = &plan
	}
	planNodesByPlan := map[int64][]model.SubscriptionPlanNode{}
	for _, pn := range input.PlanNodes {
		if !pn.Enabled {
			continue
		}
		planNodesByPlan[pn.PlanID] = append(planNodesByPlan[pn.PlanID], pn)
	}
	bindingByUser := map[int64]model.UserPlanBinding{}
	for _, binding := range input.Bindings {
		if !binding.Enabled {
			continue
		}
		if binding.StartsAt != nil && binding.StartsAt.After(now) {
			continue
		}
		if binding.ExpiresAt != nil && !binding.ExpiresAt.After(now) {
			continue
		}
		bindingByUser[binding.UserID] = binding
	}
	exceptionsByUser := map[int64][]model.UserNodeException{}
	for _, ex := range input.Exceptions {
		if ex.Status != "" && ex.Status != model.UserNodeExceptionActive {
			continue
		}
		if ex.StartsAt != nil && ex.StartsAt.After(now) {
			continue
		}
		if !ex.ExpiresAt.After(now) {
			continue
		}
		exceptionsByUser[ex.UserID] = append(exceptionsByUser[ex.UserID], ex)
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range input.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	pathByID := map[int64]model.ProxyPath{}
	for _, path := range input.Paths {
		pathByID[path.ID] = path
		snap.PathInboundIDs[path.ID] = path.InboundID
	}

	for userID := range active {
		user := snap.Users[userID]
		binding, ok := bindingByUser[userID]
		var plan *model.SubscriptionPlan
		var planNodes []model.SubscriptionPlanNode
		if ok {
			if p, found := planByID[binding.PlanID]; found && p.Enabled {
				plan = p
				planNodes = planNodesByPlan[p.ID]
			}
		}
		grantByKey := map[string]EffectiveNodeGrant{}
		if plan != nil {
			for _, pn := range planNodes {
				key := NodeKeyOf(pn.NodeType, pn.NodeID)
				grantByKey[key] = EffectiveNodeGrant{
					NodeType:     pn.NodeType,
					NodeID:       pn.NodeID,
					DisplayGroup: pn.DisplayGroup,
					Source:       "plan",
					PlanID:       plan.ID,
					PlanName:     plan.Name,
				}
			}
		}
		denied := map[string]bool{}
		for _, ex := range exceptionsByUser[userID] {
			key := NodeKeyOf(ex.NodeType, ex.NodeID)
			if ex.Effect == model.UserNodeExceptionDeny {
				denied[key] = true
				delete(grantByKey, key)
			}
		}
		for _, ex := range exceptionsByUser[userID] {
			if ex.Effect != model.UserNodeExceptionAllow {
				continue
			}
			key := NodeKeyOf(ex.NodeType, ex.NodeID)
			if denied[key] {
				continue
			}
			exception := ex
			grantByKey[key] = EffectiveNodeGrant{
				NodeType:  ex.NodeType,
				NodeID:    ex.NodeID,
				Source:    "exception_allow",
				Exception: &exception,
			}
		}
		if len(grantByKey) > 0 {
			snap.UserNodes[userID] = grantByKey
		}
		snap.UserPolicies[userID] = effectiveUserPolicy(user, plan)
		for key, grant := range grantByKey {
			snap.NodeUsers[key] = append(snap.NodeUsers[key], userID)
			switch grant.NodeType {
			case model.AssignableNodeProxyPath:
				if path, found := pathByID[grant.NodeID]; found {
					if inbound, ok := inboundByID[path.InboundID]; ok && inbound.Enabled {
						snap.ProxyPathUsers[grant.NodeID] = append(snap.ProxyPathUsers[grant.NodeID], userID)
						snap.InboundUsers[path.InboundID] = append(snap.InboundUsers[path.InboundID], userID)
					}
				}
			case model.AssignableNodeInbound:
				if inbound, ok := inboundByID[grant.NodeID]; ok && inbound.Enabled {
					snap.InboundUsers[grant.NodeID] = append(snap.InboundUsers[grant.NodeID], userID)
				}
			case model.AssignableNodeExternalOutbound:
				snap.ExternalOutboundUsers[grant.NodeID] = append(snap.ExternalOutboundUsers[grant.NodeID], userID)
			}
		}
	}
	// A user can reach the same inbound both as a standalone inbound node and
	// through a proxy_path that shares the inbound; dedupe after sorting so
	// downstream consumers never see duplicate credentials.
	for inboundID, list := range snap.InboundUsers {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		snap.InboundUsers[inboundID] = compactSortedUserIDs(list)
	}
	for pathID, list := range snap.ProxyPathUsers {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		snap.ProxyPathUsers[pathID] = compactSortedUserIDs(list)
	}
	for externalID, list := range snap.ExternalOutboundUsers {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		snap.ExternalOutboundUsers[externalID] = compactSortedUserIDs(list)
	}
	for key, list := range snap.NodeUsers {
		sort.Slice(list, func(i, j int) bool { return list[i] < list[j] })
		snap.NodeUsers[key] = compactSortedUserIDs(list)
	}
	return snap
}

// compactSortedUserIDs removes adjacent duplicates from an ascending list.
func compactSortedUserIDs(list []int64) []int64 {
	if len(list) < 2 {
		return list
	}
	out := list[:1]
	for _, id := range list[1:] {
		if id != out[len(out)-1] {
			out = append(out, id)
		}
	}
	return out
}

func effectiveUserPolicy(user model.User, plan *model.SubscriptionPlan) EffectiveUserPolicy {
	speed, traffic := user.SpeedLimitMbps, user.TrafficLimitBytes
	mode, day := user.TrafficResetMode, user.TrafficResetDay
	source := "user"
	if speed <= 0 && traffic <= 0 {
		source = "default"
	}
	if plan != nil {
		if speed <= 0 {
			speed = plan.SpeedLimitMbps
		}
		if traffic <= 0 {
			traffic = plan.TrafficLimitBytes
		}
		if user.SpeedLimitMbps > 0 || user.TrafficLimitBytes > 0 {
			source = "user_override"
		} else {
			source = "plan"
		}
		mode, day = plan.TrafficResetMode, plan.TrafficResetDay
		if user.TrafficResetMode != "" {
			mode = user.TrafficResetMode
		}
		if user.TrafficResetDay > 0 {
			day = user.TrafficResetDay
		}
	}
	if strings.TrimSpace(mode) == "" {
		mode = "monthly"
	}
	if day <= 0 {
		day = 1
	}
	return EffectiveUserPolicy{SpeedLimitMbps: speed, TrafficLimitBytes: traffic, TrafficResetMode: mode, TrafficResetDay: day, Source: source}
}

// InboundUserBindings projects the snapshot into the legacy binding shape used
// by config generation and SSH plans.
func (s *EffectiveAccessSnapshot) InboundUserBindings() []model.InboundUser {
	out := make([]model.InboundUser, 0, len(s.InboundUsers))
	for inboundID, userIDs := range s.InboundUsers {
		for _, userID := range userIDs {
			out = append(out, model.InboundUser{InboundID: inboundID, UserID: userID, Enabled: true})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].InboundID == out[j].InboundID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].InboundID < out[j].InboundID
	})
	return out
}

// ProxyPathUserBindings projects the snapshot into the legacy binding shape
// used by config generation and SSH plans.
func (s *EffectiveAccessSnapshot) ProxyPathUserBindings() []model.ProxyPathUser {
	out := make([]model.ProxyPathUser, 0, len(s.ProxyPathUsers))
	for pathID, userIDs := range s.ProxyPathUsers {
		for _, userID := range userIDs {
			out = append(out, model.ProxyPathUser{ProxyPathID: pathID, InboundID: s.PathInboundIDs[pathID], UserID: userID, Enabled: true})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProxyPathID == out[j].ProxyPathID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].ProxyPathID < out[j].ProxyPathID
	})
	return out
}

// UserLimitPolicy is the resolved speed/traffic policy for one user.
type UserLimitPolicy struct {
	SpeedLimitMbps    int
	TrafficLimitBytes int64
	TrafficResetMode  string
	TrafficResetDay   int
}

// UserLimitPolicyMap converts the snapshot policies into the legacy policy
// shape consumed by runtime limit generation.
func (s *EffectiveAccessSnapshot) UserLimitPolicyMap() map[int64]UserLimitPolicy {
	out := map[int64]UserLimitPolicy{}
	for userID, policy := range s.UserPolicies {
		out[userID] = UserLimitPolicy{SpeedLimitMbps: policy.SpeedLimitMbps, TrafficLimitBytes: policy.TrafficLimitBytes, TrafficResetMode: policy.TrafficResetMode, TrafficResetDay: policy.TrafficResetDay}
	}
	return out
}

// EffectiveNodeKeys returns the granted node key set for one user (nil when the
// user has no plan and no allow exceptions).
func (s *EffectiveAccessSnapshot) EffectiveNodeKeys(userID int64) map[string]bool {
	grants, ok := s.UserNodes[userID]
	if !ok || len(grants) == 0 {
		return nil
	}
	out := make(map[string]bool, len(grants))
	for key := range grants {
		out[key] = true
	}
	return out
}

// EffectiveNodeGroups returns the display group per granted node key for one
// user. Nodes without an explicit group are omitted so callers fall back to
// their default group.
func (s *EffectiveAccessSnapshot) EffectiveNodeGroups(userID int64) map[string]string {
	grants, ok := s.UserNodes[userID]
	if !ok {
		return nil
	}
	out := map[string]string{}
	for key, grant := range grants {
		if strings.TrimSpace(grant.DisplayGroup) != "" {
			out[key] = strings.TrimSpace(grant.DisplayGroup)
		}
	}
	return out
}

// AccessProjection is the serializable per-server user-binding projection of a
// snapshot plus the resolved user policies. Access changes materialize their
// prepare/finalize projections as JSON so orchestration survives restarts and
// later plan edits.
type AccessProjection struct {
	InboundUsers   map[int64][]int64             `json:"inbound_users"`
	ProxyPathUsers map[int64][]int64             `json:"proxy_path_users"`
	UserPolicies   map[int64]EffectiveUserPolicy `json:"user_policies"`
	PathInboundIDs map[int64]int64               `json:"path_inbound_ids"`
}

// Projection returns the serializable view of the snapshot.
func (s *EffectiveAccessSnapshot) Projection() AccessProjection {
	return AccessProjection{
		InboundUsers:   cloneUserIDMap(s.InboundUsers),
		ProxyPathUsers: cloneUserIDMap(s.ProxyPathUsers),
		UserPolicies:   s.UserPolicies,
		PathInboundIDs: cloneInt64Map(s.PathInboundIDs),
	}
}

// MergeProjections returns the union of two projections: a user is present for
// a node if either side grants it, and policies prefer the right side so the
// new plan limits win during prepare.
func MergeProjections(a, b AccessProjection) AccessProjection {
	out := AccessProjection{
		InboundUsers:   cloneUserIDMap(a.InboundUsers),
		ProxyPathUsers: cloneUserIDMap(a.ProxyPathUsers),
		UserPolicies:   map[int64]EffectiveUserPolicy{},
		PathInboundIDs: cloneInt64Map(a.PathInboundIDs),
	}
	for key, list := range b.InboundUsers {
		merged := append([]int64(nil), out.InboundUsers[key]...)
		for _, userID := range list {
			if !containsInt64(merged, userID) {
				merged = append(merged, userID)
			}
		}
		sort.Slice(merged, func(i, j int) bool { return merged[i] < merged[j] })
		out.InboundUsers[key] = merged
	}
	for key, list := range b.ProxyPathUsers {
		merged := append([]int64(nil), out.ProxyPathUsers[key]...)
		for _, userID := range list {
			if !containsInt64(merged, userID) {
				merged = append(merged, userID)
			}
		}
		sort.Slice(merged, func(i, j int) bool { return merged[i] < merged[j] })
		out.ProxyPathUsers[key] = merged
	}
	for userID, policy := range a.UserPolicies {
		out.UserPolicies[userID] = policy
	}
	for userID, policy := range b.UserPolicies {
		out.UserPolicies[userID] = policy
	}
	for pathID, inboundID := range b.PathInboundIDs {
		out.PathInboundIDs[pathID] = inboundID
	}
	return out
}

// ProjectionSnapshot converts a stored projection back into the snapshot shape
// config generation consumes. UserPolicies always uses the resolved policies
// recorded with the projection; the user rows are only used for identity data.
func ProjectionSnapshot(projection AccessProjection, users []model.User) *EffectiveAccessSnapshot {
	snap := &EffectiveAccessSnapshot{
		At:                    time.Now(),
		Users:                 map[int64]model.User{},
		UserNodes:             map[int64]map[string]EffectiveNodeGrant{},
		InboundUsers:          cloneUserIDMap(projection.InboundUsers),
		ProxyPathUsers:        cloneUserIDMap(projection.ProxyPathUsers),
		ExternalOutboundUsers: map[int64][]int64{},
		NodeUsers:             map[string][]int64{},
		UserPolicies:          projection.UserPolicies,
		PathInboundIDs:        cloneInt64Map(projection.PathInboundIDs),
	}
	for _, user := range users {
		snap.Users[user.ID] = user
	}
	return snap
}

func cloneUserIDMap(in map[int64][]int64) map[int64][]int64 {
	if in == nil {
		return map[int64][]int64{}
	}
	out := make(map[int64][]int64, len(in))
	for key, list := range in {
		out[key] = append([]int64(nil), list...)
	}
	return out
}

func cloneInt64Map(in map[int64]int64) map[int64]int64 {
	if in == nil {
		return map[int64]int64{}
	}
	out := make(map[int64]int64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func containsInt64(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}
