package store

import (
	"context"
	"database/sql"
	"errors"
)

type ServerDeliveryFlags struct {
	ServerID               int64
	AuthorizationFastLane  bool
	RuntimeUsersEnabled    bool
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
		runtime_users_enabled integer not null default 1
	)`)
	return err
}

func (s *Store) ServerDeliveryFlags(ctx context.Context, serverID int64) (ServerDeliveryFlags, error) {
	flags := defaultServerDeliveryFlags(serverID)
	if serverID <= 0 {
		return flags, nil
	}
	var authLane, usersLane int
	err := s.db.QueryRowContext(ctx, `select authorization_fast_lane, runtime_users_enabled from server_delivery_flags where server_id=?`, serverID).Scan(&authLane, &usersLane)
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
	rows, err := s.db.QueryContext(ctx, `select server_id, authorization_fast_lane, runtime_users_enabled from server_delivery_flags`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ServerDeliveryFlags{}
	for rows.Next() {
		var flags ServerDeliveryFlags
		var authLane, usersLane int
		if err := rows.Scan(&flags.ServerID, &authLane, &usersLane); err != nil {
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
	_, err := s.db.ExecContext(ctx, `insert into server_delivery_flags(server_id, authorization_fast_lane, runtime_users_enabled) values(?,?,?)
		on conflict(server_id) do update set authorization_fast_lane=excluded.authorization_fast_lane, runtime_users_enabled=excluded.runtime_users_enabled`,
		flags.ServerID, boolInt(flags.AuthorizationFastLane), boolInt(flags.RuntimeUsersEnabled))
	return err
}
