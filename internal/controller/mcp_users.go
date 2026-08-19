package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// registerUserAutomationOperations wires the users, user groups, user group
// members, user devices, and session-revocation capabilities of the MCP
// automation layer. All of them are admin-only and mirror the panel's 用户与分组
// behavior including protected bootstrap-admin guards.

func (s *Server) registerUserAutomationOperations() {
	// ---- users.create ----
	s.automation.RegisterValidator("users.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		user, err := s.userCreateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user": automationUserView(user)}, nil
	})
	s.automation.RegisterRevisionResolver("users.create", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("users.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		user, err := s.userCreateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.CreateUser(ctx, &user); err != nil {
			return nil, err
		}
		if user.Role != model.RoleNone {
			groupKey := store.UserGroupSystemUsers
			if user.Role == model.RoleAdmin {
				groupKey = store.UserGroupSystemAdmins
			}
			if err := s.store.AssignUserToBuiltinGroup(ctx, user.ID, groupKey); err != nil {
				return nil, err
			}
		}
		return map[string]any{"user": automationUserView(user)}, nil
	})

	// ---- users.update ----
	s.automation.RegisterValidator("users.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		user, changed, err := s.userUpdateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user": automationUserView(user), "changed_fields": changed}, nil
	})
	s.automation.RegisterRevisionResolver("users.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		_, _, err := s.userUpdateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return s.userAutomationRevision(ctx, input)
	})
	s.automation.Register("users.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		user, changed, err := s.userUpdateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		current, err := s.store.GetUser(ctx, user.ID)
		if err != nil {
			return nil, err
		}
		if err := s.store.UpdateUser(ctx, &user); err != nil {
			return nil, err
		}
		revokeSessions := hasAnyAutomationField(fieldsFromChanged(changed), "password") ||
			(current.Status == "active" && user.Status != "active") ||
			(current.Role != user.Role && roleAllows(current.Role, user.Role))
		if revokeSessions {
			if _, err := s.store.BumpSessionVersion(ctx, user.ID); err != nil {
				return nil, err
			}
		}
		return map[string]any{"user": automationUserView(user), "changed_fields": changed}, nil
	})

	// ---- users.delete ----
	s.automation.RegisterValidator("users.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		user, err := s.userDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user_id": user.ID}, nil
	})
	s.automation.RegisterRevisionResolver("users.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		user, err := s.userDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"user:" + strconv.FormatInt(user.ID, 10): user.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("users.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		user, err := s.userDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteUserGroupMembersForUser(ctx, user.ID); err != nil {
			return nil, err
		}
		if err := s.store.DeleteSubscriptionTokenPolicyForUser(ctx, user.ID); err != nil {
			return nil, err
		}
		if err := s.store.DeleteOneTimeSubscriptionTokensForUser(ctx, user.ID); err != nil {
			return nil, err
		}
		if err := s.store.DeleteSubscriptionAgeForUser(ctx, user.ID); err != nil {
			return nil, err
		}
		if err := s.store.DeleteNotificationDataForUser(ctx, user.ID); err != nil {
			return nil, err
		}
		if err := s.store.Delete(ctx, "users", user.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "user_id": user.ID}, nil
	})

	// ---- users.session_revoke ----
	s.automation.RegisterValidator("users.session_revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		user, err := s.userSessionRevokeCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user_id": user.ID}, nil
	})
	s.automation.RegisterRevisionResolver("users.session_revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		user, err := s.userSessionRevokeCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"user:" + strconv.FormatInt(user.ID, 10): user.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("users.session_revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		user, err := s.userSessionRevokeCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if _, err := s.store.BumpSessionVersion(ctx, user.ID); err != nil {
			return nil, err
		}
		return map[string]any{"session_revoked": true, "user_id": user.ID}, nil
	})

	// ---- user_groups.create ----
	s.automation.RegisterValidator("user_groups.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		group, err := s.userGroupCreateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user_group": automationUserGroupView(group)}, nil
	})
	s.automation.RegisterRevisionResolver("user_groups.create", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("user_groups.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		group, err := s.userGroupCreateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.CreateUserGroup(ctx, &group); err != nil {
			return nil, err
		}
		return map[string]any{"user_group": automationUserGroupView(group)}, nil
	})
	// ---- user_groups.update ----
	s.automation.RegisterValidator("user_groups.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		group, changed, err := s.userGroupUpdateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user_group": automationUserGroupView(group), "changed_fields": changed}, nil
	})
	s.automation.RegisterRevisionResolver("user_groups.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		group, _, err := s.userGroupUpdateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"user_group:" + strconv.FormatInt(group.ID, 10): group.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("user_groups.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		group, changed, err := s.userGroupUpdateAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.UpdateUserGroup(ctx, &group); err != nil {
			return nil, err
		}
		return map[string]any{"user_group": automationUserGroupView(group), "changed_fields": changed}, nil
	})

	// ---- user_groups.delete ----
	s.automation.RegisterValidator("user_groups.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		group, err := s.userGroupDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"group_id": group.ID}, nil
	})
	s.automation.RegisterRevisionResolver("user_groups.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		group, err := s.userGroupDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"user_group:" + strconv.FormatInt(group.ID, 10): group.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("user_groups.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		group, err := s.userGroupDeleteAutomationCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteUserGroupMembersForGroup(ctx, group.ID); err != nil {
			return nil, err
		}
		if err := s.store.Delete(ctx, "user_groups", group.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "group_id": group.ID}, nil
	})

	// ---- user_group_members.set ----
	s.automation.RegisterValidator("user_group_members.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		member, err := s.userGroupMemberSetCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"user_group_member": automationUserGroupMemberView(member)}, nil
	})
	s.automation.RegisterRevisionResolver("user_group_members.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		member, err := s.userGroupMemberSetCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		revisions := map[string]string{
			"user:" + strconv.FormatInt(member.UserID, 10):        member.UpdatedAt.UTC().Format(time.RFC3339Nano),
			"user_group:" + strconv.FormatInt(member.GroupID, 10): member.UpdatedAt.UTC().Format(time.RFC3339Nano),
		}
		return revisions, nil
	})
	s.automation.Register("user_group_members.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		member, err := s.userGroupMemberSetCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		existing, findErr := s.store.GetUserGroupMemberByPair(ctx, member.GroupID, member.UserID)
		switch {
		case findErr == nil:
			existing.Enabled = member.Enabled
			if err := s.store.UpdateUserGroupMember(ctx, existing); err != nil {
				return nil, err
			}
			member = *existing
		case errors.Is(findErr, sql.ErrNoRows):
			if err := s.store.CreateUserGroupMember(ctx, &member); err != nil {
				return nil, err
			}
		default:
			return nil, findErr
		}
		return map[string]any{"user_group_member": automationUserGroupMemberView(member)}, nil
	})

	// ---- user_devices.update ----
	s.automation.RegisterValidator("user_devices.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		device, err := s.userDeviceRenameCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"device": automationUserDeviceView(device)}, nil
	})
	s.automation.RegisterRevisionResolver("user_devices.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		device, err := s.userDeviceRenameCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"user:" + strconv.FormatInt(device.UserID, 10): device.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("user_devices.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, device, err := s.userDeviceRenameInput(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		updated, err := s.store.RenameUserDevice(ctx, request.UserID, request.DeviceID, request.Name)
		if err != nil {
			return nil, err
		}
		_ = device
		return map[string]any{"device": automationUserDeviceView(*updated)}, nil
	})

	// ---- user_devices.revoke ----
	s.automation.RegisterValidator("user_devices.revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		_, device, err := s.userDeviceRevokeInput(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"device": automationUserDeviceView(device), "revoked": true}, nil
	})
	s.automation.RegisterRevisionResolver("user_devices.revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		_, device, err := s.userDeviceRevokeInput(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"user:" + strconv.FormatInt(device.UserID, 10): device.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("user_devices.revoke", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, _, err := s.userDeviceRevokeInput(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		device, err := s.store.RevokeUserDevice(ctx, request.UserID, request.DeviceID)
		if err != nil {
			return nil, err
		}
		if err := s.queueUserDeviceCredentialDeployment(ctx); err != nil {
			return nil, err
		}
		return map[string]any{"device": automationUserDeviceView(*device), "revoked": true}, nil
	})
}

