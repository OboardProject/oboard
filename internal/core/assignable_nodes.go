package core

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// AssignableNodeStatus mirrors the client-visible node health in the catalog.
type AssignableNodeStatus string

const (
	AssignableNodeStatusOK       AssignableNodeStatus = "ok"
	AssignableNodeStatusOffline  AssignableNodeStatus = "offline"
	AssignableNodeStatusDisabled AssignableNodeStatus = "disabled"
)

// AssignableNode is one client-visible unit that can be bound to a subscription
// plan. Topology resources (servers, inbounds, path steps, shared chains,
// tunnels, port forwards) are not assignable.
type AssignableNode struct {
	Type                   model.AssignableNodeType   `json:"type"`
	ID                     int64                      `json:"id"`
	Key                    string                     `json:"key"`
	Name                   string                     `json:"name"`
	SourceName             string                     `json:"source_name"`
	GlobalNameOverride     *string                    `json:"global_name_override"`
	HasGlobalNameOverride  bool                       `json:"has_global_name_override"`
	EffectiveGlobalName    string                     `json:"effective_global_name"`
	MetadataLockVersion    int64                      `json:"metadata_lock_version"`
	EntryKey               string                     `json:"entry_key,omitempty"`
	EntryServerID          int64                      `json:"entry_server_id,omitempty"`
	EntryServerName        string                     `json:"entry_server_name,omitempty"`
	EntryRegion            string                     `json:"entry_region,omitempty"`
	EntryProtocol          model.Protocol             `json:"entry_protocol,omitempty"`
	EntryPort              int                        `json:"entry_port,omitempty"`
	ExitServerID           int64                      `json:"exit_server_id,omitempty"`
	ExitServerName         string                     `json:"exit_server_name,omitempty"`
	ExitExternalOutboundID int64                      `json:"exit_external_outbound_id,omitempty"`
	PathServers            []AssignableNodeServerRole `json:"path_servers,omitempty"`
	PathSummary            []string                   `json:"path_summary,omitempty"`
	ExitRegion             string                     `json:"exit_region,omitempty"`
	Enabled                bool                       `json:"enabled"`
	Status                 AssignableNodeStatus       `json:"status"`
	Renderable             bool                       `json:"renderable"`
}

// AssignableNodeCatalogInput carries the topology snapshot the catalog is
// derived from. It never changes the runtime authorization model.
type AssignableNodeCatalogInput struct {
	Servers           []model.Server
	Inbounds          []model.Inbound
	ProxyPaths        []model.ProxyPath
	ProxyPathSteps    []model.ProxyPathStep
	EgressResults     []model.ProxyPathEgressResult
	ExternalOutbounds []model.ExternalOutbound
	// ServerOnline marks servers that are currently online. A nil map treats
	// every server as online.
	ServerOnline map[int64]bool
	NodeMetadata map[string]model.AssignableNodeMetadata
}

