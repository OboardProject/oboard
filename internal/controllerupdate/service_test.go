package controllerupdate

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedCheckPersistsStatus(t *testing.T) {
	root := t.TempDir()
	dockerRoot := filepath.Join(root, "docker")
	if err := os.MkdirAll(dockerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dockerRoot, ".env"), []byte("OBOARD_IMAGE=ghcr.io/oboardproject/oboard\nOBOARD_TAG=1.2.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "status.json")
	config := ServiceConfig{DockerRoot: dockerRoot, BinaryEnvPath: filepath.Join(root, "controller.env"), StatePath: statePath}
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

func TestStageFileReplacementRollbackAndCommit(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	rollback, _, err := stageFileReplacement(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, destination, "new")
	rollback()
	assertFileContent(t, destination, "old")

	rollback, commit, err := stageFileReplacement(source, destination)
	if err != nil {
		t.Fatal(err)
	}
	_ = rollback
	commit()
	assertFileContent(t, destination, "new")
	if _, err := os.Stat(destination + ".update-backup"); !os.IsNotExist(err) {
		t.Fatalf("backup remains after commit: %v", err)
	}
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != expected {
		t.Fatalf("%s = %q, want %q", path, content, expected)
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
