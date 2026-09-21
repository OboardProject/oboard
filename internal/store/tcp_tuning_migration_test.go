package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestTCPTuningColumnMigrationFromPreviousSchema starts from a genuine database
// whose servers table predates tcp_tuning_enabled and verifies the ensureColumn
// migration: the column appears with the off default, existing rows keep their
// data and their own BBR choice, and reopening stays idempotent.
func TestTCPTuningColumnMigrationFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	previousSchema := `create table servers (id integer primary key autoincrement, name text not null, agent_id text unique, agent_token_hash text, chain_secret text not null, enrollment_hash text, enrollment_expires_at text, entry_address text, public_ipv4 text not null default '', public_ipv6 text not null default '', interface_ipv6 text not null default '', region_code text not null default '', detected_region_code text not null default '', region_mode text not null default 'auto', entry_ip_mode text not null default 'auto', listen_ip text, listen_mode text not null default 'auto', ip_stack text not null default 'auto', udp_inbound_mode text not null default 'allow', mtu_mode text not null default 'detect', mtu_value integer not null default 0, mtu_probe_host text not null default '1.1.1.1', mtu_probe_port integer not null default 443, mtu_overhead_bytes integer not null default 0, bbr_enabled integer not null default 0, stealth_enabled integer not null default 0, port_range_start integer not null default 10000, port_range_end integer not null default 20000, internal_port_range_start integer not null default 30000, internal_port_range_end integer not null default 59999, port_policy_revision integer not null default 1, status text not null, os text, distro_id text not null default '', distro_version text not null default '', distro_name text not null default '', libc text not null default '', service_manager text not null default '', package_manager text not null default '', arch text, kernel text, cpu text, cpu_cores integer not null default 0, memory_bytes integer not null default 0, cpu_usage_percent real not null default 0, memory_used_bytes integer not null default 0, memory_total_bytes integer not null default 0, agent_memory_bytes integer not null default 0, disk_bytes integer not null default 0, disk_total_bytes integer not null default 0, tcp_connection_count integer not null default 0, udp_connection_count integer not null default 0, process_count integer not null default 0, agent_version text not null default '', agent_build text not null default '', sing_box_version text, kernel_capabilities_json text not null default '[]', tcp_fastopen_state text not null default '', tcp_fastopen_value integer not null default 0, connection_audit_enabled integer not null default 0, display_tags_json text not null default '[]', last_seen_at text, created_at text not null, updated_at text not null)`
	if _, err := raw.ExecContext(ctx, previousSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, `insert into servers(name,chain_secret,status,entry_address,listen_ip,os,kernel,cpu,arch,sing_box_version,bbr_enabled,created_at,updated_at) values('pre-tcp-tuning','secret','online','203.0.113.9','0.0.0.0','linux','6.1.0','x86_64','amd64','1.14.0',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatalf("reopen with tcp tuning column: %v", err)
	}
	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || servers[0].Name != "pre-tcp-tuning" {
		t.Fatalf("migrated row lost data: %+v", servers)
	}
	if servers[0].TCPTuningEnabled {
		t.Fatal("migrated tcp_tuning_enabled must default to false")
	}
	if !servers[0].BBREnabled {
		t.Fatal("migration rewrote the existing BBR choice")
	}
	// An operator who turns the switch on must survive a reopen: ensureColumn
	// only adds a missing column and never rewrites saved values.
	updated := servers[0]
	updated.TCPTuningEnabled = true
	if err := db.UpdateServer(ctx, &updated); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	servers, err = again.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 || !servers[0].TCPTuningEnabled {
		t.Fatalf("reopen dropped the saved tuning switch: %+v", servers)
	}
}
