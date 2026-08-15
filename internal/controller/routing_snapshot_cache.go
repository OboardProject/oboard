package controller

import (
	"context"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// routingSnapshotTTL bounds how long a snapshot is reused for time-driven
// authorization state (binding windows, exception expiry). Mutations rebuild
// synchronously through the store-level routing revision; the TTL only covers
// changes that happen by time passing, so connection-audit and presence hot
// paths never rebuild the full routing snapshot per batch or delta.
const routingSnapshotTTL = 60 * time.Second

// routingSnapshot is an immutable, revision-keyed cache entry combining the
// full routing configuration, the effective access snapshot, and the ID maps
// the audit hot paths previously rebuilt on every batch.
type routingSnapshot struct {
	revision uint64
	builtAt  time.Time
	data     store.FullRoutingConfig
	snapshot *core.EffectiveAccessSnapshot

	usersByID       map[int64]model.User
	inboundsByID    map[int64]model.Inbound
	pathsByID       map[int64]model.ProxyPath
	serversByID     map[int64]model.Server
	devicesByHash   map[string]model.UserDevice
	outboundsByID   map[int64]model.Outbound
	externalByID    map[int64]model.ExternalOutbound
	ruleSetsByID    map[int64]model.RoutingRuleSet
	ruleSetsByName  map[string]model.RoutingRuleSet
	warpByServerID  map[int64]model.WARPProfile
	groupsByID      map[int64]model.UserGroup
	membersByID     map[int64]model.UserGroupMember
	dnsListsByID    map[int64]model.DNSList
	dnsPoliciesByID map[int64]model.ServerDNSPolicy
	egressByPathID  map[int64]model.ProxyPathEgressResult
	portAllocByKey  map[string]model.ProxyPathPortAllocation
}

// routingSnapshot returns the current immutable routing snapshot, rebuilding
// it only when the store revision changed or the entry aged past
// routingSnapshotTTL. The database revision is authoritative: any routing
// mutation bumps it in the same transaction as the write.
func (s *Server) routingSnapshot(ctx context.Context) (*routingSnapshot, error) {
	revision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return nil, err
	}
	if current := s.routingSnapshotCache.Load(); current != nil && current.revision == revision && time.Since(current.builtAt) < routingSnapshotTTL {
		return current, nil
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return nil, err
	}
	entry := buildRoutingSnapshot(revision, data, snap)
	s.routingSnapshotCache.Store(entry)
	return entry, nil
}

// invalidateRoutingSnapshot drops the cached entry so the next use rebuilds
// from the database even before the revision changes.
func (s *Server) invalidateRoutingSnapshot() {
	s.routingSnapshotCache.Store(nil)
}

func buildRoutingSnapshot(revision uint64, data store.FullRoutingConfig, snap *core.EffectiveAccessSnapshot) *routingSnapshot {
	entry := &routingSnapshot{
		revision:        revision,
		builtAt:         time.Now(),
		data:            data,
		snapshot:        snap,
		usersByID:       make(map[int64]model.User, len(data.Users)),
		inboundsByID:    make(map[int64]model.Inbound, len(data.Inbounds)),
		pathsByID:       make(map[int64]model.ProxyPath, len(data.ProxyPaths)),
		serversByID:     make(map[int64]model.Server, len(data.Servers)),
		devicesByHash:   make(map[string]model.UserDevice, len(data.UserDevices)),
		outboundsByID:   make(map[int64]model.Outbound, len(data.Outbounds)),
		externalByID:    make(map[int64]model.ExternalOutbound, len(data.ExternalOutbounds)),
		ruleSetsByID:    make(map[int64]model.RoutingRuleSet, len(data.RoutingRuleSets)),
		ruleSetsByName:  make(map[string]model.RoutingRuleSet, len(data.RoutingRuleSets)),
		warpByServerID:  make(map[int64]model.WARPProfile, len(data.WARPProfiles)),
		groupsByID:      make(map[int64]model.UserGroup, len(data.UserGroups)),
		membersByID:     make(map[int64]model.UserGroupMember, len(data.UserGroupMembers)),
		dnsListsByID:    make(map[int64]model.DNSList, len(data.DNSLists)),
		dnsPoliciesByID: make(map[int64]model.ServerDNSPolicy, len(data.ServerDNSPolicies)),
		egressByPathID:  make(map[int64]model.ProxyPathEgressResult, len(data.ProxyPathEgressResults)),
		portAllocByKey:  make(map[string]model.ProxyPathPortAllocation, len(data.ProxyPathPortAllocations)),
	}
	for _, item := range data.Users {
		entry.usersByID[item.ID] = item
	}
	for _, item := range data.Inbounds {
		entry.inboundsByID[item.ID] = item
	}
	for _, item := range data.ProxyPaths {
		entry.pathsByID[item.ID] = item
	}
	for _, item := range data.Servers {
		entry.serversByID[item.ID] = item
	}
	for _, item := range data.UserDevices {
		entry.devicesByHash[item.DeviceIDHash] = item
	}
	for _, item := range data.Outbounds {
		entry.outboundsByID[item.ID] = item
	}
	for _, item := range data.ExternalOutbounds {
		entry.externalByID[item.ID] = item
	}
	for _, item := range data.RoutingRuleSets {
		entry.ruleSetsByID[item.ID] = item
		entry.ruleSetsByName[item.Name] = item
	}
	for _, item := range data.WARPProfiles {
		entry.warpByServerID[item.ServerID] = item
	}
	for _, item := range data.UserGroups {
		entry.groupsByID[item.ID] = item
	}
	for _, item := range data.UserGroupMembers {
		entry.membersByID[item.ID] = item
	}
	for _, item := range data.DNSLists {
		entry.dnsListsByID[item.ID] = item
	}
	for _, item := range data.ServerDNSPolicies {
		entry.dnsPoliciesByID[item.ServerID] = item
	}
	for _, item := range data.ProxyPathEgressResults {
		entry.egressByPathID[item.PathID] = item
	}
	for _, item := range data.ProxyPathPortAllocations {
		entry.portAllocByKey[proxyPathPortAllocationKey(item)] = item
	}
	return entry
}

func proxyPathPortAllocationKey(item model.ProxyPathPortAllocation) string {
	return item.Kind + "\x00" + item.ScopeKey + "\x00" + strconv.FormatInt(item.ServerID, 10)
}
