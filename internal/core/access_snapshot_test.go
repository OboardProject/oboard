package core

import (
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func snapshotTestPlan(id int64, name string, nodes ...model.SubscriptionPlanNode) model.SubscriptionPlan {
	plan := model.SubscriptionPlan{ID: id, Name: name, Enabled: true, SpeedLimitMbps: 100, TrafficLimitBytes: 1 << 30, TrafficResetMode: "monthly", TrafficResetDay: 1}
	return plan
}

func snapshotTestNode(nodeType model.AssignableNodeType, nodeID int64) model.SubscriptionPlanNode {
	return model.SubscriptionPlanNode{PlanID: 1, NodeType: nodeType, NodeID: nodeID, Enabled: true}
}

func TestBuildEffectiveAccessSnapshotPriority(t *testing.T) {
	now := time.Now()
	alice := model.User{ID: 1, Username: "alice", Status: "active"}
	plan := snapshotTestPlan(1, "premium")
	snap := BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:     []model.User{alice},
		Bindings:  []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true}},
		Plans:     []model.SubscriptionPlan{plan},
		PlanNodes: []model.SubscriptionPlanNode{snapshotTestNode(model.AssignableNodeProxyPath, 10), snapshotTestNode(model.AssignableNodeProxyPath, 11)},
		Now:       now,
	})
	keys := snap.EffectiveNodeKeys(1)
	if len(keys) != 2 || !keys[NodeKeyOf(model.AssignableNodeProxyPath, 10)] || !keys[NodeKeyOf(model.AssignableNodeProxyPath, 11)] {
		t.Fatalf("plan nodes = %#v", keys)
	}

	// Deny beats plan.
	snap = BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:      []model.User{alice},
		Bindings:   []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true}},
		Plans:      []model.SubscriptionPlan{plan},
		PlanNodes:  []model.SubscriptionPlanNode{snapshotTestNode(model.AssignableNodeProxyPath, 10), snapshotTestNode(model.AssignableNodeProxyPath, 11)},
		Exceptions: []model.UserNodeException{{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 10, Effect: model.UserNodeExceptionDeny, ExpiresAt: now.Add(time.Hour)}},
		Now:        now,
	})
	keys = snap.EffectiveNodeKeys(1)
	if len(keys) != 1 || keys[NodeKeyOf(model.AssignableNodeProxyPath, 10)] {
		t.Fatalf("deny must remove node: %#v", keys)
	}

	// Allow adds a node outside the plan; deny still wins over an allow on the
	// same node.
	snap = BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:    []model.User{alice},
		Bindings: []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true}},
		Plans:    []model.SubscriptionPlan{plan},
		PlanNodes: []model.SubscriptionPlanNode{
			snapshotTestNode(model.AssignableNodeProxyPath, 10),
			snapshotTestNode(model.AssignableNodeProxyPath, 11),
		},
		Exceptions: []model.UserNodeException{
			{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 12, Effect: model.UserNodeExceptionAllow, ExpiresAt: now.Add(time.Hour)},
			{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 11, Effect: model.UserNodeExceptionDeny, ExpiresAt: now.Add(time.Hour)},
		},
		Now: now,
	})
	keys = snap.EffectiveNodeKeys(1)
	if len(keys) != 2 || !keys[NodeKeyOf(model.AssignableNodeProxyPath, 10)] || !keys[NodeKeyOf(model.AssignableNodeProxyPath, 12)] {
		t.Fatalf("allow/deny resolution = %#v", keys)
	}
	if snap.UserNodes[1][NodeKeyOf(model.AssignableNodeProxyPath, 12)].Source != "exception_allow" {
		t.Fatalf("allow provenance = %#v", snap.UserNodes[1][NodeKeyOf(model.AssignableNodeProxyPath, 12)])
	}

	// Expired exceptions are ignored.
	snap = BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:    []model.User{alice},
		Bindings: []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true}},
		Plans:    []model.SubscriptionPlan{plan},
		PlanNodes: []model.SubscriptionPlanNode{
			snapshotTestNode(model.AssignableNodeProxyPath, 10),
		},
		Exceptions: []model.UserNodeException{
			{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 10, Effect: model.UserNodeExceptionDeny, ExpiresAt: now.Add(-time.Hour)},
		},
		Now: now,
	})
	keys = snap.EffectiveNodeKeys(1)
	if len(keys) != 1 || !keys[NodeKeyOf(model.AssignableNodeProxyPath, 10)] {
		t.Fatalf("expired deny must be ignored: %#v", keys)
	}

	// Disabled plan and inactive users grant nothing.
	snap = BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:     []model.User{alice, {ID: 2, Username: "bob", Status: "disabled"}},
		Bindings:  []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true}, {UserID: 2, PlanID: 1, Enabled: true}},
		Plans:     []model.SubscriptionPlan{{ID: 1, Name: "off", Enabled: false}},
		PlanNodes: []model.SubscriptionPlanNode{snapshotTestNode(model.AssignableNodeProxyPath, 10)},
		Now:       now,
	})
	if len(snap.UserNodes) != 0 {
		t.Fatalf("disabled plan must grant nothing: %#v", snap.UserNodes)
	}
}

