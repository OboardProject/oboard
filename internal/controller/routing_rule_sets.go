package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/ruleset"
)

const routingRuleSetFetchTimeout = 15 * time.Second

const (
	routingRuleSetRefreshInterval = 24 * time.Hour
	routingRuleSetRefreshCheck    = time.Hour
)

type fetchedRoutingRuleSet struct {
	content      []byte
	revision     string
	etag         string
	lastModified string
	notModified  bool
}

func (s *Server) routingRuleSets(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimSuffix(r.URL.Path, "/")
	refresh := strings.HasSuffix(trimmed, "/refresh")
	base := strings.TrimSuffix(trimmed, "/refresh")
	id := idFromPath(base+"/", "/api/v1/routing-rule-sets/")
	if refresh {
		if r.Method != http.MethodPost || id <= 0 {
			method(w)
			return
		}
		item, changed, err := s.refreshRoutingRuleSet(r.Context(), id)
		if err != nil {
			fail(w, err, 400)
			return
		}
		auditReq(s, r, "refresh", "routing_rule_set", fmt.Sprint(id))
		write(w, 200, map[string]any{"routing_rule_set": item, "changed": changed})
		return
	}
	switch r.Method {
	case http.MethodGet:
		if id > 0 {
			item, err := s.store.GetRoutingRuleSet(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"routing_rule_set": item})
			return
		}
		items, err := s.store.ListRoutingRuleSets(r.Context())
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"routing_rule_sets": items})
	case http.MethodPost:
		var item model.RoutingRuleSet
		if !decode(w, r, &item) {
			return
		}
		if err := validateRoutingRuleSetInput(&item); err != nil {
			fail(w, err, 400)
			return
		}
		fetched, err := s.fetchRoutingRuleSetSnapshot(r.Context(), item, false)
		if err != nil {
			fail(w, err, 400)
			return
		}
		now := time.Now().UTC()
		item.Content, item.Revision = fetched.content, fetched.revision
		item.ETag, item.LastModified = fetched.etag, fetched.lastModified
		item.Status, item.LastAttemptAt, item.LastSuccessAt = model.RoutingRuleSetStatusReady, &now, &now
		if err := s.store.CreateRoutingRuleSet(r.Context(), &item); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "create", "routing_rule_set", fmt.Sprint(item.ID))
		write(w, 201, map[string]any{"routing_rule_set": item})
	case http.MethodPatch:
		if id <= 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		current, err := s.store.GetRoutingRuleSet(r.Context(), id)
		if err != nil {
			fail(w, err, 404)
			return
		}
		var input model.RoutingRuleSet
		if !decode(w, r, &input) {
			return
		}
		input.ID = id
		if err := validateRoutingRuleSetInput(&input); err != nil {
			fail(w, err, 400)
			return
		}
		input.Content, input.Revision = current.Content, current.Revision
		input.ETag, input.LastModified = current.ETag, current.LastModified
		input.Status, input.LastError = current.Status, current.LastError
		input.LastAttemptAt, input.LastSuccessAt = current.LastAttemptAt, current.LastSuccessAt
		contentChanged := false
		if input.URL != current.URL || input.Format != current.Format || input.MihomoBehavior != current.MihomoBehavior {
			fetched, fetchErr := s.fetchRoutingRuleSetSnapshot(r.Context(), input, false)
			if fetchErr != nil {
				fail(w, fetchErr, 400)
				return
			}
			now := time.Now().UTC()
			input.Content, input.Revision = fetched.content, fetched.revision
			input.ETag, input.LastModified = fetched.etag, fetched.lastModified
			input.Status, input.LastError = model.RoutingRuleSetStatusReady, ""
			input.LastAttemptAt, input.LastSuccessAt = &now, &now
			contentChanged = input.Revision != current.Revision
		}
		if err := s.store.UpdateRoutingRuleSet(r.Context(), &input); err != nil {
			fail(w, err, 500)
			return
		}
		if contentChanged {
			serverIDs, err := s.store.ListServerIDsReferencingRoutingRuleSet(r.Context(), id)
			if err != nil {
				fail(w, err, 500)
				return
			}
			if err := s.queueCoreConfigRefreshForServers(r.Context(), serverIDs, "routing_rule_set_updated"); err != nil {
				fail(w, err, 500)
				return
			}
		}
		auditReq(s, r, "update", "routing_rule_set", fmt.Sprint(id))
		write(w, 200, map[string]any{"routing_rule_set": input})
	case http.MethodDelete:
		if id <= 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		if err := s.store.DeleteRoutingRuleSet(r.Context(), id); err != nil {
			fail(w, err, 409)
			return
		}
		auditReq(s, r, "delete", "routing_rule_set", fmt.Sprint(id))
		write(w, 200, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func validateRoutingRuleSetInput(item *model.RoutingRuleSet) error {
	item.Name = strings.TrimSpace(item.Name)
	item.URL = strings.TrimSpace(item.URL)
	item.Format = strings.TrimSpace(item.Format)
	if item.Name == "" || len(item.Name) > 128 {
		return errors.New("name is required and must not exceed 128 characters")
	}
	parsed, err := url.Parse(item.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("url must be an HTTPS URL without credentials or fragment")
	}
	switch item.Format {
	case model.RoutingRuleSetFormatSingBoxSource, model.RoutingRuleSetFormatSingBoxBinary:
		item.MihomoBehavior = ""
	case model.RoutingRuleSetFormatMihomoDomain:
		item.MihomoBehavior = "domain"
	case model.RoutingRuleSetFormatMihomoIPCIDR:
		item.MihomoBehavior = "ipcidr"
	case model.RoutingRuleSetFormatMihomoClassical:
		item.MihomoBehavior = "classical"
	default:
		return errors.New("unsupported rule set format; Mihomo .mrs is not supported")
	}
	return nil
}

func (s *Server) fetchRoutingRuleSet(ctx context.Context, item model.RoutingRuleSet, conditional bool) (*fetchedRoutingRuleSet, error) {
	target, err := url.Parse(item.URL)
	if err != nil {
		return nil, err
	}
	if err := s.assertPublicHost(ctx, target); err != nil {
		return nil, errors.New(strings.NewReplacer("client metadata", "routing rule set", "client_id", "url").Replace(err.Error()))
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: dialPublicMetadataHost}
	client := &http.Client{Timeout: routingRuleSetFetchTimeout, Transport: transport, CheckRedirect: routingRuleSetRedirectPolicy}
	return fetchRoutingRuleSetHTTP(ctx, item, conditional, client)
}

func (s *Server) fetchRoutingRuleSetSnapshot(ctx context.Context, item model.RoutingRuleSet, conditional bool) (*fetchedRoutingRuleSet, error) {
	if s.routingRuleSetFetcher != nil {
		return s.routingRuleSetFetcher(ctx, item, conditional)
	}
	return s.fetchRoutingRuleSet(ctx, item, conditional)
}

func routingRuleSetRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) == 0 || !sameOrigin(via[0].URL, request.URL) {
		return errors.New("routing rule set redirect crossed origins")
	}
	if len(via) >= 10 {
		return errors.New("routing rule set returned too many redirects")
	}
	return nil
}

