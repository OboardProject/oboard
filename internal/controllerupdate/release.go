package controllerupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const githubAPI = "https://api.github.com/repos/OboardProject/oboard/releases"

const (
	releaseMetadataTimeout = 15 * time.Second
	devReleaseAttempts     = 2
	releaseRetryDelay      = 500 * time.Millisecond
)

var (
	hashPattern           = regexp.MustCompile(`^[a-f0-9]{64}$`)
	versionPattern        = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(?:[-+].*)?$`)
	buildTimestampPattern = regexp.MustCompile(`^[0-9]{14}$`)
)

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

type githubRelease struct {
	TagName    string         `json:"tag_name"`
	Draft      bool           `json:"draft"`
	Prerelease bool           `json:"prerelease"`
	Assets     []releaseAsset `json:"assets"`
}

type remoteRelease struct {
	Manifest Manifest
	Assets   map[string]string
}

func fetchRelease(ctx context.Context, client *http.Client, channel string) (remoteRelease, error) {
		budgetCtx, cancel := context.WithTimeout(ctx, releaseMetadataTimeout)
		defer cancel()
	attempts := 1
	if channel == "dev" {
		attempts = devReleaseAttempts
	}
	var result remoteRelease
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		result, err = fetchReleaseOnce(budgetCtx, client, channel)
		if err == nil {
			return result, nil
		}
		if attempt+1 < attempts {
			select {
			case <-ctx.Done():
				return remoteRelease{}, ctx.Err()
			case <-time.After(releaseRetryDelay):
			}
		}
	}
	return remoteRelease{}, err
}

func fetchReleaseOnce(ctx context.Context, client *http.Client, channel string) (remoteRelease, error) {
	endpoint := githubAPI + "/latest"
	if channel == "dev" {
		endpoint = githubAPI + "/tags/dev"
	}
	var release githubRelease
	if err := getJSON(ctx, client, endpoint, &release); err != nil {
		return remoteRelease{}, err
	}
	if release.Draft {
		return remoteRelease{}, errors.New("release is still a draft")
	}
	if channel == "dev" && (!release.Prerelease || release.TagName != "dev") {
		return remoteRelease{}, errors.New("development release metadata is invalid")
	}
	assets := make(map[string]string, len(release.Assets))
	for _, asset := range release.Assets {
		if asset.Name != "" && strings.HasPrefix(asset.URL, "https://github.com/OboardProject/oboard/releases/download/") {
			assets[asset.Name] = asset.URL
		}
	}
	manifestURL := assets[ManifestName]
	if manifestURL == "" {
		return remoteRelease{}, fmt.Errorf("release does not contain %s", ManifestName)
	}
	var manifest Manifest
	if err := getJSON(ctx, client, manifestURL, &manifest); err != nil {
		return remoteRelease{}, err
	}
	if err := validateManifest(manifest, channel, assets); err != nil {
		return remoteRelease{}, err
	}
	if channel == "stable" && release.TagName != manifest.Version && release.TagName != "v"+manifest.Version {
		return remoteRelease{}, errors.New("stable release tag does not match the controller manifest")
	}
	return remoteRelease{Manifest: manifest, Assets: assets}, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "oboard-controller-updater")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download release metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download release metadata: HTTP %d", resp.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode release metadata: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("release metadata contains trailing JSON data")
	}
	return nil
}

func validateManifest(manifest Manifest, channel string, assets map[string]string) error {
	if manifest.Schema != ManifestSchema {
		return fmt.Errorf("unsupported controller manifest schema %d", manifest.Schema)
	}
	if manifest.Channel != channel {
		return fmt.Errorf("controller manifest channel is %q, expected %q", manifest.Channel, channel)
	}
	if strings.TrimSpace(manifest.Version) == "" || strings.TrimSpace(manifest.Build) == "" || strings.TrimSpace(manifest.Commit) == "" {
		return errors.New("controller manifest build metadata is incomplete")
	}
	if _, err := time.Parse(time.RFC3339, manifest.Date); err != nil {
		return errors.New("controller manifest date is invalid")
	}
	seen := map[string]bool{}
	if len(manifest.Artifacts) != 2 {
		return errors.New("controller manifest must contain exactly two Linux packages")
	}
	for _, artifact := range manifest.Artifacts {
		if seen[artifact.Name] || assets[artifact.Name] == "" {
			return fmt.Errorf("controller artifact %q is duplicated or absent from the release", artifact.Name)
		}
		seen[artifact.Name] = true
		if artifact.OS != "linux" || artifact.Arch != "amd64" && artifact.Arch != "arm64" {
			return fmt.Errorf("unsupported controller artifact target %s/%s", artifact.OS, artifact.Arch)
		}
		expectedVersion := manifest.Version
		if channel == "dev" {
			expectedVersion = "dev"
		}
		expectedName := fmt.Sprintf("oboard_controller_%s_linux_%s.tar.gz", expectedVersion, artifact.Arch)
		if artifact.Name != expectedName {
			return fmt.Errorf("controller artifact %q does not match target metadata", artifact.Name)
		}
		if artifact.Size <= 0 || artifact.Size > 1<<30 || !hashPattern.MatchString(artifact.SHA256) {
			return fmt.Errorf("controller artifact %q has invalid integrity metadata", artifact.Name)
		}
	}
	for _, arch := range []string{"amd64", "arm64"} {
		if _, err := selectArtifact(manifest, "linux", arch); err != nil {
			return err
		}
	}
	return nil
}

func selectArtifact(manifest Manifest, osName, arch string) (Artifact, error) {
	for _, artifact := range manifest.Artifacts {
		if artifact.OS == osName && artifact.Arch == arch {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("release does not contain a controller package for %s/%s", osName, arch)
}

func updateAvailable(channel string, current BuildInfo, manifest Manifest) bool {
	if comparison, ok := compareVersions(current.Version, manifest.Version); ok {
		if comparison != 0 {
			return comparison < 0
		}
		return strings.Contains(current.Version, "-")
	}
	currentBuild, currentOK := parseBuildTimestamp(current.Build)
	manifestBuild, manifestOK := parseBuildTimestamp(manifest.Build)
	return currentOK && manifestOK && manifestBuild > currentBuild
}

func parseBuildTimestamp(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if !buildTimestampPattern.MatchString(value) {
		return "", false
	}
	return value, true
}

func compareVersions(left, right string) (int, bool) {
	parse := func(value string) ([3]int, bool) {
		match := versionPattern.FindStringSubmatch(strings.TrimSpace(value))
		if match == nil {
			return [3]int{}, false
		}
		var result [3]int
		for i := range result {
			value, err := strconv.Atoi(match[i+1])
			if err != nil {
				return [3]int{}, false
			}
			result[i] = value
		}
		return result, true
	}
	a, okA := parse(left)
	b, okB := parse(right)
	if !okA || !okB {
		return 0, false
	}
	for i := range a {
		if a[i] < b[i] {
			return -1, true
		}
		if a[i] > b[i] {
			return 1, true
		}
	}
	return 0, true
}

func verifyDownload(reader io.Reader, writer io.Writer, expected Artifact) error {
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(writer, hash), io.LimitReader(reader, expected.Size+1))
	if err != nil {
		return err
	}
	if written != expected.Size {
		return fmt.Errorf("controller package size is %d, expected %d", written, expected.Size)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return errors.New("controller package SHA-256 does not match the release manifest")
	}
	return nil
}
