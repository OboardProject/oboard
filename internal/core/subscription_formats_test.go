package core

import (
	"encoding/base64"
	"encoding/json"
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
				"method": "chacha20-ietf-poly1305", "password": "ss-pass", "udp_over_tcp": map[string]any{"enabled": true},
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
	}
}

const (
	sshSubscriptionPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\ntest-private-key\n-----END OPENSSH PRIVATE KEY-----\n"
	sshSubscriptionHostKey    = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestAgentHostKey"
)

func sshSubscriptionFixtureNode() SubscriptionNode {
	return SubscriptionNode{Name: "SSH Managed", Group: "SSH", Raw: map[string]any{
		"type": "ssh", "server": "ssh.example.com", "server_port": 2222,
		"username": "oboard-7", "private_key": sshSubscriptionPrivateKey,
		"host_key": []string{sshSubscriptionHostKey},
	}}
}

func TestSubscriptionTargetCapabilityMatrix(t *testing.T) {
	nodes := subscriptionFormatFixtureNodes()
	tests := []struct {
		format     model.SubscriptionFormat
		proxyCount int
		contains   []string
		excludes   []string
	}{
		{format: model.SubscriptionFormatPlainJSON, proxyCount: 6, contains: []string{`"type": "mieru"`}, excludes: []string{"oboard_group", "must-not-leak"}},
		{format: model.SubscriptionFormatSingBox, proxyCount: 5, excludes: []string{`"type": "mieru"`, "oboard_group", "must-not-leak"}},
		{format: model.SubscriptionFormatSingBoxMieru, proxyCount: 6, contains: []string{`"type": "mieru"`, `"server_port": 25250`}, excludes: []string{"oboard_group", "must-not-leak"}},
		{format: model.SubscriptionFormatMieru, proxyCount: 1, contains: []string{"mierus://", "25251-25252", "protocol=TCP"}, excludes: []string{"vless://"}},
		{format: model.SubscriptionFormatClashMeta, proxyCount: 6, contains: []string{"reality-opts:", "udp-over-tcp: true", "type: mieru", "port-range: 25250-25252", "traffic-pattern: AA=="}},
		{format: model.SubscriptionFormatMihomo, proxyCount: 6, contains: []string{"reality-opts:", "obfs-password: obfs-pass", "type: mieru", "port-range: 25250-25252"}},
		{format: model.SubscriptionFormatStash, proxyCount: 5, contains: []string{"auth: hy2-pass", "up-speed: 100", "down-speed: 200"}, excludes: []string{"type: mieru"}},
		{format: model.SubscriptionFormatShadowrocket, proxyCount: 6, contains: []string{"proxies:", "type: mieru", "port-range: 25250-25252", "user-hint-is-mandatory: true"}, excludes: []string{"proxy-groups:", "rules:"}},
		{format: model.SubscriptionFormatEgern, proxyCount: 5, contains: []string{"type: shadowsocks", "method: chacha20-poly1305", "bandwidth: 100", "user_id:"}, excludes: []string{"type: mieru"}},
		{format: model.SubscriptionFormatLoon, proxyCount: 5, contains: []string{"=vless,", "=Hysteria2,", "udp-over-tcp=true"}, excludes: []string{"mieru"}},
		{format: model.SubscriptionFormatQX, proxyCount: 4, contains: []string{"vless=", "anytls=", "udp-over-tcp=sp.v2"}, excludes: []string{"hysteria2=", "mieru"}},
		{format: model.SubscriptionFormatSurge, proxyCount: 4, contains: []string{"=hysteria2,", "=anytls,", "download-bandwidth=200", "udp-relay=true"}, excludes: []string{"=vless,", "mieru"}},
		{format: model.SubscriptionFormatSurgeMac, proxyCount: 4, contains: []string{"=hysteria2,"}, excludes: []string{"=vless,", "mieru"}},
		{format: model.SubscriptionFormatSurfboard, proxyCount: 4, contains: []string{"=hysteria2,", "download-bandwidth=200", `SOCKS=socks5,socks.example.com,1080,"alice","socks-pass"`}, excludes: []string{"=vless,", "mieru"}},
		{format: model.SubscriptionFormatClash, proxyCount: 2, contains: []string{"type: ss", "type: socks5"}, excludes: []string{"type: vless", "type: hysteria2", "type: anytls", "type: mieru"}},
		{format: model.SubscriptionFormatV2RayURI, proxyCount: 5, contains: []string{"vless://", "hysteria2://", "anytls://", "ss://", "socks://"}, excludes: []string{"mierus://"}},
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

	plain, err := renderSubscriptionTarget([]SubscriptionNode{node}, model.SubscriptionFormatPlainJSON)
	if err != nil {
		t.Fatal(err)
	}
	var canonical []map[string]any
	if err := json.Unmarshal([]byte(plain), &canonical); err != nil {
		t.Fatal(err)
	}
	if len(canonical) != 1 || canonical[0]["type"] != "ssh" || canonical[0]["server"] != "ssh.example.com" || intFromAny(canonical[0]["port"]) != 2222 || canonical[0]["username"] != "oboard-7" || canonical[0]["private_key"] != sshSubscriptionPrivateKey || !stringSetContains(stringListFromAny(canonical[0]["host_key"]), sshSubscriptionHostKey) {
		t.Fatalf("plain SSH mapping = %#v", canonical)
	}

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
	if sshOutbound["type"] != "ssh" || sshOutbound["user"] != "oboard-7" || sshOutbound["private_key"] != sshSubscriptionPrivateKey || !stringSetContains(stringListFromAny(sshOutbound["host_key"]), sshSubscriptionHostKey) {
		t.Fatalf("sing-box SSH mapping = %#v", sshOutbound)
	}

	yamlFormats := []model.SubscriptionFormat{
		model.SubscriptionFormatClashMeta,
		model.SubscriptionFormatMihomo,
		model.SubscriptionFormatShadowrocket,
		model.SubscriptionFormatStash,
		model.SubscriptionFormatEgern,
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
			usernameKey, privateKeyKey, hostKeyKey := "username", "private-key", "host-key"
			if format == model.SubscriptionFormatStash {
				usernameKey = "user"
			}
			if format == model.SubscriptionFormatEgern {
				privateKeyKey, hostKeyKey = "private_key", "host_keys"
			}
			if proxy["type"] != "ssh" || proxy["server"] != "ssh.example.com" || intFromAny(proxy["port"]) != 2222 || proxy[usernameKey] != "oboard-7" || proxy[privateKeyKey] != sshSubscriptionPrivateKey || !stringSetContains(stringListFromAny(proxy[hostKeyKey]), sshSubscriptionHostKey) {
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
			keyName := surgeSSHKeyName(sshSubscriptionPrivateKey)
			for _, want := range []string{
				"[Proxy]\n",
				"SSH Managed=ssh,ssh.example.com,2222",
				`username="oboard-7"`,
				"private-key=" + keyName,
				`server-fingerprint="` + sshSubscriptionHostKey + `"`,
				"[Keystore]\n",
				keyName + "=type=openssh-private-key,base64=" + base64.StdEncoding.EncodeToString([]byte(sshSubscriptionPrivateKey)),
			} {
				if !strings.Contains(output, want) {
					t.Fatalf("%s output missing %q:\n%s", format, want, output)
				}
			}
		})
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
		model.SubscriptionFormatV2Ray,
		model.SubscriptionFormatV2RayURI,
	}
	for _, format := range formats {
		t.Run(string(format), func(t *testing.T) {
			output, err := renderSubscriptionTarget([]SubscriptionNode{node}, format)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"SSH Managed", sshSubscriptionPrivateKey, sshSubscriptionHostKey} {
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
		model.SubscriptionFormatShadowrocket,
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
				userHint, hasUserHint := proxy["user-hint-is-mandatory"]
				if format == model.SubscriptionFormatShadowrocket {
					if !hasUserHint || userHint != true {
						t.Fatalf("Shadowrocket Mieru user hint = %#v, want true: %#v", userHint, proxy)
					}
				} else if hasUserHint {
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
			Transport map[string]map[string]any `yaml:"transport"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal([]byte(output), &parsed); err != nil {
		t.Fatal(err)
	}
	httpTransport := parsed.Proxies[0].Transport["http"]
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
	mieruOnly := subscriptionFormatFixtureNodes()[5:]
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
		model.SubscriptionFormatShadowrocket,
		model.SubscriptionFormatEgern,
	} {
		if got := SubscriptionContentType(format); got != "text/yaml; charset=utf-8" {
			t.Fatalf("%s content type = %q", format, got)
		}
	}
}

func countRenderedSubscriptionProxies(t *testing.T, format model.SubscriptionFormat, output string) int {
	t.Helper()
	switch format {
	case model.SubscriptionFormatPlainJSON:
		var parsed []map[string]any
		if err := json.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatal(err)
		}
		return len(parsed)
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru:
		var parsed SingBoxConfig
		if err := json.Unmarshal([]byte(output), &parsed); err != nil {
			t.Fatal(err)
		}
		return len(parsed.Outbounds) - 1
	case model.SubscriptionFormatMieru, model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatSurfboard, model.SubscriptionFormatLoon, model.SubscriptionFormatQX, model.SubscriptionFormatV2RayURI:
		return len(strings.Split(strings.TrimSpace(output), "\n"))
	case model.SubscriptionFormatClashMeta, model.SubscriptionFormatMihomo, model.SubscriptionFormatStash, model.SubscriptionFormatClash, model.SubscriptionFormatShadowrocket, model.SubscriptionFormatEgern:
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
