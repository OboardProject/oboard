package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/core/confighealth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// configHealthTTL bounds how long a report is reused.
//
// The routing revision already invalidates the report whenever a server,
// inbound, proxy path, routing rule or DNS list changes, because every one of
// those tables carries a revision trigger. The reference tables the evaluator
// also reads - node presets, Snell parameter sets, certificates, DNS
// credentials, family-split templates - do not, on purpose: they are not
// routing desired state. The TTL is what bounds staleness for those, so a
// deleted preset surfaces within a minute instead of never.
const configHealthTTL = 60 * time.Second

// configHealthSnapshot is one immutable evaluation. The encoded form is kept
// alongside the report so a poll that hits the cache does not re-marshal a
// result that cannot have changed.
type configHealthSnapshot struct {
	revision uint64
	builtAt  time.Time
	report   confighealth.Report
	encoded  json.RawMessage
}

// configHealthReport returns the cached report, rebuilding only when the
// routing revision moved or the entry aged out.
//
// Nothing schedules this: no goroutine, no timer, no fleet scan. The evaluation
// happens when a console actually asks for it, and then at most once per
// revision no matter how many surfaces ask.
func (s *Server) configHealthReport(ctx context.Context) (*configHealthSnapshot, error) {
	revision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return nil, err
	}
	if current := s.configHealthCache.Load(); current != nil && current.revision == revision && time.Since(current.builtAt) < configHealthTTL {
		return current, nil
	}
	s.configHealthMu.Lock()
	defer s.configHealthMu.Unlock()
	// Re-check under the lock so concurrent misses collapse into one build
	// instead of every caller evaluating the same topology.
	if current := s.configHealthCache.Load(); current != nil && current.revision == revision && time.Since(current.builtAt) < configHealthTTL {
		return current, nil
	}
	entry, err := s.buildConfigHealthSnapshot(ctx, revision)
	if err != nil {
		return nil, err
	}
	s.configHealthCache.Store(entry)
	return entry, nil
}

// freshConfigHealthReport rebuilds unconditionally. The cleanup path uses it so
// an action is always resolved against the live topology, never against a
// report the operator was looking at a minute ago.
func (s *Server) freshConfigHealthReport(ctx context.Context) (*configHealthSnapshot, error) {
	revision, err := s.store.RoutingCacheRevision(ctx)
	if err != nil {
		return nil, err
	}
	s.configHealthMu.Lock()
	defer s.configHealthMu.Unlock()
	entry, err := s.buildConfigHealthSnapshot(ctx, revision)
	if err != nil {
		return nil, err
	}
	s.configHealthCache.Store(entry)
	return entry, nil
}

func (s *Server) invalidateConfigHealth() { s.configHealthCache.Store(nil) }

func (s *Server) buildConfigHealthSnapshot(ctx context.Context, revision uint64) (*configHealthSnapshot, error) {
	// The routing snapshot is already cached per revision, so the topology
	// itself costs nothing here on a warm controller.
	snapshot, err := s.routingSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	input, err := s.configHealthInput(ctx, snapshot.data)
	if err != nil {
		return nil, err
	}
	report := confighealth.Evaluate(input)
	encoded, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return &configHealthSnapshot{revision: revision, builtAt: time.Now(), report: report, encoded: encoded}, nil
}

// configHealthInput reads the reference tables the routing snapshot does not
// carry. Each one is reduced to the set of IDs a stored reference may legally
// resolve to, so the snapshot never holds rows it would not display.
func (s *Server) configHealthInput(ctx context.Context, data store.FullRoutingConfig) (confighealth.Input, error) {
	in := confighealth.Input{
		Servers:                data.Servers,
		Inbounds:               data.Inbounds,
		ProxyPaths:             data.ProxyPaths,
		ProxyPathSteps:         data.ProxyPathSteps,
		RoutingRules:           data.RoutingRules,
		RoutingRuleSets:        data.RoutingRuleSets,
		Outbounds:              data.Outbounds,
		ExternalOutbounds:      data.ExternalOutbounds,
		DNSLists:               data.DNSLists,
		ServerDNSPolicies:      data.ServerDNSPolicies,
		FamilySplitTemplateIDs: map[int64]bool{},
		UsableNodePresetIDs:    map[int64]bool{},
		UsableSnellProfileIDs:  map[int64]bool{},
		CertificateIDs:         map[int64]bool{},
		DNSCredentialIDs:       map[int64]bool{},
	}
	templates, err := s.store.ListFamilySplitTemplates(ctx)
	if err != nil {
		return confighealth.Input{}, err
	}
	for _, item := range templates {
		in.FamilySplitTemplateIDs[item.ID] = true
	}
	presets, err := s.store.ListNodePresets(ctx)
	if err != nil {
		return confighealth.Input{}, err
	}
	for _, item := range presets {
		// A disabled preset fails to resolve exactly like a deleted one, so it
		// must not count as usable.
		if item.Enabled {
			in.UsableNodePresetIDs[item.ID] = true
		}
	}
	profiles, err := s.store.ListSnellProfiles(ctx)
	if err != nil {
		return confighealth.Input{}, err
	}
	for _, item := range profiles {
		if item.Enabled {
			in.UsableSnellProfileIDs[item.ID] = true
		}
	}
	certificates, err := s.store.ListCertificates(ctx)
	if err != nil {
		return confighealth.Input{}, err
	}
	for _, item := range certificates {
		in.CertificateIDs[item.ID] = true
	}
	credentials, err := s.store.ListDNSCredentials(ctx)
	if err != nil {
		return confighealth.Input{}, err
	}
	for _, item := range credentials {
		in.DNSCredentialIDs[item.ID] = true
	}
	if err := s.appendSyncLaneInput(ctx, &in); err != nil {
		return confighealth.Input{}, err
	}
	return in, nil
}

