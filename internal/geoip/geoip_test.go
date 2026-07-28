package geoip

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeProvince(t *testing.T) {
	tests := map[string]string{
		"广东省":        "广东",
		"北京市":        "北京",
		"广西壮族自治区":    "广西",
		"新疆维吾尔自治区":   "新疆",
		"香港特别行政区":    "香港",
		"California": "California",
	}
	for input, want := range tests {
		if got := normalizeProvince(input); got != want {
			t.Fatalf("normalizeProvince(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPublicSourceIPExcludesLocalAndDocumentationRanges(t *testing.T) {
	for _, raw := range []string{"10.0.0.1", "100.64.0.1", "192.0.2.1", "198.51.100.2", "203.0.113.3", "2001:db8::1", "::1"} {
		if isPublicSourceIP(netip.MustParseAddr(raw)) {
			t.Fatalf("%s was treated as a public source IP", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !isPublicSourceIP(netip.MustParseAddr(raw)) {
			t.Fatalf("%s was not treated as a public source IP", raw)
		}
	}
}

func TestOpenRejectsDatabaseChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	metadata := manifest{
		Provider: "ip2region", Version: "test", Revision: "test",
		IPv4: manifestFile{Name: "v4.xdb", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
		IPv6: manifestFile{Name: "v6.xdb", SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v4.xdb"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
}

func TestPinnedDatabaseLookup(t *testing.T) {
	dir := os.Getenv("OBOARD_TEST_GEOIP_DIR")
	if dir == "" {
		t.Skip("OBOARD_TEST_GEOIP_DIR is not set")
	}
	database, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, raw := range []string{"1.1.1.1", "2606:4700:4700::1111"} {
		geo, err := database.Lookup(raw)
		if err != nil {
			t.Fatalf("lookup %s: %v", raw, err)
		}
		if geo.CountryCode == "" || geo.Revision != database.Status().Revision {
			t.Fatalf("lookup %s = %#v", raw, geo)
		}
	}
}
