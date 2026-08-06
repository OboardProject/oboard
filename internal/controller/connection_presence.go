package controller

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

type connectionPresenceDelta struct {
	Events       []model.ConnectionPresenceEvent `json:"events"`
	DroppedCount int64                           `json:"dropped_count"`
}

func (s *Server) acceptConnectionPresenceDelta(ctx context.Context, server *model.Server, delta connectionPresenceDelta) ([]model.ConnectionPresenceEvent, error) {
	if server == nil || !s.effectiveConnectionAuditEnabled(ctx, server) {
		return nil, nil
	}
	if len(delta.Events) > 500 || delta.DroppedCount < 0 || delta.DroppedCount > 1_000_000_000 {
		return nil, errors.New("connection presence batch is invalid")
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	users := make(map[int64]model.User, len(data.Users))
	for _, user := range data.Users {
		users[user.ID] = user
	}
	devices := make(map[string]model.UserDevice, len(data.UserDevices))
	for _, device := range data.UserDevices {
		devices[device.DeviceIDHash] = device
	}
	inbounds := make(map[int64]model.Inbound, len(data.Inbounds))
	for _, inbound := range data.Inbounds {
		inbounds[inbound.ID] = inbound
	}
	paths := make(map[int64]model.ProxyPath, len(data.ProxyPaths))
	for _, path := range data.ProxyPaths {
		paths[path.ID] = path
	}
	type accessPair struct{ inboundID, userID, pathID int64 }
	allowed := map[accessPair]bool{}
	for _, binding := range core.EffectiveInboundUsers(data.Inbounds, data.Users, data.InboundUsers, data.UserGroups, data.UserGroupMembers, data.InboundAccessGrants) {
		if binding.Enabled {
			allowed[accessPair{inboundID: binding.InboundID, userID: binding.UserID}] = true
		}
	}
	for _, binding := range core.EffectiveProxyPathUsers(data.ProxyPaths, data.Inbounds, data.Users, data.InboundUsers, data.UserGroups, data.UserGroupMembers, data.InboundAccessGrants) {
		if binding.Enabled {
			allowed[accessPair{inboundID: binding.InboundID, userID: binding.UserID, pathID: binding.ProxyPathID}] = true
		}
	}
	accepted := make([]model.ConnectionPresenceEvent, 0, len(delta.Events))
	for _, event := range delta.Events {
		if err := validateConnectionPresenceEvent(event, server.ID); err != nil {
			return nil, err
		}
		user, ok := users[event.UserID]
		if !ok || user.Status != "active" {
			continue
		}
		if event.DeviceIDHash == "" {
			if !user.LegacyProxyEnabled {
				continue
			}
		} else {
			device, ok := devices[event.DeviceIDHash]
			if !ok || device.UserID != event.UserID || device.CredentialEpoch != event.CredentialEpoch || device.Status != "active" {
				continue
			}
		}
		inbound, ok := inbounds[event.InboundID]
		if !ok || !inbound.Enabled {
			continue
		}
		if event.PathID > 0 {
			path, ok := paths[event.PathID]
			if !ok || !path.Enabled || path.InboundID != inbound.ID || !core.IsProxyPathAccountingLocation(server.ID, inbound.ID, path.ID, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds) || !allowed[accessPair{inboundID: inbound.ID, userID: event.UserID, pathID: path.ID}] {
				continue
			}
		} else if inbound.ServerID != server.ID || core.ProxyPathRequiresAccountingPathID(inbound.ID, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds) || !allowed[accessPair{inboundID: inbound.ID, userID: event.UserID}] {
			continue
		}
		report := model.ConnectionAuditReport{SourceIP: event.SourceIP}
		s.enrichConnectionAuditReport(&report)
		event.RouteID = s.auditRouteID(event.SourceIP, report.SourceCountryCode, report.SourceISP)
		event.AgentID = server.AgentID
		accepted = append(accepted, event)
	}
	if _, err := s.store.ApplyConnectionPresenceEvents(ctx, server.AgentID, server.ID, delta.DroppedCount, accepted); err != nil {
		return nil, err
	}
	for _, event := range accepted {
		if event.DeviceIDHash != "" && event.Meaningful && !event.PayloadLastAt.IsZero() {
			_ = s.store.MarkUserDeviceProxyActivity(ctx, event.DeviceIDHash, event.PayloadLastAt)
		}
	}
	return accepted, nil
}

func validateConnectionPresenceEvent(event model.ConnectionPresenceEvent, serverID int64) error {
	if event.Sequence == 0 || event.Sequence > math.MaxInt64 || event.ServerID != serverID || event.UserID <= 0 || event.InboundID <= 0 || event.PathID < 0 || event.ActiveConnections < 0 || event.ActiveConnections > 1_000_000 {
		return errors.New("connection presence identity is invalid")
	}
	deviceIDHash := strings.TrimSpace(event.DeviceIDHash)
	if len(deviceIDHash) > 128 || (deviceIDHash == "") != (event.CredentialEpoch == 0) || event.CredentialEpoch < 0 {
		return errors.New("connection presence device identity is invalid")
	}
	sourceIP, err := netip.ParseAddr(strings.TrimSpace(event.SourceIP))
	if err != nil || !sourceIP.IsValid() {
		return errors.New("connection presence source_ip is invalid")
	}
	network := strings.ToLower(strings.TrimSpace(event.Network))
	if network != "tcp" && network != "udp" {
		return errors.New("connection presence network is invalid")
	}
	switch event.Event {
	case "first_authenticated", "first_meaningful_payload", "activity_refresh":
		if event.State != "active" || event.ActiveConnections <= 0 {
			return errors.New("connection presence active state is invalid")
		}
	case "last_connection_closed":
		if event.State != "inactive" || event.ActiveConnections != 0 {
			return errors.New("connection presence close state is invalid")
		}
	case "credential_rejected":
		if event.State != "rejected" || event.ActiveConnections != 0 {
			return errors.New("connection presence rejection state is invalid")
		}
	default:
		return errors.New("connection presence event is invalid")
	}
	now := time.Now().UTC()
	if event.At.IsZero() || event.At.Before(now.Add(-10*time.Minute)) || event.At.After(now.Add(2*time.Minute)) {
		return errors.New("connection presence time is invalid")
	}
	if event.Meaningful {
		if event.PayloadLastAt.IsZero() || event.PayloadLastAt.After(event.At.Add(2*time.Minute)) || event.PayloadLastAt.Before(event.At.Add(-10*time.Minute)) {
			return errors.New("connection presence payload time is invalid")
		}
	} else if !event.PayloadLastAt.IsZero() {
		return errors.New("connection presence payload state is invalid")
	}
	return nil
}
