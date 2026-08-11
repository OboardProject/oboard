package controller

import (
	"context"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/version"
)

func (s *Server) runScheduledManagedUpdates(ctx context.Context) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil || !automaticUpdateAllowedAt(settings, time.Now()) {
		return
	}
	if settingBool(settings, agentAutoUpdateSetting, false) {
		s.scheduleAgentUpdates(ctx)
	}
	if settingBool(settings, subscriptionRelayAutoUpdateSetting, false) {
		s.scheduleSubscriptionRelayUpdates(ctx)
	}
}

func (s *Server) scheduleAgentUpdates(ctx context.Context) {
	targetBuild := strings.TrimSpace(version.AgentBuild)
	if targetBuild == "" || strings.EqualFold(targetBuild, "dev") {
		return
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return
	}
	versionStamp := time.Now().Unix()
	for index := range servers {
		server := &servers[index]
		if server.Status != model.ServerOnline || strings.TrimSpace(server.AgentID) == "" || !buildNeedsUpdate(server.AgentBuild, targetBuild) {
			continue
		}
		_, _, _ = s.enqueueAgentUpdateWithVersion(ctx, server, model.AgentUpdateRequest{}, versionStamp)
	}
}

func (s *Server) scheduleSubscriptionRelayUpdates(ctx context.Context) {
	targetBuild := strings.TrimSpace(version.Build)
	if targetBuild == "" || strings.EqualFold(targetBuild, "dev") {
		return
	}
	relays, err := s.store.ListSubscriptionRelays(ctx)
	if err != nil {
		return
	}
	for index := range relays {
		relay := &relays[index]
		if relay.TokenHash == "" || relay.UpdateRequestedAt != nil || !buildNeedsUpdate(relay.Build, targetBuild) {
			continue
		}
		_ = s.store.RequestSubscriptionRelayUpdate(ctx, relay.ID, version.Version, targetBuild)
	}
}

func buildNeedsUpdate(current, target string) bool {
	current = strings.TrimSpace(current)
	target = strings.TrimSpace(target)
	if current == "" || target == "" {
		return current == "" && target != ""
	}
	if len(current) == len(target) && isDigits(current) && isDigits(target) {
		return current < target
	}
	return current != target
}
