package controllerupdate

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateAvailableByChannel(t *testing.T) {
	manifest := Manifest{Version: "1.2.0", Build: "22", Commit: "new"}
	tests := []struct {
		name, channel string
		current       BuildInfo
		want          bool
	}{
		{"stable newer", "stable", BuildInfo{Version: "1.1.9"}, true},
		{"stable current", "stable", BuildInfo{Version: "1.2.0"}, false},
		{"stable does not downgrade", "stable", BuildInfo{Version: "2.0.0"}, false},
		{"stable replaces prerelease", "stable", BuildInfo{Version: "1.2.0-rc.1"}, true},
		{"stable replaces development", "stable", BuildInfo{Version: "dev-old"}, true},
		{"dev commit changed", "dev", BuildInfo{Version: "dev", Build: "22", Commit: "old"}, true},
		{"dev current", "dev", BuildInfo{Version: "dev", Build: "22", Commit: "new"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := updateAvailable(test.channel, test.current, manifest); got != test.want {
				t.Fatalf("updateAvailable() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateManifestRequiresEveryLinuxArchitecture(t *testing.T) {
	manifest := Manifest{Schema: ManifestSchema, Channel: "stable", Version: "1.2.0", Build: "22", Commit: "abc", Date: "2026-07-24T00:00:00Z"}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "oboard_controller_1.2.0_linux_" + arch + ".tar.gz"
		manifest.Artifacts = append(manifest.Artifacts, Artifact{Name: name, OS: "linux", Arch: arch, SHA256: strings.Repeat("a", 64), Size: 10})
	}
	assets := map[string]string{}
	for _, artifact := range manifest.Artifacts {
		assets[artifact.Name] = "https://example.invalid/" + artifact.Name
	}
	if err := validateManifest(manifest, "stable", assets); err != nil {
		t.Fatal(err)
	}
	manifest.Artifacts = manifest.Artifacts[:1]
	if err := validateManifest(manifest, "stable", assets); err == nil {
		t.Fatal("manifest without arm64 artifact was accepted")
	}
}

func TestExtractControllerArchiveRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bad.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../../etc/shadow", Mode: 0o600, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractControllerArchive(archive, t.TempDir()); err == nil {
		t.Fatal("archive traversal was accepted")
	}
}

func TestDetectInstallation(t *testing.T) {
	root := t.TempDir()
	dockerRoot := filepath.Join(root, "docker")
	if err := os.MkdirAll(dockerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	service := NewService(ServiceConfig{DockerRoot: dockerRoot, BinaryEnvPath: binaryEnv, ControllerBinary: filepath.Join(root, "oboard-controller"), StatePath: filepath.Join(root, "status.json")})
	if err := os.WriteFile(filepath.Join(dockerRoot, ".env"), []byte("OBOARD_IMAGE=ghcr.io/oboardproject/oboard\nOBOARD_TAG=dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	method, channel, _, detectionError := service.detectInstallation()
	if method != "docker" || channel != "dev" {
		t.Fatalf("got %s/%s", method, channel)
	}
	if detectionError != "" {
		t.Fatal(detectionError)
	}
	if err := os.WriteFile(filepath.Join(dockerRoot, ".env"), []byte("OBOARD_IMAGE=example.invalid/oboard\nOBOARD_TAG=latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, channel, command, _ := service.detectInstallation()
	if channel != "pinned" || !strings.Contains(command, officialImage) {
		t.Fatalf("custom image got channel=%s command=%q", channel, command)
	}
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_INSTALL_METHOD=binary\nOBOARD_UPDATE_CHANNEL=stable\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	method, channel, _, detectionError = service.detectInstallation()
	if method != "" || channel != "" || detectionError == "" {
		t.Fatalf("conflicting installations got method=%q channel=%q error=%q", method, channel, detectionError)
	}
}

func TestValidateDockerImageLabels(t *testing.T) {
	expected := BuildInfo{Version: "1.2.0", Commit: "abc123"}
	labels := map[string]string{
		"org.opencontainers.image.source":   "https://github.com/OboardProject/oboard",
		"org.opencontainers.image.version":  expected.Version,
		"org.opencontainers.image.revision": expected.Commit,
	}
	if err := validateDockerImageLabels(labels, expected); err != nil {
		t.Fatal(err)
	}
	labels["org.opencontainers.image.revision"] = "other"
	if err := validateDockerImageLabels(labels, expected); err == nil {
		t.Fatal("mismatched Docker image commit was accepted")
	}
}