type userCreateAutomationInput struct {
	User struct {
		Username                  string `json:"username"`
		Nickname                  string `json:"nickname"`
		Password                  string `json:"password"`
		Role                      string `json:"role"`
		Status                    string `json:"status"`
		SpeedLimitMbps            int    `json:"speed_limit_mbps"`
		TrafficLimitBytes         int64  `json:"traffic_limit_bytes"`
		TrafficResetMode          string `json:"traffic_reset_mode"`
		TrafficResetDay           int    `json:"traffic_reset_day"`
		DeviceLimit               int    `json:"device_limit"`
		LegacyProxyEnabled        bool   `json:"legacy_proxy_enabled"`
		SubscriptionBurnAfterRead bool   `json:"subscription_burn_after_read"`
	} `json:"user"`
}

func (s *Server) userCreateAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.User, error) {
	var request userCreateAutomationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.User{}, err
	}
	u := model.User{
		Username:                  strings.TrimSpace(request.User.Username),
		Nickname:                  strings.TrimSpace(request.User.Nickname),
		Role:                      model.Role(request.User.Role),
		Status:                    request.User.Status,
		SpeedLimitMbps:            request.User.SpeedLimitMbps,
		TrafficLimitBytes:         request.User.TrafficLimitBytes,
		TrafficResetMode:          request.User.TrafficResetMode,
		TrafficResetDay:           request.User.TrafficResetDay,
		DeviceLimit:               request.User.DeviceLimit,
		LegacyProxyEnabled:        true,
		LegacyProxyEnabledSet:     true,
		SubscriptionBurnAfterRead: request.User.SubscriptionBurnAfterRead,
	}
	if u.Username == "" {
		return model.User{}, errors.New("username required")
	}
	if u.Role == "" {
		u.Role = model.RoleViewer
	}
	if u.Status == "" {
		u.Status = "active"
	}
	password := strings.TrimSpace(request.User.Password)
	if password == "" {
		generated, err := security.RandomToken(12)
		if err != nil {
			return model.User{}, err
		}
		password = generated
	} else if len(password) < 8 {
		return model.User{}, errors.New("password must be at least 8 characters")
	}
	pass, err := security.HashPassword(password)
	if err != nil {
		return model.User{}, err
	}
	u.PasswordHash = pass
	if u.ProxyUUID == "" {
		u.ProxyUUID, err = security.RandomUUID()
		if err != nil {
			return model.User{}, err
		}
	}
	if u.ProxyPassword == "" {
		u.ProxyPassword, err = security.RandomToken(18)
		if err != nil {
			return model.User{}, err
		}
	}
	if u.SubscriptionToken == "" {
		u.SubscriptionToken, err = security.RandomToken(24)
		if err != nil {
			return model.User{}, err
		}
	}
	if err := validateUser(&u); err != nil {
		return model.User{}, err
	}
	return u, nil
}

