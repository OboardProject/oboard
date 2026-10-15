package controller

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/ipdata"
	"github.com/OboardProject/oboard/internal/model"
)

const (
	vpngateDefaultCatalogURL  = "https://www.vpngate.net/api/iphone/"
	vpngateCatalogInterval    = 60 * time.Minute
	vpngateCatalogRetry       = 10 * time.Minute
	vpngateCatalogTimeout     = 60 * time.Second
	vpngateWorkerTick         = 30 * time.Second
	vpngateShortlistSize      = 24
	vpngateProbeTaskTimeout   = 5 * time.Minute
	vpngateApplyRetry         = 3 * time.Minute
	vpngateReselectDebounce   = 2 * time.Minute
	vpngateStatusFreshness    = 3 * time.Minute
	vpngateQualityTTL         = 24 * time.Hour
	vpngateMaxCandidates      = 64
	vpngateQualityLookupBatch = 32
	vpngateSuccessEWMA        = 0.3
)

// vpngateRuntime is the in-process state of the VPNGate selection worker.
type vpngateRuntime struct {
	once         sync.Once
	wake         chan struct{}
	mu           sync.Mutex
	lastFetchTry time.Time
	// lookup replaces the IP data service in tests.
	lookup func(ctx context.Context, ip string, scope ipdata.Scope) ipdata.Report

	// The worker goroutine is the only writer of pool selection state. Other
	// paths (task callbacks, health reports, operator requests) hand it work
	// through these maps so a slow reputation lookup never blocks them and no
	// write can overwrite another.
	inbox    sync.Mutex
	status   map[[2]int64]vpngateStatusEntry
	pending  map[[2]int64]model.VPNGateProbeReport
	reselect map[int64]bool
}

type vpngateStatusEntry struct {
	status model.VPNGatePoolStatus
	at     time.Time
}

func vpngatePairKey(poolID, serverID int64) [2]int64 {
	return [2]int64{poolID, serverID}
}

// vpngateOverlayStatus attaches the latest in-memory health report.
func (s *Server) vpngateOverlayStatus(state *model.VPNGatePoolServer) {
	runtime := s.vpngateState()
	runtime.inbox.Lock()
	entry, ok := runtime.status[vpngatePairKey(state.PoolID, state.ServerID)]
	runtime.inbox.Unlock()
	if !ok {
		return
	}
	status := entry.status
	at := entry.at
	state.Status = &status
	state.StatusAt = &at
}

func (s *Server) vpngateState() *vpngateRuntime {
	s.vpngate.once.Do(func() {
		s.vpngate.wake = make(chan struct{}, 1)
		s.vpngate.status = map[[2]int64]vpngateStatusEntry{}
		s.vpngate.pending = map[[2]int64]model.VPNGateProbeReport{}
		s.vpngate.reselect = map[int64]bool{}
	})
	return &s.vpngate
}

// WakeVPNGate asks the worker to run a pass soon.
func (s *Server) WakeVPNGate() {
	select {
	case s.vpngateState().wake <- struct{}{}:
	default:
	}
}

