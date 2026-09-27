package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func openServerDNSCustomStore(t *testing.T) (*Store, *model.Server) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dns-custom.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	server := &model.Server{Name: "edge", Status: model.ServerOnline}
	if err := s.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureServerDNSPolicy(context.Background(), server.ID); err != nil {
		t.Fatal(err)
	}
	return s, server
}

func ownedDNSListCount(t *testing.T, s *Store, serverID int64) int {
	t.Helper()
	var count int
	if err := s.db.QueryRowContext(context.Background(), `select count(*) from dns_lists where owner_server_id=?`, serverID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestServerDNSPolicyCustomResolversLifecycle(t *testing.T) {
	ctx := context.Background()
	s, server := openServerDNSCustomStore(t)
	shared, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if shared.EncryptedSource != model.DNSSourceShared || shared.BootstrapSource != model.DNSSourceShared {
		t.Fatalf("default policy sources = %s/%s, want shared/shared", shared.EncryptedSource, shared.BootstrapSource)
	}
	defaultBootstrapID := shared.BootstrapListID

	custom := model.ServerDNSPolicy{ServerID: server.ID, Strategy: "auto", AutoTest: model.DNSAutoTestFirstApply,
		EncryptedCandidates: []model.DNSCandidate{{Transport: model.DNSTransportDoT, Server: "dns.example.net", Port: 853}},
		BootstrapCandidates: []model.DNSCandidate{{Transport: model.DNSTransportUDP, Server: "9.9.9.9", Port: 53}},
	}
	if err := s.UpdateServerDNSPolicy(ctx, &custom); err != nil {
		t.Fatalf("set custom resolvers: %v", err)
	}
	if custom.EncryptedSource != model.DNSSourceCustom || custom.BootstrapSource != model.DNSSourceCustom {
		t.Fatalf("custom policy sources = %s/%s", custom.EncryptedSource, custom.BootstrapSource)
	}
	if len(custom.EncryptedCandidates) != 1 || custom.EncryptedCandidates[0].Tag != "custom-1" {
		t.Fatalf("custom encrypted candidates = %#v, want one tagged custom-1", custom.EncryptedCandidates)
	}
	if !custom.NeedsBenchmark || custom.Revision != shared.Revision+1 {
		t.Fatalf("custom write must bump revision and require a benchmark: %#v", custom)
	}
	if got := ownedDNSListCount(t, s, server.ID); got != 2 {
		t.Fatalf("owned lists = %d, want 2", got)
	}
	lists, err := s.ListDNSLists(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range lists {
		if list.ID == custom.EncryptedListID && list.OwnerServerID != server.ID {
			t.Fatalf("custom encrypted list owner = %d, want %d", list.OwnerServerID, server.ID)
		}
	}

	// Writing the bound ids back without candidates keeps the custom lists.
	keep := model.ServerDNSPolicy{ServerID: server.ID, EncryptedListID: custom.EncryptedListID, BootstrapListID: custom.BootstrapListID, Strategy: "auto", AutoTest: model.DNSAutoTestFirstApply}
	if err := s.UpdateServerDNSPolicy(ctx, &keep); err != nil {
		t.Fatalf("keep custom resolvers: %v", err)
	}
	if keep.Revision != custom.Revision || keep.EncryptedSource != model.DNSSourceCustom {
		t.Fatalf("unchanged custom write changed the policy: %#v", keep)
	}

	// New content under the same list id bumps the list and resets selection.
	if _, err := s.db.ExecContext(ctx, `update server_dns_policies set encrypted_selected_json=?,encrypted_selection_revision=1,needs_benchmark=0 where server_id=?`, `[{"tag":"custom-1","transport":"dot","server":"dns.example.net","port":853}]`, server.ID); err != nil {
		t.Fatal(err)
	}
	edited := keep
	edited.EncryptedCandidates = []model.DNSCandidate{{Tag: "primary", Transport: model.DNSTransportDoH, Server: "doh.example.net", Port: 443, Path: "/dns-query"}}
	if err := s.UpdateServerDNSPolicy(ctx, &edited); err != nil {
		t.Fatalf("edit custom resolvers: %v", err)
	}
	if edited.EncryptedListID != custom.EncryptedListID {
		t.Fatalf("editing custom resolvers must reuse the owned list: %d != %d", edited.EncryptedListID, custom.EncryptedListID)
	}
	if edited.Revision != keep.Revision+1 || !edited.NeedsBenchmark || len(edited.EncryptedSelected) != 0 || edited.EncryptedSelectionRevision != 0 {
		t.Fatalf("content change must invalidate the encrypted selection: %#v", edited)
	}

	// Plain DNS only plus a shared bootstrap list drops both owned lists.
	plain := model.ServerDNSPolicy{ServerID: server.ID, EncryptedListID: 0, BootstrapListID: defaultBootstrapID, Strategy: "auto", AutoTest: model.DNSAutoTestFirstApply}
	if err := s.UpdateServerDNSPolicy(ctx, &plain); err != nil {
		t.Fatalf("switch to plain dns only: %v", err)
	}
	if plain.EncryptedSource != model.DNSSourceNone || plain.BootstrapSource != model.DNSSourceShared || len(plain.EncryptedCandidates) != 0 {
		t.Fatalf("plain policy = %#v", plain)
	}
	if got := ownedDNSListCount(t, s, server.ID); got != 0 {
		t.Fatalf("owned lists after leaving custom = %d, want 0", got)
	}
}

func TestServerDNSPolicyCustomResolversBoundaries(t *testing.T) {
	ctx := context.Background()
	s, server := openServerDNSCustomStore(t)
	other := &model.Server{Name: "other", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, other); err != nil {
		t.Fatal(err)
	}
	current, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	custom := model.ServerDNSPolicy{ServerID: server.ID, EncryptedListID: 0, BootstrapListID: current.BootstrapListID,
		EncryptedCandidates: []model.DNSCandidate{{Transport: model.DNSTransportDoT, Server: "dns.example.net", Port: 853}}}
	if err := s.UpdateServerDNSPolicy(ctx, &custom); err != nil {
		t.Fatal(err)
	}
	ownedID := custom.EncryptedListID

	both := model.ServerDNSPolicy{ServerID: server.ID, EncryptedListID: current.EncryptedListID, BootstrapListID: current.BootstrapListID, EncryptedCandidates: custom.EncryptedCandidates}
	if err := s.UpdateServerDNSPolicy(ctx, &both); err == nil {
		t.Fatal("a shared list plus custom candidates must be rejected")
	}
	if _, err := s.EnsureServerDNSPolicy(ctx, other.ID); err != nil {
		t.Fatal(err)
	}
	foreign := model.ServerDNSPolicy{ServerID: other.ID, EncryptedListID: ownedID, BootstrapListID: current.BootstrapListID}
	if err := s.UpdateServerDNSPolicy(ctx, &foreign); err == nil {
		t.Fatal("another server must not bind a server's custom list")
	}
	noBootstrap := model.ServerDNSPolicy{ServerID: server.ID}
	if err := s.UpdateServerDNSPolicy(ctx, &noBootstrap); err == nil {
		t.Fatal("a policy without bootstrap resolvers must be rejected")
	}

	owned, err := s.GetDNSList(ctx, ownedID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateDNSList(ctx, owned); !errors.Is(err, ErrServerOwnedDNSList) {
		t.Fatalf("UpdateDNSList(owned) = %v, want ErrServerOwnedDNSList", err)
	}
	if err := s.DeleteDNSList(ctx, ownedID); !errors.Is(err, ErrServerOwnedDNSList) {
		t.Fatalf("DeleteDNSList(owned) = %v, want ErrServerOwnedDNSList", err)
	}
	if _, err := s.SetDefaultDNSList(ctx, ownedID); !errors.Is(err, ErrServerOwnedDNSList) {
		t.Fatalf("SetDefaultDNSList(owned) = %v, want ErrServerOwnedDNSList", err)
	}

	if err := s.DeleteServer(ctx, server.ID); err != nil {
		t.Fatalf("delete server with custom resolvers: %v", err)
	}
	if got := ownedDNSListCount(t, s, server.ID); got != 0 {
		t.Fatalf("owned lists after server deletion = %d, want 0", got)
	}
}

// TestServerDNSListOwnerMigratesFromSharedOnlySchema starts from the previous
// dns_lists schema, which had no owner_server_id column, and checks that the
// existing lists stay shared and a custom policy can be stored afterwards.
func TestServerDNSListOwnerMigratesFromSharedOnlySchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dns-lists-shared-only.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "edge", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, err := s.EnsureServerDNSPolicy(ctx, server.ID)
	if err != nil {
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
		`drop index if exists idx_dns_lists_owner_kind`,
		`alter table dns_lists drop column owner_server_id`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("prepare previous dns_lists schema with %q: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("migrate dns_lists owner column: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeated migration is not idempotent: %v", err)
	}
	lists, err := s.ListDNSLists(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(lists) == 0 {
		t.Fatal("expected the existing default lists to survive")
	}
	for _, list := range lists {
		if list.OwnerServerID != 0 {
			t.Fatalf("existing list %d migrated with owner %d, want shared", list.ID, list.OwnerServerID)
		}
	}
	migrated, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.EncryptedListID != policy.EncryptedListID || migrated.EncryptedSource != model.DNSSourceShared {
		t.Fatalf("migrated policy = %#v", migrated)
	}
	custom := model.ServerDNSPolicy{ServerID: server.ID, EncryptedListID: migrated.EncryptedListID,
		BootstrapCandidates: []model.DNSCandidate{{Transport: model.DNSTransportUDP, Server: "9.9.9.9", Port: 53}}}
	if err := s.UpdateServerDNSPolicy(ctx, &custom); err != nil {
		t.Fatalf("store custom bootstrap resolvers after migration: %v", err)
	}
	if custom.BootstrapSource != model.DNSSourceCustom {
		t.Fatalf("bootstrap source = %s, want custom", custom.BootstrapSource)
	}
}

// A benchmark task that fails before the Agent reports any result must still
// surface on the policy it tested; otherwise a plain-DNS-only policy with an
// intranet resolver stays "waiting for a test" forever with no error.
func TestFailedDNSBenchmarkTaskRecordsPolicyFailure(t *testing.T) {
	ctx := context.Background()
	s, server := openServerDNSCustomStore(t)
	policy := model.ServerDNSPolicy{ServerID: server.ID, Strategy: "auto", AutoTest: model.DNSAutoTestFirstApply,
		BootstrapCandidates: []model.DNSCandidate{{Transport: model.DNSTransportUDP, Server: "10.10.10.10", Port: 53}},
	}
	if err := s.UpdateServerDNSPolicy(ctx, &policy); err != nil {
		t.Fatal(err)
	}
	bootstrap, err := s.GetDNSList(ctx, policy.BootstrapListID)
	if err != nil {
		t.Fatal(err)
	}
	queue := func(requestID string) int64 {
		t.Helper()
		task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeBenchmarkDNS, PayloadJSON: "{}", Status: "pending", Nonce: requestID}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		run := model.DNSBenchmarkRun{RequestID: requestID, ServerID: server.ID, PolicyRevision: policy.Revision, BootstrapListID: bootstrap.ID, BootstrapListRevision: bootstrap.Revision, Trigger: "manual"}
		if err := s.CreateDNSBenchmarkRun(ctx, &run); err != nil {
			t.Fatal(err)
		}
		if err := s.AttachDNSBenchmarkTask(ctx, requestID, task.ID); err != nil {
			t.Fatal(err)
		}
		return task.ID
	}

	// The no-usable-candidates sentinel belongs to a real report only.
	if err := s.FailDNSBenchmarkRunForTask(ctx, queue("sentinel"), `{"error":"`+model.DNSBenchmarkNoUsableCandidatesError+`"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetServerDNSPolicy(ctx, server.ID); got.LastError != "" || got.LastAttemptAt != nil {
		t.Fatalf("task failure stored the local-dns fallback sentinel: %#v", got)
	}

	if err := s.FailDNSBenchmarkRunForTask(ctx, queue("rejected"), `{"message":"dns benchmark failed","error":"bootstrap candidates: candidate[0]: rejected"}`); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServerDNSPolicy(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastError != "bootstrap candidates: candidate[0]: rejected" || got.LastAttemptAt == nil || !got.NeedsBenchmark {
		t.Fatalf("failed task not recorded on policy: %#v", got)
	}

	// A run for a policy that has since changed leaves the new policy alone.
	stale := queue("stale")
	changed := model.ServerDNSPolicy{ServerID: server.ID, Strategy: "auto", AutoTest: model.DNSAutoTestFirstApply,
		BootstrapCandidates: []model.DNSCandidate{{Transport: model.DNSTransportUDP, Server: "10.10.10.11", Port: 53}},
	}
	if err := s.UpdateServerDNSPolicy(ctx, &changed); err != nil {
		t.Fatal(err)
	}
	if err := s.FailDNSBenchmarkRunForTask(ctx, stale, `{"error":"timeout"}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetServerDNSPolicy(ctx, server.ID); got.LastError != "" {
		t.Fatalf("stale run failure overwrote the current policy: %#v", got)
	}
}
