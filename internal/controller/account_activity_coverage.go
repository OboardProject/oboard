package controller

import (
	"context"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) recordAccountActivityExpectations(ctx context.Context, now time.Time) error {
	if !s.auditSettingsState(ctx).Enabled {
		return nil
	}
	routing, err := s.routingSnapshot(ctx)
	if err != nil {
		return err
	}
	type key struct {
		user, node int64
		stream     string
	}
	expected := make(map[key]bool)
	for pair := range routing.allowedAccessPairs() {
		user, exists := routing.usersByID[pair.userID]
		if !exists || user.Status != "active" {
			continue
		}
		inbound, exists := routing.inboundsByID[pair.inboundID]
		if !exists || !inbound.Enabled {
			continue
		}
		stream := "kernel"
		if inbound.Protocol == "ssh" {
			stream = "ssh"
		}
		for _, server := range routing.data.Servers {
			location := server.ID == inbound.ServerID
			if pair.pathID > 0 {
				location = core.IsProxyPathAccountingLocation(server.ID, inbound.ID, pair.pathID, routing.data.ProxyPaths, routing.data.ProxyPathSteps, routing.data.Inbounds)
			} else if core.ProxyPathRequiresAccountingPathID(inbound.ID, routing.data.ProxyPaths, routing.data.ProxyPathSteps, routing.data.Inbounds) {
				continue
			}
			if !location {
				continue
			}
			supported := false
			for _, capability := range server.KernelCapabilities {
				if capability == "account_activity_v1" {
					supported = true
				}
			}
			supported = supported && s.effectiveConnectionAuditEnabled(ctx, &server)
			k := key{pair.userID, server.ID, stream}
			if _, exists := expected[k]; !exists && len(expected) >= 16384 {
				return store.ErrAccountActivityCapacity
			}
			expected[k] = supported
		}
	}
	rows := make([]store.AccountActivityExpected, 0, len(expected))
	for k, supported := range expected {
		rows = append(rows, store.AccountActivityExpected{AccountID: k.user, ServerID: k.node, StreamType: k.stream, MinuteUnix: now.Truncate(time.Minute).Unix(), CapabilitySupported: supported})
	}
	return s.store.SaveAccountActivityExpected(ctx, rows, now)
}
