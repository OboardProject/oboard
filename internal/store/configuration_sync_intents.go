package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const configurationSyncIntentBatchSize = 64
const configurationSyncTargetBatchSize = 128

type ConfigurationSyncIntent struct {
	Source             string
	Scope              string
	ServerID           int64
	Revision           uint64
	TargetCount        int
	processingRevision uint64
	lastServerID       int64
}

func migrateConfigurationSyncIntentsTx(ctx context.Context, tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='configuration_sync_intents'`).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `create table configuration_sync_intents (
		source text not null,
		scope text not null check(scope in ('explicit_ids','all','unresolved')),
		server_id integer not null check((scope='explicit_ids' and server_id>0) or (scope<>'explicit_ids' and server_id=0)),
		revision integer not null check(revision>0),
		processing_revision integer not null default 0,
		last_server_id integer not null default 0,
		first_changed_at text not null,
		updated_at text not null,
		primary key(source,scope,server_id))`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `create index idx_configuration_sync_intents_oldest on configuration_sync_intents(first_changed_at,source,server_id)`); err != nil {
		return err
	}
	// The previous model could commit a revision without its post-commit mark.
	// Seed exactly once, including when importing an older backup.
	if _, err := tx.ExecContext(ctx, `insert into configuration_sync_intents(source,scope,server_id,revision,first_changed_at,updated_at)
		select 'upgrade_handoff','unresolved',0,revision,?,? from configuration_revision where id=1 and revision>0`, now(), now()); err != nil {
		return err
	}
	return nil
}

// configurationSyncIntentSQL is the statement each configuration trigger runs
// inside the mutating transaction. A table with a derivable server mapping
// records one intent per affected server; anything else records the
// `unresolved` scope the drain expands to the whole fleet.
func configurationSyncIntentSQL(table, event string) string {
	statements := ""
	for _, alias := range configurationSyncScopeAliases(event) {
		scope := configurationSyncScopeSelect(table, alias)
		if scope == "" {
			statements = ""
			break
		}
		statements += fmt.Sprintf(`insert into configuration_sync_intents(source,scope,server_id,revision,first_changed_at,updated_at)
		select '%s.%s','explicit_ids',t.server_id,cr.revision,strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ','now'),strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ','now')
		from (%s) t, configuration_revision cr
		where cr.id=1 and t.server_id is not null and t.server_id>0
		on conflict(source,scope,server_id) do update set revision=excluded.revision,updated_at=excluded.updated_at;`, table, event, scope)
	}
	if statements != "" {
		return statements
	}
	return fmt.Sprintf(`insert into configuration_sync_intents(source,scope,server_id,revision,first_changed_at,updated_at)
		select '%s.%s','unresolved',0,revision,strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ','now'),strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ','now') from configuration_revision where id=1
		on conflict(source,scope,server_id) do update set revision=excluded.revision,updated_at=excluded.updated_at;`, table, event)
}

// ConfigurationSyncSweepSource is the intent source of the periodic backstop.
const ConfigurationSyncSweepSource = "sweep.periodic"

// QueueConfigurationSyncSweep records one fleet-wide intent. Per-change scopes
// are derived from the changed row, so a mapping that is too narrow would leave
// a server pending nothing forever. This sweep is the backstop: it re-marks
// every enrolled server on a slow cadence, using the same paginated drain, and
// a server whose projection is unchanged settles as a semantic no-op without
// queuing anything.
func (s *Store) QueueConfigurationSyncSweep(ctx context.Context) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into configuration_sync_intents(source,scope,server_id,revision,first_changed_at,updated_at)
		select ?,'unresolved',0,revision,?,? from configuration_revision where id=1 and revision>0
		on conflict(source,scope,server_id) do update set revision=excluded.revision,updated_at=excluded.updated_at`, ConfigurationSyncSweepSource, ts, ts)
	return err
}

// DrainConfigurationSyncIntents hands committed intent to the existing server
// state machine. State updates and intent removal either both commit or neither
// does; a lost wake, crash, or repeated drain cannot lose the handoff.
func (s *Store) DrainConfigurationSyncIntents(ctx context.Context) ([]ConfigurationSyncIntent, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select source,scope,server_id,revision,processing_revision,last_server_id from configuration_sync_intents order by first_changed_at,source,server_id limit ?`, configurationSyncIntentBatchSize)
	if err != nil {
		return nil, err
	}
	var intents []ConfigurationSyncIntent
	for rows.Next() {
		var intent ConfigurationSyncIntent
		if err := rows.Scan(&intent.Source, &intent.Scope, &intent.ServerID, &intent.Revision, &intent.processingRevision, &intent.lastServerID); err != nil {
			rows.Close()
			return nil, err
		}
		intents = append(intents, intent)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(intents) == 0 {
		return nil, nil
	}
	remaining := configurationSyncTargetBatchSize
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	processed := make([]ConfigurationSyncIntent, 0, len(intents))
	for _, intent := range intents {
		if remaining == 0 {
			break
		}
		var ids []int64
		revision := intent.Revision
		finished := true
		switch intent.Scope {
		case "explicit_ids":
			ids, err = queryInt64sTx(ctx, tx, `select id from servers where id=? and (coalesce(agent_id,'')<>'' or exists(select 1 from configuration_sync_states where server_id=servers.id))`, intent.ServerID)
		case "all", "unresolved":
			if intent.processingRevision != 0 {
				revision = intent.processingRevision
			}
			ids, err = queryInt64sTx(ctx, tx, `select id from servers where id>? and (coalesce(agent_id,'')<>'' or exists(select 1 from configuration_sync_states where server_id=servers.id)) order by id limit ?`, intent.lastServerID, remaining)
			finished = len(ids) < remaining
		default:
			return nil, fmt.Errorf("unknown configuration intent scope %q", intent.Scope)
		}
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if err := markConfigurationSyncPendingTx(ctx, tx, id, revision, stamp); err != nil {
				return nil, err
			}
		}
		remaining -= len(ids)
		switch {
		case finished && revision == intent.Revision:
			_, err = tx.ExecContext(ctx, `delete from configuration_sync_intents where source=? and scope=? and server_id=? and revision=?`, intent.Source, intent.Scope, intent.ServerID, intent.Revision)
		case finished:
			_, err = tx.ExecContext(ctx, `update configuration_sync_intents set processing_revision=0,last_server_id=0,first_changed_at=? where source=? and scope=? and server_id=?`, stamp, intent.Source, intent.Scope, intent.ServerID)
		default:
			_, err = tx.ExecContext(ctx, `update configuration_sync_intents set processing_revision=?,last_server_id=?,first_changed_at=? where source=? and scope=? and server_id=?`, revision, ids[len(ids)-1], stamp, intent.Source, intent.Scope, intent.ServerID)
		}
		if err != nil {
			return nil, err
		}
		intent.Revision, intent.TargetCount = revision, len(ids)
		processed = append(processed, intent)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return processed, nil
}

func markConfigurationSyncPendingTx(ctx context.Context, tx *sql.Tx, serverID int64, revision uint64, stamp string) error {
	_, err := tx.ExecContext(ctx, configurationSyncPendingSQL, serverID, revision, fmt.Sprintf("routing:%d", revision), ConfigurationSyncTriggerRevision, stamp, stamp)
	return err
}
