package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestDeviceRetirementPreflightIncludesPathNodesAndMissingAccountToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scope.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
 create table users(id integer primary key,subscription_token text);
 create table user_devices(user_id integer,status text,subscription_suspended integer,proxy_access_state text);
 create table proxy_credentials(user_id integer,inbound_id integer,path_id integer,device_id_hash text,status text);
 create table inbounds(id integer primary key,server_id integer);
 create table proxy_path_steps(path_id integer,server_id integer,inbound_id integer);
 create table configuration_sync_states(server_id integer,state text);
 insert into users values(1,'');
 insert into user_devices values(1,'active',0,'active');
 insert into inbounds values(10,100),(11,102);
 insert into proxy_credentials values(1,10,20,'legacy','active');
 insert into proxy_path_steps values(20,100,10),(20,101,null),(20,null,11);
 insert into configuration_sync_states values(100,'synced'),(101,'pending');
 `)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := PreviewDeviceRetirementDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if out.AffectedNodes != 3 || out.PendingNodeSync != 2 || out.MissingAccountSubscriptions != 1 || out.NodeScopeComplete || out.NodeScopeReason == "" || out.NodeRevocationConfirmed {
		t.Fatalf("incorrect bounds: %+v", out)
	}
}
