package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
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

func TestSubscriptionOutputFiltersRoundTripLimitsAndCorruption(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user := &model.User{Username: "filter-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-filter", ProxyPassword: "password-filter"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	output, err := db.GetDefaultSubscriptionOutput(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if output.Filters != nil {
		t.Fatalf("default output has filters: %#v", output.Filters)
	}
	output.Filters = []model.SubscriptionOutputFilter{
		{Type: model.SubscriptionOutputFilterKeepName, Value: "^东京"},
		{Type: model.SubscriptionOutputFilterDropProtocol, Value: "trojan"},
	}
	if err := db.SaveSubscriptionOutput(ctx, output); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := db.GetSubscriptionOutput(ctx, user.ID, output.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(roundTrip.Filters) != 2 || roundTrip.Filters[0].Value != "^东京" || roundTrip.Filters[1].Type != model.SubscriptionOutputFilterDropProtocol {
		t.Fatalf("filter round trip = %#v", roundTrip.Filters)
	}
	listed, err := db.ListSubscriptionOutputs(ctx, user.ID)
	if err != nil || len(listed) != 1 || len(listed[0].Filters) != 2 {
		t.Fatalf("listed outputs=%#v err=%v", listed, err)
	}
	// The persisted JSON stays within the size bound.
	raw, err := db.db.QueryContext(ctx, `select filters_json from subscription_outputs where id=?`, output.ID)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if raw.Next() {
		if err := raw.Scan(&stored); err != nil {
			t.Fatal(err)
		}
	}
	raw.Close()
	if stored == "" || len(stored) > maxSubscriptionOutputFiltersBytes {
		t.Fatalf("stored filters = %q (%d bytes)", stored, len(stored))
	}
	// Corrupted JSON degrades to no filtering without breaking reads.
	if _, err := db.db.ExecContext(ctx, `update subscription_outputs set filters_json='{broken' where id=?`, output.ID); err != nil {
		t.Fatal(err)
	}
	corrupted, err := db.GetSubscriptionOutput(ctx, user.ID, output.ID)
	if err != nil || corrupted.Filters != nil {
		t.Fatalf("corrupted filters read = %#v err=%v", corrupted.Filters, err)
	}
}

func TestSubscriptionOutputFiltersRejectOversizeAndUnvalidated(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	user := &model.User{Username: "oversize-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-oversize", ProxyPassword: "password-oversize"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	output, err := db.GetDefaultSubscriptionOutput(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	output.Filters = []model.SubscriptionOutputFilter{
		{Type: model.SubscriptionOutputFilterKeepName, Value: strings.Repeat("a", 9000)},
	}
	if err := db.SaveSubscriptionOutput(ctx, output); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("oversize filters accepted: %v", err)
	}
	// The store layer itself never validates rule semantics (the controller
	// does); it must still refuse a marshaled list that cannot exist.
	output.Filters = []model.SubscriptionOutputFilter{{Type: "keep_name", Value: "x"}}
	if err := db.SaveSubscriptionOutput(ctx, output); err != nil {
		t.Fatal(err)
	}
}

func TestSubscriptionOutputFiltersMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "filter-migration-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid-migration", ProxyPassword: "password-migration"}
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
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare previous schema with %q: %v", statement, err)
		}
	}
	// Recreate the pre-filter shape of the table and its join table.
	if _, err := raw.Exec(`create table subscription_outputs (id integer primary key autoincrement, user_id integer not null references users(id) on delete cascade, name text not null, is_default integer not null default 0, enabled integer not null default 1, created_at text not null, updated_at text not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`create table subscription_output_groups (output_id integer not null references subscription_outputs(id) on delete cascade, group_id integer not null references node_groups(id) on delete cascade, position integer not null, primary key(output_id,group_id), unique(output_id,position))`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("open previous schema: %v", err)
	}
	defer db.Close()
	hasColumn := false
	rows, err := db.db.QueryContext(ctx, `select name from pragma_table_info('subscription_outputs') where name='filters_json'`)
	if err != nil {
		t.Fatal(err)
	}
	hasColumn = rows.Next()
	rows.Close()
	if !hasColumn {
		t.Fatal("filters_json column missing after migration")
	}
	outputs, err := db.ListSubscriptionOutputs(ctx, user.ID)
	if err != nil || len(outputs) != 1 || outputs[0].Filters != nil {
		t.Fatalf("migrated outputs=%#v err=%v", outputs, err)
	}
	output := &outputs[0]
	output.Filters = []model.SubscriptionOutputFilter{{Type: model.SubscriptionOutputFilterKeepRegion, Value: "JP"}}
	if err := db.SaveSubscriptionOutput(ctx, output); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := db.GetSubscriptionOutput(ctx, user.ID, output.ID)
	if err != nil || len(roundTrip.Filters) != 1 || roundTrip.Filters[0].Value != "JP" {
		t.Fatalf("migrated filter round trip = %#v err=%v", roundTrip.Filters, err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	stable, err := db.GetSubscriptionOutput(ctx, user.ID, output.ID)
	if err != nil || len(stable.Filters) != 1 {
		t.Fatalf("migration rewrote filters: %#v err=%v", stable.Filters, err)
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
