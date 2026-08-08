package controller

import (
	"context"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// buildAccessSnapshot resolves the effective plan authorization from one
// routing snapshot. The snapshot is always time-effective: bindings are
// filtered by their start/expiry window and exceptions by expiry.
func (s *Server) buildAccessSnapshot(ctx context.Context, data store.FullRoutingConfig) (*core.EffectiveAccessSnapshot, error) {
	return core.BuildEffectiveAccessSnapshot(core.EffectiveAccessInput{
		Users:             data.Users,
		Bindings:          data.PlanBindings,
		Plans:             data.SubscriptionPlans,
		PlanNodes:         data.ActivePlanNodes,
		Exceptions:        data.UserNodeExceptions,
		Paths:             data.ProxyPaths,
		Steps:             data.ProxyPathSteps,
		Inbounds:          data.Inbounds,
		ExternalOutbounds: data.ExternalOutbounds,
		Now:               time.Now(),
	}), nil
}

// authorizationMode reports the runtime authorization source. The legacy
// authorization tables are removed, so runtime is always plan-based.
func (s *Server) authorizationMode(ctx context.Context) string {
	return "plan"
}

// runtimeAccessBindings resolves the effective plan snapshot as the only
// authorization source for runtime gates (config generation, SSH plans,
// presence, audit, traffic).
func (s *Server) runtimeAccessBindings(ctx context.Context, data store.FullRoutingConfig) ([]model.InboundUser, []model.ProxyPathUser, map[int64]core.UserLimitPolicy, error) {
	snap, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return nil, nil, nil, err
	}
	return snap.InboundUserBindings(), snap.ProxyPathUserBindings(), snap.UserLimitPolicyMap(), nil
}

// defaultUserLimitPolicy derives a policy from the user's own fields when no
// plan binding is effective. Group inheritance is removed.
func defaultUserLimitPolicy(u model.User) core.UserLimitPolicy {
	speed := u.SpeedLimitMbps
	traffic := u.TrafficLimitBytes
	if speed < 0 {
		speed = 0
	}
	if traffic < 0 {
		traffic = 0
	}
	return core.UserLimitPolicy{
		SpeedLimitMbps:    speed,
		TrafficLimitBytes: traffic,
		TrafficResetMode:  normalizeControllerTrafficResetMode(u.TrafficResetMode),
		TrafficResetDay:   normalizeControllerTrafficResetDay(u.TrafficResetDay),
	}
}

// userPlanPolicies resolves the plan-based speed/traffic policy per user.
func (s *Server) userPlanPolicies(ctx context.Context, users []model.User) (map[int64]core.UserLimitPolicy, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return nil, err
	}
	return snap.UserLimitPolicyMap(), nil
}
