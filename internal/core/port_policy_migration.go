package core

import (
	"fmt"
	"sort"

	"github.com/OboardProject/oboard/internal/model"
)

// PortPolicyMove is one managed listener a port-range change moved to a new
// port inside its pool.
type PortPolicyMove struct {
	Kind     string `json:"kind"`
	ScopeKey string `json:"scope_key"`
	ServerID int64  `json:"server_id"`
	Pool     string `json:"pool"`
	FromPort int    `json:"from_port"`
	ToPort   int    `json:"to_port"`
}

// ServerPortPolicyMigration is the ledger rewrite that lets a port-range change
// keep every managed listener inside its pool. RemovedIDs are the rows of the
// affected owners (every generation) and Added are their replacement rows; both
// must be committed together with the server's new range.
type ServerPortPolicyMigration struct {
	Moves      []PortPolicyMove                `json:"moves"`
	Released   []PortPolicyAffectedItem        `json:"released,omitempty"`
	Removed    []model.ProxyPathPortAllocation `json:"-"`
	Added      []model.ProxyPathPortAllocation `json:"-"`
	RemovedIDs []int64                         `json:"-"`
}

func (m ServerPortPolicyMigration) Empty() bool {
	return len(m.RemovedIDs) == 0 && len(m.Added) == 0
}

// PortPolicyMigrationInput is the desired state a migration plan projects. The
// servers slice must already carry the proposed policy for the changed server.
type PortPolicyMigrationInput struct {
	Server      model.Server
	Servers     []model.Server
	Inbounds    []model.Inbound
	Paths       []model.ProxyPath
	Steps       []model.ProxyPathStep
	Forwards    []model.PortForward
	Allocations []model.ProxyPathPortAllocation
}

// portPolicyReservationIDBase keeps the synthetic occupancy rows far from real
// inbound IDs and from every negative range generated listeners use.
const portPolicyReservationIDBase = int64(1) << 53

// PlanServerPortPolicyMigration reassigns every managed listener the preview
// found outside its pool. The affected owners lose their stored rows and the
// ordinary deployment projection allocates them again against the proposed
// policy, so the new ports come from the same allocator, seeds and listen
// conflict model a deployment uses. Every other stored listener on the server
// and every manual port forward is reserved first, so a moved owner can never
// take a port another listener still holds and no unaffected owner moves.
// Exhaustion of the new range fails the whole plan; nothing is partially moved.
func PlanServerPortPolicyMigration(preview ServerPortPolicyChangePreview, input PortPolicyMigrationInput) (ServerPortPolicyMigration, error) {
	plan := ServerPortPolicyMigration{}
	if !preview.RequiresMigration() {
		return plan, nil
	}
	affected := map[proxyPathPortKey]PortPolicyAffectedItem{}
	for _, item := range preview.AffectedManaged {
		if item.Kind == model.ProxyPathPortKindSnellUser {
			return plan, fmt.Errorf("Snell 独立用户监听 %s 无法自动迁移", item.ScopeKey)
		}
		affected[proxyPathPortKey{Kind: item.Kind, ScopeKey: item.ScopeKey, ServerID: item.ServerID}] = item
	}
	retained := make([]model.ProxyPathPortAllocation, 0, len(input.Allocations))
	inbounds := append([]model.Inbound(nil), input.Inbounds...)
	for _, row := range input.Allocations {
		if _, ok := affected[proxyPathPortKey{Kind: row.Kind, ScopeKey: row.ScopeKey, ServerID: row.ServerID}]; ok {
			plan.Removed = append(plan.Removed, row)
			if row.ID > 0 {
				plan.RemovedIDs = append(plan.RemovedIDs, row.ID)
			}
			continue
		}
		retained = append(retained, row)
		if row.ServerID == input.Server.ID && row.Port > 0 {
			inbounds = append(inbounds, portPolicyReservation(len(inbounds), input.Server.ID, row.ListenIP, row.Port, model.ProtocolSS))
		}
	}
	for _, forward := range input.Forwards {
		if forward.SourceServerID != input.Server.ID || forward.ListenPort <= 0 {
			continue
		}
		protocol := model.ProtocolSS
		switch forward.Protocol {
		case model.ForwardProtocolTCP:
			protocol = model.ProtocolVLESS
		case model.ForwardProtocolUDP:
			protocol = model.ProtocolHY2
		}
		inbounds = append(inbounds, portPolicyReservation(len(inbounds), input.Server.ID, forward.ListenIP, forward.ListenPort, protocol))
	}
	ledger := NewProxyPathPortLedger(retained)
	if _, err := BuildProxyPathPlansWithLedger(input.Paths, input.Steps, input.Servers, inbounds, ledger); err != nil {
		return plan, err
	}
	policy := ServerPortPolicyFor(input.Server)
	claimed := map[int]proxyPathPortKey{}
	for _, row := range ledger.Pending() {
		key := proxyPathPortKey{Kind: row.Kind, ScopeKey: row.ScopeKey, ServerID: row.ServerID}
		item, ok := affected[key]
		if !ok {
			continue
		}
		if !policy.contains(item.Pool, row.Port) {
			return plan, fmt.Errorf("%s %s 重新分配的端口 %d 不在新范围内", item.Kind, item.ScopeKey, row.Port)
		}
		if other, taken := claimed[row.Port]; taken && other != key {
			return plan, fmt.Errorf("%s %s 与 %s %s 被分配到同一端口 %d", item.Kind, item.ScopeKey, other.Kind, other.ScopeKey, row.Port)
		}
		claimed[row.Port] = key
		plan.Added = append(plan.Added, row)
		plan.Moves = append(plan.Moves, PortPolicyMove{Kind: item.Kind, ScopeKey: item.ScopeKey, ServerID: item.ServerID, Pool: item.Pool, FromPort: item.Port, ToPort: row.Port})
		delete(affected, key)
	}
	// An owner the projection no longer resolves (a disabled or removed path)
	// has no listener to move; its stale rows are released instead.
	for _, item := range affected {
		plan.Released = append(plan.Released, item)
	}
	sort.Slice(plan.Moves, func(i, j int) bool {
		if plan.Moves[i].Kind == plan.Moves[j].Kind {
			return plan.Moves[i].ScopeKey < plan.Moves[j].ScopeKey
		}
		return plan.Moves[i].Kind < plan.Moves[j].Kind
	})
	sort.Slice(plan.Released, func(i, j int) bool {
		if plan.Released[i].Kind == plan.Released[j].Kind {
			return plan.Released[i].ScopeKey < plan.Released[j].ScopeKey
		}
		return plan.Released[i].Kind < plan.Released[j].Kind
	})
	return plan, nil
}

// portPolicyReservation is a disabled synthetic inbound that only occupies a
// listen resource. The allocator reserves disabled inbounds, while implicit
// inbound binding and config generation ignore them.
func portPolicyReservation(index int, serverID int64, listenIP string, port int, protocol model.Protocol) model.Inbound {
	return model.Inbound{
		ID:         -(portPolicyReservationIDBase + int64(index)),
		ServerID:   serverID,
		Name:       "port-policy-reservation",
		Protocol:   protocol,
		ListenIP:   listenIP,
		Port:       port,
		ConfigJSON: `{}`,
		Enabled:    false,
	}
}
