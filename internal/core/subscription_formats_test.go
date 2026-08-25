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

func subscriptionFormatFixtureNodes() []SubscriptionNode {
	return []SubscriptionNode{
		{
			Name: "VLESS IPv6", Group: "自动选择", Raw: map[string]any{
				"type": "vless", "tag": "VLESS IPv6", "server": "2001:db8::10", "server_port": 443,
				"uuid": "11111111-1111-4111-8111-111111111111", "flow": "xtls-rprx-vision", "packet_encoding": "xudp",
				"tls": map[string]any{
					"enabled": true, "server_name": "edge.example.com", "insecure": true,
					"utls":    map[string]any{"enabled": true, "fingerprint": "chrome"},
					"reality": map[string]any{"enabled": true, "public_key": "reality-public", "short_id": "abcd"},
				},
				"transport":    map[string]any{"type": "tcp"},
				"oboard_group": "must-not-leak",
			},
		},
		{
			Name: "HY2", Group: "自动选择", Raw: map[string]any{
				"type": "hysteria2", "tag": "HY2", "server": "hy2.example.com", "server_port": 8443, "password": "hy2-pass",
				"server_ports": []string{"8444-8446"}, "hop_interval": "20s", "up_mbps": 100, "down_mbps": 200,
				"obfs": map[string]any{"type": "salamander", "password": "obfs-pass"},
				"tls":  map[string]any{"enabled": true, "server_name": "hy2.example.com", "alpn": []any{"h3"}},
			},
		},
		{
			Name: "AnyTLS", Group: "自动选择", Raw: map[string]any{
				"type": "anytls", "tag": "AnyTLS", "server": "anytls.example.com", "server_port": 443, "password": "anytls-pass",
				"tls": map[string]any{"enabled": true, "server_name": "anytls.example.com"}, "padding_scheme": []any{"stop=8", "0=16-32"},
			},
		},
		{
			Name: "SS UoT", Group: "备用", Raw: map[string]any{
				"type": "shadowsocks", "tag": "SS UoT", "server": "ss.example.com", "server_port": 8388,
				"method": "chacha20-ietf-poly1305", "password": "ss-pass", "udp_over_tcp": map[string]any{"enabled": true, "version": 1},
			},
		},
		{
			Name: "SOCKS", Group: "备用", Raw: map[string]any{
				"type": "socks", "tag": "SOCKS", "server": "socks.example.com", "server_port": 1080,
				"username": "alice", "password": "socks-pass",
			},
		},
		{
			Name: "Mieru", Group: "Mieru", Raw: map[string]any{
				"type": "mieru", "tag": "Mieru", "server": "2001:db8::20", "server_port": 25250,
				"server_ports": []string{"25251-25252"}, "transport": "TCP", "username": "oboard-u7", "password": "mieru-pass",
				"multiplexing": "MULTIPLEXING_HIGH", "traffic_pattern": "AA==",
			},
		},
		{
			Name: "Snell v4", Group: "备用", Raw: map[string]any{
				"type": "snell", "tag": "Snell v4", "server": "snell.example.com", "server_port": 6160,
				"version": 4, "psk": "snell-v4-psk", "obfs_mode": "http", "obfs_host": "bing.com",
			},
		},
		{
			Name: "Snell v6", Group: "备用", Raw: map[string]any{
				"type": "snell", "tag": "Snell v6", "server": "snell6.example.com", "server_port": 7177,
				"version": 6, "psk": "snell-v6-psk", "mode": "unshaped",
			},
		},
	}
}

const (
	sshSubscriptionPassword = "test-password"
	sshSubscriptionHostKey  = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestAgentHostKey"
)

func sshSubscriptionFixtureNode() SubscriptionNode {
	return SubscriptionNode{Name: "SSH Managed", Group: "SSH", Raw: map[string]any{
		"type": "ssh", "server": "ssh.example.com", "server_port": 2222,
		"username": "oboard-7", "password": sshSubscriptionPassword,
		"host_key": []string{sshSubscriptionHostKey},
	}}
}

