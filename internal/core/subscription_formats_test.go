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
		{format: model.SubscriptionFormatClashMeta, proxyCount: 5, contains: []string{"reality-opts:", "udp-over-tcp: true"}, excludes: []string{"type: mieru"}},
		{format: model.SubscriptionFormatMihomo, proxyCount: 5, contains: []string{"reality-opts:", "obfs-password: obfs-pass"}, excludes: []string{"type: mieru"}},
		{format: model.SubscriptionFormatStash, proxyCount: 5, contains: []string{"auth: hy2-pass", "up-speed: 100", "down-speed: 200"}, excludes: []string{"type: mieru"}},
		{format: model.SubscriptionFormatShadowrocket, proxyCount: 5, contains: []string{"proxies:"}, excludes: []string{"proxy-groups:", "rules:", "type: mieru"}},
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
		model.SubscriptionFormatClashMeta,
		model.SubscriptionFormatShadowrocket,
		model.SubscriptionFormatSurge,
		model.SubscriptionFormatV2Ray,
		model.SubscriptionFormatV2RayURI,
	} {
		output, err := renderSubscriptionTarget(mieruOnly, format)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		switch format {
		case model.SubscriptionFormatClashMeta, model.SubscriptionFormatShadowrocket:
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
