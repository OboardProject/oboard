package store

import (
	"context"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

var ErrConnectivityEventBudget = errors.New("history_event_budget: too many state events; choose a shorter window")

func connectivityTimeBound(at time.Time) string {
	if at.Nanosecond() == 0 {
		return at.UTC().Format("2006-01-02T15:04:05.000000000Z")
	}
	return at.UTC().Format(time.RFC3339Nano)
}

type ConnectivityEventPage struct {
	Events     []model.ServerConnectivityEvent
	BeforeTime string
	BeforeID   int64
	SnapshotID int64
	HasMore    bool
}

// The cursor preserves the stored timestamp, including its original precision.
// An insertion high-water mark keeps late reports out of an already opened page sequence.
func (s *Store) ListConnectivityEventPage(ctx context.Context, serverID int64, from, to time.Time, limit int, beforeTime string, beforeID, snapshotID int64) (ConnectivityEventPage, error) {
	page := ConnectivityEventPage{Events: make([]model.ServerConnectivityEvent, 0), SnapshotID: snapshotID}
	if limit < 1 || limit > 200 || !to.After(from) {
		return page, errors.New("invalid connectivity page")
	}
	if snapshotID == 0 {
		if err := s.db.QueryRowContext(ctx, `select coalesce(max(id),0) from server_connectivity_events`).Scan(&page.SnapshotID); err != nil {
			return page, err
		}
	}
	query := `select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events where server_id=? and effective_at>=? and effective_at<? and id<=?`
	args := []any{serverID, connectivityTimeBound(from), connectivityTimeBound(to), page.SnapshotID}
	if beforeTime != "" {
		query += ` and (effective_at,id)<(?,?)`
		args = append(args, beforeTime, beforeID)
	}
	query += ` order by effective_at desc,id desc limit ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		// Read the raw timestamp separately from the public, parsed event.
		var event model.ServerConnectivityEvent
		var available *bool
		var at, created string
		if err := rows.Scan(&event.ID, &event.ServerID, &event.Kind, &available, &event.LatencyMS, &event.Error, &event.Source, &at, &event.EventKey, &created); err != nil {
			return page, err
		}
		event.Available, event.EffectiveAt, event.CreatedAt = available, parseTime(at), parseTime(created)
		page.Events = append(page.Events, event)
		page.BeforeTime, page.BeforeID = at, event.ID
	}
	return page, rows.Err()
}