// appendSyncLaneInput reads the two delivery ledgers whose version bindings a
// node can end up disagreeing with, and pairs each with what that node last
// reported it runs.
//
// Both are one bounded read per evaluation, so they follow the same cost rule as
// the reference tables above: nothing schedules them, and the TTL is what bounds
// how long a divergence stays unreported.
func (s *Server) appendSyncLaneInput(ctx context.Context, in *confighealth.Input) error {
	enrolled := make(map[int64]bool, len(in.Servers))
	for _, server := range in.Servers {
		if strings.TrimSpace(server.AgentID) != "" {
			enrolled[server.ID] = true
		}
	}
	userStates, err := s.store.ListRuntimeUserStates(ctx)
	if err != nil {
		return err
	}
	for _, state := range userStates {
		if !enrolled[state.ServerID] {
			continue
		}
		// The confirmation columns are the node's own report: every health
		// report restates what it currently runs, so they are the applied side
		// of the comparison rather than a delivery receipt.
		in.UsersLanes = append(in.UsersLanes, confighealth.UsersLane{
			ServerID:             state.ServerID,
			DesiredRevision:      state.DesiredRevision,
			DesiredDigest:        state.DesiredDigest,
			AppliedRevision:      state.ConfirmedRevision,
			AppliedContentDigest: state.ConfirmedDigest,
			PendingReason:        state.PendingReason,
			Conflicted:           state.PendingReason == store.RuntimeUsersPendingRevisionConflict,
		})
	}
	bindings, err := s.store.ListLatencyProbePlanBindings(ctx)
	if err != nil {
		return err
	}
	reported := s.syncLanes.probeSnapshot()
	for _, binding := range bindings {
		if !enrolled[binding.ServerID] {
			continue
		}
		lane := confighealth.ProbeLane{ServerID: binding.ServerID, PlanVersion: binding.PlanVersion, PlanDigest: binding.PlanDigest}
		if applied, ok := reported[binding.ServerID]; ok {
			lane.AppliedVersion, lane.AppliedDigest = applied.PlanVersion, applied.PlanDigest
		}
		in.ProbeLanes = append(in.ProbeLanes, lane)
	}
	return nil
}

// dashboardConfigHealth is the counts-only projection the dashboard carries.
// The findings array is fetched only when the operator opens the panel, so the
// hot poll stays flat regardless of how broken the configuration is.
func (s *Server) dashboardConfigHealth(ctx context.Context) (any, error) {
	entry, err := s.configHealthReport(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"revision":           entry.revision,
		"summary":            entry.report.Summary,
		"blocking_by_server": entry.report.BlockingByServer,
	}, nil
}

// ---- report endpoint ----

func (s *Server) configHealthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, errors.New("method not allowed"), http.StatusMethodNotAllowed)
		return
	}
	entry, err := s.configHealthReport(r.Context())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mergeConfigHealthRevision(entry))
		return
	}
	filtered := confighealth.Report{Findings: []confighealth.Finding{}, BlockingByServer: entry.report.BlockingByServer}
	for _, finding := range entry.report.Findings {
		if finding.Scope == scope {
			filtered.Findings = append(filtered.Findings, finding)
		}
	}
	filtered.Summary = entry.report.Summary
	write(w, http.StatusOK, map[string]any{"revision": entry.revision, "report": filtered})
}

