package controller

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const authorizationLeaseDuration = 5 * time.Minute

type credentialScopeKey struct {
	userID, inboundID, pathID, epoch int64
	device                           string
	protocol                         model.Protocol
}

func proxyScopeKey(c model.ProxyCredential) credentialScopeKey {
	return credentialScopeKey{c.UserID, c.InboundID, c.PathID, c.CredentialEpoch, c.DeviceIDHash, c.Protocol}
}

func credentialOptions(data store.FullRoutingConfig, snap *core.EffectiveAccessSnapshot) core.ConfigOptions {
	return core.ConfigOptions{Servers: data.Servers, Inbounds: data.Inbounds, ProxyPaths: data.ProxyPaths, ProxyPathSteps: data.ProxyPathSteps,
		InboundUsers: snap.InboundUserBindings(), ProxyPathUsers: snap.ProxyPathUserBindings(), AccessSnapshot: snap, UserDevices: data.UserDevices}
}

func (s *Server) InitializeProxyCredentials(ctx context.Context) error {
	return s.reconcileProxyCredentials(ctx)
}

// Allocation belongs to approved authorization workflows, never reporting or rendering.
func (s *Server) reconcileProxyCredentials(ctx context.Context) error {
	s.proxyCredentialMu.Lock()
	defer s.proxyCredentialMu.Unlock()
	revision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return err
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return err
	}
	snap, err := s.buildAccessSnapshot(ctx, data)
	if err != nil {
		return err
	}
	desired := core.ProxyCredentialScopes(data.Users, data.UserDevices, data.Inbounds, credentialOptions(data, snap))
	changes, err := s.store.ListAccessChangesByStatus(ctx, model.AccessChangePreparing, model.AccessChangeActivating, model.AccessChangeFinalizing)
	if err != nil {
		return err
	}
	for _, change := range changes {
		var projection core.AccessProjection
		if err := json.Unmarshal([]byte(change.PrepareProjectionJSON), &projection); err != nil {
			return err
		}
		prepared := core.ProjectionSnapshot(projection, data.Users)
		desired = append(desired, core.ProxyCredentialScopes(data.Users, data.UserDevices, data.Inbounds, credentialOptions(data, prepared))...)
	}
	if err := s.store.ReconcileProxyCredentials(ctx, s.sessionSecret, desired); err != nil {
		return err
	}
	s.proxyCredentialRevision.Store(revision)
	s.invalidateRoutingSnapshot()
	return nil
}

func (s *Server) loadProxyCredentialData(ctx context.Context, data store.FullRoutingConfig) (store.FullRoutingConfig, error) {
	users, err := s.store.LoadProxyCredentials(ctx, s.sessionSecret, data.Users)
	if err != nil {
		return store.FullRoutingConfig{}, err
	}
	data.Users = users
	return data, nil
}

func snapshotAt(data store.FullRoutingConfig, at time.Time) *core.EffectiveAccessSnapshot {
	return core.BuildEffectiveAccessSnapshot(core.EffectiveAccessInput{Users: data.Users, Bindings: data.PlanBindings, Plans: data.SubscriptionPlans,
		PlanNodes: data.ActivePlanNodes, Exceptions: data.UserNodeExceptions, Paths: data.ProxyPaths, Steps: data.ProxyPathSteps,
		Inbounds: data.Inbounds, ExternalOutbounds: data.ExternalOutbounds, Now: at})
}

// Capture the revision on both sides: an old response cannot claim a new mutation's revision.
func (s *Server) currentAuthorizationLease(ctx context.Context, serverID int64) (*model.AuthorizationLease, error) {
	for attempt := 0; attempt < 3; attempt++ {
		issued := time.Now().UTC()
		before, err := s.store.RoutingCacheRevision(ctx)
		if err != nil {
			return nil, err
		}
		routing, err := s.routingSnapshot(ctx)
		if err != nil {
			return nil, err
		}
		credentials, err := s.store.ListProxyCredentials(ctx)
		if err != nil {
			return nil, err
		}
		after, err := s.store.RoutingCacheRevision(ctx)
		if err != nil {
			return nil, err
		}
		if before != after || routing.revision != before {
			continue
		}
		return buildAuthorizationLease(int64(after)+1, issued, serverID, routing.data, credentials), nil
	}
	return nil, errors.New("authorization changed while issuing lease; retry required")
}

