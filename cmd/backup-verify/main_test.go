package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/backup"
	"github.com/OboardProject/oboard/internal/store"
)

func TestRunVerifiesSyntheticBackup(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "source.sqlite")
	source, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx := context.Background()
	if err := source.SetSetting(ctx, "offline-verification-sentinel", "unchanged"); err != nil {
		t.Fatal(err)
	}
	manager, err := backup.New(backup.Config{
		Root: filepath.Join(root, "backups"), DatabasePath: database,
		ACMEHome: filepath.Join(root, "acme"), MasterSecret: "synthetic-source-secret-for-cli-verification", SourceVersion: "1.2.3",
		Snapshot: func(ctx context.Context, dest string) error { return source.Backup(ctx, dest, store.BackupOptions{}) },
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(ctx, "synthetic-cli-password")
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "private-verification")
	if err := os.Mkdir(parent, 0700); err != nil {
		t.Fatal(err)
	}
	passwordPath := filepath.Join(root, "password")
	if err := os.WriteFile(passwordPath, []byte("synthetic-cli-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := os.Open(passwordPath)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	var output, diagnostic bytes.Buffer
	if code := run([]string{"-archive", created.Path, "-temp-parent", parent, "-target-version", "1.2.3"}, input, &output, &diagnostic); code != 0 {
		t.Fatalf("verification exit %d: %s", code, diagnostic.String())
	}
	var report backup.RestoreVerification
	if err := json.Unmarshal(output.Bytes(), &report); err != nil || !report.Verified {
		t.Fatalf("invalid verification report: %v", err)
	}
	if diagnostic.Len() != 0 || strings.Contains(output.String(), "synthetic-") {
		t.Fatal("unexpected diagnostic or secret in report")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("verification leftovers: %v", err)
	}
	settings, err := source.ListSettings(ctx)
	if err != nil || settings["offline-verification-sentinel"] != "unchanged" || settings["controller_backup_restore_reconcile"] != "" {
		t.Fatal("source database changed")
	}
}

func TestRunRejectsPasswordArgumentsAndRedactsFailures(t *testing.T) {
	root := t.TempDir()
	password := filepath.Join(root, "password")
	if err := os.WriteFile(password, []byte("synthetic-secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"-password", "synthetic-secret"},
		{"-archive", filepath.Join(root, "missing"), "-temp-parent", root, "-target-version", "1.2.3"},
	} {
		input, err := os.Open(password)
		if err != nil {
			t.Fatal(err)
		}
		var output, diagnostic bytes.Buffer
		code := run(args, input, &output, &diagnostic)
		input.Close()
		if code == 0 {
			t.Fatal("unexpected success")
		}
		if strings.Contains(output.String()+diagnostic.String(), "synthetic-secret") {
			t.Fatal("password leaked")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary leftovers: %v %v", entries, err)
	}
}
