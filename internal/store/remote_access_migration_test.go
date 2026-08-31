package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestRemoteAccessTablesMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "remote-access.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "remote-node", AgentID: "remote-agent", AgentTokenHash: "token", ChainSecret: "chain", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`drop table if exists consumed_step_up_tokens`,
		`drop table if exists step_up_challenges`,
		`drop table if exists remote_access_audit`,
		`drop table if exists mcp_privileged_grants`,
		`drop table if exists server_remote_access_status`,
		`drop table if exists server_remote_access_policies`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	policy, err := s.GetServerRemoteAccessPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.RemoteTerminalEnabled || policy.MCPEnabled {
		t.Fatalf("remote control must default on while MCP control defaults off: %#v", policy)
	}
	status, err := s.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.LocalMode == "hardened" {
		t.Fatalf("missing status must not imply hardened: %#v", status)
	}
}
