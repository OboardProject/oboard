package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

type nodePresetSeed struct {
	Name        string
	Protocol    string
	Kind        string
	ConfigJSON  string
	DefaultPort int
	Remark      string
}

var builtinNodePresets = []nodePresetSeed{
	{Name: "VLESS Reality Vision", Protocol: "vless", Kind: "vless-reality", DefaultPort: 443, Remark: "TCP + Reality + Vision，默认握手 cdn.icloud-content.com", ConfigJSON: `{"flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"cdn.icloud-content.com","reality":{"enabled":true,"handshake":{"server":"cdn.icloud-content.com","server_port":443}}}}`},
	{Name: "VLESS TLS Vision", Protocol: "vless", Kind: "vless-tls-vision", DefaultPort: 443, Remark: "TCP + TLS + Vision，需要证书", ConfigJSON: `{"flow":"xtls-rprx-vision","tls":{"enabled":true}}`},
	{Name: "VLESS WebSocket", Protocol: "vless", Kind: "vless-ws", DefaultPort: 443, Remark: "WebSocket + TLS，需要证书", ConfigJSON: `{"tls":{"enabled":true},"transport":{"type":"ws","path":"/vless","headers":{}}}`},
	{Name: "VLESS TCP", Protocol: "vless", Kind: "vless-tcp", DefaultPort: 443, Remark: "无 TLS，适合内网或测试", ConfigJSON: `{}`},
	{Name: "Hysteria2", Protocol: "hy2", Kind: "hy2-tls", DefaultPort: 443, Remark: "HY2 标准配置，需要证书", ConfigJSON: `{"tls":{"enabled":true},"up_mbps":100,"down_mbps":100}`},
	{Name: "AnyTLS", Protocol: "anytls", Kind: "anytls-basic", DefaultPort: 443, Remark: "AnyTLS 标准配置，需要证书", ConfigJSON: `{"tls":{"enabled":true}}`},
	{Name: "SS 128", Protocol: "shadowsocks", Kind: "ss-aes-128-gcm", DefaultPort: 8388, Remark: "AES-128-GCM，单用户", ConfigJSON: `{"method":"aes-128-gcm"}`},
	{Name: "SS 256", Protocol: "shadowsocks", Kind: "ss-aes-256-gcm", DefaultPort: 8388, Remark: "AES-256-GCM，单用户", ConfigJSON: `{"method":"aes-256-gcm"}`},
	{Name: "SS 2022-128", Protocol: "shadowsocks", Kind: "ss-2022-128", DefaultPort: 8388, Remark: "AES-128-GCM，多用户", ConfigJSON: `{"method":"2022-blake3-aes-128-gcm"}`},
	{Name: "SS 2022-256", Protocol: "shadowsocks", Kind: "ss-2022-256", DefaultPort: 8388, Remark: "AES-256-GCM，多用户", ConfigJSON: `{"method":"2022-blake3-aes-256-gcm"}`},
	{Name: "Mieru", Protocol: "mieru", Kind: "mieru-basic", DefaultPort: 25250, Remark: "Mieru 多用户入口", ConfigJSON: `{"transport":"TCP","multiplexing":"MULTIPLEXING_DEFAULT","user_hint_is_mandatory":true}`},
	{Name: "SOCKS5", Protocol: "socks", Kind: "socks5-auth", DefaultPort: 1080, Remark: "用户名密码认证，支持 TCP 与 UDP", ConfigJSON: `{"version":"5"}`},
}

