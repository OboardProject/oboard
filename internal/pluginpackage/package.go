// Package pluginpackage reads, validates, builds and signs OBoard plugin
// packages (.obplugin). A package is a small ZIP with a fixed set of root
// files; it never contains install scripts, native code or dependencies.
package pluginpackage

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"

	"github.com/OboardProject/oboard/internal/plugin"
)

const (
	Extension         = ".obplugin"
	SignatureFile     = "signature.json"
	IconFile          = "icon.png"
	ReadmeFile        = "README.md"
	LicenseFile       = "LICENSE"
	MaxArchiveBytes   = 2 << 20
	MaxEntries        = 8
	MaxTotalBytes     = 1 << 20
	maxIconBytes      = 64 << 10
	maxTextFileBytes  = 64 << 10
	maxSignatureBytes = 8 << 10
	signatureFormat   = "oboard-plugin-signature-v1"
	digestDomain      = "oboard-plugin-package\n"
)

var fileLimits = map[string]int{
	plugin.ManifestFile: plugin.MaxManifestBytes,
	plugin.EntryFile:    plugin.MaxSourceBytes,
	IconFile:            maxIconBytes,
	ReadmeFile:          maxTextFileBytes,
	LicenseFile:         maxTextFileBytes,
	SignatureFile:       maxSignatureBytes,
}

// FileLimit reports whether name may appear in a package and its size limit.
func FileLimit(name string) (int, bool) {
	limit, ok := fileLimits[name]
	return limit, ok
}

// Package is a fully validated package. Nothing in it has been executed.
type Package struct {
	Manifest       plugin.Manifest
	ManifestJSON   []byte
	Source         string
	Icon           []byte
	Readme         string
	License        string
	SHA256         string
	Publisher      Publisher
	SignatureState string
	Files          map[string][]byte
}

type Publisher struct {
	Identity string `json:"identity"`
	Name     string `json:"name"`
}

type signatureDocument struct {
	Format    string `json:"format"`
	Publisher string `json:"publisher"`
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

func invalid(message string) error { return plugin.Fail(plugin.CodeInvalidPackage, message) }

// Parse reads an archive in memory and validates every entry before any of
// it is used. Rejections cover path traversal, absolute paths, links,
// directories, duplicate names, encryption, unsupported compression,
// oversized or bomb-like entries, and any file outside the allowed set.
func Parse(data []byte) (*Package, error) {
	if len(data) == 0 || len(data) > MaxArchiveBytes {
		return nil, invalid("package must be a non-empty archive of at most 2 MiB")
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, invalid("package is not a valid ZIP archive")
	}
	if len(reader.File) == 0 || len(reader.File) > MaxEntries {
		return nil, invalid("package must contain between 1 and 8 files")
	}
	files := map[string][]byte{}
	total := 0
	for _, entry := range reader.File {
		name, err := checkEntry(entry)
		if err != nil {
			return nil, err
		}
		if _, dup := files[name]; dup {
			return nil, invalid("duplicate file " + name)
		}
		limit := fileLimits[name]
		content, err := readEntry(entry, limit)
		if err != nil {
			return nil, err
		}
		total += len(content)
		if total > MaxTotalBytes {
			return nil, invalid("package content exceeds 1 MiB")
		}
		files[name] = content
	}
	return Validate(files)
}

func checkEntry(entry *zip.File) (string, error) {
	name := entry.Name
	switch {
	case name == "" || len(name) > 64:
		return "", invalid("invalid file name")
	case strings.ContainsAny(name, `/\:`) || strings.Contains(name, ".."):
		return "", invalid("package files must be at the archive root: " + safeName(name))
	case path.IsAbs(name) || strings.HasPrefix(name, "~"):
		return "", invalid("absolute paths are not allowed")
	case !utf8.ValidString(name) || strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r > 0x7e }):
		return "", invalid("file names must be printable ASCII")
	}
	mode := entry.Mode()
	if mode.Type() != 0 {
		return "", invalid("links, directories and special files are not allowed: " + safeName(name))
	}
	if entry.Flags&0x1 != 0 {
		return "", invalid("encrypted entries are not allowed")
	}
	if entry.Method != zip.Store && entry.Method != zip.Deflate {
		return "", invalid("unsupported compression in " + safeName(name))
	}
	if _, allowed := fileLimits[name]; !allowed {
		return "", invalid(forbiddenReason(name))
	}
	if entry.UncompressedSize64 > uint64(fileLimits[name]) {
		return "", invalid(name + " is too large")
	}
	return name, nil
}

