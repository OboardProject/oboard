package auditreview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

const maxReviewJobInputBytes = 64 << 10

type Service struct {
	store *store.Store
	key   []byte
	mu    sync.Mutex
	now   func() time.Time
}

type CreateRequest struct {
	RequestID     string
	ProviderID    string
	RequestedBy   int64
	Scope         model.AuditReviewScope
	EvidenceTypes []string
	WindowStart   time.Time
	WindowEnd     time.Time
}

func New(store *store.Store, key string) *Service {
	return &Service{store: store, key: []byte(key), now: time.Now}
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (*model.AuditReview, error) {
	provider, err := s.store.GetAIProvider(ctx, strings.TrimSpace(request.ProviderID))
	if err != nil {
		return nil, err
	}
	if !provider.Enabled || !provider.HasCredential {
		return nil, errors.New("AI Provider 未启用或缺少凭据")
	}
	request.RequestID = strings.TrimSpace(request.RequestID)
	if request.RequestedBy <= 0 || request.RequestID == "" || len(request.RequestID) > 128 {
		return nil, errors.New("审查发起人无效")
	}
	if existing, existingErr := s.store.GetAuditReviewByRequestID(ctx, request.RequestID); existingErr == nil {
		if existing.RequestedBy != request.RequestedBy {
			return nil, errors.New("审查请求标识已被占用")
		}
		return existing, nil
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return nil, existingErr
	}
	nowTime := s.now().UTC()
	if request.WindowEnd.IsZero() {
		request.WindowEnd = nowTime
	}
	request.WindowStart, request.WindowEnd = request.WindowStart.UTC(), request.WindowEnd.UTC()
	if !request.WindowStart.Before(request.WindowEnd) || request.WindowStart.Before(nowTime.Add(-30*24*time.Hour)) || request.WindowEnd.After(nowTime.Add(5*time.Minute)) {
		return nil, errors.New("审查时间必须位于最近 30 天内")
	}
	types, typeSet, err := normalizeEvidenceTypes(request.EvidenceTypes)
	if err != nil {
		return nil, err
	}
	if err := validateSelector(request.Scope.Users); err != nil {
		return nil, fmt.Errorf("用户范围：%w", err)
	}
	if err := validateSelector(request.Scope.Servers); err != nil {
		return nil, fmt.Errorf("服务器范围：%w", err)
	}
	routing, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	historical, err := s.store.AuditReviewHistoricalPairs(ctx, request.WindowStart, request.WindowEnd)
	if err != nil {
		return nil, err
	}
	userIDs, serverIDs, _, err := resolveScope(request.Scope, routing, historical)
	if err != nil {
		return nil, err
	}
	data, err := s.store.AuditReviewData(ctx, userIDs, serverIDs, request.WindowStart, request.WindowEnd, typeSet)
	if err != nil {
		return nil, err
	}
	random, err := security.RandomToken(15)
	if err != nil {
		return nil, err
	}
	reviewID := "air_" + random
	privacyMode := "masked"
	if provider.AllowRawAudit {
		privacyMode = "raw"
	}
	review := &model.AuditReview{
		ID: reviewID, RequestID: request.RequestID, ProviderID: provider.ID, RequestedBy: request.RequestedBy, Status: "queued", Scope: request.Scope,
		EvidenceTypes: types, WindowStartedAt: request.WindowStart, WindowEndedAt: request.WindowEnd, SnapshotAt: nowTime,
		PrivacyMode: privacyMode, ResolvedUserIDs: userIDs, ResolvedServerIDs: serverIDs,
	}
	evidence, err := s.buildEvidence(review, routing, data, privacyMode == "raw")
	if err != nil {
		return nil, err
	}
	inputs, err := reviewInputs(review, evidence)
	if err != nil {
		return nil, err
	}
	jobs := make([]model.AuditReviewJob, 0, len(inputs))
	for index, input := range inputs {
		jobRandom, err := security.RandomToken(12)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, model.AuditReviewJob{ID: "aij_" + jobRandom, ReviewID: review.ID, ProviderID: provider.ID, Stage: 0, Position: index, Kind: "evidence", Input: input})
	}
	if err := s.store.CreateAuditReview(ctx, review, evidence, jobs); err != nil {
		return nil, err
	}
	return s.store.GetAuditReview(ctx, review.ID)
}

