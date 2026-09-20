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
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/pluginpackage"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRepositoryURL(t *testing.T) {
	for _, raw := range []string{"https://github.com/owner/repo", "https://github.com/owner/repo.git", "https://github.com/owner/repo/", "https://github.com/owner/repo.git/"} {
		source, err := parseRepository(raw)
		if err != nil || source.Owner != "owner" || source.Repo != "repo" || source.URL != "https://github.com/owner/repo" {
			t.Errorf("%q: %+v %v", raw, source, err)
		}
	}
	for _, raw := range []string{
		"http://github.com/owner/repo", "https://github.com.evil/owner/repo", "https://github.com:443/owner/repo", "https://user@github.com/owner/repo", "https://github.com/owner/repo?token=secret", "https://github.com/owner/repo?", "https://github.com/owner/repo#", "https://github.com/owner/repo/tree/main", "https://github.com/owner/repo/tree/a/b", "https://github.com/owner/repo/blob/main/main.js", "https://github.com/owner/%2e%2e", "https://github.com/owner/..", "https://github.com/owner/.git", "https://github.com/owner/repo//", "https://github.com/-owner/repo", "https://github.com/owner", "git@github.com:owner/repo.git", "https://127.0.0.1/owner/repo", "https://github.com/owner/repo\\evil",
	} {
		if _, err := parseRepository(raw); !errors.Is(err, ErrRepository) {
			t.Errorf("accepted %q: %v", raw, err)
		}
	}
	for _, ref := range []string{"main", "v1.0.0", "feature/plugin", strings.Repeat("a", 40), "refs/tags/v1"} {
		if !validRef(ref) {
			t.Errorf("rejected ref %q", ref)
		}
	}
	for _, ref := range []string{"", "../main", "main?x=1", "a%2fb", "main#x", "a//b", "/main", "main/", "main.lock", "a/.hidden", "a..b", "main@{1}", "main\n", strings.Repeat("a", 256)} {
		if validRef(ref) {
			t.Errorf("accepted ref %q", ref)
		}
	}
}

const testCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const testTree = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const testBase = "/repos/owner/repo"

type fixture struct {
	bodies map[string]any
	calls  []string
}

func newFixture(ui bool) *fixture {
	f := &fixture{bodies: map[string]any{
		testBase:                                map[string]any{"private": false, "default_branch": "main"},
		testBase + "/commits/main":              testCommit + "\n",
		testBase + "/git/commits/" + testCommit: map[string]any{"sha": testCommit, "tree": map[string]any{"sha": testTree}},
	}}
	entries := []map[string]any{}
	contents := map[string]string{"manifest.json": `{"id":"sample"}`, "main.js": `function main() { return 1; }`}
	if ui {
		contents["ui.json"] = `{"pages":[]}`
	}
	for _, name := range []string{"manifest.json", "main.js", "ui.json"} {
		content, ok := contents[name]
		if !ok {
			continue
		}
		sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
		sha := hex.EncodeToString(sum[:])
		entries = append(entries, map[string]any{"path": name, "mode": "100644", "type": "blob", "size": len(content), "sha": sha})
		f.bodies[testBase+"/git/blobs/"+sha] = map[string]any{"sha": sha, "encoding": "base64", "size": len(content), "content": base64.StdEncoding.EncodeToString([]byte(content))}
	}
	entries = append(entries, map[string]any{"path": "README.md", "mode": "120000", "type": "blob"}, map[string]any{"path": "docs", "mode": "040000", "type": "tree"})
	f.bodies[testBase+"/git/trees/"+testTree] = map[string]any{"sha": testTree, "truncated": false, "tree": entries}
	return f
}