// mergeConfigHealthRevision wraps the pre-encoded report without decoding it,
// so a cache hit copies bytes instead of re-marshalling the findings.
func mergeConfigHealthRevision(entry *configHealthSnapshot) []byte {
	out := make([]byte, 0, len(entry.encoded)+48)
	out = append(out, []byte(fmt.Sprintf(`{"revision":%d,"report":`, entry.revision))...)
	out = append(out, entry.encoded...)
	out = append(out, '}')
	return out
}

// ---- cleanup endpoint ----

type configHealthCleanupAction struct {
	Code       string `json:"code"`
	Scope      string `json:"scope"`
	ResourceID int64  `json:"resource_id"`
}

type configHealthCleanupRequest struct {
	// Revision is the report the operator acted on. A mismatch is refused
	// rather than reinterpreted: something the operator could not see changed
	// between looking and clicking.
	Revision uint64                      `json:"revision"`
	Confirm  bool                        `json:"confirm"`
	Actions  []configHealthCleanupAction `json:"actions"`
}

type configHealthCleanupResult struct {
	Code          string   `json:"code"`
	Scope         string   `json:"scope"`
	ResourceID    int64    `json:"resource_id"`
	ResourceName  string   `json:"resource_name,omitempty"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason,omitempty"`
	RemovedFields []string `json:"removed_fields,omitempty"`
}

const (
	cleanupStatusApplied = "applied"
	cleanupStatusSkipped = "skipped"
	cleanupStatusFailed  = "failed"
)

const configHealthCleanupLimit = 200

func (s *Server) configHealthCleanupHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		fail(w, errors.New("method not allowed"), http.StatusMethodNotAllowed)
		return
	}
	var request configHealthCleanupRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	if len(request.Actions) == 0 {
		fail(w, errors.New("actions is required"), http.StatusBadRequest)
		return
	}
	if len(request.Actions) > configHealthCleanupLimit {
		fail(w, fmt.Errorf("一次最多处理 %d 项", configHealthCleanupLimit), http.StatusBadRequest)
		return
	}
	response, err := s.runConfigHealthCleanup(r.Context(), r, request)
	if err != nil {
		if revision, ok := staleConfigHealthRevision(err); ok {
			write(w, http.StatusConflict, map[string]any{
				"error":    errConfigHealthRevisionConflict.Error(),
				"code":     "revision_conflict",
				"revision": revision,
			})
			return
		}
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, response)
}

// configHealthRevisionConflict carries the revision the caller should re-read.
type configHealthRevisionConflict struct{ revision uint64 }

func (e configHealthRevisionConflict) Error() string { return errConfigHealthRevisionConflict.Error() }
func (e configHealthRevisionConflict) Unwrap() error { return errConfigHealthRevisionConflict }

func staleConfigHealthRevision(err error) (uint64, bool) {
	var conflict configHealthRevisionConflict
	if errors.As(err, &conflict) {
		return conflict.revision, true
	}
	return 0, false
}

// runConfigHealthCleanup is the single implementation behind both the REST
// endpoint and the MCP capability, so the two surfaces cannot drift on what an
// action means, what is refused, or what counts as applied.
//
// The request parameter is the HTTP request when one exists; it supplies the
// audit actor and address and is nil for automation.
func (s *Server) runConfigHealthCleanup(ctx context.Context, r *http.Request, request configHealthCleanupRequest) (map[string]any, error) {
	entry, err := s.freshConfigHealthReport(ctx)
	if err != nil {
		return nil, err
	}
	if request.Revision != 0 && request.Revision != entry.revision {
		return nil, configHealthRevisionConflict{revision: entry.revision}
	}

	index := map[configHealthCleanupAction]confighealth.Finding{}
	for _, finding := range entry.report.Findings {
		index[configHealthCleanupAction{Code: finding.Code, Scope: finding.Scope, ResourceID: finding.ResourceID}] = finding
	}

	results := make([]configHealthCleanupResult, 0, len(request.Actions))
	mutated, configMutated := false, false
	for _, action := range request.Actions {
		finding, ok := index[action]
		if !ok {
			// The problem is gone, or was never in this report. Never guess a
			// mutation from a code the current topology does not produce.
			results = append(results, configHealthCleanupResult{
				Code: action.Code, Scope: action.Scope, ResourceID: action.ResourceID,
				Status: cleanupStatusSkipped, Reason: "该问题已不存在",
			})
			continue
		}
		result := s.applyConfigHealthAction(ctx, r, finding, request.Confirm)
		if result.Status == cleanupStatusApplied && request.Confirm {
			mutated = true
			// A resync only moves a delivery version; the configuration the
			// fleet is supposed to run is unchanged, so it must not tell the
			// operator a deployment is now owed.
			if finding.Remedy.Kind != confighealth.RemedyResync {
				configMutated = true
			}
		}
		results = append(results, result)
	}
	if mutated {
		s.invalidateConfigHealth()
	}

	response := map[string]any{
		"revision": entry.revision,
		"dry_run":  !request.Confirm,
		"results":  results,
	}
	applied, skipped, failed := 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case cleanupStatusApplied:
			applied++
		case cleanupStatusFailed:
			failed++
		default:
			skipped++
		}
	}
	response["applied"] = applied
	response["skipped"] = skipped
	response["failed"] = failed
	// Cleanup corrects stored desired state. It deliberately does not queue a
	// deployment: reconciliation writes are not a deployment trigger, and the
	// operator decides when the fleet gets pushed.
	response["requires_deployment"] = configMutated
	return response, nil
}

