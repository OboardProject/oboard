package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestNodePresetsSeededAndProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	presets, err := s.ListNodePresets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) < 8 {
		t.Fatalf("expected seeded node presets, got %d", len(presets))
	}
	kinds := map[string]model.NodePreset{}
	for _, preset := range presets {
		if !preset.Builtin {
			t.Fatalf("seed preset %q must be builtin", preset.Name)
		}
		if err := NormalizeNodePreset(&preset); err != nil {
			t.Fatalf("seed preset %q invalid: %v", preset.Name, err)
		}
		kinds[preset.Kind] = preset
	}
	for _, kind := range []string{"vless-reality", "hy2-tls", "hy2-salamander", "anytls-basic", "anytls-large-padding", "ss-2022-128", "mieru-basic", "socks5-auth"} {
		if _, exists := kinds[kind]; !exists {
			t.Fatalf("missing builtin kind %s", kind)
		}
	}
	assertNodePresetPadding(t, kinds["anytls-basic"], core.AnyTLSBalancedPaddingScheme())
	assertNodePresetPadding(t, kinds["anytls-large-padding"], core.AnyTLSLargePaddingScheme())
	assertHY2PresetShape(t, kinds["hy2-tls"], false)
	assertHY2PresetShape(t, kinds["hy2-salamander"], true)
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.ListNodePresets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != len(presets) {
		t.Fatalf("seed count changed after reopen: %d -> %d", len(presets), len(again))
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNodePresetsCRUDAndReferenceGuard(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	preset := model.NodePreset{Name: "机房 Reality", Protocol: "vless", Kind: "vless-reality", ConfigJSON: `{"flow":"xtls-rprx-vision","tls":{"enabled":true,"server_name":"www.cloudflare.com","reality":{"enabled":true,"handshake":{"server":"www.cloudflare.com","server_port":443}}}}`, DefaultPort: 443, Enabled: true}
	if err := s.CreateNodePreset(ctx, &preset); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNodePreset(ctx, preset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "机房 Reality" || got.Kind != "vless-reality" {
		t.Fatalf("unexpected preset %#v", got)
	}
	got.Remark = "自定义握手"
	if err := s.UpdateNodePreset(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetNodePreset(ctx, preset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Remark != "自定义握手" {
		t.Fatalf("remark not saved: %q", after.Remark)
	}
	if err := s.DeleteNodePreset(ctx, after.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNodePreset(ctx, after.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected deleted preset, got %v", err)
	}
	builtin := firstBuiltinNodePreset(t, s)
	if err := s.DeleteNodePreset(ctx, builtin.ID); err == nil {
		t.Fatal("expected builtin delete to fail")
	}
}

func TestNodePresetRejectsUnknownRealityFieldBeforeSave(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	preset := model.NodePreset{
		Name: "Invalid Reality", Protocol: "vless", Kind: "vless-reality",
		ConfigJSON:  `{"tls":{"reality":{"enabled":true,"dest":"gateway.icloud.com:443"}}}`,
		DefaultPort: 443, Enabled: true,
	}
	err = s.CreateNodePreset(context.Background(), &preset)
	if err == nil || !strings.Contains(err.Error(), "config_json.tls.reality.dest: unsupported field") {
		t.Fatalf("error = %v, want precise Reality field path", err)
	}
	items, listErr := s.ListNodePresets(context.Background())
	if listErr != nil {
		t.Fatal(listErr)
	}
	for _, item := range items {
		if item.Name == preset.Name {
			t.Fatalf("invalid node preset was persisted: %#v", item)
		}
	}
}

func TestNodePresetsMigrateFromPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table app_settings (key text primary key, value text not null, updated_at text not null)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	presets, err := s.ListNodePresets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(presets) == 0 {
		t.Fatal("expected migrate to seed node presets")
	}
}

func TestNodePresetUsageCountsInboundReference(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	preset := model.NodePreset{Name: "引用 Reality", Protocol: "vless", Kind: "vless-reality", Enabled: true}
	if err := s.CreateNodePreset(ctx, &preset); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateServer(ctx, &model.Server{Name: "edge", AgentID: "agent-preset-1", ChainSecret: "chain-secret", ListenIP: "0.0.0.0", Status: model.ServerOnline}); err != nil {
		t.Fatal(err)
	}
	servers, err := s.ListServers(ctx)
	if err != nil || len(servers) == 0 {
		t.Fatalf("server: %v", err)
	}
	inbound := model.Inbound{ServerID: servers[0].ID, Name: "vless-in", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{"node_preset_id":` + strconv.FormatInt(preset.ID, 10) + `}`, Enabled: true}
	if err := s.CreateInbound(ctx, &inbound); err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListNodePresets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var usage int64
	for _, item := range listed {
		if item.ID == preset.ID {
			usage = item.UsageCount
		}
	}
	if usage != 1 {
		t.Fatalf("usage=%d", usage)
	}
	if err := s.DeleteNodePreset(ctx, preset.ID); err == nil {
		t.Fatal("expected referenced delete to fail")
	}
}

func TestNormalizeNodePresetMergesKindDefaults(t *testing.T) {
	preset := model.NodePreset{Name: "自定义 Reality", Protocol: "vless", Kind: "vless-reality", ConfigJSON: `{"tls":{"server_name":"www.cloudflare.com"}}`}
	if err := NormalizeNodePreset(&preset); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(preset.ConfigJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["flow"] != "xtls-rprx-vision" {
		t.Fatalf("expected default flow, got %#v", cfg)
	}
	tls, _ := cfg["tls"].(map[string]any)
	if tls["server_name"] != "www.cloudflare.com" {
		t.Fatalf("custom SNI not kept: %#v", tls)
	}
	reality, _ := tls["reality"].(map[string]any)
	if reality["enabled"] != true {
		t.Fatalf("expected default reality block: %#v", reality)
	}
	if preset.DefaultPort != 443 {
		t.Fatalf("default port=%d", preset.DefaultPort)
	}
}

func TestAnyTLSPaddingPresetsMigrateFromLegacyBuiltin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table node_presets (id integer primary key autoincrement, name text not null unique, protocol text not null, kind text not null, config_json text not null default '{}', default_port integer not null default 443, remark text not null default '', builtin integer not null default 0, enabled integer not null default 1, created_at text not null, updated_at text not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into node_presets(name,protocol,kind,config_json,default_port,remark,builtin,enabled,created_at,updated_at) values(?,?,?,?,?,?,1,1,?,?)`, legacyAnyTLSBasicPresetName, "anytls", "anytls-basic", legacyAnyTLSBasicPresetConfig, 443, legacyAnyTLSBasicPresetRemark, builtinNodePresetSeedTimestamp, builtinNodePresetSeedTimestamp); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ListNodePresets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string][]model.NodePreset{}
	for _, item := range items {
		byKind[item.Kind] = append(byKind[item.Kind], item)
	}
	if len(byKind["anytls-basic"]) != 1 {
		t.Fatalf("legacy migration created duplicate anytls-basic presets: %#v", byKind["anytls-basic"])
	}
	if len(byKind["anytls-large-padding"]) != 1 {
		t.Fatalf("large-padding preset count = %d", len(byKind["anytls-large-padding"]))
	}
	if byKind["anytls-basic"][0].Name != "AnyTLS 均衡填充" {
		t.Fatalf("legacy preset name = %q", byKind["anytls-basic"][0].Name)
	}
	assertNodePresetPadding(t, byKind["anytls-basic"][0], core.AnyTLSBalancedPaddingScheme())
	assertNodePresetPadding(t, byKind["anytls-large-padding"][0], core.AnyTLSLargePaddingScheme())
	count := len(items)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.ListNodePresets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != count {
		t.Fatalf("padding preset migration is not idempotent: %d -> %d", count, len(again))
	}
}

func TestNormalizeNodePresetRejectsInvalidAnyTLSPadding(t *testing.T) {
	preset := model.NodePreset{Name: "坏填充", Protocol: "anytls", Kind: "anytls-basic", ConfigJSON: `{"padding_scheme":["stop=2","2=64-128"]}`, DefaultPort: 443}
	if err := NormalizeNodePreset(&preset); err == nil {
		t.Fatal("expected invalid AnyTLS padding to fail")
	}
}

func TestHY2PresetsMigrateFromLegacyBuiltin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table node_presets (id integer primary key autoincrement, name text not null unique, protocol text not null, kind text not null, config_json text not null default '{}', default_port integer not null default 443, remark text not null default '', builtin integer not null default 0, enabled integer not null default 1, created_at text not null, updated_at text not null)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into node_presets(name,protocol,kind,config_json,default_port,remark,builtin,enabled,created_at,updated_at) values(?,?,?,?,?,?,1,1,?,?)`, legacyHY2TLSPresetName, "hy2", "hy2-tls", legacyHY2TLSPresetConfig, 443, legacyHY2TLSPresetRemark, builtinNodePresetSeedTimestamp, builtinNodePresetSeedTimestamp); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.ListNodePresets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string][]model.NodePreset{}
	for _, item := range items {
		byKind[item.Kind] = append(byKind[item.Kind], item)
	}
	if len(byKind["hy2-tls"]) != 1 {
		t.Fatalf("legacy migration created duplicate hy2-tls presets: %#v", byKind["hy2-tls"])
	}
	if len(byKind["hy2-salamander"]) != 1 {
		t.Fatalf("salamander preset count = %d", len(byKind["hy2-salamander"]))
	}
	if byKind["hy2-tls"][0].Name != "Hysteria2 标准" {
		t.Fatalf("legacy preset name = %q", byKind["hy2-tls"][0].Name)
	}
	assertHY2PresetShape(t, byKind["hy2-tls"][0], false)
	assertHY2PresetShape(t, byKind["hy2-salamander"][0], true)
	count := len(items)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, err := reopened.ListNodePresets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != count {
		t.Fatalf("hy2 preset migration is not idempotent: %d -> %d", count, len(again))
	}
}

func TestNormalizeNodePresetStripsHY2BandwidthAndSecrets(t *testing.T) {
	standard := model.NodePreset{Name: "自定义 HY2", Protocol: "hy2", Kind: "hy2-tls", ConfigJSON: `{"tls":{"enabled":true},"up_mbps":100,"down_mbps":100,"obfs":{"type":"salamander","password":"preset-secret"}}`}
	if err := NormalizeNodePreset(&standard); err != nil {
		t.Fatal(err)
	}
	assertHY2PresetShape(t, standard, false)

	salamander := model.NodePreset{Name: "自定义 Salamander", Protocol: "hy2", Kind: "hy2-salamander", ConfigJSON: `{"up_mbps":50,"down_mbps":20,"obfs":{"type":"salamander","password":"preset-secret"}}`}
	if err := NormalizeNodePreset(&salamander); err != nil {
		t.Fatal(err)
	}
	assertHY2PresetShape(t, salamander, true)
}

func assertNodePresetPadding(t *testing.T, preset model.NodePreset, want []string) {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal([]byte(preset.ConfigJSON), &config); err != nil {
		t.Fatal(err)
	}
	raw, ok := config["padding_scheme"].([]any)
	if !ok || len(raw) != len(want) {
		t.Fatalf("preset %s padding_scheme = %#v, want %#v", preset.Kind, config["padding_scheme"], want)
	}
	for index, item := range raw {
		if item != want[index] {
			t.Fatalf("preset %s padding_scheme[%d] = %#v, want %q", preset.Kind, index, item, want[index])
		}
	}
}

func firstBuiltinNodePreset(t *testing.T, s *Store) model.NodePreset {
	t.Helper()
	items, err := s.ListNodePresets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Builtin {
			return item
		}
	}
	t.Fatal("no builtin preset")
	return model.NodePreset{}
}

func assertHY2PresetShape(t *testing.T, preset model.NodePreset, salamander bool) {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal([]byte(preset.ConfigJSON), &config); err != nil {
		t.Fatal(err)
	}
	if _, exists := config["up_mbps"]; exists {
		t.Fatalf("preset %s must not store up_mbps: %#v", preset.Kind, config)
	}
	if _, exists := config["down_mbps"]; exists {
		t.Fatalf("preset %s must not store down_mbps: %#v", preset.Kind, config)
	}
	obfs, _ := config["obfs"].(map[string]any)
	if !salamander {
		if obfs != nil {
			t.Fatalf("preset %s must not store obfs: %#v", preset.Kind, config)
		}
		return
	}
	if obfs == nil || obfs["type"] != "salamander" {
		t.Fatalf("preset %s obfs = %#v, want salamander type", preset.Kind, config["obfs"])
	}
	if _, exists := obfs["password"]; exists {
		t.Fatalf("preset %s must not store salamander password: %#v", preset.Kind, obfs)
	}
}
