package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginpackage"
)

func runTool(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestExamplesCheckPackAndInstallAsTheControllerWould(t *testing.T) {
	entries, err := os.ReadDir("../../examples/plugins")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"minimal": false, "environment-demo": false, "external-api": false, "trace-monitor": false}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join("../../examples/plugins", entry.Name())
		if code, out, errOut := runTool(t, "check", dir); code != 0 {
			t.Fatalf("%s: check failed (%d): %s%s", entry.Name(), code, out, errOut)
		}
		output := filepath.Join(t.TempDir(), entry.Name()+".obplugin")
		if code, out, errOut := runTool(t, "pack", dir, output); code != 0 {
			t.Fatalf("%s: pack failed (%d): %s%s", entry.Name(), code, out, errOut)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		pkg, err := pluginpackage.Parse(data)
		if err != nil {
			t.Fatalf("%s: packed archive does not parse: %v", entry.Name(), err)
		}
		if pkg.SignatureState != "unsigned" || pkg.Manifest.Runtime != plugin.RuntimeJS {
			t.Fatalf("%s: unexpected package %+v", entry.Name(), pkg.Manifest)
		}
		if undeclared, _ := plugin.LintSource(pkg.Manifest, pkg.Source); len(undeclared) > 0 {
			t.Fatalf("%s: example uses undeclared capabilities %v", entry.Name(), undeclared)
		}
		want[entry.Name()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("example %s is missing", name)
		}
	}
}

func TestSignedPackCarriesThePublisherIdentity(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "publisher.pem")
	code, out, errOut := runTool(t, "keygen", key)
	if code != 0 {
		t.Fatalf("keygen: %s%s", out, errOut)
	}
	identity := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(out, "\n", 2)[0], "publisher identity"))
	if info, err := os.Stat(key); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key must be 0600: %v %v", info, err)
	}
	if code, _, _ := runTool(t, "keygen", key); code == 0 {
		t.Fatal("keygen must not overwrite an existing key")
	}
	output := filepath.Join(dir, "minimal.obplugin")
	code, out, errOut = runTool(t, "pack", "-key", key, "-publisher", "Example Co", "../../examples/plugins/minimal", output)
	if code != 0 {
		t.Fatalf("signed pack: %s%s", out, errOut)
	}
	data, _ := os.ReadFile(output)
	pkg, err := pluginpackage.Parse(data)
	if err != nil || pkg.SignatureState != "verified" || pkg.Publisher.Identity != identity || pkg.Publisher.Name != "Example Co" {
		t.Fatalf("signed package: %+v %v (identity %s)", pkg.Publisher, err, identity)
	}
	if code, out, _ := runTool(t, "inspect", output); code != 0 || !strings.Contains(out, identity) {
		t.Fatalf("inspect: %d %s", code, out)
	}
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runTool(t, "pack", "-key", key, "../../examples/plugins/minimal", output); code == 0 || !strings.Contains(errOut, "readable") {
		t.Fatalf("a group-readable key must be refused: %d %s", code, errOut)
	}
}

func TestCheckRefusesRetiredAndUndeclaredPlugins(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("manifest.json", `{"plugin_id":"acme.old","name":"old","version":"1.0.0","schema_version":1,"runtime":"oboard-js-v1","entry":"main","capabilities":["services.restart"]}`)
	write("main.js", `function main() {}`)
	if code, _, errOut := runTool(t, "check", dir); code != 1 || !strings.Contains(errOut, "retired") {
		t.Fatalf("retired manifest: %d %s", code, errOut)
	}
	write("manifest.json", `{"id":"acme.sneaky","name":"sneaky","version":"1.0.0","description":"","runtime":"oboard-js","entry":"main.js","capabilities":["state.read"],"triggers":{"schedule":false},"limits":{}}`)
	write("main.js", `function main() { return oboard.http.request({ url: 'https://example.com' }) }`)
	if code, _, errOut := runTool(t, "check", dir); code != 1 || !strings.Contains(errOut, "http.request") {
		t.Fatalf("undeclared capability: %d %s", code, errOut)
	}
	write("main.js", `const cp = require('child_process'); function main() { cp.execSync('id') }`)
	if code, _, _ := runTool(t, "pack", dir, filepath.Join(dir, "out.obplugin")); code == 0 {
		t.Fatal("pack must refuse a source that uses require")
	}
	if code, _, _ := runTool(t, "exec", "id"); code != 2 {
		t.Fatal("unknown subcommands must print usage")
	}
}
