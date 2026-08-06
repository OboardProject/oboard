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
	cutoff := time.Now().UTC().Add(-connectionPresenceRetention).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `delete from connection_presence_events where event_at<?`, cutoff); err != nil {
		return nil, err
	}
	stale := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `delete from connection_presence_states where last_event_at<?`, stale); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return accepted, nil
}

func (s *Store) ListConnectionPresenceForUser(ctx context.Context, userID int64, since time.Time) ([]model.ConnectionPresenceEvent, error) {
	if since.IsZero() {
		since = time.Now().UTC().Add(-2 * time.Minute)
	}
	rows, err := s.db.QueryContext(ctx, `select server_id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,source_ip,route_id,network,active_connections,meaningful,payload_last_at,last_event_at,last_sequence,updated_at from connection_presence_states where user_id=? and last_event_at>=? order by device_id_hash,source_ip,network`, userID, since.UTC().Format(time.RFC3339Nano))
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

func (s *Store) ClearConnectionPresenceForServer(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from connection_presence_states where server_id=?`, serverID)
	return err
}
