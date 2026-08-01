package controller

import "testing"

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
