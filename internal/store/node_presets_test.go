package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

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
	kinds := map[string]bool{}
	for _, preset := range presets {
		if !preset.Builtin {
			t.Fatalf("seed preset %q must be builtin", preset.Name)
		}
		if err := NormalizeNodePreset(&preset); err != nil {
			t.Fatalf("seed preset %q invalid: %v", preset.Name, err)
		}
		kinds[preset.Kind] = true
	}
	for _, kind := range []string{"vless-reality", "hy2-tls", "anytls-basic", "ss-2022-128", "mieru-basic", "socks5-auth"} {
		if !kinds[kind] {
			t.Fatalf("missing builtin kind %s", kind)
		}
	}
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
