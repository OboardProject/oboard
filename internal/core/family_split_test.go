package core

import (
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestCollapseFamilyBranchStepsSkipsMatchingFirstHop(t *testing.T) {
	graft := int64(7)
	serverID := graft
	steps := []model.ProxyPathStep{
		{ID: 1, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: &serverID},
		{ID: 2, Position: 2, NodeType: model.ProxyPathStepWARP},
	}
	got := CollapseFamilyBranchSteps(graft, steps, map[int64]model.Inbound{})
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("collapse = %#v, want remaining WARP hop", got)
	}
	other := int64(9)
	steps[0].ServerID = &other
	got = CollapseFamilyBranchSteps(graft, steps, map[int64]model.Inbound{})
	if len(got) != 2 {
		t.Fatalf("different first hop should not collapse: %#v", got)
	}
}

func TestValidateFamilyBranchTransportRejectsPortForwardAndFirstHopTunnel(t *testing.T) {
	if err := ValidateFamilyBranchTransport([]model.ProxyPathStep{{Position: 1, TransportMode: model.ProxyPathTransportPortForward}}); err == nil || !strings.Contains(err.Error(), "透明端口转发") {
		t.Fatalf("port_forward accepted: %v", err)
	}
	if err := ValidateFamilyBranchTransport([]model.ProxyPathStep{{Position: 1, TransportMode: model.ProxyPathTransportTunnel}}); err == nil || !strings.Contains(err.Error(), "第一跳") {
		t.Fatalf("first-hop tunnel accepted: %v", err)
	}
	if err := ValidateFamilyBranchTransport([]model.ProxyPathStep{
		{Position: 1, TransportMode: model.ProxyPathTransportSingBox},
		{Position: 2, TransportMode: model.ProxyPathTransportTunnel},
	}); err != nil {
		t.Fatalf("later tunnel rejected: %v", err)
	}
}

func TestParseFamilyBranchExitBindingMutualExclusion(t *testing.T) {
	if _, err := ParseFamilyBranchExitBinding(`{"interface_name":"eth0","source_prefix":"203.0.113.8/32"}`); err == nil {
		t.Fatal("mutual bind was accepted")
	}
	got, err := ParseFamilyBranchExitBinding(`{"interface_name":"eth0"}`)
	if err != nil || got.InterfaceName != "eth0" {
		t.Fatalf("interface bind = %#v err=%v", got, err)
	}
}

func TestFamilySplitTemplatePathsRequireBothBranches(t *testing.T) {
	templateID := int64(3)
	_, _, err := FamilySplitTemplatePaths([]model.ProxyPath{{ID: 1, Kind: model.ProxyPathKindFamilyBranch, TemplateID: &templateID, Family: model.FamilySplitFamilyIPv4}}, templateID)
	if err == nil {
		t.Fatal("missing IPv6 branch was accepted")
	}
}
