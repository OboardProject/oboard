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
	classic, err := PreviewSubscriptionNodes(nodes, model.SubscriptionFormatClash)
	if err != nil || len(classic.Nodes) != 0 || classic.FilteredCount != 2 || classic.Content == "" {
		t.Fatalf("classic clash preview=%#v err=%v", classic, err)
	}
	uri, err := RenderSubscriptionNodes(nodes, model.SubscriptionFormatV2RayURI)
	if err != nil || !strings.Contains(uri, "trojan://") || !strings.Contains(uri, "tuic://") {
		t.Fatalf("URI output=%q err=%v", uri, err)
	}
}