// StartVPNGate runs the VPNGate selection worker. It costs nothing while no
// enabled path exits through a VPNGate pool.
func (s *Server) StartVPNGate(ctx context.Context) {
	state := s.vpngateState()
	ticker := time.NewTicker(vpngateWorkerTick)
	defer ticker.Stop()
	for {
		if err := s.vpngatePass(ctx, false); err != nil && ctx.Err() == nil {
			log.Printf("vpngate worker: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-state.wake:
		}
	}
}

// vpngateUsage resolves which pools each server exits through.
func (s *Server) vpngateUsage(ctx context.Context) (map[int64]map[int64]bool, error) {
	paths, err := s.store.ListProxyPaths(ctx)
	if err != nil {
		return nil, err
	}
	steps, err := s.store.ListProxyPathSteps(ctx)
	if err != nil {
		return nil, err
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	return core.ProxyPathVPNGateServerPools(paths, steps, inbounds), nil
}

func (s *Server) vpngatePass(ctx context.Context, forceCatalog bool) error {
	pools, err := s.store.ListVPNGatePools(ctx)
	if err != nil || len(pools) == 0 {
		return err
	}
	usage, err := s.vpngateUsage(ctx)
	if err != nil {
		return err
	}
	states, err := s.store.ListVPNGatePoolServers(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		if !usage[state.ServerID][state.PoolID] {
			_ = s.store.DeleteVPNGatePoolServer(ctx, state.PoolID, state.ServerID)
		}
	}
	if len(usage) == 0 {
		return nil
	}
	if err := s.vpngateRefreshCatalog(ctx, forceCatalog); err != nil {
		log.Printf("vpngate catalog: %v", err)
	}
	nodes, err := s.store.ListVPNGateNodes(ctx)
	if err != nil {
		return err
	}
	runtime := s.vpngateState()
	runtime.inbox.Lock()
	reselect := runtime.reselect
	runtime.reselect = map[int64]bool{}
	runtime.inbox.Unlock()
	poolByID := map[int64]model.VPNGatePool{}
	for _, pool := range pools {
		poolByID[pool.ID] = pool
	}
	serverIDs := make([]int64, 0, len(usage))
	for id := range usage {
		serverIDs = append(serverIDs, id)
	}
	sort.Slice(serverIDs, func(i, j int) bool { return serverIDs[i] < serverIDs[j] })
	for _, serverID := range serverIDs {
		server, err := s.store.GetServer(ctx, serverID)
		if err != nil {
			continue
		}
		poolIDs := make([]int64, 0, len(usage[serverID]))
		for id := range usage[serverID] {
			poolIDs = append(poolIDs, id)
		}
		sort.Slice(poolIDs, func(i, j int) bool { return poolIDs[i] < poolIDs[j] })
		for _, poolID := range poolIDs {
			pool, ok := poolByID[poolID]
			if !ok {
				continue
			}
			if err := s.vpngateReconcilePair(ctx, pool, *server, nodes, reselect[poolID]); err != nil {
				log.Printf("vpngate pool %d on server %d: %v", poolID, serverID, err)
			}
		}
	}
	return nil
}

func vpngateCatalogURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("OBOARD_VPNGATE_CATALOG_URL"))
	if raw == "" {
		return vpngateDefaultCatalogURL, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("OBOARD_VPNGATE_CATALOG_URL 必须是 HTTPS 地址")
	}
	return parsed.String(), nil
}

func (s *Server) vpngateRefreshCatalog(ctx context.Context, force bool) error {
	runtime := s.vpngateState()
	current, err := s.store.GetVPNGateCatalogState(ctx)
	if err != nil {
		return err
	}
	runtime.mu.Lock()
	due := force || current.FetchedAt.IsZero() || time.Since(current.FetchedAt) >= vpngateCatalogInterval
	if due && !force && time.Since(runtime.lastFetchTry) < vpngateCatalogRetry {
		due = false
	}
	if due {
		runtime.lastFetchTry = time.Now()
	}
	runtime.mu.Unlock()
	if !due {
		return nil
	}
	source, err := vpngateCatalogURL()
	if err != nil {
		_ = s.store.RecordVPNGateCatalogError(ctx, err.Error())
		return err
	}
	data, err := fetchVPNGateCatalog(ctx, source)
	if err == nil {
		var entries []core.VPNGateCatalogEntry
		var skipped int
		entries, skipped, err = core.ParseVPNGateCatalog(data)
		if err == nil {
			_, err = s.store.UpsertVPNGateCatalog(ctx, entries, skipped, source, time.Now())
		}
	}
	if err != nil {
		_ = s.store.RecordVPNGateCatalogError(ctx, boundedVPNGateText(err.Error()))
		return err
	}
	return nil
}

func fetchVPNGateCatalog(ctx context.Context, source string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, vpngateCatalogTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "oboard-controller")
	client := &http.Client{Timeout: vpngateCatalogTimeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" {
			return errors.New("VPNGate 目录重定向无效")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("获取 VPNGate 目录失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("VPNGate 目录返回 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, core.VPNGateCatalogMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > core.VPNGateCatalogMaxBytes {
		return nil, errors.New("VPNGate 目录过大")
	}
	return data, nil
}

func boundedVPNGateText(text string) string {
	if len(text) > 300 {
		return text[:300]
	}
	return text
}

func newVPNGateRequestID() string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return hex.EncodeToString(raw[:])
}

func (s *Server) loadVPNGatePoolServer(ctx context.Context, poolID, serverID int64) (*model.VPNGatePoolServer, error) {
	state, err := s.store.GetVPNGatePoolServer(ctx, poolID, serverID)
	if errors.Is(err, sql.ErrNoRows) {
		return &model.VPNGatePoolServer{PoolID: poolID, ServerID: serverID, Members: []model.VPNGateCandidate{}, Candidates: []model.VPNGateCandidate{}, History: []model.VPNGateSwitchEvent{}}, nil
	}
	return state, err
}

