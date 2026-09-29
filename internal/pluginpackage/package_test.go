package pluginpackage

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/plugin"
)

const testManifest = `{"id":"acme.minimal","name":"Minimal","version":"1.0.0","description":"","runtime":"oboard-js","entry":"main.js","capabilities":[],"triggers":{"schedule":false}}`

func files() map[string][]byte {
	return map[string][]byte{
		plugin.ManifestFile: []byte(testManifest),
		plugin.EntryFile:    []byte("function main(run) { log.info('hi'); return { ok: true } }"),
	}
}

type entry struct {
	name    string
	body    []byte
	mode    uint32
	method  uint16
	declare uint64
}

func archive(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.method != 0 {
			header.Method = e.method
		}
		if e.mode != 0 {
			header.SetMode(0)
			header.ExternalAttrs = e.mode << 16
		}
		writer, err := w.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = writer.Write(e.body)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func baseEntries() []entry {
	return []entry{{name: plugin.ManifestFile, body: []byte(testManifest)}, {name: plugin.EntryFile, body: []byte("function main() { return 1 }")}}
}

func TestPackageRoundTripAndDeterministicDigest(t *testing.T) {
	data, err := Build(files())
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Manifest.ID != "acme.minimal" || pkg.SignatureState != "unsigned" || pkg.Publisher.Identity != "local" || len(pkg.SHA256) != 64 {
		t.Fatalf("unexpected package: %+v", pkg)
	}
	reordered := archive(t, []entry{{name: plugin.EntryFile, body: files()[plugin.EntryFile]}, {name: plugin.ManifestFile, body: files()[plugin.ManifestFile], method: zip.Store}})
	other, err := Parse(reordered)
	if err != nil || other.SHA256 != pkg.SHA256 {
		t.Fatalf("digest depends on archive layout: %v", err)
	}
}

func TestPackageRejectsUnsafeArchives(t *testing.T) {
	cases := map[string][]entry{
		"traversal":        append(baseEntries(), entry{name: "../evil.js", body: []byte("x")}),
		"absolute":         append(baseEntries(), entry{name: "/etc/passwd", body: []byte("x")}),
		"subdirectory":     append(baseEntries(), entry{name: "lib/x.js", body: []byte("x")}),
		"backslash":        append(baseEntries(), entry{name: "..\\x", body: []byte("x")}),
		"symlink":          append(baseEntries(), entry{name: LicenseFile, body: []byte("/etc/passwd"), mode: 0o120777}),
		"duplicate":        append(baseEntries(), entry{name: plugin.EntryFile, body: []byte("function main(){}")}),
		"install.sh":       append(baseEntries(), entry{name: "install.sh", body: []byte("#!/bin/sh")}),
		"postinstall":      append(baseEntries(), entry{name: "package.json", body: []byte(`{"scripts":{"postinstall":"x"}}`)}),
		"native so":        append(baseEntries(), entry{name: "addon.so", body: []byte("\x7fELF")}),
		"native node":      append(baseEntries(), entry{name: "addon.node", body: []byte("\x7fELF")}),
		"node_modules":     append(baseEntries(), entry{name: "node_modules", body: []byte("x")}),
		"missing entry":    {{name: plugin.ManifestFile, body: []byte(testManifest)}},
		"missing manifest": {{name: plugin.EntryFile, body: []byte("function main(){}")}},
		"elf main":         {{name: plugin.ManifestFile, body: []byte(testManifest)}, {name: plugin.EntryFile, body: []byte("\x7fELF\x02\x01")}},
		"bomb":             {{name: plugin.ManifestFile, body: []byte(testManifest)}, {name: plugin.EntryFile, body: bytes.Repeat([]byte(" "), plugin.MaxSourceBytes+1)}},
		"no main":          {{name: plugin.ManifestFile, body: []byte(testManifest)}, {name: plugin.EntryFile, body: []byte("function start(){}")}},
		"syntax":           {{name: plugin.ManifestFile, body: []byte(testManifest)}, {name: plugin.EntryFile, body: []byte("function main( {")}},
		"module import":    {{name: plugin.ManifestFile, body: []byte(testManifest)}, {name: plugin.EntryFile, body: []byte("import fs from 'fs'\nfunction main(){}")}},
		"bad icon":         append(baseEntries(), entry{name: IconFile, body: []byte("<svg onload=alert(1)>")}),
	}
	for name, entries := range cases {
		if _, err := Parse(archive(t, entries)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse(bytes.Repeat([]byte("x"), MaxArchiveBytes+1)); err == nil {
		t.Fatal("oversized archive accepted")
	}
	many := baseEntries()
	for i := 0; i < MaxEntries; i++ {
		many = append(many, entry{name: "LICENSE", body: []byte("x")})
	}
	if _, err := Parse(archive(t, many)); err == nil {
		t.Fatal("too many entries accepted")
	}
}

func TestPublisherSignature(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	set := files()
	signature, err := Sign(set, "Acme", private)
	if err != nil {
		t.Fatal(err)
	}
	set[SignatureFile] = signature
	pkg, err := Validate(set)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.SignatureState != "verified" || pkg.Publisher.Identity != PublisherIdentity(public) || pkg.Publisher.Name != "Acme" {
		t.Fatalf("signature not verified: %+v", pkg.Publisher)
	}
	tampered := map[string][]byte{}
	for name, content := range set {
		tampered[name] = content
	}
	tampered[plugin.EntryFile] = []byte("function main() { return 'changed' }")
	if _, err := Validate(tampered); plugin.CodeOf(err) != plugin.CodeSignatureInvalid {
		t.Fatalf("tampered package accepted: %v", err)
	}
	var doc map[string]string
	_ = json.Unmarshal(signature, &doc)
	doc["publisher"] = "Someone Else"
	forged, _ := json.Marshal(doc)
	set[SignatureFile] = forged
	if pkg, err := Validate(set); err != nil || pkg.Publisher.Identity != PublisherIdentity(public) {
		t.Fatalf("publisher identity must come from the key, not the display name: %+v %v", pkg, err)
	}
	set[SignatureFile] = []byte(strings.Replace(string(signature), `"format": "oboard-plugin-signature-v1"`, `"format": "other"`, 1))
	if _, err := Validate(set); plugin.CodeOf(err) != plugin.CodeSignatureInvalid {
		t.Fatalf("unknown signature format accepted: %v", err)
	}
}
