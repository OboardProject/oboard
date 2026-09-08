package core

import (
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAffectedServersFromAccessProjectionsUnion(t *testing.T) {
	before := AccessProjection{
		InboundUsers:   map[int64][]int64{50: {1}},
		ProxyPathUsers: map[int64][]int64{100: {1}},
	}
	after := AccessProjection{
		InboundUsers:   map[int64][]int64{},
		ProxyPathUsers: map[int64][]int64{101: {1}},
	}
	inbounds := []model.Inbound{
		{ID: 50, ServerID: 10, Enabled: true},
		{ID: 60, ServerID: 20, Enabled: true},
	}
	paths := []model.ProxyPath{
		{ID: 100, InboundID: 50, Enabled: true},
		{ID: 101, InboundID: 50, Enabled: true},
	}
	proc := int64(60)
	plain := int64(20)
	steps := []model.ProxyPathStep{
		{PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, InboundID: &proc},
		{PathID: 101, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &plain},
	}
	got := AffectedServersFromAccessProjections(before, after, paths, steps, inbounds, map[int64]bool{10: true, 20: true})
	want := map[int64]bool{10: true, 20: true}
	if len(got) != len(want) {
		t.Fatalf("servers=%v want keys %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("servers=%v want keys %v", got, want)
		}
	}
}

func TestAffectedServersFromTopologyUnionKeepsBothSides(t *testing.T) {
	keys := map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}
	beforeInbounds := []model.Inbound{{ID: 1, ServerID: 10, Enabled: true}}
	afterInbounds := []model.Inbound{{ID: 1, ServerID: 20, Enabled: true}}
	got := AffectedServersFromTopologyUnion(keys, nil, nil, nil, nil, beforeInbounds, afterInbounds, nil)
	if len(got) != 2 || got[0] != 10 || got[1] != 20 {
		t.Fatalf("topology union = %#v, want [10 20]", got)
	}
}

func TestUnionInt64IDs(t *testing.T) {
	got := UnionInt64IDs([]int64{3, 1, 0}, []int64{1, 2}, nil)
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("union = %#v", got)
	}
}