// vpngateReconcilePair drives one (pool, exit server) pair: probe when due or
// degraded, and make sure the Agent holds the current membership.
func (s *Server) vpngateReconcilePair(ctx context.Context, pool model.VPNGatePool, server model.Server, nodes []model.VPNGateNode, forceProbe bool) error {
	state, err := s.loadVPNGatePoolServer(ctx, pool.ID, server.ID)
	if err != nil {
		return err
	}
	s.vpngateOverlayStatus(state)
	runtime := s.vpngateState()
	runtime.inbox.Lock()
	report, hasReport := runtime.pending[vpngatePairKey(pool.ID, server.ID)]
	delete(runtime.pending, vpngatePairKey(pool.ID, server.ID))
	runtime.inbox.Unlock()
	now := time.Now()
	if hasReport && report.RequestID == state.ProbeRequestID {
		state.ProbeTaskID = 0
		next := now.Add(time.Duration(pool.RefreshMinutes) * time.Minute)
		state.NextRefreshAt = &next
		s.vpngateFinishSelection(ctx, pool, state, report.Results, nodes, "")
	}
	if forceProbe {
		state.NextRefreshAt = nil
		state.LastProbeAt = nil
	}
	if !pool.Enabled {
		if len(state.Members) > 0 {
			state.History = append(state.History, model.VPNGateSwitchEvent{At: now, Reason: "出口池已停用，暂停出站", From: firstVPNGateMember(state.Members)})
			state.Members = []model.VPNGateCandidate{}
		}
		state.LastError = "出口池已停用"
		return s.vpngateEnsureApplied(ctx, pool, server, state, nodes)
	}
	if agentTaskImmediateFailure(&server) != "" {
		return s.store.SaveVPNGatePoolServer(ctx, state)
	}
	if !serverHasKernelCapability(server, "vpngate_pool_v1") {
		state.LastError = "服务器内核不支持 VPNGate 出口，请先更新 Agent/内核"
		return s.store.SaveVPNGatePoolServer(ctx, state)
	}
	probeBusy := false
	if state.ProbeTaskID != 0 {
		if task, err := s.store.GetTask(ctx, state.ProbeTaskID); err == nil && (task.Status == "pending" || task.Status == "running") && now.Sub(task.CreatedAt) < vpngateProbeTaskTimeout {
			probeBusy = true
		} else {
			state.ProbeTaskID = 0
		}
	}
	if !probeBusy && vpngateProbeDue(pool, state, now) {
		if err := s.vpngateQueueProbe(ctx, pool, server, state, nodes); err != nil {
			state.LastError = boundedVPNGateText(err.Error())
		}
	}
	return s.vpngateEnsureApplied(ctx, pool, server, state, nodes)
}

func vpngateProbeDue(pool model.VPNGatePool, state *model.VPNGatePoolServer, now time.Time) bool {
	if state.NextRefreshAt == nil || !now.Before(*state.NextRefreshAt) {
		return true
	}
	recentlyProbed := state.LastProbeAt != nil && now.Sub(*state.LastProbeAt) < vpngateReselectDebounce
	if recentlyProbed {
		return false
	}
	if len(state.Members) == 0 {
		return true
	}
	if state.Status != nil && state.StatusAt != nil && now.Sub(*state.StatusAt) < vpngateStatusFreshness && state.Status.Revision == state.Revision {
		want := 1 + pool.HotStandby
		if want > 2 {
			want = 2
		}
		if want > len(state.Members) {
			want = len(state.Members)
		}
		if state.Status.Healthy < want {
			return true
		}
	}
	return false
}

func firstVPNGateMember(members []model.VPNGateCandidate) string {
	if len(members) == 0 {
		return ""
	}
	return members[0].MemberID
}

// vpngateLiveMembers returns the members the kernel currently reports as up.
// They are not probed again: VPNGate servers throttle repeated sessions from
// one address, so a second probe would fail a member that is carrying traffic.
func vpngateLiveMembers(state *model.VPNGatePoolServer) map[string]model.VPNGateMemberStatus {
	live := map[string]model.VPNGateMemberStatus{}
	if state.Status == nil || state.StatusAt == nil || time.Since(*state.StatusAt) > vpngateStatusFreshness {
		return live
	}
	for _, member := range state.Status.Members {
		if member.State == "up" {
			live[member.MemberID] = member
		}
	}
	return live
}

