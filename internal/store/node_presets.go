package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
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

// BuiltinRealityHandshakeDomains is the system Reality handshake domain
// template carried by the vless-reality preset; the first entry is the default.
var BuiltinRealityHandshakeDomains = []string{"gateway.icloud.com", "cdn.icloud-content.com", "www.tesla.com", "www.nvidia.com", "www.sony.com", "www.mozilla.org"}

const (
	builtinNodePresetSeedTimestamp = "2026-01-01T00:00:00Z"
	legacyAnyTLSBasicPresetConfig  = `{"tls":{"enabled":true}}`
	legacyAnyTLSBasicPresetName    = "AnyTLS"
	legacyAnyTLSBasicPresetRemark  = "AnyTLS 标准配置，需要证书"
	legacyAnyTLSLargePresetConfig  = `{"padding_scheme":["stop=3","0=900-1400","1=900-1400","2=900-1400"],"tcp_fast_open":true,"tls":{"enabled":true}}`
	legacyHY2TLSPresetConfig       = `{"tls":{"enabled":true},"up_mbps":100,"down_mbps":100}`
	legacyHY2TLSPresetName         = "Hysteria2"
	legacyHY2TLSPresetRemark       = "HY2 标准配置，需要证书"
)

var builtinNodePresets = []nodePresetSeed{
	{Name: "VLESS Reality Vision", Protocol: "vless", Kind: "vless-reality", DefaultPort: 443, Remark: "TCP + Reality + Vision，内置握手域名模板，默认 gateway.icloud.com", ConfigJSON: `{"flow":"xtls-rprx-vision","reality_domains":["gateway.icloud.com","cdn.icloud-content.com","www.tesla.com","www.nvidia.com","www.sony.com","www.mozilla.org"],"tls":{"enabled":true,"server_name":"gateway.icloud.com","reality":{"enabled":true,"handshake":{"server":"gateway.icloud.com","server_port":443}}},"tcp_fast_open":true}`},
	{Name: "VLESS WebSocket", Protocol: "vless", Kind: "vless-ws", DefaultPort: 443, Remark: "WebSocket + TLS，需要证书", ConfigJSON: `{"tls":{"enabled":true},"transport":{"type":"ws","path":"/vless","headers":{}},"tcp_fast_open":true}`},
	{Name: "VLESS TCP", Protocol: "vless", Kind: "vless-tcp", DefaultPort: 443, Remark: "无 TLS，适合内网或测试", ConfigJSON: `{"tcp_fast_open":true}`},
	{Name: "Hysteria2 标准", Protocol: "hy2", Kind: "hy2-tls", DefaultPort: 443, Remark: "HY2 标准模式，需要证书", ConfigJSON: `{"tls":{"enabled":true}}`},
	{Name: "Hysteria2 Salamander", Protocol: "hy2", Kind: "hy2-salamander", DefaultPort: 443, Remark: "HY2 Salamander 混淆，需要证书；混淆密码每个入口单独生成", ConfigJSON: `{"tls":{"enabled":true},"obfs":{"type":"salamander"}}`},
	{Name: "AnyTLS 均衡填充", Protocol: "anytls", Kind: "anytls-basic", DefaultPort: 443, Remark: "OBoard 均衡填充，兼顾额外开销与包长变化，需要证书", ConfigJSON: mustNodePresetConfig(map[string]any{"tls": map[string]any{"enabled": true}, "padding_scheme": core.AnyTLSBalancedPaddingScheme(), "tcp_fast_open": true})},
	{Name: "AnyTLS 轻量填充", Protocol: "anytls", Kind: "anytls-large-padding", DefaultPort: 443, Remark: "低开销填充，适合高延迟、移动网络和短连接，需要证书", ConfigJSON: mustNodePresetConfig(map[string]any{"tls": map[string]any{"enabled": true}, "padding_scheme": core.AnyTLSLightPaddingScheme(), "tcp_fast_open": true})},
	{Name: "SS 128", Protocol: "shadowsocks", Kind: "ss-aes-128-gcm", DefaultPort: 8388, Remark: "AES-128-GCM，单用户", ConfigJSON: `{"method":"aes-128-gcm","tcp_fast_open":true}`},
	{Name: "SS 256", Protocol: "shadowsocks", Kind: "ss-aes-256-gcm", DefaultPort: 8388, Remark: "AES-256-GCM，单用户", ConfigJSON: `{"method":"aes-256-gcm","tcp_fast_open":true}`},
	{Name: "SS 2022-128", Protocol: "shadowsocks", Kind: "ss-2022-128", DefaultPort: 8388, Remark: "AES-128-GCM，多用户", ConfigJSON: `{"method":"2022-blake3-aes-128-gcm","tcp_fast_open":true}`},
	{Name: "SS 2022-256", Protocol: "shadowsocks", Kind: "ss-2022-256", DefaultPort: 8388, Remark: "AES-256-GCM，多用户", ConfigJSON: `{"method":"2022-blake3-aes-256-gcm","tcp_fast_open":true}`},
	{Name: "Mieru", Protocol: "mieru", Kind: "mieru-basic", DefaultPort: 25250, Remark: "Mieru 多用户入口", ConfigJSON: `{"transport":"TCP","multiplexing":"MULTIPLEXING_DEFAULT","user_hint_is_mandatory":true,"tcp_fast_open":true}`},
	{Name: "SOCKS5", Protocol: "socks", Kind: "socks5-auth", DefaultPort: 1080, Remark: "用户名密码认证，支持 TCP 与 UDP", ConfigJSON: `{"version":"5","tcp_fast_open":true}`},
}

