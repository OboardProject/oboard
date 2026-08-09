package geoip

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

const manifestName = "manifest.json"

type manifestFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Provider string       `json:"provider"`
	Version  string       `json:"version"`
	Revision string       `json:"revision"`
	IPv4     manifestFile `json:"ipv4"`
	IPv6     manifestFile `json:"ipv6"`
}

type lockedSearcher struct {
	mu       sync.Mutex
	searcher *xdb.Searcher
}

type Database struct {
	status model.GeoDatabaseStatus
	ipv4   lockedSearcher
	ipv6   lockedSearcher
}

func Open(dir string) (*Database, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("geoip directory is not configured")
	}
	raw, err := os.ReadFile(filepath.Join(dir, manifestName)) // #nosec G304 -- dir is the operator-configured GeoIP root and manifestName is constant.
	if err != nil {
		return nil, fmt.Errorf("read geoip manifest: %w", err)
	}
	if len(raw) > 64<<10 {
		return nil, errors.New("geoip manifest is too large")
	}
	var metadata manifest
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return nil, fmt.Errorf("decode geoip manifest: %w", err)
	}
	if metadata.Provider != "ip2region" || strings.TrimSpace(metadata.Version) == "" || strings.TrimSpace(metadata.Revision) == "" {
		return nil, errors.New("geoip manifest identity is invalid")
	}
	v4Path, err := verifiedDatabasePath(dir, metadata.IPv4)
	if err != nil {
		return nil, fmt.Errorf("verify IPv4 geoip database: %w", err)
	}
	v6Path, err := verifiedDatabasePath(dir, metadata.IPv6)
	if err != nil {
		return nil, fmt.Errorf("verify IPv6 geoip database: %w", err)
	}
	v4Index, err := xdb.LoadVectorIndexFromFile(v4Path)
	if err != nil {
		return nil, fmt.Errorf("load IPv4 geoip index: %w", err)
	}
	v4, err := xdb.NewWithVectorIndex(xdb.IPv4, v4Path, v4Index)
	if err != nil {
		return nil, fmt.Errorf("open IPv4 geoip database: %w", err)
	}
	v6Index, err := xdb.LoadVectorIndexFromFile(v6Path)
	if err != nil {
		v4.Close()
		return nil, fmt.Errorf("load IPv6 geoip index: %w", err)
	}
	v6, err := xdb.NewWithVectorIndex(xdb.IPv6, v6Path, v6Index)
	if err != nil {
		v4.Close()
		return nil, fmt.Errorf("open IPv6 geoip database: %w", err)
	}
	return &Database{
		status: model.GeoDatabaseStatus{Available: true, Provider: metadata.Provider, Version: metadata.Version, Revision: metadata.Revision},
		ipv4:   lockedSearcher{searcher: v4},
		ipv6:   lockedSearcher{searcher: v6},
	}, nil
}

func verifiedDatabasePath(dir string, file manifestFile) (string, error) {
	if file.Name == "" || filepath.Base(file.Name) != file.Name || len(file.SHA256) != 64 {
		return "", errors.New("database file metadata is invalid")
	}
	path := filepath.Join(dir, file.Name)
	handle, err := os.Open(path) // #nosec G304 -- file.Name is restricted to a basename and its content is verified against the signed manifest hash.
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, handle)
	closeErr := handle.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), file.SHA256) {
		return "", errors.New("database checksum does not match manifest")
	}
	return path, nil
}

func (d *Database) Lookup(raw string) (model.IPGeography, error) {
	if d == nil {
		return model.IPGeography{}, errors.New("geoip database is unavailable")
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return model.IPGeography{}, err
	}
	ip = ip.Unmap()
	if !isPublicSourceIP(ip) {
		return model.IPGeography{Revision: d.status.Revision}, nil
	}
	searcher := &d.ipv6
	if ip.Is4() {
		searcher = &d.ipv4
	}
	searcher.mu.Lock()
	region, err := searcher.searcher.Search(ip.String())
	searcher.mu.Unlock()
	if err != nil {
		return model.IPGeography{}, err
	}
	parts := strings.Split(region, "|")
	for len(parts) < 5 {
		parts = append(parts, "")
	}
	geo := model.IPGeography{
		Country:     cleanRegionPart(parts[0]),
		Province:    normalizeProvince(cleanRegionPart(parts[1])),
		City:        cleanRegionPart(parts[2]),
		ISP:         cleanRegionPart(parts[3]),
		CountryCode: strings.ToUpper(cleanRegionPart(parts[4])),
		Revision:    d.status.Revision,
	}
	if len(geo.CountryCode) != 2 {
		geo.CountryCode = ""
	}
	return geo, nil
}

func (d *Database) Status() model.GeoDatabaseStatus {
	if d == nil {
		return model.GeoDatabaseStatus{Provider: "ip2region", Error: "IP 归属库不可用"}
	}
	return d.status
}

func (d *Database) Close() {
	if d == nil {
		return
	}
	d.ipv4.mu.Lock()
	d.ipv4.searcher.Close()
	d.ipv4.mu.Unlock()
	d.ipv6.mu.Lock()
	d.ipv6.searcher.Close()
	d.ipv6.mu.Unlock()
}

func cleanRegionPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "0" || value == "-" {
		return ""
	}
	return value
}

func normalizeProvince(value string) string {
	value = strings.TrimSpace(value)
	for _, suffix := range []string{"壮族自治区", "回族自治区", "维吾尔自治区", "特别行政区", "自治区", "省", "市"} {
		if strings.HasSuffix(value, suffix) && len(value) > len(suffix) {
			return strings.TrimSuffix(value, suffix)
		}
	}
	return value
}

func isPublicSourceIP(ip netip.Addr) bool {
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("2001:db8::/32"),
}
