package store

import (
	"context"
	"database/sql"
	"errors"
)

type ServerDeliveryFlags struct {
	HasSSHInbounds          bool
	ServerID                int64
	AuthorizationFastLane   bool
	RuntimeUsersEnabled     bool
	Revision                uint64
	AppliedRevision         uint64
	ProcessingRevision      uint64
	ProcessingConfigVersion int64
}

func defaultServerDeliveryFlags(serverID int64) ServerDeliveryFlags {
	return ServerDeliveryFlags{
		ServerID:              serverID,
		AuthorizationFastLane: true,
		RuntimeUsersEnabled:   true,
	}
}

func (s *Store) migrateServerDeliveryFlags(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `create table if not exists server_delivery_flags (
		server_id integer primary key references servers(id) on delete cascade,
		authorization_fast_lane integer not null default 1,
		runtime_users_enabled integer not null default 1,
		revision integer not null default 0,
		applied_revision integer not null default 0,
		processing_revision integer not null default 0,
		processing_config_version integer not null default 0
	)`)
	return err
}

func (s *Store) ServerDeliveryFlags(ctx context.Context, serverID int64) (ServerDeliveryFlags, error) {
	flags := defaultServerDeliveryFlags(serverID)
	if serverID <= 0 {
		return flags, nil
	}
	var authLane, usersLane int
	err := s.db.QueryRowContext(ctx, `select coalesce(f.authorization_fast_lane,1), coalesce(f.runtime_users_enabled,1), coalesce(f.revision,0), coalesce(f.applied_revision,0), coalesce(f.processing_revision,0), coalesce(f.processing_config_version,0), exists(select 1 from inbounds i where i.server_id=s.id and i.protocol='ssh' and i.enabled=1) from servers s left join server_delivery_flags f on f.server_id=s.id where s.id=?`, serverID).Scan(&authLane, &usersLane, &flags.Revision, &flags.AppliedRevision, &flags.ProcessingRevision, &flags.ProcessingConfigVersion, &flags.HasSSHInbounds)
	if errors.Is(err, sql.ErrNoRows) {
		return flags, nil
	}
	if err != nil {
		return flags, err
	}
	flags.AuthorizationFastLane = authLane != 0
	flags.RuntimeUsersEnabled = usersLane != 0
	return flags, nil
}

func (s *Store) ListServerDeliveryFlags(ctx context.Context) (map[int64]ServerDeliveryFlags, error) {
	rows, err := s.db.QueryContext(ctx, `select s.id, coalesce(f.authorization_fast_lane,1), coalesce(f.runtime_users_enabled,1), coalesce(f.revision,0), coalesce(f.applied_revision,0), coalesce(f.processing_revision,0), coalesce(f.processing_config_version,0), exists(select 1 from inbounds i where i.server_id=s.id and i.protocol='ssh' and i.enabled=1) from servers s left join server_delivery_flags f on f.server_id=s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ServerDeliveryFlags{}
	for rows.Next() {
		var flags ServerDeliveryFlags
		var authLane, usersLane int
		if err := rows.Scan(&flags.ServerID, &authLane, &usersLane, &flags.Revision, &flags.AppliedRevision, &flags.ProcessingRevision, &flags.ProcessingConfigVersion, &flags.HasSSHInbounds); err != nil {
			return nil, err
		}
		flags.AuthorizationFastLane = authLane != 0
		flags.RuntimeUsersEnabled = usersLane != 0
		out[flags.ServerID] = flags
	}
	return out, rows.Err()
}

func (s *Store) SetServerDeliveryFlags(ctx context.Context, flags ServerDeliveryFlags) error {
	if flags.ServerID <= 0 {
		return errors.New("server_id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := updateServerDeliveryFlagsTx(ctx, tx, flags.ServerID, &flags.AuthorizationFastLane, &flags.RuntimeUsersEnabled); err != nil {
		return err
	}
	return tx.Commit()
}

func updateServerDeliveryFlagsTx(ctx context.Context, tx *sql.Tx, serverID int64, auth, users *bool) (ServerDeliveryFlags, error) {
	flags := defaultServerDeliveryFlags(serverID)
	var oldAuth, oldUsers bool
	if err := tx.QueryRowContext(ctx, `select coalesce(f.authorization_fast_lane,1),coalesce(f.runtime_users_enabled,1) from servers s left join server_delivery_flags f on f.server_id=s.id where s.id=?`, serverID).Scan(&oldAuth, &oldUsers); err != nil {
		return flags, err
	}
	flags.AuthorizationFastLane, flags.RuntimeUsersEnabled = oldAuth, oldUsers
	if auth != nil {
		flags.AuthorizationFastLane = *auth
	}
	if users != nil {
		flags.RuntimeUsersEnabled = *users
	}
	if oldAuth == flags.AuthorizationFastLane && oldUsers == flags.RuntimeUsersEnabled {
		return flags, nil
	}
	if _, err := tx.ExecContext(ctx, `update configuration_revision set revision=revision+1 where id=1`); err != nil {
		return flags, err
	}
	if _, err := tx.ExecContext(ctx, `insert into server_delivery_flags(server_id,authorization_fast_lane,runtime_users_enabled,revision)
		select ?,?,?,revision from configuration_revision where id=1
		on conflict(server_id) do update set authorization_fast_lane=excluded.authorization_fast_lane,runtime_users_enabled=excluded.runtime_users_enabled,revision=excluded.revision`, serverID, boolInt(flags.AuthorizationFastLane), boolInt(flags.RuntimeUsersEnabled)); err != nil {
		return flags, err
	}
	_, err := tx.ExecContext(ctx, `insert into configuration_sync_intents(source,scope,server_id,revision,first_changed_at,updated_at)
		select 'delivery_policy','explicit_ids',server_id,revision,?,? from server_delivery_flags where server_id=?
		on conflict(source,scope,server_id) do update set revision=excluded.revision,updated_at=excluded.updated_at`, now(), now(), serverID)
	return flags, err
}

// The previous save could commit flags before failing to queue their refresh.
// Backfill that lost handoff once; reopening a migrated database does no work.
func (s *Store) migrateDeliveryPolicyRevisions(ctx context.Context) error {
	has, err := s.tableHasColumn(ctx, "server_delivery_flags", "revision")
	if err != nil || has {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, column := range []string{"revision", "applied_revision", "processing_revision", "processing_config_version"} {
		if _, err := tx.ExecContext(ctx, `alter table server_delivery_flags add column `+column+` integer not null default 0`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `update configuration_revision set revision=revision+1 where id=1 and exists(select 1 from server_delivery_flags)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update server_delivery_flags set revision=(select revision from configuration_revision where id=1)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into configuration_sync_intents(source,scope,server_id,revision,first_changed_at,updated_at)
		select 'delivery_policy','explicit_ids',server_id,revision,?,? from server_delivery_flags where true
		on conflict(source,scope,server_id) do update set revision=excluded.revision,updated_at=excluded.updated_at`, now(), now()); err != nil {
		return err
	}
	return tx.Commit()
}