func mustNodePresetConfig(config map[string]any) string {
	encoded, err := json.Marshal(config)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// BuiltinNodePresetCount reports how many system templates ship with the Controller.
func BuiltinNodePresetCount() int {
	return len(builtinNodePresets)
}

func (s *Store) seedNodePresets(ctx context.Context) error {
	ts := builtinNodePresetSeedTimestamp
	for _, seed := range builtinNodePresets {
		if _, err := s.db.ExecContext(ctx, `insert into node_presets(name,protocol,kind,config_json,default_port,remark,builtin,enabled,created_at,updated_at)
			select ?,?,?,?,?,?,1,1,?,? where not exists (select 1 from node_presets where kind=? and builtin=1)`,
			seed.Name, seed.Protocol, seed.Kind, seed.ConfigJSON, seed.DefaultPort, seed.Remark, ts, ts, seed.Kind); err != nil {
			return err
		}
	}
	return nil
}

const removedVLESSTLSVisionKind = "vless-tls-vision"

func (s *Store) migrateRemoveVLESSTLSVisionPreset(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `select id from node_presets where kind=?`, removedVLESSTLSVisionKind)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if err := s.stripNodePresetReferences(ctx, id); err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `delete from node_presets where kind=?`, removedVLESSTLSVisionKind)
	return err
}