func (f *fixture) transport(t *testing.T) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.github.com" || r.URL.Scheme != "https" || r.Method != "GET" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatalf("unsafe request: %s", r.URL)
		}
		f.calls = append(f.calls, r.URL.EscapedPath())
		body, ok := f.bodies[r.URL.EscapedPath()]
		if !ok {
			t.Fatalf("unexpected request %s", r.URL)
		}
		accept := "application/vnd.github+json"
		if strings.HasPrefix(r.URL.EscapedPath(), testBase+"/commits/") {
			accept = "application/vnd.github.sha"
		}
		if r.Header.Get("Accept") != accept {
			t.Fatalf("unexpected media type for %s: %s", r.URL, r.Header.Get("Accept"))
		}
		if raw, ok := body.(string); ok {
			return response(200, raw), nil
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return response(200, string(data)), nil
	}
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body))}
}

func TestFetchPinsMutableRefAndOnlyImportsRootFiles(t *testing.T) {
	for _, ref := range []string{"", "main", "feature/plugin", "v1.0.0", "refs/tags/v1", testCommit} {
		for _, ui := range []bool{false, true} {
			f := newFixture(ui)
			if ref != "" && ref != "main" {
				pathRef := strings.ReplaceAll(ref, "/", "%2F")
				f.bodies[testBase+"/commits/"+pathRef] = f.bodies[testBase+"/commits/main"]
			}
			result, err := fetch(context.Background(), "https://github.com/owner/repo.git", ref, f.transport(t))
			if err != nil {
				t.Fatal(err)
			}
			resolvedRef := ref
			if ref == "" {
				resolvedRef = "main"
			}
			if result.Source.Commit != testCommit || result.Source.Ref != resolvedRef || result.Source.URL != "https://github.com/owner/repo" {
				t.Fatalf("wrong source: %+v", result.Source)
			}
			if (result.Package.UI != nil) != ui {
				t.Fatal("optional UI presence lost")
			}
			parsed, err := pluginpackage.Parse(result.Archive)
			if err != nil || parsed.SHA256 != result.Package.SHA256 {
				t.Fatal("invalid package")
			}
			wantCalls := 6
			if ui {
				wantCalls++
			}
			if len(f.calls) != wantCalls {
				t.Fatalf("unexpected calls: %v", f.calls)
			}
			if f.calls[2] != testBase+"/git/commits/"+testCommit {
				t.Fatalf("unpinned commit access: %v", f.calls)
			}
			for _, path := range f.calls[3:] {
				if !strings.HasPrefix(path, testBase+"/git/trees/"+testTree) && !strings.HasPrefix(path, testBase+"/git/blobs/") {
					t.Fatalf("unpinned access: %s", path)
				}
			}
		}
	}
}

func TestRefResolutionAvoidsDiffAndStaysPinned(t *testing.T) {
	f := newFixture(false)
	underlying := f.transport(t)
	resolutions := 0
	result, err := fetch(context.Background(), "https://github.com/owner/repo", "main", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == testBase+"/commits/main" {
			resolutions++
			if r.Header.Get("Accept") != "application/vnd.github.sha" {
				// A valid plugin commit can have an unrelated large documentation diff.
				return response(200, `{"files":[{"patch":"`+strings.Repeat("x", 2<<20)+`"}]}`), nil
			}
			res, err := underlying.RoundTrip(r)
			f.bodies[testBase+"/commits/main"] = strings.Repeat("c", 40)
			return res, err
		}
		return underlying.RoundTrip(r)
	}))
	if err != nil || result.Source.Commit != testCommit || resolutions != 1 {
		t.Fatalf("commit was not pinned without diff: source=%+v resolutions=%d err=%v", result.Source, resolutions, err)
	}
}

func TestCommitResponseLimits(t *testing.T) {
	for _, test := range []struct {
		path  string
		limit int
	}{
		{testBase + "/commits/main", 64},
		{testBase + "/git/commits/" + testCommit, 1 << 20},
	} {
		for _, unknownLength := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown-length=%t", test.path, unknownLength), func(t *testing.T) {
				f := newFixture(false)
				underlying := f.transport(t)
				body := strings.NewReader(strings.Repeat("x", test.limit+1024))
				_, err := fetch(context.Background(), "https://github.com/owner/repo", "main", roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path != test.path {
						return underlying.RoundTrip(r)
					}
					res := response(200, "")
					res.Body = io.NopCloser(body)
					res.ContentLength = int64(body.Len())
					if unknownLength {
						res.ContentLength = -1
					}
					return res, nil
				}))
				if !errors.Is(err, ErrTooLarge) {
					t.Fatalf("got %v, want %v", err, ErrTooLarge)
				}
				read := test.limit + 1024 - body.Len()
				if read > test.limit+1 || (!unknownLength && read != 0) {
					t.Fatalf("read beyond response limit: %d", read)
				}
			})
		}
	}
}

