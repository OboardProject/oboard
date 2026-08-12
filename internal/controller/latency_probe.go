package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	defaultProbeResourceURL     = "https://raw.githubusercontent.com/OboardProject/resource/main/probe/probe-targets-cn.json"
	latencyProbeCacheTTL        = 6 * time.Hour
	latencyProbeRefreshCooldown = 30 * time.Second
)

type latencyProbeResource struct {
	Version   string                         `json:"version"`
	UpdatedAt time.Time                      `json:"updated_at"`
	Provinces map[string]map[string][]string `json:"provinces"`
}

var latencyProbeCache struct {
	sync.Mutex
	resource   latencyProbeResource
	fetched    time.Time
	attempted  time.Time
	refreshing bool
	refreshed  chan struct{}
	lastError  error
}

var latencyProbeResourceFetcher = fetchLatencyProbeResource

func loadLatencyProbeResource(ctx context.Context, force bool) (latencyProbeResource, error) {
	latencyProbeCache.Lock()
	cached := latencyProbeCache.resource
	hasCached := len(cached.Provinces) > 0
	age := time.Since(latencyProbeCache.fetched)
	sinceAttempt := time.Since(latencyProbeCache.attempted)
	if hasCached && ((!force && age < latencyProbeCacheTTL) || sinceAttempt < latencyProbeRefreshCooldown) {
		latencyProbeCache.Unlock()
		return cached, nil
	}
	if latencyProbeCache.refreshing {
		refreshed := latencyProbeCache.refreshed
		latencyProbeCache.Unlock()
		select {
		case <-ctx.Done():
			if hasCached {
				return cached, nil
			}
			return latencyProbeResource{}, errors.New("区域延迟测试资源更新已取消")
		case <-refreshed:
			latencyProbeCache.Lock()
			resource := latencyProbeCache.resource
			lastError := latencyProbeCache.lastError
			latencyProbeCache.Unlock()
			if len(resource.Provinces) > 0 {
				return resource, nil
			}
			if lastError != nil {
				return latencyProbeResource{}, errors.New("暂时无法更新区域延迟测试资源，请稍后重试")
			}
			return latencyProbeResource{}, errors.New("区域延迟测试资源暂不可用")
		}
	}
	latencyProbeCache.refreshing = true
	latencyProbeCache.refreshed = make(chan struct{})
	latencyProbeCache.Unlock()

	resource, err := latencyProbeResourceFetcher(ctx)
	latencyProbeCache.Lock()
	latencyProbeCache.attempted = time.Now()
	if err == nil {
		latencyProbeCache.resource = resource
		latencyProbeCache.fetched = time.Now()
	}
	latencyProbeCache.lastError = err
	latencyProbeCache.refreshing = false
	close(latencyProbeCache.refreshed)
	latencyProbeCache.refreshed = nil
	latencyProbeCache.Unlock()
	if err != nil {
		if hasCached {
			return cached, nil
		}
		return latencyProbeResource{}, errors.New("暂时无法更新区域延迟测试资源，请稍后重试")
	}
	return resource, nil
}

