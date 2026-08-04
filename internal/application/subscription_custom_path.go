package application

import (
	"context"
	"database/sql"
	"errors"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

const SubscriptionCustomPathModeSetting = "subscription_custom_path_mode"

func (s *Service) EnrichSubscriptionCustomPaths(ctx context.Context, users []model.User, groups []model.UserGroup, members []model.UserGroupMember) error {
	userPolicies, groupPolicies, err := s.store.SubscriptionCustomPathPolicies(ctx)
	if err != nil {
		return err
	}
	for index := range groups {
		groups[index].SubscriptionCustomPathPolicy = model.SubscriptionCustomPathInherit
		if policy, ok := groupPolicies[groups[index].ID]; ok {
			groups[index].SubscriptionCustomPathPolicy = policy
		}
	}
	paths, err := s.store.ListSubscriptionCustomPaths(ctx)
	if err != nil {
		return err
	}
	pathByUser := make(map[int64]string, len(paths))
	for _, item := range paths {
		pathByUser[item.UserID] = item.Alias
	}
	for index := range users {
		users[index].SubscriptionCustomPathPolicy = model.SubscriptionCustomPathInherit
		if policy, ok := userPolicies[users[index].ID]; ok {
			users[index].SubscriptionCustomPathPolicy = policy
		}
		users[index].SubscriptionCustomPath = pathByUser[users[index].ID]
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return err
	}
	core.ApplySubscriptionCustomPathPolicies(core.NormalizeSubscriptionCustomPathMode(settings[SubscriptionCustomPathModeSetting]), users, groups, members)
	return nil
}

func (s *Service) SubscriptionCustomPathUser(ctx context.Context, principal Principal, userID int64) (model.User, error) {
	if userID <= 0 || !principal.AllowsInt64("user_ids", userID) {
		return model.User{}, sql.ErrNoRows
	}
	user, err := s.store.GetUser(ctx, userID)
	if err != nil {
		return model.User{}, err
	}
	groups, err := s.store.ListUserGroups(ctx)
	if err != nil {
		return model.User{}, err
	}
	members, err := s.store.ListUserGroupMembers(ctx)
	if err != nil {
		return model.User{}, err
	}
	users := []model.User{*user}
	if err := s.EnrichSubscriptionCustomPaths(ctx, users, groups, members); err != nil {
		return model.User{}, err
	}
	return users[0], nil
}

func (s *Service) SetSubscriptionCustomPath(ctx context.Context, principal Principal, userID int64, alias string) (*model.SubscriptionCustomPath, error) {
	state, err := s.SubscriptionCustomPathUser(ctx, principal, userID)
	if err != nil {
		return nil, err
	}
	if !state.SubscriptionCustomPathEnabled {
		return nil, errors.New("custom subscription path is not enabled for this user")
	}
	alias, err = core.NormalizeSubscriptionCustomPathAlias(alias)
	if err != nil {
		return nil, err
	}
	return s.store.SetSubscriptionCustomPath(ctx, userID, alias)
}

func (s *Service) DeleteSubscriptionCustomPath(ctx context.Context, principal Principal, userID int64) error {
	if userID <= 0 || !principal.AllowsInt64("user_ids", userID) {
		return sql.ErrNoRows
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return err
	}
	return s.store.DeleteSubscriptionCustomPath(ctx, userID)
}

func (s *Service) SetSubscriptionCustomPathUserPolicy(ctx context.Context, principal Principal, userID int64, mode model.SubscriptionCustomPathPolicy) error {
	if userID <= 0 || !principal.AllowsInt64("user_ids", userID) {
		return sql.ErrNoRows
	}
	if err := core.ValidateSubscriptionCustomPathPolicy(mode); err != nil {
		return err
	}
	return s.store.SetSubscriptionCustomPathUserPolicy(ctx, userID, mode)
}

func (s *Service) SetSubscriptionCustomPathGroupPolicy(ctx context.Context, principal Principal, groupID int64, mode model.SubscriptionCustomPathPolicy) error {
	if groupID <= 0 || !principal.AllowsInt64("group_ids", groupID) {
		return sql.ErrNoRows
	}
	if err := core.ValidateSubscriptionCustomPathPolicy(mode); err != nil {
		return err
	}
	return s.store.SetSubscriptionCustomPathGroupPolicy(ctx, groupID, mode)
}

func (s *Service) SetSubscriptionCustomPathMode(ctx context.Context, principal Principal, mode model.SubscriptionCustomPathMode) error {
	if !principal.AllowsGlobal() {
		return errors.New("global resource access is required")
	}
	switch mode {
	case model.SubscriptionCustomPathDisabled, model.SubscriptionCustomPathSelective, model.SubscriptionCustomPathEnabled:
		return s.store.SetSetting(ctx, SubscriptionCustomPathModeSetting, string(mode))
	default:
		return errors.New("subscription custom path mode must be disabled, selective or enabled")
	}
}