func TestGitCommitIdentityAndTreeValidation(t *testing.T) {
	for _, name := range []string{"commit-mismatch", "missing-commit", "invalid-tree", "missing-tree"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(false)
			commit := f.bodies[testBase+"/git/commits/"+testCommit].(map[string]any)
			switch name {
			case "commit-mismatch":
				commit["sha"] = strings.Repeat("c", 40)
			case "missing-commit":
				delete(commit, "sha")
			case "invalid-tree":
				commit["tree"].(map[string]any)["sha"] = "../evil"
			case "missing-tree":
				delete(commit, "tree")
			}
			_, err := fetch(context.Background(), "https://github.com/owner/repo", "main", f.transport(t))
			if !errors.Is(err, ErrContent) || len(f.calls) != 3 {
				t.Fatalf("invalid commit accepted: calls=%v err=%v", f.calls, err)
			}
		})
	}
}

func TestRejectUnsafeContents(t *testing.T) {
	for _, name := range []string{"symlink", "submodule", "directory", "missing", "oversize", "negative-size", "missing-size", "truncated", "tree-mismatch", "duplicate", "base64", "blob-hash", "blob-size", "blob-encoding", "private"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(false)
			tree := f.bodies[testBase+"/git/trees/"+testTree].(map[string]any)
			entries := tree["tree"].([]map[string]any)
			entry := entries[0]
			blob := f.bodies[testBase+"/git/blobs/"+entry["sha"].(string)].(map[string]any)
			want := ErrContent
			switch name {
			case "symlink":
				entry["mode"] = "120000"
				want = ErrFileType
			case "submodule":
				entry["mode"] = "160000"
				entry["type"] = "commit"
				want = ErrFileType
			case "directory":
				entry["mode"] = "040000"
				entry["type"] = "tree"
				want = ErrFileType
			case "missing":
				tree["tree"] = entries[1:]
				want = ErrMissingFile
			case "oversize":
				entry["size"] = pluginpackage.MaxManifestSize + 1
				want = ErrTooLarge
			case "negative-size":
				entry["size"] = -1
			case "missing-size":
				delete(entry, "size")
			case "truncated":
				tree["truncated"] = true
			case "tree-mismatch":
				tree["sha"] = testCommit
			case "duplicate":
				tree["tree"] = append(entries, entry)
			case "base64":
				blob["content"] = "!!!!"
			case "blob-hash":
				blob["content"] = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", entry["size"].(int))))
			case "blob-size":
				blob["size"] = 1
			case "blob-encoding":
				blob["encoding"] = "utf-8"
			case "private":
				f.bodies[testBase].(map[string]any)["private"] = true
				want = ErrRepository
			}
			result, err := fetch(context.Background(), "https://github.com/owner/repo", "", f.transport(t))
			if !errors.Is(err, want) || result.Package != nil {
				t.Fatalf("got %v, want %v", err, want)
			}
		})
	}
}

