package store

import (
	"context"
	"fmt"
	"log"
	"time"
)

// Index migrations that are too large to run while a Controller is starting.
//
// Schema DDL runs inside Open, before the Controller listens. That is fine for
// creating an index on an empty or small table, and wrong for rebuilding one
// over a reporting table that has grown for weeks: the process is not serving
// while it runs. On a production Controller - 2 cores, 1.06M audit reports -
// rebuilding the two indexes below took more than three minutes, which is
// longer than the updater waits for the new program to answer its health probe.
// The update was rolled back, and by then the host was loaded enough that the
// restored program missed the same window, so the rollback failed too and the
// host sat without a Controller until it recovered.
//
// These therefore run after the server is already serving. Nothing depends on
// them for correctness: until a new index exists, its queries fall back to
// whatever else the planner finds, which is what they used before. The old
// index is dropped only once the new one is in place, so no query is ever left
// without both.
type deferredIndex struct {
	name     string
	table    string
	create   string
	replaces string
}

var deferredIndexes = []deferredIndex{
	{
		name:     "idx_connection_audit_user_window",
		table:    "connection_audit_reports",
		create:   `create index if not exists idx_connection_audit_user_window on connection_audit_reports(user_id, ended_at desc, source_ip, server_id, connection_count, active_peak)`,
		replaces: "idx_connection_audit_user_time",
	},
	{
		name:     "idx_connection_audit_source_window",
		table:    "connection_audit_reports",
		create:   `create index if not exists idx_connection_audit_source_window on connection_audit_reports(source_ip, ended_at desc, user_id)`,
		replaces: "idx_connection_audit_source_time",
	},
}

// PendingDeferredIndexes names the deferred migrations that have not finished,
// either because the new index is missing or because the one it replaces is
// still present.
func (s *Store) PendingDeferredIndexes(ctx context.Context) ([]string, error) {
	pending := []string{}
	for _, item := range deferredIndexes {
		done, err := s.deferredIndexApplied(ctx, item)
		if err != nil {
			return nil, err
		}
		if !done {
			pending = append(pending, item.name)
		}
	}
	return pending, nil
}

func (s *Store) deferredIndexApplied(ctx context.Context, item deferredIndex) (bool, error) {
	present, err := s.indexExists(ctx, item.name)
	if err != nil || !present {
		return false, err
	}
	superseded, err := s.indexExists(ctx, item.replaces)
	if err != nil {
		return false, err
	}
	return !superseded, nil
}

func (s *Store) indexExists(ctx context.Context, name string) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='index' and name=?`, name).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// MigrateDeferredIndexes brings the deferred indexes up to date. It is
// idempotent and safe to call on every start: an index that is already in place
// costs one lookup.
//
// Each pair is created before its predecessor is dropped, and each step commits
// on its own, so an interruption leaves a state a later call completes rather
// than one it has to undo.
func (s *Store) MigrateDeferredIndexes(ctx context.Context) error {
	for _, item := range deferredIndexes {
		done, err := s.deferredIndexApplied(ctx, item)
		if err != nil {
			return err
		}
		if done {
			continue
		}
		present, err := s.indexExists(ctx, item.name)
		if err != nil {
			return err
		}
		if !present {
			startedAt := time.Now()
			log.Printf("building deferred index %s on %s", item.name, item.table)
			if _, err := s.db.ExecContext(ctx, item.create); err != nil {
				return fmt.Errorf("build deferred index %s: %w", item.name, err)
			}
			log.Printf("built deferred index %s in %s", item.name, time.Since(startedAt).Round(time.Millisecond))
		}
		if _, err := s.db.ExecContext(ctx, `drop index if exists `+item.replaces); err != nil {
			return fmt.Errorf("drop superseded index %s: %w", item.replaces, err)
		}
	}
	return nil
}
