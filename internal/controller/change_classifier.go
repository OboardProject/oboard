package controller

import (
	"context"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// ChangePlan is the single classification of a user or binding change. It does
// not introduce a second permission model: it only chooses which existing
// delivery lanes must run.
type ChangePlan struct {
	Authorization bool
	RuntimeUsers  bool
	TrafficPolicy bool
	CoreConfig    bool
	Reason        string
}

func ClassifyUserChange(before, after model.User) ChangePlan {
	identity := userIdentityChanged(before, after)
	traffic := userTrafficPolicyChanged(before, after)
	if !identity && !traffic {
		return ChangePlan{}
	}
	plan := ChangePlan{Reason: "user_changed"}
	if traffic && !identity {
		plan.TrafficPolicy = true
		plan.Reason = "user_policy_changed"
		return plan
	}
	plan.Authorization = true
	plan.RuntimeUsers = true
	if traffic {
		plan.TrafficPolicy = true
	}
	if before.Status != after.Status {
		plan.Reason = "user_status_changed"
		return plan
	}
	if before.Username != after.Username {
		plan.Reason = "user_identity_changed"
		return plan
	}
	plan.Reason = "user_credentials_changed"
	return plan
}

func ClassifyUserRemoval() ChangePlan {
	return ChangePlan{Authorization: true, RuntimeUsers: true, Reason: "user_removed"}
}

func ClassifyUserCreated() ChangePlan {
	return ChangePlan{Authorization: true, RuntimeUsers: true, Reason: "user_created"}
}

func ClassifyCredentialRotation() ChangePlan {
	return ChangePlan{Authorization: true, RuntimeUsers: true, Reason: "credential_rotated"}
}

func (s *Server) applyChangePlan(ctx context.Context, userID int64, plan ChangePlan) {
	s.applyChangePlanOn(ctx, userID, nil, plan)
}

func (s *Server) applyChangePlanOn(ctx context.Context, userID int64, serverIDs []int64, plan ChangePlan) {
	if !plan.Authorization && !plan.RuntimeUsers && !plan.TrafficPolicy && !plan.CoreConfig {
		return
	}
	ids := serverIDs
	if ids == nil && (plan.Authorization || plan.RuntimeUsers || plan.TrafficPolicy) {
		var err error
		ids, err = s.userAccountingServerIDs(ctx, userID)
		if err != nil {
			logConfigurationError("user accounting servers", err)
			ids = nil
		}
	}
	if plan.Authorization || plan.RuntimeUsers {
		s.invalidateAccessServers(ctx, ids)
	}
	if plan.TrafficPolicy {
		if err := s.queueApplyTrafficPolicy(ctx, ids, plan.Reason, map[int64]bool{userID: true}); err != nil {
			logConfigurationError("queue traffic policy", err)
		}
	}
	if plan.CoreConfig {
		if serverIDs != nil {
			if err := s.queueCoreConfigRefreshForServers(ctx, serverIDs, plan.Reason); err != nil {
				logConfigurationError("queue core config for user change", err)
			}
			return
		}
		if err := s.queueCoreConfigRefreshForUser(ctx, userID, plan.Reason); err != nil {
			logConfigurationError("queue core config for user change", err)
		}
		return
	}
	if plan.RuntimeUsers {
		s.queueRuntimeUsersFallbackIfNeededOn(ctx, userID, ids, plan.Reason)
	}
}

func (s *Server) queueRuntimeUsersFallbackIfNeeded(ctx context.Context, userID int64, reason string) {
	s.queueRuntimeUsersFallbackIfNeededOn(ctx, userID, nil, reason)
}

func (s *Server) queueRuntimeUsersFallbackIfNeededOn(ctx context.Context, userID int64, serverIDs []int64, reason string) {
	if serverIDs == nil {
		var err error
		serverIDs, err = s.userAccountingServerIDs(ctx, userID)
		if err != nil {
			logConfigurationError("user accounting servers", err)
			return
		}
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return
	}
	restart := make([]int64, 0)
	for _, serverID := range serverIDs {
		server, ok := serverByID(data.Servers, serverID)
		if !ok {
			continue
		}
		if !s.runtimeUsersLaneEnabled(ctx, server) {
			restart = append(restart, serverID)
			continue
		}
		needsRestart := false
		for _, inbound := range data.Inbounds {
			if inbound.ServerID != serverID || !inbound.Enabled {
				continue
			}
			if inboundNeedsCoreConfigFallback(server, inbound) {
				needsRestart = true
				break
			}
		}
		if needsRestart {
			restart = append(restart, serverID)
		}
	}
	if len(restart) == 0 {
		return
	}
	if err := s.queueCoreConfigRefreshForServers(ctx, restart, reason+"_fallback"); err != nil {
		logConfigurationError("queue core config fallback", err)
	}
}

func inboundNeedsCoreConfigFallback(server model.Server, inbound model.Inbound) bool {
	switch inbound.Protocol {
	case model.ProtocolSSH:
		// Restricted SSH stays Agent-native: the signed ssh_inbounds plan still
		// needs apply_core_config, but that path does not restart the kernel.
		return true
	case model.ProtocolVLESS, model.ProtocolHY2:
		return !core.ServerSupportsRuntimeUserProtocol(server, inbound.Protocol)
	case model.ProtocolSS:
		if !core.ServerSupportsRuntimeUserProtocol(server, inbound.Protocol) {
			return true
		}
		return !core.ProtocolSupportsRuntimeUsers(inbound.Protocol, inbound)
	case model.ProtocolSnell:
		return !core.SnellSharedPort(inbound) || !core.ServerSupportsSnellShared(server, inbound)
	default:
		return true
	}
}

func (s *Server) retryServerDelivery(ctx context.Context, serverID int64) error {
	if serverID <= 0 {
		return nil
	}
	_ = s.store.MarkAuthorizationPending(ctx, serverID, "delivering", "", true)
	_ = s.store.MarkRuntimeUsersPending(ctx, serverID, "delivering", "", true)
	s.wakeAuthorizationSync()
	s.wakeRuntimeUsersSync()
	return nil
}
