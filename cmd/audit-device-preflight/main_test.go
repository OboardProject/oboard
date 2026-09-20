package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestPreflightCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-name.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out bytes.Buffer
	if code := run([]string{"-database", path}, &out); code != 0 {
		t.Fatalf("code=%d output=%s", code, out.String())
	}
	var result store.DeviceRetirementPreflight
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.NodeRevocationConfirmed {
		t.Fatal("unproven revocation")
	}
	out.Reset()
	if code := run([]string{"-database", path + "-missing"}, &out); code != 1 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(out.String(), path) {
		t.Fatal("error disclosed path")
	}
}