func (s *Server) vpngateQueueProbe(ctx context.Context, pool model.VPNGatePool, server model.Server, state *model.VPNGatePoolServer, nodes []model.VPNGateNode) error {
	now := time.Now()
	shortlist := s.vpngateShortlist(ctx, pool, state, nodes)
	if len(shortlist) == 0 {
		next := now.Add(time.Duration(pool.RefreshMinutes) * time.Minute)
		state.NextRefreshAt = &next
		state.LastProbeAt = &now
		// Live members still count: re-score them so the selection stays current.
		s.vpngateFinishSelection(ctx, pool, state, nil, nodes, "节点目录里没有符合条件的节点")
		return nil
	}
	plan := model.VPNGateProbePlan{PoolID: pool.ID, Tag: core.VPNGatePoolTag(pool.ID), RequestID: newVPNGateRequestID()}
	for _, node := range shortlist {
		plan.Candidates = append(plan.Candidates, model.VPNGateProbeCandidate{ProbeID: core.VPNGateMemberID(node.ID), OpenVPNSpec: node.Spec})
	}
	task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeProbeVPNGate, plan, 0)
	if err != nil {
		return err
	}
	state.ProbeTaskID = task.ID
	state.ProbeRequestID = plan.RequestID
	state.LastProbeAt = &now
	return s.store.SaveVPNGatePoolServer(ctx, state)
}

// vpngateShortlist chooses the servers to measure: country, protocol and
// (when the listed address is already known) operator filters first, previous
// good performers next, then the catalog's own score.
func (s *Server) vpngateShortlist(ctx context.Context, pool model.VPNGatePool, state *model.VPNGatePoolServer, nodes []model.VPNGateNode) []model.VPNGateNode {
	live := vpngateLiveMembers(state)
	history := map[int64]model.VPNGateCandidate{}
	for _, candidate := range state.Candidates {
		history[candidate.NodeID] = candidate
	}
	type ranked struct {
		node  model.VPNGateNode
		score float64
	}
	var list []ranked
	lookups := 0
	for _, node := range nodes {
		if live[core.VPNGateMemberID(node.ID)].MemberID != "" {
			continue
		}
		if pool.Network != "any" && node.Network != pool.Network {
			continue
		}
		if len(pool.Countries) > 0 && !stringSliceHas(pool.Countries, node.CountryCode) {
			continue
		}
		if len(pool.OperatorASNs) > 0 {
			quality, _ := s.store.GetVPNGateIPQuality(ctx, node.IP)
			if (quality == nil || time.Since(quality.CheckedAt) > vpngateQualityTTL) && lookups < vpngateQualityLookupBatch {
				lookups++
				quality = s.vpngateLookupQuality(ctx, node.IP, false)
			}
			if quality != nil && quality.ASN != "" && !stringSliceHas(pool.OperatorASNs, strings.ToUpper(quality.ASN)) {
				continue
			}
		}
		score := float64(node.Score) / 1e6
		if previous, ok := history[node.ID]; ok {
			score += previous.SuccessRate * 1000
			if previous.SuccessRate < 0.15 && previous.Status != "succeeded" {
				score -= 2000
			}
		} else {
			score += 500
		}
		list = append(list, ranked{node: node, score: score})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].score > list[j].score })
	out := make([]model.VPNGateNode, 0, vpngateShortlistSize)
	for _, item := range list {
		if len(out) >= vpngateShortlistSize {
			break
		}
		out = append(out, item.node)
	}
	return out
}

