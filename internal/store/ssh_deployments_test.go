package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestSSHPasswordDeploymentsMigrateFromLegacySchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "migration-node", AgentID: "migration-agent", AgentTokenHash: "migration-token-hash", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "migration-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`drop table ssh_password_deployments`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`create table ssh_password_deployments (server_id integer not null references servers(id) on delete cascade, user_id integer not null references users(id) on delete cascade, password_digest text not null, config_version integer not null, updated_at text not null, primary key(server_id,user_id))`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`insert into ssh_password_deployments(server_id,user_id,password_digest,config_version,updated_at) values(?,?,?,?,?)`, server.ID, user.ID, "legacy-digest", 7, "2026-08-07T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("open with legacy ssh_password_deployments schema: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	deployments, err := s.ListSSHPasswordDeploymentsForUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("list deployments after migration: %v", err)
	}
	if len(deployments) != 1 {
		t.Fatalf("deployments after migration = %d, want 1", len(deployments))
	}
	got := deployments[0]
	if got.ServerID != server.ID || got.UserID != user.ID || got.DeviceIDHash != "" || got.CredentialEpoch != 0 || got.CredentialStatus != "active" || got.PasswordDigest != "legacy-digest" || got.ConfigVersion != 7 {
		t.Fatalf("migrated deployment = %#v", got)
	}
	var createSQL string
	if err := s.db.QueryRow(`select sql from sqlite_master where type='table' and name='ssh_password_deployments'`).Scan(&createSQL); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"device_id_hash", "credential_epoch", "credential_status", "primary key(server_id,user_id,device_id_hash,credential_epoch)"} {
		if !strings.Contains(createSQL, want) {
			t.Errorf("migrated table SQL missing %q: %s", want, createSQL)
		}
	}
	deviceHostKey := model.SSHServerHostKey{ServerID: server.ID, PublicKey: "pub", Fingerprint: "fp", PlanDigest: "plan", ConfigVersion: 9}
	deviceDeployment := model.SSHPasswordDeployment{ServerID: server.ID, UserID: user.ID, DeviceIDHash: "device-hash", CredentialEpoch: 3, CredentialStatus: "active", PasswordDigest: "device-digest", ConfigVersion: 9}
	if err := s.ApplySSHDeploymentState(ctx, deviceHostKey, []model.SSHPasswordDeployment{deviceDeployment}); err != nil {
		t.Fatalf("apply device deployment after migration: %v", err)
	}
	deployments, err = s.ListSSHPasswordDeploymentsForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 1 || deployments[0].DeviceIDHash != "device-hash" || deployments[0].CredentialEpoch != 3 || deployments[0].CredentialStatus != "active" {
		t.Fatalf("deployments after device write = %#v", deployments)
	}
}
