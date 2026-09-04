package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestOpenEmptyDatabaseCreatesOptionalEncryptedDNSList(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var notNull int
	if err := s.db.QueryRowContext(ctx, `select "notnull" from pragma_table_info('server_dns_policies') where name='encrypted_list_id'`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatalf("empty-database server_dns_policies.encrypted_list_id notnull=%d, want 0", notNull)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeated empty-database migration is not idempotent: %v", err)
	}
}

// TestOptionalEncryptedDNSListMigratesFromNotNullSchema starts from the real
// previous schema (encrypted_list_id NOT NULL with a dns_lists foreign key) and
// checks that an existing bound policy survives the rebuild and can then be
// switched to plain DNS only.
func TestOptionalEncryptedDNSListMigratesFromNotNullSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dns-policy-notnull.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "edge", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, err := s.EnsureServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if policy.EncryptedListID == 0 {
		t.Fatal("a new policy must bind the default encrypted list")
	}
	boundEncryptedID, boundBootstrapID := policy.EncryptedListID, policy.BootstrapListID
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`pragma foreign_keys=off`); err != nil {
		t.Fatal(err)
	}
	rows, err := raw.Query(`select name, sql from sqlite_master where type='trigger' and (tbl_name='server_dns_policies' or instr(coalesce(sql,''), 'server_dns_policies') > 0)`)
	if err != nil {
		t.Fatal(err)
	}
	type trigger struct{ name, sql string }
	var saved []trigger
	for rows.Next() {
		var item trigger
		if err := rows.Scan(&item.name, &item.sql); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		saved = append(saved, item)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(saved) == 0 {
		t.Fatal("expected revision triggers that reference server_dns_policies")
	}
	for _, item := range saved {
		if _, err := raw.Exec(`drop trigger if exists ` + item.name); err != nil {
			t.Fatalf("drop %s for previous-schema rebuild: %v", item.name, err)
		}
	}
	for _, statement := range []string{
		`create table server_dns_policies_notnull (
			server_id integer primary key references servers(id) on delete cascade,
			encrypted_list_id integer not null references dns_lists(id) on delete restrict,
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
		`insert into server_dns_policies_notnull select * from server_dns_policies`,
		`drop table server_dns_policies`,
		`alter table server_dns_policies_notnull rename to server_dns_policies`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare previous not-null encrypted list schema with %q: %v", statement, err)
		}
	}
	for _, item := range saved {
		if _, err := raw.Exec(item.sql); err != nil {
			t.Fatalf("restore trigger %s: %v", item.name, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("migrate optional encrypted dns list with leftover revision triggers: %v", err)
	}
	defer s.Close()
	var notNull int
	if err := s.db.QueryRowContext(ctx, `select "notnull" from pragma_table_info('server_dns_policies') where name='encrypted_list_id'`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatalf("migrated server_dns_policies.encrypted_list_id notnull=%d, want 0", notNull)
	}
	migrated, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.EncryptedListID != boundEncryptedID || migrated.BootstrapListID != boundBootstrapID {
		t.Fatalf("migrated policy lists = %d/%d, want %d/%d", migrated.EncryptedListID, migrated.BootstrapListID, boundEncryptedID, boundBootstrapID)
	}

	plain := *migrated
	plain.EncryptedListID = 0
	if err := s.UpdateServerDNSPolicy(ctx, &plain); err != nil {
		t.Fatalf("switch migrated policy to plain dns only: %v", err)
	}
	stored, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.EncryptedListID != 0 {
		t.Fatalf("stored encrypted_list_id = %d, want 0", stored.EncryptedListID)
	}
	if len(stored.EncryptedSelected) != 0 || stored.EncryptedSelectionRevision != 0 {
		t.Fatalf("dropping the encrypted list must clear its selection: %#v / %d", stored.EncryptedSelected, stored.EncryptedSelectionRevision)
	}
	var isNull int
	if err := s.db.QueryRowContext(ctx, `select encrypted_list_id is null from server_dns_policies where server_id=?`, server.ID).Scan(&isNull); err != nil {
		t.Fatal(err)
	}
	if isNull != 1 {
		t.Fatal("plain-only policy must store SQL NULL so the dns_lists foreign key stays satisfiable")
	}
}
