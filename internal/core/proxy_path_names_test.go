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

func TestResolveProxyPathNamesOmitsUnneededDirectSuffix(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, Enabled: true}}
	paths := []model.ProxyPath{{ID: 100, Kind: model.ProxyPathKindDirect, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true}}
	steps := []model.ProxyPathStep{{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)}}
	resolved := ResolveProxyPathNames(paths, steps, servers, inbounds, nil)
	if got := resolved[0].Name; got != "A｜B" {
		t.Fatalf("direct branch name = %q", got)
	}
}

func TestResolveProxyPathNamesUsesDirectSuffixOnlyForConflict(t *testing.T) {
	servers := []model.Server{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}}
	inbounds := []model.Inbound{{ID: 10, ServerID: 1, Name: "entry", Protocol: model.ProtocolVLESS, Enabled: true}}
	paths := []model.ProxyPath{
		{ID: 100, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true},
		{ID: 200, Kind: model.ProxyPathKindDirect, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: true},
	}
	steps := []model.ProxyPathStep{
		{ID: 1, PathID: 100, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)},
		{ID: 2, PathID: 200, Position: 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(2)},
	}
	resolved := ResolveProxyPathNames(paths, steps, servers, inbounds, nil)
	if resolved[0].Name != "A｜B" || resolved[1].Name != "A｜B｜直出" {
		t.Fatalf("resolved = %#v", resolved)
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

func TestResolveProxyPathNamesHidesSharedMiddleNodes(t *testing.T) {
	for _, test := range []struct {
		name     string
		routes   [][]int64
		disabled bool
		want     []string
	}{
		{"different exits", [][]int64{{2, 3, 5}, {2, 4, 6}}, false, []string{"A｜E", "A｜F"}},
		{"shared suffix", [][]int64{{2, 3, 5}, {4, 3, 5}}, false, []string{"A｜B｜E", "A｜D｜E"}},
		{"different lengths", [][]int64{{2, 3, 4, 5}, {2, 4, 5}}, false, []string{"A｜C｜E", "A｜E"}},
		{"shared interior", [][]int64{{2, 3, 4, 5}, {6, 3, 7, 5}}, false, []string{"A｜B｜D｜E", "A｜F｜G｜E"}},
		{"disabled alternative", [][]int64{{2, 3, 5}, {2, 4, 5}}, true, []string{"A｜E", "A｜E"}},
		{"three paths", [][]int64{{2, 3, 5}, {2, 4, 5}, {2, 6, 5}}, false, []string{"A｜C｜E", "A｜D｜E", "A｜F｜E"}},
		{"identical middles", [][]int64{{2, 3, 5}, {2, 3, 5}}, false, []string{"A｜E｜VLESS｜SS2022-128｜01", "A｜E｜VLESS｜SS2022-128｜02"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			servers := []model.Server{{ID: 1, Name: "A"}, {ID: 2, Name: "B"}, {ID: 3, Name: "C"}, {ID: 4, Name: "D"}, {ID: 5, Name: "E"}, {ID: 6, Name: "F"}, {ID: 7, Name: "G"}}
			inbounds := []model.Inbound{{ID: 10, ServerID: 1, Protocol: model.ProtocolVLESS, Enabled: true}}
			var paths []model.ProxyPath
			var steps []model.ProxyPathStep
			for i, route := range test.routes {
				id := int64(i + 1)
				paths = append(paths, model.ProxyPath{ID: id, NameMode: model.ProxyPathNameAuto, InboundID: 10, Enabled: !test.disabled || i == 0})
				for j, serverID := range route {
					steps = append(steps, model.ProxyPathStep{PathID: id, Position: j + 1, NodeType: model.ProxyPathStepServerInbound, ServerID: int64Ptr(serverID)})
				}
			}
			for pass := 0; pass < 2; pass++ {
				for _, path := range ResolveProxyPathNames(paths, steps, servers, inbounds, nil) {
					if got, want := path.Name, test.want[path.ID-1]; got != want {
						t.Fatalf("path %d name = %q, want %q", path.ID, got, want)
					}
				}
				paths[0], paths[len(paths)-1] = paths[len(paths)-1], paths[0]
			}
		})
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
