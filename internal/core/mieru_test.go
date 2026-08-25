package core

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestNormalizeMieruPortConfigCanonicalizesAndCompresses(t *testing.T) {
	primary, configJSON, err := NormalizeMieruPortConfig(9000, `{"listen_ports":["9002-9004","8999-9001"],"transport":"TCP"}`, "listen_ports")
	if err != nil {
		t.Fatal(err)
	}
	if primary != 8999 {
		t.Fatalf("primary port = %d, want 8999", primary)
	}
	ports, err := mieruPortsFromConfig(primary, configJSON, "listen_ports")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{8999, 9000, 9001, 9002, 9003, 9004}; !reflect.DeepEqual(ports, want) {
		t.Fatalf("ports = %v, want %v", ports, want)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		t.Fatal(err)
	}
	ranges, err := mieruPortRangeStrings(config["listen_ports"])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ranges, []string{"9000-9004"}) {
		t.Fatalf("canonical ranges = %v", ranges)
	}
}

func TestNormalizeMieruPortConfigRejectsMoreThan64Ports(t *testing.T) {
	if _, _, err := NormalizeMieruPortConfig(0, `{"server_ports":["1000-1064"]}`, "server_ports"); err == nil {
		t.Fatal("expected Mieru port limit error")
	}
}

func TestMieruAdapterGeneratesBoundedUserAliasesAndRanges(t *testing.T) {
	inbound := model.Inbound{
		ID: 9, Protocol: model.ProtocolMieru, ListenIP: "0.0.0.0", Port: 8964,
		ConfigJSON: `{"transport":"TCP","listen_ports":["8965-8966"],"user_hint_is_mandatory":true}`,
	}
	user := model.User{ID: 7, Username: strings.Repeat("long-user-", 12), Status: "active", ProxyPassword: "secret", SpeedLimitMbps: 20}
	item, err := (mieruAdapter{}).Inbound(inbound, []model.User{user})
	if err != nil {
		t.Fatal(err)
	}
	users := mapList(item["users"])
	if len(users) != 1 || stringFromAny(users[0]["name"]) != "oboard-u7" {
		t.Fatalf("generated Mieru users = %#v", users)
	}
	ports, err := mieruPortsFromValue(intFromAny(item["listen_port"]), item["listen_ports"])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ports, []int{8964, 8965, 8966}) {
		t.Fatalf("generated ports = %v", ports)
	}

	config := &SingBoxConfig{}
	addRuntimeLimitsForInboundTag(config, inbound, []model.User{user}, ConfigOptions{}, "in-9")
	limit, ok := config.OBoard.RateLimits.Users["oboard-u7"]
	if !ok || limit.UserID != user.ID || limit.SpeedLimitMbps != 20 {
		t.Fatalf("Mieru runtime alias missing original policy: %#v", config.OBoard)
	}
}

func TestMieruProxyPathIdentitiesUseRuntimeAliases(t *testing.T) {
	root := model.Inbound{ID: 9, ServerID: 1, Protocol: model.ProtocolMieru, Port: 8964, ConfigJSON: `{"transport":"TCP"}`, Enabled: true}
	path := model.ProxyPath{ID: 12, InboundID: root.ID, Enabled: true}
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "secret"}

	branchNames := proxyPathBranchUsernames(path, root, []model.User{user})
	if !reflect.DeepEqual(branchNames, []string{"oboard-u7-p12"}) {
		t.Fatalf("Mieru branch auth users = %v", branchNames)
	}
	branchUser := proxyPathBranchUser(path, root, user)
	config := &SingBoxConfig{}
	addRuntimeLimitsForInboundTag(config, root, []model.User{branchUser}, ConfigOptions{}, "in-9")
	limit, ok := config.OBoard.RateLimits.Users["oboard-u7-p12"]
	if !ok || limit.UserID != user.ID || limit.PathID != path.ID {
		t.Fatalf("Mieru branch runtime identity = %#v", config.OBoard)
	}

	target := model.Inbound{ID: 22, ServerID: 2, Protocol: model.ProtocolMieru}
	targetID := target.ID
	step := model.ProxyPathStep{InboundID: &targetID}
	_, targetNames := proxyPathStepInboundIdentity(path, step, root, target.ServerID, map[int64]model.Inbound{target.ID: target}, nil, ConfigOptions{}, nil, nil)
	if !reflect.DeepEqual(targetNames, []string{"oboard-ic"}) {
		t.Fatalf("Mieru target auth users = %v", targetNames)
	}
}