func (s *Server) userAutomationRevision(ctx context.Context, input json.RawMessage) (map[string]string, error) {
	var request struct {
		UserID int64 `json:"user_id"`
	}
	if err := json.Unmarshal(input, &request); err != nil || request.UserID <= 0 {
		return nil, errors.New("user_id must be a positive integer")
	}
	user, err := s.store.GetUser(ctx, request.UserID)
	if err != nil {
		return nil, err
	}
	return map[string]string{"user:" + strconv.FormatInt(user.ID, 10): user.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
}

var userAutomationChangeFields = map[string]bool{
	"nickname": true, "role": true, "status": true, "password": true,
	"speed_limit_mbps": true, "traffic_limit_bytes": true, "traffic_reset_mode": true,
	"traffic_reset_day": true, "device_limit": true, "legacy_proxy_enabled": true,
	"subscription_burn_after_read": true, "subscription_age_enabled": true,
	"subscription_age_public_key": true,
}

type userUpdateAutomationInput struct {
	UserID  int64           `json:"user_id"`
	Changes json.RawMessage `json:"changes"`
}

func (s *Server) userUpdateAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.User, []string, error) {
	var request userUpdateAutomationInput
	if err := strictAutomationInput(input, &request); err != nil || request.UserID <= 0 {
		return model.User{}, nil, errors.New("user_id must be a positive integer")
	}
	if !principal.AllowsInt64("user_ids", request.UserID) {
		return model.User{}, nil, errors.New("user is outside the authorized user boundary")
	}
	fields, err := decodeClosedAutomationFields(request.Changes, userAutomationChangeFields, "changes")
	if err != nil {
		return model.User{}, nil, err
	}
	if len(fields) == 0 {
		return model.User{}, nil, errors.New("changes must contain at least one user field")
	}
	current, err := s.store.GetUser(ctx, request.UserID)
	if err != nil {
		return model.User{}, nil, err
	}
	protected, err := s.store.IsBootstrapAdmin(ctx, request.UserID)
	if err != nil {
		return model.User{}, nil, err
	}
	u := *current
	changed := make([]string, 0, len(fields))
	if value, ok := fields["nickname"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("nickname: %w", err)
		}
		u.Nickname = strings.TrimSpace(v)
		changed = append(changed, "nickname")
	}
	if value, ok := fields["role"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("role: %w", err)
		}
		u.Role = model.Role(v)
		changed = append(changed, "role")
	}
	if value, ok := fields["status"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("status: %w", err)
		}
		u.Status = v
		changed = append(changed, "status")
	}
	if value, ok := fields["password"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("password: %w", err)
		}
		if len(v) < 8 {
			return model.User{}, nil, errors.New("password must be at least 8 characters")
		}
		h, err := security.HashPassword(v)
		if err != nil {
			return model.User{}, nil, err
		}
		u.PasswordHash = h
		changed = append(changed, "password")
	}
	if value, ok := fields["speed_limit_mbps"]; ok {
		var v int
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("speed_limit_mbps: %w", err)
		}
		u.SpeedLimitMbps = v
		changed = append(changed, "speed_limit_mbps")
	}
	if value, ok := fields["traffic_limit_bytes"]; ok {
		var v int64
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("traffic_limit_bytes: %w", err)
		}
		u.TrafficLimitBytes = v
		changed = append(changed, "traffic_limit_bytes")
	}
	if value, ok := fields["traffic_reset_mode"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("traffic_reset_mode: %w", err)
		}
		u.TrafficResetMode = v
		changed = append(changed, "traffic_reset_mode")
	}
	if value, ok := fields["traffic_reset_day"]; ok {
		var v int
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("traffic_reset_day: %w", err)
		}
		u.TrafficResetDay = v
		changed = append(changed, "traffic_reset_day")
	}
	if value, ok := fields["device_limit"]; ok {
		var v int
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("device_limit: %w", err)
		}
		u.DeviceLimit = v
		changed = append(changed, "device_limit")
	}
	if value, ok := fields["legacy_proxy_enabled"]; ok {
		var v bool
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("legacy_proxy_enabled: %w", err)
		}
		u.LegacyProxyEnabled = v
		u.LegacyProxyEnabledSet = true
		changed = append(changed, "legacy_proxy_enabled")
	}
	if value, ok := fields["subscription_burn_after_read"]; ok {
		var v bool
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("subscription_burn_after_read: %w", err)
		}
		u.SubscriptionBurnAfterRead = v
		changed = append(changed, "subscription_burn_after_read")
	}
	if value, ok := fields["subscription_age_enabled"]; ok {
		var v bool
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("subscription_age_enabled: %w", err)
		}
		u.SubscriptionAgeEnabled = v
		changed = append(changed, "subscription_age_enabled")
	}
	if value, ok := fields["subscription_age_public_key"]; ok {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return model.User{}, nil, fmt.Errorf("subscription_age_public_key: %w", err)
		}
		u.SubscriptionAgePublicKey = strings.TrimSpace(v)
		changed = append(changed, "subscription_age_public_key")
	}
	if protected {
		u.Role = model.RoleAdmin
		u.Status = "active"
	}
	u.ID = request.UserID
	if err := validateUser(&u); err != nil {
		return model.User{}, nil, err
	}
	u.SessionVersion = current.SessionVersion
	return u, changed, nil
}

