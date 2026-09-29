package core

import (
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestPlanResetAlignmentFollowsEffectiveTrafficLimit(t *testing.T) {
	anchor := time.Date(2026, 10, 14, 14, 11, 0, 0, time.UTC)
	binding := model.UserPlanBinding{TrafficResetAnchorAt: &anchor, TrafficResetHourAligned: true}
	plan := &model.SubscriptionPlan{TrafficLimitBytes: 1000, TrafficResetMode: model.TrafficResetAnniversaryMonth}
	user := model.User{TrafficLimitBytes: 0}
	policy := effectiveUserPolicy(user, plan, binding)
	if !policy.TrafficResetHourAligned || policy.TrafficResetAnchor != anchor {
		t.Fatalf("plan policy = %#v", policy)
	}
	user.TrafficLimitBytes = 500
	policy = effectiveUserPolicy(user, plan, binding)
	if policy.TrafficResetHourAligned || !policy.TrafficResetAnchor.IsZero() {
		t.Fatalf("user override policy = %#v", policy)
	}
}