func (s *Service) buildEvidence(review *model.AuditReview, routing store.FullRoutingConfig, data model.AuditReviewData, raw bool) ([]model.AuditReviewEvidence, error) {
	users := map[int64]model.User{}
	for _, item := range routing.Users {
		users[item.ID] = item
	}
	evidence := make([]model.AuditReviewEvidence, 0, len(data.Users)+1)
	coverage := map[string]any{
		"window_started_at": review.WindowStartedAt, "window_ended_at": review.WindowEndedAt, "snapshot_at": review.SnapshotAt,
		"resolved_user_count": len(review.ResolvedUserIDs), "resolved_server_count": len(review.ResolvedServerIDs), "evidence_types": review.EvidenceTypes,
	}
	coverageJSON, _ := json.Marshal(coverage)
	evidence = append(evidence, model.AuditReviewEvidence{Ref: "scope:coverage", ReviewID: review.ID, Kind: "scope", Payload: coverageJSON})
	for _, userData := range data.Users {
		user, exists := users[userData.UserID]
		if !exists {
			continue
		}
		ref := s.subjectRef("user", user.ID, raw)
		payload := s.userEvidencePayload(ref, user, userData, raw)
		rawPayload, _ := json.Marshal(payload)
		userID := user.ID
		evidence = append(evidence, model.AuditReviewEvidence{Ref: ref, ReviewID: review.ID, Kind: "user", UserID: &userID, Payload: rawPayload})
	}
	sort.SliceStable(evidence, func(i, j int) bool { return evidence[i].Ref < evidence[j].Ref })
	return evidence, nil
}

func (s *Service) userEvidencePayload(ref string, user model.User, data model.AuditReviewUserData, raw bool) map[string]any {
	payload := map[string]any{
		"subject_ref": ref, "status": user.Status, "role": user.Role, "subscription_suspended": user.SubscriptionSuspended,
		"subscription": map[string]any{"pulls": data.SubscriptionPulls, "successful": data.SubscriptionSuccessful, "denied": data.SubscriptionDenied, "source_ip_count": data.SubscriptionSourceIPs, "region_count": data.SubscriptionRegions, "client_count": data.SubscriptionClients, "format_count": data.SubscriptionFormats, "last_seen_at": data.SubscriptionLastSeenAt},
		"connection":   map[string]any{"connections": data.ConnectionCount, "closed": data.ConnectionClosed, "active_peak": data.ConnectionActivePeak, "active_at_end": data.ConnectionActiveAtEnd, "source_ip_count": data.ConnectionSourceIPs, "server_count": data.ConnectionServers, "destination_count": data.ConnectionDestinations, "dropped_bucket_count": data.ConnectionDropped, "last_seen_at": data.ConnectionLastSeenAt},
	}
	if raw {
		payload["user_id"], payload["username"], payload["nickname"] = user.ID, user.Username, user.Nickname
	}
	serverBreakdown := []map[string]any{}
	for _, item := range data.ServerBreakdown {
		serverBreakdown = append(serverBreakdown, map[string]any{"server_ref": s.subjectRef("server", item.ServerID, raw), "connection_count": item.ConnectionCount, "active_peak": item.ActivePeak, "last_seen_at": item.LastSeenAt})
	}
	payload["server_breakdown"] = serverBreakdown
	subscriptions := make([]map[string]any, 0, len(data.RecentSubscriptions))
	for _, item := range data.RecentSubscriptions {
		source := maskIP(item.SourceIP)
		entry := map[string]any{"source_ip": source, "region": firstNonEmpty(item.SourceProvince, item.SourceCountry), "client": item.ClientName, "format": item.Format, "outcome": item.Outcome, "requested_at": item.RequestedAt}
		if raw {
			entry["source_ip"], entry["user_agent"] = item.SourceIP, item.UserAgent
		}
		subscriptions = append(subscriptions, entry)
	}
	payload["recent_subscriptions"] = subscriptions
	connections := make([]map[string]any, 0, len(data.RecentConnections))
	for _, item := range data.RecentConnections {
		destination := reducedDestination(item.Destination)
		source := maskIP(item.SourceIP)
		if raw {
			destination, source = item.Destination, item.SourceIP
		}
		connections = append(connections, map[string]any{"server_ref": s.subjectRef("server", item.ServerID, raw), "source_ip": source, "region": firstNonEmpty(item.SourceProvince, item.SourceCountry), "network": item.Network, "destination": destination, "destination_port": item.DestinationPort, "connection_count": item.ConnectionCount, "closed_count": item.ClosedCount, "duration_max_ms": item.DurationMaxMS, "active_peak": item.ActivePeak, "active_at_end": item.ActiveAtEnd, "ended_at": item.EndedAt})
	}
	payload["recent_connections"] = connections
	destinations := make([]map[string]any, 0, len(data.Destinations))
	for _, item := range data.Destinations {
		destination := reducedDestination(item.Destination)
		if raw {
			destination = item.Destination
		}
		destinations = append(destinations, map[string]any{"destination": destination, "port": item.Port, "network": item.Network, "connection_count": item.ConnectionCount, "server_count": item.ServerCount, "last_seen_at": item.LastSeenAt})
	}
	payload["destinations"] = destinations
	return payload
}

