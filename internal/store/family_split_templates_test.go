package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestOpenEmptyDatabaseCreatesNullableProxyPathInbound(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "empty.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var notNull int
	if err := s.db.QueryRowContext(ctx, `select "notnull" from pragma_table_info('proxy_paths') where name='inbound_id'`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatalf("empty-database proxy_paths.inbound_id notnull=%d, want 0", notNull)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeated empty-database migration is not idempotent: %v", err)
	}
}

func TestNullableProxyPathInboundMigratesWithRevisionTriggers(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nullable-inbound-triggers.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "entry", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "::", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := s.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	item := model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "keep-me", Enabled: true}
	if err := s.CreateProxyPath(ctx, &item); err != nil {
		t.Fatal(err)
	}
	step := model.ProxyPathStep{PathID: item.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &server.ID, ConfigJSON: `{}`}
	if err := s.CreateProxyPathStep(ctx, &step); err != nil {
		t.Fatal(err)
	}
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
	rows, err := raw.Query(`select name, sql from sqlite_master where type='trigger' and (tbl_name='proxy_paths' or instr(coalesce(sql,''), 'proxy_paths') > 0)`)
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
		t.Fatal("expected revision triggers that reference proxy_paths")
	}
	for _, item := range saved {
		if _, err := raw.Exec(`drop trigger if exists ` + item.name); err != nil {
			t.Fatalf("drop %s for previous-schema rebuild: %v", item.name, err)
		}
	}
	for _, statement := range []string{
		`create table proxy_paths_notnull (
			id integer primary key autoincrement,
			inbound_id integer not null references inbounds(id) on delete cascade,
			kind text not null default 'chain',
			branch_source_step_id integer references proxy_path_steps(id) on delete set null,
			name_mode text not null default 'auto',
			name_template_json text not null default '[]',
			exit_region_mode text not null default 'auto',
			exit_region_code text not null default '',
			secret text not null default '',
			enabled integer not null default 1,
			template_id integer references family_split_templates(id) on delete cascade,
			family text not null default '',
			created_at text not null,
			updated_at text not null
		)`,
		`insert into proxy_paths_notnull(id,inbound_id,kind,branch_source_step_id,name_mode,name_template_json,exit_region_mode,exit_region_code,secret,enabled,template_id,family,created_at,updated_at)
			select id,inbound_id,kind,branch_source_step_id,name_mode,name_template_json,exit_region_mode,exit_region_code,secret,enabled,template_id,family,created_at,updated_at from proxy_paths`,
		`drop table proxy_paths`,
		`alter table proxy_paths_notnull rename to proxy_paths`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare previous not-null inbound schema with %q: %v", statement, err)
		}
	}
	for _, item := range saved {
		if _, err := raw.Exec(item.sql); err != nil {
			t.Fatalf("restore trigger %s: %v", item.name, err)
		}
	}
	var insertSQL string
	if err := raw.QueryRow(`select sql from sqlite_master where type='trigger' and name='config_rev_proxy_path_steps_insert'`).Scan(&insertSQL); err != nil {
		t.Fatalf("config_rev_proxy_path_steps_insert missing after restore: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("migrate nullable proxy path inbound with leftover revision triggers: %v", err)
	}
	defer s.Close()
	var notNull int
	if err := s.db.QueryRowContext(ctx, `select "notnull" from pragma_table_info('proxy_paths') where name='inbound_id'`).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatalf("migrated proxy_paths.inbound_id notnull=%d, want 0", notNull)
	}
	stored, err := s.GetProxyPath(ctx, item.ID)
	if err != nil || stored.Secret != "keep-me" || stored.InboundID != inbound.ID {
		t.Fatalf("migrated path=%#v err=%v", stored, err)
	}
	steps, err := s.ListProxyPathStepsForPath(ctx, item.ID)
	if err != nil || len(steps) != 1 || steps[0].ID != step.ID {
		t.Fatalf("migrated steps=%#v err=%v", steps, err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeated nullable inbound migration is not idempotent: %v", err)
	}
}
