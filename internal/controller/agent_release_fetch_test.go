package controller

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type testAgentReleaseManifest struct {
	Version string                         `json:"version"`
	Build   string                         `json:"build"`
	Commit  string                         `json:"commit"`
	Date    string                         `json:"date"`
	Repo    string                         `json:"repo"`
	Files   []testAgentReleaseManifestFile `json:"files"`
}

type testAgentReleaseManifestFile struct {
	Name      string `json:"name"`
	Component string `json:"component"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

func TestFetchAgentDevelopmentReleaseWaitsForExpectedCommit(t *testing.T) {
	root := controllerRepositoryRoot(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	oldCommit := strings.Repeat("1", 40)
	expectedCommit := strings.Repeat("2", 40)
	oldRelease := filepath.Join(temp, "old")
	expectedRelease := filepath.Join(temp, "expected")
	writeTestAgentRelease(t, oldRelease, oldCommit, privateKey)
	writeTestAgentRelease(t, expectedRelease, expectedCommit, privateKey)

	fakeBin, countFile := writeFakeGH(t, temp)
	target := filepath.Join(temp, "target")
	output, err := runAgentReleaseFetch(t, root,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"GH_TOKEN=test-token",
		"OBOARD_AGENT_CHANNEL=dev",
		"OBOARD_AGENT_EXPECTED_COMMIT="+expectedCommit,
		"OBOARD_AGENT_RELEASE_WAIT_ATTEMPTS=2",
		"OBOARD_AGENT_RELEASE_WAIT_SECONDS=0",
		"OBOARD_AGENT_RELEASE_TARGET="+target,
		"OBOARD_RELEASE_PUBLIC_KEY="+base64.RawStdEncoding.EncodeToString(publicKey),
		"FAKE_GH_COUNT_FILE="+countFile,
		"FAKE_GH_OLD_RELEASE="+oldRelease,
		"FAKE_GH_EXPECTED_RELEASE="+expectedRelease,
		"FAKE_GH_MODE=old-then-expected",
	)
	if err != nil {
		t.Fatalf("fetch failed: %v\n%s", err, output)
	}
	if count := readFakeGHCount(t, countFile); count != 2 {
		t.Fatalf("gh download attempts = %d, want 2\n%s", count, output)
	}
	metadata := readTestAgentReleaseManifest(t, filepath.Join(target, "release-metadata.json"))
	if metadata.Commit != expectedCommit {
		t.Fatalf("promoted Agent commit = %q, want %q", metadata.Commit, expectedCommit)
	}
	if !strings.Contains(output, "does not match the expected commit") {
		t.Fatalf("fetch output does not explain the retry:\n%s", output)
	}
}

func TestFetchAgentDevelopmentReleaseDoesNotPromoteStaleAssets(t *testing.T) {
	root := controllerRepositoryRoot(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	oldCommit := strings.Repeat("3", 40)
	expectedCommit := strings.Repeat("4", 40)
	oldRelease := filepath.Join(temp, "old")
	writeTestAgentRelease(t, oldRelease, oldCommit, privateKey)

	fakeBin, countFile := writeFakeGH(t, temp)
	target := filepath.Join(temp, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runAgentReleaseFetch(t, root,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"GH_TOKEN=test-token",
		"OBOARD_AGENT_CHANNEL=dev",
		"OBOARD_AGENT_EXPECTED_COMMIT="+expectedCommit,
		"OBOARD_AGENT_RELEASE_WAIT_ATTEMPTS=2",
		"OBOARD_AGENT_RELEASE_WAIT_SECONDS=0",
		"OBOARD_AGENT_RELEASE_TARGET="+target,
		"OBOARD_RELEASE_PUBLIC_KEY="+base64.RawStdEncoding.EncodeToString(publicKey),
		"FAKE_GH_COUNT_FILE="+countFile,
		"FAKE_GH_OLD_RELEASE="+oldRelease,
		"FAKE_GH_EXPECTED_RELEASE="+oldRelease,
		"FAKE_GH_MODE=stale",
	)
	if err == nil {
		t.Fatalf("stale Agent release unexpectedly succeeded:\n%s", output)
	}
	if count := readFakeGHCount(t, countFile); count != 2 {
		t.Fatalf("gh download attempts = %d, want 2\n%s", count, output)
	}
	if content, readErr := os.ReadFile(sentinel); readErr != nil || string(content) != "preserved" {
		t.Fatalf("existing target was changed: content=%q err=%v", content, readErr)
	}
	if _, statErr := os.Stat(filepath.Join(target, "release-metadata.json")); !os.IsNotExist(statErr) {
		t.Fatalf("stale release metadata was promoted: %v", statErr)
	}
}

func TestFetchLocalAgentDevelopmentReleaseDoesNotRequireExpectedCommit(t *testing.T) {
	root := controllerRepositoryRoot(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	commit := strings.Repeat("5", 40)
	source := filepath.Join(temp, "source")
	target := filepath.Join(temp, "target")
	writeTestAgentRelease(t, source, commit, privateKey)

	output, err := runAgentReleaseFetch(t, root,
		"OBOARD_AGENT_CHANNEL=dev",
		"OBOARD_AGENT_EXPECTED_COMMIT=",
		"OBOARD_AGENT_RELEASE_DIR="+source,
		"OBOARD_AGENT_RELEASE_TARGET="+target,
		"OBOARD_RELEASE_PUBLIC_KEY="+base64.RawStdEncoding.EncodeToString(publicKey),
	)
	if err != nil {
		t.Fatalf("local fetch failed: %v\n%s", err, output)
	}
	metadata := readTestAgentReleaseManifest(t, filepath.Join(target, "release-metadata.json"))
	if metadata.Commit != commit {
		t.Fatalf("local Agent commit = %q, want %q", metadata.Commit, commit)
	}
}

// realm is bundled by the Agent release, so a release that omits it would
// produce Controller downloads that install a node unable to port forward.
// Verification must reject it instead of shipping a partial fleet payload.
func TestFetchAgentReleaseRejectsMissingRealmAssets(t *testing.T) {
	root := controllerRepositoryRoot(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	commit := strings.Repeat("7", 40)
	source := filepath.Join(temp, "source")
	target := filepath.Join(temp, "target")
	writeTestAgentRelease(t, source, commit, privateKey)
	stripReleaseAsset(t, source, "oboard-realm-linux-arm64", privateKey)

	output, err := runAgentReleaseFetch(t, root,
		"OBOARD_AGENT_CHANNEL=dev",
		"OBOARD_AGENT_EXPECTED_COMMIT=",
		"OBOARD_AGENT_RELEASE_DIR="+source,
		"OBOARD_AGENT_RELEASE_TARGET="+target,
		"OBOARD_RELEASE_PUBLIC_KEY="+base64.RawStdEncoding.EncodeToString(publicKey),
	)
	if err == nil {
		t.Fatalf("release without a realm asset unexpectedly succeeded:\n%s", output)
	}
	if _, statErr := os.Stat(filepath.Join(target, "release-metadata.json")); !os.IsNotExist(statErr) {
		t.Fatalf("incomplete release was promoted: %v", statErr)
	}
}

// stripReleaseAsset removes one asset from a signed test release and re-signs
// the manifest, producing a release that is internally consistent but no longer
// carries every required component.
func stripReleaseAsset(t *testing.T, directory, name string, privateKey ed25519.PrivateKey) {
	t.Helper()
	manifest := readTestAgentReleaseManifest(t, filepath.Join(directory, "release-manifest.json"))
	kept := manifest.Files[:0]
	for _, file := range manifest.Files {
		if file.Name == name {
			continue
		}
		kept = append(kept, file)
	}
	manifest.Files = kept
	if err := os.Remove(filepath.Join(directory, name)); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "release-manifest.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, payload)
	if err := os.WriteFile(filepath.Join(directory, "release-manifest.json.sig"), []byte(base64.RawStdEncoding.EncodeToString(signature)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFetchStableAgentReleaseDoesNotRetryVerificationFailure(t *testing.T) {
	root := controllerRepositoryRoot(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	commit := strings.Repeat("6", 40)
	developmentRelease := filepath.Join(temp, "development")
	writeTestAgentRelease(t, developmentRelease, commit, privateKey)

	fakeBin, countFile := writeFakeGH(t, temp)
	output, err := runAgentReleaseFetch(t, root,
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"GH_TOKEN=test-token",
		"VERSION=1.2.3",
		"OBOARD_AGENT_CHANNEL=release",
		"OBOARD_AGENT_EXPECTED_COMMIT=",
		"OBOARD_AGENT_RELEASE_TARGET="+filepath.Join(temp, "target"),
		"OBOARD_RELEASE_PUBLIC_KEY="+base64.RawStdEncoding.EncodeToString(publicKey),
		"FAKE_GH_COUNT_FILE="+countFile,
		"FAKE_GH_OLD_RELEASE="+developmentRelease,
		"FAKE_GH_EXPECTED_RELEASE="+developmentRelease,
		"FAKE_GH_MODE=stale",
	)
	if err == nil {
		t.Fatalf("mismatched stable release unexpectedly succeeded:\n%s", output)
	}
	if count := readFakeGHCount(t, countFile); count != 1 {
		t.Fatalf("stable gh download attempts = %d, want 1\n%s", count, output)
	}
}

func controllerRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate repository")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func runAgentReleaseFetch(t *testing.T, root string, env ...string) (string, error) {
	t.Helper()
	command := exec.Command("bash", filepath.Join(root, "scripts", "fetch-agent-release.sh"))
	command.Env = controllerTestEnv(append([]string{
		"OBOARD_AGENT_RELEASE_DIR=",
		"OBOARD_GITHUB_TOKEN=",
		"VERSION=dev-controller",
	}, env...)...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func writeTestAgentRelease(t *testing.T, directory, commit string, privateKey ed25519.PrivateKey) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := []struct {
		name      string
		component string
		arch      string
	}{
		{name: "oboard-agent-linux-amd64", component: "agent", arch: "amd64"},
		{name: "oboard-agent-linux-arm64", component: "agent", arch: "arm64"},
		{name: "oboard-sb-linux-amd64", component: "sb", arch: "amd64"},
		{name: "oboard-sb-linux-arm64", component: "sb", arch: "arm64"},
		{name: "oboard-realm-linux-amd64", component: "realm", arch: "amd64"},
		{name: "oboard-realm-linux-arm64", component: "realm", arch: "arm64"},
	}
	manifest := testAgentReleaseManifest{
		Version: "dev-" + commit[:12],
		Build:   "20260731000000",
		Commit:  commit,
		Date:    "2026-07-31T00:00:00Z",
		Repo:    "OboardProject/oboard-agent",
	}
	for _, file := range files {
		content := []byte(file.name + "-" + commit)
		if err := os.WriteFile(filepath.Join(directory, file.name), content, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(content)
		manifest.Files = append(manifest.Files, testAgentReleaseManifestFile{
			Name:      file.name,
			Component: file.component,
			OS:        "linux",
			Arch:      file.arch,
			SHA256:    hex.EncodeToString(digest[:]),
			Size:      int64(len(content)),
		})
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "release-manifest.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, payload)
	if err := os.WriteFile(filepath.Join(directory, "release-manifest.json.sig"), []byte(base64.RawStdEncoding.EncodeToString(signature)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestAgentReleaseManifest(t *testing.T, path string) testAgentReleaseManifest {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest testAgentReleaseManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeFakeGH(t *testing.T, temp string) (string, string) {
	t.Helper()
	fakeBin := filepath.Join(temp, "bin")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
count=0
if [ -f "$FAKE_GH_COUNT_FILE" ]; then count=$(cat "$FAKE_GH_COUNT_FILE"); fi
count=$((count + 1))
echo "$count" > "$FAKE_GH_COUNT_FILE"
source=$FAKE_GH_OLD_RELEASE
if [ "$FAKE_GH_MODE" = old-then-expected ] && [ "$count" -gt 1 ]; then
  source=$FAKE_GH_EXPECTED_RELEASE
fi
destination=
while [ "$#" -gt 0 ]; do
  if [ "$1" = --dir ]; then destination=$2; shift 2; else shift; fi
done
test -n "$destination"
for file in oboard-agent-linux-amd64 oboard-agent-linux-arm64 oboard-sb-linux-amd64 oboard-sb-linux-arm64 oboard-realm-linux-amd64 oboard-realm-linux-arm64 release-manifest.json release-manifest.json.sig; do
  cp "$source/$file" "$destination/$file"
done
`
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return fakeBin, filepath.Join(temp, "gh-count")
}

func readFakeGHCount(t *testing.T, path string) int {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(payload)))
	if err != nil {
		t.Fatal(fmt.Errorf("decode fake gh count: %w", err))
	}
	return count
}
