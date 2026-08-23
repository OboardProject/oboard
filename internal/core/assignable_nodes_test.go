package core

import (
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func timePtrAssignTest(t time.Time) *time.Time { return &t }

func TestBuildAssignableNodeCatalog(t *testing.T) {
	now := time.Now()
	input := AssignableNodeCatalogInput{
		Servers: []model.Server{
			{ID: 1, Name: "hk", Status: model.ServerOnline},
			{ID: 2, Name: "sg", Status: model.ServerOnline},
		},
		Inbounds: []model.Inbound{
			{ID: 11, ServerID: 1, Name: "hk-vless", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true},
			{ID: 12, ServerID: 1, Name: "hk-ss", Protocol: model.ProtocolSS, Port: 8388, Enabled: true},
			{ID: 21, ServerID: 2, Name: "sg-hy2", Protocol: model.ProtocolHY2, Port: 8443, Enabled: true},
		},
		ProxyPaths: []model.ProxyPath{
			{ID: 100, Kind: model.ProxyPathKindChain, Name: "hk-sg", InboundID: 11, Enabled: true},
			{ID: 101, Kind: model.ProxyPathKindDirect, Name: "disabled-path", InboundID: 12, Enabled: false},
		},
		ProxyPathSteps: []model.ProxyPathStep{
			{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &[]int64{2}[0], InboundID: &[]int64{21}[0]},
		},
		ExternalOutbounds: []model.ExternalOutbound{
			{ID: 200, Name: "imported-us", Protocol: model.ProtocolVLESS, ExposeToUsers: true, Enabled: true, EffectiveRegionCode: "US"},
			{ID: 201, Name: "hidden", Protocol: model.ProtocolVLESS, ExposeToUsers: false, Enabled: true},
		},
		ServerOnline: map[int64]bool{1: true, 2: true},
	}
	nodes, err := BuildAssignableNodeCatalog(input)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]AssignableNode{}
	for _, node := range nodes {
		byKey[node.Key] = node
	}
	if len(nodes) != 5 {
		t.Fatalf("catalog size = %d, want 5 (2 paths + 2 standalone inbounds + 1 external): %#v", len(nodes), nodes)
	}
	path := byKey["proxy_path:100"]
	if path.Name == "" || path.ExitRegion != "" {
		t.Fatalf("path node = %#v", path)
	}
	if path.EntryServerID != 1 || path.EntryProtocol != model.ProtocolVLESS || path.Status != AssignableNodeStatusOK {
		t.Fatalf("path node = %#v", path)
	}
	if len(path.PathSummary) != 2 || path.PathSummary[1] != "sg" {
		t.Fatalf("path summary = %#v", path.PathSummary)
	}
	disabled := byKey["proxy_path:101"]
	if disabled.Status != AssignableNodeStatusDisabled || disabled.Enabled {
		t.Fatalf("disabled path node = %#v", disabled)
	}
	standalone := byKey["inbound:21"]
	if standalone.EntryServerID != 2 || standalone.Status != AssignableNodeStatusOK {
		t.Fatalf("standalone node = %#v", standalone)
	}
	external := byKey["external_outbound:200"]
	if external.ExitRegion != "US" {
		t.Fatalf("external node = %#v", external)
	}
	if _, ok := byKey["external_outbound:201"]; ok {
		t.Fatalf("hidden external must not be assignable")
	}
	if _, ok := byKey["inbound:11"]; ok {
		t.Fatalf("inbound with branches must not be standalone assignable")
	}
	// A disabled path's inbound falls back to a standalone node, mirroring the
	// subscription generator. The migration phase materializes these as
	// zero-step direct proxy_paths and drops the inbound identity.
	if _, ok := byKey["inbound:12"]; !ok {
		t.Fatalf("inbound with only disabled branches must remain standalone assignable")
	}

	// Offline root server flips the path status but keeps it assignable.
	input.ServerOnline = map[int64]bool{1: false, 2: true}
	nodes, err = BuildAssignableNodeCatalog(input)
	if err != nil {
		t.Fatal(err)
	}
	byKey = map[string]AssignableNode{}
	for _, node := range nodes {
		byKey[node.Key] = node
	}
	if got := byKey["proxy_path:100"].Status; got != AssignableNodeStatusOffline {
		t.Fatalf("offline path status = %s", got)
	}
	_ = now
}

