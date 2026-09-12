package controller

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/version"
)

type resourceDownloadTransport func(*http.Request) (*http.Response, error)

func (f resourceDownloadTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResourceDownloadPinnedVersions(t *testing.T) {
	for _, tc := range []struct{ version, build, tag string }{
		{"1.2.3", "20260909010101", "v1.2.3"},
		{"v1.2.3", "20260909010101", "v1.2.3"},
		{"1.2.3-rc.1", "20260909010101", "v1.2.3-rc.1"},
		{"dev-012345abcdef", "20260909010101", "dev-012345abcdef-20260909010101"},
		{"dev-012345abcdef", "20260909020202", "dev-012345abcdef-20260909020202"},
		{"dev", "dev", ""}, {"dev-012345abcdef", "dev", ""},
		{"latest", "20260909010101", ""}, {"../../main", "20260909010101", ""},
	} {
		if got := resourceReleaseTag(tc.version, tc.build); got != tc.tag {
			t.Errorf("tag(%q, %q) = %q, want %q", tc.version, tc.build, got, tc.tag)
		}
	}
}

func TestResourceDownloadSourceAndFallback(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := &Server{store: db}
	dir := t.TempDir()
	t.Setenv("OBOARD_DOWNLOADS", dir)
	oldVersion, oldBuild, oldClient := version.AgentVersion, version.AgentBuild, resourceDownloadClient
	t.Cleanup(func() {
		version.AgentVersion, version.AgentBuild, resourceDownloadClient = oldVersion, oldBuild, oldClient
	})
	version.AgentVersion, version.AgentBuild = "dev-012345abcdef", "20260909010101"
	probeStatus, probes := http.StatusOK, 0
	resourceDownloadClient = &http.Client{Transport: resourceDownloadTransport(func(r *http.Request) (*http.Response, error) {
		probes++
		if r.Method != http.MethodHead || !strings.HasPrefix(r.URL.String(), "https://github.com/OboardProject/oboard-agent/releases/download/dev-012345abcdef-20260909010101/") {
			t.Errorf("unexpected GitHub probe: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: probeStatus, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	for _, name := range []string{"oboard-agent-linux-amd64", "oboard-sb-linux-arm64", "oboard-realm-linux-amd64", "release-manifest.json", "release-manifest.json.sig", "oboard-subscription-relay-linux-amd64.tar.gz", "oboard-subscription-relay-linux-arm64.tar.gz", "subscription-relay-sha256s.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("bundled:"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(path string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		s.downloadArtifact(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status {
			t.Fatalf("%s status = %d: %s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("download source must not be cached")
		}
		return w
	}
	check("/downloads/oboard-agent-linux-amd64", http.StatusOK)
	if probes != 0 {
		t.Fatal("default source contacted GitHub")
	}
	if err := db.SetSetting(context.Background(), resourceDownloadSourceSetting, "github"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"oboard-agent-linux-amd64", "oboard-sb-linux-arm64", "oboard-realm-linux-amd64"} {
		w := check("/downloads/"+name, http.StatusTemporaryRedirect)
		if w.Header().Get("Location") != resourceGitHubURL(name) {
			t.Fatal("redirect lost pinned version")
		}
	}
	before := probes
	for _, name := range []string{"release-manifest.json", "release-manifest.json.sig", "oboard-subscription-relay-linux-amd64.tar.gz", "oboard-subscription-relay-linux-arm64.tar.gz", "subscription-relay-sha256s.txt"} {
		w := check("/downloads/github/"+name, http.StatusOK)
		if w.Body.String() != "bundled:"+name {
			t.Fatal("trust metadata and relays must stay on Controller")
		}
	}
	check("/downloads/oboard-agent-linux-amd64?source=controller", http.StatusOK)
	if probes != before {
		t.Fatal("local resources must not contact GitHub")
	}
	probeStatus = http.StatusNotFound
	check("/downloads/oboard-agent-linux-amd64", http.StatusOK)
	probeStatus = http.StatusServiceUnavailable
	check("/downloads/oboard-agent-linux-amd64", http.StatusOK)
	if err := db.SetSetting(context.Background(), resourceDownloadSourceSetting, "controller"); err != nil {
		t.Fatal(err)
	}
	probeStatus = http.StatusOK
	check("/downloads/github/oboard-agent-linux-amd64", http.StatusTemporaryRedirect)
	check("/downloads/oboard-agent-linux-amd64", http.StatusOK)
}

type resourceDownloadGeoResolver struct {
	fakeConnectionAuditGeoResolver
	seen string
	fail bool
}

func (g *resourceDownloadGeoResolver) Lookup(ip string) (model.IPGeography, error) {
	g.seen = ip
	if g.fail {
		return model.IPGeography{}, errors.New("unavailable")
	}
	return g.geo, nil
}

func TestResourceDownloadMainlandPreference(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := &Server{store: db}
	oldVersion, oldBuild, oldClient := version.AgentVersion, version.AgentBuild, resourceDownloadClient
	t.Cleanup(func() {
		version.AgentVersion, version.AgentBuild, resourceDownloadClient = oldVersion, oldBuild, oldClient
	})
	version.AgentVersion, version.AgentBuild = "1.2.3", "20260909010101"
	probes := 0
	resourceDownloadClient = &http.Client{Transport: resourceDownloadTransport(func(r *http.Request) (*http.Response, error) {
		probes++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	t.Setenv("OBOARD_TRUSTED_PROXY_CIDRS", "")
	for _, tc := range []struct {
		name, source, enabled, country, peer, forwarded, seen string
		githubPath, missingGeo, failedGeo, redirect           bool
	}{
		{name: "disabled", source: "github", enabled: "false", country: "CN", peer: "1.2.3.4:123", redirect: true},
		{name: "default mainland", source: "github", country: "CN", peer: "1.2.3.4:123", seen: "1.2.3.4"},
		{name: "mainland", source: "github", enabled: "true", country: "CN", peer: "1.2.3.4:123", seen: "1.2.3.4"},
		{name: "explicit github", source: "controller", enabled: "true", country: "CN", peer: "1.2.3.4:123", seen: "1.2.3.4", githubPath: true},
		{name: "hong kong", source: "github", enabled: "true", country: "HK", peer: "1.2.3.4:123", seen: "1.2.3.4", redirect: true},
		{name: "macau", source: "github", enabled: "true", country: "MO", peer: "1.2.3.4:123", seen: "1.2.3.4", redirect: true},
		{name: "taiwan", source: "github", enabled: "true", country: "TW", peer: "1.2.3.4:123", seen: "1.2.3.4", redirect: true},
		{name: "unknown", source: "github", enabled: "true", peer: "1.2.3.4:123", seen: "1.2.3.4", redirect: true},
		{name: "missing database", source: "github", enabled: "true", peer: "1.2.3.4:123", missingGeo: true, redirect: true},
		{name: "failed lookup", source: "github", enabled: "true", country: "CN", peer: "1.2.3.4:123", seen: "1.2.3.4", failedGeo: true, redirect: true},
		{name: "trusted proxy", source: "github", enabled: "true", country: "CN", peer: "127.0.0.1:123", forwarded: "1.2.3.4", seen: "1.2.3.4"},
		{name: "untrusted header", source: "github", enabled: "true", country: "US", peer: "8.8.8.8:123", forwarded: "1.2.3.4", seen: "8.8.8.8", redirect: true},
		{name: "ipv6", source: "github", enabled: "true", country: "CN", peer: "[2400:3200::1]:123", seen: "2400:3200::1"},
		{name: "private ip", source: "github", enabled: "true", country: "CN", peer: "10.0.0.1:123", redirect: true},
		{name: "controller default", source: "controller", enabled: "true", country: "US", peer: "1.2.3.4:123"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range map[string]string{resourceDownloadSourceSetting: tc.source, resourceDownloadCNControllerSetting: tc.enabled} {
				if err := db.SetSetting(context.Background(), key, value); err != nil {
					t.Fatal(err)
				}
			}
			geo := &resourceDownloadGeoResolver{fakeConnectionAuditGeoResolver: fakeConnectionAuditGeoResolver{geo: model.IPGeography{CountryCode: tc.country}}, fail: tc.failedGeo}
			s.geoIP = geo
			if tc.missingGeo {
				s.geoIP = nil
			}
			path := "/downloads/"
			if tc.githubPath {
				path += "github/"
			}
			name := "oboard-agent-linux-amd64"
			r := httptest.NewRequest(http.MethodGet, path+name, nil)
			r.RemoteAddr = tc.peer
			r.Header.Set("X-Forwarded-For", tc.forwarded)
			before := probes
			if got := s.redirectResourceDownload(httptest.NewRecorder(), r, name); got != tc.redirect {
				t.Fatalf("redirect = %v, want %v", got, tc.redirect)
			}
			if geo.seen != tc.seen {
				t.Fatalf("looked up %q, want %q", geo.seen, tc.seen)
			}
			if !tc.redirect && probes != before {
				t.Fatal("Controller download contacted GitHub")
			}
		})
	}
}
