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

func TestWorkspaceMergeCanonicalOrderRegression(t *testing.T) {
	// P0 regression: workspace nodes must participate in the same canonical sort.
	// OBoard: A US, B JP ; Workspace: C HK, D JP ; policy HK > JP > US => C, B, D, A
	policy := model.SubscriptionNodeOrderPolicy{
		Version: 2, Mode: model.SubscriptionNodeOrderExitRegion,
		ExitRegionOrder: []string{"HK", "JP", "US"},
		EntryRegionOrderMode: model.SubscriptionNodeEntryRegionOrderInheritExit,
		NewNodePlacement: model.SubscriptionNodePlacementByTemplate,
		UnmatchedPlacement: model.SubscriptionNodePlacementAppend,
	}
	nodes := []SubscriptionNode{
		{Key: "inbound:1", Name: "🇺🇸 A US", Group: "default", ExitRegion: "US"},
		{Key: "inbound:2", Name: "🇯🇵 B JP", Group: "default", ExitRegion: "JP"},
		{Key: "private:1", Name: "🇭🇰 C HK", Group: "default", ExitRegion: "HK"},
		{Key: "private:2", Name: "🇯🇵 D JP", Group: "default", ExitRegion: "JP"},
	}
	// Simulate old buggy order: OBoard sorted alone => B, A ; workspace appended => B, A, C, D
	oboardOnly := []SubscriptionNode{nodes[0], nodes[1]}
	orderedOboard := OrderSubscriptionNodes(oboardOnly, policy)
	if canonicalKeys(orderedOboard)[0] != "inbound:2" {
		t.Fatalf("precondition failed: oboard order = %v", canonicalKeys(orderedOboard))
	}
	buggy := append(orderedOboard, nodes[2], nodes[3])
	if canonicalKeys(buggy)[0] != "inbound:2" || canonicalKeys(buggy)[2] != "private:1" {
		t.Fatalf("buggy order = %v", canonicalKeys(buggy))
	}
	// Correct: merge then sort once
	correct := OrderSubscriptionNodes(nodes, policy)
	want := []string{"private:1", "inbound:2", "private:2", "inbound:1"}
	got := canonicalKeys(correct)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("canonical order got %v want %v", got, want)
		}
	}
}

