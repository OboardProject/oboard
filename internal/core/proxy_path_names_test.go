package core

import (
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestResolveProxyPathNamesUsesEndpointsAndDynamicResourceNames(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "香港"}, {ID: 2, Name: "东京"}, {ID: 3, Name: "洛杉矶"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, Enabled: true}}
	paths := []model.ProxyPath{{ID: 100, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true}}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)},
		{ID: 2, PathID: 100, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(3)},
	}
	resolved := ResolveProxyPathNames(paths, steps, servers, inbounds, nil)
	if got := resolved[0].Name; got != "香港｜洛杉矶" {
		t.Fatalf("name = %q", got)
	}
	servers[0].Name = "广州"
	servers[2].Name = "纽约"
	resolved = ResolveProxyPathNames(paths, steps, servers, inbounds, nil)
	if got := resolved[0].Name; got != "广州｜纽约" {
		t.Fatalf("renamed path = %q", got)
	}
}

func TestResolveProxyPathNamesAddsDistinguishingMiddleNodes(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "香港"}, {ID: 2, Name: "东京"}, {ID: 3, Name: "新加坡"}, {ID: 4, Name: "首尔"}, {ID: 5, Name: "洛杉矶"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Protocol: model.ProtocolVLESS, Enabled: true}}
	paths := []model.ProxyPath{
		{ID: 100, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true},
		{ID: 200, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)},
		{ID: 2, PathID: 100, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(3)},
		{ID: 3, PathID: 100, Position: 3, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(5)},
		{ID: 4, PathID: 200, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)},
		{ID: 5, PathID: 200, Position: 2, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(4)},
		{ID: 6, PathID: 200, Position: 3, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(5)},
	}
	resolved := ResolveProxyPathNames(paths, steps, servers, inbounds, nil)
	if resolved[0].Name != "香港｜新加坡｜洛杉矶" || resolved[1].Name != "香港｜首尔｜洛杉矶" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestResolveProxyPathNamesUsesTransportFeaturesThenStableOrdinals(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "香港"}, {ID: 2, Name: "洛杉矶"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Protocol: model.ProtocolVLESS, Enabled: true}}
	paths := []model.ProxyPath{
		{ID: 30, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true},
		{ID: 10, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true},
		{ID: 20, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 30, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2), TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"wireguard"}`},
		{ID: 2, PathID: 10, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2), TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh"}`},
		{ID: 3, PathID: 20, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2), TransportMode: model.ProxyPathTransportTunnel, ConfigJSON: `{"type":"ssh"}`},
	}
	resolved := ResolveProxyPathNames(paths, steps, servers, inbounds, nil)
	byID := map[int64]string{}
	for _, path := range resolved {
		byID[path.ID] = path.Name
	}
	if byID[30] != "香港｜洛杉矶｜VLESS｜WireGuard" {
		t.Fatalf("wireguard name = %q", byID[30])
	}
	if byID[10] != "香港｜洛杉矶｜VLESS｜SSH｜01" || byID[20] != "香港｜洛杉矶｜VLESS｜SSH｜02" {
		t.Fatalf("ssh names = %#v", byID)
	}
}

func TestResolveProxyPathNamesRendersCustomReferencesAndDisambiguates(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "香港"}, {ID: 2, Name: "洛杉矶"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Protocol: model.ProtocolHY2, Enabled: true}}
	template := []model.ProxyPathNamePart{
		{Kind: model.ProxyPathNameLiteral, Value: "专线 "},
		{Kind: model.ProxyPathNameServer, ServerID: 1},
		{Kind: model.ProxyPathNameLiteral, Value: "｜"},
		{Kind: model.ProxyPathNameServer, ServerID: 2},
	}
	paths := []model.ProxyPath{
		{ID: 1, NameMode: model.ProxyPathNameCustom, NameTemplate: template, InboundID: 10, Enabled: true},
		{ID: 2, NameMode: model.ProxyPathNameCustom, NameTemplate: template, InboundID: 10, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 1, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)},
		{ID: 2, PathID: 2, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)},
	}
	resolved := ResolveProxyPathNames(paths, steps, servers, inbounds, nil)
	if !strings.HasPrefix(resolved[0].Name, "专线 香港｜洛杉矶｜HY2｜SS2022-128｜") || !strings.HasSuffix(resolved[1].Name, "02") {
		t.Fatalf("custom names = %#v", resolved)
	}
	servers[1].Name = "纽约"
	resolved = ResolveProxyPathNames(paths[:1], steps[:1], servers, inbounds, nil)
	if resolved[0].Name != "专线 香港｜纽约" {
		t.Fatalf("renamed custom path = %q", resolved[0].Name)
	}
}

func TestNormalizeProxyPathNameRejectsReferencesOutsidePath(t *testing.T) {
	path := model.ProxyPath{
		ID:           1,
		InboundID:    10,
		NameMode:     model.ProxyPathNameCustom,
		NameTemplate: []model.ProxyPathNamePart{{Kind: model.ProxyPathNameServer, ServerID: 3}},
	}
	servers := []model.Server{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}, {ID: 3, Name: "C"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1}}
	steps := []model.ProxyPathStep{{PathID: 1, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)}}
	if err := NormalizeProxyPathName(&path, steps, servers, inbounds, nil); err == nil || !strings.Contains(err.Error(), "not part") {
		t.Fatalf("error = %v", err)
	}
}

func int64Ptr(value int64) *int64 { return &value }
