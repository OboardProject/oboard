package core

import (
	"sort"
	"strconv"

	"github.com/OboardProject/oboard/internal/model"
)

// AssignableNodeServerRole records one managed server's part in a node's
// topology. Roles are "entry", "transit" and "exit"; one server can hold
// several roles in one node.
type AssignableNodeServerRole struct {
	ServerID   int64    `json:"server_id"`
	ServerName string   `json:"server_name"`
	Roles      []string `json:"roles"`
}

// AssignableNodeTopology is the shared topology view used by the node catalog,
// the subscription generator and the node scope picker. It is derived from the
// real path plan, never from display text or egress probe ownership.
type AssignableNodeTopology struct {
	NodeType model.AssignableNodeType `json:"node_type"`
	NodeID   int64                    `json:"node_id"`
	NodeKey  string                   `json:"node_key"`

	EntryKey        string         `json:"entry_key"`
	EntryInboundID  int64          `json:"entry_inbound_id"`
	EntryServerID   int64          `json:"entry_server_id"`
	EntryServerName string         `json:"entry_server_name"`
	EntryRegion     string         `json:"entry_region"`
	EntryProtocol   model.Protocol `json:"entry_protocol"`
	EntryPort       int            `json:"entry_port"`

	ExitKind               string `json:"exit_kind"` // direct | server_inbound | warp | imported | unknown
	ExitServerID           int64  `json:"exit_server_id"`
	ExitServerName         string `json:"exit_server_name"`
	ExitExternalOutboundID int64  `json:"exit_external_outbound_id"`
	ExitRegion             string `json:"exit_region"`

	ServerRoles []AssignableNodeServerRole `json:"server_roles"`
}