func TestCrossFormatStableSubsequenceInvariant(t *testing.T) {
	policy := model.SubscriptionNodeOrderPolicy{
		Version: 2, Mode: model.SubscriptionNodeOrderExitRegion,
		ExitRegionOrder: []string{"HK", "JP", "SG", "TW", "US"},
		EntryRegionOrderMode: model.SubscriptionNodeEntryRegionOrderInheritExit,
		NewNodePlacement: model.SubscriptionNodePlacementByTemplate,
		UnmatchedPlacement: model.SubscriptionNodePlacementAppend,
	}
	canonicalNodes := []SubscriptionNode{
		{Key: "inbound:1", Name: "🇭🇰 HK Vanguard", Group: "HK", ExitRegion: "HK", Raw: map[string]any{"type": "vless", "server": "hk.example.com", "server_port": 443, "uuid": "11111111-1111-4111-8111-111111111111", "tls": map[string]any{"enabled": true, "server_name": "hk.example.com"}}},
		{Key: "inbound:2", Name: "🇺🇸 Snell v6", Group: "US", ExitRegion: "US", Raw: map[string]any{"type": "snell", "server": "us.example.com", "server_port": 6160, "version": 6, "psk": "psk", "userkey": "uk"}},
		{Key: "inbound:3", Name: "🇯🇵 VLESS Reality", Group: "JP", ExitRegion: "JP", Raw: map[string]any{"type": "vless", "server": "jp.example.com", "server_port": 443, "uuid": "22222222-2222-4222-8222-222222222222", "tls": map[string]any{"enabled": true, "reality": map[string]any{"public_key": "pk"}}}},
		{Key: "inbound:4", Name: "🇸🇬 Mieru Discrete", Group: "SG", ExitRegion: "SG", Raw: map[string]any{"type": "mieru", "server": "sg.example.com", "server_port": 5000, "server_ports": []string{"5200-5200", "6000-6000"}, "transport": "TCP", "username": "u", "password": "p"}},
		{Key: "inbound:5", Name: "🇭🇰 SSH", Group: "HK", ExitRegion: "HK", Raw: map[string]any{"type": "ssh", "server": "hk.example.com", "server_port": 2222, "username": "oboard", "password": "pass", "host_key": []string{"ssh-ed25519 AAAAC3"}}},
		{Key: "inbound:6", Name: "🇹🇼 Snell v4", Group: "TW", ExitRegion: "TW", Raw: map[string]any{"type": "snell", "server": "tw.example.com", "server_port": 6160, "version": 4, "psk": "psk4", "userkey": "uk4"}},
		{Key: "inbound:7", Name: "🇺🇸 SS", Group: "US", ExitRegion: "US", Raw: map[string]any{"type": "shadowsocks", "server": "ss.example.com", "server_port": 8388, "method": "aes-128-gcm", "password": "pass"}},
		{Key: "inbound:8", Name: "🇺🇸 HY2", Group: "US", ExitRegion: "US", Raw: map[string]any{"type": "hysteria2", "server": "hy2.example.com", "server_port": 443, "password": "pass", "tls": map[string]any{"enabled": true, "server_name": "hy2.example.com"}}},
	}
	// Canonical order is policy-driven, but for invariant we also check that
	// OrderSubscriptionNodes is stable when applied to canonical order.
	canonical := OrderSubscriptionNodes(canonicalNodes, policy)
	keyList := canonicalKeys(canonical)
	// Verify Order is idempotent
	reordered := OrderSubscriptionNodes(canonical, policy)
	if strings.Join(canonicalKeys(reordered), ",") != strings.Join(keyList, ",") {
		t.Fatalf("order not idempotent: %v vs %v", keyList, canonicalKeys(reordered))
	}
	for _, format := range ConcreteSubscriptionFormats() {
		t.Run(string(format), func(t *testing.T) {
			proxies, err := normalizeSubscriptionNodes(canonical)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			compatible := filterCompatibleSubscriptionProxies(proxies, format, SubscriptionRenderOptions{})
			expected := []string{}
			compatibleSet := map[string]bool{}
			for _, p := range compatible {
				compatibleSet[p.Name] = true
			}
			for _, k := range keyList {
				for _, n := range canonical {
					if n.Key == k && compatibleSet[n.Name] {
						expected = append(expected, n.Name)
						break
					}
				}
			}
			// Render and inspect
			body, err := renderSubscriptionTarget(canonical, format)
			if err != nil {
				t.Fatalf("render %s: %v", format, err)
			}
			actual := inspectRenderedNames(t, format, body)
			// For SurgeMac, native+bridge proxies both count; their order must also preserve canonical
			// subsequence, but bridge helper "Mihomo-Core" must be ignored.
			// inspectRenderedNames already filters helpers.
			if len(expected) == 0 && len(actual) == 0 {
				return
			}
			// Verify actual is stable subsequence of canonical (preserves relative order)
			if !isSubsequence(expected, actual) && !isSubsequence(actual, expected) {
				// Prefer strict equality: filtered compatible should equal inspected
				t.Fatalf("format %s: expected subsequence %v, actual %v (canonical %v)", format, expected, actual, keyList)
			}
			if strings.Join(expected, "|") != strings.Join(actual, "|") {
				t.Fatalf("format %s: order mismatch\n expected: %v\n actual:   %v\n canonical: %v", format, expected, actual, keyList)
			}
		})
	}
}