func forbiddenReason(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".sh") || strings.HasSuffix(lower, ".bash") || lower == "install" || lower == "setup":
		return "install and setup scripts are not allowed: " + name
	case strings.HasSuffix(lower, ".so") || strings.HasSuffix(lower, ".dll") || strings.HasSuffix(lower, ".dylib") || strings.HasSuffix(lower, ".node") || strings.HasSuffix(lower, ".wasm") || strings.HasSuffix(lower, ".exe"):
		return "native code is not allowed: " + name
	case lower == "package.json" || lower == "package-lock.json" || lower == "yarn.lock" || lower == "pnpm-lock.yaml":
		return "dependency manifests are not allowed; bundle the plugin into main.js: " + name
	default:
		return "unexpected file " + safeName(name) + "; allowed: manifest.json, main.js, icon.png, README.md, LICENSE, signature.json"
	}
}

func safeName(name string) string {
	if len(name) > 64 {
		name = name[:64]
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '?'
		}
		return r
	}, name)
}

func readEntry(entry *zip.File, limit int) ([]byte, error) {
	rc, err := entry.Open()
	if err != nil {
		return nil, invalid("cannot read " + safeName(entry.Name))
	}
	defer rc.Close()
	content, err := io.ReadAll(io.LimitReader(rc, int64(limit)+1))
	if err != nil {
		return nil, invalid("corrupt entry " + safeName(entry.Name))
	}
	// The declared size is attacker-controlled; the limited reader is what
	// actually stops a decompression bomb.
	if len(content) > limit || uint64(len(content)) != entry.UncompressedSize64 {
		return nil, invalid(entry.Name + " is too large or its size does not match")
	}
	return content, nil
}

// Validate checks an in-memory file set: required files, manifest, source
// syntax, native-binary markers and the optional publisher signature.
func Validate(files map[string][]byte) (*Package, error) {
	manifestRaw, ok := files[plugin.ManifestFile]
	if !ok {
		return nil, invalid("manifest.json is missing")
	}
	sourceRaw, ok := files[plugin.EntryFile]
	if !ok {
		return nil, invalid("main.js is missing")
	}
	for name, content := range files {
		if name == IconFile {
			continue
		}
		if looksNative(content) {
			return nil, invalid("native binaries are not allowed: " + name)
		}
	}
	manifest, err := plugin.ParseManifest(manifestRaw)
	if err != nil {
		return nil, err
	}
	source := string(sourceRaw)
	if err := CheckSource(source); err != nil {
		return nil, err
	}
	pkg := &Package{Manifest: manifest, ManifestJSON: manifest.CanonicalJSON(), Source: source, Files: files, SignatureState: "unsigned", Publisher: Publisher{Identity: "local", Name: ""}}
	if icon, ok := files[IconFile]; ok {
		if !bytes.HasPrefix(icon, []byte("\x89PNG\r\n\x1a\n")) {
			return nil, invalid("icon.png must be a PNG image")
		}
		pkg.Icon = icon
	}
	for _, name := range []string{ReadmeFile, LicenseFile} {
		if text, ok := files[name]; ok {
			if !utf8.Valid(text) {
				return nil, invalid(name + " must be UTF-8 text")
			}
			if name == ReadmeFile {
				pkg.Readme = string(text)
			} else {
				pkg.License = string(text)
			}
		}
	}
	digest := ContentDigest(files)
	pkg.SHA256 = hex.EncodeToString(digest[:])
	if raw, ok := files[SignatureFile]; ok {
		publisher, err := verifySignature(raw, digest)
		if err != nil {
			return nil, err
		}
		pkg.Publisher = publisher
		pkg.SignatureState = "verified"
	}
	return pkg, nil
}

func looksNative(content []byte) bool {
	for _, magic := range [][]byte{{0x7f, 'E', 'L', 'F'}, {'M', 'Z'}, {0xcf, 0xfa, 0xed, 0xfe}, {0xce, 0xfa, 0xed, 0xfe}, {0xfe, 0xed, 0xfa, 0xcf}, {0xca, 0xfe, 0xba, 0xbe}, {0x00, 'a', 's', 'm'}} {
		if bytes.HasPrefix(content, magic) {
			return true
		}
	}
	return false
}