func TestUpstreamFailuresAreBoundedAndSecretFree(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{301, ErrRedirect}, {302, ErrRedirect}, {404, ErrNotFound}, {409, ErrNotFound}, {422, ErrNotFound}, {403, ErrRateLimit}, {429, ErrRateLimit}, {500, ErrUpstream}} {
		calls := 0
		_, err := fetch(context.Background(), "https://github.com/owner/repo", "stale", roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			res := response(test.status, "upstream-secret-token")
			res.Header.Set("Location", "http://127.0.0.1/secret")
			return res, nil
		}))
		if !errors.Is(err, test.want) || calls != 1 || strings.Contains(err.Error(), "secret") {
			t.Fatalf("status %d: %v, calls %d", test.status, err, calls)
		}
	}
	for _, contentLength := range []int64{-1, 1 << 20} {
		_, err := fetch(context.Background(), "https://github.com/owner/repo", "", roundTripFunc(func(*http.Request) (*http.Response, error) {
			res := response(200, strings.Repeat("x", (64<<10)+1))
			res.ContentLength = contentLength
			return res, nil
		}))
		if !errors.Is(err, ErrTooLarge) {
			t.Fatal(err)
		}
	}
	_, err := fetch(context.Background(), "https://github.com/owner/repo", "", roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("upstream-secret-token") }))
	if !errors.Is(err, ErrUpstream) {
		t.Fatal(err)
	}
}

func TestStaleRefAndInvalidCommit(t *testing.T) {
	for _, name := range []string{"stale", "malformed", "mismatched-full-sha"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(false)
			ref := "main"
			want := ErrContent
			if name == "malformed" {
				f.bodies[testBase+"/commits/main"] = "../evil"
			}
			if name == "mismatched-full-sha" {
				ref = strings.Repeat("c", 40)
				f.bodies[testBase+"/commits/"+ref] = f.bodies[testBase+"/commits/main"]
			}
			underlying := f.transport(t)
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if name == "stale" && strings.Contains(r.URL.Path, "/commits/") {
					return response(404, "deleted branch and secret"), nil
				}
				return underlying.RoundTrip(r)
			})
			if name == "stale" {
				want = ErrNotFound
			}
			_, err := fetch(context.Background(), "https://github.com/owner/repo", ref, transport)
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			for _, path := range f.calls {
				if strings.Contains(path, "/git/") {
					t.Fatal("fetched content without resolving commit")
				}
			}
		})
	}
}

func TestOversizeBlobEnvelope(t *testing.T) {
	f := newFixture(false)
	underlying := f.transport(t)
	_, err := fetch(context.Background(), "https://github.com/owner/repo", "", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/git/blobs/") {
			res := response(200, strings.Repeat("x", pluginpackage.MaxManifestSize*2+4097))
			res.ContentLength = -1
			return res, nil
		}
		return underlying.RoundTrip(r)
	}))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
}

func TestInvalidInputMakesNoRequest(t *testing.T) {
	transport := roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("request for invalid input"); return nil, nil })
	if _, err := fetch(context.Background(), "https://evil.com/a/b", "", transport); !errors.Is(err, ErrRepository) {
		t.Fatal(err)
	}
	if _, err := fetch(context.Background(), "https://github.com/owner/repo", "../secret", transport); !errors.Is(err, ErrRef) {
		t.Fatal(err)
	}
}

func TestProductionTransportBoundary(t *testing.T) {
	transport := newTransport()
	defer transport.CloseIdleConnections()
	if transport.Proxy != nil || transport.TLSClientConfig.InsecureSkipVerify || transport.ResponseHeaderTimeout == 0 || transport.MaxResponseHeaderBytes == 0 {
		t.Fatal("unsafe transport")
	}
	for _, address := range []string{"127.0.0.1:443", "evil.com:443", "api.github.com:80", "raw.githubusercontent.com:443"} {
		_, err := dialPublic(context.Background(), address, nil, nil)
		if !errors.Is(err, ErrAddress) {
			t.Fatal(err)
		}
	}
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "198.18.0.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "64:ff9b::7f00:1", "2002:7f00:1::", "2001:db8::1"} {
		_, err := dialPublic(context.Background(), "api.github.com:443", func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("140.82.112.5"), netip.MustParseAddr(address)}, nil
		}, func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("dialed denied address")
			return nil, nil
		})
		if !errors.Is(err, ErrAddress) {
			t.Errorf("%s: %v", address, err)
		}
	}
	_, err := dialPublic(context.Background(), "api.github.com:443", func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("140.82.112.5")}, nil
	}, func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "140.82.112.5:443" {
			t.Fatalf("dial not pinned: %s %s", network, address)
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
