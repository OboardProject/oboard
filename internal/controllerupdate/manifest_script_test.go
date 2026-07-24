package controllerupdate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestGenerateControllerManifestScript(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "generate-controller-manifest.py")
	output := t.TempDir()
	for _, arch := range []string{"amd64", "arm64"} {
		name := "oboard_controller_1.2.3_linux_" + arch + ".tar.gz"
		if err := os.WriteFile(filepath.Join(output, name), []byte("package-"+arch), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("python3", script, output, "stable", "1.2.3", "22", "abc123", "2026-07-24T00:00:00Z")
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generate manifest: %v\n%s", err, result)
	}
	data, err := os.ReadFile(filepath.Join(output, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	assets := map[string]string{}
	for _, artifact := range manifest.Artifacts {
		assets[artifact.Name] = "present"
	}
	if err := validateManifest(manifest, "stable", assets); err != nil {
		t.Fatal(err)
	}
}
