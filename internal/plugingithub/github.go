// Package plugingithub imports public GitHub plugin repositories at immutable commits.
package plugingithub

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/pluginpackage"
)

var (
	ErrRepository  = errors.New("plugin_github_invalid_repository")
	ErrRef         = errors.New("plugin_github_invalid_ref")
	ErrNotFound    = errors.New("plugin_github_repository_or_ref_not_found")
	ErrRateLimit   = errors.New("plugin_github_rate_limited")
	ErrUpstream    = errors.New("plugin_github_request_failed")
	ErrRedirect    = errors.New("plugin_github_redirect_denied")
	ErrTooLarge    = errors.New("plugin_github_response_too_large")
	ErrContent     = errors.New("plugin_github_invalid_content")
	ErrMissingFile = errors.New("plugin_github_required_file_missing")
	ErrFileType    = errors.New("plugin_github_not_regular_file")
	ErrAddress     = errors.New("plugin_github_address_denied")
)

// Source describes exactly what was imported. Ref is resolved even when omitted
// by the caller; URL is the canonical repository URL, not a download URL.
type Source struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	URL    string `json:"url"`
}

type Result struct {
	Archive []byte
	Package *pluginpackage.Package
	Source  Source
}

var (
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
	refPattern   = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,255}$`)
	shaPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func parseRepository(raw string) (Source, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 512 || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.RawPath != "" || strings.Contains(raw, "%") {
		return Source{}, ErrRepository
	}
	parts := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "" || !ownerPattern.MatchString(parts[1]) {
		return Source{}, ErrRepository
	}
	repo := strings.TrimSuffix(parts[2], ".git")
	if !repoPattern.MatchString(repo) || repo == "." || repo == ".." {
		return Source{}, ErrRepository
	}
	return Source{Owner: parts[1], Repo: repo, URL: "https://github.com/" + parts[1] + "/" + repo}, nil
}

func validRef(ref string) bool {
	if !refPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.HasSuffix(ref, ".") {
		return false
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// Fetch accepts only a repository root URL (optionally ending in .git or /).
// Branches, tags, or full commit IDs belong in ref, never in a /tree/ URL.
// Empty ref selects the public repository's default branch. No credentials,
// environment proxies, redirects, repository code, or dependencies are used.
func Fetch(ctx context.Context, repositoryURL, ref string) (Result, error) {
	transport := newTransport()
	defer transport.CloseIdleConnections()
	return fetch(ctx, repositoryURL, ref, transport)
}

func fetch(parent context.Context, repositoryURL, ref string, transport http.RoundTripper) (Result, error) {
	source, err := parseRepository(repositoryURL)
	if err != nil {
		return Result{}, err
	}
	if ref != "" && !validRef(ref) {
		return Result{}, ErrRef
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	base := "/repos/" + source.Owner + "/" + source.Repo
	var repository struct {
		Private       bool   `json:"private"`
		DefaultBranch string `json:"default_branch"`
	}
	if err := getJSON(ctx, client, base, 64<<10, &repository); err != nil {
		return Result{}, err
	}
	if repository.Private {
		return Result{}, ErrRepository
	}
	if ref == "" {
		ref = repository.DefaultBranch
	}
	if !validRef(ref) {
		return Result{}, ErrRef
	}
	source.Ref = ref
	// Resolve once without diff data, then fetch only immutable Git objects.
	sha, err := getBody(ctx, client, base+"/commits/"+url.PathEscape(ref), "application/vnd.github.sha", 64)
	if err != nil {
		return Result{}, err
	}
	source.Commit = strings.TrimSpace(string(sha))
	if !shaPattern.MatchString(source.Commit) || (shaPattern.MatchString(ref) && ref != source.Commit) {
		return Result{}, ErrContent
	}
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := getJSON(ctx, client, base+"/git/commits/"+source.Commit, 1<<20, &commit); err != nil {
		return Result{}, err
	}
	if commit.SHA != source.Commit || !shaPattern.MatchString(commit.Tree.SHA) {
		return Result{}, ErrContent
	}
	var tree struct {
		SHA       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Tree      []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size *int   `json:"size"`
		} `json:"tree"`
	}
	// Nonrecursive: only root entries are inspected; documentation and source
	// subdirectories are never fetched. Blob IDs derive from this pinned tree.
	if err := getJSON(ctx, client, base+"/git/trees/"+commit.Tree.SHA, 2<<20, &tree); err != nil {
		return Result{}, err
	}
	if tree.Truncated || tree.SHA != commit.Tree.SHA {
		return Result{}, ErrContent
	}
	limits := map[string]int{"manifest.json": pluginpackage.MaxManifestSize, "main.js": pluginpackage.MaxSourceSize, "ui.json": pluginpackage.MaxUISize}
	files := make(map[string][]byte, 3)
	for _, entry := range tree.Tree {
		limit, wanted := limits[entry.Path]
		if !wanted {
			continue
		}
		if _, duplicate := files[entry.Path]; duplicate {
			return Result{}, ErrContent
		}
		if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") {
			return Result{}, ErrFileType
		}
		if !shaPattern.MatchString(entry.SHA) || entry.Size == nil || *entry.Size < 0 {
			return Result{}, ErrContent
		}
		if *entry.Size > limit {
			return Result{}, ErrTooLarge
		}
		var blob struct {
			SHA      string `json:"sha"`
			Encoding string `json:"encoding"`
			Size     *int   `json:"size"`
			Content  string `json:"content"`
		}
		if err := getJSON(ctx, client, base+"/git/blobs/"+entry.SHA, int64(limit*2+4096), &blob); err != nil {
			return Result{}, err
		}
		if blob.SHA != entry.SHA || blob.Encoding != "base64" || blob.Size == nil || *blob.Size != *entry.Size {
			return Result{}, ErrContent
		}
		content, err := base64.StdEncoding.Strict().DecodeString(blob.Content)
		if err != nil || len(content) != *entry.Size {
			return Result{}, ErrContent
		}
		if len(content) > limit {
			return Result{}, ErrTooLarge
		}
		h := sha1.New() // Git's protocol-defined blob identity, not a signature.
		fmt.Fprintf(h, "blob %d\x00", len(content))
		h.Write(content)
		if hex.EncodeToString(h.Sum(nil)) != entry.SHA {
			return Result{}, ErrContent
		}
		files[entry.Path] = content
	}
	if _, ok := files["manifest.json"]; !ok {
		return Result{}, ErrMissingFile
	}
	if _, ok := files["main.js"]; !ok {
		return Result{}, ErrMissingFile
	}
	archive, err := pluginpackage.Build(files["manifest.json"], files["main.js"], files["ui.json"])
	if err != nil {
		return Result{}, ErrContent
	}
	pkg, err := pluginpackage.Parse(archive)
	if err != nil {
		return Result{}, ErrContent
	}
	return Result{Archive: archive, Package: pkg, Source: source}, nil
}

func getJSON(ctx context.Context, client *http.Client, path string, limit int64, target any) error {
	body, err := getBody(ctx, client, path, "application/vnd.github+json", limit)
	if err != nil {
		return err
	}
	if json.Unmarshal(body, target) != nil {
		return ErrContent
	}
	return nil
}

func getBody(ctx context.Context, client *http.Client, path, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return nil, ErrUpstream
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "OBoard-Plugin-Importer")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, ErrAddress) {
			return nil, ErrAddress
		}
		return nil, ErrUpstream
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, ErrRedirect
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden:
		return nil, ErrRateLimit
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusConflict || resp.StatusCode == http.StatusUnprocessableEntity:
		return nil, ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return nil, ErrUpstream
	}
	if resp.ContentLength > limit {
		return nil, ErrTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, ErrUpstream
	}
	if int64(len(body)) > limit {
		return nil, ErrTooLarge
	}
	return body, nil
}
