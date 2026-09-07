package core

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestParsePrivateSubscriptionContainersAndPartialFailure(t *testing.T) {
	uriList := "trojan://secret@[2001:4860:4860::8888]:443?sni=example.com#Trojan\ninvalid://broken\ntuic://uuid:password@1.1.1.1:443?sni=tuic.example#TUIC"
	encoded := base64.StdEncoding.EncodeToString([]byte(uriList))
	result, err := ParsePrivateSubscription(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 || len(result.Issues) != 1 || result.Nodes[0].Protocol != model.PrivateProtocolTrojan || result.Nodes[1].Protocol != model.PrivateProtocolTUIC {
		t.Fatalf("base64 result = %#v", result)
	}

	wrapped := encoded[:24] + "\n" + encoded[24:48] + "\r\n" + encoded[48:]
	result, err = ParsePrivateSubscription(wrapped)
	if err != nil || len(result.Nodes) != 2 || result.Nodes[0].Name != "Trojan" || result.Nodes[1].Name != "TUIC" {
		t.Fatalf("line-wrapped base64 result=%#v err=%v", result, err)
	}

	yamlInput := `proxies:
  - name: Clash VMess
    type: vmess
    server: 8.8.8.8
    port: 443
    uuid: 00000000-0000-0000-0000-000000000001
    alterId: 0
    cipher: auto
    tls: true
    servername: vmess.example
    network: ws
    ws-opts:
      path: /socket
      headers:
        Host: vmess.example
`
	result, err = ParsePrivateSubscription(yamlInput)
	if err != nil || len(result.Nodes) != 1 || result.Nodes[0].Protocol != model.PrivateProtocolVMess {
		t.Fatalf("yaml result=%#v err=%v", result, err)
	}

	jsonInput := `{"outbounds":[{"type":"hysteria2","tag":"JSON HY2","server":"1.0.0.1","server_port":8443,"password":"secret","tls":{"enabled":true,"server_name":"hy.example"}}]}`
	result, err = ParsePrivateSubscription(jsonInput)
	if err != nil || len(result.Nodes) != 1 || result.Nodes[0].Protocol != model.PrivateProtocolHysteria2 {
		t.Fatalf("json result=%#v err=%v", result, err)
	}
}

func TestPrivateProtocolsRenderAndFilterByTarget(t *testing.T) {
	input := "trojan://secret@8.8.8.8:443?sni=example.com#Trojan\ntuic://uuid:password@1.1.1.1:443?sni=tuic.example#TUIC"
	parsed, err := ParsePrivateSubscription(input)
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]SubscriptionNode, 0, len(parsed.Nodes))
	for index, item := range parsed.Nodes {
		encoded, _ := json.Marshal(item.Raw)
		var raw map[string]any
		_ = json.Unmarshal(encoded, &raw)
		nodes = append(nodes, SubscriptionNode{Key: item.Fingerprint, Name: item.Name, Group: "third-party", Raw: raw, NodeID: int64(index + 1)})
	}
	preview, err := PreviewSubscriptionNodes(nodes, model.SubscriptionFormatSingBox)
	if err != nil || len(preview.Nodes) != 2 || !strings.Contains(preview.Content, `"type": "trojan"`) || !strings.Contains(preview.Content, `"type": "tuic"`) {
		t.Fatalf("sing-box preview=%#v err=%v", preview, err)
	}
	classic, err := PreviewSubscriptionNodes(nodes, model.SubscriptionFormatLoon)
	if err != nil || len(classic.Nodes) != 0 || classic.FilteredCount != 2 {
		t.Fatalf("classic clash preview=%#v err=%v", classic, err)
	}
	uri, err := RenderSubscriptionNodes(nodes, model.SubscriptionFormatV2RayURI)
	if err != nil || !strings.Contains(uri, "trojan://") || !strings.Contains(uri, "tuic://") {
		t.Fatalf("URI output=%q err=%v", uri, err)
	}
}