// applyConfigHealthAction performs exactly one remedy. The client selected a
// finding; the mutation is derived here from the freshly evaluated finding, so
// a caller can never describe an edit of its own.
func (s *Server) applyConfigHealthAction(ctx context.Context, r *http.Request, finding confighealth.Finding, confirm bool) configHealthCleanupResult {
	result := configHealthCleanupResult{
		Code: finding.Code, Scope: finding.Scope,
		ResourceID: finding.ResourceID, ResourceName: finding.ResourceName,
	}
	switch finding.Remedy.Kind {
	case confighealth.RemedyNormalize:
		return s.applyConfigHealthNormalize(ctx, r, finding, confirm, result)
	case confighealth.RemedyResync:
		if !confirm {
			result.Status = cleanupStatusApplied
			result.Reason = finding.Remedy.Summary
			return result
		}
		if err := s.resyncForRepair(ctx, finding); err != nil {
			result.Status = cleanupStatusFailed
			result.Reason = err.Error()
			return result
		}
		s.auditConfigHealth(ctx, r, "config_health_resync", finding, "")
		result.Status = cleanupStatusApplied
		return result
	case confighealth.RemedyDisable:
		if !confirm {
			result.Status = cleanupStatusApplied
			result.Reason = "将停用该资源"
			return result
		}
		if err := s.disableForRepair(ctx, finding); err != nil {
			result.Status = cleanupStatusFailed
			result.Reason = err.Error()
			return result
		}
		s.auditConfigHealth(ctx, r, "config_health_disable", finding, "")
		result.Status = cleanupStatusApplied
		return result
	case confighealth.RemedyDelete:
		if !confirm {
			result.Status = cleanupStatusApplied
			result.Reason = "将删除该记录"
			return result
		}
		if err := s.deleteForRepair(ctx, finding); err != nil {
			result.Status = cleanupStatusFailed
			result.Reason = err.Error()
			return result
		}
		s.auditConfigHealth(ctx, r, "config_health_delete", finding, "")
		result.Status = cleanupStatusApplied
		return result
	default:
		result.Status = cleanupStatusSkipped
		result.Reason = "该问题需要手动处理"
		return result
	}
}

func (s *Server) applyConfigHealthNormalize(ctx context.Context, r *http.Request, finding confighealth.Finding, confirm bool, result configHealthCleanupResult) configHealthCleanupResult {
	if finding.Scope != confighealth.ScopeInbound {
		result.Status = cleanupStatusSkipped
		result.Reason = "该问题需要手动处理"
		return result
	}
	inbound, err := s.store.GetInbound(ctx, finding.ResourceID)
	if err != nil {
		result.Status = cleanupStatusFailed
		result.Reason = err.Error()
		return result
	}
	normalized, removed, err := normalizedInboundDocument(*inbound, finding)
	if err != nil {
		result.Status = cleanupStatusFailed
		result.Reason = err.Error()
		return result
	}
	if len(removed) == 0 {
		result.Status = cleanupStatusSkipped
		result.Reason = "没有可移除的内容"
		return result
	}
	result.RemovedFields = removed
	if !confirm {
		result.Status = cleanupStatusApplied
		result.Reason = "将移除 " + strings.Join(removed, "、")
		return result
	}
	if err := s.store.RepairInboundConfigJSON(ctx, inbound.ID, normalized); err != nil {
		result.Status = cleanupStatusFailed
		result.Reason = err.Error()
		return result
	}
	s.auditConfigHealth(ctx, r, "config_health_normalize", finding, strings.Join(removed, ","))
	result.Status = cleanupStatusApplied
	return result
}

