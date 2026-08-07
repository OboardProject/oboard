package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func portRequirement(server int64, policyRevision int64) PortRequirement {
	return PortRequirement{
		Kind:           model.ProxyPathPortKindChainService,
		ScopeKey:       "2022-blake3-aes-128-gcm",
		ServerID:       server,
		Pool:           model.PortPoolPublic,
		ListenIP:       "0.0.0.0",
		Network:        model.ForwardProtocolTCPUDP,
		PolicyRevision: policyRevision,
		Allocate:       func() int { return 41001 },
	}
}

func TestLedgerResolveUsesOnlyActiveGeneration(t *testing.T) {
	stored := []model.ProxyPathPortAllocation{
		{ID: 1, Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: 9, Pool: model.PortPoolPublic, Network: "tcp_udp", Generation: 1, Ordinal: 0, Port: 41001, State: model.PortAllocationStateActive, PolicyRevision: 1},
		{ID: 2, Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: 9, Pool: model.PortPoolPublic, Network: "tcp_udp", Generation: 2, Ordinal: 0, Port: 43001, State: model.PortAllocationStatePreparing, PolicyRevision: 2},
	}
	ledger := NewProxyPathPortLedger(stored)
	port := ledger.resolve(portRequirement(9, 2))
	if port != 41001 {
		t.Fatalf("normal deployment dialed preparing port %d, want active 41001", port)
	}
	// The preparing generation belongs to the migration flow, not to stale
	// cleanup: a complete normal projection must not delete it.
	ledger.markProjectionComplete()
	if stale := StaleProxyPathPortAllocationIDs(stored, ledger); len(stale) != 0 {
		t.Fatalf("preparing generation reported stale: %#v", stale)
	}
}

func TestLedgerAllocatePreparingKeepsActiveAndRecordsRevision(t *testing.T) {
	ledger := NewProxyPathPortLedger(nil)
	active := ledger.resolve(portRequirement(9, 1))
	if active != 41001 {
		t.Fatalf("active port = %d", active)
	}
	requirement := portRequirement(9, 3)
	requirement.Allocate = func() int { return 43001 }
	rows, err := ledger.AllocatePreparing(requirement, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("preparing rows = %d", len(rows))
	}
	row := rows[0]
	if row.Generation != 2 || row.Ordinal != 0 || row.Port != 43001 || row.State != model.PortAllocationStatePreparing || row.PolicyRevision != 3 {
		t.Fatalf("preparing row = %#v", row)
	}
	pending := ledger.Pending()
	if len(pending) != 2 {
		t.Fatalf("pending = %#v", pending)
	}
	// The active generation still dials the old port.
	if port := ledger.ResolveForPhase(requirement, PortMigrationPhasePrepare); port != 41001 {
		t.Fatalf("prepare phase port = %d, want active 41001", port)
	}
	if port := ledger.ResolveForPhase(requirement, PortMigrationPhaseSwitch); port != 43001 {
		t.Fatalf("switch phase port = %d, want preparing 43001", port)
	}
}

func TestLedgerAllocatePreparingIsAtomicForMultiport(t *testing.T) {
	ledger := NewProxyPathPortLedger(nil)
	if port := ledger.resolve(portRequirement(9, 1)); port != 41001 {
		t.Fatalf("active port = %d", port)
	}
	requirement := portRequirement(9, 2)
	requirement.AllocateOrdinal = func(ordinal int) int {
		if ordinal == 1 {
			return 0 // second ordinal cannot be satisfied
		}
		return 43000 + ordinal
	}
	_, err := ledger.AllocatePreparing(requirement, 3)
	if err == nil {
		t.Fatal("group allocation must fail when any ordinal has no port")
	}
	pending := ledger.Pending()
	if len(pending) != 1 {
		t.Fatalf("failed group left %d pending rows, want only the active one: %#v", len(pending), pending)
	}
}

func TestLedgerAllocatePreparingRequiresActiveGeneration(t *testing.T) {
	ledger := NewProxyPathPortLedger(nil)
	if _, err := ledger.AllocatePreparing(portRequirement(9, 1), 1); err == nil {
		t.Fatal("reserving a preparing generation without an active generation must fail")
	}
}