// sing-box carries the client hello fingerprint only in tls.utls and knows no
// "tcp" transport, and it decodes with unknown fields disallowed. An imported
// reality node used to render both a flat tls.fingerprint and a tcp transport
// object, so every sing-box client rejected the whole configuration.
func TestPrivateRealityImportRendersSingBoxCompatibleFields(t *testing.T) {
	uri := "vless://11111111-1111-4111-8111-111111111111@203.0.113.9:443?encryption=none&security=reality&sni=example.com&fp=chrome&pbk=client-public&sid=1a2b&type=tcp&flow=xtls-rprx-vision#imported"
	parsed, err := ParsePrivateSubscription(uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Nodes) != 1 {
		t.Fatalf("nodes = %d", len(parsed.Nodes))
	}
	raw := parsed.Nodes[0].Raw
	tls, ok := raw["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls missing: %#v", raw)
	}
	if _, ok := tls["fingerprint"]; ok {
		t.Fatalf("import stored a flat tls.fingerprint: %#v", tls)
	}
	utls, ok := tls["utls"].(map[string]any)
	if !ok || utls["fingerprint"] != "chrome" || utls["enabled"] != true {
		t.Fatalf("import did not store utls: %#v", tls)
	}
	if transport, ok := raw["transport"]; ok {
		t.Fatalf("plain TCP stored a transport object: %#v", transport)
	}
	rendered := singBoxOutboundForTest(t, SubscriptionNodeFromPrivate(model.ImportedNode{ID: 5, Name: "imported", Protocol: parsed.Nodes[0].Protocol}, raw, "third-party"))
	renderedTLS, _ := rendered["tls"].(map[string]any)
	if _, ok := renderedTLS["fingerprint"]; ok {
		t.Fatalf("rendered outbound kept a flat tls.fingerprint: %#v", renderedTLS)
	}
	if _, ok := rendered["transport"]; ok {
		t.Fatalf("rendered outbound kept a tcp transport: %#v", rendered)
	}
}

// Nodes imported before the parser was fixed still hold the invalid shape in
// the database, so rendering has to normalize them too.
func TestSingBoxSubscriptionNormalizesStoredImportedNodeShape(t *testing.T) {
	stored := map[string]any{
		"type": "vless", "tag": "[vless]USA-GM1", "server": "203.0.113.9", "server_port": 443,
		"uuid": "11111111-1111-4111-8111-111111111111", "flow": "xtls-rprx-vision",
		"tls": map[string]any{
			"enabled": true, "server_name": "example.com", "fingerprint": "chrome",
			"reality": map[string]any{"public_key": "client-public", "short_id": "1a2b"},
		},
		"transport": map[string]any{"type": "tcp", "host": "", "path": "", "service_name": ""},
	}
	rendered := singBoxOutboundForTest(t, SubscriptionNode{Key: "private:1", Name: "[vless]USA-GM1", Group: "third-party", Raw: stored})
	tls, ok := rendered["tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls missing: %#v", rendered)
	}
	if _, ok := tls["fingerprint"]; ok {
		t.Fatalf("stored flat fingerprint survived rendering: %#v", tls)
	}
	utls, ok := tls["utls"].(map[string]any)
	if !ok || utls["fingerprint"] != "chrome" || utls["enabled"] != true {
		t.Fatalf("stored fingerprint was not folded into utls: %#v", tls)
	}
	if _, ok := rendered["transport"]; ok {
		t.Fatalf("stored tcp transport survived rendering: %#v", rendered)
	}

	stored["transport"] = map[string]any{"type": "ws", "host": "cdn.example.com", "path": "/ray", "service_name": ""}
	rendered = singBoxOutboundForTest(t, SubscriptionNode{Key: "private:1", Name: "[vless]USA-GM1", Group: "third-party", Raw: stored})
	transport, ok := rendered["transport"].(map[string]any)
	if !ok || transport["type"] != "ws" || transport["path"] != "/ray" {
		t.Fatalf("websocket transport = %#v", rendered["transport"])
	}
	if _, ok := transport["host"]; ok {
		t.Fatalf("websocket transport kept a bare host field: %#v", transport)
	}
	if _, ok := transport["service_name"]; ok {
		t.Fatalf("websocket transport kept an empty service_name: %#v", transport)
	}
	headers, ok := transport["headers"].(map[string]any)
	if !ok || headers["Host"] != "cdn.example.com" {
		t.Fatalf("websocket host was not moved into headers: %#v", transport)
	}
}

func singBoxOutboundForTest(t *testing.T, node SubscriptionNode) map[string]any {
	t.Helper()
	rendered, err := RenderSubscriptionNodes([]SubscriptionNode{node}, model.SubscriptionFormatSingBox)
	if err != nil {
		t.Fatalf("render sing-box: %v", err)
	}
	var doc struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal([]byte(rendered), &doc); err != nil {
		t.Fatalf("sing-box output is not valid JSON: %v\n%s", err, rendered)
	}
	for _, outbound := range doc.Outbounds {
		if outbound["type"] != "direct" {
			return outbound
		}
	}
	t.Fatalf("no proxy outbound rendered: %s", rendered)
	return nil
}