func stringSliceHas(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

// vpngateLookupQuality returns the cached or freshly looked-up reputation of
// an IP. full also queries the rate-limited risk providers; the operator
// filter only needs the ASN.
func (s *Server) vpngateLookupQuality(ctx context.Context, ip string, full bool) *model.VPNGateIPQuality {
	cached, _ := s.store.GetVPNGateIPQuality(ctx, ip)
	if cached != nil && time.Since(cached.CheckedAt) < vpngateQualityTTL && (!full || cached.Risk != nil || cached.Abuse != nil || cached.Hosting != nil) {
		return cached
	}
	runtime := s.vpngateState()
	lookup := s.ipDataService(ctx).Lookup
	if runtime.lookup != nil {
		lookup = runtime.lookup
	}
	quality := model.VPNGateIPQuality{IP: ip, CheckedAt: time.Now().UTC()}
	if cached != nil {
		quality = *cached
		quality.CheckedAt = time.Now().UTC()
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	report := lookup(lookupCtx, ip, ipdata.Scope{Attribution: true, Quality: full})
	found := false
	if a := report.Attribution; a != nil && (a.ASN != "" || a.CountryCode != "") {
		found = true
		if a.ASN != "" {
			quality.ASN = strings.ToUpper(a.ASN)
		}
		if a.ASName != "" {
			quality.Organization = a.ASName
		}
		if a.CountryCode != "" {
			quality.Country = a.CountryCode
		}
	}
	if q := report.Quality; q != nil && q.Status != "unknown" {
		found = true
		for _, item := range []struct {
			target  **bool
			verdict ipdata.Verdict
		}{{&quality.Hosting, q.Hosting}, {&quality.VPN, q.VPN}, {&quality.Tor, q.Tor}} {
			if value := item.verdict.Bool(); value != nil {
				*item.target = value
			}
		}
		if q.Risk != nil {
			quality.Risk = q.Risk
		}
		if q.AbuseScore != nil {
			quality.Abuse = q.AbuseScore
		}
	}
	if !found {
		return cached
	}
	_ = s.store.SaveVPNGateIPQuality(ctx, quality)
	return &quality
}

// applyVPNGateProbeResult scores a finished probe and updates the selection.
func (s *Server) applyVPNGateProbeResult(ctx context.Context, serverID int64, task model.AgentTask, taskStatus, resultJSON string) error {
	var plan model.VPNGateProbePlan
	if err := json.Unmarshal([]byte(task.PayloadJSON), &plan); err != nil {
		return nil
	}
	if plan.PoolID <= 0 || len(plan.Candidates) == 0 || len(plan.Candidates) > vpngateMaxCandidates {
		return errors.New("invalid vpngate probe payload")
	}
	report := parseVPNGateProbeReport(resultJSON)
	if report.RequestID != "" && report.RequestID != plan.RequestID {
		return errors.New("vpngate probe report does not match its task")
	}
	if len(report.Results) > len(plan.Candidates) {
		return errors.New("vpngate probe returned too many results")
	}
	pool, err := s.store.GetVPNGatePool(ctx, plan.PoolID)
	if err != nil {
		return nil
	}
	state, err := s.loadVPNGatePoolServer(ctx, plan.PoolID, serverID)
	if err != nil {
		return err
	}
	if state.ProbeRequestID != plan.RequestID {
		return nil
	}
	expected := map[string]bool{}
	for _, candidate := range plan.Candidates {
		expected[candidate.ProbeID] = true
	}
	results := map[string]model.VPNGateProbeResult{}
	for _, result := range report.Results {
		if !expected[result.ProbeID] {
			return errors.New("vpngate probe returned an unknown probe_id")
		}
		results[result.ProbeID] = result
	}
	ordered := make([]model.VPNGateProbeResult, 0, len(plan.Candidates))
	for _, candidate := range plan.Candidates {
		result, ok := results[candidate.ProbeID]
		if !ok {
			message := "探测未返回结果"
			if taskStatus != "succeeded" {
				message = "探测任务失败"
			}
			result = model.VPNGateProbeResult{ProbeID: candidate.ProbeID, Status: "failed", Stage: "task", Error: message}
		}
		ordered = append(ordered, result)
	}
	runtime := s.vpngateState()
	runtime.inbox.Lock()
	runtime.pending[vpngatePairKey(pool.ID, serverID)] = model.VPNGateProbeReport{PoolID: pool.ID, RequestID: plan.RequestID, Results: ordered}
	runtime.inbox.Unlock()
	s.WakeVPNGate()
	return nil
}

func parseVPNGateProbeReport(resultJSON string) model.VPNGateProbeReport {
	var report model.VPNGateProbeReport
	if json.Unmarshal([]byte(resultJSON), &report) == nil && len(report.Results) > 0 {
		return report
	}
	var wrapped struct {
		Report model.VPNGateProbeReport `json:"report"`
	}
	if json.Unmarshal([]byte(resultJSON), &wrapped) == nil {
		return wrapped.Report
	}
	return model.VPNGateProbeReport{}
}

// vpngateFinishSelection merges probe results with live member evidence,
// scores everything, selects members, and persists the state. Applying the
// membership happens in vpngateEnsureApplied.
func (s *Server) vpngateFinishSelection(ctx context.Context, pool model.VPNGatePool, state *model.VPNGatePoolServer, results []model.VPNGateProbeResult, nodes []model.VPNGateNode, emptyReason string) {
	nodeByMember := map[string]model.VPNGateNode{}
	for _, node := range nodes {
		nodeByMember[core.VPNGateMemberID(node.ID)] = node
	}
	previous := map[string]model.VPNGateCandidate{}
	for _, candidate := range state.Candidates {
		previous[candidate.MemberID] = candidate
	}
	for _, member := range state.Members {
		if _, ok := previous[member.MemberID]; !ok {
			previous[member.MemberID] = member
		}
	}
	candidates := []model.VPNGateCandidate{}
	seen := map[string]bool{}
	add := func(node model.VPNGateNode, candidate model.VPNGateCandidate, ok bool) {
		rate := 0.5
		if prior, found := previous[candidate.MemberID]; found {
			rate = prior.SuccessRate
			if candidate.EgressIP == "" {
				candidate.EgressIP = prior.EgressIP
			}
		}
		outcome := 0.0
		if ok {
			outcome = 1
		}
		candidate.SuccessRate = rate*(1-vpngateSuccessEWMA) + outcome*vpngateSuccessEWMA
		var quality *model.VPNGateIPQuality
		if ok && candidate.EgressIP != "" {
			quality = s.vpngateLookupQuality(ctx, candidate.EgressIP, true)
		}
		if quality != nil {
			candidate.ASN, candidate.Organization = quality.ASN, quality.Organization
		}
		candidate.Quality = core.VPNGateQualityScore(quality, candidate.SuccessRate, node)
		candidate.Reason = core.VPNGateEligibility(pool, candidate, quality)
		candidate.Eligible = candidate.Reason == ""
		candidates = append(candidates, candidate)
		seen[candidate.MemberID] = true
	}
	// Live members: the kernel's own health check is the measurement.
	for memberID, status := range vpngateLiveMembers(state) {
		node, ok := nodeByMember[memberID]
		if !ok {
			continue
		}
		candidate := model.VPNGateCandidate{NodeID: node.ID, MemberID: memberID, IP: node.IP, Port: node.Port, Network: node.Network, CountryCode: node.CountryCode, Status: "succeeded", Stage: "live", HandshakeMS: status.HandshakeMS, RTTMS: status.RTTMS}
		add(node, candidate, true)
	}
	for _, result := range results {
		node, ok := nodeByMember[result.ProbeID]
		if !ok || seen[result.ProbeID] {
			continue
		}
		candidate := model.VPNGateCandidate{NodeID: node.ID, MemberID: result.ProbeID, IP: node.IP, Port: node.Port, Network: node.Network, CountryCode: node.CountryCode, Status: result.Status, Stage: result.Stage, Error: boundedVPNGateText(result.Error), EgressIP: result.EgressIP, HandshakeMS: result.HandshakeMS, RTTMS: core.MedianRTT(result.RTTMS), TTFBMS: result.TTFBMS}
		add(node, candidate, result.Status == "succeeded")
	}
	ranked := core.RankVPNGateCandidates(pool, candidates)
	selection := core.SelectVPNGateMembers(pool, state.Members, ranked, state.Improvements)
	selected := map[string]bool{}
	for _, member := range selection.Members {
		selected[member.MemberID] = true
	}
	for i := range candidates {
		candidates[i].Selected = selected[candidates[i].MemberID]
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Selected != candidates[j].Selected {
			return candidates[i].Selected
		}
		if candidates[i].Eligible != candidates[j].Eligible {
			return candidates[i].Eligible
		}
		return candidates[i].RTTMS < candidates[j].RTTMS
	})
	if len(candidates) > vpngateMaxCandidates {
		candidates = candidates[:vpngateMaxCandidates]
	}
	state.Candidates = candidates
	state.Improvements = selection.Improvements
	if !core.SameVPNGateMembers(selection.Members, state.Members) {
		reason := selection.Reason
		if reason == "" {
			reason = "更新候选节点"
		}
		if len(selection.Members) == 0 {
			reason = "没有符合条件的节点，暂停出站"
		}
		state.History = append(state.History, model.VPNGateSwitchEvent{At: time.Now(), Reason: reason, From: firstVPNGateMember(state.Members), To: firstVPNGateMember(selection.Members)})
		state.Members = selection.Members
	}
	if state.Members == nil {
		state.Members = []model.VPNGateCandidate{}
	}
	state.LastError = ""
	if len(state.Members) == 0 {
		state.LastError = "没有符合条件的节点"
		if emptyReason != "" {
			state.LastError = emptyReason
		}
	}
	if err := s.store.SaveVPNGatePoolServer(ctx, state); err != nil {
		log.Printf("save vpngate state: %v", err)
	}
}

// vpngatePlan renders the signed membership for the current selection.
func vpngatePlan(pool model.VPNGatePool, state *model.VPNGatePoolServer, nodes []model.VPNGateNode) model.VPNGatePoolPlan {
	nodeByID := map[int64]model.VPNGateNode{}
	for _, node := range nodes {
		nodeByID[node.ID] = node
	}
	plan := model.VPNGatePoolPlan{PoolID: pool.ID, Tag: core.VPNGatePoolTag(pool.ID), HotStandby: pool.HotStandby, Members: []model.VPNGatePoolMember{}}
	if pool.Enabled {
		for _, member := range state.Members {
			node, ok := nodeByID[member.NodeID]
			if !ok {
				continue
			}
			plan.Members = append(plan.Members, model.VPNGatePoolMember{MemberID: member.MemberID, OpenVPNSpec: node.Spec})
		}
	}
	plan.ContentDigest = core.VPNGatePlanDigest(plan.HotStandby, plan.Members)
	return plan
}

// vpngateEnsureApplied allocates a new revision when the rendered membership
// changed and (re)sends it until the Agent reports holding it.
func (s *Server) vpngateEnsureApplied(ctx context.Context, pool model.VPNGatePool, server model.Server, state *model.VPNGatePoolServer, nodes []model.VPNGateNode) error {
	plan := vpngatePlan(pool, state, nodes)
	now := time.Now()
	if plan.ContentDigest != state.ContentDigest || state.Revision == 0 {
		state.Revision++
		state.ContentDigest = plan.ContentDigest
		state.ApplyTaskID = 0
		state.AppliedAt = nil
	}
	plan.Revision = state.Revision
	held := state.Status != nil && state.Status.Revision == state.Revision && state.Status.ContentDigest == state.ContentDigest
	if !held && agentTaskImmediateFailure(&server) == "" {
		inFlight := false
		if state.ApplyTaskID != 0 {
			if task, err := s.store.GetTask(ctx, state.ApplyTaskID); err == nil && (task.Status == "pending" || task.Status == "running") {
				inFlight = true
			}
		}
		if !inFlight && (state.AppliedAt == nil || now.Sub(*state.AppliedAt) >= vpngateApplyRetry) {
			task, err := s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeApplyVPNGatePool, plan, 0)
			if err != nil {
				state.LastError = boundedVPNGateText(err.Error())
			} else {
				state.ApplyTaskID = task.ID
				state.AppliedAt = &now
			}
		}
	}
	return s.store.SaveVPNGatePoolServer(ctx, state)
}