// TestTCPFastOpenSubscriptionMapping pins the per-client mapping of the listen
// side tcp_fast_open option: sing-box keeps the raw dial field, the mihomo
// engine uses `tfo`, Surge uses `tfo=true`, formats without an equivalent
// parameter drop it, and QUIC-only proxies never carry it at all.
func TestTCPFastOpenSubscriptionMapping(t *testing.T) {
	nodes := []SubscriptionNode{
		{Name: "VLESS TFO", Group: "自动选择", Raw: map[string]any{
			"type": "vless", "tag": "VLESS TFO", "server": "edge.example.com", "server_port": 443,
			"uuid": "11111111-1111-4111-8111-111111111111", "tcp_fast_open": true,
			"tls": map[string]any{"enabled": true, "server_name": "edge.example.com"},
		}},
		{Name: "SS TFO", Group: "备用", Raw: map[string]any{
			"type": "shadowsocks", "tag": "SS TFO", "server": "ss.example.com", "server_port": 8388,
			"method": "chacha20-ietf-poly1305", "password": "ss-pass", "tcp_fast_open": true,
		}},
	}
	for _, test := range []struct {
		format model.SubscriptionFormat
		want   string
	}{
		{format: model.SubscriptionFormatSingBox, want: `"tcp_fast_open": true`},
		{format: model.SubscriptionFormatMihomo, want: "tfo: true"},
		{format: model.SubscriptionFormatSurge, want: "tfo=true"},
		{format: model.SubscriptionFormatClash},
		{format: model.SubscriptionFormatSurfboard},
	} {
		t.Run(string(test.format), func(t *testing.T) {
			output, err := renderSubscriptionTarget(nodes, test.format)
			if err != nil {
				t.Fatal(err)
			}
			if test.want == "" {
				if strings.Contains(output, "tfo") {
					t.Fatalf("output contains tfo:\n%s", output)
				}
				return
			}
			if !strings.Contains(output, test.want) {
				t.Fatalf("output missing %q:\n%s", test.want, output)
			}
		})
	}
	hy2 := []SubscriptionNode{{Name: "HY2", Group: "自动选择", Raw: map[string]any{
		"type": "hysteria2", "tag": "HY2", "server": "hy2.example.com", "server_port": 8443, "password": "hy2-pass",
		"tcp_fast_open": true, "tls": map[string]any{"enabled": true, "server_name": "hy2.example.com"},
	}}}
	for _, format := range []model.SubscriptionFormat{model.SubscriptionFormatSingBox, model.SubscriptionFormatMihomo, model.SubscriptionFormatSurge} {
		output, err := renderSubscriptionTarget(hy2, format)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output, "tfo") || strings.Contains(output, "tcp_fast_open") {
			t.Fatalf("%s advertised TCP Fast Open for a QUIC proxy:\n%s", format, output)
		}
	}
}

