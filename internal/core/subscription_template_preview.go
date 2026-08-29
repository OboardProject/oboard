package core

import (
	"fmt"

	"github.com/OboardProject/oboard/internal/model"
)

// SubscriptionTemplatePreviewNodes returns synthetic protocol fixtures used
// when validating or previewing a client template. Credentials are fake.
func SubscriptionTemplatePreviewNodes() []SubscriptionNode {
	return []SubscriptionNode{
		{Name: "VLESS Reality", Group: "preview", Raw: map[string]any{
			"type": "vless", "server": "vless.example.com", "server_port": 443,
			"uuid": "11111111-1111-4111-8111-111111111111", "flow": "xtls-rprx-vision", "packet_encoding": "xudp",
			"tls": map[string]any{"enabled": true, "server_name": "vless.example.com", "utls": map[string]any{"fingerprint": "chrome"}, "reality": map[string]any{"public_key": "preview-reality-public", "short_id": "abcd"}},
		}},
		{Name: "HY2", Group: "preview", Raw: map[string]any{
			"type": "hysteria2", "server": "hy2.example.com", "server_port": 8443, "password": "preview-hy2-pass",
			"server_ports": []string{"8444-8446"}, "hop_interval": "20s", "up_mbps": 100, "down_mbps": 200,
			"obfs": map[string]any{"type": "salamander", "password": "preview-obfs"},
			"tls":  map[string]any{"enabled": true, "server_name": "hy2.example.com", "alpn": []any{"h3"}},
		}},
		{Name: "AnyTLS", Group: "preview", Raw: map[string]any{
			"type": "anytls", "server": "anytls.example.com", "server_port": 443, "password": "preview-anytls-pass",
			"tls": map[string]any{"enabled": true, "server_name": "anytls.example.com"},
		}},
		{Name: "SS2022", Group: "preview", Raw: map[string]any{
			"type": "shadowsocks", "server": "ss.example.com", "server_port": 8388,
			"method": "2022-blake3-aes-128-gcm", "password": "preview-ss2022-pass",
		}},
		{Name: "SOCKS5", Group: "preview", Raw: map[string]any{
			"type": "socks", "server": "socks.example.com", "server_port": 1080,
			"username": "preview-user", "password": "preview-socks-pass",
		}},
		{Name: "SSH", Group: "preview", Raw: map[string]any{
			"type": "ssh", "server": "ssh.example.com", "server_port": 2222,
			"username": "preview-ssh", "password": "preview-ssh-pass",
			"host_key": []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIPreviewHostKey"},
		}},
		{Name: "Mieru", Group: "preview", Raw: map[string]any{
			"type": "mieru", "server": "mieru.example.com", "server_port": 25250,
			"transport": "TCP", "username": "preview-mieru", "password": "preview-mieru-pass",
			"multiplexing": "MULTIPLEXING_HIGH",
		}},
		{Name: "Mieru Multiport", Group: "preview", Raw: map[string]any{
			"type": "mieru", "server": "mieru.example.com", "server_port": 25250,
			"server_ports": []string{"25251-25252"}, "transport": "TCP",
			"username": "preview-mieru", "password": "preview-mieru-pass",
			"multiplexing": "MULTIPLEXING_HIGH", "traffic_pattern": "AA==",
		}},
		{Name: "Snell v4", Group: "preview", Raw: map[string]any{
			"type": "snell", "server": "snell.example.com", "server_port": 6160,
			"version": 4, "psk": "preview-snell-v4", "userkey": "preview-snell-v4-userkey", "obfs_mode": "http", "obfs_host": "bing.com", "reuse": true,
		}},
		{Name: "Snell v6", Group: "preview", Raw: map[string]any{
			"type": "snell", "server": "snell6.example.com", "server_port": 7177,
			"version": 6, "psk": "preview-snell-v6", "userkey": "preview-snell-v6-userkey", "mode": "unshaped", "reuse": true,
		}},
	}
}

func RenderSubscriptionTemplatePreview(format model.SubscriptionFormat, template string) (string, error) {
	format = normalizeSubscriptionFormat(format)
	if err := ValidateSubscriptionTemplate(format, template); err != nil {
		return "", err
	}
	content, err := renderSubscriptionDocument(SubscriptionTemplatePreviewNodes(), format, SubscriptionRenderOptions{Template: template})
	if err != nil {
		return "", fmt.Errorf("template preview failed: %w", err)
	}
	return content, nil
}

func ValidateSubscriptionTemplateWithPreview(format model.SubscriptionFormat, template string) error {
	_, err := RenderSubscriptionTemplatePreview(format, template)
	return err
}
