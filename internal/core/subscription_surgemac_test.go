package core

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestParseSubscriptionRenderOptionsIgnoresNonSurgeMac(t *testing.T) {
	query := url.Values{"mihomo": []string{"always"}, "mihomoLocalPort": []string{"not-a-port"}}
	opts, err := ParseSubscriptionRenderOptions(model.SubscriptionFormatSurge, query, "1:2")
	if err != nil {
		t.Fatal(err)
	}
	if opts.SurgeMac != (SurgeMacOptions{}) {
		t.Fatalf("unexpected options for surge: %#v", opts.SurgeMac)
	}
}

func TestParseSubscriptionRenderOptionsSurgeMacDefaultsAndValidation(t *testing.T) {
	opts, err := ParseSubscriptionRenderOptions(model.SubscriptionFormatSurgeMac, url.Values{}, "7:9")
	if err != nil {
		t.Fatal(err)
	}
	if opts.SurgeMac.Mode != SurgeMacMihomoAuto || !opts.SurgeMac.Merge || opts.SurgeMac.Exec != defaultSurgeMacMihomoExec || opts.SurgeMac.MergeName != defaultSurgeMacMergeName || opts.SurgeMac.PortScopeKey != "7:9" {
		t.Fatalf("defaults = %#v", opts.SurgeMac)
	}

	query := url.Values{
		"mihomo":          []string{"always"},
		"mihomoMerge":     []string{"false"},
		"mihomoExec":      []string{"/opt/homebrew/bin/mihomo"},
		"mihomoLocalPort": []string{"51001"},
		"mihomoMergeName": []string{"Core"},
	}
	opts, err = ParseSubscriptionRenderOptions(model.SubscriptionFormatSurgeMac, query, "1:1")
	if err != nil {
		t.Fatal(err)
	}
	if opts.SurgeMac.Mode != SurgeMacMihomoAlways || opts.SurgeMac.Merge || opts.SurgeMac.Exec != "/opt/homebrew/bin/mihomo" || opts.SurgeMac.LocalPort != 51001 || opts.SurgeMac.MergeName != "Core" {
		t.Fatalf("parsed = %#v", opts.SurgeMac)
	}

	for _, raw := range []url.Values{
		{"mihomo": []string{"maybe"}},
		{"mihomoMerge": []string{"sometimes"}},
		{"mihomoExec": []string{"mihomo"}},
		{"mihomoLocalPort": []string{"70000"}},
		{"mihomoMergeName": []string{"=\n"}},
	} {
		if _, err := ParseSubscriptionRenderOptions(model.SubscriptionFormatSurgeMac, raw, ""); err == nil {
			t.Fatalf("accepted invalid query %#v", raw)
		}
	}
}

func TestSurgeMacDerivedPortBaseIsStablePerSubscription(t *testing.T) {
	if surgeMacDerivedPortBase("") != defaultSurgeMacPortBase {
		t.Fatalf("empty scope = %d", surgeMacDerivedPortBase(""))
	}
	first := surgeMacDerivedPortBase("11:22")
	second := surgeMacDerivedPortBase("11:22")
	other := surgeMacDerivedPortBase("11:23")
	if first != second || first < surgeMacPortHashMin || first >= surgeMacPortHashMin+surgeMacPortHashSpan {
		t.Fatalf("stable hash = %d / %d", first, second)
	}
	if first == other {
		t.Fatal("different subscriptions unexpectedly shared a port base")
	}
}