// normalizedInboundDocument derives the corrected document for one finding.
// The invalid-document case asks the authoritative validator what to remove;
// a dangling reference removes exactly the key that points at the missing row,
// keeping the parameters that were already merged from it.
func normalizedInboundDocument(inbound model.Inbound, finding confighealth.Finding) (string, []string, error) {
	if finding.Code == "inbound.config.invalid" {
		repair, err := core.NormalizeInboundConfig(inbound)
		if err != nil {
			return "", nil, err
		}
		if !repair.Resolved {
			return "", nil, fmt.Errorf("规范化后仍不合法：%s", repair.Remaining)
		}
		return repair.ConfigJSON, repair.RemovedPaths, nil
	}
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(inbound.ConfigJSON))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil || document == nil {
		if err == nil {
			err = errors.New("config_json 不是一个 JSON 对象")
		}
		return "", nil, err
	}
	removed := []string{}
	for _, field := range finding.Remedy.Fields {
		if _, exists := document[field]; exists {
			delete(document, field)
			removed = append(removed, field)
		}
	}
	sort.Strings(removed)
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", nil, err
	}
	return string(encoded), removed, nil
}

// resyncForRepair drops one delivery lane's version binding for one server.
//
// It is not a configuration edit and not a deployment: the content the node is
// supposed to run does not change, only the version it is offered under. That is
// the whole repair, because the node's gate refuses the version it already holds
// and accepts any higher one.
func (s *Server) resyncForRepair(ctx context.Context, finding confighealth.Finding) error {
	switch finding.Code {
	case confighealth.CodeUsersRevisionConflict:
		if err := s.store.ForceRuntimeUsersResync(ctx, finding.ResourceID); err != nil {
			return err
		}
		s.bumpRuntimeUserPackageGeneration()
		s.wakeRuntimeUsersSyncFor("config_health_resync", finding.ResourceID)
		return nil
	case confighealth.CodeProbeVersionConflict:
		if err := s.store.RebindLatencyProbePlanVersion(ctx, finding.ResourceID); err != nil {
			return err
		}
		// The rendered plan is cached per server; without this the next
		// heartbeat would serve the cached plan and never ask for a version.
		s.invalidateLatencyProbePlans(finding.ResourceID)
		return nil
	default:
		return fmt.Errorf("%s 不支持重新同步", finding.Code)
	}
}

func (s *Server) disableForRepair(ctx context.Context, finding confighealth.Finding) error {
	switch finding.Scope {
	case confighealth.ScopeInbound:
		return s.store.SetInboundEnabledForRepair(ctx, finding.ResourceID, false)
	case confighealth.ScopeProxyPath:
		return s.store.SetProxyPathEnabledForRepair(ctx, finding.ResourceID, false)
	case confighealth.ScopeRoutingRule:
		return s.store.SetRoutingRuleEnabledForRepair(ctx, finding.ResourceID, false)
	default:
		return fmt.Errorf("scope %s 不支持停用", finding.Scope)
	}
}

func (s *Server) deleteForRepair(ctx context.Context, finding confighealth.Finding) error {
	switch finding.Code {
	// This code carries a step ID rather than a path ID, because an orphan step
	// has no path to point at.
	case "proxy_path.step.orphan":
		return s.store.DeleteProxyPathStepForRepair(ctx, finding.ResourceID)
	case "inbound.server.missing":
		return s.store.Delete(ctx, "inbounds", finding.ResourceID)
	}
	switch finding.Scope {
	case confighealth.ScopeRoutingRule:
		return s.store.DeleteRoutingRuleForRepair(ctx, finding.ResourceID)
	case confighealth.ScopeProxyPath:
		// Refuse while anything live still points at this path, so cleaning up
		// one orphan cannot take down a topology that is still in use.
		count, err := s.store.ProxyPathReferenceCount(ctx, finding.ResourceID)
		if err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("仍有 %d 处引用这条链路，请先解除引用", count)
		}
		return s.store.DeleteProxyPath(ctx, finding.ResourceID)
	default:
		return fmt.Errorf("scope %s 不支持删除", finding.Scope)
	}
}

func (s *Server) auditConfigHealth(ctx context.Context, r *http.Request, action string, finding confighealth.Finding, extra string) {
	detail := fmt.Sprintf("%s %s#%d", finding.Code, finding.Scope, finding.ResourceID)
	if extra != "" {
		detail += " removed=" + extra
	}
	// Automation has no HTTP request; the Changeset record is its own trail, so
	// the audit entry records the controller as the actor instead of guessing.
	entry := model.AuditLog{Action: action, Target: "config_health", Detail: detail, IP: "controller"}
	if r != nil {
		entry.IP = clientIP(r)
		if user := currentUser(r); user != nil {
			actorID := user.ID
			entry.ActorID = &actorID
		}
	}
	_ = s.store.AddAudit(ctx, entry)
}
