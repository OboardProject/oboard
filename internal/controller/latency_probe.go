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

	"github.com/OboardProject/oboard/internal/core"
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
			return latencyProbeResource{}, errors.New("延迟测试资源更新已取消")
		case <-refreshed:
			latencyProbeCache.Lock()
			resource := latencyProbeCache.resource
			lastError := latencyProbeCache.lastError
			latencyProbeCache.Unlock()
			if len(resource.Provinces) > 0 {
				return resource, nil
			}
			if lastError != nil {
				return latencyProbeResource{}, errors.New("暂时无法更新延迟测试资源，请稍后重试")
			}
			return latencyProbeResource{}, errors.New("延迟测试资源暂不可用")
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
		return latencyProbeResource{}, errors.New("暂时无法更新延迟测试资源，请稍后重试")
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
				if _, _, ok := parseLatencyProbeResourceTarget(value); !ok {
					return latencyProbeResource{}, fmt.Errorf("probe resource contains invalid target %q", value)
				}
			}
		}
	}
	sum := sha256.Sum256(body)
	version := hex.EncodeToString(sum[:8])
	return latencyProbeResource{Version: version, UpdatedAt: updatedAt.UTC(), Provinces: raw}, nil
}

func parseLatencyProbeResourceTarget(value string) (host, ip string, ok bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	if addr, err := netip.ParseAddr(value); err == nil {
		if !validLatencyProbeResourceIPv4(addr) {
			return "", "", false
		}
		ip := addr.String()
		return ip, ip, true
	}
	if !validLatencyProbeHostname(value) {
		return "", "", false
	}
	return value, "", true
}

func validLatencyProbeResourceIPv4(addr netip.Addr) bool {
	return addr.IsValid() && addr.Is4() && addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast() && !addr.IsLinkLocalMulticast() && !addr.IsMulticast() && !addr.IsUnspecified()
}

