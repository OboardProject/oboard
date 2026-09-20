package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/pluginpackage"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"manifest.json", "main.js", "ui.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "examples", "plugins", "node-inspection", name))
		if err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(dir, name), data)
	}
	return dir
}

func put(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPackCheckExampleAndIgnoreDevelopmentFiles(t *testing.T) {
	dir := fixture(t)
	if err := os.Mkdir(filepath.Join(dir, "tests"), 0700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "tests", "development.js"), []byte("not packaged"))
	put(t, filepath.Join(dir, "package.json"), []byte(`{"private":true}`))
	out := filepath.Join(t.TempDir(), "plugin.zip")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"pack", dir, out}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack exit=%d stderr=%s", code, &stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := pluginpackage.Parse(data)
	if err != nil || pkg.UI == nil {
		t.Fatalf("package=%+v err=%v", pkg, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(zr.File) != 3 {
		t.Fatalf("expected exactly three installation files: %v", err)
	}
	if !strings.Contains(stdout.String(), pkg.SHA256) || strings.Contains(stdout.String(), "function main") || stderr.Len() != 0 {
		t.Fatalf("unexpected output: stdout=%s stderr=%s", &stdout, &stderr)
	}
	stdout.Reset()
	if code := run([]string{"check", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("check exit=%d stderr=%s", code, &stderr)
	}
	if !strings.Contains(stdout.String(), pkg.SHA256) {
		t.Fatal("check summary does not match pack")
	}
}

func TestValidationCompilesWithoutExecutingOrLoadingSourceMaps(t *testing.T) {
	dir := fixture(t)
	if err := os.Remove(filepath.Join(dir, "ui.json")); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(dir, "main.js"), []byte("throw new Error('must not execute');\nfunction main() {}\n//# sourceMappingURL=/dev/zero"))
	data, err := buildDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := validate(data)
	if err != nil || pkg.UI != nil {
		t.Fatalf("package=%+v err=%v", pkg, err)
	}
}

func TestPackRejectsInvalidInputsWithoutWritingOutput(t *testing.T) {
	cases := []struct {
		name string
		file string
		data []byte
	}{
		{"syntax", "main.js", []byte("function main( { secret-in-source")},
		{"compiler", "main.js", []byte("break; function main() {}")},
		{"modules", "main.js", []byte("function main() { return require('secret-in-source'); }")},
		{"empty source", "main.js", []byte(" ")},
		{"source limit", "main.js", bytes.Repeat([]byte("a"), pluginpackage.MaxSourceSize+1)},
		{"manifest", "manifest.json", []byte(`{"secret-in-source":"hidden"}`)},
		{"manifest limit", "manifest.json", bytes.Repeat([]byte(" "), pluginpackage.MaxManifestSize+1)},
		{"ui", "ui.json", []byte(`{"pages":[],"secret-in-source":"hidden"}`)},
		{"empty ui", "ui.json", []byte{}},
		{"ui limit", "ui.json", bytes.Repeat([]byte(" "), pluginpackage.MaxUISize+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixture(t)
			put(t, filepath.Join(dir, tc.file), tc.data)
			out := filepath.Join(t.TempDir(), "bad.zip")
			var stdout, stderr bytes.Buffer
			if code := run([]string{"pack", dir, out}, &stdout, &stderr); code != 1 {
				t.Fatalf("exit=%d", code)
			}
			if stdout.Len() != 0 || stderr.Len() == 0 || strings.Contains(stderr.String(), "secret-in-source") {
				t.Fatalf("unexpected output: stdout=%s stderr=%s", &stdout, &stderr)
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatalf("invalid pack created output: %v", err)
			}
		})
	}
}

func TestPackRejectsMissingNonRegularAndSymlinkInputs(t *testing.T) {
	for _, name := range []string{"manifest.json", "main.js", "ui.json"} {
		for _, kind := range []string{"missing", "directory", "symlink"} {
			if name == "ui.json" && kind == "missing" {
				continue
			}
			t.Run(name+"/"+kind, func(t *testing.T) {
				dir := fixture(t)
				path := filepath.Join(dir, name)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "directory":
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				case "symlink":
					target := filepath.Join(t.TempDir(), "target")
					put(t, target, []byte("{}"))
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := buildDirectory(dir); err == nil {
					t.Fatal("unsafe input accepted")
				}
			})
		}
	}
	dir := fixture(t)
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := buildDirectory(link); err == nil {
		t.Fatal("symlink project accepted")
	}
}

func TestPackNeverOverwritesInputsOrExistingOutput(t *testing.T) {
	dir := fixture(t)
	existing := filepath.Join(t.TempDir(), "existing.zip")
	put(t, existing, []byte("keep"))
	link := filepath.Join(t.TempDir(), "link.zip")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "manifest.json"), filepath.Join(dir, "main.js"), filepath.Join(dir, "ui.json"), existing, link} {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if code := run([]string{"pack", dir, path}, &stdout, &stderr); code != 1 || stderr.Len() == 0 {
			t.Fatalf("pack overwrite exit=%d stderr=%s", code, &stderr)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("input changed")
		}
	}
}

func TestCheckRejectsUntrustedArchives(t *testing.T) {
	var unknown bytes.Buffer
	zw := zip.NewWriter(&unknown)
	f, err := zw.Create("../secret-in-source")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("secret-in-source")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{[]byte("not ZIP"), unknown.Bytes(), make([]byte, pluginpackage.MaxUploadSize+1)} {
		path := filepath.Join(t.TempDir(), "bad.zip")
		put(t, path, data)
		var stdout, stderr bytes.Buffer
		if code := run([]string{"check", path}, &stdout, &stderr); code != 1 || stdout.Len() != 0 || stderr.Len() == 0 || strings.Contains(stderr.String(), "secret-in-source") {
			t.Fatalf("exit=%d stdout=%s stderr=%s", code, &stdout, &stderr)
		}
	}
}

func TestUsageRequiresExplicitOutput(t *testing.T) {
	for _, args := range [][]string{nil, {"pack"}, {"pack", "project"}, {"check"}, {"check", "x", "y"}, {"unknown"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage:") {
			t.Fatalf("args=%v exit=%d stdout=%s stderr=%s", args, code, &stdout, &stderr)
		}
	}
}
