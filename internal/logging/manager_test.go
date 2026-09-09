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

func TestRedactNetworkErrorsAcrossSinks(t *testing.T) {
	raw := `telegram getUpdates: Get "https://api.telegram.org/bot123456:synthetic-token/getUpdates?offset=42": context deadline exceeded; Get "https://operator:synthetic-password@example.com/package?X-Amz-Signature=synthetic-signature": EOF`
	check := func(t *testing.T, got string) {
		t.Helper()
		for _, secret := range []string{"synthetic-token", "synthetic-password", "synthetic-signature"} {
			if strings.Contains(got, secret) {
				t.Fatalf("credential leaked: %s", got)
			}
		}
		for _, want := range []string{"getUpdates", "context deadline exceeded", "example.com/package", "EOF"} {
			if !strings.Contains(got, want) {
				t.Fatalf("lost diagnostic context %q: %s", want, got)
			}
		}
	}
	var stdout bytes.Buffer
	if n, err := NewRedactingWriter(&stdout).Write([]byte(raw)); err != nil || n != len(raw) {
		t.Fatalf("write: %d %v", n, err)
	}
	check(t, stdout.String())
	path := filepath.Join(t.TempDir(), "controller.log")
	// Seed logs from a release that did not redact URL credentials.
	for _, name := range []string{path, path + ".1"} {
		if err := os.WriteFile(name, []byte(raw+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New(path, Config{Backups: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	snapshot, err := m.Snapshot(20, "")
	if err != nil {
		t.Fatal(err)
	}
	check(t, snapshot.Content)
	var archive bytes.Buffer
	if err := m.WriteZIP(&archive); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != 2 {
		t.Fatalf("files: %d", len(zr.File))
	}
	for _, entry := range zr.File {
		f, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		check(t, string(content))
	}
	if _, err := m.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	check(t, string(content[len(raw)+1:]))
}

func TestRedactURLVariants(t *testing.T) {
	for _, raw := range []string{
		"https://api.telegram.org/bot123456%3Asynthetic-token/getUpdates",
		"https://api.telegram.org/file/bot123456:synthetic-token/document/file.bin",
		"https://example.com/?token=synthetic-token&next=value",
	} {
		got := Redact(raw)
		if strings.Contains(got, "synthetic-token") {
			t.Fatalf("credential leaked: %s", got)
		}
		if twice := Redact(got); twice != got {
			t.Fatalf("redaction is not idempotent: %q -> %q", got, twice)
		}
	}
}