// BuildAssignableNodeCatalog enumerates every client-visible node:
// proxy_path:<id> for every path (including zero-step direct paths),
// external_outbound:<id> for public imported nodes, and inbound:<id> only for
// standalone inbounds that have no path branches (transitional until migration
// materializes them as direct proxy_paths).
func BuildAssignableNodeCatalog(input AssignableNodeCatalogInput) ([]AssignableNode, error) {
	topologies, paths, externals, err := ResolveAssignableNodeTopologies(input)
	if err != nil {
		return nil, err
	}
	serverByID := map[int64]model.Server{}
	for _, server := range input.Servers {
		serverByID[server.ID] = server
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range input.Inbounds {
		inboundByID[inbound.ID] = inbound
	}
	externalByID := map[int64]model.ExternalOutbound{}
	for _, external := range externals {
		externalByID[external.ID] = external
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range input.ProxyPathSteps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}

	type draftNode struct {
		node       AssignableNode
		name       string
		regionCode string
		serverID   int64
		protocol   model.Protocol
	}
	drafts := []draftNode{}
	nameNodes := []SubscriptionNode{}
	nameRefs := []subscriptionNodeNameRef{}
	standaloneProtocols := map[int64]map[model.Protocol]bool{}

	for _, inbound := range input.Inbounds {
		if !inbound.Enabled {
			continue
		}
		configuredBranches := subscriptionBranchesForInbound(inbound, paths, input.ProxyPathSteps)
		if len(configuredBranches) > 0 {
			continue
		}
		server, ok := serverByID[inbound.ServerID]
		if !ok {
			continue
		}
		regionCode, _ := EffectiveServerRegion(server)
		label := proxyPathServerLabel(server, server.ID)
		idx := len(nameNodes)
		nameNodes = append(nameNodes, SubscriptionNode{Name: label, Server: server, Inbound: inbound, Raw: map[string]any{}})
		nameRefs = append(nameRefs, subscriptionNodeNameRef{index: idx, kind: subscriptionNodeNameStandalone, resourceID: inbound.ID, serverID: server.ID, regionCode: regionCode})
		if standaloneProtocols[server.ID] == nil {
			standaloneProtocols[server.ID] = map[model.Protocol]bool{}
		}
		standaloneProtocols[server.ID][inbound.Protocol] = true
		drafts = append(drafts, draftNode{node: AssignableNode{
			Type:            model.AssignableNodeInbound,
			ID:              inbound.ID,
			EntryKey:        "inbound:" + strconv.FormatInt(inbound.ID, 10),
			EntryServerID:   server.ID,
			EntryServerName: label,
			EntryRegion:     regionCode,
			EntryProtocol:   inbound.Protocol,
			EntryPort:       inbound.Port,
			ExitRegion:      regionCode,
			Enabled:         true,
			Status:          assignableNodeStatusFor(server, true),
			Renderable:      true,
		}, name: label, regionCode: regionCode, serverID: server.ID, protocol: inbound.Protocol})
	}

	pathByID := map[int64]model.ProxyPath{}
	for _, path := range paths {
		pathByID[path.ID] = path
	}
	applyTopology := func(node *AssignableNode) {
		if topo, ok := topologies[node.Key]; ok {
			node.EntryKey = topo.EntryKey
			node.EntryRegion = topo.EntryRegion
			node.ExitServerID = topo.ExitServerID
			node.ExitServerName = topo.ExitServerName
			node.ExitExternalOutboundID = topo.ExitExternalOutboundID
			node.PathServers = topo.ServerRoles
			node.Renderable = node.Enabled
		}
	}
	for _, path := range paths {
		root, ok := inboundByID[path.InboundID]
		if !ok {
			continue
		}
		rootServer, ok := serverByID[root.ServerID]
		if !ok {
			rootServer = model.Server{}
		}
		enabled := path.Enabled && root.Enabled
		status := AssignableNodeStatusOK
		if !enabled {
			status = AssignableNodeStatusDisabled
		} else if !serverOnline(rootServer, input.ServerOnline) {
			status = AssignableNodeStatusOffline
		}
		pathName := strings.TrimSpace(path.Name)
		if pathName == "" {
			pathName = fmt.Sprintf("%s 分支 %d", proxyPathServerLabel(rootServer, rootServer.ID), path.ID)
		}
		drafts = append(drafts, draftNode{node: AssignableNode{
			Type:            model.AssignableNodeProxyPath,
			ID:              path.ID,
			EntryServerID:   root.ServerID,
			EntryServerName: proxyPathServerLabel(rootServer, rootServer.ID),
			EntryProtocol:   root.Protocol,
			EntryPort:       root.Port,
			PathSummary:     proxyPathSummaryLabels(path, orderedProxyPathSteps(stepsByPath[path.ID]), serverByID, inboundByID, externalByID),
			ExitRegion:      path.EffectiveExitRegionCode,
			Enabled:         enabled,
			Status:          status,
			Renderable:      enabled,
		}, name: pathName, regionCode: path.EffectiveExitRegionCode, serverID: root.ServerID, protocol: root.Protocol})
	}

	for _, external := range externals {
		if !external.Enabled || !external.ExposeToUsers {
			continue
		}
		name := strings.TrimSpace(external.Name)
		if name == "" {
			name = fmt.Sprintf("%s-%d", external.Protocol, external.ID)
		}
		idx := len(nameNodes)
		nameNodes = append(nameNodes, SubscriptionNode{Name: name, Raw: map[string]any{}})
		nameRefs = append(nameRefs, subscriptionNodeNameRef{index: idx, kind: subscriptionNodeNameExternal, resourceID: external.ID, regionCode: external.EffectiveRegionCode})
		drafts = append(drafts, draftNode{node: AssignableNode{
			Type:       model.AssignableNodeExternalOutbound,
			ID:         external.ID,
			ExitRegion: external.EffectiveRegionCode,
			Enabled:    true,
			Status:     AssignableNodeStatusOK,
			Renderable: true,
		}, name: name, regionCode: external.EffectiveRegionCode})
	}

	resolveSubscriptionNodeNames(nameNodes, nameRefs)
	for i := range drafts {
		drafts[i].node.Key = NodeKeyOf(drafts[i].node.Type, drafts[i].node.ID)
		applyTopology(&drafts[i].node)
	}

	// Resolve final standalone names after protocol-suffix disambiguation:
	// proxyPathServerLabel-style names with a protocol suffix must match what
	// the subscription generator emits, so re-read names from nameNodes.
	for i := range drafts {
		for _, ref := range nameRefs {
			if ref.resourceID == drafts[i].node.ID && ref.kind == subscriptionNodeNameStandalone && drafts[i].node.Type == model.AssignableNodeInbound {
				drafts[i].name = nameNodes[ref.index].Name
				break
			}
		}
		drafts[i].node.SourceName = drafts[i].name
		metadata, ok := input.NodeMetadata[drafts[i].node.Key]
		if ok {
			drafts[i].node.MetadataLockVersion = metadata.LockVersion
		}
		if ok && metadata.DisplayNameOverride != nil {
			value := *metadata.DisplayNameOverride
			drafts[i].node.GlobalNameOverride = &value
			drafts[i].node.HasGlobalNameOverride = true
		}
		drafts[i].node.EffectiveGlobalName = ResolveEffectiveNodeName(drafts[i].name, drafts[i].node.GlobalNameOverride, nil)
		drafts[i].node.Name = RegionFlagEmoji(drafts[i].regionCode) + " " + drafts[i].node.EffectiveGlobalName
	}

	sort.SliceStable(drafts, func(i, j int) bool {
		if drafts[i].node.Type == drafts[j].node.Type {
			return drafts[i].node.ID < drafts[j].node.ID
		}
		return drafts[i].node.Type < drafts[j].node.Type
	})
	out := make([]AssignableNode, 0, len(drafts))
	for _, draft := range drafts {
		out = append(out, draft.node)
	}
	return out, nil
}

func serverOnline(server model.Server, online map[int64]bool) bool {
	if online == nil {
		return true
	}
	ok, found := online[server.ID]
	return found && ok
}

func assignableNodeStatusFor(server model.Server, enabled bool) AssignableNodeStatus {
	if !enabled {
		return AssignableNodeStatusDisabled
	}
	if server.Status != model.ServerOnline {
		return AssignableNodeStatusOffline
	}
	return AssignableNodeStatusOK
}

func proxyPathSummaryLabels(path model.ProxyPath, steps []model.ProxyPathStep, servers map[int64]model.Server, inbounds map[int64]model.Inbound, externals map[int64]model.ExternalOutbound) []string {
	labels := []string{}
	if root, ok := inbounds[path.InboundID]; ok {
		if server, ok := servers[root.ServerID]; ok {
			labels = append(labels, proxyPathServerLabel(server, server.ID))
		} else {
			labels = append(labels, fmt.Sprintf("server-%d", root.ServerID))
		}
	}
	for _, step := range steps {
		switch step.NodeType {
		case model.ProxyPathStepServerInbound:
			serverID, _, found := proxyPathStepTargetServer(step, inbounds)
			if !found {
				continue
			}
			if server, ok := servers[serverID]; ok {
				labels = append(labels, proxyPathServerLabel(server, serverID))
			} else {
				labels = append(labels, fmt.Sprintf("server-%d", serverID))
			}
		case model.ProxyPathStepImported:
			if step.ExternalOutboundID != nil {
				if external, ok := externals[*step.ExternalOutboundID]; ok {
					name := strings.TrimSpace(external.Name)
					if name == "" {
						name = fmt.Sprintf("%s-%d", external.Protocol, external.ID)
					}
					labels = append(labels, name)
					continue
				}
			}
			labels = append(labels, "导入节点")
		case model.ProxyPathStepWARP:
			labels = append(labels, "WARP")
		}
	}
	return labels
}

// NodeKeyOf builds the stable identity "type:id" used by plan nodes, user
// exceptions, and effective-node sets.
func NodeKeyOf(nodeType model.AssignableNodeType, nodeID int64) string {
	return string(nodeType) + ":" + strconv.FormatInt(nodeID, 10)
}

// ParseNodeKey splits a "type:id" key back into its parts.
func ParseNodeKey(key string) (model.AssignableNodeType, int64, bool) {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) != 2 {
		return "", 0, false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id <= 0 {
		return "", 0, false
	}
	return model.AssignableNodeType(parts[0]), id, true
}

// UserEffectiveNodeSet computes the effective node keys for one user with a
// fixed priority: deny exception > allow exception > active plan > default deny.
// Expired exceptions are ignored. A disabled plan or a missing binding yields
// only exception-derived nodes.
func UserEffectiveNodeSet(plan *model.SubscriptionPlan, planNodes []model.SubscriptionPlanNode, exceptions []model.UserNodeException, now time.Time) map[string]bool {
	planKeys := map[string]bool{}
	if plan != nil && plan.Enabled {
		for _, pn := range planNodes {
			if !pn.Enabled {
				continue
			}
			planKeys[NodeKeyOf(pn.NodeType, pn.NodeID)] = true
		}
	}
	denied := map[string]bool{}
	for _, ex := range exceptions {
		if ex.ExpiresAt != nil && !ex.ExpiresAt.After(now) {
			continue
		}
		if ex.Effect == model.UserNodeExceptionDeny {
			denied[NodeKeyOf(ex.NodeType, ex.NodeID)] = true
		}
	}
	out := map[string]bool{}
	for key := range planKeys {
		if !denied[key] {
			out[key] = true
		}
	}
	for _, ex := range exceptions {
		if ex.ExpiresAt != nil && !ex.ExpiresAt.After(now) || ex.Effect != model.UserNodeExceptionAllow {
			continue
		}
		key := NodeKeyOf(ex.NodeType, ex.NodeID)
		if !denied[key] {
			out[key] = true
		}
	}
	return out
}

// UserEffectiveNodeSource explains where one effective node came from, so node
// detail views can trace plan membership and exceptions.
type UserEffectiveNodeSource struct {
	Key       string                        `json:"key"`
	NodeType  model.AssignableNodeType      `json:"node_type"`
	NodeID    int64                         `json:"node_id"`
	Source    string                        `json:"source"` // plan | exception
	PlanID    int64                         `json:"plan_id,omitempty"`
	PlanName  string                        `json:"plan_name,omitempty"`
	Effect    model.UserNodeExceptionEffect `json:"effect,omitempty"`
	Reason    string                        `json:"reason,omitempty"`
	ExpiresAt *time.Time                    `json:"expires_at,omitempty"`
}

// UserEffectiveNodeSources lists every effective node for a user with its
// provenance, in stable key order.
func UserEffectiveNodeSources(binding *model.UserPlanBinding, plan *model.SubscriptionPlan, planNodes []model.SubscriptionPlanNode, exceptions []model.UserNodeException, now time.Time) []UserEffectiveNodeSource {
	effective := UserEffectiveNodeSet(plan, planNodes, exceptions, now)
	denied := map[string]bool{}
	allow := map[string]model.UserNodeException{}
	for _, ex := range exceptions {
		if ex.ExpiresAt != nil && !ex.ExpiresAt.After(now) {
			continue
		}
		key := NodeKeyOf(ex.NodeType, ex.NodeID)
		if ex.Effect == model.UserNodeExceptionDeny {
			denied[key] = true
		} else {
			allow[key] = ex
		}
	}
	out := []UserEffectiveNodeSource{}
	for _, pn := range planNodes {
		if !pn.Enabled {
			continue
		}
		key := NodeKeyOf(pn.NodeType, pn.NodeID)
		if !effective[key] || denied[key] {
			continue
		}
		out = append(out, UserEffectiveNodeSource{Key: key, NodeType: pn.NodeType, NodeID: pn.NodeID, Source: "plan"})
	}
	for _, ex := range allow {
		key := NodeKeyOf(ex.NodeType, ex.NodeID)
		if !effective[key] {
			continue
		}
		out = append(out, UserEffectiveNodeSource{Key: key, NodeType: ex.NodeType, NodeID: ex.NodeID, Source: "exception", Effect: ex.Effect, Reason: ex.Reason, ExpiresAt: ex.ExpiresAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// PlanChangePreview summarizes a batch plan assignment without writing
// anything. It is the single source for the preview page: node diff, affected
// authentication servers, offline servers, and the projected task count.
type PlanChangePreview struct {
	UsersAffected   int      `json:"users_affected"`
	UsersUnchanged  int      `json:"users_unchanged"`
	NodesAdded      []string `json:"nodes_added"`
	NodesRemoved    []string `json:"nodes_removed"`
	NodesUnchanged  int      `json:"nodes_unchanged"`
	AffectedServers []int64  `json:"affected_servers"`
	AffectedPaths   int      `json:"affected_paths"`
	TaskCount       int      `json:"task_count"`
	OfflineServers  []int64  `json:"offline_servers"`
	CapacityIssues  []string `json:"capacity_issues"`
}

// PreviewPlanAssignment computes the user-node diff of switching every listed
// user to targetPlan (nil removes the plan) and derives the affected
// authentication servers for the changed proxy_path nodes. Only the server that
// authenticates the terminal user needs an access sync; downstream chain
// servers are intentionally excluded.
func PreviewPlanAssignment(users []model.User, bindings []model.UserPlanBinding, plans []model.SubscriptionPlan, planNodes []model.SubscriptionPlanNode, exceptions []model.UserNodeException, targetPlan *model.SubscriptionPlan, targetPlanNodes []model.SubscriptionPlanNode, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, serverOnline map[int64]bool, now time.Time) PlanChangePreview {
	state := newPlanAccessState(users, bindings, plans, planNodes, exceptions, now)
	targetState := state.withAssignment(targetPlan, targetPlanNodes)

	added := map[string]bool{}
	removed := map[string]bool{}
	unchanged := map[string]bool{}
	usersAffected := 0
	usersUnchanged := 0
	for _, user := range users {
		currentNodes := state.effective[user.ID]
		targetNodes := targetState.effective[user.ID]
		changed := false
		for key := range currentNodes {
			if targetNodes[key] {
				unchanged[key] = true
				continue
			}
			removed[key] = true
			changed = true
		}
		for key := range targetNodes {
			if currentNodes[key] {
				continue
			}
			added[key] = true
			changed = true
		}
		if changed {
			usersAffected++
		} else {
			usersUnchanged++
		}
	}

	preview := PlanChangePreview{
		UsersAffected:  usersAffected,
		UsersUnchanged: usersUnchanged,
		NodesAdded:     sortedKeys(added),
		NodesRemoved:   sortedKeys(removed),
		NodesUnchanged: len(unchanged),
	}
	affectedKeys := map[string]bool{}
	for key := range added {
		affectedKeys[key] = true
	}
	for key := range removed {
		affectedKeys[key] = true
	}
	preview.AffectedServers, preview.AffectedPaths, preview.OfflineServers = affectedAuthServers(affectedKeys, paths, steps, inbounds, serverOnline)
	preview.TaskCount = len(preview.AffectedServers)
	preview.CapacityIssues = state.validatePlanCapacity(targetState, paths, inbounds, now)
	return preview
}

func bindingPlanID(binding *model.UserPlanBinding) int64 {
	if binding == nil {
		return 0
	}
	return binding.PlanID
}

// affectedAuthServers resolves the authentication server for every changed
// proxy_path (and standalone inbound) node: the processing-role server for
// transparent prefixes, the SSH user entry server, otherwise the root inbound
// server. External nodes never need a server task.
func affectedAuthServers(keys map[string]bool, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, serverOnline map[int64]bool) ([]int64, int, []int64) {
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range steps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	serverSet := map[int64]bool{}
	offlineSet := map[int64]bool{}
	affectedPaths := 0
	for key := range keys {
		nodeType, nodeID, ok := ParseNodeKey(key)
		if !ok {
			continue
		}
		switch nodeType {
		case model.AssignableNodeProxyPath:
			var path *model.ProxyPath
			for i := range paths {
				if paths[i].ID == nodeID {
					path = &paths[i]
					break
				}
			}
			if path == nil {
				continue
			}
			serverID, ok := ProxyPathAccountingServerID(*path, stepsByPath[path.ID], inbounds)
			if !ok || serverID == 0 {
				continue
			}
			affectedPaths++
			serverSet[serverID] = true
			if !serverOnline[serverID] {
				offlineSet[serverID] = true
			}
		case model.AssignableNodeInbound:
			inbound, ok := inboundByID[nodeID]
			if !ok {
				continue
			}
			serverSet[inbound.ServerID] = true
			if !serverOnline[inbound.ServerID] {
				offlineSet[inbound.ServerID] = true
			}
		}
	}
	servers := sortedInt64Keys(serverSet)
	offline := sortedInt64Keys(offlineSet)
	return servers, affectedPaths, offline
}

// AffectedAuthServers exposes the authentication-server resolution used by the
// access-change engine: the processing-role server for transparent prefixes,
// the SSH user entry server, otherwise the root inbound server.
func AffectedAuthServers(keys map[string]bool, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, serverOnline map[int64]bool) ([]int64, int, []int64) {
	return affectedAuthServers(keys, paths, steps, inbounds, serverOnline)
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sortedInt64Keys(set map[int64]bool) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PreviewPlanNodeChange summarizes editing a plan's node set (add/remove/
// replace) without writing anything: node diff for users bound to the plan,
// affected authentication servers, and capacity checks across all users.
func PreviewPlanNodeChange(users []model.User, bindings []model.UserPlanBinding, plans []model.SubscriptionPlan, planNodes []model.SubscriptionPlanNode, exceptions []model.UserNodeException, planID int64, newNodes []model.SubscriptionPlanNode, paths []model.ProxyPath, steps []model.ProxyPathStep, inbounds []model.Inbound, serverOnline map[int64]bool, now time.Time) PlanChangePreview {
	state := newPlanAccessState(users, bindings, plans, planNodes, exceptions, now)
	newState := state.withPlanNodes(planID, newNodes)

	added := map[string]bool{}
	removed := map[string]bool{}
	unchanged := map[string]bool{}
	usersAffected := 0
	for _, user := range users {
		binding := state.bindingByUser[user.ID]
		if binding == nil || binding.PlanID != planID || !binding.Enabled {
			continue
		}
		currentNodes := state.effective[user.ID]
		targetNodes := newState.effective[user.ID]
		changed := false
		for key := range currentNodes {
			if targetNodes[key] {
				unchanged[key] = true
				continue
			}
			removed[key] = true
			changed = true
		}
		for key := range targetNodes {
			if currentNodes[key] {
				continue
			}
			added[key] = true
			changed = true
		}
		if changed {
			usersAffected++
		}
	}
	affectedKeys := map[string]bool{}
	for key := range added {
		affectedKeys[key] = true
	}
	for key := range removed {
		affectedKeys[key] = true
	}
	servers, affectedPaths, offline := affectedAuthServers(affectedKeys, paths, steps, inbounds, serverOnline)
	preview := PlanChangePreview{
		UsersAffected:   usersAffected,
		NodesAdded:      sortedKeys(added),
		NodesRemoved:    sortedKeys(removed),
		NodesUnchanged:  len(unchanged),
		AffectedServers: servers,
		AffectedPaths:   affectedPaths,
		TaskCount:       len(servers),
		OfflineServers:  offline,
	}
	preview.CapacityIssues = state.validatePlanCapacity(newState, paths, inbounds, now)
	return preview
}

// planAccessState is the read-only snapshot used for effective-node
// computation and previews. withPlanNodes returns a derived state where one
// plan's node set is replaced, leaving every other binding untouched.
type planAccessState struct {
	plansByID        map[int64]*model.SubscriptionPlan
	planNodesByPlan  map[int64][]model.SubscriptionPlanNode
	bindingByUser    map[int64]*model.UserPlanBinding
	exceptionsByUser map[int64][]model.UserNodeException
	effective        map[int64]map[string]bool
	activeUsers      map[int64]bool
	now              time.Time
}

func newPlanAccessState(users []model.User, bindings []model.UserPlanBinding, plans []model.SubscriptionPlan, planNodes []model.SubscriptionPlanNode, exceptions []model.UserNodeException, now time.Time) *planAccessState {
	state := &planAccessState{
		plansByID:        map[int64]*model.SubscriptionPlan{},
		planNodesByPlan:  map[int64][]model.SubscriptionPlanNode{},
		bindingByUser:    map[int64]*model.UserPlanBinding{},
		exceptionsByUser: map[int64][]model.UserNodeException{},
		effective:        map[int64]map[string]bool{},
		activeUsers:      map[int64]bool{},
		now:              now,
	}
	for i := range plans {
		p := plans[i]
		state.plansByID[p.ID] = &p
	}
	for _, pn := range planNodes {
		if pn.Enabled {
			state.planNodesByPlan[pn.PlanID] = append(state.planNodesByPlan[pn.PlanID], pn)
		}
	}
	for i := range bindings {
		if bindings[i].Enabled {
			b := bindings[i]
			state.bindingByUser[b.UserID] = &b
		}
	}
	for _, ex := range exceptions {
		state.exceptionsByUser[ex.UserID] = append(state.exceptionsByUser[ex.UserID], ex)
	}
	for _, user := range users {
		if user.Status == "active" {
			state.activeUsers[user.ID] = true
		}
		state.computeUser(user.ID)
	}
	return state
}

func (st *planAccessState) computeUser(userID int64) map[string]bool {
	binding := st.bindingByUser[userID]
	var plan *model.SubscriptionPlan
	if binding != nil {
		plan = st.plansByID[binding.PlanID]
	}
	st.effective[userID] = UserEffectiveNodeSet(plan, st.planNodesByPlan[bindingPlanID(binding)], st.exceptionsByUser[userID], st.now)
	return st.effective[userID]
}

func (st *planAccessState) withPlanNodes(planID int64, nodes []model.SubscriptionPlanNode) *planAccessState {
	derived := &planAccessState{
		plansByID:        st.plansByID,
		bindingByUser:    st.bindingByUser,
		exceptionsByUser: st.exceptionsByUser,
		effective:        map[int64]map[string]bool{},
		activeUsers:      st.activeUsers,
		now:              st.now,
		planNodesByPlan:  map[int64][]model.SubscriptionPlanNode{},
	}
	for pid, existing := range st.planNodesByPlan {
		if pid == planID {
			continue
		}
		derived.planNodesByPlan[pid] = existing
	}
	kept := make([]model.SubscriptionPlanNode, 0, len(nodes))
	for _, pn := range nodes {
		if pn.Enabled {
			kept = append(kept, pn)
		}
	}
	derived.planNodesByPlan[planID] = kept
	for userID := range st.effective {
		derived.computeUser(userID)
	}
	return derived
}

// withAssignment derives a state where every user's active plan is replaced by
// the given target plan (nil removes the plan), keeping exceptions unchanged.
func (st *planAccessState) withAssignment(targetPlan *model.SubscriptionPlan, targetNodes []model.SubscriptionPlanNode) *planAccessState {
	derived := &planAccessState{
		plansByID:        st.plansByID,
		planNodesByPlan:  st.planNodesByPlan,
		bindingByUser:    st.bindingByUser,
		exceptionsByUser: st.exceptionsByUser,
		effective:        map[int64]map[string]bool{},
		activeUsers:      st.activeUsers,
		now:              st.now,
	}
	for userID := range st.effective {
		derived.effective[userID] = UserEffectiveNodeSet(targetPlan, targetNodes, st.exceptionsByUser[userID], st.now)
	}
	return derived
}

// validatePlanCapacity checks projected per-inbound user counts against the
// protocol capacity rules for the given effective sets.
func (st *planAccessState) validatePlanCapacity(target *planAccessState, paths []model.ProxyPath, inbounds []model.Inbound, now time.Time) []string {
	pathByID := map[int64]model.ProxyPath{}
	for _, path := range paths {
		if path.Enabled {
			pathByID[path.ID] = path
		}
	}
	inboundByID := map[int64]model.Inbound{}
	for _, inbound := range inbounds {
		inboundByID[inbound.ID] = inbound
	}
	counts := map[int64]int{}
	for userID, keys := range target.effective {
		if !target.activeUsers[userID] {
			continue
		}
		for key := range keys {
			nodeType, nodeID, ok := ParseNodeKey(key)
			if !ok {
				continue
			}
			switch nodeType {
			case model.AssignableNodeProxyPath:
				if path, ok := pathByID[nodeID]; ok {
					counts[path.InboundID]++
				}
			case model.AssignableNodeInbound:
				if _, ok := inboundByID[nodeID]; ok {
					counts[nodeID]++
				}
			}
		}
	}
	issues := []string{}
	for _, inbound := range inbounds {
		if counts[inbound.ID] > 1 && !InboundSupportsMultipleUsers(inbound) {
			issues = append(issues, fmt.Sprintf("入口 %s 仅支持单用户，当前预计 %d 个有效用户", inbound.Name, counts[inbound.ID]))
		}
	}
	sort.Strings(issues)
	return issues
}