func fetchRoutingRuleSetHTTP(ctx context.Context, item model.RoutingRuleSet, conditional bool, client *http.Client) (*fetchedRoutingRuleSet, error) {
	target, err := url.Parse(item.URL)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil) // #nosec G704 -- HTTPS-only URL and each dial is checked against public IP ranges.
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json, application/octet-stream, text/yaml, text/plain")
	if conditional {
		request.Header.Set("If-None-Match", item.ETag)
		request.Header.Set("If-Modified-Since", item.LastModified)
	}
	response, err := client.Do(request) // #nosec G704 -- dialPublicMetadataHost validates DNS on every connection.
	if err != nil {
		return nil, fmt.Errorf("routing rule set is unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified && conditional {
		return &fetchedRoutingRuleSet{notModified: true, revision: item.Revision, etag: item.ETag, lastModified: item.LastModified}, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("routing rule set returned status " + response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, ruleset.MaxContentSize+1))
	if err != nil {
		return nil, errors.New("routing rule set could not be read")
	}
	if len(body) > ruleset.MaxContentSize {
		return nil, errors.New("routing rule set exceeds the 8 MiB limit")
	}
	converted, err := ruleset.Convert(item.Format, body)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(converted)
	return &fetchedRoutingRuleSet{content: converted, revision: hex.EncodeToString(sum[:]), etag: response.Header.Get("ETag"), lastModified: response.Header.Get("Last-Modified")}, nil
}

