package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestLedgerResolveUsesActiveGenerationOnly(t *testing.T) {
	stored := []model.ProxyPathPortAllocation{
		{ID: 1, Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Generation: 1, Ordinal: 0, Port: 41001, State: model.PortAllocationStateActive, PolicyRevision: 1},
		{ID: 2, Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Generation: 2, Ordinal: 0, Port: 43001, State: model.PortAllocationStatePreparing, PolicyRevision: 2},
	}
	ledger := NewProxyPathPortLedger(stored)
	port := ledger.resolve(PortRequirement{
		Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1,
		Pool: model.PortPoolPublic, Network: model.ForwardProtocolTCPUDP,
		Allocate: func() int { t.Fatal("active generation must be used"); return 0 },
	})
	if port != 41001 {
		t.Fatalf("ordinary resolve read %d, want active 41001 (preparing 43001 must not leak)", port)
	}
}

func TestLedgerAllocatePreparingAtomicMultiport(t *testing.T) {
	ledger := NewProxyPathPortLedger(nil)
	active := ledger.resolve(PortRequirement{
		Kind: model.ProxyPathPortKindChainService, ScopeKey: "mieru", ServerID: 1,
		Pool: model.PortPoolPublic, Network: model.ForwardProtocolTCP, PolicyRevision: 1,
		Allocate: func() int { return 10001 },
	})
	if active != 10001 {
		t.Fatalf("active port = %d", active)
	}
	call := 0
	rows, err := ledger.AllocatePreparing(PortRequirement{
		Kind: model.ProxyPathPortKindChainService, ScopeKey: "mieru", ServerID: 1,
		Pool: model.PortPoolPublic, Network: model.ForwardProtocolTCP, PolicyRevision: 2,
		AllocateOrdinal: func(ordinal int) int {
			call++
			if call == 3 {
				return 0 // third ordinal unavailable: the whole group must fail
			}
			return 20001 + ordinal
		},
	}, 3)
	if err == nil {
		t.Fatal("group allocation with an unavailable ordinal must fail")
	}
	if len(ledger.Pending()) != 1 {
		t.Fatalf("failed group left pending rows behind: %#v", ledger.Pending())
	}
	if port := ledger.resolve(PortRequirement{Kind: model.ProxyPathPortKindChainService, ScopeKey: "mieru", ServerID: 1, Pool: model.PortPoolPublic, Allocate: func() int { return 0 }}); port != 10001 {
		t.Fatalf("failed group changed active port to %d", port)
	}

	rows, err = ledger.AllocatePreparing(PortRequirement{
		Kind: model.ProxyPathPortKindChainService, ScopeKey: "mieru", ServerID: 1,
		Pool: model.PortPoolPublic, Network: model.ForwardProtocolTCP, PolicyRevision: 2,
		AllocateOrdinal: func(ordinal int) int { return 20001 + ordinal },
	}, 3)
	if err != nil {
		t.Fatalf("group allocation failed: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("preparing rows = %d, want 3", len(rows))
	}
	for ordinal, row := range rows {
		if row.Generation != 2 || row.Ordinal != ordinal || row.Port != 20001+ordinal || row.State != model.PortAllocationStatePreparing || row.PolicyRevision != 2 {
			t.Fatalf("preparing row %d = %#v", ordinal, row)
		}
	}
}

func TestLedgerPhaseRowsAndCutover(t *testing.T) {
	// Persisted rows carry IDs; in-memory pending rows have ID 0 and nothing to
	// delete yet. Rebuilding from stored rows mirrors the Controller restart
	// path and lets DeleteRetired report the retired row ID.
	stored := []model.ProxyPathPortAllocation{
		{ID: 9, Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Network: "tcp_udp", Generation: 1, Ordinal: 0, Port: 41001, State: model.PortAllocationStateActive, PolicyRevision: 1},
		{ID: 10, Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Network: "tcp_udp", Generation: 2, Ordinal: 0, Port: 43001, State: model.PortAllocationStatePreparing, PolicyRevision: 2},
	}
	ledger := NewProxyPathPortLedger(stored)
	requirement := PortRequirement{Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Network: model.ForwardProtocolTCPUDP}

	if rows := ledger.RowsForPhase(requirement, PortMigrationPhasePrepare); len(rows) != 2 || rows[0].Port != 41001 || rows[1].Port != 43001 {
		t.Fatalf("prepare rows = %#v", rows)
	}
	if port := ledger.ResolveForPhase(requirement, PortMigrationPhasePrepare); port != 41001 {
		t.Fatalf("prepare consumer port = %d, want 41001", port)
	}
	if port := ledger.ResolveForPhase(requirement, PortMigrationPhaseSwitch); port != 43001 {
		t.Fatalf("switch consumer port = %d, want 43001", port)
	}

	promoted, err := ledger.PromotePreparing(model.ProxyPathPortKindChainService, "ss", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(promoted) != 2 {
		t.Fatalf("promote rows = %#v", promoted)
	}
	if port := ledger.resolve(requirement); port != 43001 {
		t.Fatalf("post-promote resolve = %d, want 43001", port)
	}
	if rows := ledger.RowsForPhase(requirement, PortMigrationPhaseRetire); len(rows) != 2 || rows[0].State != model.PortAllocationStateRetiring || rows[1].State != model.PortAllocationStateActive {
		t.Fatalf("retire rows = %#v", rows)
	}

	ids, err := ledger.DeleteRetired(model.ProxyPathPortKindChainService, "ss", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("deleted retired ids = %#v", ids)
	}
	if ids[0] != 9 {
		t.Fatalf("deleted id = %d, want the persisted row id 9", ids[0])
	}
	if port := ledger.resolve(requirement); port != 43001 {
		t.Fatalf("post-retire resolve = %d, want 43001", port)
	}
}

func TestLedgerStaleCleanupKeepsClaimedGenerationsAndReleasesUnclaimed(t *testing.T) {
	stored := []model.ProxyPathPortAllocation{
		{ID: 1, Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Generation: 1, Ordinal: 0, Port: 41001, State: model.PortAllocationStateActive},
		{ID: 2, Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Generation: 2, Ordinal: 0, Port: 43001, State: model.PortAllocationStatePreparing},
		{ID: 3, Kind: model.ProxyPathPortKindTunnelWG, ScopeKey: "9", ServerID: 2, Generation: 1, Ordinal: 0, Port: 42001, State: model.PortAllocationStateActive},
	}
	ledger := NewProxyPathPortLedger(stored)
	ledger.resolve(PortRequirement{Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Allocate: func() int { return 0 }})
	ledger.markProjectionComplete()

	stale := StaleProxyPathPortAllocationIDs(stored, ledger)
	// The claimed owner keeps both generations; the unclaimed owner releases.
	if len(stale) != 1 || stale[0] != 3 {
		t.Fatalf("stale ids = %#v, want [3]", stale)
	}

	if _, err := ledger.PromotePreparing(model.ProxyPathPortKindChainService, "ss", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.DeleteRetired(model.ProxyPathPortKindChainService, "ss", 1, 1); err != nil {
		t.Fatal(err)
	}
	stale = StaleProxyPathPortAllocationIDs(stored, ledger)
	if len(stale) != 2 {
		t.Fatalf("stale ids after DeleteRetired = %#v, want [3 1]", stale)
	}
}

func TestLedgerGenerationResumeAfterControllerRestart(t *testing.T) {
	// Rebuild the ledger from persisted rows exactly like Controller does on
	// startup, with an active and a preparing generation interleaved by
	// generation number, and verify the active one still wins and the switch
	// phase still resolves the preparing one.
	stored := []model.ProxyPathPortAllocation{
		{ID: 10, Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Generation: 2, Ordinal: 0, Port: 43001, State: model.PortAllocationStatePreparing, PolicyRevision: 2},
		{ID: 9, Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Generation: 1, Ordinal: 0, Port: 41001, State: model.PortAllocationStateActive, PolicyRevision: 1},
	}
	ledger := NewProxyPathPortLedger(stored)
	requirement := PortRequirement{Kind: model.ProxyPathPortKindChainService, ScopeKey: "ss", ServerID: 1, Pool: model.PortPoolPublic, Network: model.ForwardProtocolTCPUDP}
	if port := ledger.resolve(requirement); port != 41001 {
		t.Fatalf("restart resolve = %d, want active 41001", port)
	}
	if port := ledger.ResolveForPhase(requirement, PortMigrationPhaseSwitch); port != 43001 {
		t.Fatalf("restart switch resolve = %d, want preparing 43001", port)
	}
	rows := ledger.RowsForPhase(requirement, PortMigrationPhasePrepare)
	if len(rows) != 2 {
		t.Fatalf("restart prepare rows = %#v", rows)
	}
}
