package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestNodeWorkspaceMigratesPreviousSchemaAndInitializesDefaults(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "existing-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
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
		`drop table subscription_output_groups`,
		`drop table subscription_outputs`,
		`drop table imported_nodes`,
		`drop table node_sources`,
		`drop table node_groups`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare previous schema with %q: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("open previous schema: %v", err)
	}
	defer db.Close()
	groups, err := db.ListNodeGroups(ctx, user.ID)
	if err != nil || len(groups) != 1 || groups[0].Kind != model.NodeGroupOBoard || groups[0].SystemKey != "oboard" {
		t.Fatalf("migrated groups=%#v err=%v", groups, err)
	}
	outputs, err := db.ListSubscriptionOutputs(ctx, user.ID)
	if err != nil || len(outputs) != 1 || !outputs[0].IsDefault || len(outputs[0].GroupIDs) != 1 || outputs[0].GroupIDs[0] != groups[0].ID {
		t.Fatalf("migrated outputs=%#v err=%v", outputs, err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	groupsAgain, _ := db.ListNodeGroups(ctx, user.ID)
	outputsAgain, _ := db.ListSubscriptionOutputs(ctx, user.ID)
	if len(groupsAgain) != 1 || len(outputsAgain) != 1 {
		t.Fatalf("repeat migration duplicated defaults: groups=%d outputs=%d", len(groupsAgain), len(outputsAgain))
	}
}

func TestNodeWorkspaceOwnershipAndSystemResourceProtection(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	first := &model.User{Username: "first-workspace", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-1", ProxyPassword: "password-1"}
	second := &model.User{Username: "second-workspace", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-2", ProxyPassword: "password-2"}
	if err := db.CreateUser(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(ctx, second); err != nil {
		t.Fatal(err)
	}
	groups, _ := db.ListNodeGroups(ctx, first.ID)
	if err := db.DeleteNodeGroup(ctx, first.ID, groups[0].ID); err == nil {
		t.Fatal("system OBoard group was deleted")
	}
	if _, err := db.GetNodeGroup(ctx, second.ID, groups[0].ID); err == nil {
		t.Fatal("cross-user node group read succeeded")
	}
	outputs, _ := db.ListSubscriptionOutputs(ctx, first.ID)
	if err := db.DeleteSubscriptionOutput(ctx, first.ID, outputs[0].ID); err == nil {
		t.Fatal("default subscription output was deleted")
	}
}