func reviewInputs(review *model.AuditReview, evidence []model.AuditReviewEvidence) ([]json.RawMessage, error) {
	header := map[string]any{"review_id": review.ID, "kind": "evidence", "privacy_mode": review.PrivacyMode, "evidence_types": review.EvidenceTypes, "window_started_at": review.WindowStartedAt, "window_ended_at": review.WindowEndedAt}
	inputs := []json.RawMessage{}
	items := []json.RawMessage{}
	flush := func() error {
		if len(items) == 0 {
			return nil
		}
		payload := map[string]any{}
		for key, value := range header {
			payload[key] = value
		}
		payload["evidence"] = items
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		inputs = append(inputs, raw)
		items = nil
		return nil
	}
	for _, item := range evidence {
		entry, _ := json.Marshal(map[string]any{"ref": item.Ref, "kind": item.Kind, "payload": item.Payload})
		candidate := append(append([]json.RawMessage(nil), items...), entry)
		payload := map[string]any{"review_id": review.ID, "kind": "evidence", "privacy_mode": review.PrivacyMode, "evidence_types": review.EvidenceTypes, "window_started_at": review.WindowStartedAt, "window_ended_at": review.WindowEndedAt, "evidence": candidate}
		raw, _ := json.Marshal(payload)
		if len(raw) > maxReviewJobInputBytes && len(items) > 0 {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		if len(items) == 0 {
			single, _ := json.Marshal(map[string]any{"review_id": review.ID, "kind": "evidence", "privacy_mode": review.PrivacyMode, "evidence_types": review.EvidenceTypes, "window_started_at": review.WindowStartedAt, "window_ended_at": review.WindowEndedAt, "evidence": []json.RawMessage{entry}})
			if len(single) > maxReviewJobInputBytes {
				return nil, errors.New("单条审查证据超过大小限制")
			}
		}
		items = append(items, entry)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, errors.New("审查没有可用证据")
	}
	return inputs, nil
}

func (s *Service) Advance(ctx context.Context, reviewID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	review, err := s.store.GetAuditReview(ctx, reviewID)
	if err != nil || review.Status == "failed" || review.Status == "cancelled" || review.Status == "succeeded" {
		return err
	}
	jobs, err := s.store.ListAuditReviewJobs(ctx, reviewID, true)
	if err != nil {
		return err
	}
	maxStage := -1
	for _, job := range jobs {
		if job.Stage > maxStage {
			maxStage = job.Stage
		}
	}
	stageJobs := []model.AuditReviewJob{}
	for _, job := range jobs {
		if job.Stage == maxStage {
			stageJobs = append(stageJobs, job)
		}
	}
	for _, job := range stageJobs {
		if job.Status != "succeeded" {
			return nil
		}
	}
	if len(stageJobs) == 1 {
		return s.store.FinalizeAuditReview(ctx, reviewID, stageJobs[0].Output)
	}
	inputs := packSynthesisInputs(review, stageJobs)
	ids := make([]string, 0, len(inputs))
	for range inputs {
		random, err := security.RandomToken(12)
		if err != nil {
			return err
		}
		ids = append(ids, "aij_"+random)
	}
	return s.store.CreateAuditReviewStage(ctx, review.ID, review.ProviderID, maxStage+1, inputs, ids)
}

func packSynthesisInputs(review *model.AuditReview, jobs []model.AuditReviewJob) []json.RawMessage {
	inputs := []json.RawMessage{}
	partials := []json.RawMessage{}
	flush := func() {
		if len(partials) == 0 {
			return
		}
		raw, _ := json.Marshal(map[string]any{"review_id": review.ID, "kind": "synthesis", "evidence_types": review.EvidenceTypes, "partial_reports": partials})
		inputs = append(inputs, raw)
		partials = nil
	}
	for _, job := range jobs {
		candidate := append(append([]json.RawMessage(nil), partials...), job.Output)
		raw, _ := json.Marshal(map[string]any{"review_id": review.ID, "kind": "synthesis", "evidence_types": review.EvidenceTypes, "partial_reports": candidate})
		if len(raw) > maxReviewJobInputBytes && len(partials) > 0 {
			flush()
		}
		partials = append(partials, job.Output)
	}
	flush()
	return inputs
}

func (s *Service) ValidateReport(ctx context.Context, reviewID string, report *model.AuditReviewReport) error {
	encoded, _ := json.Marshal(report)
	if report == nil || len(encoded) > maxReviewJobInputBytes-4096 || !oneOf(report.Verdict, "normal", "attention", "high_risk", "insufficient_evidence") || !validRisk(report.RiskLevel) || !validHealthScore(report.RiskLevel, report.HealthScore) || report.Confidence < 0 || report.Confidence > 1 || strings.TrimSpace(report.Summary) == "" || len(report.Summary) > 2000 || len(report.CoverageSummary) > 1000 || len(report.Dimensions) > 3 || len(report.NotableSubjects) > 100 || len(report.RecommendedActions) > 12 || len(report.DataGaps) > 32 {
		return errors.New("AI 审查输出结构无效")
	}
	refs, err := s.store.AuditReviewEvidenceRefs(ctx, reviewID)
	if err != nil {
		return err
	}
	allowedKinds := map[string]bool{model.AuditReviewEvidenceSubscription: true, model.AuditReviewEvidenceConnection: true, model.AuditReviewEvidenceDestination: true}
	for _, dimension := range report.Dimensions {
		if !allowedKinds[dimension.Kind] || !validRisk(dimension.RiskLevel) || strings.TrimSpace(dimension.Summary) == "" || len(dimension.Summary) > 1000 || len(dimension.EvidenceRefs) > 32 || len(dimension.CounterEvidence) > 32 {
			return errors.New("AI 审查分项无效")
		}
		if err := validateRefs(refs, append(append([]string{}, dimension.EvidenceRefs...), dimension.CounterEvidence...)); err != nil {
			return err
		}
	}
	for _, subject := range report.NotableSubjects {
		if !strings.HasPrefix(subject.SubjectRef, "user:") || len(subject.SubjectRef) > 256 || !refs[subject.SubjectRef] || !validRisk(subject.RiskLevel) || strings.TrimSpace(subject.Summary) == "" || len(subject.Summary) > 1000 || len(subject.EvidenceRefs) > 32 {
			return errors.New("AI 审查对象引用无效")
		}
		if err := validateRefs(refs, subject.EvidenceRefs); err != nil {
			return err
		}
	}
	for _, action := range report.RecommendedActions {
		if !oneOf(action, "notify_admin", "request_manual_review", "continue_observation", "inspect_user", "inspect_server", "propose_temporary_subscription_suspension") {
			return errors.New("AI 审查建议无效")
		}
	}
	for _, gap := range report.DataGaps {
		if len(gap) > 1000 {
			return errors.New("AI 审查数据缺口无效")
		}
	}
	return nil
}

func resolveScope(scope model.AuditReviewScope, routing store.FullRoutingConfig, historical map[int64]map[int64]bool) ([]int64, []int64, map[int64]map[int64]bool, error) {
	userExists, serverExists := map[int64]bool{}, map[int64]bool{}
	for _, user := range routing.Users {
		userExists[user.ID] = true
	}
	for _, server := range routing.Servers {
		serverExists[server.ID] = true
	}
	access := map[int64]map[int64]bool{}
	serverByInbound := map[int64]int64{}
	for _, inbound := range routing.Inbounds {
		serverByInbound[inbound.ID] = inbound.ServerID
	}
	bindings := core.EffectiveInboundUsers(routing.Inbounds, routing.Users, routing.InboundUsers, routing.UserGroups, routing.UserGroupMembers, routing.InboundAccessGrants)
	for _, binding := range bindings {
		serverID := serverByInbound[binding.InboundID]
		if access[binding.UserID] == nil {
			access[binding.UserID] = map[int64]bool{}
		}
		access[binding.UserID][serverID] = true
	}
	for _, binding := range core.EffectiveProxyPathUsers(routing.ProxyPaths, routing.Inbounds, routing.Users, routing.InboundUsers, routing.UserGroups, routing.UserGroupMembers, routing.InboundAccessGrants) {
		serverID := serverByInbound[binding.InboundID]
		if access[binding.UserID] == nil {
			access[binding.UserID] = map[int64]bool{}
		}
		access[binding.UserID][serverID] = true
	}
	for userID, servers := range historical {
		if access[userID] == nil {
			access[userID] = map[int64]bool{}
		}
		for serverID := range servers {
			access[userID][serverID] = true
		}
	}
	users := selectedIDs(scope.Users, userExists)
	servers := selectedIDs(scope.Servers, serverExists)
	if scope.Users.Mode == "selected" && len(users) != len(store.SortAuditReviewIDs(scope.Users.IDs)) {
		return nil, nil, nil, errors.New("指定用户不存在")
	}
	if scope.Servers.Mode == "selected" && len(servers) != len(store.SortAuditReviewIDs(scope.Servers.IDs)) {
		return nil, nil, nil, errors.New("指定服务器不存在")
	}
	if scope.Users.Mode == "selected" && scope.Servers.Mode == "selected" {
		serverSet := idSet(servers)
		matched := users[:0]
		for _, userID := range users {
			if intersects(access[userID], serverSet) {
				matched = append(matched, userID)
			}
		}
		users = matched
	}
	if scope.Users.Mode == "all" && scope.Servers.Mode == "selected" {
		serverSet := idSet(servers)
		users = nil
		for userID, related := range access {
			if intersects(related, serverSet) {
				users = append(users, userID)
			}
		}
	}
	if scope.Users.Mode == "selected" && scope.Servers.Mode == "all" {
		servers = nil
		for _, userID := range users {
			for serverID := range access[userID] {
				servers = append(servers, serverID)
			}
		}
	}
	users, servers = store.SortAuditReviewIDs(users), store.SortAuditReviewIDs(servers)
	filteredAccess := map[int64]map[int64]bool{}
	serverSet := idSet(servers)
	for _, userID := range users {
		filteredAccess[userID] = map[int64]bool{}
		for serverID := range access[userID] {
			if serverSet[serverID] {
				filteredAccess[userID][serverID] = true
			}
		}
	}
	return users, servers, filteredAccess, nil
}

func validateSelector(selector model.AuditReviewSelector) error {
	if selector.Mode != "all" && selector.Mode != "selected" {
		return errors.New("必须选择全部或指定对象")
	}
	if selector.Mode == "selected" && len(store.SortAuditReviewIDs(selector.IDs)) == 0 {
		return errors.New("至少选择一个对象")
	}
	return nil
}

func selectedIDs(selector model.AuditReviewSelector, exists map[int64]bool) []int64 {
	if selector.Mode == "all" {
		out := make([]int64, 0, len(exists))
		for id := range exists {
			out = append(out, id)
		}
		return store.SortAuditReviewIDs(out)
	}
	out := []int64{}
	for _, id := range store.SortAuditReviewIDs(selector.IDs) {
		if exists[id] {
			out = append(out, id)
		}
	}
	return out
}

func normalizeEvidenceTypes(values []string) ([]string, map[string]bool, error) {
	set := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !oneOf(value, model.AuditReviewEvidenceSubscription, model.AuditReviewEvidenceConnection, model.AuditReviewEvidenceDestination) {
			return nil, nil, errors.New("审查项无效")
		}
		set[value] = true
	}
	if len(set) == 0 {
		return nil, nil, errors.New("至少选择一个审查项")
	}
	ordered := []string{}
	for _, value := range []string{model.AuditReviewEvidenceSubscription, model.AuditReviewEvidenceConnection, model.AuditReviewEvidenceDestination} {
		if set[value] {
			ordered = append(ordered, value)
		}
	}
	return ordered, set, nil
}

