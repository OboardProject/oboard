package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceRetirementReadOnlyPreviousDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`drop table device_retirement_reviews`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	specialPath := filepath.Join(filepath.Dir(path), "previous ?#.sqlite")
	if err := os.Rename(path, specialPath); err != nil {
		t.Fatal(err)
	}
	path = specialPath
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := PreviewDeviceRetirementDatabase(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReviewAccounts != 0 || result.NodeRevocationConfirmed {
		t.Fatalf("unexpected review result: %+v", result)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("preflight modified database")
	}
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	if _, err := PreviewDeviceRetirementDatabase(context.Background(), missing); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("preflight created a database")
	}
}
