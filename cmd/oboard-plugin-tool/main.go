package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/parser"

	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginpackage"
	"github.com/OboardProject/oboard/internal/pluginui"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "check" && args[0] != "pack") ||
		(args[0] == "check" && len(args) != 2) || (args[0] == "pack" && len(args) != 3) {
		fmt.Fprintln(stderr, "usage: oboard-plugin-tool check <package.zip> | pack <directory> <output.zip>")
		return 2
	}
	var data []byte
	var err error
	if args[0] == "check" {
		data, err = readArchive(args[1])
	} else {
		data, err = buildDirectory(args[1])
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	pkg, err := validate(data)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if args[0] == "pack" {
		if err := writeArchive(args[2], data); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return 1
		}
	}
	fmt.Fprintf(stdout, "valid oboard-js-v1 package: sha256=%s source_bytes=%d ui=%t\n", pkg.SHA256, len(pkg.Source), pkg.UI != nil)
	return 0
}

func validate(data []byte) (*pluginpackage.Package, error) {
	pkg, err := pluginpackage.Parse(data)
	if err != nil {
		return nil, errors.New("invalid plugin ZIP container (allowed files: manifest.json, main.js, optional ui.json)")
	}
	if err := validateManifest(pkg.Manifest); err != nil {
		return nil, errors.New("manifest.json does not satisfy the plugin manifest contract")
	}
	if err := plugin.ValidateSource(string(pkg.Source)); err != nil {
		return nil, errors.New("main.js violates plugin source limits or module restrictions")
	}
	// Source-map comments must not make validation read arbitrary host files.
	program, err := goja.Parse("main.js", string(pkg.Source), parser.WithDisableSourceMaps)
	if err != nil {
		return nil, errors.New("main.js cannot be parsed by the Goja runtime")
	}
	if _, err := goja.CompileAST(program, false); err != nil {
		return nil, errors.New("main.js cannot be compiled by the Goja runtime")
	}
	if _, err := pluginui.Parse(pkg.UI); err != nil {
		return nil, errors.New("ui.json does not satisfy the declarative UI contract")
	}
	return pkg, nil
}

func validateManifest(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errors.New("manifest must be an object")
	}
	patterns := map[string]*regexp.Regexp{
		"plugin_id": regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`),
		"version":   regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`),
	}
	for key, limit := range map[string]int{"plugin_id": 128, "name": 80, "version": 128, "description": 2000} {
		var value string
		if json.Unmarshal(fields[key], &value) != nil || strings.TrimSpace(value) == "" || len(value) > limit {
			return errors.New("invalid installation metadata")
		}
		if pattern := patterns[key]; pattern != nil && !pattern.MatchString(value) {
			return errors.New("invalid installation metadata")
		}
	}
	if _, exists := fields["ui_content_sha256"]; exists {
		return errors.New("ui_content_sha256 is host-generated")
	}
	config, hasConfig := fields["config_schema"]
	delete(fields, "config_schema")
	runtimeRaw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	manifest, err := plugin.ParseManifest(runtimeRaw)
	if err != nil {
		return err
	}
	if hasConfig {
		manifest.Params = config
		if err := plugin.ValidateManifest(manifest); err != nil {
			return err
		}
	}
	return nil
}

func readArchive(path string) ([]byte, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, errors.New("cannot open package directory")
	}
	defer root.Close()
	return readRegular(root, filepath.Base(path), pluginpackage.MaxUploadSize)
}

func buildDirectory(path string) ([]byte, error) {
	info, err := os.Lstat(filepath.Clean(path))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("source must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.New("cannot open source directory")
	}
	defer root.Close()
	inputs := []struct {
		name  string
		limit int
	}{
		{"manifest.json", pluginpackage.MaxManifestSize},
		{"main.js", pluginpackage.MaxSourceSize},
		{"ui.json", pluginpackage.MaxUISize},
	}
	files := make(map[string][]byte, len(inputs))
	for _, input := range inputs {
		if input.name == "ui.json" {
			if _, err := root.Lstat(input.name); errors.Is(err, os.ErrNotExist) {
				continue
			}
		}
		content, err := readRegular(root, input.name, input.limit)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", input.name, err)
		}
		files[input.name] = content
	}
	data, err := pluginpackage.Build(files["manifest.json"], files["main.js"], files["ui.json"])
	if err != nil {
		return nil, errors.New("source violates package encoding or size limits")
	}
	return data, nil
}

func readRegular(root *os.Root, name string, limit int) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("input must be a readable regular file, not a symlink")
	}
	if before.Size() > int64(limit) {
		return nil, errors.New("input exceeds size limit")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, errors.New("cannot open input file")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errors.New("input changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, errors.New("cannot read input file")
	}
	if len(data) > limit {
		return nil, errors.New("input exceeds size limit")
	}
	return data, nil
}

func writeArchive(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create output: provide a new file in an existing directory; existing files are never overwritten")
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("cannot write complete output package")
	}
	return nil
}
