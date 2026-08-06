package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestPreviewServerPortPolicyChangeWideningMovesNothing(t *testing.T) {
	current := model.Server{ID: 1, PortRangeStart: 10000, PortRangeEnd: 10100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	next := model.Server{ID: 1, PortRangeStart: 9000, PortRangeEnd: 20000, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	allocations := []model.ProxyPathPortAllocation{
		{Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: 1, Pool: model.PortPoolPublic, Port: 10050},
	}
	preview := PreviewServerPortPolicyChange(current, next, allocations, nil)
	if preview.RequiresMigration() || len(preview.AffectedManaged) != 0 {
		t.Fatalf("widening must not move ports: %#v", preview)
	}
}

func TestPreviewServerPortPolicyChangeNarrowingExcludesManagedPort(t *testing.T) {
	current := model.Server{ID: 1, PortRangeStart: 10000, PortRangeEnd: 10100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	next := model.Server{ID: 1, PortRangeStart: 20000, PortRangeEnd: 20100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	allocations := []model.ProxyPathPortAllocation{
		{Kind: model.ProxyPathPortKindChainService, ScopeKey: "2022-blake3-aes-128-gcm", ServerID: 1, Pool: model.PortPoolPublic, Port: 10050},
		{Kind: model.ProxyPathPortKindInternal, ScopeKey: "7:2", ServerID: 1, Pool: model.PortPoolPublic, Port: 10060},
	}
	preview := PreviewServerPortPolicyChange(current, next, allocations, nil)
	if !preview.RequiresMigration() || len(preview.AffectedManaged) != 2 {
		t.Fatalf("affected managed = %#v, want both public listeners", preview.AffectedManaged)
	}
}

func TestPreviewServerPortPolicyChangePublicChangeIgnoresInternalPool(t *testing.T) {
	current := model.Server{ID: 1, PortRangeStart: 10000, PortRangeEnd: 10100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	next := model.Server{ID: 1, PortRangeStart: 20000, PortRangeEnd: 20100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	allocations := []model.ProxyPathPortAllocation{
		// Loopback-only listeners keep working no matter what the public range is.
		{Kind: model.ProxyPathPortKindTrustedInner, ScopeKey: "7:2", ServerID: 1, Pool: model.PortPoolInternal, Port: 40010},
		{Kind: model.ProxyPathPortKindTunnelSSH, ScopeKey: "555", ServerID: 1, Pool: model.PortPoolInternal, Port: 40020},
	}
	preview := PreviewServerPortPolicyChange(current, next, allocations, nil)
	if preview.RequiresMigration() || len(preview.AffectedManaged) != 0 {
		t.Fatalf("public range change must not migrate internal pool: %#v", preview)
	}
}

func TestPreviewServerPortPolicyChangeInternalChangeExcludesLegacyLoopbackPort(t *testing.T) {
	current := model.Server{ID: 1, PortRangeStart: 10000, PortRangeEnd: 10100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	next := model.Server{ID: 1, PortRangeStart: 10000, PortRangeEnd: 10100, InternalPortRangeStart: 60000, InternalPortRangeEnd: 61000}
	allocations := []model.ProxyPathPortAllocation{
		// Legacy rows predate the pool column; the kind classifies them.
		{Kind: model.ProxyPathPortKindTunnelSSH, ScopeKey: "555", ServerID: 1, Port: 40020},
		{Kind: model.ProxyPathPortKindTrustedInner, ScopeKey: "7:2", ServerID: 1, Port: 40010},
	}
	preview := PreviewServerPortPolicyChange(current, next, allocations, nil)
	if !preview.RequiresMigration() || len(preview.AffectedManaged) != 2 {
		t.Fatalf("internal range change must flag loopback listeners: %#v", preview.AffectedManaged)
	}
}

func TestPreviewServerPortPolicyChangeManualInboundIsWarningOnly(t *testing.T) {
	current := model.Server{ID: 1, PortRangeStart: 10000, PortRangeEnd: 10100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	next := model.Server{ID: 1, PortRangeStart: 20000, PortRangeEnd: 20100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	manual := model.Inbound{ID: 3, ServerID: 1, Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, Enabled: true}
	preview := PreviewServerPortPolicyChange(current, next, nil, []model.Inbound{manual})
	if preview.RequiresMigration() {
		t.Fatalf("manual inbound must not block the update: %#v", preview)
	}
	if len(preview.ManualOutsidePolicy) != 1 || preview.ManualOutsidePolicy[0].Port != 443 {
		t.Fatalf("manual outside = %#v, want port 443 warning", preview.ManualOutsidePolicy)
	}
}

func TestPreviewServerPortPolicyChangeUnchangedRangeDoesNothing(t *testing.T) {
	current := model.Server{ID: 1, PortRangeStart: 10000, PortRangeEnd: 10100, InternalPortRangeStart: 30000, InternalPortRangeEnd: 59999}
	next := current
	allocations := []model.ProxyPathPortAllocation{{Kind: model.ProxyPathPortKindChainService, ScopeKey: "m", ServerID: 1, Pool: model.PortPoolPublic, Port: 99}}
	preview := PreviewServerPortPolicyChange(current, next, allocations, nil)
	if preview.PublicRangeChanged || preview.InternalRangeChanged || preview.RequiresMigration() {
		t.Fatalf("unchanged range must be a no-op: %#v", preview)
	}
}