func TestBuildEffectiveAccessSnapshotTimeWindows(t *testing.T) {
	now := time.Now()
	alice := model.User{ID: 1, Username: "alice", Status: "active"}
	plan := snapshotTestPlan(1, "premium")
	future := now.Add(24 * time.Hour)
	past := now.Add(-24 * time.Hour)

	// Future start and expired bindings are not effective.
	snap := BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:     []model.User{alice},
		Bindings:  []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true, StartsAt: &future}},
		Plans:     []model.SubscriptionPlan{plan},
		PlanNodes: []model.SubscriptionPlanNode{snapshotTestNode(model.AssignableNodeProxyPath, 10)},
		Now:       now,
	})
	if len(snap.UserNodes) != 0 {
		t.Fatalf("future binding must not be effective: %#v", snap.UserNodes)
	}
	snap = BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:     []model.User{alice},
		Bindings:  []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true, ExpiresAt: &past}},
		Plans:     []model.SubscriptionPlan{plan},
		PlanNodes: []model.SubscriptionPlanNode{snapshotTestNode(model.AssignableNodeProxyPath, 10)},
		Now:       now,
	})
	if len(snap.UserNodes) != 0 {
		t.Fatalf("expired binding must not be effective: %#v", snap.UserNodes)
	}

	// A window containing now is effective.
	start := now.Add(-time.Hour)
	snap = BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:     []model.User{alice},
		Bindings:  []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true, StartsAt: &start, ExpiresAt: &future}},
		Plans:     []model.SubscriptionPlan{plan},
		PlanNodes: []model.SubscriptionPlanNode{snapshotTestNode(model.AssignableNodeProxyPath, 10)},
		Now:       now,
	})
	if len(snap.UserNodes) != 1 {
		t.Fatalf("in-window binding must be effective: %#v", snap.UserNodes)
	}
}

func TestBuildEffectiveAccessSnapshotProjections(t *testing.T) {
	now := time.Now()
	alice := model.User{ID: 1, Username: "alice", Status: "active"}
	plan := snapshotTestPlan(1, "premium")
	inbound := model.Inbound{ID: 5, ServerID: 1, Enabled: true}
	path := model.ProxyPath{ID: 7, InboundID: 5, Enabled: true}
	external := model.ExternalOutbound{ID: 9, Enabled: true, ExposeToUsers: true}
	snap := BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:    []model.User{alice},
		Bindings: []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true}},
		Plans:    []model.SubscriptionPlan{plan},
		PlanNodes: []model.SubscriptionPlanNode{
			snapshotTestNode(model.AssignableNodeProxyPath, 7),
			snapshotTestNode(model.AssignableNodeInbound, 5),
			snapshotTestNode(model.AssignableNodeExternalOutbound, 9),
		},
		Inbounds:          []model.Inbound{inbound},
		Paths:             []model.ProxyPath{path},
		ExternalOutbounds: []model.ExternalOutbound{external},
		Now:               now,
	})
	if len(snap.ProxyPathUsers[7]) != 1 || len(snap.InboundUsers[5]) != 1 || len(snap.ExternalOutboundUsers[9]) != 1 {
		t.Fatalf("projections = %#v", snap)
	}
	if len(snap.NodeUsers[NodeKeyOf(model.AssignableNodeProxyPath, 7)]) != 1 {
		t.Fatalf("node users = %#v", snap.NodeUsers)
	}
	inboundBindings := snap.InboundUserBindings()
	if len(inboundBindings) != 1 || inboundBindings[0].InboundID != 5 || inboundBindings[0].UserID != 1 {
		t.Fatalf("inbound bindings = %#v", inboundBindings)
	}
	pathBindings := snap.ProxyPathUserBindings()
	if len(pathBindings) != 1 || pathBindings[0].ProxyPathID != 7 {
		t.Fatalf("path bindings = %#v", pathBindings)
	}
}

