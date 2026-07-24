package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestBackupCreatesReadableSnapshot(t *testing.T) {
	root := t.TempDir()
	db, err := Open(filepath.Join(root, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(context.Background(), "backup-test", "present"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "backups", "snapshot.sqlite")
	if err := db.Backup(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	settings, err := snapshot.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings["backup-test"] != "present" {
		t.Fatalf("snapshot setting = %q", settings["backup-test"])
	}
}
