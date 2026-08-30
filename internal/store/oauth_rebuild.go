package store

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *Store) tableExists(ctx context.Context, name string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name=?`, name).Scan(&count)
	return count > 0, err
}

// tableHasColumn reports whether a column exists on a table.
func (s *Store) tableHasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `pragma table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// DropLegacyOAuthScopeColumns rebuilds the OAuth tables to drop the legacy
// scopes_json and oauth_grants.resource_filter_json columns that no longer
// participate in authorization. It is idempotent: once the columns are gone the
// rebuild is skipped. The rebuild copies all rows and recreates the indexes.
// Foreign-key enforcement is temporarily disabled for the rebuild and restored
// immediately after, matching SQLite's documented drop-column procedure.
func (s *Store) DropLegacyOAuthScopeColumns(ctx context.Context) error {
	has, err := s.tableHasColumn(ctx, "oauth_grants", "scopes_json")
	if err != nil {
		return err
	}
	if !has {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `pragma foreign_keys=off`); err != nil {
		return err
	}
	defer func() { _, _ = s.db.ExecContext(ctx, `pragma foreign_keys=on`) }()

	type rebuild struct {
		table   string
		create  string
		copy    string
		indexes []string
	}
	rebuilds := []rebuild{
		{
			table: "oauth_grants",
			create: `create table oauth_grants_v2 (
				id text primary key,
				client_id text not null references oauth_clients(id) on delete cascade,
				user_id integer not null references users(id) on delete cascade,
				principal_id text not null unique references api_principals(id) on delete cascade,
				access_level text not null default 'read',
				resource_boundary_v2_json text not null default '{}',
				approval_profile_id text not null references oauth_approval_profiles(id) on delete restrict,
				offline_access integer not null default 0,
				policy_version integer not null default 1,
				role_version integer not null default 1,
				consent_version integer not null default 1,
				status text not null default 'active',
				expires_at text,
				last_used_at text,
				revoked_at text,
				revoke_reason text not null default '',
				created_at text not null)`,
			copy: `insert into oauth_grants_v2(id,client_id,user_id,principal_id,access_level,resource_boundary_v2_json,approval_profile_id,offline_access,policy_version,role_version,consent_version,status,expires_at,last_used_at,revoked_at,revoke_reason,created_at)
				select id,client_id,user_id,principal_id,access_level,resource_boundary_v2_json,approval_profile_id,offline_access,policy_version,role_version,consent_version,status,expires_at,last_used_at,revoked_at,revoke_reason,created_at from oauth_grants`,
			indexes: []string{
				`create index if not exists idx_oauth_grants_user_client on oauth_grants(user_id,client_id,created_at desc)`,
			},
		},
		{
			table: "oauth_authorization_codes",
			create: `create table oauth_authorization_codes_v2 (
				code_hash text primary key,
				grant_id text not null references oauth_grants(id) on delete cascade,
				client_id text not null references oauth_clients(id) on delete cascade,
				user_id integer not null references users(id) on delete cascade,
				principal_id text not null references api_principals(id) on delete cascade,
				redirect_uri text not null,
				resource text not null,
				code_challenge text not null,
				expires_at text not null,
				created_at text not null)`,
			copy: `insert into oauth_authorization_codes_v2(code_hash,grant_id,client_id,user_id,principal_id,redirect_uri,resource,code_challenge,expires_at,created_at)
				select code_hash,grant_id,client_id,user_id,principal_id,redirect_uri,resource,code_challenge,expires_at,created_at from oauth_authorization_codes`,
		},
		{
			table: "oauth_access_tokens",
			create: `create table oauth_access_tokens_v2 (
				token_hash text primary key,
				grant_id text not null references oauth_grants(id) on delete cascade,
				principal_id text not null references api_principals(id) on delete cascade,
				client_id text not null references oauth_clients(id) on delete cascade,
				user_id integer not null references users(id) on delete cascade,
				resource text not null,
				expires_at text not null,
				revoked_at text,
				created_at text not null)`,
			copy: `insert into oauth_access_tokens_v2(token_hash,grant_id,principal_id,client_id,user_id,resource,expires_at,revoked_at,created_at)
				select token_hash,grant_id,principal_id,client_id,user_id,resource,expires_at,revoked_at,created_at from oauth_access_tokens`,
			indexes: []string{
				`create index if not exists idx_oauth_access_grant on oauth_access_tokens(grant_id,expires_at)`,
			},
		},
		{
			table: "oauth_refresh_tokens",
			create: `create table oauth_refresh_tokens_v2 (
				token_hash text primary key,
				family_id text not null,
				grant_id text not null references oauth_grants(id) on delete cascade,
				parent_token_hash text not null default '',
				principal_id text not null references api_principals(id) on delete cascade,
				client_id text not null references oauth_clients(id) on delete cascade,
				user_id integer not null references users(id) on delete cascade,
				resource text not null,
				expires_at text not null,
				consumed_at text,
				revoked_at text,
				reuse_detected_at text,
				created_at text not null)`,
			copy: `insert into oauth_refresh_tokens_v2(token_hash,family_id,grant_id,parent_token_hash,principal_id,client_id,user_id,resource,expires_at,consumed_at,revoked_at,reuse_detected_at,created_at)
				select token_hash,family_id,grant_id,parent_token_hash,principal_id,client_id,user_id,resource,expires_at,consumed_at,revoked_at,reuse_detected_at,created_at from oauth_refresh_tokens`,
			indexes: []string{
				`create index if not exists idx_oauth_refresh_grant on oauth_refresh_tokens(grant_id,family_id)`,
			},
		},
	}
	// Pre-check each table before opening the transaction so the single
	// connection is never blocked by a nested query during the rebuild.
	pending := make([]rebuild, 0, len(rebuilds))
	for _, item := range rebuilds {
		stillPresent, err := s.tableHasColumn(ctx, item.table, "scopes_json")
		if err != nil {
			return err
		}
		if stillPresent {
			pending = append(pending, item)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range pending {
		if _, err := tx.ExecContext(ctx, item.create); err != nil {
			return fmt.Errorf("create %s_v2: %w", item.table, err)
		}
		if _, err := tx.ExecContext(ctx, item.copy); err != nil {
			return fmt.Errorf("copy %s: %w", item.table, err)
		}
		if _, err := tx.ExecContext(ctx, `drop table `+item.table); err != nil {
			return fmt.Errorf("drop %s: %w", item.table, err)
		}
		if _, err := tx.ExecContext(ctx, `alter table `+item.table+`_v2 rename to `+item.table); err != nil {
			return fmt.Errorf("rename %s: %w", item.table, err)
		}
		for _, index := range item.indexes {
			if _, err := tx.ExecContext(ctx, index); err != nil {
				return fmt.Errorf("index %s: %w", item.table, err)
			}
		}
	}
	return tx.Commit()
}