// CheckSource compiles (never runs) the bundle and requires a top-level main.
func CheckSource(source string) error {
	if strings.TrimSpace(source) == "" {
		return plugin.FailField(plugin.CodeInvalidPackage, "main.js", "main.js is empty")
	}
	if len(source) > plugin.MaxSourceBytes {
		return plugin.FailField(plugin.CodeInvalidPackage, "main.js", "main.js exceeds 512 KiB")
	}
	if !utf8.ValidString(source) || strings.ContainsRune(source, 0) {
		return plugin.FailField(plugin.CodeInvalidPackage, "main.js", "main.js must be UTF-8 text")
	}
	// Source-map comments must never make validation read host files.
	program, err := parser.ParseFile(nil, plugin.EntryFile, source, 0, parser.WithDisableSourceMaps)
	if err != nil {
		return plugin.FailField(plugin.CodeInvalidPackage, "main.js", "syntax error: "+firstLine(err.Error()))
	}
	if _, err := goja.CompileAST(program, true); err != nil {
		return plugin.FailField(plugin.CodeInvalidPackage, "main.js", "compile error: "+firstLine(err.Error()))
	}
	if loc := moduleLoader.FindStringIndex(source); loc != nil {
		return plugin.FailField(plugin.CodeInvalidPackage, "main.js", "require, import and importScripts are not available: the runtime has no modules, use the oboard SDK")
	}
	if !declaresMain(program) {
		return plugin.FailField(plugin.CodeInvalidPackage, "main.js", "main.js must declare a top-level function main(run)")
	}
	return nil
}

// moduleLoader matches a call to a module loader. The runtime defines none of
// them, so such a plugin could only fail at run time.
var moduleLoader = regexp.MustCompile(`(?:^|[^.\w$])(?:require|importScripts|import)\s*\(`)

func declaresMain(program *ast.Program) bool {
	for _, statement := range program.Body {
		switch node := statement.(type) {
		case *ast.FunctionDeclaration:
			if node.Function != nil && node.Function.Name != nil && node.Function.Name.Name == "main" {
				return true
			}
		case *ast.VariableStatement:
			for _, binding := range node.List {
				if id, ok := binding.Target.(*ast.Identifier); ok && id.Name == "main" {
					return true
				}
			}
		case *ast.LexicalDeclaration:
			for _, binding := range node.List {
				if id, ok := binding.Target.(*ast.Identifier); ok && id.Name == "main" {
					return true
				}
			}
		}
	}
	return false
}

func firstLine(text string) string {
	text, _, _ = strings.Cut(text, "\n")
	if len(text) > 240 {
		text = text[:240]
	}
	return text
}

// ContentDigest is the package identity: every file except signature.json,
// in name order, length-prefixed. ZIP timestamps and ordering do not matter.
func ContentDigest(files map[string][]byte) [32]byte {
	names := make([]string, 0, len(files))
	for name := range files {
		if name != SignatureFile {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	h := sha256.New()
	h.Write([]byte(digestDomain))
	for _, name := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(files[name]))
		h.Write(files[name])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

func signatureMessage(digest [32]byte) []byte {
	return []byte(signatureFormat + "\n" + hex.EncodeToString(digest[:]))
}

// PublisherIdentity is the stable identity of a publisher key.
func PublisherIdentity(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return "ed25519:" + hex.EncodeToString(sum[:])
}

func verifySignature(raw []byte, digest [32]byte) (Publisher, error) {
	var doc signatureDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil || doc.Format != signatureFormat {
		return Publisher{}, plugin.Fail(plugin.CodeSignatureInvalid, "signature.json is not a valid signature document")
	}
	publisherName := strings.TrimSpace(doc.Publisher)
	if publisherName == "" || len(publisherName) > 80 || strings.ContainsFunc(publisherName, func(r rune) bool { return r < 0x20 }) {
		return Publisher{}, plugin.Fail(plugin.CodeSignatureInvalid, "signature publisher name is invalid")
	}
	public, err := base64.StdEncoding.DecodeString(doc.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return Publisher{}, plugin.Fail(plugin.CodeSignatureInvalid, "signature public key is invalid")
	}
	signature, err := base64.StdEncoding.DecodeString(doc.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Publisher{}, plugin.Fail(plugin.CodeSignatureInvalid, "signature value is invalid")
	}
	if !ed25519.Verify(ed25519.PublicKey(public), signatureMessage(digest), signature) {
		return Publisher{}, plugin.Fail(plugin.CodeSignatureInvalid, "the package signature does not match its content")
	}
	return Publisher{Identity: PublisherIdentity(ed25519.PublicKey(public)), Name: publisherName}, nil
}

// Sign produces signature.json for a file set with an Ed25519 publisher key.
func Sign(files map[string][]byte, publisherName string, key ed25519.PrivateKey) ([]byte, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key")
	}
	digest := ContentDigest(files)
	doc := signatureDocument{
		Format:    signatureFormat,
		Publisher: strings.TrimSpace(publisherName),
		PublicKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, signatureMessage(digest))),
	}
	return json.MarshalIndent(doc, "", "  ")
}

// Build writes a deterministic archive of the given files.
func Build(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		if _, ok := fileLimits[name]; !ok {
			return nil, fmt.Errorf("file %s is not allowed in a plugin package", name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: epoch}
		header.SetMode(0o644)
		w, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if _, err := Parse(buf.Bytes()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
