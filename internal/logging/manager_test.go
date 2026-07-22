package logging

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerRotateSnapshotRedactAndClear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.log")
	m, err := New(path, Config{MaxBytes: 80, Backups: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	for _, line := range []string{
		"first line\n",
		"authorization: Bearer abc.def\n",
		"password=unsafe-value\n",
		"fourth line long enough to force rotation 1234567890\n",
	} {
		if _, err := m.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := m.Snapshot(20, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snapshot.Content, "abc.def") || strings.Contains(snapshot.Content, "unsafe-value") {
		t.Fatalf("sensitive value leaked: %s", snapshot.Content)
	}
	if !strings.Contains(snapshot.Content, "[REDACTED]") || len(snapshot.Files) < 2 {
		t.Fatalf("rotation or redaction missing: %#v", snapshot)
	}
	filtered, err := m.Snapshot(20, "fourth")
	if err != nil || filtered.LineCount != 1 {
		t.Fatalf("filtered snapshot = %#v, %v", filtered, err)
	}
	var archive bytes.Buffer
	if err := m.WriteZIP(&archive); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil || len(zr.File) < 2 {
		t.Fatalf("zip files = %d, err = %v", len(zr.File), err)
	}
	for _, file := range zr.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, r)
		_ = r.Close()
	}
	if err := m.Clear(); err != nil {
		t.Fatal(err)
	}
	cleared, err := m.Snapshot(20, "")
	if err != nil || cleared.TotalSizeBytes != 0 || cleared.LineCount != 0 {
		t.Fatalf("cleared snapshot = %#v, %v", cleared, err)
	}
}

func TestManagerSingleWriteCannotExceedLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "controller.log")
	m, err := New(path, Config{MaxBytes: 64, Backups: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Write([]byte(strings.Repeat("x", 256))); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 64 {
		t.Fatalf("oversized single write produced %d bytes, want 64", info.Size())
	}
}