// recordVPNGateHealth keeps pool health from a health report in memory and
// wakes the worker when a pool lost healthy members. It never writes the
// database, so a busy health path costs nothing per pool.
func (s *Server) recordVPNGateHealth(ctx context.Context, serverID int64, pools []model.VPNGatePoolStatus) {
	if len(pools) == 0 {
		return
	}
	runtime := s.vpngateState()
	wake := false
	now := time.Now()
	runtime.inbox.Lock()
	for i, status := range pools {
		if i >= 64 || status.PoolID <= 0 || status.Tag != core.VPNGatePoolTag(status.PoolID) {
			continue
		}
		if len(status.Members) > 16 {
			status.Members = status.Members[:16]
		}
		members := make([]model.VPNGateMemberStatus, len(status.Members))
		copy(members, status.Members)
		for j := range members {
			members[j].LastError = boundedVPNGateText(members[j].LastError)
		}
		status.Members = members
		key := vpngatePairKey(status.PoolID, serverID)
		previous, seen := runtime.status[key]
		if seen && status.Revision == previous.status.Revision && status.Healthy < previous.status.Healthy && status.Healthy < 2 {
			wake = true
		}
		if !seen && status.Healthy == 0 && len(status.Members) > 0 {
			wake = true
		}
		runtime.status[key] = vpngateStatusEntry{status: status, at: now}
	}
	runtime.inbox.Unlock()
	if wake {
		s.WakeVPNGate()
	}
}
