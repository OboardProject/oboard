package controller

import (
	"context"
	"errors"
	"log"
	"math"
	"net/netip"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// connectionPresencePayloadHorizon bounds how far behind its event a payload
// timestamp may sit. A connection can stay open for days, so this is a sanity
// bound against a corrupt clock, not a freshness check.
const connectionPresencePayloadHorizon = 30 * 24 * time.Hour

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
	// Reuse the immutable routing snapshot; presence deltas never rebuild the
	// full routing state per delta.
	routing, err := s.routingSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	data, snapshot := routing.data, routing.snapshot
	users := routing.usersByID
	devices := routing.devicesByHash
	inbounds := routing.inboundsByID
	paths := routing.pathsByID
	type accessPair struct{ inboundID, userID, pathID int64 }
	allowed := map[accessPair]bool{}
	for _, binding := range snapshot.InboundUserBindings() {
		if binding.Enabled {
			allowed[accessPair{inboundID: binding.InboundID, userID: binding.UserID}] = true
		}
	}
	for _, binding := range snapshot.ProxyPathUserBindings() {
		if binding.Enabled {
			allowed[accessPair{inboundID: binding.InboundID, userID: binding.UserID, pathID: binding.ProxyPathID}] = true
		}
	}
	accepted := make([]model.ConnectionPresenceEvent, 0, len(delta.Events))
	skipped := 0
	skippedReason := ""
	for _, event := range delta.Events {
		if err := validateConnectionPresenceEvent(event, server.ID); err != nil {
			// One malformed event is dropped on its own. Failing the delta
			// discarded up to 500 valid events with it, and the Agent resent the
			// same batch, so a single bad event silenced a server's presence for
			// as long as it kept being produced.
			skipped++
			if skippedReason == "" {
				skippedReason = err.Error()
			}
			continue
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
	if skipped > 0 {
		// A malformed event drains once the Agent prunes it. A sustained stream
		// from one server is the signal that its kernel emits something this
		// Controller does not accept, which the previous whole-batch failure
		// reported only as a rejected delta.
		log.Printf("connection presence skipped %d unusable event(s) from agent=%s server_id=%d first_reason=%s",
			skipped, server.AgentID, server.ID, skippedReason)
	}
	// The write re-checks the effective audit state under the same per-server
	// lock the disabled-state cleanup uses, so a report that passed the gate
	// above cannot land after an administrator turned audit off.
	written := false
	if err := s.withPresenceIngestGuard(ctx, server, func() error {
		written = true
		_, err := s.store.ApplyConnectionPresenceEvents(ctx, server.AgentID, server.ID, delta.DroppedCount, accepted)
		return err
	}); err != nil {
		return nil, err
	}
	if !written {
		return nil, nil
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
		// PayloadLastAt is historical: it is when this connection last moved
		// payload, not when the event was emitted. The kernel re-emits
		// activity_refresh every 30s for as long as the connection is open, so
		// a session that stays open while idle legitimately carries a payload
		// time arbitrarily far behind At. Requiring recency here rejected every
		// refresh for such a connection.
		//
		// Whether presence is *currently* meaningful is decided on read, where
		// connectionAuditMeaningfulPresence applies the TCP 120s / UDP 60s
		// window to this same field. An old payload time is data that answers
		// "not online", not a malformed event. Only an impossible one is
		// refused: ahead of the event, or beyond the retention horizon behind
		// it.
		if event.PayloadLastAt.IsZero() || event.PayloadLastAt.After(event.At.Add(2*time.Minute)) || event.PayloadLastAt.Before(event.At.Add(-connectionPresencePayloadHorizon)) {
			return errors.New("connection presence payload time is invalid")
		}
	} else if !event.PayloadLastAt.IsZero() {
		return errors.New("connection presence payload state is invalid")
	}
	return nil
}
