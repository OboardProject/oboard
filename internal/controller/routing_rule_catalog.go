package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	blackmatrixRuleCatalogAPI = "https://api.github.com/repos/blackmatrix7/ios_rule_script/git/trees/master?recursive=1"
	blackmatrixRawBaseURL     = "https://raw.githubusercontent.com/blackmatrix7/ios_rule_script/master/"
	blackmatrixCatalogTTL     = 15 * time.Minute
)

type blackmatrixCatalogEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int    `json:"size"`
}

type blackmatrixCatalogResponse struct {
	Tree []blackmatrixCatalogEntry `json:"tree"`
}

type routingRuleCatalogItem struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	URL      string `json:"url"`
	Format   string `json:"format"`
	Category string `json:"category"`
	Size     int    `json:"size"`
}

var blackmatrixCatalogCache struct {
	sync.Mutex
	at    time.Time
	items []routingRuleCatalogItem
}

func (s *Server) routingRuleCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) > 80 {
		fail(w, errors.New("q must not exceed 80 characters"), http.StatusBadRequest)
		return
	}
	items, err := s.blackmatrixRuleCatalog(r.Context())
	if err != nil {
		fail(w, err, http.StatusBadGateway)
		return
	}
	if query != "" {
		needle := strings.ToLower(query)
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Name+" "+item.Path+" "+item.Category), needle) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if len(items) > 100 {
		items = items[:100]
	}
	write(w, http.StatusOK, map[string]any{"items": items, "source": "blackmatrix7/ios_rule_script"})
}

func (s *Server) blackmatrixRuleCatalog(ctx context.Context) ([]routingRuleCatalogItem, error) {
	blackmatrixCatalogCache.Lock()
	if time.Since(blackmatrixCatalogCache.at) < blackmatrixCatalogTTL && blackmatrixCatalogCache.items != nil {
		items := append([]routingRuleCatalogItem(nil), blackmatrixCatalogCache.items...)
		blackmatrixCatalogCache.Unlock()
		return items, nil
	}
	blackmatrixCatalogCache.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blackmatrixRuleCatalogAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "OBoard-Controller")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("blackmatrix rule catalog unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blackmatrix rule catalog returned %s", response.Status)
	}
	var payload blackmatrixCatalogResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, errors.New("invalid blackmatrix rule catalog response")
	}
	items := make([]routingRuleCatalogItem, 0)
	for _, entry := range payload.Tree {
		if entry.Type != "blob" || !strings.HasSuffix(strings.ToLower(entry.Path), ".list") {
			continue
		}
		parts := strings.Split(entry.Path, "/")
		var provider, category string
		switch {
		case len(parts) >= 4 && parts[0] == "source" && parts[1] == "rule":
			provider, category = "source", parts[2]
		case len(parts) >= 3 && parts[0] == "rule":
			provider, category = parts[1], parts[2]
		default:
			continue
		}
		name := strings.TrimSuffix(parts[len(parts)-1], ".list")
		items = append(items, routingRuleCatalogItem{
			Name: name, Path: entry.Path, Category: provider + " · " + category, Size: entry.Size,
			URL:    blackmatrixRawBaseURL + entry.Path,
			Format: model.RoutingRuleSetFormatBlackmatrixClassical,
		})
	}
	sort.Slice(items, func(i, j int) bool { return strings.ToLower(items[i].Path) < strings.ToLower(items[j].Path) })
	blackmatrixCatalogCache.Lock()
	blackmatrixCatalogCache.at = time.Now()
	blackmatrixCatalogCache.items = append([]routingRuleCatalogItem(nil), items...)
	blackmatrixCatalogCache.Unlock()
	return items, nil
}
