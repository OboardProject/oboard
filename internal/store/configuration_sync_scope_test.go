package store

import (
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// drainedTargets runs the drain and reports which servers were marked pending
// by it, so a scope can be asserted as the set of servers that re-converge.
func drainedTargets(t *testing.T, db *Store) []int64 {
	t.Helper()
	ctx := context.Background()
	before := map[int64]uint64{}
	servers, err := db.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		state, err := db.ConfigurationSyncState(ctx, server.ID)
		if err == nil {
			before[server.ID] = state.WantedRevision
		}
	}
	for pass := 0; pass < 10; pass++ {
		intents, err := db.DrainConfigurationSyncIntents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(intents) == 0 {
			break
		}
	}
	var marked []int64
	for _, server := range servers {
		state, err := db.ConfigurationSyncState(ctx, server.ID)
		if err != nil {
			continue
		}
		if state.WantedRevision > before[server.ID] {
			marked = append(marked, server.ID)
		}
	}
	sort.Slice(marked, func(i, j int) bool { return marked[i] < marked[j] })
	return marked
}

// TestConfigurationScopeMarksOnlyRelatedServers pins the point of scoping an
// intent: editing one node must not make every enrolled server recompute its
// projection, and a hop change must still reach the whole chain it belongs to.
func TestConfigurationScopeMarksOnlyRelatedServers(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "scope.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	newServer := func(name, agent string) *model.Server {
		server := &model.Server{Name: name, AgentID: agent, Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		return server
	}
	entry, hop, unrelated := newServer("entry", "agent-entry"), newServer("hop", "agent-hop"), newServer("unrelated", "agent-unrelated")
	entryInbound := &model.Inbound{ServerID: entry.ID, Name: "entry-in", Protocol: model.ProtocolVLESS, Port: 443, ListenIP: "0.0.0.0", Enabled: true}
	if err := db.CreateInbound(ctx, entryInbound); err != nil {
		t.Fatal(err)
	}
	hopInbound := &model.Inbound{ServerID: hop.ID, Name: "hop-in", Protocol: model.ProtocolVLESS, Port: 444, ListenIP: "0.0.0.0", Enabled: true}
	if err := db.CreateInbound(ctx, hopInbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: entryInbound.ID, Kind: model.ProxyPathKindChain, Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	step := &model.ProxyPathStep{PathID: path.ID, Position: 0, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &hop.ID, InboundID: &hopInbound.ID}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	drainedTargets(t, db)

	// Editing the entry inbound reaches the chain it roots, not the fleet.
	entryInbound.Name = "entry-in-renamed"
	if err := db.UpdateInbound(ctx, entryInbound); err != nil {
		t.Fatal(err)
	}
	marked := drainedTargets(t, db)
	if len(marked) != 2 || marked[0] != entry.ID || marked[1] != hop.ID {
		t.Fatalf("inbound edit marked %v, want the entry and its hop only", marked)
	}

	// An unrelated server's own edit stays local.
	unrelated.Name = "unrelated-renamed"
	if err := db.UpdateServer(ctx, unrelated); err != nil {
		t.Fatal(err)
	}
	marked = drainedTargets(t, db)
	if len(marked) != 1 || marked[0] != unrelated.ID {
		t.Fatalf("server edit marked %v, want only itself", marked)
	}

	// Moving a hop to another server must reach both: the server that loses
	// the hop still has stale listeners and routes to drop.
	step.ServerID, step.InboundID = &unrelated.ID, nil
	unrelatedInbound := &model.Inbound{ServerID: unrelated.ID, Name: "unrelated-in", Protocol: model.ProtocolVLESS, Port: 445, ListenIP: "0.0.0.0", Enabled: true}
	if err := db.CreateInbound(ctx, unrelatedInbound); err != nil {
		t.Fatal(err)
	}
	drainedTargets(t, db)
	step.InboundID = &unrelatedInbound.ID
	if err := db.UpdateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	marked = drainedTargets(t, db)
	if len(marked) != 3 {
		t.Fatalf("moved hop marked %v, want the server it left, the one it joined, and the entry", marked)
	}

	// The periodic backstop still reaches everyone, which is what makes a
	// narrow mapping survivable.
	if err := db.QueueConfigurationSyncSweep(ctx); err != nil {
		t.Fatal(err)
	}
	drainedTargets(t, db)
	current, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{entry.ID, hop.ID, unrelated.ID} {
		state, err := db.ConfigurationSyncState(ctx, id)
		if err != nil || state.WantedRevision != current {
			t.Fatalf("sweep left server %d behind at %v (want %d): %v", id, state.WantedRevision, current, err)
		}
	}
}

// TestAuthorizationScopeFollowsGrantedNodes covers the authorization half of
// the mapping: a user, device, binding or plan change reaches the servers that
// actually serve that user's nodes, and leaves the rest of the fleet alone.
func TestAuthorizationScopeFollowsGrantedNodes(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "auth-scope.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	newServer := func(name, agent string) *model.Server {
		server := &model.Server{Name: name, AgentID: agent, Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		return server
	}
	granted, other := newServer("granted", "agent-granted"), newServer("other", "agent-other")
	grantedInbound := &model.Inbound{ServerID: granted.ID, Name: "granted-in", Protocol: model.ProtocolVLESS, Port: 443, ListenIP: "0.0.0.0", Enabled: true}
	if err := db.CreateInbound(ctx, grantedInbound); err != nil {
		t.Fatal(err)
	}
	otherInbound := &model.Inbound{ServerID: other.ID, Name: "other-in", Protocol: model.ProtocolVLESS, Port: 444, ListenIP: "0.0.0.0", Enabled: true}
	if err := db.CreateInbound(ctx, otherInbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "scoped", PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "scoped-plan", Enabled: true}
	if err := db.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: grantedInbound.ID}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: plan.ID, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	drainedTargets(t, db)

	// The plan grants a node on one server only.
	marked := drainedTargets(t, db)
	if len(marked) != 0 {
		t.Fatalf("baseline drain left work behind: %v", marked)
	}

	// An exception that grants a node on the other server reaches it, and only
	// it: the plan's own node has not changed.
	exception := &model.UserNodeException{UserID: user.ID, NodeType: model.AssignableNodeInbound, NodeID: otherInbound.ID, Effect: model.UserNodeExceptionAllow, Status: model.UserNodeExceptionActive}
	if err := db.CreateUserNodeException(ctx, exception); err != nil {
		t.Fatal(err)
	}
	marked = drainedTargets(t, db)
	if len(marked) != 1 || marked[0] != other.ID {
		t.Fatalf("exception marked %v, want the server hosting the excepted node", marked)
	}

	// Removing the plan binding reaches the server that was serving it.
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: 0}}); err != nil {
		t.Fatal(err)
	}
	marked = drainedTargets(t, db)
	if len(marked) == 0 {
		t.Fatal("unbinding a plan marked nothing")
	}
	for _, id := range marked {
		if id != granted.ID && id != other.ID {
			t.Fatalf("unbinding marked unrelated server %d", id)
		}
	}
}
