package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	clientMetadataBodyLimit    = 256 << 10
	clientMetadataFetchTimeout = 5 * time.Second
	clientMetadataMaxAge       = 24 * time.Hour
)

// clientMetadataDocument is the CIMD body. It may declare only identity and
// callbacks; it can never declare OBoard business permission ceilings and can
// never override OBoard consent copy.
type clientMetadataDocument struct {
	ClientID     string   `json:"client_id"`
	ClientName   string   `json:"client_name"`
	RedirectURIs []string `json:"redirect_uris"`
	LogoURI      string   `json:"logo_uri"`
	ClientURI    string   `json:"client_uri"`
}

type clientMetadata struct {
	hash         string
	etag         string
	lastModified string
	fetchedAt    *time.Time
	redirectURIs []string
	clientName   string
}

// fetchClientMetadata fetches and validates a Client ID Metadata Document with
// SSRF, DNS-rebinding, size, and timeout protections. The document's client_id
// must exactly match the requested URL.
func (s *Server) fetchClientMetadata(ctx context.Context, raw string) (*clientMetadata, error) {
	documentURL, err := parseClientMetadataURL(raw)
	if err != nil {
		return nil, err
	}
	if err := s.assertPublicHost(ctx, documentURL); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		DisableKeepAlives: true,
	}
	// Default deny redirects; the only allowed redirect is same-origin and the
	// final URL is re-validated before the body is trusted.
	client := &http.Client{Timeout: clientMetadataFetchTimeout, Transport: transport}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, documentURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("client metadata is unavailable: " + err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("client metadata returned status " + response.Status)
	}
	final := response.Request.URL
	if final != nil && final.String() != documentURL.String() {
		if !sameOrigin(documentURL, final) {
			return nil, errors.New("client metadata redirect crossed origins")
		}
		if err := s.assertPublicHost(ctx, final); err != nil {
			return nil, err
		}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, clientMetadataBodyLimit+1))
	if err != nil {
		return nil, errors.New("client metadata could not be read")
	}
	if len(body) > clientMetadataBodyLimit {
		return nil, errors.New("client metadata exceeds the 256 KiB limit")
	}
	var document clientMetadataDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return nil, errors.New("client metadata is not valid JSON")
	}
	if document.ClientID != documentURL.String() {
		return nil, errors.New("client_id in the metadata document does not exactly match the requested URL")
	}
	redirectURIs, err := validateRedirectURIs(document.RedirectURIs)
	if err != nil {
		return nil, err
	}
	metadata := &clientMetadata{
		etag:         response.Header.Get("ETag"),
		lastModified: response.Header.Get("Last-Modified"),
		fetchedAt:    timePtr(time.Now().UTC()),
		redirectURIs: redirectURIs,
		clientName:   strings.TrimSpace(document.ClientName),
	}
	if metadata.clientName == "" {
		metadata.clientName = "Remote MCP Client"
	}
	sum := sha256.Sum256(body)
	metadata.hash = hex.EncodeToString(sum[:])
	return metadata, nil
}

func timePtr(value time.Time) *time.Time { return &value }

func parseClientMetadataURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("client_id must be an HTTPS URL")
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" || path == "/" {
		return nil, errors.New("client_id must include a concrete path, not a bare host")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && ip.IsLoopback() {
		return nil, errors.New("client_id must not use a loopback address")
	}
	return parsed, nil
}

// assertPublicHost resolves the host and rejects any address that is loopback,
// RFC1918 private, link-local, multicast, unspecified, or a well-known cloud
// metadata endpoint. Resolving first and checking every returned address
// defends against DNS rebinding.
func (s *Server) assertPublicHost(ctx context.Context, target *url.URL) error {
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(target.Hostname()), "."))
	if host == "" {
		return errors.New("client metadata host is empty")
	}
	if strings.Contains(host, "/") || strings.Contains(host, "\\") {
		return errors.New("client metadata host is invalid")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return errors.New("client metadata host does not resolve")
	}
	for _, address := range addresses {
		ip, ok := netip.AddrFromSlice(address.IP)
		if !ok {
			return errors.New("client metadata host resolved to an invalid address")
		}
		ip = ip.Unmap()
		if forbiddenClientMetadataIP(ip) {
			return fmt.Errorf("client metadata host %q resolves to a forbidden address", host)
		}
	}
	return nil
}

