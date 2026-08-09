package controller

import (
	"testing"
)

func TestNewRecipeRouting(t *testing.T) {
	s := &Server{}
	tests := []struct {
		name, goal string
		want       string
		refs       []string
		params     map[string]any
	}{
		{name: "outbound zh", goal: "给东京服务器创建出口", want: "outbound.manage", refs: []string{"server:1"}, params: map[string]any{"name": "出口A", "protocol": "shadowsocks", "target_address": "203.0.113.5", "target_port": 8388}},
		{name: "outbound en", goal: "create outbound on server 1", want: "outbound.manage", params: map[string]any{"server_id": 1}},
		{name: "routing zh", goal: "新建一条分流规则", want: "routing.manage", refs: []string{"server:1"}, params: map[string]any{"name": "直连", "action": "direct"}},
		{name: "import zh", goal: "导入节点", want: "external_outbound.import", params: map[string]any{"content": "ss://base64@host:8388"}},
		{name: "dns policy", goal: "设置东京服务器的 DNS 策略", want: "dns_policy.manage", refs: []string{"server:1"}, params: map[string]any{"encrypted_list_id": 1, "bootstrap_list_id": 2}},
		{name: "dns record", goal: "创建一条解析记录", want: "dns_record.manage", params: map[string]any{"record": map[string]any{"dns_zone_id": 1, "name": "www", "type": "A", "content": "1.2.3.4"}}},
		{name: "port forward", goal: "添加端口转发", want: "port_forward.manage", params: map[string]any{"port_forward": map[string]any{"name": "pf", "listen_port": 1000, "target_port": 2000}}},
		{name: "tunnel", goal: "创建一条 wireguard 隧道", want: "tunnel.manage", params: map[string]any{"tunnel": map[string]any{"name": "t1"}}},
		{name: "diagnose", goal: "诊断服务器", want: "host_ops.manage", refs: []string{"server:1"}},
		{name: "logs", goal: "拉取服务器日志", want: "host_ops.manage", refs: []string{"server:1"}},
		{name: "notification", goal: "创建一个通知频道", want: "notification.manage", params: map[string]any{"notification_channel": map[string]any{"name": "tg", "type": "telegram"}}},
		{name: "certificate", goal: "签发证书", want: "certificate.manage", params: map[string]any{"certificate_id": 3}},
		{name: "settings", goal: "修改全局设置", want: "settings.manage", params: map[string]any{"audit_enabled": false}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := mcpTaskInput{Goal: test.goal, TargetRefs: test.refs, Params: test.params}
			recipe, candidates, ok := s.matchMCPRecipe(input)
			if !ok || recipe.ID != test.want {
				t.Fatalf("recipe=%#v candidates=%#v ok=%v", recipe, candidates, ok)
			}
		})
	}
}
