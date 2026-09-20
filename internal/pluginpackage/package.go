// Package pluginpackage validates the bounded ZIP container for plugin code.
// Manifest and UI schemas, permissions, and JavaScript semantics belong to callers.
package pluginpackage

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	MaxUploadSize       = 8 << 20
	MaxUncompressedSize = 2 << 20
	MaxSourceSize       = 256 << 10
	MaxManifestSize     = 256 << 10
	MaxUISize           = 256 << 10
	MaxFiles            = 16
)

// Package contains the original, validated file bytes. UI is nil when absent.
// SHA256 hashes the domain "oboard-plugin-package-v1\x00", followed by each
// present file in manifest.json, main.js, ui.json order: its name, a NUL byte,
// its uint64 big-endian content length, and its raw contents. JSON whitespace
// is significant; ZIP order, compression, timestamps, and modes are not.
type Package struct {
	Manifest []byte
	Source   []byte
	UI       []byte
	SHA256   string
}

var fileNames = [...]string{"manifest.json", "main.js", "ui.json"}

func fileLimit(name string) (uint64, bool) {
	switch name {
	case "manifest.json":
		return MaxManifestSize, true
	case "main.js":
		return MaxSourceSize, true
	case "ui.json":
		return MaxUISize, true
	default:
		return 0, false
	}
}

// Parse validates an in-memory ZIP without extracting files to disk.
func Parse(data []byte) (*Package, error) {
	if len(data) > MaxUploadSize {
		return nil, fmt.Errorf("plugin package exceeds upload limit (%d bytes)", MaxUploadSize)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("invalid plugin ZIP: %w", err)
	}
	if len(r.File) > MaxFiles {
		return nil, fmt.Errorf("plugin package exceeds file count limit (%d)", MaxFiles)
	}
	files := make(map[string][]byte, len(fileNames))
	var total uint64
	for _, f := range r.File {
		limit, allowed := fileLimit(f.Name)
		if !allowed {
			return nil, fmt.Errorf("plugin package contains a disallowed file name")
		}
		if !f.Mode().IsRegular() || f.Flags&1 != 0 {
			return nil, fmt.Errorf("plugin file %s must be a regular unencrypted file", f.Name)
		}
		if _, exists := files[f.Name]; exists {
			return nil, fmt.Errorf("duplicate plugin file %s", f.Name)
		}
		if f.UncompressedSize64 > limit || f.UncompressedSize64 > MaxUncompressedSize-total {
			return nil, fmt.Errorf("plugin file %s exceeds uncompressed size limit", f.Name)
		}
		contents, err := readFile(f, limit)
		if err != nil {
			return nil, fmt.Errorf("invalid plugin file %s: %w", f.Name, err)
		}
		total += uint64(len(contents))
		if total > MaxUncompressedSize {
			return nil, fmt.Errorf("plugin package exceeds total uncompressed size limit")
		}
		files[f.Name] = contents
	}
	manifest, manifestPresent := files["manifest.json"]
	source, sourcePresent := files["main.js"]
	if !manifestPresent || !sourcePresent {
		return nil, fmt.Errorf("plugin package requires manifest.json and main.js")
	}
	return validate(manifest, source, files["ui.json"])
}

func readFile(f *zip.File, limit uint64) ([]byte, error) {
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	contents, readErr := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	closeErr := r.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if uint64(len(contents)) > limit {
		return nil, fmt.Errorf("uncompressed file exceeds limit")
	}
	return contents, nil
}

func validate(manifest, source, ui []byte) (*Package, error) {
	if len(manifest) > MaxManifestSize || len(source) > MaxSourceSize || len(ui) > MaxUISize {
		return nil, fmt.Errorf("plugin file exceeds size limit")
	}
	if len(manifest)+len(source)+len(ui) > MaxUncompressedSize {
		return nil, fmt.Errorf("plugin package exceeds total uncompressed size limit")
	}
	contents := [...][]byte{manifest, source, ui}
	for i, content := range contents {
		if i == 2 && content == nil {
			continue
		}
		if !utf8.Valid(content) {
			return nil, fmt.Errorf("plugin file %s is not valid UTF-8", fileNames[i])
		}
		if i != 1 && !json.Valid(content) {
			return nil, fmt.Errorf("plugin file %s is not valid JSON", fileNames[i])
		}
	}
	h := sha256.New()
	h.Write([]byte("oboard-plugin-package-v1\x00"))
	for i, content := range contents {
		if i == 2 && content == nil {
			continue
		}
		h.Write([]byte(fileNames[i]))
		h.Write([]byte{0})
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(content)))
		h.Write(size[:])
		h.Write(content)
	}
	return &Package{Manifest: manifest, Source: source, UI: ui, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// Build creates a deterministic ZIP after applying the same content validation
// as Parse. A nil UI omits ui.json; an empty but non-nil UI is invalid JSON.
// No manifest or JavaScript semantics are inferred or rewritten.
func Build(manifest, source, ui []byte) ([]byte, error) {
	if _, err := validate(manifest, source, ui); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	contents := [...][]byte{manifest, source, ui}
	for i, content := range contents {
		if i == 2 && content == nil {
			continue
		}
		header := &zip.FileHeader{Name: fileNames[i], Method: zip.Deflate}
		header.SetMode(0600)
		f, err := w.CreateHeader(header)
		if err != nil {
			return nil, fmt.Errorf("build plugin ZIP: %w", err)
		}
		if _, err := f.Write(content); err != nil {
			return nil, fmt.Errorf("build plugin ZIP: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("build plugin ZIP: %w", err)
	}
	if buf.Len() > MaxUploadSize {
		return nil, fmt.Errorf("plugin package exceeds upload limit")
	}
	return buf.Bytes(), nil
}
