package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// AccessDeadlineNextDue returns the earliest future business boundary that can
// change authorization or runtime-user packages without relying on a cache TTL:
// binding/exception start and expiry (including AccessLifecycleNextDue sources)
// plus active traffic-period ends (billing period switch). A nil result means
// nothing is scheduled and the Controller scheduler should sleep on its fallback.
func (s *Store) AccessDeadlineNextDue(ctx context.Context, at time.Time) (*time.Time, error) {
	nowText := at.UTC().Format(time.RFC3339Nano)
	var candidates []*time.Time

	lifecycle, err := s.AccessLifecycleNextDue(ctx, at)
	if err != nil {
		return nil, err
	}
	if lifecycle != nil {
		candidates = append(candidates, lifecycle)
	}

	var raw sql.NullString
	err = s.db.QueryRowContext(ctx, `select min(due) from (
		select min(starts_at) as due from user_plan_bindings where enabled=1 and starts_at is not null and starts_at != '' and starts_at>?
		union all
		select min(expires_at) from user_plan_bindings where enabled=1 and expires_at is not null and expires_at != '' and expires_at>?
		union all
		select min(starts_at) from user_node_exceptions where status in ('pending','active') and starts_at is not null and starts_at != '' and starts_at>?
		union all
		select min(expires_at) from user_node_exceptions where status in ('pending','active') and expires_at is not null and expires_at != '' and expires_at>?
		union all
		select min(ends_at) from traffic_periods where state='active' and ends_at is not null and ends_at != '' and ends_at>?
	)`, nowText, nowText, nowText, nowText, nowText).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if raw.Valid && strings.TrimSpace(raw.String) != "" {
		due := parseTime(raw.String)
		if !due.IsZero() {
			candidates = append(candidates, &due)
		}
	}

	var next *time.Time
	for _, candidate := range candidates {
		if candidate == nil || candidate.IsZero() {
			continue
		}
		if next == nil || candidate.Before(*next) {
			t := candidate.UTC()
			next = &t
		}
	}
	return next, nil
}

// MarkAuthorizationEvaluatedStale clears the evaluated routing watermark for
// the given servers so StaleAuthorizationServerIDs includes them without a
// fleet-wide routing revision bump. Used by precise invalidation and the
// time-boundary scheduler.
func (s *Store) MarkAuthorizationEvaluatedStale(ctx context.Context, serverIDs []int64) error {
	return s.markEvaluatedRoutingStale(ctx, "authorization_states", serverIDs)
}

// MarkRuntimeUsersEvaluatedStale clears the evaluated routing watermark for
// the given servers so StaleRuntimeUserServerIDs includes them. Extends the
// existing wakeRuntimeUsersSync / StaleRuntimeUserServerIDs pattern.
func (s *Store) MarkRuntimeUsersEvaluatedStale(ctx context.Context, serverIDs []int64) error {
	return s.markEvaluatedRoutingStale(ctx, "runtime_user_states", serverIDs)
}

func (s *Store) markEvaluatedRoutingStale(ctx context.Context, table string, serverIDs []int64) error {
	ids := uniquePositiveServerIDs(serverIDs)
	if len(ids) == 0 {
		return nil
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, ts)
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
		if _, err := s.db.ExecContext(ctx, `insert or ignore into `+table+`(server_id,updated_at) values(?,?)`, id, ts); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `update `+table+` set evaluated_routing_revision=0,updated_at=? where server_id in (`+strings.Join(placeholders, ",")+`)`, args...) // #nosec G202 -- table is a fixed literal; placeholders are generated question marks.
	return err
}

func uniquePositiveServerIDs(serverIDs []int64) []int64 {
	seen := make(map[int64]bool, len(serverIDs))
	out := make([]int64, 0, len(serverIDs))
	for _, id := range serverIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// HasTrafficPeriodEndingNear reports whether any active traffic period ends
// within window of at. The access-deadline scheduler uses this to bump the
// traffic-policy revision only on billing boundaries.
func (s *Store) HasTrafficPeriodEndingNear(ctx context.Context, at time.Time, window time.Duration) (bool, error) {
	if window < 0 {
		window = -window
	}
	from := at.UTC().Add(-window).Format(time.RFC3339Nano)
	to := at.UTC().Add(window).Format(time.RFC3339Nano)
	var count int
	err := s.db.QueryRowContext(ctx, `select count(1) from traffic_periods where state='active' and ends_at is not null and ends_at != '' and ends_at>=? and ends_at<=?`, from, to).Scan(&count)
	return count > 0, err
}
