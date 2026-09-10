package store

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const connectionPresenceRetention = 24 * time.Hour

func (s *Store) ApplyConnectionPresenceEvents(ctx context.Context, agentID string, serverID int64, droppedCount int64, events []model.ConnectionPresenceEvent) ([]uint64, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || serverID <= 0 || droppedCount < 0 {
		return nil, errors.New("invalid connection presence source")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `insert into connection_presence_agents(agent_id,server_id,dropped_count,updated_at) values(?,?,?,?) on conflict(agent_id) do update set server_id=excluded.server_id,dropped_count=connection_presence_agents.dropped_count+excluded.dropped_count,updated_at=excluded.updated_at`, agentID, serverID, droppedCount, ts); err != nil {
		return nil, err
	}
	accepted := make([]uint64, 0, len(events))
	for _, event := range events {
		if event.Sequence == 0 || event.Sequence > math.MaxInt64 || event.ServerID != serverID || event.UserID <= 0 {
			continue
		}
		var payloadLastAt any
		if !event.PayloadLastAt.IsZero() {
			payloadLastAt = event.PayloadLastAt.UTC().Format(time.RFC3339Nano)
		}
		res, err := tx.ExecContext(ctx, `insert or ignore into connection_presence_events(agent_id,sequence,server_id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,source_ip,route_id,network,event,state,active_connections,meaningful,payload_last_at,event_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, agentID, event.Sequence, serverID, event.UserID, event.InboundID, event.PathID, event.DeviceIDHash, event.CredentialEpoch, event.SourceIP, event.RouteID, event.Network, event.Event, event.State, event.ActiveConnections, boolInt(event.Meaningful), payloadLastAt, event.At.UTC().Format(time.RFC3339Nano), ts)
		if err != nil {
			return nil, err
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return nil, err
		}
		accepted = append(accepted, event.Sequence)
		if inserted == 0 || event.Event == "credential_rejected" {
			continue
		}
		args := []any{serverID, event.UserID, event.InboundID, event.PathID, event.DeviceIDHash, event.CredentialEpoch, event.SourceIP, event.Network}
		if (event.State == "inactive" || event.ActiveConnections == 0) && (!event.Meaningful || payloadLastAt == nil) {
			if _, err := tx.ExecContext(ctx, `delete from connection_presence_states where server_id=? and user_id=? and inbound_id=? and path_id=? and device_id_hash=? and credential_epoch=? and source_ip=? and network=?`, args...); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `insert into connection_presence_states(server_id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,source_ip,route_id,network,active_connections,meaningful,payload_last_at,last_event_at,last_sequence,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(server_id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,source_ip,network) do update set route_id=excluded.route_id,active_connections=excluded.active_connections,meaningful=excluded.meaningful,payload_last_at=excluded.payload_last_at,last_event_at=excluded.last_event_at,last_sequence=excluded.last_sequence,updated_at=excluded.updated_at`, serverID, event.UserID, event.InboundID, event.PathID, event.DeviceIDHash, event.CredentialEpoch, event.SourceIP, event.RouteID, event.Network, event.ActiveConnections, boolInt(event.Meaningful), payloadLastAt, event.At.UTC().Format(time.RFC3339Nano), event.Sequence, ts); err != nil {
			return nil, err
		}
	}
	// Retention cleanup deliberately does not run here. An Agent report must pay
	// only for its own batch; sweeping the whole fleet's expired presence inside
	// every report transaction made one node's upload cadence the schedule for
	// everybody's deletes. PruneConnectionPresence runs it from the maintenance
	// loop instead, and no reader depends on it: presence is always read through
	// its business validity window, so an expired row that is still on disk is
	// already invisible.
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return accepted, nil
}

func (s *Store) ListConnectionPresenceForUser(ctx context.Context, userID int64, since time.Time) ([]model.ConnectionPresenceEvent, error) {
	if since.IsZero() {
		since = time.Now().UTC().Add(-2 * time.Minute)
	}
	// A server whose connection audit is switched off leaves presence rows behind
	// until the cleanup runs. Filtering here makes the exclusion immediate: an
	// online-device count or risk evaluation never waits for the physical delete.
	rows, err := s.db.QueryContext(ctx, `select p.server_id,p.user_id,p.inbound_id,p.path_id,p.device_id_hash,p.credential_epoch,p.source_ip,p.route_id,p.network,p.active_connections,p.meaningful,p.payload_last_at,p.last_event_at,p.last_sequence,p.updated_at from connection_presence_states p join servers s on s.id=p.server_id and s.connection_audit_enabled=1 where p.user_id=? and p.last_event_at>=? order by p.device_id_hash,p.source_ip,p.network`, userID, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.ConnectionPresenceEvent{}
	for rows.Next() {
		var item model.ConnectionPresenceEvent
		var meaningful int
		var payloadLastAt *string
		var at, updatedAt string
		if err := rows.Scan(&item.ServerID, &item.UserID, &item.InboundID, &item.PathID, &item.DeviceIDHash, &item.CredentialEpoch, &item.SourceIP, &item.RouteID, &item.Network, &item.ActiveConnections, &meaningful, &payloadLastAt, &at, &item.Sequence, &updatedAt); err != nil {
			return nil, err
		}
		item.Event = "current"
		if item.ActiveConnections > 0 {
			item.State = "active"
		} else {
			item.State = "inactive"
		}
		item.Meaningful = meaningful == 1
		if payloadLastAt != nil {
			item.PayloadLastAt = parseTime(*payloadLastAt)
		}
		item.At = parseTime(at)
		item.CreatedAt = parseTime(updatedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

// connectionPresenceStateRetention is how long a presence state row survives
// without a new event. It matches the window readers already filter by, so the
// delete only reclaims space.
const connectionPresenceStateRetention = 5 * time.Minute

// ConnectionPresencePruneResult reports one bounded cleanup pass.
type ConnectionPresencePruneResult struct {
	EventsDeleted int64
	StatesDeleted int64
	// More is true when the budget was exhausted before the backlog was, so the
	// caller can come back promptly instead of waiting a full maintenance cycle.
	More bool
}

// PruneConnectionPresence deletes expired presence rows in bounded batches.
//
// It never deletes a whole retention window in one statement: a backlog is
// worked off batch by batch so the write lock is held briefly and an Agent
// report is not blocked behind a fleet-wide delete. Passing a non-positive
// batch uses the default.
func (s *Store) PruneConnectionPresence(ctx context.Context, now time.Time, batch int, maxBatches int) (ConnectionPresencePruneResult, error) {
	if batch <= 0 {
		batch = 500
	}
	if maxBatches <= 0 {
		maxBatches = 8
	}
	out := ConnectionPresencePruneResult{}
	eventCutoff := now.UTC().Add(-connectionPresenceRetention).Format(time.RFC3339Nano)
	stateCutoff := now.UTC().Add(-connectionPresenceStateRetention).Format(time.RFC3339Nano)
	for round := 0; round < maxBatches; round++ {
		if err := ctx.Err(); err != nil {
			out.More = true
			return out, nil
		}
		res, err := s.db.ExecContext(ctx, `delete from connection_presence_events where rowid in (select rowid from connection_presence_events where event_at<? limit ?)`, eventCutoff, batch)
		if err != nil {
			return out, err
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return out, err
		}
		out.EventsDeleted += deleted
		if deleted < int64(batch) {
			break
		}
		if round == maxBatches-1 {
			out.More = true
		}
	}
	for round := 0; round < maxBatches; round++ {
		if err := ctx.Err(); err != nil {
			out.More = true
			return out, nil
		}
		res, err := s.db.ExecContext(ctx, `delete from connection_presence_states where rowid in (select rowid from connection_presence_states where last_event_at<? limit ?)`, stateCutoff, batch)
		if err != nil {
			return out, err
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return out, err
		}
		out.StatesDeleted += deleted
		if deleted < int64(batch) {
			break
		}
		if round == maxBatches-1 {
			out.More = true
		}
	}
	return out, nil
}

func (s *Store) ClearConnectionPresenceForServer(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from connection_presence_states where server_id=?`, serverID)
	return err
}
