package controller

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const userDashboardAuditWindowHours = 24

const (
	userDashboardStatusNormal    = "normal"
	userDashboardStatusAttention = "attention"

	userDashboardReasonAccountInactive       = "account_inactive"
	userDashboardReasonNoActivePlan          = "no_active_plan"
	userDashboardReasonSubscriptionSuspended = "subscription_suspended"
	userDashboardReasonQuotaExceeded         = "quota_exceeded"
	userDashboardReasonAuditRisk              = "audit_risk"
)

type userDashboardTraffic struct {
	UsedBytes  int64  `json:"used_bytes"`
	LimitBytes int64  `json:"limit_bytes"`
	QuotaState string `json:"quota_state"`
	PeriodEnd string `json:"period_end,omitempty"`
}

type userDashboardAudit struct {
	Enabled bool `json:"enabled"`
	Risk    bool `json:"risk"`
}

type userDashboardOverview struct {
	AssignedNodeCount int                  `json:"assigned_node_count"`
	AccountStatus     string               `json:"account_status"`
	StatusReasons     []string             `json:"status_reasons"`
	HasActivePlan     bool                 `json:"has_active_plan"`
	Traffic           userDashboardTraffic `json:"traffic"`
	Audit             userDashboardAudit   `json:"audit"`
}

func (s *Server) userDashboardOverview(ctx context.Context, user model.User) (userDashboardOverview, error) {
	overview := userDashboardOverview{AccountStatus: userDashboardStatusNormal, StatusReasons: []string{}}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return overview, err
	}
	snapshot, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return overview, err
	}
	overview.AssignedNodeCount = len(snapshot.EffectiveNodeKeys(user.ID))
	overview.HasActivePlan = hasEffectiveDashboardPlan(data.PlanBindings, data.SubscriptionPlans, user.ID)

	limit, ok := snapshot.UserLimitPolicyMap()[user.ID]
	if !ok {
		limit = defaultUserLimitPolicy(user)
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return overview, err
	}
	periodKey, start, end, err := s.resolvedTrafficWindow(ctx, user.ID, time.Now(), limit, trafficLocation(settings))
	if err != nil {
		return overview, err
	}
	period, err := s.store.EnsureTrafficPeriod(ctx, user.ID, periodKey, start, end, limit.TrafficLimitBytes)
	if err != nil {
		return overview, err
	}
	overview.Traffic = userDashboardTraffic{
		UsedBytes:  max(int64(0), period.Upload+period.Download),
		LimitBytes: max(int64(0), limit.TrafficLimitBytes),
		QuotaState: period.State,
		PeriodEnd:  period.EndsAt.UTC().Format(time.RFC3339Nano),
	}

	overview.Audit, err = s.userDashboardAudit(ctx, user.ID)
	if err != nil {
		return overview, err
	}
	overview.StatusReasons = userDashboardStatusReasons(user, overview)
	if len(overview.StatusReasons) > 0 {
		overview.AccountStatus = userDashboardStatusAttention
	}
	return overview, nil
}

func hasEffectiveDashboardPlan(bindings []model.UserPlanBinding, plans []model.SubscriptionPlan, userID int64) bool {
	planByID := make(map[int64]model.SubscriptionPlan, len(plans))
	for _, plan := range plans {
		planByID[plan.ID] = plan
	}
	for _, binding := range bindings {
		if binding.UserID != userID || !binding.Enabled {
			continue
		}
		plan, ok := planByID[binding.PlanID]
		if ok && plan.Enabled && plan.CurrentRevisionID > 0 {
			return true
		}
	}
	return false
}

func (s *Server) userDashboardAudit(ctx context.Context, userID int64) (userDashboardAudit, error) {
	state := s.auditSettingsState(ctx)
	connectionEnabled := state.Enabled && state.Connection
	subscriptionEnabled := state.Enabled && state.Subscription
	audit := userDashboardAudit{Enabled: connectionEnabled || subscriptionEnabled}
	if !audit.Enabled {
		return audit, nil
	}
	now := time.Now().UTC()
	connectionScore, subscriptionScore := 0, 0
	if connectionEnabled {
		connection, err := s.store.ConnectionAuditUserRisk(ctx, userID, userDashboardAuditWindowHours, s.auditPolicy(ctx), now)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return audit, err
		}
		if connection != nil {
			connectionScore = connection.RiskScore
		}
	}
	if subscriptionEnabled {
		subscription, _, err := s.store.SubscriptionAuditCurrentRisk(ctx, userID, now, s.auditPolicy(ctx))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return audit, err
		}
		if err == nil {
			subscriptionScore = subscription.Score
		}
	}
	audit.Risk = userDashboardAuditRisk(connectionScore, subscriptionScore)
	return audit, nil
}

func userDashboardAuditRisk(connectionScore, subscriptionScore int) bool {
	higher, lower := max(connectionScore, subscriptionScore), min(connectionScore, subscriptionScore)
	return min(100, higher+int(0.20*float64(lower))) >= 55
}

func userDashboardStatusReasons(user model.User, overview userDashboardOverview) []string {
	reasons := make([]string, 0, 5)
	if user.Status != "active" {
		reasons = append(reasons, userDashboardReasonAccountInactive)
	}
	if !overview.HasActivePlan {
		reasons = append(reasons, userDashboardReasonNoActivePlan)
	}
	if user.SubscriptionSuspended {
		reasons = append(reasons, userDashboardReasonSubscriptionSuspended)
	}
	if overview.Traffic.QuotaState == "quota_exceeded" {
		reasons = append(reasons, userDashboardReasonQuotaExceeded)
	}
	if overview.Audit.Risk {
		reasons = append(reasons, userDashboardReasonAuditRisk)
	}
	return reasons
}
