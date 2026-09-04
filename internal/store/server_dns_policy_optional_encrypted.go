package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ensureOptionalEncryptedDNSList rebuilds server_dns_policies so
// encrypted_list_id is nullable. A server may resolve through the plain
// bootstrap resolvers only, which is stored as SQL NULL; the old schema
// declared the column NOT NULL with a foreign key, so 0 could not be stored and
// the operator was forced to pick an encrypted list. Existing rows keep their
// bound list. The rebuild is skipped once the column is already nullable, so it
// is idempotent across restarts.
func (s *Store) ensureOptionalEncryptedDNSList(ctx context.Context) error {
	exists, err := s.tableExists(ctx, "server_dns_policies")
	if err != nil || !exists {
		return err
	}
	var notNull int
	if err := s.db.QueryRowContext(ctx, `select "notnull" from pragma_table_info('server_dns_policies') where name='encrypted_list_id'`).Scan(&notNull); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if notNull == 0 {
		return nil
	}
	if err := s.dropTriggersReferencingTable(ctx, "server_dns_policies"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`create table server_dns_policies_optional_encrypted (
			server_id integer primary key references servers(id) on delete cascade,
			encrypted_list_id integer references dns_lists(id) on delete restrict,
			bootstrap_list_id integer not null references dns_lists(id) on delete restrict,
			revision integer not null default 1,
			strategy text not null default 'auto',
			auto_test text not null default 'first_apply',
			test_interval_seconds integer not null default 3600,
			encrypted_selected_json text not null default '[]',
			bootstrap_selected_json text not null default '[]',
			encrypted_selection_revision integer not null default 0,
			bootstrap_selection_revision integer not null default 0,
			last_attempt_at text,
			last_success_at text,
			last_error text not null default '',
			needs_benchmark integer not null default 1,
			created_at text not null,
			updated_at text not null
		)`,
		`insert into server_dns_policies_optional_encrypted(server_id,encrypted_list_id,bootstrap_list_id,revision,strategy,auto_test,test_interval_seconds,encrypted_selected_json,bootstrap_selected_json,encrypted_selection_revision,bootstrap_selection_revision,last_attempt_at,last_success_at,last_error,needs_benchmark,created_at,updated_at)
			select server_id,nullif(encrypted_list_id,0),bootstrap_list_id,revision,strategy,auto_test,test_interval_seconds,encrypted_selected_json,bootstrap_selected_json,encrypted_selection_revision,bootstrap_selection_revision,last_attempt_at,last_success_at,last_error,needs_benchmark,created_at,updated_at from server_dns_policies`,
		`drop table server_dns_policies`,
		`alter table server_dns_policies_optional_encrypted rename to server_dns_policies`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate optional encrypted dns list: %w", err)
		}
	}
	return tx.Commit()
}
