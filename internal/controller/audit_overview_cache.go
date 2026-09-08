package controller

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	auditOverviewTTL        = 5 * time.Second
	auditOverviewMaxStale   = 2 * time.Minute
	auditOverviewMaxEntries = 8
	auditOverviewMaxBuilds  = 2
)

type auditOverviewValue struct {
	connection   model.ConnectionAuditOverview
	subscription model.SubscriptionAuditOverview
	combined     model.CombinedAuditOverview
}

type auditOverviewKey struct {
	hours             int
	policy            string
	connectionEnabled bool
	routingRevision   uint64
}

type auditOverviewCache struct {
	once  sync.Once
	cache *coalesceCache[auditOverviewKey, auditOverviewValue]
}

func (c *auditOverviewCache) init() {
	c.once.Do(func() {
		c.cache = newCoalesceCache[auditOverviewKey, auditOverviewValue](auditOverviewTTL, auditOverviewMaxEntries, auditOverviewMaxBuilds)
		c.cache.maxStale = auditOverviewMaxStale
	})
}

// This cache serves display summaries only. Enforcement and user detail reads
// continue to evaluate current evidence independently.
//
// TTL is measured from successful build completion so a slow build does not
// expire the moment it finishes. Failed refreshes leave the previous entry's
// expiry unchanged and never forge freshness.
func (s *Server) auditOverviewData(ctx context.Context, hours int) (model.ConnectionAuditOverview, model.SubscriptionAuditOverview, model.CombinedAuditOverview, error) {
	if hours < 1 {
		hours = 24
	}
	if hours > 30*24 {
		hours = 30 * 24
	}
	c := &s.auditOverviews
	c.init()

	policy, _ := json.Marshal(s.auditPolicy(ctx))
	revision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return model.ConnectionAuditOverview{}, model.SubscriptionAuditOverview{}, model.CombinedAuditOverview{}, err
	}
	key := auditOverviewKey{
		hours:             hours,
		policy:            string(policy),
		connectionEnabled: s.connectionAuditEnabled(ctx),
		routingRevision:   revision,
	}
	entry, err := c.cache.getOrBuild(ctx, key, revision, func(buildCtx context.Context) (auditOverviewValue, time.Time, error) {
		dataAsOf := time.Now().UTC()
		connection, subscription, combined, buildErr := s.buildAuditOverviewData(buildCtx, hours)
		if buildErr != nil {
			return auditOverviewValue{}, time.Time{}, buildErr
		}
		connection.GeneratedAt = dataAsOf
		combined.GeneratedAt = dataAsOf
		combined.BuildCompletedAt = time.Time{} // filled on publish
		combined.CacheExpiresAt = time.Time{}
		combined.CacheStatus = "fresh"
		return auditOverviewValue{connection: connection, subscription: subscription, combined: combined}, dataAsOf, nil
	})
	if err != nil {
		return model.ConnectionAuditOverview{}, model.SubscriptionAuditOverview{}, model.CombinedAuditOverview{}, err
	}
	connection := entry.value.connection
	subscription := entry.value.subscription
	combined := entry.value.combined
	combined.GeneratedAt = entry.dataAsOf
	combined.BuildCompletedAt = entry.buildCompletedAt
	combined.CacheExpiresAt = entry.expiresAt
	if time.Now().Before(entry.expiresAt) {
		combined.CacheStatus = "fresh"
	} else {
		combined.CacheStatus = "stale"
	}
	connection.GeneratedAt = entry.dataAsOf
	return connection, subscription, combined, nil
}
