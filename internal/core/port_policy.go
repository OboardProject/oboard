package core

import (
	"github.com/OboardProject/oboard/internal/model"
)

// ServerPortPolicy describes the two managed allocation pools of one server:
// the public auto pool for listeners other devices or servers must reach, and
// the internal loopback pool for components that bind 127.0.0.1 or ::1 only.
type ServerPortPolicy struct {
	PublicStart   int
	PublicEnd     int
	InternalStart int
	InternalEnd   int
}

func ServerPortPolicyFor(server model.Server) ServerPortPolicy {
	policy := ServerPortPolicy{PublicStart: server.PortRangeStart, PublicEnd: server.PortRangeEnd, InternalStart: server.InternalPortRangeStart, InternalEnd: server.InternalPortRangeEnd}
	if policy.PublicStart <= 0 || policy.PublicEnd < policy.PublicStart {
		policy.PublicStart, policy.PublicEnd = 30000, 60000
	}
	if policy.InternalStart <= 0 || policy.InternalEnd < policy.InternalStart {
		policy.InternalStart, policy.InternalEnd = 30000, 59999
	}
	return policy
}

func (p ServerPortPolicy) contains(pool string, port int) bool {
	if pool == model.PortPoolInternal {
		return port >= p.InternalStart && port <= p.InternalEnd
	}
	return port >= p.PublicStart && port <= p.PublicEnd
}

// PortPolicyAffectedItem is one listener the preview found outside a pool.
type PortPolicyAffectedItem struct {
	Kind     string `json:"kind"`
	ScopeKey string `json:"scope_key"`
	ServerID int64  `json:"server_id"`
	Pool     string `json:"pool"`
	ListenIP string `json:"listen_ip"`
	Network  string `json:"network"`
	Port     int    `json:"port"`
}

// ServerPortPolicyChangePreview describes what a server port-range PATCH would
// affect. Managed (ledger-owned) listeners outside their pool force a 409 until
// the migration flow can move them; manual inbounds outside the public pool are
// warnings only and never block the update.
type ServerPortPolicyChangePreview struct {
	PublicRangeChanged   bool                     `json:"public_range_changed"`
	InternalRangeChanged bool                     `json:"internal_range_changed"`
	AffectedManaged      []PortPolicyAffectedItem `json:"affected_managed"`
	ManualOutsidePolicy  []PortPolicyAffectedItem `json:"manual_outside_policy"`
}

func (p ServerPortPolicyChangePreview) RequiresMigration() bool {
	return len(p.AffectedManaged) > 0
}

// PreviewServerPortPolicyChange compares the current and proposed port policy
// for one server and reports which persisted listeners would fall outside their
// pool. Only the pools whose range actually changed are checked, so widening or
// narrowing the public range never moves internal loopback listeners and vice
// versa. Managed allocations are classified by their persisted pool, falling
// back to the kind for rows created before pool metadata existed.
func PreviewServerPortPolicyChange(current, next model.Server, allocations []model.ProxyPathPortAllocation, inbounds []model.Inbound) ServerPortPolicyChangePreview {
	preview := ServerPortPolicyChangePreview{}
	preview.PublicRangeChanged = current.PortRangeStart != next.PortRangeStart || current.PortRangeEnd != next.PortRangeEnd
	preview.InternalRangeChanged = current.InternalPortRangeStart != next.InternalPortRangeStart || current.InternalPortRangeEnd != next.InternalPortRangeEnd
	if !preview.PublicRangeChanged && !preview.InternalRangeChanged {
		return preview
	}
	nextPolicy := ServerPortPolicyFor(next)
	for _, item := range allocations {
		if item.ServerID != next.ID || item.Port <= 0 {
			continue
		}
		pool := item.Pool
		if pool == "" {
			pool = PortAllocationPoolForKind(item.Kind)
		}
		if pool == model.PortPoolInternal && !preview.InternalRangeChanged {
			continue
		}
		if pool == model.PortPoolPublic && !preview.PublicRangeChanged {
			continue
		}
		if nextPolicy.contains(pool, item.Port) {
			continue
		}
		preview.AffectedManaged = append(preview.AffectedManaged, PortPolicyAffectedItem{
			Kind: item.Kind, ScopeKey: item.ScopeKey, ServerID: item.ServerID,
			Pool: pool, ListenIP: item.ListenIP, Network: item.Network, Port: item.Port,
		})
	}
	if !preview.PublicRangeChanged {
		return preview
	}
	for _, inbound := range inbounds {
		if inbound.ServerID != next.ID || inbound.Port <= 0 {
			continue
		}
		if nextPolicy.contains(model.PortPoolPublic, inbound.Port) {
			continue
		}
		preview.ManualOutsidePolicy = append(preview.ManualOutsidePolicy, PortPolicyAffectedItem{
			Kind: "inbound", ScopeKey: "", ServerID: inbound.ServerID,
			Pool: model.PortPoolPublic, ListenIP: inbound.ListenIP, Network: forwardNetworkName(transparentForwardProtocol(inbound)), Port: inbound.Port,
		})
	}
	return preview
}
