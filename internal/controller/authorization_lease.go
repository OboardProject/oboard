package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

const authorizationLeaseDuration = 5 * time.Minute

// authorizationLeaseReissueAfter bounds how long an issued lease answers a
// renewal for an unchanged grant set. Every traffic report and every
// authorization poll renews, so reissuing each time cost two write
// transactions per request; the window stays far inside the lease lifetime, so
// the Agent's installed grants never approach their deadlines.
const authorizationLeaseReissueAfter = 30 * time.Second

// authorizationProjectionTTL bounds how long a projection built from one
// routing revision is reused. Time-driven transitions are encoded as intervals
// inside the projection, so the TTL only guards against clock skew in the
// interval arithmetic and keeps memory bounded after a burst of revisions.
const authorizationProjectionTTL = 60 * time.Second

// authorizationMaxEventsPerUser caps the number of future binding/exception
// boundaries evaluated for one user when building the projection.
const authorizationMaxEventsPerUser = 32

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
	s.invalidateAuthorizationProjection()
	s.wakeAuthorizationSync()
	s.wakeRuntimeUsersSync()
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

// authorizationInterval is one half-open window [from, to) during which a
// credential scope is authorized. A zero `to` is open-ended.
type authorizationInterval struct {
	from, to time.Time
}

// authorizationEntry is one active credential with the servers that
// authenticate it and the time windows in which it is authorized.
type authorizationEntry struct {
	key       string
	servers   map[int64]bool
	intervals []authorizationInterval
}

// authorizationProjection is the server-independent grant projection for one
// routing revision. It is computed once per revision and answers every server's
// lease for the projection's lifetime by filtering, so a fleet of N servers
// renewing every 30 seconds costs N cheap filters, not N full snapshot builds.
type authorizationProjection struct {
	routingRevision uint64
	builtAt         time.Time
	entries         []authorizationEntry
	leaseMu         sync.Mutex
	leases          map[int64]*model.AuthorizationLease
}

// serverGrant is the per-server, per-credential outcome at one instant.
type serverGrant struct {
	key      string
	deadline time.Time
	boundary time.Time
}