func TestUserEffectiveNodeSetPriority(t *testing.T) {
	now := time.Now()
	plan := &model.SubscriptionPlan{ID: 1, Name: "premium", Enabled: true}
	planNodes := []model.SubscriptionPlanNode{
		{PlanID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 1, Enabled: true},
		{PlanID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 2, Enabled: true},
	}
	exceptions := []model.UserNodeException{
		{UserID: 9, NodeType: model.AssignableNodeProxyPath, NodeID: 1, Effect: model.UserNodeExceptionDeny, Reason: "abuse", ExpiresAt: timePtrAssignTest(now.Add(time.Hour))},
		{UserID: 9, NodeType: model.AssignableNodeProxyPath, NodeID: 3, Effect: model.UserNodeExceptionAllow, Reason: "trial", ExpiresAt: timePtrAssignTest(now.Add(time.Hour))},
		{UserID: 9, NodeType: model.AssignableNodeProxyPath, NodeID: 4, Effect: model.UserNodeExceptionAllow, Reason: "expired trial", ExpiresAt: timePtrAssignTest(now.Add(-time.Hour))},
		{UserID: 9, NodeType: model.AssignableNodeProxyPath, NodeID: 2, Effect: model.UserNodeExceptionDeny, Reason: "deny then allow", ExpiresAt: timePtrAssignTest(now.Add(time.Hour))},
	}
	// deny wins over allow for node 2.
	exceptions = append(exceptions, model.UserNodeException{UserID: 9, NodeType: model.AssignableNodeProxyPath, NodeID: 2, Effect: model.UserNodeExceptionAllow, Reason: "conflict", ExpiresAt: timePtrAssignTest(now.Add(time.Hour))})

	got := UserEffectiveNodeSet(plan, planNodes, exceptions, now)
	if got["proxy_path:1"] {
		t.Fatalf("deny exception must win over plan: %#v", got)
	}
	if !got["proxy_path:3"] {
		t.Fatalf("allow exception must add a node: %#v", got)
	}
	if got["proxy_path:4"] {
		t.Fatalf("expired allow exception must be ignored: %#v", got)
	}
	if got["proxy_path:2"] {
		t.Fatalf("deny must win over allow: %#v", got)
	}

	// Disabled plan: only allow exceptions remain.
	disabled := *plan
	disabled.Enabled = false
	got = UserEffectiveNodeSet(&disabled, planNodes, exceptions, now)
	if len(got) != 1 || !got["proxy_path:3"] {
		t.Fatalf("disabled plan set = %#v", got)
	}

	// No binding at all: deny-by-default except allow exceptions.
	got = UserEffectiveNodeSet(nil, nil, exceptions, now)
	if len(got) != 1 || !got["proxy_path:3"] {
		t.Fatalf("no-plan set = %#v", got)
	}
}

func TestPreviewPlanAssignment(t *testing.T) {
	now := time.Now()
	users := []model.User{
		{ID: 1, Username: "a", Status: "active"},
		{ID: 2, Username: "b", Status: "active"},
		{ID: 3, Username: "c", Status: "active"},
	}
	plans := []model.SubscriptionPlan{
		{ID: 1, Name: "standard", Enabled: true},
		{ID: 2, Name: "premium", Enabled: true},
	}
	planNodes := []model.SubscriptionPlanNode{
		{PlanID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 100, Enabled: true},
		{PlanID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 101, Enabled: true},
		{PlanID: 2, NodeType: model.AssignableNodeProxyPath, NodeID: 101, Enabled: true},
		{PlanID: 2, NodeType: model.AssignableNodeProxyPath, NodeID: 102, Enabled: true},
	}
	bindings := []model.UserPlanBinding{
		{UserID: 1, PlanID: 1, Enabled: true},
		{UserID: 2, PlanID: 1, Enabled: true},
	}
	inbounds := []model.Inbound{
		{ID: 50, ServerID: 10, Name: "root", Protocol: model.ProtocolSS, Port: 8388, Enabled: true, ConfigJSON: `{"method":"2022-blake3-aes-128-gcm"}`},
	}
	paths := []model.ProxyPath{
		{ID: 100, Kind: model.ProxyPathKindDirect, Name: "p100", InboundID: 50, Enabled: true},
		{ID: 101, Kind: model.ProxyPathKindDirect, Name: "p101", InboundID: 50, Enabled: true},
		{ID: 102, Kind: model.ProxyPathKindDirect, Name: "p102", InboundID: 50, Enabled: true},
	}
	serverOnline := map[int64]bool{10: true}
	target := &plans[1]

	preview := PreviewPlanAssignment(users, bindings, plans, planNodes, nil, target, planNodesForPlan(t, planNodes, 2), paths, nil, inbounds, serverOnline, now)
	if preview.UsersAffected != 3 || preview.UsersUnchanged != 0 {
		t.Fatalf("users affected/unchanged = %d/%d", preview.UsersAffected, preview.UsersUnchanged)
	}
	if len(preview.NodesAdded) != 2 || preview.NodesAdded[0] != "proxy_path:101" || preview.NodesAdded[1] != "proxy_path:102" {
		t.Fatalf("added = %#v", preview.NodesAdded)
	}
	if len(preview.NodesRemoved) != 1 || preview.NodesRemoved[0] != "proxy_path:100" {
		t.Fatalf("removed = %#v", preview.NodesRemoved)
	}
	if preview.NodesUnchanged != 1 {
		t.Fatalf("unchanged = %d", preview.NodesUnchanged)
	}
	if len(preview.AffectedServers) != 1 || preview.AffectedServers[0] != 10 || preview.TaskCount != 1 {
		t.Fatalf("servers = %#v tasks = %d", preview.AffectedServers, preview.TaskCount)
	}
	if len(preview.CapacityIssues) != 0 {
		t.Fatalf("capacity issues = %#v", preview.CapacityIssues)
	}

	// Removing the plan for users 1+2 removes nodes 100 and 101 for them.
	preview = PreviewPlanAssignment(users, bindings, plans, planNodes, nil, nil, nil, paths, nil, inbounds, serverOnline, now)
	if preview.UsersAffected != 2 || len(preview.NodesRemoved) != 2 {
		t.Fatalf("removal preview = %#v", preview)
	}
}