func TestEffectiveUserPolicyPrecedence(t *testing.T) {
	now := time.Now()
	alice := model.User{ID: 1, Username: "alice", Status: "active"}
	plan := model.SubscriptionPlan{ID: 1, Name: "premium", Enabled: true, SpeedLimitMbps: 100, TrafficLimitBytes: 1 << 30, TrafficResetMode: "monthly", TrafficResetDay: 1}

	// No binding: user fields (default when zero).
	snap := BuildEffectiveAccessSnapshot(EffectiveAccessInput{Users: []model.User{alice}, Now: now})
	if p := snap.UserPolicies[1]; p.Source != "default" || p.SpeedLimitMbps != 0 {
		t.Fatalf("default policy = %#v", p)
	}

	// Plan limits apply when the user has no override.
	snap = BuildEffectiveAccessSnapshot(EffectiveAccessInput{Users: []model.User{alice}, Bindings: []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true}}, Plans: []model.SubscriptionPlan{plan}, Now: now})
	if p := snap.UserPolicies[1]; p.Source != "plan" || p.SpeedLimitMbps != 100 || p.TrafficLimitBytes != 1<<30 {
		t.Fatalf("plan policy = %#v", p)
	}

	// Explicit user override wins over the plan.
	bob := model.User{ID: 2, Username: "bob", Status: "active", SpeedLimitMbps: 500, TrafficResetMode: "month_day", TrafficResetDay: 15}
	snap = BuildEffectiveAccessSnapshot(EffectiveAccessInput{Users: []model.User{bob}, Bindings: []model.UserPlanBinding{{UserID: 2, PlanID: 1, Enabled: true}}, Plans: []model.SubscriptionPlan{plan}, Now: now})
	if p := snap.UserPolicies[2]; p.Source != "user_override" || p.SpeedLimitMbps != 500 || p.TrafficResetDay != 15 {
		t.Fatalf("override policy = %#v", p)
	}
}

func TestCompareLegacyAndPlanAccess(t *testing.T) {
	now := time.Now()
	alice := model.User{ID: 1, Username: "alice", Status: "active"}
	plan := snapshotTestPlan(1, "premium")
	snapshot := BuildEffectiveAccessSnapshot(EffectiveAccessInput{
		Users:     []model.User{alice},
		Bindings:  []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true}},
		Plans:     []model.SubscriptionPlan{plan},
		PlanNodes: []model.SubscriptionPlanNode{snapshotTestNode(model.AssignableNodeProxyPath, 10)},
		Now:       now,
	})
	legacy := LegacyAccessInput{Paths: []model.ProxyPath{{ID: 10, Enabled: true}}}
	comparison := CompareLegacyAndPlanAccess([]model.User{alice}, legacy, snapshot, 5)
	if comparison.UsersCompared != 1 || comparison.DivergentUsers != 1 {
		t.Fatalf("comparison = %#v", comparison)
	}
	if len(comparison.SampleDivergences) != 1 || len(comparison.SampleDivergences[0].PlanNodes) != 1 || len(comparison.SampleDivergences[0].LegacyNodes) != 0 {
		t.Fatalf("divergence = %#v", comparison.SampleDivergences)
	}
}