func buildAuthorizationLease(revision int64, at time.Time, serverID int64, data store.FullRoutingConfig, credentials []model.ProxyCredential) *model.AuthorizationLease {
	lease := &model.AuthorizationLease{Revision: revision, IssuedAt: at.UTC().Format(time.RFC3339Nano), Grants: map[string]string{}}
	snap := snapshotAt(data, at)
	scopes := core.ProxyCredentialScopes(data.Users, data.UserDevices, data.Inbounds, credentialOptions(data, snap))
	allowed := make(map[credentialScopeKey]time.Time, len(scopes))
	for _, scope := range scopes {
		allowed[proxyScopeKey(scope)] = at.Add(authorizationLeaseDuration)
	}
	// Evaluate each subject's time boundaries so a redundant grant's expiry does
	// not disconnect a route that another plan/exception still authorizes.
	for _, user := range data.Users {
		events := map[time.Time]bool{}
		consider := func(t *time.Time) {
			if t != nil && t.After(at) && t.Before(at.Add(authorizationLeaseDuration)) {
				events[t.UTC()] = true
			}
		}
		for _, binding := range data.PlanBindings {
			if binding.UserID == user.ID {
				consider(binding.StartsAt)
				consider(binding.ExpiresAt)
			}
		}
		for _, ex := range data.UserNodeExceptions {
			if ex.UserID == user.ID {
				consider(ex.StartsAt)
				consider(ex.ExpiresAt)
			}
		}
		times := make([]time.Time, 0, len(events))
		for event := range events {
			times = append(times, event)
		}
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		for _, event := range times {
			single := data
			single.Users = []model.User{user}
			future := snapshotAt(single, event)
			remaining := map[credentialScopeKey]bool{}
			for _, scope := range core.ProxyCredentialScopes(single.Users, single.UserDevices, single.Inbounds, credentialOptions(single, future)) {
				remaining[proxyScopeKey(scope)] = true
			}
			for key, end := range allowed {
				if key.userID == user.ID && event.Before(end) && !remaining[key] {
					allowed[key] = event
				}
			}
		}
	}
	serversByScope := map[[2]int64]bool{}
	resolved := map[[2]int64]bool{}
	for _, c := range credentials {
		if c.Status != "active" || c.ID == "" {
			continue
		}
		deadline, ok := allowed[proxyScopeKey(c)]
		if !ok {
			continue
		}
		route := [2]int64{c.InboundID, c.PathID}
		if !resolved[route] {
			nodeType, nodeID := model.AssignableNodeInbound, c.InboundID
			for _, path := range data.ProxyPaths {
				if path.Enabled && path.ID == c.PathID && path.InboundID == c.InboundID {
					nodeType, nodeID = model.AssignableNodeProxyPath, path.ID
					break
				}
			}
			servers, _, _ := core.AffectedAuthServers(map[string]bool{core.NodeKeyOf(nodeType, nodeID): true}, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, nil)
			for _, id := range servers {
				if id == serverID {
					serversByScope[route] = true
				}
			}
			resolved[route] = true
		}
		if serversByScope[route] {
			lease.Grants[c.ID] = deadline.UTC().Format(time.RFC3339Nano)
		}
	}
	return lease
}

func (s *Server) attachAuthorizationLease(ctx context.Context, serverID int64, config string) (string, error) {
	lease, err := s.currentAuthorizationLease(ctx, serverID)
	if err != nil {
		return "", err
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(config), &root); err != nil {
		return "", err
	}
	metadata, _ := root["_oboard"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		root["_oboard"] = metadata
	}
	metadata["authorization"] = lease
	encoded, err := json.MarshalIndent(root, "", "  ")
	return string(encoded), err
}