func TestSubscriptionTargetCapabilityMatrix(t *testing.T) {
	nodes := subscriptionFormatFixtureNodes()
	tests := []struct {
		format     model.SubscriptionFormat
		proxyCount int
		contains   []string
		excludes   []string
	}{
		{format: model.SubscriptionFormatSingBox, proxyCount: 7, contains: []string{`"udp_over_tcp": {`, `"version": 2`, `"type": "snell"`, `"version": 4`, `"obfs_mode": "http"`}, excludes: []string{`"type": "mieru"`, `"version": 1`, `"padding_scheme"`, "oboard_group", "must-not-leak"}},
		{format: model.SubscriptionFormatSingBoxMieru, proxyCount: 8, contains: []string{`"type": "mieru"`, `"server_port": 25250`, `"type": "snell"`}, excludes: []string{"padding_scheme", "oboard_group", "must-not-leak"}},
		{format: model.SubscriptionFormatMieru, proxyCount: 1, contains: []string{"mierus://", "25251-25252", "protocol=TCP"}, excludes: []string{"vless://", "snell"}},
		{format: model.SubscriptionFormatClashMeta, proxyCount: 7, contains: []string{"reality-opts:", "udp-over-tcp: true", "udp-over-tcp-version: 2", "type: mieru", "port-range: 25250-25252", "traffic-pattern: AA==", "type: snell", "psk: snell-v4-psk", "obfs-opts:", "host: bing.com"}, excludes: []string{"udp-over-tcp-version: 1", "snell-v6-psk"}},
		{format: model.SubscriptionFormatMihomo, proxyCount: 7, contains: []string{"reality-opts:", "obfs-password: obfs-pass", "type: mieru", "port-range: 25250-25252", "type: snell", "psk: snell-v4-psk"}, excludes: []string{"snell-v6-psk"}},
		{format: model.SubscriptionFormatStash, proxyCount: 5, contains: []string{"auth: hy2-pass", "up-speed: 100", "down-speed: 200"}, excludes: []string{"type: mieru", "type: snell"}},
		{format: model.SubscriptionFormatShadowrocket, proxyCount: 8, contains: []string{"proxies:", "type: vless", "type: mieru", "user-hint-is-mandatory: true", "type: snell", "psk: snell-v4-psk", "version: 4", "psk: snell-v6-psk", "version: 6"}, excludes: []string{"proxy-groups:", "rules:"}},
		{format: model.SubscriptionFormatEgern, proxyCount: 6, contains: []string{"shadowsocks:", "method: chacha20-poly1305", "bandwidth: 100", "user_id:", "snell:", "psk: snell-v4-psk"}, excludes: []string{"mieru:", "snell-v6-psk"}},
		{format: model.SubscriptionFormatLoon, proxyCount: 5, contains: []string{"=vless,", "=Hysteria2,", "udp-over-tcp=true"}, excludes: []string{"mieru", "snell"}},
		{format: model.SubscriptionFormatQX, proxyCount: 4, contains: []string{"vless=", "anytls=", "udp-over-tcp=sp.v2"}, excludes: []string{"udp-over-tcp=sp.v1", "hysteria2=", "mieru", "snell"}},
		{format: model.SubscriptionFormatSurge, proxyCount: 6, contains: []string{"=hysteria2,", "=anytls,", "download-bandwidth=200", "udp-relay=true", "=snell,", "psk=\"snell-v4-psk\"", "version=4", "obfs=http", "obfs-host=\"bing.com\"", "version=6", "mode=unshaped"}, excludes: []string{"=vless,", "mieru"}},
		{format: model.SubscriptionFormatSurgeMac, proxyCount: 9, contains: []string{"=hysteria2,", "=snell,", "snell-v6", "=socks5,127.0.0.1,", "=external,", `exec="/usr/local/bin/mihomo"`}, excludes: []string{"=vless,"}},
		{format: model.SubscriptionFormatSurfboard, proxyCount: 5, contains: []string{"=hysteria2,", "download-bandwidth=200", `SOCKS=socks5,socks.example.com,1080,"alice","socks-pass"`, "=snell,", "snell-v4"}, excludes: []string{"=vless,", "mieru", "snell-v6", "mode=unshaped"}},
		{format: model.SubscriptionFormatClash, proxyCount: 2, contains: []string{"type: ss", "type: socks5"}, excludes: []string{"type: vless", "type: hysteria2", "type: anytls", "type: mieru", "type: snell"}},
		{format: model.SubscriptionFormatV2RayURI, proxyCount: 5, contains: []string{"vless://", "hysteria2://", "anytls://", "ss://", "socks://"}, excludes: []string{"mierus://", "snell"}},
	}
	for _, test := range tests {
		t.Run(string(test.format), func(t *testing.T) {
			output, err := renderSubscriptionTarget(nodes, test.format)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range test.contains {
				if !strings.Contains(output, want) {
					t.Fatalf("output missing %q:\n%s", want, output)
				}
			}
			for _, unwanted := range test.excludes {
				if strings.Contains(output, unwanted) {
					t.Fatalf("output contains %q:\n%s", unwanted, output)
				}
			}
			if got := countRenderedSubscriptionProxies(t, test.format, output); got != test.proxyCount {
				t.Fatalf("proxy count = %d, want %d:\n%s", got, test.proxyCount, output)
			}
		})
	}
}

