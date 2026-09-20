package store

import (
	"context"
	"fmt"
)

// MigratePluginExtensionsSchema must run after the base plugin schema.
func (s *Store) MigratePluginExtensionsSchema(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`create table if not exists plugin_installations (
 plugin_id integer primary key references plugins(id) on delete restrict,
 package_id text not null unique,
 active_revision_id integer references plugin_revisions(id) on delete restrict,
 installed integer not null default 1 check(installed in (0,1)),
 config_json text not null default '{}', updated_at text not null
 )`,
		`create table if not exists plugin_package_versions (
 revision_id integer primary key references plugin_revisions(id) on delete restrict,
 plugin_id integer not null references plugin_installations(plugin_id) on delete restrict,
 version text not null, sha256 text not null, manifest_json text not null,
 ui_json text not null default 'null', source_kind text not null,
 source_repository text not null default '', source_commit text not null default '', created_at text not null,
 unique(plugin_id,version), unique(plugin_id,sha256)
 )`,
		`create table if not exists plugin_installation_secrets (
 plugin_id integer not null references plugin_installations(plugin_id) on delete restrict,
 name text not null, value_encrypted text not null, updated_at text not null,
 primary key(plugin_id,name)
 )`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("plugin extensions schema: %w", err)
		}
	}
	return tx.Commit()
}