func (s *Store) stripNodePresetReferences(ctx context.Context, presetID int64) error {
	rows, err := s.db.QueryContext(ctx, `select id, config_json from inbounds where config_json like '%"node_preset_id":'||?||'%' or config_json like '%"node_preset_id": '||?||'%'`, presetID, presetID)
	if err != nil {
		return err
	}
	type inboundRow struct {
		id         int64
		configJSON string
	}
	var toUpdate []inboundRow
	for rows.Next() {
		var item inboundRow
		if err := rows.Scan(&item.id, &item.configJSON); err != nil {
			rows.Close()
			return err
		}
		toUpdate = append(toUpdate, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range toUpdate {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(item.configJSON), &cfg); err != nil || cfg == nil {
			continue
		}
		value, ok := cfg["node_preset_id"]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			if int64(typed) != presetID {
				continue
			}
		case json.Number:
			parsed, err := typed.Int64()
			if err != nil || parsed != presetID {
				continue
			}
		default:
			if fmt.Sprint(value) != fmt.Sprint(presetID) {
				continue
			}
		}
		delete(cfg, "node_preset_id")
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `update inbounds set config_json=?, updated_at=? where id=?`, string(encoded), now(), item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateAnyTLSPaddingPresets(ctx context.Context) error {
	var seed *nodePresetSeed
	for index := range builtinNodePresets {
		if builtinNodePresets[index].Kind == "anytls-basic" {
			seed = &builtinNodePresets[index]
			break
		}
	}
	if seed == nil {
		return errors.New("anytls-basic builtin preset seed missing")
	}
	rows, err := s.db.QueryContext(ctx, `select id,name,remark,config_json from node_presets where builtin=1 and kind='anytls-basic' and updated_at=?`, builtinNodePresetSeedTimestamp)
	if err != nil {
		return err
	}
	type legacyPreset struct {
		id                   int64
		name, remark, config string
	}
	var legacyRows []legacyPreset
	for rows.Next() {
		var item legacyPreset
		if err := rows.Scan(&item.id, &item.name, &item.remark, &item.config); err != nil {
			rows.Close()
			return err
		}
		legacyRows = append(legacyRows, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range legacyRows {
		if !sameJSONDocument(item.config, legacyAnyTLSBasicPresetConfig) {
			continue
		}
		if item.name == legacyAnyTLSBasicPresetName {
			var conflicts int
			if err := s.db.QueryRowContext(ctx, `select count(*) from node_presets where id<>? and name=?`, item.id, seed.Name).Scan(&conflicts); err != nil {
				return err
			}
			if conflicts == 0 {
				item.name = seed.Name
			}
		}
		if item.remark == legacyAnyTLSBasicPresetRemark {
			item.remark = seed.Remark
		}
		if _, err := s.db.ExecContext(ctx, `update node_presets set name=?,remark=?,config_json=?,updated_at=? where id=?`, item.name, item.remark, seed.ConfigJSON, now(), item.id); err != nil {
			return err
		}
	}
	var lightSeed *nodePresetSeed
	for index := range builtinNodePresets {
		if builtinNodePresets[index].Kind == "anytls-large-padding" {
			lightSeed = &builtinNodePresets[index]
			break
		}
	}
	if lightSeed == nil {
		return errors.New("AnyTLS light builtin preset seed missing")
	}
	rows, err = s.db.QueryContext(ctx, `select id,name,remark,config_json from node_presets where builtin=1 and kind='anytls-large-padding'`)
	if err != nil {
		return err
	}
	legacyRows = legacyRows[:0]
	for rows.Next() {
		var item legacyPreset
		if err := rows.Scan(&item.id, &item.name, &item.remark, &item.config); err != nil {
			rows.Close()
			return err
		}
		legacyRows = append(legacyRows, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range legacyRows {
		if !sameJSONDocument(item.config, legacyAnyTLSLargePresetConfig) {
			continue
		}
		if item.name == "AnyTLS 大包填充" {
			item.name = lightSeed.Name
		}
		if item.remark == "前三次写入使用 900-1400 字节填充，需要证书" {
			item.remark = lightSeed.Remark
		}
		if _, err := s.db.ExecContext(ctx, `update node_presets set name=?,remark=?,config_json=?,updated_at=? where id=?`, item.name, item.remark, lightSeed.ConfigJSON, now(), item.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) migrateHY2Presets(ctx context.Context) error {
	var seed *nodePresetSeed
	for index := range builtinNodePresets {
		if builtinNodePresets[index].Kind == "hy2-tls" {
			seed = &builtinNodePresets[index]
			break
		}
	}
	if seed == nil {
		return errors.New("hy2-tls builtin preset seed missing")
	}
	rows, err := s.db.QueryContext(ctx, `select id,name,remark,config_json from node_presets where builtin=1 and kind='hy2-tls' and updated_at=?`, builtinNodePresetSeedTimestamp)
	if err != nil {
		return err
	}
	type legacyPreset struct {
		id                   int64
		name, remark, config string
	}
	var legacyRows []legacyPreset
	for rows.Next() {
		var item legacyPreset
		if err := rows.Scan(&item.id, &item.name, &item.remark, &item.config); err != nil {
			rows.Close()
			return err
		}
		legacyRows = append(legacyRows, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range legacyRows {
		if !sameJSONDocument(item.config, legacyHY2TLSPresetConfig) {
			continue
		}
		if item.name == legacyHY2TLSPresetName {
			var conflicts int
			if err := s.db.QueryRowContext(ctx, `select count(*) from node_presets where id<>? and name=?`, item.id, seed.Name).Scan(&conflicts); err != nil {
				return err
			}
			if conflicts == 0 {
				item.name = seed.Name
			}
		}
		if item.remark == legacyHY2TLSPresetRemark {
			item.remark = seed.Remark
		}
		if _, err := s.db.ExecContext(ctx, `update node_presets set name=?,remark=?,config_json=?,updated_at=? where id=?`, item.name, item.remark, seed.ConfigJSON, now(), item.id); err != nil {
			return err
		}
	}
	return s.stripHY2PresetInboundOwnedFields(ctx)
}

func (s *Store) stripHY2PresetInboundOwnedFields(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `select id,kind,config_json from node_presets where protocol='hy2'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var toUpdate []struct {
		id         int64
		configJSON string
	}
	for rows.Next() {
		var id int64
		var kind, configJSON string
		if err := rows.Scan(&id, &kind, &configJSON); err != nil {
			return err
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(configJSON), &cfg); err != nil || cfg == nil {
			continue
		}
		sanitizeHY2NodePresetConfig(kind, cfg)
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		if string(encoded) == strings.TrimSpace(configJSON) || sameJSONDocument(string(encoded), configJSON) {
			continue
		}
		toUpdate = append(toUpdate, struct {
			id         int64
			configJSON string
		}{id: id, configJSON: string(encoded)})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range toUpdate {
		if _, err := s.db.ExecContext(ctx, `update node_presets set config_json=?, updated_at=? where id=?`, row.configJSON, now(), row.id); err != nil {
			return err
		}
	}
	return nil
}

func sanitizeHY2NodePresetConfig(kind string, cfg map[string]any) {
	if cfg == nil {
		return
	}
	delete(cfg, "up_mbps")
	delete(cfg, "down_mbps")
	if kind == "hy2-tls" {
		delete(cfg, "obfs")
		return
	}
	if kind != "hy2-salamander" {
		return
	}
	cfg["obfs"] = map[string]any{"type": "salamander"}
}

func sameJSONDocument(left, right string) bool {
	var leftValue, rightValue any
	if json.Unmarshal([]byte(left), &leftValue) != nil || json.Unmarshal([]byte(right), &rightValue) != nil {
		return false
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func (s *Store) migratePresetTCPFastOpen(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `update snell_profiles set tcp_fast_open=1, updated_at=? where builtin=1 and tcp_fast_open=0`, now()); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `select id, kind, config_json from node_presets where builtin=1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type presetRow struct {
		id         int64
		kind       string
		configJSON string
	}
	var toUpdate []presetRow
	for rows.Next() {
		var row presetRow
		if err := rows.Scan(&row.id, &row.kind, &row.configJSON); err != nil {
			return err
		}
		if strings.HasPrefix(row.kind, "hy2-") {
			continue
		}
		var cfg map[string]any
		if err := json.Unmarshal([]byte(row.configJSON), &cfg); err != nil || cfg == nil {
			continue
		}
		if _, exists := cfg["tcp_fast_open"]; exists {
			continue
		}
		// Mieru UDP does not support TFO; builtin Mieru is TCP so it should receive TFO.
		if row.kind == "mieru-basic" {
			transport := "TCP"
			if value, ok := cfg["transport"].(string); ok && strings.TrimSpace(value) != "" {
				transport = value
			}
			if strings.ToUpper(strings.TrimSpace(transport)) != "TCP" {
				continue
			}
		}
		if row.kind == "vless-ws" || strings.HasPrefix(row.kind, "vless-") {
			// VLESS with QUIC transport does not support TFO; no builtin uses QUIC, so enable.
			if raw, ok := cfg["transport"].(map[string]any); ok {
				if typ, _ := raw["type"].(string); strings.ToLower(strings.TrimSpace(typ)) == "quic" {
					continue
				}
			}
		}
		cfg["tcp_fast_open"] = true
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		row.configJSON = string(encoded)
		toUpdate = append(toUpdate, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range toUpdate {
		if _, err := s.db.ExecContext(ctx, `update node_presets set config_json=?, updated_at=? where id=?`, row.configJSON, now(), row.id); err != nil {
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

// RestoreBuiltinNodePresets resets every builtin node preset row back to the
// canonical system template of its kind. Builtin rows keep their kind locked,
// so kind is a stable identity even after an operator rename; missing builtin
// templates are re-seeded. Custom presets are never touched and row IDs (and
// therefore inbound references) are preserved.
func (s *Store) RestoreBuiltinNodePresets(ctx context.Context) (int64, error) {
	ts := now()
	var restored int64
	for _, seed := range builtinNodePresets {
		result, err := s.db.ExecContext(ctx, `update node_presets set name=?,config_json=?,default_port=?,remark=?,enabled=1,builtin=1,updated_at=? where kind=? and builtin=1`,
			seed.Name, seed.ConfigJSON, seed.DefaultPort, seed.Remark, ts, seed.Kind)
		if err != nil {
			return restored, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return restored, err
		}
		if affected > 0 {
			restored += affected
			continue
		}
		if _, err := s.db.ExecContext(ctx, `insert into node_presets(name,protocol,kind,config_json,default_port,remark,builtin,enabled,created_at,updated_at) values(?,?,?,?,?,?,1,1,?,?)`,
			seed.Name, seed.Protocol, seed.Kind, seed.ConfigJSON, seed.DefaultPort, seed.Remark, ts, ts); err != nil {
			return restored, fmt.Errorf("恢复系统模板 %s 失败：%w", seed.Name, err)
		}
		restored++
	}
	return restored, nil
}

var nodePresetKinds = map[string]string{
	"vless-reality":        "vless",
	"vless-ws":             "vless",
	"vless-tcp":            "vless",
	"hy2-tls":              "hy2",
	"hy2-salamander":       "hy2",
	"anytls-basic":         "anytls",
	"anytls-large-padding": "anytls",
	"ss-aes-128-gcm":       "shadowsocks",
	"ss-aes-256-gcm":       "shadowsocks",
	"ss-2022-128":          "shadowsocks",
	"ss-2022-256":          "shadowsocks",
	"mieru-basic":          "mieru",
	"socks5-auth":          "socks",
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
	if strings.HasPrefix(kind, "hy2-") {
		sanitizeHY2NodePresetConfig(kind, merged)
	}
	if strings.HasPrefix(kind, "anytls-") {
		if err := core.ValidateAnyTLSPaddingScheme(merged["padding_scheme"]); err != nil {
			return "", err
		}
	}
	if err := core.ValidateListenTransportConfig(model.Protocol(nodePresetKinds[kind]), merged); err != nil {
		return "", err
	}
	if err := core.ValidateInboundConfigObject(model.Protocol(nodePresetKinds[kind]), merged); err != nil {
		return "", err
	}
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
