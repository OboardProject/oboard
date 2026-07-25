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

func TestExtractControllerArchiveAcceptsSelfUpdatePayload(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "controller.tar.gz")
	writeTestControllerArchive(t, archive, []archiveEntry{
		{name: "bin/oboard-controller", content: "controller"},
		{name: "bin/oboard-controller-updater", content: "updater"},
		{name: "web/dist/index.html", content: "web"},
		{name: "downloads/release-manifest.json", content: "{}"},
	})
	stage := t.TempDir()
	if err := extractControllerArchive(archive, stage); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin/oboard-controller", "bin/oboard-controller-updater", "web/dist/index.html", "downloads/release-manifest.json"} {
		if info, err := os.Stat(filepath.Join(stage, filepath.FromSlash(name))); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("self-update payload did not extract %s: %v", name, err)
		}
	}
}

func TestExtractControllerArchiveRejectsInstallerPayload(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "controller-install.tar.gz")
	writeTestControllerArchive(t, archive, []archiveEntry{{
		name:    "deploy/systemd/oboard-controller-updater.service",
		content: "unit",
	}})
	if err := extractControllerArchive(archive, t.TempDir()); err == nil {
		t.Fatal("privileged updater accepted an installation-only archive member")
	}
}

type archiveEntry struct {
	name    string
	content string
}

func writeTestControllerArchive(t *testing.T, path string, entries []archiveEntry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
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
}

func TestDetectInstallation(t *testing.T) {
	root := t.TempDir()
	binaryEnv := filepath.Join(root, "controller.env")
	binary := filepath.Join(root, "oboard-controller")
	service := NewService(ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: filepath.Join(root, "status.json")})
	if channel, _, detectionError := service.detectInstallation(); channel != "" || detectionError == "" {
		t.Fatalf("missing binary got channel=%q error=%q", channel, detectionError)
	}
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if channel, _, detectionError := service.detectInstallation(); channel != "dev" || detectionError != "" {
		t.Fatalf("development binary got channel=%q error=%q", channel, detectionError)
	}
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if channel, command, detectionError := service.detectInstallation(); channel != "pinned" || !strings.Contains(command, "OBOARD_UPDATE_CHANNEL=stable") || detectionError != "" {
		t.Fatalf("pinned binary got channel=%q command=%q error=%q", channel, command, detectionError)
	}
}