func TestSSHSubscriptionTargetMappings(t *testing.T) {
	node := sshSubscriptionFixtureNode()

	singBox, err := renderSubscriptionTarget([]SubscriptionNode{node}, model.SubscriptionFormatSingBox)
	if err != nil {
		t.Fatal(err)
	}
	var singBoxConfig SingBoxConfig
	if err := json.Unmarshal([]byte(singBox), &singBoxConfig); err != nil {
		t.Fatal(err)
	}
	if len(singBoxConfig.Outbounds) != 2 {
		t.Fatalf("sing-box outbounds = %#v", singBoxConfig.Outbounds)
	}
	sshOutbound := singBoxConfig.Outbounds[1]
	if sshOutbound["type"] != "ssh" || sshOutbound["user"] != "oboard-7" || sshOutbound["password"] != sshSubscriptionPassword || !stringSetContains(stringListFromAny(sshOutbound["host_key"]), sshSubscriptionHostKey) {
		t.Fatalf("sing-box SSH mapping = %#v", sshOutbound)
	}

	yamlFormats := []model.SubscriptionFormat{
		model.SubscriptionFormatClashMeta,
		model.SubscriptionFormatMihomo,
		model.SubscriptionFormatStash,
		model.SubscriptionFormatEgern,
		model.SubscriptionFormatShadowrocket,
	}
	for _, format := range yamlFormats {
		t.Run(string(format), func(t *testing.T) {
			output, err := renderSubscriptionTarget([]SubscriptionNode{node}, format)
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				Proxies []map[string]any `yaml:"proxies"`
			}
			if err := yaml.Unmarshal([]byte(output), &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Proxies) != 1 {
				t.Fatalf("%s SSH proxies = %#v", format, document.Proxies)
			}
			proxy := document.Proxies[0]
			usernameKey, hostKeyKey := "username", "host-key"
			if format == model.SubscriptionFormatStash {
				usernameKey = "user"
			}
			if format == model.SubscriptionFormatEgern {
				var ok bool
				if len(proxy) != 1 {
					t.Fatalf("Egern SSH wrapper = %#v", proxy)
				}
				proxy, ok = proxy["ssh"].(map[string]any)
				if !ok {
					t.Fatalf("Egern SSH wrapper = %#v", document.Proxies[0])
				}
				if _, exists := proxy["type"]; exists {
					t.Fatalf("Egern SSH payload contains Clash type field: %#v", proxy)
				}
				hostKeyKey = "host_keys"
			}
			if (format != model.SubscriptionFormatEgern && proxy["type"] != "ssh") || proxy["server"] != "ssh.example.com" || intFromAny(proxy["port"]) != 2222 || proxy[usernameKey] != "oboard-7" || proxy["password"] != sshSubscriptionPassword || !stringSetContains(stringListFromAny(proxy[hostKeyKey]), sshSubscriptionHostKey) {
				t.Fatalf("%s SSH mapping = %#v", format, proxy)
			}
		})
	}

	for _, format := range []model.SubscriptionFormat{model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac} {
		t.Run(string(format), func(t *testing.T) {
			output, err := renderSubscriptionTarget([]SubscriptionNode{node}, format)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				"SSH Managed=ssh,ssh.example.com,2222",
				`username="oboard-7"`,
				`password="` + sshSubscriptionPassword + `"`,
				`server-fingerprint="` + sshSubscriptionHostKey + `"`,
			} {
				if !strings.Contains(output, want) {
					t.Fatalf("%s output missing %q:\n%s", format, want, output)
				}
			}
		})
	}

	output, err := renderSubscriptionTarget([]SubscriptionNode{node}, model.SubscriptionFormatV2RayURI)
	if err != nil {
		t.Fatal(err)
	}
	if output != "ssh://oboard-7:test-password@ssh.example.com:2222#SSH%20Managed\n" {
		t.Fatalf("V2Ray URI SSH output = %q", output)
	}
}

func TestSSHSubscriptionIsOmittedFromUnsupportedTargets(t *testing.T) {
	node := sshSubscriptionFixtureNode()
	formats := []model.SubscriptionFormat{
		model.SubscriptionFormatSingBoxMieru,
		model.SubscriptionFormatMieru,
		model.SubscriptionFormatClash,
		model.SubscriptionFormatLoon,
		model.SubscriptionFormatQX,
		model.SubscriptionFormatSurfboard,
	}
	for _, format := range formats {
		t.Run(string(format), func(t *testing.T) {
			output, err := renderSubscriptionTarget([]SubscriptionNode{node}, format)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"SSH Managed", sshSubscriptionPassword, sshSubscriptionHostKey} {
				if strings.Contains(output, secret) {
					t.Fatalf("%s leaked unsupported SSH node data:\n%s", format, output)
				}
			}
		})
	}
}

