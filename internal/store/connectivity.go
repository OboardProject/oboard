package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const connectivityProbeRetention = 35 * 24 * time.Hour

func (s *Store) ensureConnectivityEventKinds(ctx context.Context) error {
	var schemaSQL string
	if err := s.db.QueryRowContext(ctx, `select sql from sqlite_master where type='table' and name='server_connectivity_events'`).Scan(&schemaSQL); err != nil {
		return err
	}
	if strings.Contains(schemaSQL, "'controller_connected'") && strings.Contains(schemaSQL, "'controller_disconnected'") {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`create table server_connectivity_events_v2 ` + serverConnectivityEventsColumnsSQL,
		`insert into server_connectivity_events_v2(id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at) select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events`,
		`drop table server_connectivity_events`,
		`alter table server_connectivity_events_v2 rename to server_connectivity_events`,
		`create index idx_server_connectivity_events_server_time on server_connectivity_events(server_id,effective_at)`,
		`create index idx_server_connectivity_events_server_kind_time on server_connectivity_events(server_id,kind,effective_at desc)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type connectivityExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertConnectivityEvent(ctx context.Context, exec connectivityExecer, event model.ServerConnectivityEvent) (bool, error) {
	if event.ServerID <= 0 || event.EffectiveAt.IsZero() || event.EventKey == "" {
		return false, fmt.Errorf("invalid connectivity event")
	}
	var available any
	if event.Available != nil {
		available = boolInt(*event.Available)
	}
	createdAt := event.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	result, err := exec.ExecContext(ctx, `insert or ignore into server_connectivity_events(server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at) values(?,?,?,?,?,?,?,?,?)`,
		event.ServerID, event.Kind, available, event.LatencyMS, event.Error, event.Source, event.EffectiveAt.UTC().Format(time.RFC3339Nano), event.EventKey, createdAt.Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func recordConnectivitySettingEvent(ctx context.Context, exec connectivityExecer, serverID int64, enabled bool, effectiveAt time.Time, eventKey, source string) (bool, error) {
	kind := model.ConnectivityEventProbeDisabled
	if enabled {
		kind = model.ConnectivityEventProbeEnabled
	}
	return insertConnectivityEvent(ctx, exec, model.ServerConnectivityEvent{
		ServerID:    serverID,
		Kind:        kind,
		Source:      source,
		EffectiveAt: effectiveAt.UTC(),
		EventKey:    eventKey,
	})
}

func (s *Store) RecordConnectivityProbeSettingEvent(ctx context.Context, serverID int64, enabled bool, effectiveAt time.Time) error {
	value := 0
	if enabled {
		value = 1
	}
	_, err := recordConnectivitySettingEvent(ctx, s.db, serverID, enabled, effectiveAt, fmt.Sprintf("setting:%s:%d", effectiveAt.UTC().Format(time.RFC3339Nano), value), "setting_change")
	return err
}

func (s *Store) RecordControllerConnectionEvent(ctx context.Context, serverID int64, connected bool, effectiveAt time.Time) error {
	kind := model.ConnectivityEventControllerDisconnected
	state := "disconnected"
	available := 0
	statusError := "主控连接已断开"
	if connected {
		kind = model.ConnectivityEventControllerConnected
		state = "connected"
		available = 1
		statusError = ""
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	inserted, err := insertConnectivityEvent(ctx, tx, model.ServerConnectivityEvent{
		ServerID:    serverID,
		Kind:        kind,
		Source:      "agent_socket",
		EffectiveAt: effectiveAt.UTC(),
		EventKey:    "controller:" + state + ":" + effectiveAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	if inserted {
		if _, err := tx.ExecContext(ctx, `update server_telemetry set connectivity_available=?,connectivity_error=?,updated_at=? where server_id=?`, available, statusError, time.Now().UTC().Format(time.RFC3339Nano), serverID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CloseOpenControllerConnections(ctx context.Context, effectiveAt time.Time) error {
	rows, err := s.db.QueryContext(ctx, `select e.server_id from server_connectivity_events e where e.kind=? and not exists(select 1 from server_connectivity_events newer where newer.server_id=e.server_id and newer.kind in (?,?) and (newer.effective_at>e.effective_at or (newer.effective_at=e.effective_at and newer.id>e.id)))`, model.ConnectivityEventControllerConnected, model.ConnectivityEventControllerConnected, model.ConnectivityEventControllerDisconnected)
	if err != nil {
		return err
	}
	serverIDs := make([]int64, 0)
	for rows.Next() {
		var serverID int64
		if err := rows.Scan(&serverID); err != nil {
			return errors.Join(err, rows.Close())
		}
		serverIDs = append(serverIDs, serverID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, serverID := range serverIDs {
		if err := s.RecordControllerConnectionEvent(ctx, serverID, false, effectiveAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListConnectivityHistory(ctx context.Context, serverID int64, from, to time.Time) (model.ServerConnectivityHistory, error) {
	var history model.ServerConnectivityHistory
	for _, kinds := range [][]model.ConnectivityEventKind{
		{model.ConnectivityEventProbeEnabled, model.ConnectivityEventProbeDisabled, model.ConnectivityEventProbeTargetChanged},
		{model.ConnectivityEventProbeResult, model.ConnectivityEventServerOffline},
		{model.ConnectivityEventControllerConnected, model.ConnectivityEventControllerDisconnected},
	} {
		event, err := s.latestConnectivityEventBefore(ctx, serverID, from, kinds)
		if err != nil && err != sql.ErrNoRows {
			return history, err
		}
		if err == nil {
			history.Baseline = append(history.Baseline, event)
		}
	}
	sort.Slice(history.Baseline, func(i, j int) bool {
		if history.Baseline[i].EffectiveAt.Equal(history.Baseline[j].EffectiveAt) {
			return history.Baseline[i].ID < history.Baseline[j].ID
		}
		return history.Baseline[i].EffectiveAt.Before(history.Baseline[j].EffectiveAt)
	})
	rows, err := s.db.QueryContext(ctx, `select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events where server_id=? and effective_at>=? and effective_at<? order by effective_at asc,id asc`, serverID, from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return history, err
	}
	history.Events, err = scanConnectivityEvents(rows)
	if err != nil {
		return history, err
	}
	var dataStart sql.NullString
	if err := s.db.QueryRowContext(ctx, `select min(effective_at) from server_connectivity_events where server_id=?`, serverID).Scan(&dataStart); err != nil && err != sql.ErrNoRows {
		return history, err
	}
	if dataStart.Valid && dataStart.String != "" {
		parsed := parseTime(dataStart.String)
		history.DataStart = &parsed
	}
	return history, nil
}

func (s *Store) latestConnectivityEventBefore(ctx context.Context, serverID int64, before time.Time, kinds []model.ConnectivityEventKind) (model.ServerConnectivityEvent, error) {
	if len(kinds) == 0 {
		return model.ServerConnectivityEvent{}, errors.New("connectivity event kinds are required")
	}
	placeholders := make([]string, len(kinds))
	args := make([]any, 0, len(kinds)+2)
	args = append(args, serverID)
	for index, kind := range kinds {
		placeholders[index] = "?"
		args = append(args, kind)
	}
	args = append(args, before.UTC().Format(time.RFC3339Nano))
	query := `select id,server_id,kind,available,latency_ms,error,source,effective_at,event_key,created_at from server_connectivity_events where server_id=? and kind in (` + strings.Join(placeholders, ",") + `) and effective_at<? order by effective_at desc,id desc limit 1`
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanConnectivityEvent(row)
}

type connectivityScanner interface {
	Scan(...any) error
}

func scanConnectivityEvent(scanner connectivityScanner) (model.ServerConnectivityEvent, error) {
	var event model.ServerConnectivityEvent
	var available sql.NullInt64
	var effectiveAt, createdAt string
	err := scanner.Scan(&event.ID, &event.ServerID, &event.Kind, &available, &event.LatencyMS, &event.Error, &event.Source, &effectiveAt, &event.EventKey, &createdAt)
	if err != nil {
		return event, err
	}
	if available.Valid {
		value := available.Int64 == 1
		event.Available = &value
	}
	event.EffectiveAt = parseTime(effectiveAt)
	event.CreatedAt = parseTime(createdAt)
	return event, nil
}

func scanConnectivityEvents(rows *sql.Rows) ([]model.ServerConnectivityEvent, error) {
	defer rows.Close()
	var events []model.ServerConnectivityEvent
	for rows.Next() {
		event, err := scanConnectivityEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) CleanupOldConnectivityProbeEvents(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `delete from server_connectivity_events where kind=? and effective_at<?`, model.ConnectivityEventProbeResult, now.UTC().Add(-connectivityProbeRetention).Format(time.RFC3339Nano))
	return err
}

func (s *Store) SeedConnectivityHistory(ctx context.Context, migrationAt time.Time) error {
	rows, err := s.db.QueryContext(ctx, `select s.id,s.status,s.created_at,coalesce(t.connectivity_probe_enabled,0),coalesce(t.connectivity_available,-1),coalesce(t.connectivity_latency_ms,0),t.connectivity_checked_at,coalesce(t.connectivity_error,'') from servers s left join server_telemetry t on t.server_id=s.id where not exists(select 1 from server_connectivity_events e where e.server_id=s.id) order by s.id`)
	if err != nil {
		return err
	}
	type seed struct {
		serverID, latency                        int64
		status, createdAt, checkedAt, probeError string
		probeEnabled, available                  int
	}
	var seeds []seed
	for rows.Next() {
		var item seed
		var checked sql.NullString
		if err := rows.Scan(&item.serverID, &item.status, &item.createdAt, &item.probeEnabled, &item.available, &item.latency, &checked, &item.probeError); err != nil {
			return errors.Join(err, rows.Close())
		}
		if checked.Valid {
			item.checkedAt = checked.String
		}
		seeds = append(seeds, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range seeds {
		effectiveAt := migrationAt.UTC()
		if item.probeEnabled != 0 && item.checkedAt != "" {
			effectiveAt = parseTime(item.checkedAt).UTC()
		}
		if _, err := recordConnectivitySettingEvent(ctx, tx, item.serverID, item.probeEnabled != 0, effectiveAt, "bootstrap:setting", "migration_seed"); err != nil {
			return err
		}
		if item.probeEnabled != 0 && item.checkedAt != "" {
			available := item.available == 1
			if _, err := insertConnectivityEvent(ctx, tx, model.ServerConnectivityEvent{ServerID: item.serverID, Kind: model.ConnectivityEventProbeResult, Available: &available, LatencyMS: int(item.latency), Error: item.probeError, Source: "migration_seed", EffectiveAt: effectiveAt, EventKey: "probe:" + effectiveAt.Format(time.RFC3339Nano)}); err != nil {
				return err
			}
		}
		if item.probeEnabled != 0 && item.status == string(model.ServerOffline) {
			available := false
			if _, err := insertConnectivityEvent(ctx, tx, model.ServerConnectivityEvent{ServerID: item.serverID, Kind: model.ConnectivityEventServerOffline, Available: &available, Source: "migration_seed", EffectiveAt: migrationAt.UTC(), EventKey: "bootstrap:offline"}); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
