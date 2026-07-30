//go:build ignore

package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type manifest struct {
	Version string         `json:"version"`
	Build   string         `json:"build"`
	Commit  string         `json:"commit"`
	Date    string         `json:"date"`
	Repo    string         `json:"repo"`
	Files   []manifestFile `json:"files"`
}

type manifestFile struct {
	Name      string `json:"name"`
	Component string `json:"component"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

func main() {
	dir := flag.String("dir", "", "directory containing Agent release assets")
	publicKey := flag.String("public-key", "", "base64 raw Ed25519 public key")
	repo := flag.String("repo", "OboardProject/oboard-agent", "expected release repository")
	channel := flag.String("channel", "stable", "stable, prerelease, or dev")
	expectedVersion := flag.String("expected-version", "", "exact expected version for immutable releases")
	expectedCommit := flag.String("expected-commit", "", "exact expected commit for development releases")
	flag.Parse()
	if *dir == "" || *publicKey == "" {
		fatal(errors.New("--dir and --public-key are required"))
	}

	manifestPath := filepath.Join(*dir, "release-manifest.json")
	payload, err := os.ReadFile(manifestPath)
	if err != nil {
		fatal(err)
	}
	var release manifest
	if err := json.Unmarshal(payload, &release); err != nil {
		fatal(fmt.Errorf("decode manifest: %w", err))
	}
	canonical, err := json.Marshal(release)
	if err != nil {
		fatal(err)
	}
	signature, err := os.ReadFile(filepath.Join(*dir, "release-manifest.json.sig"))
	if err != nil {
		fatal(err)
	}
	pub, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(*publicKey))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		fatal(errors.New("invalid Agent release public key"))
	}
	sig, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		fatal(errors.New("invalid Agent release manifest signature"))
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), canonical, sig) {
		fatal(errors.New("Agent release manifest signature verification failed"))
	}
	if release.Repo != *repo {
		fatal(fmt.Errorf("manifest repo %q does not match %q", release.Repo, *repo))
	}
	if *expectedVersion != "" && release.Version != *expectedVersion {
		fatal(fmt.Errorf("manifest version %q does not match %q", release.Version, *expectedVersion))
	}
	if *expectedCommit != "" {
		if len(*expectedCommit) != 40 {
			fatal(errors.New("expected Agent commit must be a full 40-character SHA"))
		}
		if _, err := hex.DecodeString(*expectedCommit); err != nil {
			fatal(errors.New("expected Agent commit must be hexadecimal"))
		}
		if release.Commit != *expectedCommit {
			fatal(fmt.Errorf("manifest commit %q does not match %q", release.Commit, *expectedCommit))
		}
	}
	if *channel == "dev" && !strings.Contains(strings.ToLower(release.Version), "dev") {
		fatal(fmt.Errorf("development channel requires a development manifest version, got %q", release.Version))
	}
	if release.Version == "" || release.Build == "" || release.Commit == "" || release.Date == "" {
		fatal(errors.New("manifest release metadata is incomplete"))
	}
	required := map[string]string{
		"oboard-agent-linux-amd64": "agent",
		"oboard-agent-linux-arm64": "agent",
		"oboard-sb-linux-amd64":    "sb",
		"oboard-sb-linux-arm64":    "sb",
	}
	seen := make(map[string]bool, len(required))
	for _, file := range release.Files {
		component, requiredFile := required[file.Name]
		if !requiredFile || component != file.Component || file.OS != "linux" || (file.Arch != "amd64" && file.Arch != "arm64") || !safeName(file.Name) || file.Size <= 0 {
			fatal(fmt.Errorf("invalid manifest file entry %q", file.Name))
		}
		if seen[file.Name] {
			fatal(fmt.Errorf("duplicate manifest file entry %q", file.Name))
		}
		seen[file.Name] = true
		digest, size, err := sha256File(filepath.Join(*dir, file.Name))
		if err != nil {
			fatal(err)
		}
		if digest != file.SHA256 || size != file.Size {
			fatal(fmt.Errorf("asset %q does not match signed manifest", file.Name))
		}
	}
	if len(seen) != len(required) {
		fatal(errors.New("manifest does not contain every required Linux Agent and kernel asset"))
	}
	encoder := json.NewEncoder(os.Stdout)
	if err := encoder.Encode(release); err != nil {
		fatal(err)
	}
}

func safeName(name string) bool {
	return name != "" && filepath.Base(name) == name && !strings.Contains(name, "..")
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), size, err
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "Agent release verification failed:", err); os.Exit(1) }
