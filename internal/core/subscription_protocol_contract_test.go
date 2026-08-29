package core

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"go.yaml.in/yaml/v3"
)

func specialProtocolContractNodes() []SubscriptionNode {
	return []SubscriptionNode{
		sshSubscriptionFixtureNode(),
		{
			Name: "Snell v4", Group: "备用", Raw: map[string]any{
				"type": "snell", "server": "snell.example.com", "server_port": 6160,
				"version": 4, "psk": "snell-v4-psk", "userkey": "snell-v4-userkey", "obfs_mode": "http", "obfs_host": "bing.com",
				"reuse": true, "tcp_fast_open": true,
			},
		},
		{
			Name: "Snell v6", Group: "备用", Raw: map[string]any{
				"type": "snell", "server": "snell6.example.com", "server_port": 7177,
				"version": 6, "psk": "snell-v6-psk", "userkey": "snell-v6-userkey", "mode": "unshaped", "reuse": true,
				"tcp_fast_open": true,
			},
		},
		{
			Name: "Mieru Multiport", Group: "Mieru", Raw: map[string]any{
				"type": "mieru", "server": "mieru.example.com", "server_port": 25250,
				"server_ports": []string{"25251-25252"}, "transport": "TCP",
				"username": "oboard-u7", "password": "mieru-pass",
				"multiplexing": "MULTIPLEXING_HIGH", "traffic_pattern": "AA==",
				"tcp_fast_open": true,
			},
		},
	}
}

func TestProtocolFragmentGoldenSSHSnellMieru(t *testing.T) {
	ssh, err := normalizeSubscriptionNode(sshSubscriptionFixtureNode())
	if err != nil {
		t.Fatal(err)
	}
	snellV4, err := normalizeSubscriptionNode(specialProtocolContractNodes()[1])
	if err != nil {
		t.Fatal(err)
	}
	snellV6, err := normalizeSubscriptionNode(specialProtocolContractNodes()[2])
	if err != nil {
		t.Fatal(err)
	}
	mieru, err := normalizeSubscriptionNode(specialProtocolContractNodes()[3])
	if err != nil {
		t.Fatal(err)
	}

	mihomoSSH, err := encodeProtocolFragment(ssh, model.SubscriptionFormatMihomo)
	if err != nil {
		t.Fatal(err)
	}
	var sshMap map[string]any
	if err := yaml.Unmarshal([]byte(mihomoSSH), &sshMap); err != nil {
		t.Fatal(err)
	}
	if sshMap["type"] != "ssh" || sshMap["username"] != "oboard-7" || sshMap["password"] != sshSubscriptionPassword {
		t.Fatalf("mihomo SSH fragment = %#v", sshMap)
	}
	if _, ok := sshMap["host-key"]; !ok {
		t.Fatalf("mihomo SSH lost host-key: %#v", sshMap)
	}

	stashSSH, err := encodeProtocolFragment(ssh, model.SubscriptionFormatStash)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stashSSH, "user:") || !strings.Contains(stashSSH, "host-key:") {
		t.Fatalf("stash SSH fragment = %s", stashSSH)
	}

	egernSSH, err := encodeProtocolFragment(ssh, model.SubscriptionFormatEgern)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(egernSSH, "username:") || !strings.Contains(egernSSH, "host_keys:") {
		t.Fatalf("egern SSH fragment = %s", egernSSH)
	}

	surgeSSH, err := encodeProtocolFragment(ssh, model.SubscriptionFormatSurge)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(surgeSSH, "username=") || !strings.Contains(surgeSSH, "server-fingerprint=") {
		t.Fatalf("surge SSH fragment = %s", surgeSSH)
	}

	singboxSSH, err := encodeProtocolFragment(ssh, model.SubscriptionFormatSingBox)
	if err != nil {
		t.Fatal(err)
	}
	var native map[string]any
	if err := json.Unmarshal([]byte(singboxSSH), &native); err != nil {
		t.Fatal(err)
	}
	if native["type"] != "ssh" || native["user"] != "oboard-7" || native["password"] != sshSubscriptionPassword {
		t.Fatalf("sing-box SSH fragment = %#v", native)
	}

	uriSSH, err := encodeProtocolFragment(ssh, model.SubscriptionFormatShadowrocket)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(uriSSH, "ssh://oboard-7:") || !strings.Contains(uriSSH, "@ssh.example.com:2222") {
		t.Fatalf("shadowrocket SSH fragment = %s", uriSSH)
	}

	mihomoSnell, err := encodeProtocolFragment(snellV4, model.SubscriptionFormatMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mihomoSnell, "type: snell") || !strings.Contains(mihomoSnell, "version: 4") || !strings.Contains(mihomoSnell, "psk: snell-v4-psk") || !strings.Contains(mihomoSnell, "obfs-opts:") {
		t.Fatalf("mihomo Snell v4 fragment = %s", mihomoSnell)
	}

	surgeV4, err := encodeProtocolFragment(snellV4, model.SubscriptionFormatSurge)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(surgeV4, "=snell,") || !strings.Contains(surgeV4, "version=4") || !strings.Contains(surgeV4, "obfs=http") || !strings.Contains(surgeV4, `obfs-host="bing.com"`) {
		t.Fatalf("surge Snell v4 fragment = %s", surgeV4)
	}

	surgeV6, err := encodeProtocolFragment(snellV6, model.SubscriptionFormatSurge)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(surgeV6, "version=6") || !strings.Contains(surgeV6, "mode=unshaped") || strings.Contains(surgeV6, "obfs=") {
		t.Fatalf("surge Snell v6 fragment = %s", surgeV6)
	}

	rocket, err := encodeProtocolFragment(snellV4, model.SubscriptionFormatShadowrocket)
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.SplitN(strings.TrimPrefix(rocket, "snell://"), "?", 2)[0]
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(encoded)
	}
	if err != nil {
		t.Fatalf("shadowrocket Snell payload: %v %s", err, rocket)
	}
	if string(decoded) != "chacha20-ietf-poly1305:snell-v4-psk@snell.example.com:6160" {
		t.Fatalf("shadowrocket Snell payload = %q from %s", decoded, rocket)
	}
	parsed, err := url.Parse(rocket)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("version") != "4" || query.Get("obfs") == "" || query.Get("obfsParam") == "" {
		t.Fatalf("shadowrocket Snell query = %v", query)
	}

	mihomoMieru, err := encodeProtocolFragment(mieru, model.SubscriptionFormatMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mihomoMieru, "type: mieru") || !strings.Contains(mihomoMieru, "port-range:") || !strings.Contains(mihomoMieru, "multiplexing:") {
		t.Fatalf("mihomo Mieru fragment = %s", mihomoMieru)
	}

	rocketMieru, err := encodeProtocolFragment(mieru, model.SubscriptionFormatShadowrocket)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rocketMieru, "mierus://") || !strings.Contains(rocketMieru, "user-hint-is-mandatory=true") {
		t.Fatalf("shadowrocket Mieru fragment = %s", rocketMieru)
	}
}

