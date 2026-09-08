package controller

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const auditOverviewTTL = 5 * time.Second

type auditOverviewEntry struct {
	connection   model.ConnectionAuditOverview
	subscription model.SubscriptionAuditOverview
	combined     model.CombinedAuditOverview
	builtAt      time.Time
}

type auditOverviewKey struct {
	hours             int
	policy            string
	connectionEnabled bool
	routingRevision   uint64
}

type auditOverviewCache struct {
	mu      sync.Mutex
	entries map[auditOverviewKey]auditOverviewEntry
}

// This cache serves display summaries only. Enforcement and user detail reads
// continue to evaluate current evidence independently.
func (s *Server) auditOverviewData(ctx context.Context, hours int) (model.ConnectionAuditOverview, model.SubscriptionAuditOverview, model.CombinedAuditOverview, error) {
	if hours < 1 {
		hours = 24
	}
	if hours > 30*24 {
		hours = 30 * 24
	}
	c := &s.auditOverviews
	c.mu.Lock()
	defer c.mu.Unlock()
	policy, _ := json.Marshal(s.auditPolicy(ctx))
	revision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return model.ConnectionAuditOverview{}, model.SubscriptionAuditOverview{}, model.CombinedAuditOverview{}, err
	}
	key := auditOverviewKey{hours: hours, policy: string(policy), connectionEnabled: s.connectionAuditEnabled(ctx), routingRevision: revision}
	now := time.Now()
	if entry, ok := c.entries[key]; ok && now.Sub(entry.builtAt) >= 0 && now.Sub(entry.builtAt) < auditOverviewTTL {
		return entry.connection, entry.subscription, entry.combined, nil
	}
	connection, subscription, combined, err := s.buildAuditOverviewData(ctx, hours)
	if err != nil {
		return connection, subscription, combined, err
	}
	if len(c.entries) >= 8 {
		c.entries = nil
	}
	if c.entries == nil {
		c.entries = make(map[auditOverviewKey]auditOverviewEntry)
	}
	for key, entry := range c.entries {
		if now.Sub(entry.builtAt) >= auditOverviewTTL {
			delete(c.entries, key)
		}
	}
	c.entries[key] = auditOverviewEntry{connection: connection, subscription: subscription, combined: combined, builtAt: now}
	return connection, subscription, combined, nil
}