func TestSubscriptionTargetFiltersUnsupportedVariants(t *testing.T) {
	tests := []struct {
		name    string
		raw     map[string]any
		allowed []model.SubscriptionFormat
		denied  []model.SubscriptionFormat
	}{
		{
			name: "AnyTLS Reality",
			raw: map[string]any{
				"type": "anytls", "server": "example.com", "server_port": 443, "password": "secret",
				"tls": map[string]any{"enabled": true, "reality": map[string]any{"public_key": "public-key"}},
			},
			allowed: []model.SubscriptionFormat{model.SubscriptionFormatEgern, model.SubscriptionFormatLoon, model.SubscriptionFormatQX},
			denied:  []model.SubscriptionFormat{model.SubscriptionFormatStash, model.SubscriptionFormatSurge, model.SubscriptionFormatSurfboard},
		},
		{
			name: "VLESS gRPC",
			raw: map[string]any{
				"type": "vless", "server": "example.com", "server_port": 443, "uuid": "11111111-1111-4111-8111-111111111111",
				"transport": map[string]any{"type": "grpc", "service_name": "edge"},
			},
			allowed: []model.SubscriptionFormat{model.SubscriptionFormatEgern, model.SubscriptionFormatMihomo},
			denied:  []model.SubscriptionFormat{model.SubscriptionFormatLoon, model.SubscriptionFormatQX, model.SubscriptionFormatSurge},
		},
		{
			name: "HY2 gecko",
			raw: map[string]any{
				"type": "hysteria2", "server": "example.com", "server_port": 443, "password": "secret",
				"obfs": map[string]any{"type": "gecko", "password": "obfs-secret"},
			},
			allowed: []model.SubscriptionFormat{model.SubscriptionFormatMihomo, model.SubscriptionFormatSurge},
			denied:  []model.SubscriptionFormat{model.SubscriptionFormatLoon, model.SubscriptionFormatSurfboard},
		},
		{
			name: "SS 2022 chacha",
			raw: map[string]any{
				"type": "shadowsocks", "server": "example.com", "server_port": 8388,
				"method": "2022-blake3-chacha20-poly1305", "password": "secret",
			},
			allowed: []model.SubscriptionFormat{model.SubscriptionFormatMihomo, model.SubscriptionFormatShadowrocket},
			denied:  []model.SubscriptionFormat{model.SubscriptionFormatStash, model.SubscriptionFormatEgern, model.SubscriptionFormatLoon, model.SubscriptionFormatQX, model.SubscriptionFormatSurge, model.SubscriptionFormatSurfboard},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			proxy, err := normalizeSubscriptionNode(SubscriptionNode{Name: test.name, Raw: test.raw})
			if err != nil {
				t.Fatal(err)
			}
			for _, format := range test.allowed {
				if !subscriptionTargetSupports(format, proxy) {
					t.Errorf("%s unexpectedly rejected", format)
				}
			}
			for _, format := range test.denied {
				if subscriptionTargetSupports(format, proxy) {
					t.Errorf("%s unexpectedly accepted", format)
				}
			}
		})
	}
}

func TestMieruYAMLPortMapping(t *testing.T) {
	tests := []struct {
		name        string
		serverPorts []string
		wantPort    int
		wantRange   string
	}{
		{name: "continuous", serverPorts: []string{"25251-25252"}, wantRange: "25250-25252"},
		{name: "disjoint", serverPorts: []string{"25252-25253"}, wantPort: 25250},
	}
	formats := []model.SubscriptionFormat{
		model.SubscriptionFormatClashMeta,
		model.SubscriptionFormatMihomo,
	}
	for _, test := range tests {
		for _, format := range formats {
			t.Run(test.name+"/"+string(format), func(t *testing.T) {
				node := SubscriptionNode{Name: "Mieru", Raw: map[string]any{
					"type": "mieru", "server": "2001:db8::20", "server_port": 25250,
					"server_ports": test.serverPorts, "transport": "TCP",
					"username": "oboard-u7", "password": "mieru-pass",
					"multiplexing": "MULTIPLEXING_HIGH", "traffic_pattern": "AA==",
				}}
				output, err := renderSubscriptionTarget([]SubscriptionNode{node}, format)
				if err != nil {
					t.Fatal(err)
				}
				var document struct {
					Proxies []map[string]any `yaml:"proxies"`
				}
				if err := yaml.Unmarshal([]byte(output), &document); err != nil {
					t.Fatal(err)
				}
				if len(document.Proxies) != 1 {
					t.Fatalf("proxy count = %d, want 1:\n%s", len(document.Proxies), output)
				}
				proxy := document.Proxies[0]
				if proxy["type"] != "mieru" || proxy["transport"] != "TCP" || proxy["udp"] != true || proxy["username"] != "oboard-u7" || proxy["password"] != "mieru-pass" || proxy["multiplexing"] != "MULTIPLEXING_HIGH" || proxy["traffic-pattern"] != "AA==" {
					t.Fatalf("unexpected Mieru mapping: %#v", proxy)
				}
				if _, hasUserHint := proxy["user-hint-is-mandatory"]; hasUserHint {
					t.Fatalf("%s unexpectedly received Shadowrocket user hint: %#v", format, proxy)
				}
				if got := intFromAny(proxy["port"]); got != test.wantPort {
					t.Fatalf("port = %d, want %d: %#v", got, test.wantPort, proxy)
				}
				if got := stringFromAny(proxy["port-range"]); got != test.wantRange {
					t.Fatalf("port-range = %q, want %q: %#v", got, test.wantRange, proxy)
				}
			})
		}
	}
}