func TestSurgeMacMergeUsesOneMihomoAndLocalSOCKS(t *testing.T) {
	output, err := renderSubscriptionTargetWithOptions(subscriptionFormatFixtureNodes(), model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoAuto, Merge: true, LocalPort: 51000, Exec: "/opt/homebrew/bin/mihomo", MergeName: "Mihomo-Core"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "=vless,") || strings.Count(output, "=external,") != 1 {
		t.Fatalf("unexpected native/external mix:\n%s", output)
	}
	if !strings.Contains(output, "VLESS IPv6=socks5,127.0.0.1,51001") || !strings.Contains(output, "Mieru=socks5,127.0.0.1,51002") {
		t.Fatalf("missing merged socks lines:\n%s", output)
	}
	if !strings.Contains(output, `Mihomo-Core=external,exec="/opt/homebrew/bin/mihomo",local-port=51000`) {
		t.Fatalf("missing merged core:\n%s", output)
	}
	if !strings.Contains(output, "addresses=2001:db8::10,2001:db8::20") {
		t.Fatalf("missing VIF addresses:\n%s", output)
	}
	if !strings.Contains(output, "HY2=hysteria2,") || !strings.Contains(output, "Snell v6=snell,") {
		t.Fatalf("native nodes were rewritten:\n%s", output)
	}
	config := decodeSurgeMacExternalConfig(t, output, "Mihomo-Core")
	if intFromAny(config["mixed-port"]) != 51000 {
		t.Fatalf("mixed-port = %#v", config["mixed-port"])
	}
	proxies := anySlice(config["proxies"])
	listeners := anySlice(config["listeners"])
	if len(proxies) != 2 || len(listeners) != 2 {
		t.Fatalf("merged proxies/listeners = %#v %#v", proxies, listeners)
	}
	if stringFromAny(asMap(proxies[0])["type"]) != "vless" || stringFromAny(asMap(proxies[0])["name"]) != "p-51001" {
		t.Fatalf("vless proxy = %#v", proxies[0])
	}
	if stringFromAny(asMap(proxies[1])["type"]) != "mieru" || stringFromAny(asMap(proxies[1])["name"]) != "p-51002" {
		t.Fatalf("mieru proxy = %#v", proxies[1])
	}
	if stringFromAny(asMap(listeners[0])["proxy"]) != "p-51001" || intFromAny(asMap(listeners[0])["port"]) != 51001 {
		t.Fatalf("vless listener = %#v", listeners[0])
	}
}

func TestSurgeMacPerNodeStartsOneExternalEach(t *testing.T) {
	output, err := renderSubscriptionTargetWithOptions(subscriptionFormatFixtureNodes()[:1], model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoAuto, Merge: false, LocalPort: 62000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(output, "=external,") != 1 || strings.Contains(output, "Mihomo-Core=") || strings.Contains(output, "=socks5,127.0.0.1,") {
		t.Fatalf("unexpected per-node output:\n%s", output)
	}
	if !strings.Contains(output, "VLESS IPv6=external,") || !strings.Contains(output, "local-port=62000") || !strings.Contains(output, "addresses=2001:db8::10") {
		t.Fatalf("missing per-node external:\n%s", output)
	}
	config := decodeSurgeMacExternalConfig(t, output, "VLESS IPv6")
	if intFromAny(config["mixed-port"]) != 62000 || stringFromAny(config["mode"]) != "global" {
		t.Fatalf("per-node config = %#v", config)
	}
	proxies := anySlice(config["proxies"])
	if len(proxies) != 1 || stringFromAny(asMap(proxies[0])["name"]) != "proxy" || stringFromAny(asMap(proxies[0])["type"]) != "vless" {
		t.Fatalf("per-node proxies = %#v", proxies)
	}
}

func TestSurgeMacAlwaysUsesMihomoExceptUnsupportedNativeFallback(t *testing.T) {
	nodes := []SubscriptionNode{subscriptionFormatFixtureNodes()[0], subscriptionFormatFixtureNodes()[7]}
	output, err := renderSubscriptionTargetWithOptions(nodes, model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoAlways, Merge: true, LocalPort: 53000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "VLESS IPv6=socks5,127.0.0.1,53001") {
		t.Fatalf("always mode missing vless socks:\n%s", output)
	}
	if !strings.Contains(output, "Snell v6=snell,") {
		t.Fatalf("snell v6 should stay native because Mihomo cannot produce it:\n%s", output)
	}
	if strings.Contains(output, "Snell v6=socks5,") {
		t.Fatalf("snell v6 was incorrectly sent to Mihomo:\n%s", output)
	}
}

func TestSurgeMacOffMatchesNativeSurge(t *testing.T) {
	nodes := subscriptionFormatFixtureNodes()
	off, err := renderSubscriptionTargetWithOptions(nodes, model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoOff},
	})
	if err != nil {
		t.Fatal(err)
	}
	native, err := renderSubscriptionTarget(nodes, model.SubscriptionFormatSurge)
	if err != nil {
		t.Fatal(err)
	}
	if off != native {
		t.Fatalf("off output differs from surge:\n%s\n---\n%s", off, native)
	}
}