// ResolveAssignableNodeTopologies builds the topology map for every assignable
// node from the routing snapshot and returns the name/exit-region resolved
// paths and externals so callers share one resolution pass. The map is keyed
// by the stable node key ("proxy_path:<id>", "external_outbound:<id>",
// "inbound:<id>").
func ResolveAssignableNodeTopologies(input AssignableNodeCatalogInput) (map[string]AssignableNodeTopology, []model.ProxyPath, []model.ExternalOutbound, error) {
	paths := ResolveProxyPathNames(input.ProxyPaths, input.ProxyPathSteps, input.Servers, input.Inbounds, input.ExternalOutbounds)
	var externals []model.ExternalOutbound
	paths, externals = ResolveProxyPathExitRegions(paths, input.ProxyPathSteps, input.Servers, input.Inbounds, input.ExternalOutbounds, input.EgressResults)
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
	out := map[string]AssignableNodeTopology{}
	for _, inbound := range input.Inbounds {
		if !inbound.Enabled {
			continue
		}
		// SSH renders only through proxy-path branches; keep the topology
		// consistent with the subscription generator and the node catalog.
		if inbound.Protocol == model.ProtocolSSH {
			continue
		}
		if len(subscriptionBranchesForInbound(inbound, paths, input.ProxyPathSteps)) > 0 {
			continue
		}
		server, ok := serverByID[inbound.ServerID]
		if !ok {
			continue
		}
		regionCode, _ := EffectiveServerRegion(server)
		node := AssignableNodeTopology{
			NodeType:        model.AssignableNodeInbound,
			NodeID:          inbound.ID,
			NodeKey:         NodeKeyOf(model.AssignableNodeInbound, inbound.ID),
			EntryKey:        "inbound:" + strconv.FormatInt(inbound.ID, 10),
			EntryInboundID:  inbound.ID,
			EntryServerID:   server.ID,
			EntryServerName: proxyPathServerLabel(server, server.ID),
			EntryRegion:     regionCode,
			EntryProtocol:   inbound.Protocol,
			EntryPort:       inbound.Port,
			ExitKind:        "direct",
			ExitServerID:    server.ID,
			ExitServerName:  proxyPathServerLabel(server, server.ID),
			ExitRegion:      regionCode,
			ServerRoles: []AssignableNodeServerRole{{
				ServerID:   server.ID,
				ServerName: proxyPathServerLabel(server, server.ID),
				Roles:      []string{"entry", "exit"},
			}},
		}
		out[node.NodeKey] = node
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
		rootName := proxyPathServerLabel(rootServer, rootServer.ID)
		entryRegion, _ := EffectiveServerRegion(rootServer)
		node := AssignableNodeTopology{
			NodeType:        model.AssignableNodeProxyPath,
			NodeID:          path.ID,
			NodeKey:         NodeKeyOf(model.AssignableNodeProxyPath, path.ID),
			EntryKey:        "inbound:" + strconv.FormatInt(root.ID, 10),
			EntryInboundID:  root.ID,
			EntryServerID:   root.ServerID,
			EntryServerName: rootName,
			EntryRegion:     entryRegion,
			EntryProtocol:   root.Protocol,
			EntryPort:       root.Port,
			ExitRegion:      path.EffectiveExitRegionCode,
		}
		roleIndex := map[int64]int{}
		addRole := func(serverID int64, role string) {
			if serverID == 0 {
				return
			}
			index, ok := roleIndex[serverID]
			if !ok {
				name := proxyPathServerLabel(serverByID[serverID], serverID)
				index = len(node.ServerRoles)
				roleIndex[serverID] = index
				node.ServerRoles = append(node.ServerRoles, AssignableNodeServerRole{ServerID: serverID, ServerName: name})
			}
			node.ServerRoles[index].Roles = append(node.ServerRoles[index].Roles, role)
		}
		addRole(root.ServerID, "entry")
		steps := orderedProxyPathSteps(stepsByPath[path.ID])
		if len(steps) == 0 {
			node.ExitKind = "direct"
			node.ExitServerID = root.ServerID
			node.ExitServerName = rootName
			addRole(root.ServerID, "exit")
			out[node.NodeKey] = node
			continue
		}
		for position, step := range steps {
			switch step.NodeType {
			case model.ProxyPathStepServerInbound:
				serverID, _, found := proxyPathStepTargetServer(step, inboundByID)
				if !found {
					continue
				}
				role := "transit"
				if position == len(steps)-1 {
					role = "exit"
					node.ExitKind = "server_inbound"
					node.ExitServerID = serverID
					node.ExitServerName = proxyPathServerLabel(serverByID[serverID], serverID)
				}
				addRole(serverID, role)
			case model.ProxyPathStepWARP:
				if step.ServerID != nil {
					role := "transit"
					if position == len(steps)-1 {
						role = "exit"
						node.ExitKind = "warp"
						node.ExitServerID = *step.ServerID
						node.ExitServerName = proxyPathServerLabel(serverByID[*step.ServerID], *step.ServerID)
					}
					addRole(*step.ServerID, role)
				}
			case model.ProxyPathStepImported:
				if position == len(steps)-1 && step.ExternalOutboundID != nil {
					node.ExitKind = "imported"
					node.ExitExternalOutboundID = *step.ExternalOutboundID
					if external, ok := externalByID[*step.ExternalOutboundID]; ok {
						if external.ServerID != nil && *external.ServerID != 0 {
							node.ExitServerID = *external.ServerID
							node.ExitServerName = proxyPathServerLabel(serverByID[*external.ServerID], *external.ServerID)
							addRole(*external.ServerID, "exit")
						}
					}
				}
			}
		}
		if node.ExitKind == "" {
			node.ExitKind = "unknown"
		}
		for i := range node.ServerRoles {
			sort.Strings(node.ServerRoles[i].Roles)
		}
		out[node.NodeKey] = node
	}
	for _, external := range externals {
		if !external.Enabled || !external.ExposeToUsers {
			continue
		}
		node := AssignableNodeTopology{
			NodeType:               model.AssignableNodeExternalOutbound,
			NodeID:                 external.ID,
			NodeKey:                NodeKeyOf(model.AssignableNodeExternalOutbound, external.ID),
			ExitKind:               "imported",
			ExitExternalOutboundID: external.ID,
			ExitRegion:             external.EffectiveRegionCode,
		}
		if external.ServerID != nil && *external.ServerID != 0 {
			node.ExitServerID = *external.ServerID
			node.ExitServerName = proxyPathServerLabel(serverByID[*external.ServerID], *external.ServerID)
			node.ServerRoles = []AssignableNodeServerRole{{
				ServerID:   *external.ServerID,
				ServerName: node.ExitServerName,
				Roles:      []string{"exit"},
			}}
		}
		out[node.NodeKey] = node
	}
	return out, paths, externals, nil
}