func forbiddenClientMetadataIP(ip netip.Addr) bool {
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	if ip.Is4() {
		if ip.IsPrivate() {
			return true
		}
		value := ip.As4()
		// 169.254.169.254 and 169.254.170.x cloud metadata.
		if value[0] == 169 && value[1] == 254 {
			return true
		}
		// 100.100.100.200 Aliyun metadata.
		if value == [4]byte{100, 100, 100, 200} {
			return true
		}
	}
	// IPv6 unique-local and ULA private ranges.
	if ip.Is6() && ip.IsPrivate() {
		return true
	}
	// Well-known AWS IPv6 metadata.
	if ip == netip.MustParseAddr("fd00:ec2::254") {
		return true
	}
	return false
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func validateRedirectURIs(redirectURIs []string) ([]string, error) {
	if len(redirectURIs) == 0 || len(redirectURIs) > 16 {
		return nil, errors.New("redirect_uris must contain between 1 and 16 exact URIs")
	}
	seen := map[string]bool{}
	validated := make([]string, 0, len(redirectURIs))
	for _, raw := range redirectURIs {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Fragment != "" || u.User != nil || u.Host == "" {
			return nil, errors.New("redirect URIs must be absolute and must not contain a fragment or user info")
		}
		if u.Scheme != "https" {
			if u.Scheme != "http" || !isLoopbackRedirectHost(u.Hostname()) {
				return nil, errors.New("non-loopback redirect URIs must use HTTPS")
			}
		}
		if u.Scheme == "https" && isLoopbackRedirectHost(u.Hostname()) && u.Port() == "" {
			return nil, errors.New("loopback HTTPS redirect URIs require an explicit port")
		}
		canonical := u.String()
		if !seen[canonical] {
			seen[canonical] = true
			validated = append(validated, canonical)
		}
	}
	return validated, nil
}

// refreshClientMetadataIfStale re-validates a CIMD client's metadata before
// authorization when the cache is stale. Redirect URI changes require a fresh
// fetch and a re-consent (existing grants stay but are marked for re-consent).
func (s *Server) refreshClientMetadataIfStale(ctx context.Context, client *model.OAuthClient) error {
	if client.IdentityType != "cimd" || client.MetadataURI == "" {
		return nil
	}
	if client.MetadataFetchedAt != nil && time.Since(*client.MetadataFetchedAt) < clientMetadataMaxAge {
		return nil
	}
	metadata, err := s.fetchClientMetadata(ctx, client.MetadataURI)
	if err != nil {
		return err
	}
	redirectsChanged := !slices.Equal(client.RedirectURIs, metadata.redirectURIs)
	client.MetadataHash = metadata.hash
	client.MetadataETag = metadata.etag
	client.MetadataFetchedAt = metadata.fetchedAt
	client.RedirectURIs = metadata.redirectURIs
	if client.Name == "" || client.Name == "Remote MCP Client" {
		client.Name = metadata.clientName
	}
	if err := s.store.UpdateOAuthClient(ctx, client); err != nil {
		return err
	}
	if redirectsChanged {
		if err := s.markClientGrantsForReconsent(ctx, client.ID, "client metadata redirect URIs changed"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) markClientGrantsForReconsent(ctx context.Context, clientID, reason string) error {
	grants, err := s.store.ListOAuthGrants(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, grant := range grants {
		if grant.ClientID != clientID || grant.Status != model.OAuthGrantActive {
			continue
		}
		if err := s.store.MarkOAuthGrantNeedsReconsent(ctx, grant.ID, now, reason); err != nil {
			return err
		}
	}
	return nil
}