// grantsAt returns the credentials this server must admit at `at`, each with
// its lease deadline (at + lease duration, clamped to the business boundary).
func (p *authorizationProjection) grantsAt(serverID int64, at time.Time) []serverGrant {
	out := make([]serverGrant, 0)
	for _, entry := range p.entries {
		if !entry.servers[serverID] {
			continue
		}
		for _, interval := range entry.intervals {
			if at.Before(interval.from) {
				continue
			}
			if !interval.to.IsZero() && !at.Before(interval.to) {
				continue
			}
			deadline := at.Add(authorizationLeaseDuration)
			if !interval.to.IsZero() && interval.to.Before(deadline) {
				deadline = interval.to
			}
			out = append(out, serverGrant{key: entry.key, deadline: deadline, boundary: interval.to})
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

// nextTransition returns the earliest interval boundary strictly after `after`
// across all entries, or zero when nothing is scheduled.
func (p *authorizationProjection) nextTransition(after time.Time) time.Time {
	var next time.Time
	consider := func(t time.Time) {
		if t.IsZero() || !t.After(after) {
			return
		}
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	for _, entry := range p.entries {
		for _, interval := range entry.intervals {
			consider(interval.from)
			consider(interval.to)
		}
	}
	return next
}

// authorizationDigest is the semantic identity of a grant set: sorted
// credential keys with their business boundaries. Renewal deadlines are not
// part of it, so a renewal never changes the digest while a revoke, a new
// grant, or a boundary change always does.
func authorizationDigest(grants []serverGrant) (string, []string) {
	keys := make([]string, 0, len(grants))
	h := sha256.New()
	for _, grant := range grants {
		keys = append(keys, grant.key)
		h.Write([]byte(grant.key))
		h.Write([]byte{'\t'})
		if !grant.boundary.IsZero() {
			h.Write([]byte(grant.boundary.UTC().Format(time.RFC3339Nano)))
		}
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil)), keys
}

// buildAuthorizationProjection evaluates every user's authorization windows.
// Users without future boundaries are covered by the single snapshot at `at`;
// users with binding or exception boundaries get one snapshot per boundary so
// a redundant grant's expiry does not disconnect a route another plan still
// authorizes, and a scheduled start becomes visible without a rebuild.
func buildAuthorizationProjection(revision uint64, at time.Time, data store.FullRoutingConfig, credentials []model.ProxyCredential) *authorizationProjection {
	at = at.UTC()
	snap := snapshotAt(data, at)
	scopesNow := map[credentialScopeKey]bool{}
	for _, scope := range core.ProxyCredentialScopes(data.Users, data.UserDevices, data.Inbounds, credentialOptions(data, snap)) {
		scopesNow[proxyScopeKey(scope)] = true
	}
	intervalsByScope := map[credentialScopeKey][]authorizationInterval{}
	for _, user := range data.Users {
		events := map[time.Time]bool{}
		consider := func(t *time.Time) {
			if t != nil && t.After(at) {
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
		if len(events) == 0 {
			for key := range scopesNow {
				if key.userID == user.ID {
					intervalsByScope[key] = []authorizationInterval{{from: at}}
				}
			}
			continue
		}
		times := make([]time.Time, 0, len(events)+1)
		for event := range events {
			times = append(times, event)
		}
		sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
		if len(times) > authorizationMaxEventsPerUser {
			times = times[:authorizationMaxEventsPerUser]
		}
		times = append([]time.Time{at}, times...)
		single := data
		single.Users = []model.User{user}
		presence := make([]map[credentialScopeKey]bool, len(times))
		for i, t := range times {
			if i == 0 {
				presence[i] = map[credentialScopeKey]bool{}
				for key := range scopesNow {
					if key.userID == user.ID {
						presence[i][key] = true
					}
				}
				continue
			}
			future := snapshotAt(single, t)
			presence[i] = map[credentialScopeKey]bool{}
			for _, scope := range core.ProxyCredentialScopes(single.Users, single.UserDevices, single.Inbounds, credentialOptions(single, future)) {
				presence[i][proxyScopeKey(scope)] = true
			}
		}
		allKeys := map[credentialScopeKey]bool{}
		for _, set := range presence {
			for key := range set {
				allKeys[key] = true
			}
		}
		for key := range allKeys {
			var intervals []authorizationInterval
			var open *authorizationInterval
			for i, t := range times {
				present := presence[i][key]
				switch {
				case present && open == nil:
					intervals = append(intervals, authorizationInterval{from: t})
					open = &intervals[len(intervals)-1]
				case !present && open != nil:
					open.to = t
					open = nil
				}
			}
			intervalsByScope[key] = intervals
		}
	}
	projection := &authorizationProjection{routingRevision: revision, builtAt: at}
	serversByRoute := map[[2]int64]map[int64]bool{}
	for _, c := range credentials {
		if c.Status != "active" || c.ID == "" {
			continue
		}
		intervals, ok := intervalsByScope[proxyScopeKey(c)]
		if !ok || len(intervals) == 0 {
			continue
		}
		route := [2]int64{c.InboundID, c.PathID}
		servers, resolved := serversByRoute[route]
		if !resolved {
			nodeType, nodeID := model.AssignableNodeInbound, c.InboundID
			for _, path := range data.ProxyPaths {
				if path.Enabled && path.ID == c.PathID && path.InboundID == c.InboundID {
					nodeType, nodeID = model.AssignableNodeProxyPath, path.ID
					break
				}
			}
			ids, _, _ := core.AffectedAuthServers(map[string]bool{core.NodeKeyOf(nodeType, nodeID): true}, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, nil)
			servers = make(map[int64]bool, len(ids))
			for _, id := range ids {
				servers[id] = true
			}
			serversByRoute[route] = servers
		}
		if len(servers) == 0 {
			continue
		}
		projection.entries = append(projection.entries, authorizationEntry{key: c.ID, servers: servers, intervals: intervals})
	}
	sort.Slice(projection.entries, func(i, j int) bool { return projection.entries[i].key < projection.entries[j].key })
	return projection
}

// authorizationProjectionCache is the revision-keyed cache of the projection.
type authorizationProjectionCache struct {
	mu      sync.Mutex
	current *authorizationProjection
}

func (s *Server) invalidateAuthorizationProjection() {
	s.authorizationProjections.mu.Lock()
	s.authorizationProjections.current = nil
	s.authorizationProjections.mu.Unlock()
}

// authorizationProjection returns the projection for the current routing
// revision, rebuilding it only when the revision changed or the entry aged out.
// The revision is read before and after loading so a concurrent mutation can
// never be attributed to an older projection.
func (s *Server) authorizationProjection(ctx context.Context) (*authorizationProjection, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before, err := s.store.RoutingCacheRevision(ctx)
		if err != nil {
			return nil, err
		}
		s.authorizationProjections.mu.Lock()
		current := s.authorizationProjections.current
		s.authorizationProjections.mu.Unlock()
		if current != nil && current.routingRevision == before && time.Since(current.builtAt) < authorizationProjectionTTL {
			return current, nil
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
		built := buildAuthorizationProjection(after, time.Now().UTC(), routing.data, credentials)
		s.authorizationProjections.mu.Lock()
		s.authorizationProjections.current = built
		s.authorizationProjections.mu.Unlock()
		return built, nil
	}
	return nil, errors.New("authorization changed while building projection; retry required")
}

// currentAuthorizationLease issues the lease a server must hold right now. It
// records the semantic desired revision in the ledger (advancing it only when
// the grant set changed), allocates the renewal sequence, and carries every
// still-unconfirmed denial so a kernel can deny before the snapshot lands.
func (s *Server) currentAuthorizationLease(ctx context.Context, serverID int64) (*model.AuthorizationLease, error) {
	projection, err := s.authorizationProjection(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	grants := projection.grantsAt(serverID, now)
	digest, keys := authorizationDigest(grants)
	projection.leaseMu.Lock()
	defer projection.leaseMu.Unlock()
	// A grant-set change alters the digest and a routing change replaces the
	// whole projection, so a reused lease can never hide a revoke.
	if lease := projection.leases[serverID]; lease != nil && lease.Digest == digest {
		issued, err := time.Parse(time.RFC3339Nano, lease.IssuedAt)
		if elapsed := now.Sub(issued); err == nil && elapsed >= 0 && elapsed < authorizationLeaseReissueAfter {
			return lease, nil
		}
	}
	expires := now.Add(authorizationLeaseDuration)
	evaluation, sequence, err := s.store.EvaluateAuthorizationDesiredWithSequence(ctx, serverID, projection.routingRevision, digest, keys, now, expires)
	if err != nil {
		return nil, err
	}
	denials, err := s.store.PendingAuthorizationDenials(ctx, serverID)
	if err != nil {
		return nil, err
	}
	lease := &model.AuthorizationLease{
		Revision:  evaluation.State.DesiredRevision,
		Sequence:  sequence,
		Digest:    digest,
		IssuedAt:  now.Format(time.RFC3339Nano),
		ExpiresAt: expires.Format(time.RFC3339Nano),
		Grants:    make(map[string]string, len(grants)),
	}
	for _, grant := range grants {
		lease.Grants[grant.key] = grant.deadline.UTC().Format(time.RFC3339Nano)
	}
	for _, denial := range denials {
		if _, granted := lease.Grants[denial.CredentialID]; granted {
			continue
		}
		lease.Denied = append(lease.Denied, denial.CredentialID)
	}
	sort.Strings(lease.Denied)
	if projection.leases == nil {
		projection.leases = make(map[int64]*model.AuthorizationLease)
	}
	projection.leases[serverID] = lease
	return lease, nil
}

// buildAuthorizationLease is the stateless projection of one server's grants
// at `at`. It carries no ledger revision or denials and exists for callers that
// only need the grant set, such as tests and previews.
func buildAuthorizationLease(revision int64, at time.Time, serverID int64, data store.FullRoutingConfig, credentials []model.ProxyCredential) *model.AuthorizationLease {
	projection := buildAuthorizationProjection(0, at, data, credentials)
	grants := projection.grantsAt(serverID, at.UTC())
	digest, _ := authorizationDigest(grants)
	lease := &model.AuthorizationLease{Revision: revision, Digest: digest, IssuedAt: at.UTC().Format(time.RFC3339Nano), ExpiresAt: at.UTC().Add(authorizationLeaseDuration).Format(time.RFC3339Nano), Grants: map[string]string{}}
	for _, grant := range grants {
		lease.Grants[grant.key] = grant.deadline.UTC().Format(time.RFC3339Nano)
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

// authorizationLeaseValidUntil parses the absolute expiry of a lease.
func authorizationLeaseValidUntil(lease *model.AuthorizationLease) time.Time {
	if lease == nil {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lease.ExpiresAt)); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(lease.IssuedAt)); err == nil {
		return t.Add(authorizationLeaseDuration)
	}
	return time.Time{}
}
