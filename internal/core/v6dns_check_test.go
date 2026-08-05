package core

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// TestV6OnlyDNSConfigPassesSingBoxCheck verifies the generated DNS config for
// an IPv6-only server (only IPv6 bootstrap resolvers, prefer_ipv6 strategy)
// is accepted by the real kernel binary.
func TestV6OnlyDNSConfigPassesSingBoxCheck(t *testing.T) {
	bin, oboardSB := configuredSingBoxCheckBinary()
	if bin == "" {
		t.Skip("set OBOARD_SB_BIN or SING_BOX_BIN to run a sing-box check")
	}
	dns, err := BuildDNSConfig(model.Server{ID: 1, Name: "v6", IPStack: model.IPStackIPv6Only}, nil)
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"log": map[string]any{"level": "warn"}, "dns": dns, "inbounds": []map[string]any{}, "outbounds": []map[string]any{{"type": "direct", "tag": "direct"}}, "route": map[string]any{"final": "direct"}}
	b, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"check", "-c", path}
	if oboardSB {
		args = []string{"-check", "-config", path}
	}
	cmd := exec.Command(bin, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sing-box check failed: %v\n%s\nconfig:\n%s", err, out, b)
	}
}
