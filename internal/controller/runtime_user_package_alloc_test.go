package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

// hotPathFleetFixture is the shape the load actually has in production: several
// enrolled servers, each with one runtime-managed inbound, and one shared user
// population authorized on all of them. Credential decryption and the effective
// access snapshot both scale with that population, so a single-user fixture
// hides the cost the fleet poll cycle really pays.
func hotPathFleetFixture(t testing.TB, servers, users int) (*store.Store, *Server, []model.Server) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := newTestServer(db, "fleet-hot-path-secret", "")

	planNodes := make([]model.SubscriptionPlanNode, 0, servers)
	out := make([]model.Server, 0, servers)
	for i := 0; i < servers; i++ {
		server := &model.Server{
			Name: fmt.Sprintf("fleet-node-%02d", i), PublicIPv4: fmt.Sprintf("203.0.113.%d", i+1),
			AgentID: fmt.Sprintf("fleet-agent-%02d", i), AgentTokenHash: security.HashSecret(fmt.Sprintf("fleet-token-%02d", i)),
			Status:             model.ServerOnline,
			KernelCapabilities: []string{model.AgentCapabilityRuntimeUsers, model.KernelCapabilityRuntimeUsers, model.AgentCapabilityRuntimeUsersVLESS},
		}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		inbound := &model.Inbound{ServerID: server.ID, Name: "vless", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true, ConfigJSON: "{}"}
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
		planNodes = append(planNodes, model.SubscriptionPlanNode{NodeType: model.AssignableNodeInbound, NodeID: inbound.ID})
		out = append(out, *server)
	}
	// One plan carrying every node: SetUserPlanBindings keeps a single enabled
	// binding per user, so the fleet shape is "one plan, whole population".
	plan := &model.SubscriptionPlan{Name: "fleet-plan", Enabled: true}
	if err := db.CreateSubscriptionPlan(ctx, plan, planNodes); err != nil {
		t.Fatal(err)
	}
	bindings := make([]model.UserPlanBinding, 0, users)
	for i := 0; i < users; i++ {
		user := &model.User{
			Username: fmt.Sprintf("fleet-account-%04d", i), PasswordHash: "hash", Role: model.RoleViewer, Status: "active",
			ProxyUUID: fmt.Sprintf("11111111-1111-4111-8111-%012d", i), ProxyPassword: "password",
		}
		if err := db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, model.UserPlanBinding{UserID: user.ID, PlanID: plan.ID})
	}
	if err := db.SetUserPlanBindings(ctx, bindings); err != nil {
		t.Fatal(err)
	}
	if err := srv.InitializeProxyCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	return db, srv, out
}

// BenchmarkRuntimeUserPackageBuild measures the cold build that every Agent
// users-snapshot pull and every runtime-users sync wake pays when the package
// cache misses. A fleet poll cycle repeats this once per server, so its
// allocation cost is the Controller's dominant steady-state garbage source.
func BenchmarkRuntimeUserPackageBuild(b *testing.B) {
	ctx := context.Background()
	_, srv, server, _, _ := hotPathFixture(b)

	if _, err := srv.buildRuntimeUserPackage(ctx, *server, 1); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := srv.buildRuntimeUserPackage(ctx, *server, 1); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRuntimeUserPackageCachedRead measures the read path a warm cache is
// supposed to serve: no configuration generation, only the per-revision digest
// and whatever copying the caller needs.
func BenchmarkRuntimeUserPackageCachedRead(b *testing.B) {
	ctx := context.Background()
	_, srv, server, _, _ := hotPathFixture(b)

	if _, _, err := srv.currentRuntimeUserPackage(ctx, *server, 1); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := srv.currentRuntimeUserPackage(ctx, *server, 1); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRuntimeUserPackageFleetBuild is the production-shaped cold build:
// one server's package derived from a fleet-wide routing set.
func BenchmarkRuntimeUserPackageFleetBuild(b *testing.B) {
	ctx := context.Background()
	_, srv, servers := hotPathFleetFixture(b, 22, 200)
	server := servers[0]

	if _, err := srv.buildRuntimeUserPackage(ctx, server, 1); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := srv.buildRuntimeUserPackage(ctx, server, 1); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRuntimeUserPackageFleetPollCycle is one full fleet poll: every
// enrolled server pulls its package once. This is the loop that keeps a core
// busy in production, so it is the number that has to come down.
func BenchmarkRuntimeUserPackageFleetPollCycle(b *testing.B) {
	ctx := context.Background()
	_, srv, servers := hotPathFleetFixture(b, 22, 200)

	for _, server := range servers {
		if _, _, err := srv.currentRuntimeUserPackage(ctx, server, 1); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, server := range servers {
			if _, _, err := srv.currentRuntimeUserPackage(ctx, server, 1); err != nil {
				b.Fatal(err)
			}
		}
	}
}
