package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// The real previous state: a database whose server_dns_policies table was
// dropped by resetLegacyDNSSchema while servers already existed. Those servers
// kept failing every core-config build with "server N has no dns policy",
// because CreateServer only writes a policy for servers it creates and no read
// path ever repaired one.
func TestBackfillServerDNSPoliciesRepairsDroppedTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	kept := &model.Server{Name: "node-kept", Status: model.ServerOnline, AgentID: "agent-kept"}
	orphaned := &model.Server{Name: "node-orphaned", Status: model.ServerOnline, AgentID: "agent-orphaned"}
	for _, server := range []*model.Server{kept, orphaned} {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.ExecContext(ctx, `delete from server_dns_policies`); err != nil {
		t.Fatal(err)
	}
	policies, err := db.ListServerDNSPolicies(ctx)
	if err != nil || len(policies) != 0 {
		t.Fatalf("precondition failed: %d policies err=%v", len(policies), err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	repaired, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repaired.Close()
	policies, err = repaired.ListServerDNSPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 2 {
		t.Fatalf("expected one policy per server, got %d", len(policies))
	}
	byServer := map[int64]model.ServerDNSPolicy{}
	for _, policy := range policies {
		byServer[policy.ServerID] = policy
	}
	for _, server := range []*model.Server{kept, orphaned} {
		policy, ok := byServer[server.ID]
		if !ok {
			t.Fatalf("server %d was not repaired", server.ID)
		}
		if policy.BootstrapListID == 0 {
			t.Fatalf("server %d policy must bind a bootstrap list: %#v", server.ID, policy)
		}
		if policy.EncryptedListID == 0 {
			t.Fatalf("server %d policy should bind the protected encrypted list: %#v", server.ID, policy)
		}
	}
}

// A second start must not create duplicates or bump anything, and a server
// created after the repair keeps the policy CreateServer already wrote.
func TestBackfillServerDNSPoliciesIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "node", Status: model.ServerOnline, AgentID: "agent"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	before, err := db.ListServerDNSPolicies(ctx)
	if err != nil || len(before) != 1 {
		t.Fatalf("expected one policy, got %d err=%v", len(before), err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after, err := reopened.ListServerDNSPolicies(ctx)
	if err != nil || len(after) != 1 {
		t.Fatalf("expected one policy after restart, got %d err=%v", len(after), err)
	}
	if after[0].ServerID != before[0].ServerID || after[0].Revision != before[0].Revision ||
		after[0].EncryptedListID != before[0].EncryptedListID || after[0].BootstrapListID != before[0].BootstrapListID ||
		!after[0].UpdatedAt.Equal(before[0].UpdatedAt) {
		t.Fatalf("an untouched policy must not change:\nbefore %#v\nafter  %#v", before[0], after[0])
	}
}

// Without a protected default list there is nothing correct to bind, so the
// repair reports nothing and startup still succeeds.
func TestBackfillServerDNSPoliciesSkipsWithoutProtectedLists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "node", Status: model.ServerOnline, AgentID: "agent"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `delete from server_dns_policies`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, `update dns_lists set enabled=0 where protected=1`); err != nil {
		t.Fatal(err)
	}
	if err := db.backfillServerDNSPolicies(ctx); err != nil {
		t.Fatalf("a missing protected list must not fail startup: %v", err)
	}
	policies, err := db.ListServerDNSPolicies(ctx)
	if err != nil || len(policies) != 0 {
		t.Fatalf("nothing should have been bound: %d err=%v", len(policies), err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}
