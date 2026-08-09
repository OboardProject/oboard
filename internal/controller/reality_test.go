package controller

import (
	"testing"
)

func TestApplyVLESSRealityDefaultsSynchronizesServerName(t *testing.T) {
	tests := []struct {
		name       string
		tlsName    string
		handshake  string
		wantServer string
	}{
		{name: "default", wantServer: defaultVLESSRealityServerName},
		{name: "tls name wins", tlsName: "front.example.com", handshake: "legacy.example.com", wantServer: "front.example.com"},
		{name: "handshake fallback", handshake: "front.example.com", wantServer: "front.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := map[string]any{
				"tls": map[string]any{
					"enabled":     true,
					"server_name": tt.tlsName,
					"reality": map[string]any{
						"enabled":   true,
						"handshake": map[string]any{"server": tt.handshake, "server_port": 443},
					},
				},
			}
			if err := applyVLESSRealityDefaults(cfg); err != nil {
				t.Fatal(err)
			}
			tls := cfg["tls"].(map[string]any)
			reality := tls["reality"].(map[string]any)
			handshake := reality["handshake"].(map[string]any)
			if got := tls["server_name"]; got != tt.wantServer {
				t.Fatalf("tls.server_name = %q, want %q", got, tt.wantServer)
			}
			if got := handshake["server"]; got != tt.wantServer {
				t.Fatalf("reality.handshake.server = %q, want %q", got, tt.wantServer)
			}
		})
	}
}

// TestApplyVLESSRealityDefaultsCompletesPanelPresetShape verifies the
// Controller-side default fill matches the panel's vless-reality preset and
// the chain-service template: Vision flow, tls.enabled, and handshake
// server_port are always present, and a keypair is generated when missing.
func TestApplyVLESSRealityDefaultsCompletesPanelPresetShape(t *testing.T) {
	cfg := map[string]any{
		"tls": map[string]any{
			"reality": map[string]any{
				"enabled": true,
			},
		},
	}
	if err := applyVLESSRealityDefaults(cfg); err != nil {
		t.Fatal(err)
	}
	tls := cfg["tls"].(map[string]any)
	if got, _ := tls["enabled"].(bool); !got {
		t.Fatal("tls.enabled was not defaulted to true")
	}
	if got, _ := cfg["flow"].(string); got != "xtls-rprx-vision" {
		t.Fatalf("flow = %q, want xtls-rprx-vision", got)
	}
	reality := tls["reality"].(map[string]any)
	handshake := reality["handshake"].(map[string]any)
	if got := handshake["server_port"]; got != 443 {
		t.Fatalf("handshake.server_port = %v, want 443", got)
	}
	if got := handshake["server"]; got != defaultVLESSRealityServerName {
		t.Fatalf("handshake.server = %q, want %q", got, defaultVLESSRealityServerName)
	}
	if got := tls["server_name"]; got != defaultVLESSRealityServerName {
		t.Fatalf("tls.server_name = %q, want %q", got, defaultVLESSRealityServerName)
	}
	if got := reality["private_key"]; got == "" || got == nil {
		t.Fatal("reality.private_key was not generated")
	}
	if got := reality["public_key"]; got == "" || got == nil {
		t.Fatal("reality.public_key was not generated")
	}
	if got := reality["short_id"]; got == "" || got == nil {
		t.Fatal("reality.short_id was not generated")
	}
}

// TestApplyVLESSRealityDefaultsKeepsExistingValues verifies explicit values
// (flow, tls.enabled, server_port, keypair) are never overwritten.
func TestApplyVLESSRealityDefaultsKeepsExistingValues(t *testing.T) {
	cfg := map[string]any{
		"flow": "xtls-rprx-vision",
		"tls": map[string]any{
			"enabled":     true,
			"server_name": "front.example.com",
			"reality": map[string]any{
				"enabled":     true,
				"private_key": "QQgMqPRP3R8xz_Bu1ejHvZIAZvHD21UkHjzpX2YODVU",
				"short_id":    "6adc1d56",
				"handshake": map[string]any{
					"server":      "front.example.com",
					"server_port": 8443,
				},
			},
		},
	}
	if err := applyVLESSRealityDefaults(cfg); err != nil {
		t.Fatal(err)
	}
	tls := cfg["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	handshake := reality["handshake"].(map[string]any)
	if got := handshake["server_port"]; got != 8443 {
		t.Fatalf("handshake.server_port = %v, want 8443 (kept)", got)
	}
	if got := reality["private_key"]; got != "QQgMqPRP3R8xz_Bu1ejHvZIAZvHD21UkHjzpX2YODVU" {
		t.Fatalf("private_key was rotated: %v", got)
	}
	if got := reality["public_key"]; got == "" {
		t.Fatal("public_key was not derived from the existing private key")
	}
	if got := reality["short_id"]; got != "6adc1d56" {
		t.Fatalf("short_id was rotated: %v", got)
	}
}
