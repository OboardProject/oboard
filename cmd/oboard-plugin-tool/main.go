// oboard-plugin-tool checks, packs, signs and inspects OBoard plugin packages
// offline. It never executes plugin code: main.js is only parsed and
// compiled, exactly as the Controller does before installation.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginpackage"
)

const usage = `usage:
  oboard-plugin-tool check <directory|package.obplugin>
  oboard-plugin-tool pack [-key publisher.pem -publisher NAME] <directory> <output.obplugin>
  oboard-plugin-tool keygen <publisher.pem>
  oboard-plugin-tool inspect <package.obplugin>`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "check":
		err = check(args[1:], stdout)
	case "pack":
		err = pack(args[1:], stdout)
	case "keygen":
		err = keygen(args[1:], stdout)
	case "inspect":
		err = inspect(args[1:], stdout)
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if errors.Is(err, errUsage) {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage")

// readDirectory loads only the files a package may contain. Anything else in
// the directory is ignored; a symlink or oversized file is an error.
func readDirectory(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	for _, name := range []string{plugin.ManifestFile, plugin.EntryFile, pluginpackage.IconFile, pluginpackage.ReadmeFile, pluginpackage.LicenseFile} {
		path := filepath.Join(dir, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s must be a regular file", name)
		}
		limit, _ := pluginpackage.FileLimit(name)
		if info.Size() > int64(limit) {
			return nil, fmt.Errorf("%s exceeds %d bytes", name, limit)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		files[name] = content
	}
	return files, nil
}

func load(target string) (*pluginpackage.Package, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		files, err := readDirectory(target)
		if err != nil {
			return nil, err
		}
		return pluginpackage.Validate(files)
	}
	if info.Size() > pluginpackage.MaxArchiveBytes {
		return nil, fmt.Errorf("package exceeds %d bytes", pluginpackage.MaxArchiveBytes)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	return pluginpackage.Parse(data)
}

func describe(pkg *pluginpackage.Package, stdout io.Writer) {
	m := pkg.Manifest
	fmt.Fprintf(stdout, "%s %s (%s)\n", m.ID, m.Version, m.Name)
	fmt.Fprintf(stdout, "sha256     %s\n", pkg.SHA256)
	if pkg.SignatureState == "verified" {
		fmt.Fprintf(stdout, "publisher  %s (%s)\n", pkg.Publisher.Identity, pkg.Publisher.Name)
	} else {
		fmt.Fprintln(stdout, "publisher  local (unsigned)")
	}
	for _, capability := range m.Capabilities {
		spec, _ := plugin.LookupCapability(capability)
		fmt.Fprintf(stdout, "capability %-22s %-6s %s\n", capability, spec.Risk, spec.Label)
	}
	if m.HTTP != nil {
		fmt.Fprintf(stdout, "http       %s %s\n", strings.Join(m.HTTP.Methods, ","), strings.Join(m.HTTP.Hosts, " "))
	}
	for _, field := range m.Environment {
		required := ""
		if field.Required {
			required = " required"
		}
		fmt.Fprintf(stdout, "env        %-20s %s%s\n", field.Name, field.Type, required)
	}
}

func check(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errUsage
	}
	pkg, err := load(args[0])
	if err != nil {
		return err
	}
	describe(pkg, stdout)
	undeclared, unused := plugin.LintSource(pkg.Manifest, pkg.Source)
	sort.Strings(unused)
	for _, name := range unused {
		fmt.Fprintf(stdout, "warning    environment %s is never read by main.js\n", name)
	}
	if len(undeclared) > 0 {
		return fmt.Errorf("main.js uses undeclared capabilities: %s", strings.Join(undeclared, ", "))
	}
	fmt.Fprintln(stdout, "ok")
	return nil
}

func pack(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("pack", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	keyPath := flags.String("key", "", "Ed25519 publisher key (PEM, from keygen)")
	publisher := flags.String("publisher", "", "publisher display name")
	if err := flags.Parse(args); err != nil || flags.NArg() != 2 {
		return errUsage
	}
	files, err := readDirectory(flags.Arg(0))
	if err != nil {
		return err
	}
	if _, err := pluginpackage.Validate(files); err != nil {
		return err
	}
	if *keyPath != "" {
		key, err := readKey(*keyPath)
		if err != nil {
			return err
		}
		signature, err := pluginpackage.Sign(files, *publisher, key)
		if err != nil {
			return err
		}
		files[pluginpackage.SignatureFile] = signature
	} else if *publisher != "" {
		return errors.New("-publisher requires -key")
	}
	data, err := pluginpackage.Build(files)
	if err != nil {
		return err
	}
	pkg, err := pluginpackage.Parse(data)
	if err != nil {
		return fmt.Errorf("built package does not verify: %w", err)
	}
	out := flags.Arg(1)
	if !strings.HasSuffix(out, pluginpackage.Extension) {
		return fmt.Errorf("output must end with %s", pluginpackage.Extension)
	}
	if err := writeFileAtomic(out, data, 0o644); err != nil {
		return err
	}
	describe(pkg, stdout)
	fmt.Fprintf(stdout, "wrote %s (%d bytes)\n", out, len(data))
	return nil
}

func keygen(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errUsage
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(file, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "publisher identity %s\n", pluginpackage.PublisherIdentity(public))
	fmt.Fprintln(stdout, "keep this key private; every update must be signed with the same key")
	return nil
}

func inspect(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errUsage
	}
	pkg, err := load(args[0])
	if err != nil {
		return err
	}
	describe(pkg, stdout)
	return nil
}

func readKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must not be readable by group or others", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("publisher key must be a PEM PKCS#8 Ed25519 key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("publisher key must be a PEM PKCS#8 Ed25519 key")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("publisher key must be Ed25519")
	}
	return key, nil
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".obplugin-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