func TestLedgerPromotePreparingRetiresOldGeneration(t *testing.T) {
	stored := []model.ProxyPathPortAllocation{
		{ID: 1, Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: 9, Pool: model.PortPoolPublic, Network: "tcp_udp", Generation: 1, Ordinal: 0, Port: 41001, State: model.PortAllocationStateActive, PolicyRevision: 1},
		{ID: 2, Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: 9, Pool: model.PortPoolPublic, Network: "tcp_udp", Generation: 2, Ordinal: 0, Port: 43001, State: model.PortAllocationStatePreparing, PolicyRevision: 2},
	}
	ledger := NewProxyPathPortLedger(stored)
	requirement := portRequirement(9, 2)
	if _, err := ledger.PromotePreparing(requirement.Kind, requirement.ScopeKey, requirement.ServerID); err != nil {
		t.Fatal(err)
	}
	if port := ledger.resolve(requirement); port != 43001 {
		t.Fatalf("after promotion active port = %d, want 43001", port)
	}
	if rows := ledger.RowsForPhase(requirement, PortMigrationPhaseRetire); len(rows) != 2 {
		t.Fatalf("retire phase rows = %#v, want active + retiring", rows)
	}
	// DeleteRetired drops only the old generation and reports its persisted id.
	ids, err := ledger.DeleteRetired(requirement.Kind, requirement.ScopeKey, requirement.ServerID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("retired ids = %#v", ids)
	}
	if _, err := ledger.DeleteRetired(requirement.Kind, requirement.ScopeKey, requirement.ServerID, 1); err == nil {
		t.Fatal("deleting an already-deleted generation must fail")
	}
	if port := ledger.resolve(requirement); port != 43001 {
		t.Fatalf("port after retire = %d, want 43001", port)
	}
}

func TestLedgerStaleCleanupReleasesEveryGenerationOfUnclaimedOwner(t *testing.T) {
	stored := []model.ProxyPathPortAllocation{
		{ID: 1, Kind: model.ProxyPathPortKindChainService, ScopeKey: "gone", ServerID: 9, Generation: 1, Ordinal: 0, Port: 41001, State: model.PortAllocationStateActive},
		{ID: 2, Kind: model.ProxyPathPortKindChainService, ScopeKey: "gone", ServerID: 9, Generation: 2, Ordinal: 0, Port: 43001, State: model.PortAllocationStatePreparing},
	}
	ledger := NewProxyPathPortLedger(stored)
	ledger.resolve(portRequirement(9, 1))
	ledger.markProjectionComplete()
	stale := StaleProxyPathPortAllocationIDs(stored, ledger)
	if len(stale) != 2 || stale[0] != 1 || stale[1] != 2 {
		t.Fatalf("unclaimed owner stale ids = %#v, want both generations", stale)
	}
}

func TestLedgerMarkActiveRetiring(t *testing.T) {
	ledger := NewProxyPathPortLedger(nil)
	if port := ledger.resolve(portRequirement(9, 1)); port != 41001 {
		t.Fatalf("active port = %d", port)
	}
	if _, err := ledger.MarkActiveRetiring(model.ProxyPathPortKindChainService, "2022-blake3-aes-128-gcm", 9); err != nil {
		t.Fatal(err)
	}
	if rows := ledger.RowsForPhase(portRequirement(9, 1), PortMigrationPhaseRetire); len(rows) != 1 || rows[0].State != model.PortAllocationStateRetiring {
		t.Fatalf("retire rows = %#v", rows)
	}
	// No active generation remains, so the next resolve must allocate fresh.
	requirement := portRequirement(9, 2)
	requirement.Allocate = func() int { return 44001 }
	if port := ledger.resolve(requirement); port != 44001 {
		t.Fatalf("re-allocated active port = %d, want 44001", port)
	}
}

func TestLedgerMultiportGroupKeepsOrdinals(t *testing.T) {
	ledger := NewProxyPathPortLedger(nil)
	requirement := PortRequirement{
		Kind:           model.ProxyPathPortKindInternal,
		ScopeKey:       "inbound:7:1",
		ServerID:       9,
		Pool:           model.PortPoolPublic,
		ListenIP:       "0.0.0.0",
		Network:        model.ForwardProtocolTCP,
		PolicyRevision: 4,
		AllocateOrdinal: func(ordinal int) int {
			return 45000 + ordinal
		},
	}
	if port := ledger.resolve(requirement); port != 45000 {
		t.Fatalf("active port = %d", port)
	}
	requirement.AllocateOrdinal = func(ordinal int) int { return 46000 + ordinal }
	rows, err := ledger.AllocatePreparing(requirement, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("group rows = %d", len(rows))
	}
	for index, row := range rows {
		if row.Ordinal != index || row.Port != 46000+index || row.Generation != 2 || row.State != model.PortAllocationStatePreparing || row.PolicyRevision != 4 {
			t.Fatalf("group row %d = %#v", index, row)
		}
	}
}
