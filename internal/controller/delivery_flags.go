package controller

import (
	"context"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) authorizationFastLaneEnabled(ctx context.Context, server model.Server) bool {
	if !serverSupportsAuthorizationControl(server) {
		return false
	}
	if s == nil || s.store == nil {
		return true
	}
	flags, err := s.store.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		return true
	}
	return flags.AuthorizationFastLane
}

func (s *Server) runtimeUsersLaneEnabled(ctx context.Context, server model.Server) bool {
	if !serverSupportsRuntimeUsersLane(server) {
		return false
	}
	if s == nil || s.store == nil {
		return true
	}
	flags, err := s.store.ServerDeliveryFlags(ctx, server.ID)
	if err != nil {
		return true
	}
	return flags.RuntimeUsersEnabled
}

func (s *Server) applyServerDeliveryFlags(ctx context.Context, serverID int64, authorizationFastLane, runtimeUsersEnabled *bool) error {
	if authorizationFastLane == nil && runtimeUsersEnabled == nil {
		return nil
	}
	current, err := s.store.ServerDeliveryFlags(ctx, serverID)
	if err != nil {
		return err
	}
	if authorizationFastLane != nil {
		current.AuthorizationFastLane = *authorizationFastLane
	}
	if runtimeUsersEnabled != nil {
		current.RuntimeUsersEnabled = *runtimeUsersEnabled
	}
	current.ServerID = serverID
	if err := s.store.SetServerDeliveryFlags(ctx, current); err != nil {
		return err
	}
	s.wakeAuthorizationSync()
	s.wakeRuntimeUsersSync()
	if !current.RuntimeUsersEnabled {
		if err := s.queueCoreConfigRefreshForServers(ctx, []int64{serverID}, "runtime_users_disabled"); err != nil {
			return err
		}
	}
	return nil
}

func applyDeliveryFlagsToServer(server *model.Server, flags store.ServerDeliveryFlags) {
	if server == nil {
		return
	}
	server.AuthorizationFastLane = flags.AuthorizationFastLane
	server.RuntimeUsersEnabled = flags.RuntimeUsersEnabled
}