func TestMieruYAMLDefaultMultiplexingIsOmitted(t *testing.T) {
	formats := []model.SubscriptionFormat{
		model.SubscriptionFormatClashMeta,
		model.SubscriptionFormatMihomo,
	}
	for _, format := range formats {
		t.Run(string(format), func(t *testing.T) {
			node := SubscriptionNode{Name: "Mieru", Raw: map[string]any{
				"type": "mieru", "server": "mieru.example.com", "server_port": 25250,
				"transport": "TCP", "username": "oboard-u7", "password": "mieru-pass",
				"multiplexing": "MULTIPLEXING_DEFAULT",
			}}
			output, err := renderSubscriptionTarget([]SubscriptionNode{node}, format)
			if err != nil {
				t.Fatal(err)
			}
			var document struct {
				Proxies []map[string]any `yaml:"proxies"`
			}
			if err := yaml.Unmarshal([]byte(output), &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Proxies) != 1 {
				t.Fatalf("proxy count = %d, want 1:\n%s", len(document.Proxies), output)
			}
			if _, exists := document.Proxies[0]["multiplexing"]; exists {
				t.Fatalf("%s emitted unsupported default multiplexing value: %#v", format, document.Proxies[0])
			}
		})
	}
}

func TestEgernHTTPTransportAndURIFragmentArePreserved(t *testing.T) {
	node := SubscriptionNode{Name: "Edge+A B", Raw: map[string]any{
		"type": "vless", "server": "example.com", "server_port": 443,
		"uuid":      "11111111-1111-4111-8111-111111111111",
		"transport": map[string]any{"type": "http", "path": "/edge", "headers": map[string]any{"Host": "cdn.example.com"}},
		"tls":       map[string]any{"enabled": true, "server_name": "edge.example.com", "insecure": true},
	}}
	output, err := renderSubscriptionTarget([]SubscriptionNode{node}, model.SubscriptionFormatEgern)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Proxies []struct {
			VLESS struct {
				Transport map[string]map[string]any `yaml:"transport"`
			} `yaml:"vless"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatal(err)
	}
	httpTransport := parsed.Proxies[0].VLESS.Transport["http"]
	if httpTransport["path"] != "/edge" || httpTransport["sni"] != "edge.example.com" {
		t.Fatalf("Egern HTTP transport = %#v", httpTransport)
	}
	uri, err := renderSubscriptionTarget([]SubscriptionNode{node}, model.SubscriptionFormatV2RayURI)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uri, "allowInsecure=1") || !strings.HasSuffix(strings.TrimSpace(uri), "#Edge%2BA%20B") {
		t.Fatalf("VLESS URI escaping/TLS options = %s", uri)
	}
}

func TestMieruURIProfileUsesPercentEncodedSpaces(t *testing.T) {
	node := SubscriptionNode{Name: "🇯🇵 沪日 | Mieru+", Raw: map[string]any{
		"type": "mieru", "server": "mieru.example.com", "server_port": 25250,
		"transport": "TCP", "username": "oboard-u7", "password": "mieru-pass",
	}}

	output, err := renderSubscriptionTarget([]SubscriptionNode{node}, model.SubscriptionFormatMieru)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "profile=%F0%9F%87%AF%F0%9F%87%B5+") {
		t.Fatalf("Mieru profile contains a form-encoded space: %s", output)
	}
	if !strings.Contains(output, "profile=%F0%9F%87%AF%F0%9F%87%B5%20%E6%B2%AA%E6%97%A5%20%7C%20Mieru%2B") {
		t.Fatalf("Mieru profile is not URI encoded: %s", output)
	}
}

func TestShadowrocketMieruYAMLEnablesUserHint(t *testing.T) {
	node := SubscriptionNode{Name: "Mieru", Raw: map[string]any{
		"type": "mieru", "server": "mieru.example.com", "server_port": 25250,
		"transport": "TCP", "username": "oboard-u7", "password": "mieru-pass",
	}}

	output, err := renderSubscriptionTarget([]SubscriptionNode{node}, model.SubscriptionFormatShadowrocket)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(output), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Proxies) != 1 || document.Proxies[0]["user-hint-is-mandatory"] != true {
		t.Fatalf("Shadowrocket Mieru proxy = %#v, want mandatory user hint", document.Proxies)
	}

	official, err := renderSubscriptionTarget([]SubscriptionNode{node}, model.SubscriptionFormatMieru)
	if err != nil {
		t.Fatal(err)
	}
	officialURL, err := url.Parse(strings.TrimSpace(official))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := officialURL.Query()["user-hint-is-mandatory"]; exists {
		t.Fatalf("official Mieru URI unexpectedly received a Shadowrocket option: %s", official)
	}
}

func TestV2RaySubscriptionEncodesURIListWithoutMieru(t *testing.T) {
	output, err := renderSubscriptionTarget(subscriptionFormatFixtureNodes(), model.SubscriptionFormatV2Ray)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(output)
	if err != nil {
		t.Fatal(err)
	}
	text := string(decoded)
	if strings.Contains(text, "mieru") || !strings.Contains(text, "vless://") || strings.HasSuffix(text, "\n") {
		t.Fatalf("unexpected V2Ray payload: %q", text)
	}
}

func TestSubscriptionTargetEmptyOutputsAreValid(t *testing.T) {
	mieruOnly := subscriptionFormatFixtureNodes()[5:6]
	for _, format := range []model.SubscriptionFormat{
		model.SubscriptionFormatStash,
		model.SubscriptionFormatClash,
		model.SubscriptionFormatEgern,
		model.SubscriptionFormatSurge,
		model.SubscriptionFormatV2Ray,
		model.SubscriptionFormatV2RayURI,
	} {
		output, err := renderSubscriptionTarget(mieruOnly, format)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		switch format {
		case model.SubscriptionFormatStash, model.SubscriptionFormatClash, model.SubscriptionFormatEgern:
			var parsed map[string]any
			if err := yaml.Unmarshal([]byte(output), &parsed); err != nil {
				t.Fatalf("%s invalid YAML: %v\n%s", format, err, output)
			}
		default:
			if output != "" {
				t.Fatalf("%s empty output = %q", format, output)
			}
		}
	}
}

func TestSubscriptionContentTypesMatchNativeTargets(t *testing.T) {
	for _, format := range []model.SubscriptionFormat{
		model.SubscriptionFormatClashMeta,
		model.SubscriptionFormatMihomo,
		model.SubscriptionFormatStash,
		model.SubscriptionFormatClash,
		model.SubscriptionFormatEgern,
		model.SubscriptionFormatShadowrocket,
	} {
		if got := SubscriptionContentType(format); got != "text/yaml; charset=utf-8" {
			t.Fatalf("%s content type = %q", format, got)
		}
	}
}

func countRenderedSubscriptionProxies(t *testing.T, format model.SubscriptionFormat, output string) int {
	t.Helper()
	switch format {
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru:
		var parsed SingBoxConfig
		if err := json.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatal(err)
		}
		return len(parsed.Outbounds) - 1
	case model.SubscriptionFormatMieru, model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatSurfboard, model.SubscriptionFormatLoon, model.SubscriptionFormatQX, model.SubscriptionFormatV2RayURI:
		return len(strings.Split(strings.TrimSpace(output), "\n"))
	case model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo, model.SubscriptionFormatStash, model.SubscriptionFormatClash, model.SubscriptionFormatEgern, model.SubscriptionFormatShadowrocket:
		var parsed struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatal(err)
		}
		return len(parsed.Proxies)
	default:
		t.Fatalf("unhandled format %s", format)
		return 0
	}
}