func (s *Server) refreshRoutingRuleSet(ctx context.Context, id int64) (*model.RoutingRuleSet, bool, error) {
	item, err := s.store.GetRoutingRuleSet(ctx, id)
	if err != nil {
		return nil, false, err
	}
	attemptedAt := time.Now().UTC()
	fetched, fetchErr := s.fetchRoutingRuleSetSnapshot(ctx, *item, true)
	item.LastAttemptAt = &attemptedAt
	if fetchErr != nil {
		item.Status = model.RoutingRuleSetStatusError
		item.LastError = fetchErr.Error()
		if err := s.store.UpdateRoutingRuleSet(ctx, item); err != nil {
			return nil, false, err
		}
		return item, false, fetchErr
	}
	item.Status, item.LastError = model.RoutingRuleSetStatusReady, ""
	item.LastSuccessAt = &attemptedAt
	changed := !fetched.notModified && fetched.revision != item.Revision
	if !fetched.notModified {
		item.Content, item.Revision = fetched.content, fetched.revision
		item.ETag, item.LastModified = fetched.etag, fetched.lastModified
	}
	if err := s.store.UpdateRoutingRuleSet(ctx, item); err != nil {
		return nil, false, err
	}
	if changed {
		serverIDs, err := s.store.ListServerIDsReferencingRoutingRuleSet(ctx, id)
		if err != nil {
			return item, true, err
		}
		if err := s.queueCoreConfigRefreshForServers(ctx, serverIDs, "routing_rule_set_updated"); err != nil {
			return item, true, err
		}
	}
	return item, changed, nil
}

func (s *Server) StartRoutingRuleSetRefresh(ctx context.Context) {
	s.refreshDueRoutingRuleSets(ctx, time.Now().UTC())
	ticker := time.NewTicker(routingRuleSetRefreshCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.refreshDueRoutingRuleSets(ctx, now.UTC())
		}
	}
}

func (s *Server) refreshDueRoutingRuleSets(ctx context.Context, now time.Time) {
	items, err := s.store.ListRoutingRuleSets(ctx)
	if err != nil {
		log.Printf("list routing rule sets for refresh: %v", err)
		return
	}
	cutoff := now.Add(-routingRuleSetRefreshInterval)
	for _, item := range items {
		if item.LastAttemptAt != nil && item.LastAttemptAt.After(cutoff) {
			continue
		}
		if _, _, err := s.refreshRoutingRuleSet(ctx, item.ID); err != nil {
			log.Printf("refresh routing rule set %d: %v", item.ID, err)
		}
	}
}

func (s *Server) placeRoutingRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var request struct {
		ProxyPathID int64                        `json:"proxy_path_id"`
		Placements  []model.RoutingRulePlacement `json:"placements"`
	}
	if !decode(w, r, &request) {
		return
	}
	before, err := s.routingRuleServerIDsForPath(r.Context(), request.ProxyPathID)
	if err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.store.PlaceRoutingRules(r.Context(), request.ProxyPathID, request.Placements); err != nil {
		fail(w, err, 400)
		return
	}
	after, err := s.routingRuleServerIDsForPath(r.Context(), request.ProxyPathID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	for id := range after {
		before[id] = true
	}
	serverIDs := make([]int64, 0, len(before))
	for id := range before {
		serverIDs = append(serverIDs, id)
	}
	if err := s.queueCoreConfigRefreshForServers(r.Context(), serverIDs, "routing_rules_placed"); err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "place", "routing_rules", fmt.Sprint(request.ProxyPathID))
	items, err := s.store.ListRoutingRules(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"routing_rules": items})
}

func (s *Server) routingRuleServerIDsForPath(ctx context.Context, pathID int64) (map[int64]bool, error) {
	items, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return nil, err
	}
	serverIDs := map[int64]bool{}
	for _, item := range items {
		if item.Scope == model.RoutingRuleScopePathStage && item.ProxyPathID != nil && *item.ProxyPathID == pathID && item.ServerID > 0 {
			serverIDs[item.ServerID] = true
		}
	}
	return serverIDs, nil
}
