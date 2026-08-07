package controller

import (
	"context"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// authorizationModeSetting is the runtime authorization source switch:
// legacy (plans are data only), shadow (compare without switching the runtime),
// plan (the effective plan snapshot is the only runtime source).
const authorizationModeSetting = "authorization.mode"

// AuthorizationModeSettingName is exposed to the settings API and UI.
const AuthorizationModeSettingName = "authorization_mode"

func (s *Server) authorizationMode(ctx context.Context) model.AuthorizationMode {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return model.AuthorizationModeLegacy
	}
	switch strings.ToLower(strings.TrimSpace(settings[authorizationModeSetting])) {
	case "shadow":
		return model.AuthorizationModeShadow
	case "plan":
		return model.AuthorizationModePlan
	default:
		return model.AuthorizationModeLegacy
	}
}

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

// runtimeAccessBindings resolves the authorization source for runtime gates
// (config generation, SSH plans, presence, audit, traffic). In plan mode the
// effective plan snapshot is the only source; legacy and shadow keep the
// legacy tables so the runtime stays unchanged until the switch.
func (s *Server) runtimeAccessBindings(ctx context.Context, data store.FullRoutingConfig) ([]model.InboundUser, []model.ProxyPathUser, map[int64]core.UserLimitPolicy, error) {
	if s.authorizationMode(ctx) == model.AuthorizationModePlan {
		snap, err := s.buildAccessSnapshot(ctx, data)
		if err != nil {
			return nil, nil, nil, err
		}
		return snap.InboundUserBindings(), snap.ProxyPathUserBindings(), snap.UserLimitPolicyMap(), nil
	}
	return effectiveInboundUsersForRouting(data), effectiveProxyPathUsersForRouting(data), nil, nil
}

// userPlanPolicies resolves the plan-based speed/traffic policy per user. It
// returns nil in legacy and shadow mode, so callers keep the legacy policy
// resolution.
func (s *Server) userPlanPolicies(ctx context.Context, users []model.User) (map[int64]core.UserLimitPolicy, error) {
	if s.authorizationMode(ctx) != model.AuthorizationModePlan {
		return nil, nil
	}
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
