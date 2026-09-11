package store

import (
	"context"
	"database/sql"
	"time"
)

func (s *Store) ensureLatencyLegacyArchive(ctx context.Context) error {
	for _, query := range []string{
		`create table if not exists latency_legacy_archive(id integer primary key references server_connectivity_events(id) on delete cascade,server_id integer not null references servers(id) on delete cascade,kind text not null,source text not null,available integer,latency_ms integer not null,effective_at text not null)`,
		`create index if not exists idx_latency_legacy_server_time on latency_legacy_archive(server_id,effective_at)`,
		`create table if not exists latency_legacy_progress(id integer primary key check(id=1),cursor integer not null default -1)`,
		`insert into latency_legacy_progress(id) values(1) on conflict(id) do nothing`,
	} {
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RunLatencyLegacyBackfill(ctx context.Context, limit int) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var cursor int64
	if err := tx.QueryRowContext(ctx, `select cursor from latency_legacy_progress where id=1`).Scan(&cursor); err != nil {
		return 0, err
	}
	if cursor < 0 {
		if err := tx.QueryRowContext(ctx, `select coalesce(max(id),0) from server_connectivity_events`).Scan(&cursor); err != nil {
			return 0, err
		}
	}
	rows, err := tx.QueryContext(ctx, `select id,server_id,kind,source,available,latency_ms,effective_at from server_connectivity_events where id<=? order by id desc limit ?`, cursor, min(500, max(1, limit)))
	if err != nil {
		return 0, err
	}
	type item struct {
		id, server, latency int64
		kind, source, at    string
		available           sql.NullInt64
	}
	items := []item{}
	next := int64(0)
	count := 0
	for rows.Next() {
		var x item
		if err := rows.Scan(&x.id, &x.server, &x.kind, &x.source, &x.available, &x.latency, &x.at); err != nil {
			rows.Close()
			return 0, err
		}
		next = x.id - 1
		count++
		if x.kind == "probe_result" && x.source != "latency_probe" {
			items = append(items, x)
		}
	}
	readErr, closeErr := rows.Err(), rows.Close()
	if readErr != nil {
		return 0, readErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	for _, x := range items {
		if _, err := tx.ExecContext(ctx, `insert into latency_legacy_archive(id,server_id,kind,source,available,latency_ms,effective_at) values(?,?,?,?,?,?,?) on conflict(id) do nothing`, x.id, x.server, x.kind, x.source, x.available, x.latency, x.at); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update latency_legacy_progress set cursor=? where id=1`, next); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}

func (s *Store) legacyLatencySource(ctx context.Context, db latencyHistoryQueryer, serverID int64, from, to time.Time) (string, error) {
	var cursor int64
	if err := db.QueryRowContext(ctx, `select cursor from latency_legacy_progress where id=1`).Scan(&cursor); err != nil {
		return "", err
	}
	table := "server_connectivity_events"
	if cursor == 0 {
		table = "latency_legacy_archive"
	}
	var count int
	if err := db.QueryRowContext(ctx, `select count(*) from (select id from `+table+` where server_id=? and effective_at>=? and effective_at<? order by effective_at limit 50001)`, serverID, connectivityTimeBound(from), connectivityTimeBound(to)).Scan(&count); err != nil {
		return "", err
	}
	if count > 50000 {
		return "", ErrHistoryCoverage
	}
	return table, nil
}