func (s *Server) userDeleteAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.User, error) {
	var request struct {
		UserID  int64 `json:"user_id"`
		Confirm bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.User{}, err
	}
	if request.UserID <= 0 || !request.Confirm {
		return model.User{}, errors.New("user_id and confirm=true are required")
	}
	if !principal.AllowsInt64("user_ids", request.UserID) {
		return model.User{}, errors.New("user is outside the authorized user boundary")
	}
	protected, err := s.store.IsBootstrapAdmin(ctx, request.UserID)
	if err != nil {
		return model.User{}, err
	}
	if protected {
		return model.User{}, errors.New("初始管理员账号不允许删除")
	}
	user, err := s.store.GetUser(ctx, request.UserID)
	if err != nil {
		return model.User{}, err
	}
	return *user, nil
}

func (s *Server) userSessionRevokeCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.User, error) {
	var request struct {
		UserID int64 `json:"user_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil || request.UserID <= 0 {
		return model.User{}, errors.New("user_id must be a positive integer")
	}
	if !principal.AllowsInt64("user_ids", request.UserID) {
		return model.User{}, errors.New("user is outside the authorized user boundary")
	}
	user, err := s.store.GetUser(ctx, request.UserID)
	if err != nil {
		return model.User{}, err
	}
	return *user, nil
}

type userGroupCreateAutomationInput struct {
	UserGroup struct {
		Name                         string `json:"name"`
		Description                  string `json:"description"`
		Role                         string `json:"role"`
		Enabled                      bool   `json:"enabled"`
		SubscriptionCustomPathPolicy string `json:"subscription_custom_path_policy"`
	} `json:"user_group"`
}

func (s *Server) userGroupCreateAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.UserGroup, error) {
	var request userGroupCreateAutomationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.UserGroup{}, err
	}
	v := model.UserGroup{
		Name:                         strings.TrimSpace(request.UserGroup.Name),
		Description:                  strings.TrimSpace(request.UserGroup.Description),
		Role:                         model.Role(request.UserGroup.Role),
		Enabled:                      request.UserGroup.Enabled,
		SubscriptionCustomPathPolicy: model.SubscriptionCustomPathPolicy(request.UserGroup.SubscriptionCustomPathPolicy),
	}
	if err := validateUserGroup(&v); err != nil {
		return model.UserGroup{}, err
	}
	return v, nil
}

var userGroupAutomationChangeFields = map[string]bool{
	"name": true, "description": true, "role": true, "enabled": true,
	"subscription_custom_path_policy": true,
}

type userGroupUpdateAutomationInput struct {
	GroupID int64           `json:"group_id"`
	Changes json.RawMessage `json:"changes"`
}

func (s *Server) userGroupUpdateAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.UserGroup, []string, error) {
	var request userGroupUpdateAutomationInput
	if err := strictAutomationInput(input, &request); err != nil || request.GroupID <= 0 {
		return model.UserGroup{}, nil, errors.New("group_id must be a positive integer")
	}
	fields, err := decodeClosedAutomationFields(request.Changes, userGroupAutomationChangeFields, "changes")
	if err != nil {
		return model.UserGroup{}, nil, err
	}
	if len(fields) == 0 {
		return model.UserGroup{}, nil, errors.New("changes must contain at least one user group field")
	}
	current, err := s.store.GetUserGroup(ctx, request.GroupID)
	if err != nil {
		return model.UserGroup{}, nil, err
	}
	v := *current
	changed := make([]string, 0, len(fields))
	if value, ok := fields["name"]; ok {
		var name string
		if err := json.Unmarshal(value, &name); err != nil {
			return model.UserGroup{}, nil, fmt.Errorf("name: %w", err)
		}
		v.Name = strings.TrimSpace(name)
		changed = append(changed, "name")
	}
	if value, ok := fields["description"]; ok {
		var description string
		if err := json.Unmarshal(value, &description); err != nil {
			return model.UserGroup{}, nil, fmt.Errorf("description: %w", err)
		}
		v.Description = strings.TrimSpace(description)
		changed = append(changed, "description")
	}
	if value, ok := fields["role"]; ok {
		var role string
		if err := json.Unmarshal(value, &role); err != nil {
			return model.UserGroup{}, nil, fmt.Errorf("role: %w", err)
		}
		v.Role = model.Role(role)
		changed = append(changed, "role")
	}
	if value, ok := fields["enabled"]; ok {
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err != nil {
			return model.UserGroup{}, nil, fmt.Errorf("enabled: %w", err)
		}
		v.Enabled = enabled
		changed = append(changed, "enabled")
	}
	if value, ok := fields["subscription_custom_path_policy"]; ok {
		var policy string
		if err := json.Unmarshal(value, &policy); err != nil {
			return model.UserGroup{}, nil, fmt.Errorf("subscription_custom_path_policy: %w", err)
		}
		v.SubscriptionCustomPathPolicy = model.SubscriptionCustomPathPolicy(policy)
		changed = append(changed, "subscription_custom_path_policy")
	}
	if v.SystemKey == store.UserGroupSystemAdmins {
		v.Role = model.RoleAdmin
		v.Enabled = true
	}
	if err := validateUserGroup(&v); err != nil {
		return model.UserGroup{}, nil, err
	}
	return v, changed, nil
}

func (s *Server) userGroupDeleteAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.UserGroup, error) {
	var request struct {
		GroupID int64 `json:"group_id"`
		Confirm bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.UserGroup{}, err
	}
	if request.GroupID <= 0 || !request.Confirm {
		return model.UserGroup{}, errors.New("group_id and confirm=true are required")
	}
	current, err := s.store.GetUserGroup(ctx, request.GroupID)
	if err != nil {
		return model.UserGroup{}, err
	}
	if current.SystemKey != "" {
		return model.UserGroup{}, errors.New("系统用户组不允许删除")
	}
	return *current, nil
}

type userGroupMemberSetInput struct {
	GroupID int64 `json:"group_id"`
	UserID  int64 `json:"user_id"`
	Enabled bool  `json:"enabled"`
}

func (s *Server) userGroupMemberSetCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.UserGroupMember, error) {
	var request userGroupMemberSetInput
	if err := strictAutomationInput(input, &request); err != nil {
		return model.UserGroupMember{}, err
	}
	if request.GroupID <= 0 || request.UserID <= 0 {
		return model.UserGroupMember{}, errors.New("group_id and user_id must be positive integers")
	}
	if !principal.AllowsInt64("user_ids", request.UserID) {
		return model.UserGroupMember{}, errors.New("user is outside the authorized user boundary")
	}
	member := model.UserGroupMember{GroupID: request.GroupID, UserID: request.UserID, Enabled: request.Enabled}
	if err := s.validateUserGroupMember(ctx, member); err != nil {
		return model.UserGroupMember{}, err
	}
	if existing, err := s.store.GetUserGroupMemberByPair(ctx, request.GroupID, request.UserID); err == nil {
		if protected, err := s.protectedAdminMembership(ctx, *existing); err != nil {
			return model.UserGroupMember{}, err
		} else if protected && !request.Enabled {
			return model.UserGroupMember{}, errors.New("初始管理员必须保留在管理员组中")
		}
	}
	return member, nil
}

type userDeviceInput struct {
	UserID   int64  `json:"user_id"`
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Revoked  bool   `json:"revoked"`
}

func (s *Server) userDeviceBaseCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (userDeviceInput, model.UserDevice, error) {
	var request userDeviceInput
	if err := strictAutomationInput(input, &request); err != nil {
		return request, model.UserDevice{}, err
	}
	if request.UserID <= 0 || strings.TrimSpace(request.DeviceID) == "" {
		return request, model.UserDevice{}, errors.New("user_id and device_id are required")
	}
	if !principal.AllowsInt64("user_ids", request.UserID) {
		return request, model.UserDevice{}, errors.New("user is outside the authorized user boundary")
	}
	device, err := s.store.GetUserDevice(ctx, request.UserID, request.DeviceID)
	if err != nil {
		return request, model.UserDevice{}, err
	}
	return request, *device, nil
}

func (s *Server) userDeviceRenameCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.UserDevice, error) {
	_, device, err := s.userDeviceBaseCandidate(ctx, principal, input)
	return device, err
}

func (s *Server) userDeviceRenameInput(ctx context.Context, principal application.Principal, input json.RawMessage) (userDeviceInput, model.UserDevice, error) {
	request, device, err := s.userDeviceBaseCandidate(ctx, principal, input)
	if err != nil {
		return request, device, err
	}
	if strings.TrimSpace(request.Name) == "" || len([]rune(request.Name)) > 80 {
		return request, device, errors.New("device name must be between 1 and 80 characters")
	}
	return request, device, nil
}

func (s *Server) userDeviceRevokeInput(ctx context.Context, principal application.Principal, input json.RawMessage) (userDeviceInput, model.UserDevice, error) {
	request, device, err := s.userDeviceBaseCandidate(ctx, principal, input)
	if err != nil {
		return request, device, err
	}
	if !request.Revoked {
		return request, device, errors.New("revoked=true is required to revoke a device")
	}
	return request, device, nil
}

func fieldsFromChanged(changed []string) map[string]json.RawMessage {
	fields := map[string]json.RawMessage{}
	for _, name := range changed {
		fields[name] = json.RawMessage(`true`)
	}
	return fields
}

func automationUserView(user model.User) map[string]any {
	return map[string]any{
		"id": user.ID, "revision": user.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"username": user.Username, "nickname": user.Nickname, "role": user.Role, "status": user.Status,
		"speed_limit_mbps": user.SpeedLimitMbps, "traffic_limit_bytes": strconv.FormatInt(user.TrafficLimitBytes, 10),
		"traffic_used_bytes":      strconv.FormatInt(user.TrafficUsedBytes, 10),
		"subscription_configured": user.SubscriptionToken != "", "subscription_age_enabled": user.SubscriptionAgeEnabled,
		"subscription_suspended": user.SubscriptionSuspended, "subscription_suspended_at": user.SubscriptionSuspendedAt,
		"subscription_suspend_reason": user.SubscriptionSuspendReason, "device_limit": user.DeviceLimit,
		"legacy_proxy_enabled": user.LegacyProxyEnabled, "protected": user.Protected,
		"created_at": user.CreatedAt, "updated_at": user.UpdatedAt,
	}
}

func automationUserGroupView(group model.UserGroup) map[string]any {
	return map[string]any{
		"id": group.ID, "revision": group.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"name": group.Name, "description": group.Description, "role": group.Role,
		"system_key": group.SystemKey, "enabled": group.Enabled,
		"subscription_custom_path_policy": group.SubscriptionCustomPathPolicy,
		"created_at":                      group.CreatedAt, "updated_at": group.UpdatedAt,
	}
}

func automationUserGroupMemberView(member model.UserGroupMember) map[string]any {
	return map[string]any{
		"id": member.ID, "group_id": member.GroupID, "user_id": member.UserID, "enabled": member.Enabled,
		"created_at": member.CreatedAt, "updated_at": member.UpdatedAt,
	}
}

func automationUserDeviceView(device model.UserDevice) map[string]any {
	return map[string]any{
		"id": device.ID, "device_id_hash": device.DeviceIDHash, "user_id": device.UserID, "name": device.Name,
		"token_prefix": device.TokenPrefix, "credential_epoch": device.CredentialEpoch, "status": device.Status,
		"subscription_suspended": device.SubscriptionSuspended, "proxy_access_state": device.ProxyAccessState,
		"created_at": device.CreatedAt, "updated_at": device.UpdatedAt,
		"last_subscription_at": device.LastSubscriptionAt, "last_proxy_activity_at": device.LastProxyActivityAt,
		"revoked_at": device.RevokedAt,
	}
}