func TestSpecialProtocolCapabilityMatrixIsFrozen(t *testing.T) {
	nodes := specialProtocolContractNodes()
	tests := []struct {
		format model.SubscriptionFormat
		want   []string
		deny   []string
	}{
		{format: model.SubscriptionFormatMihomo, want: []string{"SSH Managed", "Snell v4", "Mieru Multiport"}, deny: []string{"Snell v6"}},
		{format: model.SubscriptionFormatSurge, want: []string{"SSH Managed", "Snell v4", "Snell v6"}, deny: []string{"Mieru Multiport"}},
		{format: model.SubscriptionFormatShadowrocket, want: []string{"SSH Managed", "Snell v4", "Snell v6", "Mieru Multiport"}},
		{format: model.SubscriptionFormatSingBox, want: []string{"SSH Managed", "Snell v4", "Snell v6"}, deny: []string{"Mieru Multiport"}},
	}
	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			preview, err := PreviewSubscriptionNodes(nodes, test.format)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, node := range preview.Nodes {
				got[node.Name] = true
			}
			for _, name := range test.want {
				if !got[name] {
					t.Fatalf("missing %s in %s: %#v", name, test.format, got)
				}
			}
			for _, name := range test.deny {
				if got[name] {
					t.Fatalf("unexpected %s in %s", name, test.format)
				}
			}
		})
	}
}

func TestSurgeMacAutoKeepsNativeAndMihomoBridge(t *testing.T) {
	preview, err := PreviewSubscriptionNodesWithOptions(specialProtocolContractNodes(), model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoAuto, Merge: true, LocalPort: 53000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Nodes) != 4 {
		t.Fatalf("surge-mac auto nodes=%d filtered=%d", len(preview.Nodes), preview.FilteredCount)
	}
	if !strings.Contains(preview.Content, "SSH Managed=") || !strings.Contains(preview.Content, "Snell v4=snell,") || !strings.Contains(preview.Content, "Snell v6=snell,") {
		t.Fatalf("native surge-mac lines missing:\n%s", preview.Content)
	}
	if !strings.Contains(preview.Content, "Mieru Multiport=socks5,127.0.0.1,") {
		t.Fatalf("mieru should ride Mihomo bridge:\n%s", preview.Content)
	}
}

func TestBuiltinTemplatesAreCompleteDocuments(t *testing.T) {
	for _, format := range ConcreteSubscriptionFormats() {
		t.Run(string(format), func(t *testing.T) {
			template, err := BuiltinSubscriptionTemplate(format)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateSubscriptionTemplate(format, template); err != nil {
				t.Fatal(err)
			}
			if err := ValidateSubscriptionTemplateWithPreview(format, template); err != nil {
				t.Fatal(err)
			}
		})
	}
}
