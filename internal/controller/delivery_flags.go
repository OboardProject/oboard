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

func applyDeliveryFlagsToServer(server *model.Server, flags store.ServerDeliveryFlags) {
	if server == nil {
		return
	}
	server.AuthorizationFastLane = flags.AuthorizationFastLane
	server.RuntimeUsersEnabled = flags.RuntimeUsersEnabled
}