func isSubsequence(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func inspectRenderedNames(t *testing.T, format model.SubscriptionFormat, body string) []string {
	t.Helper()
	format = normalizeSubscriptionFormat(format)
	switch format {
	case model.SubscriptionFormatMihomo, model.SubscriptionFormatStash:
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("yaml unmarshal %s: %v\n%s", format, err, body)
		}
		out := []string{}
		for _, p := range doc.Proxies {
			if name, ok := p["name"].(string); ok {
				out = append(out, name)
			}
		}
		return out
	case model.SubscriptionFormatEgern:
		var doc struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			t.Fatalf("yaml unmarshal %s: %v\n%s", format, err, body)
		}
		out := []string{}
		for _, p := range doc.Proxies {
			for _, v := range p {
				if m, ok := v.(map[string]any); ok {
					if name, ok := m["name"].(string); ok {
						out = append(out, name)
					}
				} else if mm, ok := v.(map[any]any); ok {
					for _, vv := range mm {
						if m, ok := vv.(map[string]any); ok {
							if name, ok := m["name"].(string); ok {
								out = append(out, name)
							}
						}
					}
				}
				break
			}
		}
		return out
	case model.SubscriptionFormatSingBox:
		var cfg SingBoxConfig
		if err := json.Unmarshal([]byte(body), &cfg); err != nil {
			t.Fatalf("singbox json: %v", err)
		}
		out := []string{}
		for _, o := range cfg.Outbounds {
			if tag, ok := o["tag"].(string); ok && tag != "direct" && tag != "block" {
				out = append(out, tag)
			}
		}
		return out
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurfboard, model.SubscriptionFormatLoon:
		out := []string{}
		inProxy := false
		for _, line := range strings.Split(body, "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
				inProxy = strings.EqualFold(trim, "[Proxy]")
				continue
			}
			if !inProxy || trim == "" || strings.HasPrefix(trim, "#") {
				continue
			}
			// Filter SurgeMac helper
			if format == model.SubscriptionFormatSurgeMac && strings.Contains(trim, "exec=") && strings.Contains(trim, "mihomo") {
				continue
			}
			name, _, ok := strings.Cut(trim, "=")
			if !ok {
				continue
			}
			out = append(out, strings.TrimSpace(name))
		}
		return out
	case model.SubscriptionFormatSurgeMac:
		out := []string{}
		inProxy := false
		for _, line := range strings.Split(body, "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
				inProxy = strings.EqualFold(trim, "[Proxy]")
				continue
			}
			if !inProxy || trim == "" || strings.HasPrefix(trim, "#") {
				continue
			}
			if strings.Contains(trim, "exec=") && strings.Contains(trim, "mihomo") {
				continue
			}
			name, _, ok := strings.Cut(trim, "=")
			if !ok {
				continue
			}
			out = append(out, strings.TrimSpace(name))
		}
		return out
	case model.SubscriptionFormatQX:
		out := []string{}
		inProxy := false
		for _, line := range strings.Split(body, "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
				inProxy = strings.EqualFold(trim, "[Proxy]")
				continue
			}
			if !inProxy || trim == "" || strings.HasPrefix(trim, "#") {
				continue
			}
			// QX name is in tag= param
			tag := ""
			for _, part := range strings.Split(trim, ",") {
				if after, ok := strings.CutPrefix(strings.TrimSpace(part), "tag="); ok {
					tag = after
					break
				}
			}
			if tag != "" {
				// unescape
				tag = strings.ReplaceAll(tag, "\\,", ",")
				out = append(out, tag)
			}
		}
		return out
	case model.SubscriptionFormatShadowrocket, model.SubscriptionFormatV2RayURI:
		if strings.TrimSpace(body) == "" {
			return []string{}
		}
		lines := strings.Split(strings.TrimSpace(body), "\n")
		out := []string{}
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "mierus://") {
				if u, err := url.Parse(line); err == nil {
					if profile := u.Query().Get("profile"); profile != "" {
						out = append(out, profile)
						continue
					}
				}
			}
			if idx := strings.LastIndex(line, "#"); idx >= 0 {
				out = append(out, inspectShadowrocketFragment(line[idx+1:]))
			} else {
				out = append(out, line)
			}
		}
		return out
	case model.SubscriptionFormatV2Ray:
		if strings.TrimSpace(body) == "" {
			return []string{}
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body))
		if err != nil {
			t.Fatalf("v2ray base64 decode: %v", err)
		}
		return inspectRenderedNames(t, model.SubscriptionFormatV2RayURI, string(decoded))
	default:
		t.Fatalf("unhandled format %s", format)
		return nil
	}
}

func inspectShadowrocketFragment(raw string) string {
	if decoded, err := url.PathUnescape(raw); err == nil {
		return decoded
	}
	if decoded, err := url.QueryUnescape(raw); err == nil {
		return decoded
	}
	return raw
}

func canonicalKeys(nodes []SubscriptionNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.Key
	}
	return out
}