func TestSurgeMacLeavesDomainServersWithoutAddresses(t *testing.T) {
	node := SubscriptionNode{Name: "VLESS Domain", Group: "g", Raw: map[string]any{
		"type": "vless", "server": "edge.example.com", "server_port": 443,
		"uuid": "11111111-1111-4111-8111-111111111111",
		"tls":  map[string]any{"enabled": true, "server_name": "edge.example.com"},
	}}
	output, err := renderSubscriptionTargetWithOptions([]SubscriptionNode{node}, model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoAuto, Merge: false, LocalPort: 54000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "addresses=") {
		t.Fatalf("domain node should not pin addresses:\n%s", output)
	}
}

func TestSurgeMacPortOverflowIsAnError(t *testing.T) {
	nodes := subscriptionFormatFixtureNodes()[:1]
	_, err := renderSubscriptionTargetWithOptions(nodes, model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoAuto, Merge: true, LocalPort: 65535},
	})
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestSurgeMacPreviewKeepsMihomoBackedNodes(t *testing.T) {
	preview, err := PreviewSubscriptionNodes(subscriptionFormatFixtureNodes(), model.SubscriptionFormatSurgeMac)
	if err != nil {
		t.Fatal(err)
	}
	if preview.FilteredCount != 0 || len(preview.Nodes) != 8 {
		t.Fatalf("preview nodes=%d filtered=%d reasons=%v", len(preview.Nodes), preview.FilteredCount, preview.InvalidReasons)
	}
	if !strings.Contains(preview.Content, "VLESS IPv6=socks5,127.0.0.1,") || !strings.Contains(preview.Content, "Mihomo-Core=external,") {
		t.Fatalf("preview content missing Mihomo adapter:\n%s", preview.Content)
	}
}

func TestSurgeMacStableJSONDoesNotChangeBetweenEncodes(t *testing.T) {
	first, err := renderSubscriptionTargetWithOptions(subscriptionFormatFixtureNodes()[:1], model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoAuto, Merge: false, LocalPort: 60000, PortScopeKey: "stable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderSubscriptionTargetWithOptions(subscriptionFormatFixtureNodes()[:1], model.SubscriptionFormatSurgeMac, SubscriptionRenderOptions{
		SurgeMac: SurgeMacOptions{Mode: SurgeMacMihomoAuto, Merge: false, LocalPort: 60000, PortScopeKey: "stable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("non-deterministic Surge Mac output\n%s\n---\n%s", first, second)
	}
}

func decodeSurgeMacExternalConfig(t *testing.T, output, name string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, name+"=external,") {
			continue
		}
		const marker = `args="-config",args="`
		index := strings.Index(line, marker)
		if index < 0 {
			t.Fatalf("missing encoded config on %s: %s", name, line)
		}
		encoded := line[index+len(marker):]
		encoded = strings.SplitN(encoded, `"`, 2)[0]
		decoded, err := decodeSurgeMacMihomoConfig(encoded)
		if err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	t.Fatalf("external node %s not found in:\n%s", name, output)
	return nil
}

func anySlice(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	default:
		return nil
	}
}

func asMap(value any) map[string]any {
	typed, _ := value.(map[string]any)
	if typed == nil {
		return map[string]any{}
	}
	return typed
}

func TestSurgeMacMihomoConfigRoundTrip(t *testing.T) {
	encoded, err := encodeSurgeMacMihomoConfig(map[string]any{"mixed-port": 1, "ipv6": true})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSurgeMacMihomoConfig(encoded)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(decoded)
	if !strings.Contains(string(raw), `"mixed-port":1`) {
		t.Fatalf("round trip = %s", raw)
	}
}