func fetchLatencyProbeResource(ctx context.Context) (latencyProbeResource, error) {
	url := strings.TrimSpace(os.Getenv("OBOARD_PROBE_RESOURCE_URL"))
	if url == "" {
		url = defaultProbeResourceURL
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return latencyProbeResource{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return latencyProbeResource{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return latencyProbeResource{}, fmt.Errorf("probe resource returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return latencyProbeResource{}, err
	}
	resource, err := parseLatencyProbeResource(body, time.Now().UTC())
	if err != nil {
		return latencyProbeResource{}, err
	}
	return resource, nil
}

func parseLatencyProbeResource(body []byte, updatedAt time.Time) (latencyProbeResource, error) {
	var raw map[string]map[string][]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return latencyProbeResource{}, fmt.Errorf("invalid probe resource: %w", err)
	}
	if len(raw) == 0 || len(raw) > 40 {
		return latencyProbeResource{}, errors.New("probe resource has invalid province count")
	}
	for province, carriers := range raw {
		if strings.TrimSpace(province) == "" || len(carriers) > 8 {
			return latencyProbeResource{}, errors.New("probe resource has invalid province")
		}
		for carrier, ips := range carriers {
			if strings.TrimSpace(carrier) == "" || len(ips) > 64 {
				return latencyProbeResource{}, errors.New("probe resource has invalid carrier")
			}
			for _, value := range ips {
				addr, parseErr := netip.ParseAddr(strings.TrimSpace(value))
				if parseErr != nil || !validLatencyProbeResourceIPv4(addr) {
					return latencyProbeResource{}, fmt.Errorf("probe resource contains invalid IPv4 %q", value)
				}
			}
		}
	}
	sum := sha256.Sum256(body)
	version := hex.EncodeToString(sum[:8])
	return latencyProbeResource{Version: version, UpdatedAt: updatedAt.UTC(), Provinces: raw}, nil
}

func validLatencyProbeResourceIPv4(addr netip.Addr) bool {
	return addr.IsValid() && addr.Is4() && addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() && !addr.IsLinkLocalMulticast() && !addr.IsMulticast() && !addr.IsUnspecified()
}

func latencyProbeTargets(resource latencyProbeResource, server model.Server) []model.LatencyProbeTarget {
	provinces := map[string]bool{}
	for _, value := range server.LatencyProbeProvinces {
		provinces[value] = true
	}
	carriers := map[string]bool{}
	for _, value := range server.LatencyProbeCarriers {
		carriers[value] = true
	}
	provinceNames := make([]string, 0, len(resource.Provinces))
	for province := range resource.Provinces {
		if len(provinces) == 0 || provinces[province] {
			provinceNames = append(provinceNames, province)
		}
	}
	sort.Strings(provinceNames)
	type targetGroup struct {
		province string
		carrier  string
		ips      []string
	}
	carriersByProvince := make(map[string][]string, len(provinceNames))
	maxCarriers := 0
	for _, province := range provinceNames {
		carrierNames := make([]string, 0, len(resource.Provinces[province]))
		for carrier := range resource.Provinces[province] {
			if len(carriers) == 0 || carriers[carrier] {
				carrierNames = append(carrierNames, carrier)
			}
		}
		sort.Strings(carrierNames)
		carriersByProvince[province] = carrierNames
		if len(carrierNames) > maxCarriers {
			maxCarriers = len(carrierNames)
		}
	}
	groups := make([]targetGroup, 0)
	for carrierRound := 0; carrierRound < maxCarriers; carrierRound++ {
		for provinceIndex, province := range provinceNames {
			carrierNames := carriersByProvince[province]
			if carrierRound >= len(carrierNames) {
				continue
			}
			carrier := carrierNames[(provinceIndex+carrierRound)%len(carrierNames)]
			groups = append(groups, targetGroup{province: province, carrier: carrier, ips: resource.Provinces[province][carrier]})
		}
	}
	items := []model.LatencyProbeTarget{}
	for ipIndex := 0; ; ipIndex++ {
		added := false
		for _, group := range groups {
			if ipIndex >= len(group.ips) {
				continue
			}
			added = true
			items = append(items, model.LatencyProbeTarget{ProbeID: fmt.Sprintf("%s-%s-%d", group.province, group.carrier, ipIndex), Province: group.province, Carrier: group.carrier, IP: group.ips[ipIndex]})
			if server.LatencyProbeMaxTargets > 0 && len(items) >= server.LatencyProbeMaxTargets {
				return items
			}
		}
		if !added {
			break
		}
	}
	return items
}

func (s *Server) serverLatencyProbe(w http.ResponseWriter, r *http.Request, serverID int64) {
	server, err := s.store.GetServer(r.Context(), serverID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		resource, resourceErr := loadLatencyProbeResource(r.Context(), false)
		results, resultErr := s.store.ListLatencyProbeResults(r.Context(), serverID, intQuery(r, "limit", 512))
		if resultErr != nil {
			fail(w, resultErr, 500)
			return
		}
		provinces := []string{}
		carriers := map[string]bool{}
		if resourceErr == nil {
			for province, entries := range resource.Provinces {
				provinces = append(provinces, province)
				for carrier := range entries {
					carriers[carrier] = true
				}
			}
			sort.Strings(provinces)
		}
		carrierList := make([]string, 0, len(carriers))
		for carrier := range carriers {
			carrierList = append(carrierList, carrier)
		}
		sort.Strings(carrierList)
		write(w, 200, map[string]any{"server": server, "resource_version": resource.Version, "resource_updated_at": resource.UpdatedAt, "resource_error": errorString(resourceErr), "provinces": provinces, "carriers": carrierList, "results": results})
	case http.MethodPost:
		if !server.LatencyProbeEnabled {
			fail(w, errors.New("服务器未启用区域延迟测试"), http.StatusConflict)
			return
		}
		if reason := agentTaskImmediateFailure(server); reason != "" {
			fail(w, errors.New(reason), http.StatusConflict)
			return
		}
		if latencyProbeAgentUpgradeRequired(*server) {
			fail(w, errors.New("服务器 Agent 版本过旧，请先更新 Agent 后再执行区域延迟测试"), http.StatusConflict)
			return
		}
		resource, err := loadLatencyProbeResource(r.Context(), true)
		if err != nil {
			fail(w, err, 503)
			return
		}
		targets := latencyProbeTargets(resource, *server)
		if len(targets) == 0 {
			fail(w, errors.New("没有匹配的省份或运营商探针"), 400)
			return
		}
		task, existing, err := s.queueLatencyProbeTask(r.Context(), server, resource, targets)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 202, map[string]any{"task_id": task.ID, "task_status": task.Status, "existing": existing, "resource_version": resource.Version, "target_count": latencyProbeTaskTargetCount(task, len(targets))})
	default:
		method(w)
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Server) latencyProbeResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	resource, err := loadLatencyProbeResource(r.Context(), false)
	if err != nil {
		fail(w, err, http.StatusServiceUnavailable)
		return
	}
	provinces := make([]string, 0, len(resource.Provinces))
	carriers := map[string]bool{}
	for province, entries := range resource.Provinces {
		provinces = append(provinces, province)
		for carrier := range entries {
			carriers[carrier] = true
		}
	}
	sort.Strings(provinces)
	carrierList := make([]string, 0, len(carriers))
	for carrier := range carriers {
		carrierList = append(carrierList, carrier)
	}
	sort.Strings(carrierList)
	write(w, http.StatusOK, map[string]any{
		"resource_version":    resource.Version,
		"resource_updated_at": resource.UpdatedAt,
		"provinces":           provinces,
		"carriers":            carrierList,
	})
}

func (s *Server) applyLatencyProbeTaskResult(ctx context.Context, serverID int64, task model.AgentTask, status, resultJSON string) error {
	if status != "succeeded" && strings.TrimSpace(resultJSON) == "" {
		return nil
	}
	var report model.LatencyProbeResultReport
	if err := json.Unmarshal([]byte(resultJSON), &report); err != nil {
		return err
	}
	var plan model.LatencyProbeTargetsPlan
	if err := json.Unmarshal([]byte(task.PayloadJSON), &plan); err != nil {
		return errors.New("latency probe task payload is invalid")
	}
	if report.ResourceVersion == "" {
		report.ResourceVersion = plan.ResourceVersion
	}
	if report.ResourceVersion != plan.ResourceVersion || len(report.Items) == 0 || len(report.Items) > len(plan.Targets) || len(report.Items) > 256 {
		return errors.New("latency probe result does not match task resource")
	}
	expected := make(map[string]model.LatencyProbeTarget, len(plan.Targets))
	for _, target := range plan.Targets {
		expected[target.ProbeID] = target
	}
	seen := make(map[string]bool, len(report.Items))
	for _, item := range report.Items {
		target, ok := expected[item.ProbeID]
		if !ok || seen[item.ProbeID] || item.Province != target.Province || item.Carrier != target.Carrier || item.IP != target.IP {
			return errors.New("latency probe result contains an unexpected target")
		}
		if item.SampleCount < 1 || item.SampleCount > 10 || item.SuccessCount < 0 || item.SuccessCount > item.SampleCount || item.Available != (item.SuccessCount > 0) || item.LatencyMS < 0 || item.MinLatencyMS < 0 || item.P95LatencyMS < 0 || item.JitterMS < 0 {
			return errors.New("latency probe result contains invalid statistics")
		}
		if item.SuccessCount == 0 && (item.LatencyMS != 0 || item.MinLatencyMS != 0 || item.P95LatencyMS != 0 || item.JitterMS != 0) {
			return errors.New("latency probe result contains statistics without successful samples")
		}
		seen[item.ProbeID] = true
	}
	report.CheckedAt = time.Now().UTC()
	return s.store.SaveLatencyProbeResults(ctx, serverID, report)
}

func (s *Server) enqueueConfiguredLatencyProbe(ctx context.Context, server model.Server, force bool) error {
	if !server.LatencyProbeEnabled || strings.TrimSpace(server.AgentID) == "" || server.Status == model.ServerOffline || latencyProbeAgentUpgradeRequired(server) {
		return nil
	}
	resource, err := loadLatencyProbeResource(ctx, false)
	if err != nil {
		return err
	}
	if !force && server.LatencyProbeResourceVersion == resource.Version {
		results, _ := s.store.ListLatencyProbeResults(ctx, server.ID, 1)
		if len(results) > 0 && time.Since(results[0].CheckedAt) < time.Duration(server.LatencyProbeIntervalSeconds)*time.Second {
			return nil
		}
		latest, latestErr := s.store.LatestTaskByServerType(ctx, server.ID, model.AgentTaskTypeProbeLatencyTargets)
		if latestErr == nil && time.Since(latest.CreatedAt) < time.Duration(server.LatencyProbeIntervalSeconds)*time.Second {
			return nil
		}
		if latestErr != nil && !errors.Is(latestErr, sql.ErrNoRows) {
			return latestErr
		}
	}
	targets := latencyProbeTargets(resource, server)
	if len(targets) == 0 {
		return nil
	}
	_, _, err = s.queueLatencyProbeTask(ctx, &server, resource, targets)
	return err
}

func latencyProbeAgentUpgradeRequired(server model.Server) bool {
	return strings.TrimSpace(server.AgentBuild) != "" && !agentBuildSupportsTask(server.AgentBuild, agentBuildMinLatencyProbe)
}

func (s *Server) queueLatencyProbeTask(ctx context.Context, server *model.Server, resource latencyProbeResource, targets []model.LatencyProbeTarget) (model.AgentTask, bool, error) {
	s.latencyProbeMu.Lock()
	defer s.latencyProbeMu.Unlock()
	active, err := s.store.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeProbeLatencyTargets)
	if err == nil && active != nil {
		return *active, true, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.AgentTask{}, false, err
	}
	version, err := s.store.NextConfigVersion(ctx)
	if err != nil {
		return model.AgentTask{}, false, err
	}
	plan := model.LatencyProbeTargetsPlan{Version: version, ResourceVersion: resource.Version, SampleCount: server.LatencyProbeSampleCount, IntervalMS: 150, TimeoutMS: 3000, Targets: targets}
	server.LatencyProbeResourceVersion = resource.Version
	if err := s.store.UpdateServerLatencyProbeSettings(ctx, server); err != nil {
		return model.AgentTask{}, false, err
	}
	task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeProbeLatencyTargets, plan, version)
	if err != nil {
		return model.AgentTask{}, false, err
	}
	return task, false, nil
}

func latencyProbeTaskTargetCount(task model.AgentTask, fallback int) int {
	var plan model.LatencyProbeTargetsPlan
	if json.Unmarshal([]byte(task.PayloadJSON), &plan) == nil {
		return len(plan.Targets)
	}
	return fallback
}
