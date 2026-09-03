package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// TestPortForwardBackendsCollapseOntoRealm starts from the previous schema:
// a sample_rate column, free-form backends and the sampling probe modes that
// only the removed builtin data path could serve, plus loopback port
// allocations owned by the removed trusted-forward inner listeners.
func TestPortForwardBackendsCollapseOntoRealm(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "port-forward-realm-only.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := &model.Server{Name: "source", Status: model.ServerOnline}
	target := &model.Server{Name: "target", Status: model.ServerOnline}
	for _, server := range []*model.Server{source, target} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`drop index idx_port_forwards_source`,
		`alter table port_forwards rename to port_forwards_legacy`,
		`create table port_forwards (id integer primary key autoincrement, name text not null, source_server_id integer not null references servers(id) on delete cascade, target_server_id integer references servers(id) on delete cascade, listen_ip text not null default '', listen_port integer not null, target_address text not null default '', target_port integer not null, protocol text not null default 'tcp', backend text not null default 'auto', probe_mode text not null default 'apply', probe_interval_seconds integer not null default 300, sample_rate real not null default 0, priority integer not null default 100, config_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`drop table port_forwards_legacy`,
		`create index idx_port_forwards_source on port_forwards(source_server_id, enabled, priority)`,
		`insert into port_forwards(id,name,source_server_id,target_server_id,listen_ip,listen_port,target_address,target_port,protocol,backend,probe_mode,probe_interval_seconds,sample_rate,priority,config_json,enabled,created_at,updated_at) values
			(1,'nft-rule',1,2,'0.0.0.0',10000,'203.0.113.2',443,'tcp','nft','periodic_sampled',300,0.25,100,'{}',1,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z'),
			(2,'builtin-rule',1,2,'0.0.0.0',10001,'203.0.113.2',444,'tcp','builtin','sampled',300,0.5,110,'{}',1,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z'),
			(3,'auto-rule',1,2,'0.0.0.0',10002,'203.0.113.2',445,'udp','auto','never',300,0,120,'{}',1,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`,
		`insert into proxy_path_port_allocations(kind,scope_key,server_id,pool,listen_ip,network,generation,ordinal,port,state,policy_revision,created_at,updated_at) values
			('trusted_forward_inner','7:2',2,'internal','127.0.0.1','tcp',1,0,40010,'active',1,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z'),
			('tunnel_ssh_loopback','555',2,'internal','127.0.0.1','tcp',1,0,40020,'active',1,'2026-09-01T00:00:00Z','2026-09-01T00:00:00Z')`,
	}
	for _, statement := range statements {
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

	forwards, err := db.ListPortForwards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(forwards) != 3 {
		t.Fatalf("migrated forwards = %#v, want the three legacy rules", forwards)
	}
	wantProbe := map[int64]string{1: "periodic", 2: "periodic", 3: "never"}
	for _, forward := range forwards {
		if forward.Backend != model.ForwardBackendRealm {
			t.Fatalf("forward %d backend = %q, want realm", forward.ID, forward.Backend)
		}
		if forward.ProbeMode != wantProbe[forward.ID] {
			t.Fatalf("forward %d probe_mode = %q, want %q", forward.ID, forward.ProbeMode, wantProbe[forward.ID])
		}
	}
	if forwards[0].ListenPort == 0 || forwards[0].TargetAddress == "" {
		t.Fatalf("migration lost forward payload: %#v", forwards[0])
	}

	var sampleRate int
	if err := db.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('port_forwards') where name='sample_rate'`).Scan(&sampleRate); err != nil {
		t.Fatal(err)
	}
	if sampleRate != 0 {
		t.Fatal("sample_rate column survived the migration")
	}

	allocations, err := db.ListProxyPathPortAllocations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allocations) != 1 || allocations[0].Kind != model.ProxyPathPortKindTunnelSSH {
		t.Fatalf("port allocations = %#v, want only the SSH loopback row", allocations)
	}

	// Reopening must be a no-op: the migration is keyed on the state it fixes.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen migrated schema: %v", err)
	}
	again, err := db.ListPortForwards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(forwards) || again[0].ID != forwards[0].ID {
		t.Fatalf("repeated migration changed rows: before=%#v after=%#v", forwards, again)
	}
}
