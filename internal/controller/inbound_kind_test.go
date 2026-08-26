package controller

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestApplyInboundKindHY2Defaults(t *testing.T) {
	standard := model.Inbound{Kind: "hy2-tls", Protocol: model.ProtocolHY2, ConfigJSON: `{}`}
	if err := applyInboundKindDefaults(&standard, nil); err != nil {
		t.Fatal(err)
	}
	var standardCfg map[string]any
	if err := json.Unmarshal([]byte(standard.ConfigJSON), &standardCfg); err != nil {
		t.Fatal(err)
	}
	if standardCfg["up_mbps"] != float64(1000) || standardCfg["down_mbps"] != float64(500) {
		t.Fatalf("standard hy2 bandwidth = %#v", standardCfg)
	}
	if _, exists := standardCfg["obfs"]; exists {
		t.Fatalf("standard hy2 must not keep obfs: %#v", standardCfg)
	}

	salamander := model.Inbound{Kind: "hy2-salamander", Protocol: model.ProtocolHY2, ConfigJSON: `{"obfs":{"type":"salamander"}}`}
	if err := applyInboundKindDefaults(&salamander, nil); err != nil {
		t.Fatal(err)
	}
	var salamanderCfg map[string]any
	if err := json.Unmarshal([]byte(salamander.ConfigJSON), &salamanderCfg); err != nil {
		t.Fatal(err)
	}
	if salamanderCfg["up_mbps"] != float64(1000) || salamanderCfg["down_mbps"] != float64(500) {
		t.Fatalf("salamander hy2 bandwidth = %#v", salamanderCfg)
	}
	obfs, _ := salamanderCfg["obfs"].(map[string]any)
	if obfs == nil || obfs["type"] != "salamander" {
		t.Fatalf("salamander obfs = %#v", salamanderCfg["obfs"])
	}
	password, _ := obfs["password"].(string)
	if strings.TrimSpace(password) == "" {
		t.Fatal("salamander password must be generated")
	}

	keep := model.Inbound{Kind: "hy2-salamander", Protocol: model.ProtocolHY2, ConfigJSON: `{"up_mbps":200,"down_mbps":80,"obfs":{"type":"salamander","password":"keep-me"}}`}
	if err := applyInboundKindDefaults(&keep, nil); err != nil {
		t.Fatal(err)
	}
	var keepCfg map[string]any
	if err := json.Unmarshal([]byte(keep.ConfigJSON), &keepCfg); err != nil {
		t.Fatal(err)
	}
	if keepCfg["up_mbps"] != float64(200) || keepCfg["down_mbps"] != float64(80) {
		t.Fatalf("existing bandwidth overwritten: %#v", keepCfg)
	}
	keepObfs, _ := keepCfg["obfs"].(map[string]any)
	if keepObfs["password"] != "keep-me" {
		t.Fatalf("existing salamander password overwritten: %#v", keepObfs)
	}
}

func TestInferredInboundKindHY2Salamander(t *testing.T) {
	if got := inferredInboundKind(model.Inbound{Protocol: model.ProtocolHY2, ConfigJSON: `{"tls":{"enabled":true}}`}); got != "hy2-tls" {
		t.Fatalf("standard kind = %q", got)
	}
	if got := inferredInboundKind(model.Inbound{Protocol: model.ProtocolHY2, ConfigJSON: `{"obfs":{"type":"salamander","password":"x"}}`}); got != "hy2-salamander" {
		t.Fatalf("salamander kind = %q", got)
	}
}

func TestMergeInboundPresetConfigSkipsHY2BandwidthAndObfsPassword(t *testing.T) {
	preset := map[string]any{
		"tls":      map[string]any{"enabled": true},
		"up_mbps":  100,
		"down_mbps": 100,
		"obfs":     map[string]any{"type": "salamander", "password": "from-preset"},
	}
	inbound := map[string]any{
		"up_mbps":  1000,
		"down_mbps": 500,
		"obfs":     map[string]any{"type": "salamander", "password": "from-inbound"},
	}
	merged := mergeInboundPresetConfig(preset, inbound)
	if merged["up_mbps"] != 1000 || merged["down_mbps"] != 500 {
		t.Fatalf("inbound bandwidth should win, got %#v", merged)
	}
	obfs, _ := merged["obfs"].(map[string]any)
	if obfs["type"] != "salamander" || obfs["password"] != "from-inbound" {
		t.Fatalf("obfs merge = %#v", obfs)
	}

	created := mergeInboundPresetConfig(preset, map[string]any{"tls": map[string]any{"enabled": true}})
	if _, exists := created["up_mbps"]; exists {
		t.Fatalf("preset bandwidth leaked into inbound: %#v", created)
	}
	createdObfs, _ := created["obfs"].(map[string]any)
	if _, exists := createdObfs["password"]; exists {
		t.Fatalf("preset salamander password leaked: %#v", createdObfs)
	}
}