func TestMieruSubscriptionFormatsAreExplicit(t *testing.T) {
	nodes := []SubscriptionNode{
		{Name: "Mieru node", Raw: map[string]any{
			"type": "mieru", "tag": "Mieru node", "server": "2001:db8::1", "server_port": 8964,
			"server_ports": []string{"8965-8966"}, "transport": "TCP",
			"username": "oboard-u7", "password": "secret", "multiplexing": "MULTIPLEXING_HIGH",
		}},
		{Name: "VLESS node", Raw: map[string]any{"type": "vless", "tag": "VLESS node", "server": "example.com", "server_port": 443, "uuid": "11111111-1111-4111-8111-111111111111"}},
	}
	official, err := renderSingBoxSubscription(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(official, `"type": "mieru"`) {
		t.Fatalf("official sing-box subscription contains Mieru: %s", official)
	}
	extended, err := renderSingBoxMieruSubscription(nodes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(extended, `"type": "mieru"`) {
		t.Fatalf("extended sing-box subscription omitted Mieru: %s", extended)
	}
	links, err := renderMieruSubscription(nodes)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(strings.TrimSpace(links))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "mierus" || parsed.Hostname() != "2001:db8::1" || parsed.User.Username() != "oboard-u7" {
		t.Fatalf("unexpected Mieru share URL: %s", links)
	}
	if got := parsed.Query()["port"]; !reflect.DeepEqual(got, []string{"8964", "8965-8966"}) {
		t.Fatalf("share URL ports = %v", got)
	}
	if got := parsed.Query()["protocol"]; !reflect.DeepEqual(got, []string{"TCP", "TCP"}) {
		t.Fatalf("share URL protocols = %v", got)
	}
}

func TestMieruSubscriptionTargetMatrix(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "secret"}
	server := model.Server{ID: 1, Name: "IPv6 Mieru", PublicIPv6: "2001:db8::1"}
	inbound := model.Inbound{
		ID: 1, ServerID: server.ID, Name: "Mieru", Protocol: model.ProtocolMieru, Port: 8964, Enabled: true,
		ConfigJSON: `{"transport":"TCP","listen_ports":["8965-8966"],"multiplexing":"MULTIPLEXING_HIGH","traffic_pattern":"AA=="}`,
	}

	for _, test := range []struct {
		format    model.SubscriptionFormat
		wantMieru bool
	}{
		{format: model.SubscriptionFormatSingBox},
		{format: model.SubscriptionFormatSingBoxMieru, wantMieru: true},
		{format: model.SubscriptionFormatMieru, wantMieru: true},
		{format: model.SubscriptionFormatClashMeta, wantMieru: true},
		{format: model.SubscriptionFormatMihomo, wantMieru: true},
		{format: model.SubscriptionFormatShadowrocket, wantMieru: true},
		{format: model.SubscriptionFormatClash},
		{format: model.SubscriptionFormatStash},
		{format: model.SubscriptionFormatV2RayURI},
	} {
		t.Run(string(test.format), func(t *testing.T) {
			subscription, err := GenerateSubscriptionWithOptions(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{Format: test.format, EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(subscription, "mieru"); got != test.wantMieru {
				t.Fatalf("Mieru presence = %v, want %v:\n%s", got, test.wantMieru, subscription)
			}
		})
	}

	if got := SubscriptionContentType(model.SubscriptionFormatShadowrocket); got != "text/plain; charset=utf-8" {
		t.Fatalf("Shadowrocket content type = %q", got)
	}
	empty, err := renderSubscriptionTarget(nil, model.SubscriptionFormatShadowrocket)
	if err != nil {
		t.Fatal(err)
	}
	if empty != "" {
		t.Fatalf("empty Shadowrocket subscription = %q", empty)
	}
}

func TestMieruExtendedSubscriptionPreservesDisjointRanges(t *testing.T) {
	user := model.User{ID: 7, Username: "alice", Status: "active", ProxyPassword: "secret"}
	server := model.Server{ID: 1, Name: "Mieru", PublicIPv4: "203.0.113.1"}
	inbound := model.Inbound{
		ID: 1, ServerID: server.ID, Name: "Mieru", Protocol: model.ProtocolMieru, Port: 8964, Enabled: true,
		ConfigJSON: `{"transport":"UDP","listen_ports":["9000-9001"]}`,
	}
	subscription, err := GenerateSubscriptionWithOptions(user, []model.Server{server}, []model.Inbound{inbound}, SubscriptionOptions{Format: model.SubscriptionFormatSingBoxMieru, EffectiveNodes: map[string]bool{NodeKeyOf(model.AssignableNodeInbound, 1): true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(subscription, `"server_port": 8964`) || !strings.Contains(subscription, `"9000-9001"`) {
		t.Fatalf("extended subscription lost Mieru ports:\n%s", subscription)
	}
	if !strings.Contains(subscription, `"transport": "UDP"`) {
		t.Fatalf("Mieru transport missing:\n%s", subscription)
	}
}

func TestMieruExtraListenPortsParticipateInConflictValidation(t *testing.T) {
	inbounds := []map[string]any{
		{"type": "mieru", "tag": "mieru", "listen": "0.0.0.0", "listen_port": 8964, "listen_ports": []string{"8965-8966"}, "transport": "TCP"},
		{"type": "vless", "tag": "vless", "listen": "0.0.0.0", "listen_port": 8966},
	}
	if err := validateListenResources(singBoxListenResources(1, inbounds)); err == nil {
		t.Fatal("expected conflict on Mieru secondary port")
	}
}

func TestMultiportMieruRejectsTrustedForwardPath(t *testing.T) {
	root := model.Inbound{ID: 1, ServerID: 1, Protocol: model.ProtocolMieru, Port: 8964, ConfigJSON: `{"transport":"TCP","listen_ports":["8965-8966"]}`, Enabled: true}
	path := model.ProxyPath{ID: 2, Name: "transparent", InboundID: root.ID, Enabled: true}
	steps := map[int64][]model.ProxyPathStep{
		path.ID: {{ID: 3, PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true}},
	}
	if err := validateProxyPathTransportSet([]model.ProxyPath{path}, steps, map[int64]model.Inbound{root.ID: root}); err == nil {
		t.Fatal("expected trusted-forward multiport rejection")
	}
}
