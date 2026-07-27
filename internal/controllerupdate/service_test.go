package controllerupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedCheckPersistsStatus(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "status.json")
	config := ServiceConfig{BinaryEnvPath: binaryEnv, ControllerBinary: binary, StatePath: statePath}
	service := NewService(config)
	status, err := service.check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "pinned" || status.UpdateAvailable {
		t.Fatalf("unexpected pinned status: %#v", status)
	}
	restored := NewService(config)
	if restored.status.State != "pinned" || restored.status.LastCheckedAt == "" {
		t.Fatalf("status was not restored: %#v", restored.status)
	}
}

func TestDefaultServiceConfigUsesSelectedInstallDirectory(t *testing.T) {
	t.Setenv("OBOARD_INSTALL_DIR", "/data/oboard/")
	config := DefaultServiceConfig()
	if config.ControllerBinary != "/data/oboard/oboard-controller" || config.UpdaterBinary != "/data/oboard/oboard-controller-updater" {
		t.Fatalf("unexpected custom binary paths: %#v", config)
	}

	t.Setenv("OBOARD_INSTALL_DIR", "../tmp/unsafe")
	config = DefaultServiceConfig()
	if config.ControllerBinary != "/usr/local/bin/oboard-controller" || config.UpdaterBinary != "/usr/local/bin/oboard-controller-updater" {
		t.Fatalf("unsafe install directory was accepted: %#v", config)
	}
}

func TestNormalizeInstallDir(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "/data/oboard", want: "/data/oboard"},
		{input: "/data/oboard/", want: "/data/oboard"},
		{input: "/usr/local/bin", want: "/usr/local/bin"},
	} {
		got, ok := normalizeInstallDir(test.input)
		if !ok || got != test.want {
			t.Errorf("normalizeInstallDir(%q) = %q, %v; want %q, true", test.input, got, ok, test.want)
		}
	}
	for _, input := range []string{"", "/", "data/oboard", "/data//oboard", "/data/../etc", "/data/oboard path", "/data/oboard;rm"} {
		if got, ok := normalizeInstallDir(input); ok {
			t.Errorf("normalizeInstallDir(%q) = %q, true; want rejection", input, got)
		}
	}
}

func TestValidateStagedBuild(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "controller")
	script := "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"1.2.0\",\"build\":\"22\",\"commit\":\"abc\",\"date\":\"2026-07-24T00:00:00Z\"}'\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{Version: "1.2.0", Build: "22", Commit: "abc", Date: "2026-07-24T00:00:00Z"}
	if err := validateStagedBuild(context.Background(), binary, manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Commit = "other"
	if err := validateStagedBuild(context.Background(), binary, manifest); err == nil {
		t.Fatal("mismatched staged Controller metadata was accepted")
	}
}
