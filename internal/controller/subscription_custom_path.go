package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const settingSubscriptionCustomPathMode = "subscription_custom_path_mode"

type customSubscriptionCredential struct {
	Alias string
	User  model.User
}

type customSubscriptionCredentialContextKey struct{}

func (s *Server) subscriptionCustomPath(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	alias := strings.TrimPrefix(r.URL.Path, "/s/")
	if strings.Contains(alias, "/") {
		http.NotFound(w, r)
		return
	}
	normalized, err := core.NormalizeSubscriptionCustomPathAlias(alias)
	if err != nil || normalized != alias {
		http.NotFound(w, r)
		return
	}
	user, err := s.store.GetUserBySubscriptionCustomPath(r.Context(), alias)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	state, err := s.subscriptionCustomPathUser(r.Context(), *user)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if !state.SubscriptionCustomPathEnabled || state.SubscriptionCustomPath != alias {
		http.NotFound(w, r)
		return
	}
	request := r.Clone(context.WithValue(r.Context(), customSubscriptionCredentialContextKey{}, customSubscriptionCredential{Alias: alias, User: state}))
	request.URL.Path = "/api/v1/subscriptions/" + alias
	s.subscription(w, request)
}

func (s *Server) selfSubscriptionCustomPath(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	s.manageSubscriptionCustomPath(w, r, user.ID, false)
}

func (s *Server) userSubscriptionCustomPath(w http.ResponseWriter, r *http.Request, userID int64) {
	if userID <= 0 {
		fail(w, errors.New("missing id"), http.StatusBadRequest)
		return
	}
	s.manageSubscriptionCustomPath(w, r, userID, true)
}

func (s *Server) manageSubscriptionCustomPath(w http.ResponseWriter, r *http.Request, userID int64, admin bool) {
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodPut:
		state, err := s.subscriptionCustomPathUser(r.Context(), *user)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		if !state.SubscriptionCustomPathEnabled {
			fail(w, errors.New("custom subscription path is not enabled for this user"), http.StatusForbidden)
			return
		}
		var request struct {
			Alias string `json:"alias"`
		}
		if !decode(w, r, &request) {
			return
		}
		alias, err := core.NormalizeSubscriptionCustomPathAlias(request.Alias)
		if err != nil {
			fail(w, err, http.StatusBadRequest)
			return
		}
		item, err := s.application.SetSubscriptionCustomPath(r.Context(), subscriptionCustomPathPrincipal(r), userID, alias)
		if err != nil {
			if store.IsSubscriptionCustomPathConflict(err) {
				fail(w, errors.New("custom subscription path is already in use"), http.StatusConflict)
				return
			}
			fail(w, err, http.StatusInternalServerError)
			return
		}
		action := "update"
		if !admin {
			action = "self_update"
		}
		auditReq(s, r, action, "subscription-custom-path", fmt.Sprint(userID))
		write(w, http.StatusOK, map[string]any{"subscription_custom_path": item, "user": s.subscriptionCustomPathUserResponse(r.Context(), *user)})
	case http.MethodDelete:
		if err := s.application.DeleteSubscriptionCustomPath(r.Context(), subscriptionCustomPathPrincipal(r), userID); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		auditReq(s, r, "delete", "subscription-custom-path", fmt.Sprint(userID))
		write(w, http.StatusOK, map[string]any{"deleted": true, "user": s.subscriptionCustomPathUserResponse(r.Context(), *user)})
	default:
		method(w)
	}
}

func (s *Server) userSubscriptionCustomPathPolicy(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	if _, err := s.store.GetUser(r.Context(), userID); err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	var request struct {
		Mode model.SubscriptionCustomPathPolicy `json:"mode"`
	}
	if !decode(w, r, &request) {
		return
	}
	if err := core.ValidateSubscriptionCustomPathPolicy(request.Mode); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if err := s.application.SetSubscriptionCustomPathUserPolicy(r.Context(), subscriptionCustomPathPrincipal(r), userID, request.Mode); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	user, _ := s.store.GetUser(r.Context(), userID)
	auditReq(s, r, "update", "subscription-custom-path-policy", fmt.Sprintf("user:%d", userID))
	write(w, http.StatusOK, map[string]any{"user": s.subscriptionCustomPathUserResponse(r.Context(), *user)})
}

func (s *Server) userGroupSubscriptionCustomPathPolicy(w http.ResponseWriter, r *http.Request, groupID int64) {
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	if _, err := s.store.GetUserGroup(r.Context(), groupID); err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	var request struct {
		Mode model.SubscriptionCustomPathPolicy `json:"mode"`
	}
	if !decode(w, r, &request) {
		return
	}
	if err := core.ValidateSubscriptionCustomPathPolicy(request.Mode); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if err := s.application.SetSubscriptionCustomPathGroupPolicy(r.Context(), subscriptionCustomPathPrincipal(r), groupID, request.Mode); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	groups, err := s.subscriptionCustomPathGroups(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	var selected *model.UserGroup
	for index := range groups {
		if groups[index].ID == groupID {
			selected = &groups[index]
			break
		}
	}
	auditReq(s, r, "update", "subscription-custom-path-policy", fmt.Sprintf("group:%d", groupID))
	write(w, http.StatusOK, map[string]any{"user_group": selected})
}

func (s *Server) subscriptionCustomPathUser(ctx context.Context, user model.User) (model.User, error) {
	users := []model.User{user}
	if err := s.enrichSubscriptionCustomPaths(ctx, users, nil, nil); err != nil {
		return model.User{}, err
	}
	return users[0], nil
}

func (s *Server) subscriptionCustomPathUserResponse(ctx context.Context, user model.User) model.User {
	item, err := s.subscriptionCustomPathUser(ctx, user)
	if err == nil {
		return item
	}
	return user
}

func (s *Server) subscriptionCustomPathGroups(ctx context.Context) ([]model.UserGroup, error) {
	groups, err := s.store.ListUserGroups(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.enrichSubscriptionCustomPaths(ctx, nil, groups, nil); err != nil {
		return nil, err
	}
	return groups, nil
}

func (s *Server) enrichSubscriptionCustomPaths(ctx context.Context, users []model.User, groups []model.UserGroup, members []model.UserGroupMember) error {
	if groups == nil {
		var err error
		groups, err = s.store.ListUserGroups(ctx)
		if err != nil {
			return err
		}
	}
	if members == nil {
		var err error
		members, err = s.store.ListUserGroupMembers(ctx)
		if err != nil {
			return err
		}
	}
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
	core.ApplySubscriptionCustomPathPolicies(core.NormalizeSubscriptionCustomPathMode(settings[settingSubscriptionCustomPathMode]), users, groups, members)
	return nil
}

func isSubscriptionCustomCredential(r *http.Request) (customSubscriptionCredential, bool) {
	credential, ok := r.Context().Value(customSubscriptionCredentialContextKey{}).(customSubscriptionCredential)
	return credential, ok
}

func subscriptionCustomPathPrincipal(r *http.Request) application.Principal {
	if user := currentUser(r); user != nil {
		return application.HumanPrincipal(*user, currentRole(r), netip.Addr{})
	}
	return application.Principal{}
}
