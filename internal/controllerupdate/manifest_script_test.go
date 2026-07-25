package controllerupdate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateControllerManifestScript(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	script := filepath.Join(filepath.Dir(file), "..", "..", "scripts", "generate-controller-manifest.py")
	tests := []struct {
		name, channel, version, artifactVersion string
	}{
		{name: "stable", channel: "stable", version: "1.2.3", artifactVersion: "1.2.3"},
		{name: "development", channel: "dev", version: "dev-abc123", artifactVersion: "dev"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := t.TempDir()
			for _, arch := range []string{"amd64", "arm64"} {
				name := "oboard_controller_" + test.artifactVersion + "_linux_" + arch + ".tar.gz"
				if err := os.WriteFile(filepath.Join(output, name), []byte("package-"+arch), 0o600); err != nil {
					t.Fatal(err)
				}
				installName := "oboard_controller_" + test.artifactVersion + "_linux_" + arch + "_install.tar.gz"
				if err := os.WriteFile(filepath.Join(output, installName), []byte("installer-"+arch), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.Command("python3", script, output, test.channel, test.version, "22", "abc123", "2026-07-24T00:00:00Z")
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
			if len(manifest.Artifacts) != 2 {
				t.Fatalf("manifest contains %d artifacts, want only the two self-update packages", len(manifest.Artifacts))
			}
			assets := map[string]string{}
			for _, artifact := range manifest.Artifacts {
				if strings.Contains(artifact.Name, "_install.tar.gz") {
					t.Fatalf("installer archive %q must not be accepted by the privileged updater", artifact.Name)
				}
				assets[artifact.Name] = "present"
			}
			if err := validateManifest(manifest, test.channel, assets); err != nil {
				t.Fatal(err)
			}
		})
	}
}
