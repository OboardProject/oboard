package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestPortForwardTargetServerBecomesNullable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "port-forward-target.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := &model.Server{Name: "source", Status: model.ServerOnline}
	target := &model.Server{Name: "target", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, target); err != nil {
		t.Fatal(err)
	}
	legacy := &model.PortForward{Name: "legacy", SourceServerID: source.ID, TargetServerID: target.ID, ListenPort: 10000, TargetPort: 443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendRealm, ProbeMode: "never", ProbeIntervalSeconds: 300, Priority: 100, ConfigJSON: "{}", Enabled: true}
	if err := db.CreatePortForward(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`drop index idx_port_forwards_source`,
		`alter table port_forwards rename to port_forwards_nullable`,
		`create table port_forwards (id integer primary key autoincrement, name text not null, source_server_id integer not null references servers(id) on delete cascade, target_server_id integer not null references servers(id) on delete cascade, listen_ip text not null default '', listen_port integer not null, target_address text not null default '', target_port integer not null, protocol text not null default 'tcp', backend text not null default 'auto', probe_mode text not null default 'apply', probe_interval_seconds integer not null default 300, sample_rate real not null default 0, priority integer not null default 100, config_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`insert into port_forwards(id,name,source_server_id,target_server_id,listen_ip,listen_port,target_address,target_port,protocol,backend,probe_mode,probe_interval_seconds,priority,config_json,enabled,created_at,updated_at) select id,name,source_server_id,target_server_id,listen_ip,listen_port,target_address,target_port,protocol,backend,probe_mode,probe_interval_seconds,priority,config_json,enabled,created_at,updated_at from port_forwards_nullable`,
		`drop table port_forwards_nullable`,
		`create index idx_port_forwards_source on port_forwards(source_server_id, enabled, priority)`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare previous port_forwards schema with %q: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("open previous port forward schema: %v", err)
	}
	defer db.Close()
	var notNull int
	if err := db.db.QueryRowContext(ctx, `select "notnull" from pragma_table_info('port_forwards') where name='target_server_id'`).Scan(&notNull); err != nil || notNull != 0 {
		t.Fatalf("target_server_id notnull=%d err=%v", notNull, err)
	}
	storedLegacy, err := db.GetPortForward(ctx, legacy.ID)
	if err != nil || storedLegacy.TargetServerID != target.ID {
		t.Fatalf("legacy forward was not preserved: %#v err=%v", storedLegacy, err)
	}
	external := &model.PortForward{Name: "external", SourceServerID: source.ID, ListenPort: 10001, TargetAddress: "203.0.113.80", TargetPort: 8443, Protocol: model.ForwardProtocolTCP, Backend: model.ForwardBackendRealm, ProbeMode: "never", ProbeIntervalSeconds: 300, Priority: 100, ConfigJSON: "{}", Enabled: true}
	if err := db.CreatePortForward(ctx, external); err != nil {
		t.Fatal(err)
	}
	var targetIsNull int
	if err := db.db.QueryRowContext(ctx, `select target_server_id is null from port_forwards where id=?`, external.ID).Scan(&targetIsNull); err != nil || targetIsNull != 1 {
		t.Fatalf("external target null=%d err=%v", targetIsNull, err)
	}
	storedExternal, err := db.GetPortForward(ctx, external.ID)
	if err != nil || storedExternal.TargetServerID != 0 || storedExternal.TargetAddress != external.TargetAddress {
		t.Fatalf("external forward round trip: %#v err=%v", storedExternal, err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
}