func (s *Service) subjectRef(kind string, id int64, raw bool) string {
	if raw {
		return kind + ":" + strconv.FormatInt(id, 10)
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte("oboard-ai-review-" + kind + "\x00" + strconv.FormatInt(id, 10)))
	return kind + ":" + hex.EncodeToString(mac.Sum(nil)[:8])
}

func maskIP(raw string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return "unknown"
	}
	addr = addr.Unmap()
	bits := 48
	if addr.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(addr, bits).Masked().String()
}

func reducedDestination(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if _, err := netip.ParseAddr(raw); err == nil {
		return maskIP(raw)
	}
	if value, err := publicsuffix.EffectiveTLDPlusOne(strings.TrimSuffix(strings.ToLower(raw), ".")); err == nil {
		return value
	}
	return "unknown-domain"
}

func validateRefs(allowed map[string]bool, values []string) error {
	for _, value := range values {
		if !allowed[value] {
			return errors.New("AI 返回了不存在的证据引用")
		}
	}
	return nil
}

func validRisk(value string) bool {
	return oneOf(value, "low", "medium", "high", "critical", "unknown")
}

func validHealthScore(riskLevel string, score *int) bool {
	if score == nil || *score < 0 || *score > 100 {
		return false
	}
	switch riskLevel {
	case "low":
		return *score >= 80
	case "medium", "unknown":
		return *score >= 60 && *score < 80
	case "high", "critical":
		return *score < 60
	default:
		return false
	}
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func idSet(values []int64) map[int64]bool {
	out := map[int64]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func intersects(left, right map[int64]bool) bool {
	for value := range left {
		if right[value] {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