func planNodesForPlan(t *testing.T, all []model.SubscriptionPlanNode, planID int64) []model.SubscriptionPlanNode {
	t.Helper()
	out := []model.SubscriptionPlanNode{}
	for _, pn := range all {
		if pn.PlanID == planID {
			out = append(out, pn)
		}
	}
	return out
}

func TestPreviewPlanNodeChangeAndTransparentAuthServer(t *testing.T) {
	now := time.Now()
	users := []model.User{
		{ID: 1, Username: "a", Status: "active"},
		{ID: 2, Username: "b", Status: "active"},
	}
	plans := []model.SubscriptionPlan{{ID: 1, Name: "standard", Enabled: true}}
	planNodes := []model.SubscriptionPlanNode{
		{PlanID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 100, Enabled: true},
		{PlanID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 101, Enabled: true},
	}
	bindings := []model.UserPlanBinding{
		{UserID: 1, PlanID: 1, Enabled: true},
		{UserID: 2, PlanID: 1, Enabled: true},
	}
	inbounds := []model.Inbound{
		{ID: 50, ServerID: 10, Name: "front-in", Protocol: model.ProtocolVLESS, Port: 443, Enabled: true},
		{ID: 60, ServerID: 20, Name: "proc-in", Protocol: model.ProtocolVLESS, Port: 8443, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, InboundID: &[]int64{60}[0]},
		{ID: 2, PathID: 101, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &[]int64{20}[0]},
	}
	paths := []model.ProxyPath{
		{ID: 100, Kind: model.ProxyPathKindChain, Name: "transparent", InboundID: 50, Enabled: true},
		{ID: 101, Kind: model.ProxyPathKindChain, Name: "plain", InboundID: 50, Enabled: true},
	}
	serverOnline := map[int64]bool{10: true, 20: true}

	// Replace plan 1 with only path 101: path 100 (transparent, auth server 20)
	// disappears and path 101 stays.
	newNodes := []model.SubscriptionPlanNode{
		{PlanID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 101, Enabled: true},
	}
	preview := PreviewPlanNodeChange(users, bindings, plans, planNodes, nil, 1, newNodes, paths, steps, inbounds, serverOnline, now)
	if preview.UsersAffected != 2 {
		t.Fatalf("users affected = %d", preview.UsersAffected)
	}
	if len(preview.NodesRemoved) != 1 || preview.NodesRemoved[0] != "proxy_path:100" {
		t.Fatalf("removed = %#v", preview.NodesRemoved)
	}
	if len(preview.AffectedServers) != 1 || preview.AffectedServers[0] != 20 {
		t.Fatalf("affected servers = %#v, want the processing server 20", preview.AffectedServers)
	}
	if preview.TaskCount != 1 {
		t.Fatalf("task count = %d", preview.TaskCount)
	}
}

func TestUserEffectiveNodeSources(t *testing.T) {
	now := time.Now()
	binding := &model.UserPlanBinding{UserID: 1, PlanID: 1, Enabled: true}
	plan := &model.SubscriptionPlan{ID: 1, Name: "premium", Enabled: true}
	planNodes := []model.SubscriptionPlanNode{
		{PlanID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 1, Enabled: true},
	}
	exceptions := []model.UserNodeException{
		{UserID: 1, NodeType: model.AssignableNodeProxyPath, NodeID: 2, Effect: model.UserNodeExceptionAllow, Reason: "trial", ExpiresAt: timePtrAssignTest(now.Add(time.Hour))},
	}
	sources := UserEffectiveNodeSources(binding, plan, planNodes, exceptions, now)
	if len(sources) != 2 {
		t.Fatalf("sources = %#v", sources)
	}
	if sources[0].Source != "plan" || sources[1].Source != "exception" {
		t.Fatalf("sources = %#v", sources)
	}
}
