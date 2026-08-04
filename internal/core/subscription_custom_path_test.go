package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestApplySubscriptionCustomPathPoliciesPrecedence(t *testing.T) {
	users := []model.User{
		{ID: 1, SubscriptionCustomPathPolicy: model.SubscriptionCustomPathInherit},
		{ID: 2, SubscriptionCustomPathPolicy: model.SubscriptionCustomPathAllow},
		{ID: 3, SubscriptionCustomPathPolicy: model.SubscriptionCustomPathDeny},
		{ID: 4, SubscriptionCustomPathPolicy: model.SubscriptionCustomPathInherit},
	}
	groups := []model.UserGroup{
		{ID: 10, Enabled: true, SubscriptionCustomPathPolicy: model.SubscriptionCustomPathAllow},
		{ID: 11, Enabled: true, SubscriptionCustomPathPolicy: model.SubscriptionCustomPathDeny},
		{ID: 12, Enabled: false, SubscriptionCustomPathPolicy: model.SubscriptionCustomPathDeny},
	}
	members := []model.UserGroupMember{
		{UserID: 1, GroupID: 10, Enabled: true},
		{UserID: 1, GroupID: 11, Enabled: true},
		{UserID: 2, GroupID: 11, Enabled: true},
		{UserID: 3, GroupID: 10, Enabled: true},
		{UserID: 4, GroupID: 12, Enabled: true},
	}
	ApplySubscriptionCustomPathPolicies(model.SubscriptionCustomPathSelective, users, groups, members)
	if users[0].SubscriptionCustomPathEnabled || users[0].SubscriptionCustomPathSource != "group_deny" {
		t.Fatalf("deny group did not win: %#v", users[0])
	}
	if !users[1].SubscriptionCustomPathEnabled || users[1].SubscriptionCustomPathSource != "user_allow" {
		t.Fatalf("user allow did not override group: %#v", users[1])
	}
	if users[2].SubscriptionCustomPathEnabled || users[2].SubscriptionCustomPathSource != "user_deny" {
		t.Fatalf("user deny did not override group: %#v", users[2])
	}
	if users[3].SubscriptionCustomPathEnabled || users[3].SubscriptionCustomPathSource != "default_disabled" {
		t.Fatalf("disabled group affected result: %#v", users[3])
	}
	ApplySubscriptionCustomPathPolicies(model.SubscriptionCustomPathEnabled, users, groups, members)
	for _, user := range users {
		if !user.SubscriptionCustomPathEnabled || user.SubscriptionCustomPathSource != "global_enabled" {
			t.Fatalf("global enable did not force user: %#v", user)
		}
	}
	ApplySubscriptionCustomPathPolicies(model.SubscriptionCustomPathDisabled, users, groups, members)
	for _, user := range users {
		if user.SubscriptionCustomPathEnabled || user.SubscriptionCustomPathSource != "global_disabled" {
			t.Fatalf("global disable did not force user: %#v", user)
		}
	}
}

func TestNormalizeSubscriptionCustomPathAlias(t *testing.T) {
	for _, valid := range []string{"abc", "user-01", "a_b"} {
		if got, err := NormalizeSubscriptionCustomPathAlias(valid); err != nil || got != valid {
			t.Fatalf("valid alias %q = %q, %v", valid, got, err)
		}
	}
	for _, invalid := range []string{"ab", "ABC", "-abc", "abc-", "api", "a/b", "用户"} {
		if _, err := NormalizeSubscriptionCustomPathAlias(invalid); err == nil {
			t.Fatalf("invalid alias %q accepted", invalid)
		}
	}
}
