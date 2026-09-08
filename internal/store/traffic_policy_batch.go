package store

import (
	"context"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// The runtime traffic policy for one server used to cost four SQLite round
// trips per authorized user: a period-transition lookup, a period upsert, a
// period read, and a lease read. Every server in the fleet repeated all of them
// for the same user population on every poll cycle, so a 22-server fleet with a
// few hundred users spent most of a core inside SQLite. The batched forms below
// answer the same questions for a whole user set in one query each and write
// only the rows that actually diverge.

// TrafficPeriodRequest is one desired accounting window for a user.
type TrafficPeriodRequest struct {
	UserID     int64
	PeriodKey  string
	StartedAt  time.Time
	EndsAt     time.Time
	LimitBytes int64
}

// TrafficLeaseProbe is one read-only lease question: does this server already
// hold a healthy lease for the user's current window?
type TrafficLeaseProbe struct {
	UserID     int64
	PeriodKey  string
	LimitBytes int64
}

// storedTrafficPeriod keeps the raw timestamp strings alongside the parsed row
// so the divergence test can compare exactly what the SQL upsert compares.
type storedTrafficPeriod struct {
	period   model.TrafficPeriod
	rawStart string
	rawEnd   string
}

// TrafficPeriodTransitionSet is one user's period-key redirections in both
// directions: forward resolves a stale key to its replacement, reverse names
// the most recent key a window replaced.
type TrafficPeriodTransitionSet struct {
	forward map[string]string
	reverse map[string]string
}

// Resolve follows the forward chain. It mirrors ResolveTrafficPeriodKey
// exactly, including the depth bound that protects against a cyclic chain.
func (t TrafficPeriodTransitionSet) Resolve(periodKey string) (string, bool, error) {
	current := periodKey
	changed := false
	for range 16 {
		next, ok := t.forward[current]
		if !ok || next == "" || next == current {
			return current, changed, nil
		}
		current = next
		changed = true
	}
	return "", false, errTrafficPeriodTransitionTooDeep
}

// Previous mirrors PreviousTrafficPeriodKey for a preloaded set.
func (t TrafficPeriodTransitionSet) Previous(targetPeriodKey string) (string, bool) {
	source := t.reverse[targetPeriodKey]
	return source, source != ""
}

// Forward exposes the source→target remaps for callers that only follow the
// chain, such as the listing projection.
func (t TrafficPeriodTransitionSet) Forward() map[string]string {
	if t.forward == nil {
		return map[string]string{}
	}
	return t.forward
}

// TrafficPeriodTransitions returns the transition sets for the given users. The
// table is normally empty; loading it once avoids two queries per user inside
// the policy loop.
func (s *Store) TrafficPeriodTransitions(ctx context.Context, userIDs []int64) (map[int64]TrafficPeriodTransitionSet, error) {
	out := map[int64]TrafficPeriodTransitionSet{}
	args, placeholders := int64IDQueryArgs(userIDs)
	if len(args) == 0 {
		return out, nil
	}
	// Ascending created_at means the last write per target wins, which is the
	// row `order by created_at desc limit 1` would have selected.
	rows, err := s.db.QueryContext(ctx, `select user_id,source_period_key,target_period_key from traffic_period_transitions where user_id in (`+placeholders+`) order by created_at asc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var source, target string
		if err := rows.Scan(&userID, &source, &target); err != nil {
			return nil, err
		}
		set, ok := out[userID]
		if !ok {
			set = TrafficPeriodTransitionSet{forward: map[string]string{}, reverse: map[string]string{}}
		}
		set.forward[source] = target
		set.reverse[target] = source
		out[userID] = set
	}
	return out, rows.Err()
}

func (s *Store) loadTrafficPeriodsFor(ctx context.Context, requests []TrafficPeriodRequest) (map[int64]storedTrafficPeriod, error) {
	out := make(map[int64]storedTrafficPeriod, len(requests))
	userIDs := make([]int64, 0, len(requests))
	wanted := make(map[int64]string, len(requests))
	for _, request := range requests {
		if _, seen := wanted[request.UserID]; seen {
			continue
		}
		wanted[request.UserID] = request.PeriodKey
		userIDs = append(userIDs, request.UserID)
	}
	args, placeholders := int64IDQueryArgs(userIDs)
	if len(args) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx, trafficPeriodSelectSQL+` where user_id in (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p model.TrafficPeriod
		var start, end, updated string
		if err := rows.Scan(&p.ID, &p.UserID, &p.PeriodKey, &start, &end, &p.Upload, &p.Download, &p.Limit, &p.State, &updated); err != nil {
			return nil, err
		}
		if wanted[p.UserID] != p.PeriodKey {
			continue
		}
		p.StartedAt = parseTime(start)
		p.EndsAt = parseTime(end)
		p.UpdatedAt = parseTime(updated)
		out[p.UserID] = storedTrafficPeriod{period: p, rawStart: start, rawEnd: end}
	}
	return out, rows.Err()
}

// trafficPeriodNeedsWrite mirrors the WHERE clause of the EnsureTrafficPeriod
// upsert: the stored limit gates quota exhaustion, but the comparison uses the
// incoming limit.
func trafficPeriodNeedsWrite(stored storedTrafficPeriod, request TrafficPeriodRequest) bool {
	wantStart := request.StartedAt.Format(time.RFC3339Nano)
	wantEnd := request.EndsAt.Format(time.RFC3339Nano)
	if stored.rawStart != wantStart || stored.rawEnd != wantEnd || stored.period.Limit != request.LimitBytes {
		return true
	}
	conflictState := "active"
	if stored.period.Limit > 0 && stored.period.Upload+stored.period.Download >= request.LimitBytes {
		conflictState = "quota_exceeded"
	}
	return stored.period.State != conflictState
}

// EnsureTrafficPeriods is the batched EnsureTrafficPeriod. It reads the current
// rows once, writes only the rows the per-row upsert would have changed, and
// returns the resulting period for every request.
func (s *Store) EnsureTrafficPeriods(ctx context.Context, requests []TrafficPeriodRequest) (map[int64]model.TrafficPeriod, error) {
	out := make(map[int64]model.TrafficPeriod, len(requests))
	if len(requests) == 0 {
		return out, nil
	}
	stored, err := s.loadTrafficPeriodsFor(ctx, requests)
	if err != nil {
		return nil, err
	}
	pending := make([]TrafficPeriodRequest, 0, 8)
	for _, request := range requests {
		if _, done := out[request.UserID]; done {
			continue
		}
		current, ok := stored[request.UserID]
		if ok && !trafficPeriodNeedsWrite(current, request) {
			out[request.UserID] = current.period
			continue
		}
		pending = append(pending, request)
	}
	if len(pending) == 0 {
		return out, nil
	}
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, request := range pending {
		if _, err := tx.ExecContext(ctx, trafficPeriodUpsertSQL, request.UserID, request.PeriodKey,
			request.StartedAt.Format(time.RFC3339Nano), request.EndsAt.Format(time.RFC3339Nano), request.LimitBytes, ts); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, request := range pending {
		period, err := s.GetTrafficPeriod(ctx, request.UserID, request.PeriodKey)
		if err != nil {
			return nil, err
		}
		out[request.UserID] = period
	}
	return out, nil
}

// CurrentTrafficLeaseAllocations is the batched read-only lease fast path. A
// user missing from the result has no healthy lease and still needs the
// serialized EnsureTrafficLeaseAllocation write path.
func (s *Store) CurrentTrafficLeaseAllocations(ctx context.Context, serverID int64, probes []TrafficLeaseProbe, at time.Time) (map[int64]TrafficLeaseAllocation, error) {
	out := map[int64]TrafficLeaseAllocation{}
	if serverID <= 0 || len(probes) == 0 {
		return out, nil
	}
	byUser := make(map[int64]TrafficLeaseProbe, len(probes))
	userIDs := make([]int64, 0, len(probes))
	for _, probe := range probes {
		if probe.LimitBytes <= 0 || probe.UserID <= 0 || probe.PeriodKey == "" {
			continue
		}
		if _, seen := byUser[probe.UserID]; seen {
			continue
		}
		byUser[probe.UserID] = probe
		userIDs = append(userIDs, probe.UserID)
	}
	args, placeholders := int64IDQueryArgs(userIDs)
	if len(args) == 0 {
		return out, nil
	}
	args = append([]any{serverID}, args...)
	rows, err := s.db.QueryContext(ctx, `select user_id, period_key, lease_bytes, consumed_bytes, coalesce(nullif(state,''),'active'), coalesce(valid_until,'') from traffic_leases where server_id=? and user_id in (`+placeholders+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var periodKey, state, validUntil string
		var leaseBytes, consumedBytes int64
		if err := rows.Scan(&userID, &periodKey, &leaseBytes, &consumedBytes, &state, &validUntil); err != nil {
			return nil, err
		}
		probe, ok := byUser[userID]
		if !ok || probe.PeriodKey != periodKey {
			continue
		}
		if allocation, healthy := trafficLeaseIsHealthy(leaseBytes, consumedBytes, state, validUntil, probe.LimitBytes, at); healthy {
			out[userID] = allocation
		}
	}
	return out, rows.Err()
}

// trafficLeaseIsHealthy is the shared predicate behind the single-row and the
// batched lease fast paths. Both must accept exactly the same leases, so the
// batched form can never hand out a lease the serialized path would refresh.
func trafficLeaseIsHealthy(leaseBytes, consumedBytes int64, state, validUntil string, limitBytes int64, at time.Time) (TrafficLeaseAllocation, bool) {
	if state != trafficLeaseActive || strings.TrimSpace(validUntil) == "" {
		return TrafficLeaseAllocation{}, false
	}
	expiry, err := time.Parse(time.RFC3339Nano, validUntil)
	if err != nil || !expiry.After(at.Add(trafficLeaseRefreshBefore)) {
		return TrafficLeaseAllocation{}, false
	}
	remaining := leaseBytes - consumedBytes
	if remaining < 0 {
		remaining = 0
	}
	if remaining < trafficLeaseChunk(limitBytes)/2 {
		return TrafficLeaseAllocation{}, false
	}
	return TrafficLeaseAllocation{RemainingBytes: remaining, ResetBytes: leaseBytes}, true
}
