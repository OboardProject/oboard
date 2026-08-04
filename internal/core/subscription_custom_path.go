package core

import (
	"errors"
	"regexp"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

var subscriptionCustomPathPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,62}[a-z0-9]$`)

var reservedSubscriptionCustomPaths = map[string]struct{}{
	"admin": {}, "api": {}, "assets": {}, "downloads": {}, "healthz": {},
	"install": {}, "login": {}, "logout": {}, "mcp": {}, "oauth": {},
}

func NormalizeSubscriptionCustomPathAlias(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !subscriptionCustomPathPattern.MatchString(value) {
		return "", errors.New("custom subscription path must be 3-64 lowercase letters, numbers, - or _, and start and end with a letter or number")
	}
	if _, reserved := reservedSubscriptionCustomPaths[value]; reserved {
		return "", errors.New("custom subscription path is reserved")
	}
	return value, nil
}

func NormalizeSubscriptionCustomPathMode(value string) model.SubscriptionCustomPathMode {
	switch model.SubscriptionCustomPathMode(strings.ToLower(strings.TrimSpace(value))) {
	case model.SubscriptionCustomPathEnabled:
		return model.SubscriptionCustomPathEnabled
	case model.SubscriptionCustomPathSelective:
		return model.SubscriptionCustomPathSelective
	default:
		return model.SubscriptionCustomPathDisabled
	}
}

func ValidateSubscriptionCustomPathPolicy(value model.SubscriptionCustomPathPolicy) error {
	switch value {
	case model.SubscriptionCustomPathInherit, model.SubscriptionCustomPathAllow, model.SubscriptionCustomPathDeny:
		return nil
	default:
		return errors.New("custom subscription path policy must be inherit, allow or deny")
	}
}

func ApplySubscriptionCustomPathPolicies(mode model.SubscriptionCustomPathMode, users []model.User, groups []model.UserGroup, members []model.UserGroupMember) {
	groupByID := make(map[int64]model.UserGroup, len(groups))
	for _, group := range groups {
		if group.Enabled {
			groupByID[group.ID] = group
		}
	}
	for index := range users {
		user := &users[index]
		user.SubscriptionCustomPathEnabled = false
		user.SubscriptionCustomPathSource = "global_disabled"
		if mode == model.SubscriptionCustomPathEnabled {
			user.SubscriptionCustomPathEnabled = true
			user.SubscriptionCustomPathSource = "global_enabled"
			continue
		}
		if mode != model.SubscriptionCustomPathSelective {
			continue
		}
		switch user.SubscriptionCustomPathPolicy {
		case model.SubscriptionCustomPathAllow:
			user.SubscriptionCustomPathEnabled = true
			user.SubscriptionCustomPathSource = "user_allow"
			continue
		case model.SubscriptionCustomPathDeny:
			user.SubscriptionCustomPathSource = "user_deny"
			continue
		}
		allowed := false
		for _, member := range members {
			if !member.Enabled || member.UserID != user.ID {
				continue
			}
			group, ok := groupByID[member.GroupID]
			if !ok {
				continue
			}
			if group.SubscriptionCustomPathPolicy == model.SubscriptionCustomPathDeny {
				user.SubscriptionCustomPathSource = "group_deny"
				allowed = false
				break
			}
			if group.SubscriptionCustomPathPolicy == model.SubscriptionCustomPathAllow {
				allowed = true
			}
		}
		user.SubscriptionCustomPathEnabled = allowed
		if allowed {
			user.SubscriptionCustomPathSource = "group_allow"
		} else if user.SubscriptionCustomPathSource != "group_deny" {
			user.SubscriptionCustomPathSource = "default_disabled"
		}
	}
}