func (s *Store) seedNodePresets(ctx context.Context) error {
	ts := "2026-01-01T00:00:00Z"
	for _, seed := range builtinNodePresets {
		if _, err := s.db.ExecContext(ctx, `insert or ignore into node_presets(name,protocol,kind,config_json,default_port,remark,builtin,enabled,created_at,updated_at) values(?,?,?,?,?,?,1,1,?,?)`,
			seed.Name, seed.Protocol, seed.Kind, seed.ConfigJSON, seed.DefaultPort, seed.Remark, ts, ts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateNodePreset(ctx context.Context, v *model.NodePreset) error {
	if err := NormalizeNodePreset(v); err != nil {
		return err
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into node_presets(name,protocol,kind,config_json,default_port,remark,builtin,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
		v.Name, v.Protocol, v.Kind, v.ConfigJSON, v.DefaultPort, v.Remark, boolInt(v.Builtin), boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	return nil
}

func (s *Store) UpdateNodePreset(ctx context.Context, v *model.NodePreset) error {
	var builtin int
	var currentKind, currentProtocol string
	if err := s.db.QueryRowContext(ctx, `select builtin,kind,protocol from node_presets where id=?`, v.ID).Scan(&builtin, &currentKind, &currentProtocol); err != nil {
		return err
	}
	v.Builtin = builtin == 1
	if v.Builtin {
		v.Enabled = true
		v.Kind = currentKind
		v.Protocol = currentProtocol
	}
	if err := NormalizeNodePreset(v); err != nil {
		return err
	}
	ts := now()
	if _, err := s.db.ExecContext(ctx, `update node_presets set name=?,protocol=?,kind=?,config_json=?,default_port=?,remark=?,enabled=?,updated_at=? where id=?`,
		v.Name, v.Protocol, v.Kind, v.ConfigJSON, v.DefaultPort, v.Remark, boolInt(v.Enabled), ts, v.ID); err != nil {
		return err
	}
	v.UpdatedAt = parseTime(ts)
	return nil
}

func (s *Store) ListNodePresets(ctx context.Context) ([]model.NodePreset, error) {
	query := `select p.id,p.name,p.protocol,p.kind,p.config_json,p.default_port,p.remark,p.builtin,p.enabled,p.created_at,p.updated_at,
		(select count(*) from inbounds i where i.config_json like '%"node_preset_id":'||p.id||'%' or i.config_json like '%"node_preset_id": '||p.id||'%')
		from node_presets p order by p.protocol,p.builtin desc,p.name,p.id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.NodePreset
	for rows.Next() {
		var item model.NodePreset
		var created, updated string
		var builtin, enabled int
		if err := rows.Scan(&item.ID, &item.Name, &item.Protocol, &item.Kind, &item.ConfigJSON, &item.DefaultPort, &item.Remark, &builtin, &enabled, &created, &updated, &item.UsageCount); err != nil {
			return nil, err
		}
		item.Builtin = builtin == 1
		item.Enabled = enabled == 1
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetNodePreset(ctx context.Context, id int64) (*model.NodePreset, error) {
	items, err := s.ListNodePresets(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].ID == id {
			return &items[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *Store) DeleteNodePreset(ctx context.Context, id int64) error {
	var builtin, usage int
	if err := s.db.QueryRowContext(ctx, `select builtin,
		(select count(*) from inbounds i where i.config_json like '%"node_preset_id":'||?||'%' or i.config_json like '%"node_preset_id": '||?||'%')
		from node_presets where id=?`, id, id, id).Scan(&builtin, &usage); err != nil {
		return err
	}
	if builtin == 1 {
		return errors.New("内置节点预设不可删除")
	}
	if usage > 0 {
		return fmt.Errorf("节点预设正被 %d 个入口引用，请先解绑", usage)
	}
	_, err := s.db.ExecContext(ctx, `delete from node_presets where id=?`, id)
	return err
}

var nodePresetKinds = map[string]string{
	"vless-reality":    "vless",
	"vless-tls-vision": "vless",
	"vless-ws":         "vless",
	"vless-tcp":        "vless",
	"hy2-tls":          "hy2",
	"anytls-basic":     "anytls",
	"ss-aes-128-gcm":   "shadowsocks",
	"ss-aes-256-gcm":   "shadowsocks",
	"ss-2022-128":      "shadowsocks",
	"ss-2022-256":      "shadowsocks",
	"mieru-basic":      "mieru",
	"socks5-auth":      "socks",
}

func NormalizeNodePreset(v *model.NodePreset) error {
	v.Name = strings.TrimSpace(v.Name)
	v.Protocol = strings.ToLower(strings.TrimSpace(v.Protocol))
	v.Kind = strings.ToLower(strings.TrimSpace(v.Kind))
	v.Remark = strings.TrimSpace(v.Remark)
	if v.Name == "" {
		return errors.New("name required")
	}
	expected, ok := nodePresetKinds[v.Kind]
	if !ok {
		return fmt.Errorf("unsupported node preset kind %q", v.Kind)
	}
	if v.Protocol == "" {
		v.Protocol = expected
	}
	if v.Protocol != expected {
		return fmt.Errorf("kind %s belongs to protocol %s", v.Kind, expected)
	}
	if v.DefaultPort <= 0 || v.DefaultPort > 65535 {
		v.DefaultPort = 443
		for _, seed := range builtinNodePresets {
			if seed.Kind == v.Kind {
				v.DefaultPort = seed.DefaultPort
				break
			}
		}
	}
	config, err := normalizeNodePresetConfig(v.Kind, v.ConfigJSON)
	if err != nil {
		return err
	}
	v.ConfigJSON = config
	return nil
}

func normalizeNodePresetConfig(kind, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		trimmed = "{}"
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil || parsed == nil {
		return "", errors.New("config_json must be a JSON object")
	}
	merged := mergeJSONObjects(defaultNodePresetConfig(kind), parsed)
	return compactJSON(merged)
}

func defaultNodePresetConfig(kind string) map[string]any {
	for _, seed := range builtinNodePresets {
		if seed.Kind != kind {
			continue
		}
		var parsed map[string]any
		if err := json.Unmarshal([]byte(seed.ConfigJSON), &parsed); err == nil && parsed != nil {
			return parsed
		}
	}
	return map[string]any{}
}

func mergeJSONObjects(base, overlay map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range base {
		out[key] = cloneJSONValue(value)
	}
	for key, value := range overlay {
		if nested, ok := value.(map[string]any); ok {
			if current, ok := out[key].(map[string]any); ok {
				out[key] = mergeJSONObjects(current, nested)
				continue
			}
		}
		out[key] = cloneJSONValue(value)
	}
	return out
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return mergeJSONObjects(typed, nil)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneJSONValue(item)
		}
		return out
	default:
		return typed
	}
}

func compactJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
