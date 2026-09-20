package pluginpackage

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"io/fs"
	"strings"
	"testing"
	"time"
)

type entry struct {
	name string
	data []byte
	mode fs.FileMode
}

func archive(t *testing.T, entries []entry, method uint16, modified time.Time) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: method, Modified: modified}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(h.Name, "/") {
			if _, err := f.Write(e.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func required() []entry {
	return []entry{{name: "manifest.json", data: []byte(`{"id":"example"}`)}, {name: "main.js", data: []byte(`function main() { return "ok"; }`)}}
}

func TestRoundTrip(t *testing.T) {
	manifest := []byte("{\n  \"id\": \"example\"\n}\n")
	source := []byte("// 中文\nfunction main() { return 42; }\n")
	for _, ui := range [][]byte{nil, []byte(`{"pages":[]}`)} {
		data, err := Build(manifest, source, ui)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got.Manifest, manifest) || !bytes.Equal(got.Source, source) || !bytes.Equal(got.UI, ui) || (got.UI == nil) != (ui == nil) {
			t.Fatalf("original contents not preserved: %+v", got)
		}
		if len(got.SHA256) != 64 {
			t.Fatalf("invalid SHA256: %q", got.SHA256)
		}
		again, err := Build(manifest, source, ui)
		if err != nil || !bytes.Equal(data, again) {
			t.Fatalf("build is not deterministic: %v", err)
		}
	}
}

func TestRejectUnsafeEntries(t *testing.T) {
	for _, name := range []string{"../main.js", "/main.js", "dir/../main.js", "dir/main.js", `dir\main.js`, `C:\main.js`, "./main.js", "main.js/", "main.js\x00", "MAIN.JS", "asset.png", ""} {
		t.Run(name, func(t *testing.T) {
			entries := append(required(), entry{name: name, data: []byte("x")})
			if _, err := Parse(archive(t, entries, zip.Store, time.Time{})); err == nil {
				t.Fatalf("accepted unsafe file name %q", name)
			}
		})
	}
	for _, mode := range []fs.FileMode{fs.ModeSymlink | 0600, fs.ModeDir | 0700, fs.ModeNamedPipe | 0600, fs.ModeSocket | 0600, fs.ModeDevice | 0600} {
		entries := required()
		entries[1].mode = mode
		if _, err := Parse(archive(t, entries, zip.Store, time.Time{})); err == nil {
			t.Fatalf("accepted non-regular file mode %v", mode)
		}
	}
}

func TestRejectMissingDuplicateAndExcessFiles(t *testing.T) {
	for _, entries := range [][]entry{nil, required()[:1], required()[1:], append(required(), required()[0]), append(required(), required()[1]), append(required(), entry{name: "ui.json", data: []byte(`{}`)}, entry{name: "ui.json", data: []byte(`{}`)})} {
		if _, err := Parse(archive(t, entries, zip.Store, time.Time{})); err == nil {
			t.Fatalf("accepted missing or duplicate entries: %+v", entries)
		}
	}
	entries := required()
	for len(entries) <= MaxFiles {
		entries = append(entries, entry{name: "ui.json", data: []byte(`{}`)})
	}
	if _, err := Parse(archive(t, entries, zip.Store, time.Time{})); err == nil || !strings.Contains(err.Error(), "file count") {
		t.Fatalf("file-count check did not run: %v", err)
	}
}

func TestContentValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		file int
		data []byte
	}{
		{"manifest-json", 0, []byte(`{"id":`)},
		{"manifest-empty", 0, []byte{}},
		{"manifest-trailing", 0, []byte(`{} {}`)},
		{"manifest-utf8", 0, []byte{'"', 0xff, '"'}},
		{"source-utf8", 1, []byte{0xff}},
		{"ui-json", 2, []byte(`{`)},
		{"ui-empty", 2, []byte{}},
		{"ui-utf8", 2, []byte{'"', 0xff, '"'}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := append(required(), entry{name: "ui.json", data: []byte(`{}`)})
			entries[tc.file].data = tc.data
			if _, err := Parse(archive(t, entries, zip.Deflate, time.Time{})); err == nil {
				t.Fatal("Parse accepted invalid content")
			}
			if _, err := Build(entries[0].data, entries[1].data, entries[2].data); err == nil {
				t.Fatal("Build accepted invalid content")
			}
		})
	}
	// Only presence, not JavaScript semantics or manifest schema, is enforced.
	data, err := Build([]byte(`null`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(data); err != nil {
		t.Fatal(err)
	}
}

func TestSizeLimitsAndCompressionBomb(t *testing.T) {
	entries := []entry{
		{name: "manifest.json", data: append([]byte(`{}`), bytes.Repeat([]byte(" "), MaxManifestSize-2)...)},
		{name: "main.js", data: bytes.Repeat([]byte(" "), MaxSourceSize)},
		{name: "ui.json", data: append([]byte(`{}`), bytes.Repeat([]byte(" "), MaxUISize-2)...)},
	}
	if _, err := Parse(archive(t, entries, zip.Deflate, time.Time{})); err != nil {
		t.Fatalf("exact limits rejected: %v", err)
	}
	if _, err := Build(entries[0].data, entries[1].data, entries[2].data); err != nil {
		t.Fatalf("exact limits rejected by Build: %v", err)
	}
	for i := range entries {
		over := append([]entry(nil), entries...)
		over[i].data = append(bytes.Clone(over[i].data), ' ')
		data := archive(t, over, zip.Deflate, time.Time{})
		if len(data) > 4096 {
			t.Fatalf("fixture should be highly compressed: %d", len(data))
		}
		if _, err := Parse(data); err == nil {
			t.Fatalf("accepted compressed oversized %s", over[i].name)
		}
		if _, err := Build(over[0].data, over[1].data, over[2].data); err == nil {
			t.Fatalf("Build accepted oversized %s", over[i].name)
		}
	}
	bomb := required()
	bomb[1].data = bytes.Repeat([]byte(" "), MaxUncompressedSize+1)
	if _, err := Parse(archive(t, bomb, zip.Deflate, time.Time{})); err == nil {
		t.Fatal("accepted compressed total-size bomb")
	}
	data := archive(t, required(), zip.Store, time.Time{})
	// A valid ZIP can carry a large executable prefix; this exercises the upload
	// check independently of ZIP syntax and uncompressed-file restrictions.
	exact := append(make([]byte, MaxUploadSize-len(data)), data...)
	if _, err := Parse(exact); err != nil {
		t.Fatalf("exact upload limit rejected: %v", err)
	}
	if _, err := Parse(append([]byte{0}, exact...)); err == nil || !strings.Contains(err.Error(), "upload limit") {
		t.Fatalf("upload limit not enforced: %v", err)
	}
}

func TestDigestIndependentOfZIPMetadata(t *testing.T) {
	entries := append(required(), entry{name: "ui.json", data: []byte(`{"pages":[]}`)})
	original, err := Parse(archive(t, entries, zip.Store, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	reordered := []entry{entries[2], entries[1], entries[0]}
	for i := range reordered {
		reordered[i].mode = 0644
	}
	other, err := Parse(archive(t, reordered, zip.Deflate, time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if original.SHA256 != other.SHA256 {
		t.Fatal("digest depends on ZIP order or metadata")
	}
	for i := range entries {
		changed := append([]entry(nil), entries...)
		changed[i].data = append(bytes.Clone(changed[i].data), ' ')
		p, err := Parse(archive(t, changed, zip.Store, time.Time{}))
		if err != nil || p.SHA256 == original.SHA256 {
			t.Fatalf("digest failed to bind %s: %v", entries[i].name, err)
		}
	}
	withoutUI, err := Parse(archive(t, required(), zip.Store, time.Time{}))
	if err != nil || withoutUI.SHA256 == original.SHA256 {
		t.Fatalf("digest failed to bind optional UI: %v", err)
	}
}

func TestMalformedArchive(t *testing.T) {
	valid := archive(t, required(), zip.Store, time.Time{})
	corrupt := bytes.Clone(valid)
	start := bytes.Index(corrupt, required()[1].data)
	corrupt[start] ^= 1
	for _, data := range [][]byte{nil, []byte("not a zip"), valid[:len(valid)-1], corrupt} {
		if _, err := Parse(data); err == nil {
			t.Fatal("accepted invalid or corrupted archive")
		}
	}
}

func TestDishonestUncompressedSize(t *testing.T) {
	entries := required()
	entries[1].data = bytes.Repeat([]byte(" "), MaxSourceSize+1)
	data := archive(t, entries, zip.Deflate, time.Time{})
	// Lie in the central directory to bypass the early advertised-size check.
	pos := 0
	for {
		i := bytes.Index(data[pos:], []byte{'P', 'K', 1, 2})
		if i < 0 {
			t.Fatal("missing central-directory entry")
		}
		pos += i
		nameLen := int(binary.LittleEndian.Uint16(data[pos+28:]))
		if string(data[pos+46:pos+46+nameLen]) == "main.js" {
			binary.LittleEndian.PutUint32(data[pos+24:], 1)
			break
		}
		pos += 46 + nameLen
	}
	if _, err := Parse(data); err == nil {
		t.Fatal("accepted oversized stream with dishonest size header")
	}
}