func validLatencyProbeHostname(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || len(host) > 253 || strings.Contains(host, "..") {
		return false
	}
	host = strings.TrimSuffix(host, ".")
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 {
		return false
	}
	for _, c := range tld {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func latencyProbeTargets(resource latencyProbeResource, server model.Server) []model.LatencyProbeTarget {
	publicTarget := effectiveLatencyProbePublicTarget(server)
	publicHost := "cp.cloudflare.com"
	if publicTarget == model.ConnectivityProbeTarget12306 {
		publicHost = "www.12306.cn"
	} else if publicTarget == model.ConnectivityProbeTargetGoogle {
		publicHost = "www.gstatic.com"
	}
	publicPort, regionalPort := 443, 80
	if server.LatencyProbeMode == model.LatencyProbeModeICMP {
		publicPort, regionalPort = 0, 0
	}
	items := []model.LatencyProbeTarget{{ProbeID: "public-" + string(publicTarget), Kind: "public", Host: publicHost, Port: publicPort}}
	if server.LatencyProbeMaxTargets == 1 {
		return items
	}
	type targetGroup struct {
		province string
		carrier  string
		ips      []string
	}
	groups := make([]targetGroup, 0, len(server.LatencyProbeRegions))
	for _, region := range server.LatencyProbeRegions {
		ips := resource.Provinces[region.Province][region.Carrier]
		if len(ips) > 0 {
			groups = append(groups, targetGroup{province: region.Province, carrier: region.Carrier, ips: ips})
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].province == groups[j].province {
			return groups[i].carrier < groups[j].carrier
		}
		return groups[i].province < groups[j].province
	})
	for ipIndex := 0; ; ipIndex++ {
		added := false
		for _, group := range groups {
			if ipIndex >= len(group.ips) {
				continue
			}
			added = true
			if server.LatencyProbeMaxTargets > 0 && len(items) >= server.LatencyProbeMaxTargets {
				return items
			}
			host, ip, ok := parseLatencyProbeResourceTarget(group.ips[ipIndex])
			if !ok {
				continue
			}
			items = append(items, model.LatencyProbeTarget{ProbeID: fmt.Sprintf("%s-%s-%d", group.province, group.carrier, ipIndex), Kind: "regional", Province: group.province, Carrier: group.carrier, Host: host, IP: ip, Port: regionalPort})
		}
		if !added {
			break
		}
	}
	return items
}

func effectiveLatencyProbePublicTarget(server model.Server) model.ConnectivityTarget {
	switch server.LatencyProbePublicTarget {
	case model.ConnectivityProbeTargetCloudflare, model.ConnectivityProbeTarget12306, model.ConnectivityProbeTargetGoogle:
		return server.LatencyProbePublicTarget
	}
	region, _ := core.EffectiveServerRegion(server)
	if region == "CN" {
		return model.ConnectivityProbeTarget12306
	}
	return model.ConnectivityProbeTargetCloudflare
}

func latencyProbeRegionOptions(resource latencyProbeResource) []model.LatencyProbeRegion {
	options := make([]model.LatencyProbeRegion, 0)
	for province, carriers := range resource.Provinces {
		for carrier := range carriers {
			options = append(options, model.LatencyProbeRegion{Province: province, Carrier: carrier})
		}
	}
	sort.Slice(options, func(i, j int) bool {
		if options[i].Province == options[j].Province {
			return options[i].Carrier < options[j].Carrier
		}
		return options[i].Province < options[j].Province
	})
	return options
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
		regions := []model.LatencyProbeRegion{}
		if resourceErr == nil {
			regions = latencyProbeRegionOptions(resource)
		}
		write(w, 200, map[string]any{"server": server, "resource_version": resource.Version, "resource_updated_at": resource.UpdatedAt, "resource_error": errorString(resourceErr), "regions": regions, "results": results})
	case http.MethodPost:
		if !server.LatencyProbeEnabled {
			fail(w, errors.New("服务器未启用延迟测试"), http.StatusConflict)
			return
		}
		if reason := agentTaskImmediateFailure(server); reason != "" {
			fail(w, errors.New(reason), http.StatusConflict)
			return
		}
		if latencyProbeAgentUpgradeRequired(*server) {
			fail(w, errors.New("服务器 Agent 版本过旧，请先更新 Agent 后再执行延迟测试"), http.StatusConflict)
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
	write(w, http.StatusOK, map[string]any{
		"resource_version":    resource.Version,
		"resource_updated_at": resource.UpdatedAt,
		"regions":             latencyProbeRegionOptions(resource),
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
		if !ok || seen[item.ProbeID] || item.Kind != target.Kind || item.Mode != string(plan.Mode) || item.Province != target.Province || item.Carrier != target.Carrier || item.Host != target.Host || item.IP != target.IP || item.Port != target.Port {
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

func latencyProbePlanForServer(ctx context.Context, server model.Server) (model.LatencyProbeTargetsPlan, error) {
	resource, err := loadLatencyProbeResource(ctx, false)
	if err != nil {
		if server.LatencyProbeEnabled {
			return model.LatencyProbeTargetsPlan{}, err
		}
		resource.Version = strings.TrimSpace(server.LatencyProbeResourceVersion)
		resource.UpdatedAt = server.UpdatedAt.UTC()
	}
	if resource.Version == "" {
		resource.Version = "public"
	}
	version := server.UpdatedAt.UnixNano()
	if resourceVersion := resource.UpdatedAt.UnixNano(); resourceVersion > version {
		version = resourceVersion
	}
	if version < 1 {
		version = 1
	}
	return model.LatencyProbeTargetsPlan{
		Version:         version,
		ResourceVersion: resource.Version,
		Mode:            server.LatencyProbeMode,
		Enabled:         server.LatencyProbeEnabled,
		IntervalSeconds: server.LatencyProbeIntervalSeconds,
		SampleCount:     server.LatencyProbeSampleCount,
		IntervalMS:      150,
		TimeoutMS:       3000,
		Targets:         latencyProbeTargets(resource, server),
	}, nil
}

func validateAutonomousLatencyProbeReport(report *model.LatencyProbeResultReport) error {
	if report == nil || strings.TrimSpace(report.ReportID) == "" || strings.TrimSpace(report.ResourceVersion) == "" || len(report.Items) == 0 || len(report.Items) > 256 {
		return errors.New("延迟测试回报不完整")
	}
	now := time.Now().UTC()
	checkedAt := report.CheckedAt.UTC()
	if checkedAt.IsZero() || checkedAt.Before(now.Add(-35*24*time.Hour)) || checkedAt.After(now.Add(2*time.Minute)) {
		return errors.New("延迟测试回报时间无效")
	}
	publicCount := 0
	seen := make(map[string]bool, len(report.Items))
	for index := range report.Items {
		item := &report.Items[index]
		item.ProbeID = strings.TrimSpace(item.ProbeID)
		item.Host = strings.TrimSpace(item.Host)
		item.IP = strings.TrimSpace(item.IP)
		if item.ProbeID == "" || seen[item.ProbeID] || (item.Mode != string(model.LatencyProbeModeTCP) && item.Mode != string(model.LatencyProbeModeICMP)) || item.SampleCount < 1 || item.SampleCount > 10 || item.SuccessCount < 0 || item.SuccessCount > item.SampleCount || item.Available != (item.SuccessCount > 0) || item.LatencyMS < 0 || item.MinLatencyMS < 0 || item.P95LatencyMS < 0 || item.JitterMS < 0 {
			return errors.New("延迟测试回报包含无效结果")
		}
		seen[item.ProbeID] = true
		if item.Kind == "public" {
			publicCount++
			if item.Host != "cp.cloudflare.com" && item.Host != "www.12306.cn" && item.Host != "www.gstatic.com" {
				return errors.New("公网延迟目标无效")
			}
		} else if item.Kind == "regional" {
			if item.Province == "" || item.Carrier == "" {
				return errors.New("地区延迟目标无效")
			}
			host, expectedIP, ok := parseLatencyProbeResourceTarget(item.Host)
			if !ok || item.Host != host {
				return errors.New("地区延迟目标无效")
			}
			if expectedIP != "" {
				if item.IP != expectedIP {
					return errors.New("地区延迟目标无效")
				}
			} else if item.IP != "" {
				addr, err := netip.ParseAddr(item.IP)
				if err != nil || !validLatencyProbeResourceIPv4(addr) {
					return errors.New("地区延迟目标无效")
				}
			}
		} else {
			return errors.New("延迟测试目标类型无效")
		}
		if item.Mode == string(model.LatencyProbeModeTCP) && (item.Port < 1 || item.Port > 65535) {
			return errors.New("TCP 延迟目标端口无效")
		}
		if item.Mode == string(model.LatencyProbeModeICMP) {
			item.Port = 0
		}
		if item.SuccessCount == 0 && (item.LatencyMS != 0 || item.MinLatencyMS != 0 || item.P95LatencyMS != 0 || item.JitterMS != 0) {
			return errors.New("失败结果不能包含延迟统计")
		}
		if len(item.Error) > 240 {
			item.Error = item.Error[:240]
		}
	}
	if publicCount != 1 {
		return errors.New("每次延迟测试必须包含一个公网目标")
	}
	return nil
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
	plan := model.LatencyProbeTargetsPlan{Version: version, ResourceVersion: resource.Version, Mode: server.LatencyProbeMode, Enabled: server.LatencyProbeEnabled, IntervalSeconds: server.LatencyProbeIntervalSeconds, SampleCount: server.LatencyProbeSampleCount, IntervalMS: 150, TimeoutMS: 3000, Targets: targets}
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
