package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// accessChangePayload is the change-specific activation data stored in
// access_changes.payload_json.
type accessChangePayload struct {
	UserIDs      []int64 `json:"user_ids,omitempty"`
	ExceptionIDs []int64 `json:"exception_ids,omitempty"`
	TargetStatus string  `json:"target_status,omitempty"`
}

// ---------------------------------------------------------------------------
// Snapshot helpers
// ---------------------------------------------------------------------------

// snapshotFromConfig resolves one effective access snapshot from explicit
// binding, node and exception inputs at time at. It is the single projection
// builder for both previews and access-change projections.
func (s *Server) snapshotFromConfig(data store.FullRoutingConfig, bindings []model.UserPlanBinding, planNodes []model.SubscriptionPlanNode, exceptions []model.UserNodeException, at time.Time) *core.EffectiveAccessSnapshot {
	return core.BuildEffectiveAccessSnapshot(core.EffectiveAccessInput{
		Users:             data.Users,
		Bindings:          bindings,
		Plans:             data.SubscriptionPlans,
		PlanNodes:         planNodes,
		Exceptions:        exceptions,
		Paths:             data.ProxyPaths,
		Steps:             data.ProxyPathSteps,
		Inbounds:          data.Inbounds,
		ExternalOutbounds: data.ExternalOutbounds,
		Now:               at,
	})
}

// replacePlanNodes swaps one plan's active nodes with a candidate node set,
// keeping every other plan's nodes untouched.
func replacePlanNodes(all []model.SubscriptionPlanNode, planID int64, candidate []model.SubscriptionPlanNode) []model.SubscriptionPlanNode {
	out := make([]model.SubscriptionPlanNode, 0, len(all)+len(candidate))
	for _, pn := range all {
		if pn.PlanID == planID {
			continue
		}
		out = append(out, pn)
	}
	out = append(out, candidate...)
	return out
}

// planSnapshotForRevision resolves the snapshot as if plan used revisionID for
// both its limits and its node set.
func (s *Server) planSnapshotForRevision(ctx context.Context, data store.FullRoutingConfig, plan *model.SubscriptionPlan, revisionID int64, bindings []model.UserPlanBinding, exceptions []model.UserNodeException, at time.Time) (*core.EffectiveAccessSnapshot, error) {
	nodes, err := s.store.ListPlanRevisionNodes(ctx, revisionID)
	if err != nil {
		return nil, err
	}
	revision, err := s.store.GetPlanRevision(ctx, plan.ID, revisionID)
	if err != nil {
		return nil, err
	}
	planCopy := *plan
	planCopy.SpeedLimitMbps = revision.SpeedLimitMbps
	planCopy.TrafficLimitBytes = revision.TrafficLimitBytes
	planCopy.TrafficResetMode = revision.TrafficResetMode
	planCopy.TrafficResetDay = revision.TrafficResetDay
	plans := make([]model.SubscriptionPlan, 0, len(data.SubscriptionPlans))
	for _, p := range data.SubscriptionPlans {
		if p.ID == plan.ID {
			p = planCopy
		}
		plans = append(plans, p)
	}
	snap := core.BuildEffectiveAccessSnapshot(core.EffectiveAccessInput{
		Users:             data.Users,
		Bindings:          bindings,
		Plans:             plans,
		PlanNodes:         replacePlanNodes(data.ActivePlanNodes, plan.ID, nodes),
		Exceptions:        exceptions,
		Paths:             data.ProxyPaths,
		Steps:             data.ProxyPathSteps,
		Inbounds:          data.Inbounds,
		ExternalOutbounds: data.ExternalOutbounds,
		Now:               at,
	})
	return snap, nil
}

// planSnapshotDisabled resolves the snapshot with one plan disabled.
func (s *Server) planSnapshotDisabled(data store.FullRoutingConfig, plan *model.SubscriptionPlan, bindings []model.UserPlanBinding, exceptions []model.UserNodeException, at time.Time) *core.EffectiveAccessSnapshot {
	plans := make([]model.SubscriptionPlan, 0, len(data.SubscriptionPlans))
	for _, p := range data.SubscriptionPlans {
		if p.ID == plan.ID {
			p.Enabled = false
		}
		plans = append(plans, p)
	}
	return core.BuildEffectiveAccessSnapshot(core.EffectiveAccessInput{
		Users:             data.Users,
		Bindings:          bindings,
		Plans:             plans,
		PlanNodes:         data.ActivePlanNodes,
		Exceptions:        exceptions,
		Paths:             data.ProxyPaths,
		Steps:             data.ProxyPathSteps,
		Inbounds:          data.Inbounds,
		ExternalOutbounds: data.ExternalOutbounds,
		Now:               at,
	})
}

// exceptionsWith replaces one exception row in the list (matching by ID) or
// appends it when the ID is new.
func exceptionsWith(items []model.UserNodeException, v model.UserNodeException) []model.UserNodeException {
	out := make([]model.UserNodeException, 0, len(items)+1)
	replaced := false
	for _, ex := range items {
		if v.ID != 0 && ex.ID == v.ID {
			out = append(out, v)
			replaced = true
			continue
		}
		out = append(out, ex)
	}
	if !replaced {
		out = append(out, v)
	}
	return out
}

func exceptionsWithout(items []model.UserNodeException, id int64) []model.UserNodeException {
	out := make([]model.UserNodeException, 0, len(items))
	for _, ex := range items {
		if ex.ID != id {
			out = append(out, ex)
		}
	}
	return out
}

// effectiveWindow returns at shifted so a binding/exception with starts_at is
// considered effective (post-activation projection) and one with expires_at is
// still inside its window.
func effectiveWindow(at time.Time, startsAt, expiresAt *time.Time) time.Time {
	if startsAt != nil && startsAt.After(at) {
		at = *startsAt
	}
	if expiresAt != nil && !expiresAt.After(at) {
		at = expiresAt.Add(-time.Second)
	}
	return at
}

// ---------------------------------------------------------------------------
// Preview hash
// ---------------------------------------------------------------------------

type PlanRevisionNodeDigest struct {
	Key          string `json:"key"`
	DisplayGroup string `json:"display_group"`
	SortPosition *int   `json:"sort_position"`
}

type planChangePreviewData struct {
	PlanID                   int64                             `json:"plan_id"`
	ExpectedActiveRevisionID int64                             `json:"expected_active_revision_id"`
	CandidateRevisionID      int64                             `json:"candidate_revision_id"`
	Nodes                    []PlanRevisionNodeDigest          `json:"nodes"`
	OrderPolicy              model.SubscriptionNodeOrderPolicy `json:"order_policy"`
	SpeedLimitMbps           int                               `json:"speed_limit_mbps"`
	TrafficLimitBytes        int64                             `json:"traffic_limit_bytes"`
	TrafficResetMode         string                            `json:"traffic_reset_mode"`
	TrafficResetDay          int                               `json:"traffic_reset_day"`
}

// planRevisionNodeDigests renders the full revision node set as a sorted
// digest so any node, display group or manual position change alters the
// preview hash.
func planRevisionNodeDigests(nodes []model.SubscriptionPlanNode) []PlanRevisionNodeDigest {
	out := make([]PlanRevisionNodeDigest, 0, len(nodes))
	for _, pn := range nodes {
		digest := PlanRevisionNodeDigest{
			Key:          core.NodeKeyOf(pn.NodeType, pn.NodeID),
			DisplayGroup: pn.DisplayGroup,
		}
		if pn.SortPosition != nil {
			position := *pn.SortPosition
			digest.SortPosition = &position
		}
		out = append(out, digest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func planChangePreviewHash(data planChangePreviewData) string {
	raw, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// planChangePreview computes the publish preview for one plan: node diff,
// presentation deltas (ordering policy, manual positions, display groups),
// affected users and authentication servers, plus the deterministic preview
// hash the apply call must echo back. Authorization changes deploy agent
// tasks; pure presentation changes create the same plan_publish access change
// but with zero tasks.
func (s *Server) planChangePreview(ctx context.Context, plan *model.SubscriptionPlan, revisionID int64) (map[string]any, error) {
	activeNodes, err := s.store.ListActivePlanNodes(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	draftNodes, err := s.store.ListPlanRevisionNodes(ctx, revisionID)
	if err != nil {
		return nil, err
	}
	activeKeys := map[string]bool{}
	activeByKey := map[string]model.SubscriptionPlanNode{}
	for _, pn := range activeNodes {
		key := core.NodeKeyOf(pn.NodeType, pn.NodeID)
		activeKeys[key] = true
		activeByKey[key] = pn
	}
	draftKeys := map[string]bool{}
	draftByKey := map[string]model.SubscriptionPlanNode{}
	for _, pn := range draftNodes {
		key := core.NodeKeyOf(pn.NodeType, pn.NodeID)
		draftKeys[key] = true
		draftByKey[key] = pn
	}
	added := sortedNodeKeySet(draftKeys, activeKeys)
	removed := sortedNodeKeySet(activeKeys, draftKeys)
	diffKeys := map[string]bool{}
	for _, key := range append(append([]string{}, added...), removed...) {
		diffKeys[key] = true
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.ListEffectiveUserPlanBindings(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	affectedUsers := 0
	for _, binding := range bindings {
		if binding.PlanID == plan.ID {
			affectedUsers++
		}
	}
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	servers, affectedPaths, offline := core.AffectedAuthServers(diffKeys, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	revision, err := s.store.GetPlanRevision(ctx, plan.ID, revisionID)
	if err != nil {
		return nil, err
	}
	activeRevision, err := s.store.GetPlanRevision(ctx, plan.ID, plan.CurrentRevisionID)
	if err != nil && plan.CurrentRevisionID != 0 {
		return nil, err
	}
	normalizedPolicy, _ := core.ValidateSubscriptionNodeOrderPolicy(revision.NodeOrderPolicy)
	membershipChanged := len(added) > 0 || len(removed) > 0
	limitsChanged := activeRevision == nil ||
		activeRevision.SpeedLimitMbps != revision.SpeedLimitMbps ||
		activeRevision.TrafficLimitBytes != revision.TrafficLimitBytes ||
		activeRevision.TrafficResetMode != revision.TrafficResetMode ||
		activeRevision.TrafficResetDay != revision.TrafficResetDay
	orderingChanged := false
	displayGroupsChanged := false
	if activeRevision != nil && !membershipChanged {
		activePolicy, _ := core.ValidateSubscriptionNodeOrderPolicy(activeRevision.NodeOrderPolicy)
		orderingChanged = !sameOrderPolicy(activePolicy, normalizedPolicy)
		if !orderingChanged {
			for key := range draftKeys {
				activePN, aOK := activeByKey[key]
				draftPN, dOK := draftByKey[key]
				if !aOK || !dOK {
					continue
				}
				if sortPositionOf(activePN) != sortPositionOf(draftPN) {
					orderingChanged = true
					break
				}
			}
		}
	} else if activeRevision == nil {
		orderingChanged = true
	}
	if activeRevision != nil {
		for key := range draftKeys {
			activePN, aOK := activeByKey[key]
			draftPN, dOK := draftByKey[key]
			if !aOK || !dOK {
				continue
			}
			if activePN.DisplayGroup != draftPN.DisplayGroup {
				displayGroupsChanged = true
				break
			}
		}
	}
	changeClass := "presentation_only"
	if membershipChanged || limitsChanged {
		changeClass = "authorization"
	}
	taskCount := len(servers)
	hash := planChangePreviewHash(planChangePreviewData{
		PlanID:                   plan.ID,
		ExpectedActiveRevisionID: plan.CurrentRevisionID,
		CandidateRevisionID:      revisionID,
		Nodes:                    planRevisionNodeDigests(draftNodes),
		OrderPolicy:              normalizedPolicy,
		SpeedLimitMbps:           revision.SpeedLimitMbps,
		TrafficLimitBytes:        revision.TrafficLimitBytes,
		TrafficResetMode:         revision.TrafficResetMode,
		TrafficResetDay:          revision.TrafficResetDay,
	})
	return map[string]any{
		"preview_hash":                hash,
		"expected_active_revision_id": plan.CurrentRevisionID,
		"candidate_revision_id":       revisionID,
		"change_class":                changeClass,
		"membership_changed":          membershipChanged,
		"limits_changed":              limitsChanged,
		"display_groups_changed":      displayGroupsChanged,
		"ordering_changed":            orderingChanged,
		"added_nodes":                 added,
		"removed_nodes":               removed,
		"affected_users":              affectedUsers,
		"affected_servers":            servers,
		"affected_paths":              affectedPaths,
		"offline_servers":             offline,
		"task_count":                  taskCount,
	}, nil
}

func sortPositionOf(pn model.SubscriptionPlanNode) int {
	if pn.SortPosition == nil {
		return -1
	}
	return *pn.SortPosition
}

func sameOrderPolicy(a, b model.SubscriptionNodeOrderPolicy) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}

func sortedNodeKeySet(in, exclude map[string]bool) []string {
	out := make([]string, 0, len(in))
	for key := range in {
		if !exclude[key] {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func sortedNodeKeys(keys map[string]bool) []string {
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Change creation and phase deployment
// ---------------------------------------------------------------------------

type accessChangeDraft struct {
	changeType               model.AccessChangeType
	sourcePlanID             int64
	candidateRevisionID      int64
	expectedActiveRevisionID int64
	previewHash              string
	affectedUserCount        int
	activateAt               *time.Time
	payload                  accessChangePayload
	prepareProjection        core.AccessProjection
	finalizeProjection       core.AccessProjection
	serverIDs                []int64
	createdBy                *int64
}

func (s *Server) createAccessChange(ctx context.Context, r *http.Request, draft accessChangeDraft) (*model.AccessChange, error) {
	prepareJSON, err := json.Marshal(draft.prepareProjection)
	if err != nil {
		return nil, err
	}
	finalizeJSON, err := json.Marshal(draft.finalizeProjection)
	if err != nil {
		return nil, err
	}
	payloadJSON, err := json.Marshal(draft.payload)
	if err != nil {
		return nil, err
	}
	createdBy := draft.createdBy
	if createdBy == nil && r != nil {
		if user := currentUser(r); user != nil {
			createdBy = &user.ID
		}
	}
	change := &model.AccessChange{
		ChangeType:               draft.changeType,
		SourcePlanID:             draft.sourcePlanID,
		CandidateRevisionID:      draft.candidateRevisionID,
		ExpectedActiveRevisionID: draft.expectedActiveRevisionID,
		Status:                   model.AccessChangePreparing,
		PreviewHash:              draft.previewHash,
		AffectedUserCount:        draft.affectedUserCount,
		ActivateAt:               draft.activateAt,
		PayloadJSON:              string(payloadJSON),
		PrepareProjectionJSON:    string(prepareJSON),
		FinalizeProjectionJSON:   string(finalizeJSON),
		CreatedBy:                createdBy,
	}
	changeID, err := s.store.CreateAccessChange(ctx, change, draft.serverIDs)
	if err != nil {
		return nil, err
	}
	change.ID = changeID
	if _, err := s.queueAccessChangePhase(ctx, change, "prepare"); err != nil {
		_ = s.store.UpdateAccessChangeStatus(ctx, changeID, []model.AccessChangeStatus{model.AccessChangePreparing}, model.AccessChangeFailed, err.Error())
		return nil, err
	}
	change.Targets, err = s.store.ListAccessChangeTargets(ctx, changeID)
	if err != nil {
		return nil, err
	}
	s.wakeAccessWorkers()
	return change, nil
}

// generateServerCoreConfigForProjection generates one server's config from an
// explicit access projection instead of the live snapshot. Access changes use
// it so prepare deploys exactly old-union-new and finalize exactly the new set,
// independent of later plan edits.
func (s *Server) generateServerCoreConfigForProjection(ctx context.Context, server model.Server, data store.FullRoutingConfig, ledger *core.ProxyPathPortLedger, snap *core.EffectiveAccessSnapshot) (generatedServerCoreConfig, error) {
	var err error
	data.RoutingRules, err = s.routingRulesWithInterfaceIPStacks(ctx, server.ID, data.RoutingRules)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	resolveRoutingProxyPathNames(&data)
	inbounds, assets, err := s.prepareCertificateInbounds(ctx, data.Inbounds, server.ID)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	dnsState, err := core.DNSConfigStateForServer(server.ID, data.DNSLists, data.ServerDNSPolicies)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	bindings := snap.InboundUserBindings()
	pathBindings := snap.ProxyPathUserBindings()
	userPolicies := snap.UserLimitPolicyMap()
	accountingUsers := core.TrafficAccountingUsersForServer(server.ID, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, bindings, pathBindings)
	trafficPolicies, err := s.trafficRuntimePolicies(ctx, server.ID, data.Users, accountingUsers, userPolicies)
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	config, err := core.GenerateServerConfigWithOptions(server, inbounds, data.Outbounds, dnsState, data.Users, core.ConfigOptions{
		RoutingRules: data.RoutingRules, RoutingRuleSets: data.RoutingRuleSets, ExternalOutbounds: data.ExternalOutbounds, ProxyPaths: data.ProxyPaths, ProxyPathSteps: data.ProxyPathSteps,
		Servers: data.Servers, Inbounds: inbounds, WARPProfiles: data.WARPProfiles, InboundUsers: bindings, ProxyPathUsers: pathBindings,
		AccessSnapshot: snap, UserPolicies: userPolicies, TrafficPolicies: trafficPolicies, UserDevices: data.UserDevices,
		PortLedger: ledger,
	})
	if err != nil {
		return generatedServerCoreConfig{}, err
	}
	return generatedServerCoreConfig{Config: config, Assets: assets, Inbounds: inbounds, TrafficPolicies: trafficPolicies}, nil
}

// queueAccessChangePhase deploys one phase (prepare or finalize) to every
// target server with one shared configuration version. Servers whose config is
// already identical to the phase projection are recorded as done without a
// task.
func (s *Server) queueAccessChangePhase(ctx context.Context, change *model.AccessChange, phase string) (int, error) {
	if phase != "prepare" && phase != "finalize" {
		return 0, errors.New("invalid access change phase")
	}
	projectionJSON := change.PrepareProjectionJSON
	if phase == "finalize" {
		projectionJSON = change.FinalizeProjectionJSON
	}
	var projection core.AccessProjection
	if err := json.Unmarshal([]byte(projectionJSON), &projection); err != nil {
		return 0, err
	}
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return 0, err
	}
	ledger := core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)
	// Resolving derived forwards seeds the ledger with the ports the generated
	// listeners already own, so the config below reuses them instead of picking
	// new ones.
	_, err = core.DerivedPortForwardsFromProxyPathsWithLedger(data.ProxyPaths, data.ProxyPathSteps, data.Servers, data.Inbounds, ledger)
	if err != nil {
		return 0, err
	}
	snap := core.ProjectionSnapshot(projection, data.Users)
	serverByID := map[int64]model.Server{}
	for _, server := range data.Servers {
		serverByID[server.ID] = server
	}
	targets, err := s.store.ListAccessChangeTargets(ctx, change.ID)
	if err != nil {
		return 0, err
	}
	type preparedCoreRefresh struct {
		serverID int64
		payload  model.ApplyCoreConfigTaskPayload
	}
	prepared := make([]preparedCoreRefresh, 0, len(targets))
	for _, target := range targets {
		server, ok := serverByID[target.ServerID]
		if !ok || strings.TrimSpace(server.AgentID) == "" {
			if err := s.store.SetAccessChangeTargetTask(ctx, change.ID, target.ServerID, 0, phase, model.AccessChangeTargetPrepared); err != nil {
				return 0, err
			}
			continue
		}
		if err := requireReadyWARPForFocusedApply(data, server.ID); err != nil {
			return 0, err
		}
		generated, err := s.generateServerCoreConfigForProjection(ctx, server, data, ledger, snap)
		if err != nil {
			return 0, err
		}
		unchanged, err := s.serverConfigUnchanged(ctx, server.ID, generated.Config)
		if err != nil {
			return 0, err
		}
		if unchanged {
			if err := s.store.SetAccessChangeTargetTask(ctx, change.ID, target.ServerID, 0, phase, model.AccessChangeTargetPrepared); err != nil {
				return 0, err
			}
			continue
		}
		reason := "access_change_" + string(change.ChangeType) + "_" + phase
		prepared = append(prepared, preparedCoreRefresh{serverID: server.ID, payload: model.ApplyCoreConfigTaskPayload{Config: generated.Config, Reason: reason, Assets: generated.Assets}})
	}
	if len(prepared) == 0 {
		return 0, nil
	}
	version, err := s.store.NextConfigVersion(ctx)
	if err != nil {
		return 0, err
	}
	for _, item := range prepared {
		task, err := s.queueAgentTask(ctx, item.serverID, model.AgentTaskTypeApplyCoreConfig, item.payload, version)
		if err != nil {
			return 0, err
		}
		if err := s.store.SetAccessChangeTargetTask(ctx, change.ID, item.serverID, task.ID, phase, model.AccessChangeTargetPreparing); err != nil {
			return 0, err
		}
	}
	return len(prepared), nil
}

// accessChangePhaseDone reports whether every target of one phase finished
// (taskID 0 means the phase projection was already deployed) and the first
// failure text when any task failed.
func (s *Server) accessChangePhaseDone(ctx context.Context, change *model.AccessChange, phase string) (bool, string) {
	targets, err := s.store.ListAccessChangeTargets(ctx, change.ID)
	if err != nil {
		return false, err.Error()
	}
	for _, target := range targets {
		taskID := target.PrepareTaskID
		if phase == "finalize" {
			taskID = target.FinalizeTaskID
		}
		if taskID == 0 {
			continue
		}
		task, err := s.store.GetTask(ctx, taskID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return false, err.Error()
		}
		switch task.Status {
		case "succeeded":
			continue
		case "failed":
			return false, fmt.Sprintf("server %d task %d failed", target.ServerID, taskID)
		default:
			return false, ""
		}
	}
	return true, ""
}

// ---------------------------------------------------------------------------
// Activation
// ---------------------------------------------------------------------------

func (s *Server) activateAccessChange(ctx context.Context, change *model.AccessChange) error {
	var payload accessChangePayload
	_ = json.Unmarshal([]byte(change.PayloadJSON), &payload)
	switch change.ChangeType {
	case model.AccessChangePlanPublish, model.AccessChangePlanRestore:
		// Idempotent: a crash between durable activation and the finalize
		// queue leaves the change open; re-activating must not fail because
		// the activation already happened.
		plan, err := s.store.GetSubscriptionPlan(ctx, change.SourcePlanID)
		if err != nil {
			return err
		}
		if plan.CurrentRevisionID == change.CandidateRevisionID {
			return nil
		}
		migrations, err := s.planTrafficPeriodMigrations(ctx, change.SourcePlanID, change.ExpectedActiveRevisionID, change.CandidateRevisionID)
		if err != nil {
			return err
		}
		if err := s.store.ActivatePlanVersionGuarded(ctx, change.SourcePlanID, change.ExpectedActiveRevisionID, change.CandidateRevisionID, change.ID, migrations); err != nil {
			return err
		}
		s.signalPlanReconcile(change.SourcePlanID)
	case model.AccessChangePlanDisable:
		if err := s.store.SetSubscriptionPlanEnabled(ctx, change.SourcePlanID, false); err != nil {
			return err
		}
	case model.AccessChangePlanDelete:
		if err := s.store.ClearUserPlanBindingsForPlan(ctx, change.SourcePlanID); err != nil {
			return err
		}
	case model.AccessChangeUserBindings:
		if err := s.store.SetUserPlanBindingsActiveForUsers(ctx, payload.UserIDs); err != nil {
			return err
		}
	case model.AccessChangeExceptions:
		status := model.UserNodeExceptionStatus(payload.TargetStatus)
		for _, id := range payload.ExceptionIDs {
			if status != "" {
				if err := s.store.SetUserNodeExceptionStatus(ctx, id, status); err != nil {
					return err
				}
			}
		}
	default:
		return fmt.Errorf("unknown access change type %q", change.ChangeType)
	}
	if revision, err := s.store.ConfigurationRevision(ctx); err == nil && revision > 0 {
		// Activation is the desired-state commit for access changes. Draft and
		// pending rows are intentionally ignored by configuration triggers; only
		// the active transition enters the normal convergence coordinator.
		s.markConfigurationRevision(ctx, revision, nil)
	}
	return nil
}

func (s *Server) planTrafficPeriodMigrations(ctx context.Context, planID, currentRevisionID, candidateRevisionID int64) ([]store.TrafficPeriodMigration, error) {
	if currentRevisionID == 0 || candidateRevisionID == 0 {
		return nil, nil
	}
	current, err := s.store.GetPlanRevision(ctx, planID, currentRevisionID)
	if err != nil {
		return nil, err
	}
	candidate, err := s.store.GetPlanRevision(ctx, planID, candidateRevisionID)
	if err != nil {
		return nil, err
	}
	if current.TrafficResetMode == candidate.TrafficResetMode && current.TrafficResetDay == candidate.TrafficResetDay {
		return nil, nil
	}
	bindings, err := s.store.ListUserPlanBindingsForPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	usersByID := make(map[int64]model.User, len(users))
	for _, user := range users {
		usersByID[user.ID] = user
	}
	settings, _ := s.store.ListSettings(ctx)
	loc := trafficLocation(settings)
	now := time.Now()
	migrations := make([]store.TrafficPeriodMigration, 0, len(bindings))
	for _, binding := range bindings {
		if binding.StartsAt != nil && binding.StartsAt.After(now) || binding.ExpiresAt != nil && !binding.ExpiresAt.After(now) {
			continue
		}
		user, ok := usersByID[binding.UserID]
		if !ok || user.Status != "active" || user.TrafficLimitBytes != 0 {
			continue
		}
		anchor := binding.CreatedAt
		if binding.TrafficResetAnchorAt != nil {
			anchor = *binding.TrafficResetAnchorAt
		}
		oldKey, _, _ := trafficWindow(now, current.TrafficResetMode, current.TrafficResetDay, anchor, loc)
		if resolved, _, resolveErr := s.store.ResolveTrafficPeriodKey(ctx, user.ID, oldKey); resolveErr != nil {
			return nil, resolveErr
		} else {
			oldKey = resolved
		}
		newKey, newStart, newEnd := trafficWindow(now, candidate.TrafficResetMode, candidate.TrafficResetDay, anchor, loc)
		if oldKey == newKey {
			continue
		}
		migrations = append(migrations, store.TrafficPeriodMigration{UserID: user.ID, SourcePeriodKey: oldKey, TargetPeriodKey: newKey, TargetStart: newStart, TargetEnd: newEnd, TrafficLimit: candidate.TrafficLimitBytes})
	}
	return migrations, nil
}

func (s *Server) markAccessChangeFailed(ctx context.Context, change *model.AccessChange, message string) {
	_ = s.store.UpdateAccessChangeStatus(ctx, change.ID, []model.AccessChangeStatus{model.AccessChangePreparing, model.AccessChangeActivating, model.AccessChangeFinalizing}, model.AccessChangeFailed, message)
}

// proceedAccessChangeActivation switches the durable state and starts the
// finalize phase. Called by the worker when prepare completed (or the change's
// activate_at time arrived).
func (s *Server) proceedAccessChangeActivation(ctx context.Context, change *model.AccessChange) {
	if err := s.activateAccessChange(ctx, change); err != nil {
		if store.IsSQLiteBusy(err) {
			log.Printf("access change %d activation waiting for SQLite writer; will retry: %v", change.ID, err)
			return
		}
		s.markAccessChangeFailed(ctx, change, "activate: "+err.Error())
		return
	}
	if _, err := s.queueAccessChangePhase(ctx, change, "finalize"); err != nil {
		s.markAccessChangeFailed(ctx, change, "finalize queue: "+err.Error())
		return
	}
	if err := s.store.UpdateAccessChangeStatus(ctx, change.ID, []model.AccessChangeStatus{model.AccessChangePreparing, model.AccessChangeActivating}, model.AccessChangeFinalizing, ""); err != nil {
		s.markAccessChangeFailed(ctx, change, err.Error())
	}
}

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

// StartAccessChangeWorker drives the prepare -> activate -> finalize state
// machine. It is safe to restart: open changes are re-read from the database
// and the phase tasks are already persisted with their task ids. Wake events
// (new changes, retries, task completions) drive progress; a jittered
// 30-60 second recovery scan covers missed events.
func (s *Server) StartAccessChangeWorker(ctx context.Context) {
	recoveryMin, recoveryMax := s.taskRecoveryScanMin, s.taskRecoveryScanMax
	if recoveryMin <= 0 {
		recoveryMin = defaultTaskRecoveryScanMin
	}
	if recoveryMax <= recoveryMin {
		recoveryMax = recoveryMin + defaultTaskRecoveryScanMin
	}
	// Progress open changes left over from a restart immediately.
	s.reconcileAccessChanges(ctx)
	timer := time.NewTimer(recoveryMin)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.accessWorkersWake:
			timer.Stop()
			s.reconcileAccessChanges(ctx)
			timer.Reset(recoveryMin + time.Duration(rand.Int63n(int64(recoveryMax-recoveryMin))))
		case <-timer.C:
			s.reconcileAccessChanges(ctx)
			timer.Reset(recoveryMin + time.Duration(rand.Int63n(int64(recoveryMax-recoveryMin))))
		}
	}
}

// wakeAccessWorkers wakes the access-change and authorization-lifecycle
// workers. The wake is a coalesced hint; both workers keep a database-backed
// recovery scan as fallback.
func (s *Server) wakeAccessWorkers() {
	select {
	case s.accessWorkersWake <- struct{}{}:
	default:
	}
}

func (s *Server) reconcileAccessChanges(ctx context.Context) {
	changes, err := s.store.ListAccessChangesByStatus(ctx, model.AccessChangePreparing, model.AccessChangeActivating, model.AccessChangeFinalizing)
	if err != nil {
		log.Printf("access change reconcile: %v", err)
		return
	}
	for _, change := range changes {
		switch change.Status {
		case model.AccessChangePreparing:
			done, failure := s.accessChangePhaseDone(ctx, &change, "prepare")
			if failure != "" {
				s.markAccessChangeFailed(ctx, &change, failure)
				continue
			}
			if !done {
				continue
			}
			if change.ActivateAt != nil && time.Now().Before(*change.ActivateAt) {
				_ = s.store.UpdateAccessChangeStatus(ctx, change.ID, []model.AccessChangeStatus{model.AccessChangePreparing}, model.AccessChangeActivating, "")
				continue
			}
			s.proceedAccessChangeActivation(ctx, &change)
		case model.AccessChangeActivating:
			if change.ActivateAt != nil && time.Now().Before(*change.ActivateAt) {
				continue
			}
			s.proceedAccessChangeActivation(ctx, &change)
		case model.AccessChangeFinalizing:
			done, failure := s.accessChangePhaseDone(ctx, &change, "finalize")
			if failure != "" {
				s.markAccessChangeFailed(ctx, &change, failure)
				continue
			}
			if !done {
				continue
			}
			if change.ChangeType == model.AccessChangePlanDelete && change.SourcePlanID != 0 {
				if err := s.store.DetachAndDeleteSubscriptionPlan(ctx, change.SourcePlanID, change.ID); err != nil {
					s.markAccessChangeFailed(ctx, &change, err.Error())
					continue
				}
			}
			if err := s.store.UpdateAccessChangeStatus(ctx, change.ID, []model.AccessChangeStatus{model.AccessChangeFinalizing}, model.AccessChangeFinalized, ""); err != nil {
				log.Printf("access change finalize status: %v", err)
			} else if change.SourcePlanID != 0 {
				s.signalPlanReconcile(change.SourcePlanID)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func (s *Server) accessChanges(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/api/v1/access-changes/")
	switch r.Method {
	case http.MethodGet:
		if id != 0 {
			change, err := s.store.GetAccessChange(r.Context(), id)
			if err != nil {
				fail(w, err, 404)
				return
			}
			write(w, 200, map[string]any{"access_change": change, "runtime_authorization_mode": s.authorizationMode(r.Context())})
			return
		}
		limit := intQuery(r, "limit", 50)
		changes, err := s.store.ListAccessChanges(r.Context(), limit)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"access_changes": changes, "runtime_authorization_mode": s.authorizationMode(r.Context())})
	case http.MethodPost:
		if id == 0 {
			fail(w, errors.New("missing id"), 400)
			return
		}
		action := ""
		parts := pathParts(r.URL.Path, "/api/v1/access-changes/")
		if len(parts) == 2 && parts[1] != "" {
			action = parts[1]
		}
		switch action {
		case "retry":
			s.accessChangeRetry(w, r, id)
		case "cancel":
			s.accessChangeCancel(w, r, id)
		default:
			fail(w, errors.New("expected /api/v1/access-changes/:id/retry or /cancel"), 404)
		}
	default:
		method(w)
	}
}

func (s *Server) accessChangeRetry(w http.ResponseWriter, r *http.Request, id int64) {
	phase, queued, err := s.retryAccessChange(r.Context(), id)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		fail(w, err, status)
		return
	}
	auditReq(s, r, "retry", "access-change", fmt.Sprint(id))
	write(w, 200, map[string]any{"access_change_id": id, "phase": phase, "queued_tasks": queued, "status": phaseStatusName(phase)})
}

// retryAccessChange resumes a failed access change from its durable failure
// point: the prepare phase (or only the finalize phase when activation already
// completed) is re-queued and the Controller worker continues the state
// machine. It is shared by the panel endpoint and the MCP workflow retry.
func (s *Server) retryAccessChange(ctx context.Context, id int64) (phase string, queued int, err error) {
	change, err := s.store.GetAccessChange(ctx, id)
	if err != nil {
		return "", 0, err
	}
	if change.Status != model.AccessChangeFailed {
		return "", 0, errors.New("only failed access changes can be retried")
	}
	phase = "prepare"
	if change.ActivatedAt != nil {
		phase = "finalize"
	}
	if err := s.store.UpdateAccessChangeStatus(ctx, id, []model.AccessChangeStatus{model.AccessChangeFailed}, model.AccessChangePreparing, ""); err != nil {
		return "", 0, err
	}
	if phase == "finalize" {
		// Keep the durable activation; only the finalize phase is retried.
		_ = s.store.UpdateAccessChangeStatus(ctx, id, []model.AccessChangeStatus{model.AccessChangePreparing}, model.AccessChangeFinalizing, "")
	}
	queued, err = s.queueAccessChangePhase(ctx, change, phase)
	if err != nil {
		s.markAccessChangeFailed(ctx, change, "retry: "+err.Error())
		return "", 0, err
	}
	s.wakeAccessWorkers()
	return phase, queued, nil
}

func phaseStatusName(phase string) string {
	if phase == "finalize" {
		return string(model.AccessChangeFinalizing)
	}
	return string(model.AccessChangePreparing)
}

func (s *Server) accessChangeCancel(w http.ResponseWriter, r *http.Request, id int64) {
	change, err := s.store.GetAccessChange(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	failedPlanChange := change.Status == model.AccessChangeFailed && change.ActivatedAt == nil && (change.ChangeType == model.AccessChangePlanPublish || change.ChangeType == model.AccessChangePlanRestore)
	if change.Status != model.AccessChangePreparing && change.Status != model.AccessChangeActivating && !failedPlanChange {
		fail(w, errors.New("access change already activated; cancel is not possible"), http.StatusConflict)
		return
	}
	if err := s.store.MarkAccessChangeCancelled(r.Context(), id); err != nil {
		fail(w, err, http.StatusConflict)
		return
	}
	if change.SourcePlanID != 0 && change.CandidateRevisionID != 0 {
		plan, err := s.store.GetSubscriptionPlan(r.Context(), change.SourcePlanID)
		if err == nil && plan.PendingRevisionID == change.CandidateRevisionID {
			_, _ = s.store.SetPendingIfEmpty(r.Context(), plan.ID, 0) // clear by direct update
			// Use raw exec to clear pending (SetPendingIfEmpty only sets when empty, so we need direct)
			_, _ = s.store.ClearPendingRevision(r.Context(), plan.ID, change.CandidateRevisionID)
		}
		_ = s.store.SetPlanReconcileIdle(r.Context(), change.SourcePlanID)
		s.signalPlanReconcile(change.SourcePlanID)
	}
	s.wakeAccessWorkers()
	auditReq(s, r, "cancel", "access-change", fmt.Sprint(id))
	write(w, 200, map[string]any{"access_change_id": id, "status": model.AccessChangeCancelled})
}

func (s *Server) accessChangeAbandonable(ctx context.Context, change *model.AccessChange) bool {
	if change == nil || change.Status != model.AccessChangeFailed || change.ActivatedAt != nil || change.SourcePlanID == 0 || change.CandidateRevisionID == 0 ||
		(change.ChangeType != model.AccessChangePlanPublish && change.ChangeType != model.AccessChangePlanRestore) {
		return false
	}
	plan, err := s.store.GetSubscriptionPlan(ctx, change.SourcePlanID)
	return err == nil && plan.PendingRevisionID == change.CandidateRevisionID
}

// planChangePreviewHandler serves POST /subscription-plans/:id/changes/preview.
func (s *Server) planChangePreviewHandler(w http.ResponseWriter, r *http.Request, id int64) {
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	var req struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	_ = decode(w, r, &req)
	expected := req.ExpectedRevision
	if expected == 0 {
		expected = plan.LockVersion
	}
	if plan.LockVersion != expected {
		fail(w, store.ErrPlanRevisionConflict, http.StatusConflict)
		return
	}
	if plan.PendingRevisionID == 0 {
		fail(w, errors.New("subscription plan has no pending version to preview"), 400)
		return
	}
	preview, err := s.planChangePreview(r.Context(), plan, plan.PendingRevisionID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	preview["status"] = "ready"
	preview["runtime_authorization_mode"] = s.authorizationMode(r.Context())
	write(w, 200, preview)
}

// planChangeApplyHandler serves POST /subscription-plans/:id/changes/apply.
func (s *Server) planChangeApplyHandler(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		PreviewHash              string `json:"preview_hash"`
		ExpectedActiveRevisionID int64  `json:"expected_active_revision_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if req.ExpectedActiveRevisionID != 0 && plan.CurrentRevisionID != req.ExpectedActiveRevisionID {
		fail(w, store.ErrPlanRevisionConflict, http.StatusConflict)
		return
	}
	if plan.PendingRevisionID == 0 {
		// No pending version means the plan state has already moved on (the
		// pending version was applied, cancelled, or never created). Treat a
		// stale apply attempt as a conflict so the client re-previewes
		// instead of silently succeeding.
		fail(w, store.ErrPlanRevisionConflict, http.StatusConflict)
		return
	}
	preview, err := s.planChangePreview(r.Context(), plan, plan.PendingRevisionID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	currentHash, _ := preview["preview_hash"].(string)
	if req.PreviewHash == "" || req.PreviewHash != currentHash {
		fail(w, errors.New("preview_hash mismatch: the plan changed since preview; re-preview"), http.StatusConflict)
		return
	}
	change, err := s.createPlanPublishChange(r.Context(), r, plan, plan.PendingRevisionID)
	if err != nil {
		fail(w, err, planWriteStatus(err))
		return
	}
	auditReq(s, r, "apply", "access-change", fmt.Sprintf("plan=%d change=%d", id, change.ID))
	write(w, 200, map[string]any{"access_change_id": change.ID, "status": change.Status, "queued_tasks": len(change.Targets), "access_change": change, "runtime_authorization_mode": s.authorizationMode(r.Context())})
}

// createPlanPublishChange builds the prepare/finalize projections for a plan
// revision publish and creates the access change.
func (s *Server) createPlanPublishChange(ctx context.Context, r *http.Request, plan *model.SubscriptionPlan, revisionID int64) (*model.AccessChange, error) {
	var actorID *int64
	if r != nil {
		actorID = requestActorID(r)
	}
	return s.createPlanPublishChangeForActor(ctx, r, actorID, plan, revisionID)
}

func (s *Server) createPlanPublishChangeForActor(ctx context.Context, r *http.Request, actorID *int64, plan *model.SubscriptionPlan, revisionID int64) (*model.AccessChange, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.ListEffectiveUserPlanBindings(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	oldSnap := s.snapshotFromConfig(data, bindings, data.ActivePlanNodes, exceptions, now)
	newSnap, err := s.planSnapshotForRevision(ctx, data, plan, revisionID, bindings, exceptions, now)
	if err != nil {
		return nil, err
	}
	prepare := core.MergeProjections(oldSnap.Projection(), newSnap.Projection())
	finalize := newSnap.Projection()
	diffKeys := map[string]bool{}
	for _, key := range planNodeKeyDiff(oldSnap, newSnap) {
		diffKeys[key] = true
	}
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	servers, _, _ := core.AffectedAuthServers(diffKeys, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	affectedUsers := 0
	for _, binding := range bindings {
		if binding.PlanID == plan.ID {
			affectedUsers++
		}
	}
	preview, err := s.planChangePreview(ctx, plan, revisionID)
	if err != nil {
		return nil, err
	}
	hash, _ := preview["preview_hash"].(string)
	change, err := s.createAccessChange(ctx, r, accessChangeDraft{
		changeType:               model.AccessChangePlanPublish,
		sourcePlanID:             plan.ID,
		candidateRevisionID:      revisionID,
		expectedActiveRevisionID: plan.CurrentRevisionID,
		previewHash:              hash,
		affectedUserCount:        affectedUsers,
		prepareProjection:        prepare,
		finalizeProjection:       finalize,
		serverIDs:                servers,
		createdBy:                actorID,
	})
	if err != nil {
		return nil, err
	}
	if err := s.store.SetPlanRevisionActivationChange(ctx, plan.ID, revisionID, change.ID); err != nil {
		return nil, err
	}
	return change, nil
}

// createPlanDisableChange materializes the disable projections: prepare keeps
// the current (still-enabled) state, activation disables the plan, finalize
// prunes the bound users' credentials.
func (s *Server) createPlanDisableChange(ctx context.Context, r *http.Request, plan *model.SubscriptionPlan) (*model.AccessChange, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.ListEffectiveUserPlanBindings(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	oldSnap := s.snapshotFromConfig(data, bindings, data.ActivePlanNodes, exceptions, now)
	newSnap := s.planSnapshotDisabled(data, plan, bindings, exceptions, now)
	prepare := core.MergeProjections(oldSnap.Projection(), newSnap.Projection())
	activeNodes, err := s.store.ListActivePlanNodes(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, pn := range activeNodes {
		keys[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
	}
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	servers, _, _ := core.AffectedAuthServers(keys, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	affectedUsers := 0
	for _, binding := range bindings {
		if binding.PlanID == plan.ID {
			affectedUsers++
		}
	}
	change, err := s.createAccessChange(ctx, r, accessChangeDraft{
		changeType:         model.AccessChangePlanDisable,
		sourcePlanID:       plan.ID,
		affectedUserCount:  affectedUsers,
		activateAt:         &now,
		prepareProjection:  prepare,
		finalizeProjection: newSnap.Projection(),
		serverIDs:          servers,
	})
	if err != nil {
		return nil, err
	}
	return change, nil
}

// planDisable serves POST /subscription-plans/:id/disable. In plan mode the
// disable is an access change (prepare keeps credentials until activation);
// in legacy mode the flag flips directly because plans are data only.
func (s *Server) planDisable(w http.ResponseWriter, r *http.Request, id int64) {
	var req struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	_ = decode(w, r, &req)
	plan, err := s.store.GetSubscriptionPlan(r.Context(), id)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if req.ExpectedRevision != 0 && plan.Revision != req.ExpectedRevision {
		fail(w, store.ErrPlanRevisionConflict, http.StatusConflict)
		return
	}
	if !plan.Enabled {
		fail(w, errors.New("subscription plan is already disabled"), 400)
		return
	}
	change, err := s.createPlanDisableChange(r.Context(), r, plan)
	if err != nil {
		fail(w, err, planWriteStatus(err))
		return
	}
	auditReq(s, r, "disable", "access-change", fmt.Sprintf("plan=%d change=%d", id, change.ID))
	write(w, 200, map[string]any{"disabled": false, "access_change_id": change.ID, "access_change_status": change.Status, "runtime_authorization_mode": s.authorizationMode(r.Context())})
}

// createPlanDeleteChange materializes delete projections: prepare keeps the
// current grants, activation unbinds users from the plan, and finalize prunes
// credentials then physically deletes the plan. Bound users keep their
// accounts and become planless.
func (s *Server) createPlanDeleteChange(ctx context.Context, r *http.Request, actorID *int64, plan *model.SubscriptionPlan, affectedUsers int) (*model.AccessChange, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.ListEffectiveUserPlanBindings(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	oldSnap := s.snapshotFromConfig(data, bindings, data.ActivePlanNodes, exceptions, now)
	newSnap := s.planSnapshotDisabled(data, plan, bindings, exceptions, now)
	prepare := core.MergeProjections(oldSnap.Projection(), newSnap.Projection())
	activeNodes, err := s.store.ListActivePlanNodes(ctx, plan.ID)
	if err != nil {
		return nil, err
	}
	keys := map[string]bool{}
	for _, pn := range activeNodes {
		keys[core.NodeKeyOf(pn.NodeType, pn.NodeID)] = true
	}
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	servers, _, _ := core.AffectedAuthServers(keys, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	return s.createAccessChange(ctx, r, accessChangeDraft{
		changeType:         model.AccessChangePlanDelete,
		sourcePlanID:       plan.ID,
		affectedUserCount:  affectedUsers,
		activateAt:         &now,
		prepareProjection:  prepare,
		finalizeProjection: newSnap.Projection(),
		serverIDs:          servers,
		createdBy:          actorID,
	})
}

// createUserBindingChange materializes the two-phase projections for a user
// plan assignment. The new bindings are stored pending; activation flips them
// active (at starts_at when the assignment is scheduled) and finalize prunes
// the old plan's credentials.
func (s *Server) createUserBindingChange(ctx context.Context, r *http.Request, data store.FullRoutingConfig, userIDs []int64, newBindings []model.UserPlanBinding, startsAt, expiresAt *time.Time) (*model.AccessChange, error) {
	now := time.Now()
	exceptions, err := s.store.ListUserNodeExceptions(ctx)
	if err != nil {
		return nil, err
	}
	oldEnabled, err := s.store.ListEnabledUserPlanBindings(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	oldEffective := filterEffectiveBindings(oldEnabled, now)
	oldSnap := s.snapshotFromConfig(data, oldEffective, data.ActivePlanNodes, exceptions, now)
	at := effectiveWindow(now, startsAt, expiresAt)
	newEffective := filterEffectiveBindings(newBindings, at)
	newSnap := s.snapshotFromConfig(data, newEffective, data.ActivePlanNodes, exceptions, at)
	prepare := core.MergeProjections(oldSnap.Projection(), newSnap.Projection())
	finalize := newSnap.Projection()
	diffKeys := map[string]bool{}
	for _, key := range planNodeKeyDiff(oldSnap, newSnap) {
		diffKeys[key] = true
	}
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	servers, _, _ := core.AffectedAuthServers(diffKeys, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	change, err := s.createAccessChange(ctx, r, accessChangeDraft{
		changeType:         model.AccessChangeUserBindings,
		affectedUserCount:  len(userIDs),
		activateAt:         &at,
		payload:            accessChangePayload{UserIDs: userIDs},
		prepareProjection:  prepare,
		finalizeProjection: finalize,
		serverIDs:          servers,
	})
	if err != nil {
		return nil, err
	}
	return change, nil
}

// createExceptionChange materializes the two-phase projections for one
// exception mutation. before/after are the exception lists the runtime moves
// from/to; targetStatus is applied to the changed exception at activation.
func (s *Server) createExceptionChange(ctx context.Context, r *http.Request, data store.FullRoutingConfig, before, after []model.UserNodeException, changed model.UserNodeException, targetStatus model.UserNodeExceptionStatus) (*model.AccessChange, error) {
	now := time.Now()
	at := effectiveWindow(now, changed.StartsAt, changed.ExpiresAt)
	change, err := s.createExceptionChanges(ctx, r, data, before, after, []int64{changed.ID}, targetStatus, 1, at)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetUserNodeExceptionChange(ctx, changed.ID, change.ID); err != nil {
		return nil, err
	}
	return change, nil
}

// createExceptionChanges materializes the two-phase projections for one
// aggregate exception mutation (single-row or batch). before/after are the
// exception lists the runtime moves from/to; exceptionIDs are the rows that
// become visible at activation and targetStatus is applied to each of them.
// The batch always produces exactly one access change.
func (s *Server) createExceptionChanges(ctx context.Context, r *http.Request, data store.FullRoutingConfig, before, after []model.UserNodeException, exceptionIDs []int64, targetStatus model.UserNodeExceptionStatus, affectedUserCount int, at time.Time) (*model.AccessChange, error) {
	return s.createExceptionChangesForActor(ctx, r, nil, data, before, after, exceptionIDs, targetStatus, affectedUserCount, at)
}

func (s *Server) createExceptionChangesForActor(ctx context.Context, r *http.Request, actorID *int64, data store.FullRoutingConfig, before, after []model.UserNodeException, exceptionIDs []int64, targetStatus model.UserNodeExceptionStatus, affectedUserCount int, at time.Time) (*model.AccessChange, error) {
	effective, err := s.store.ListEffectiveUserPlanBindings(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	oldSnap := s.snapshotFromConfig(data, effective, data.ActivePlanNodes, before, time.Now())
	newSnap := s.snapshotFromConfig(data, effective, data.ActivePlanNodes, after, at)
	prepare := core.MergeProjections(oldSnap.Projection(), newSnap.Projection())
	finalize := newSnap.Projection()
	diffKeys := map[string]bool{}
	for _, key := range planNodeKeyDiff(oldSnap, newSnap) {
		diffKeys[key] = true
	}
	serverOnline := make(map[int64]bool, len(data.Servers))
	for _, server := range data.Servers {
		serverOnline[server.ID] = server.Status == model.ServerOnline
	}
	servers, _, _ := core.AffectedAuthServers(diffKeys, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, serverOnline)
	change, err := s.createAccessChange(ctx, r, accessChangeDraft{
		changeType:         model.AccessChangeExceptions,
		affectedUserCount:  affectedUserCount,
		activateAt:         &at,
		payload:            accessChangePayload{ExceptionIDs: exceptionIDs, TargetStatus: string(targetStatus)},
		prepareProjection:  prepare,
		finalizeProjection: finalize,
		serverIDs:          servers,
		createdBy:          actorID,
	})
	if err != nil {
		return nil, err
	}
	return change, nil
}

// exceptionChangeAfterWrite creates the access change for an exception that
// was already stored. The projection list forces the changed exception active
// so the prepare phase deploys its node before it becomes visible.
func (s *Server) exceptionChangeAfterWrite(ctx context.Context, r *http.Request, before []model.UserNodeException, v model.UserNodeException) (*model.AccessChange, error) {
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return nil, err
	}
	projectionV := v
	projectionV.Status = model.UserNodeExceptionActive
	after := exceptionsWith(before, projectionV)
	targetStatus := model.UserNodeExceptionStatus("")
	if v.Effect == model.UserNodeExceptionAllow {
		targetStatus = model.UserNodeExceptionActive
	}
	return s.createExceptionChange(ctx, r, data, before, after, v, targetStatus)
}

func filterEffectiveBindings(bindings []model.UserPlanBinding, at time.Time) []model.UserPlanBinding {
	out := make([]model.UserPlanBinding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.StartsAt != nil && binding.StartsAt.After(at) {
			continue
		}
		if binding.ExpiresAt != nil && !binding.ExpiresAt.After(at) {
			continue
		}
		out = append(out, binding)
	}
	return out
}

// planNodeKeyDiff returns the node keys whose membership differs between two
// snapshots.
func planNodeKeyDiff(a, b *core.EffectiveAccessSnapshot) []string {
	keys := map[string]bool{}
	for userID := range a.UserNodes {
		for key := range a.UserNodes[userID] {
			keys[key] = true
		}
	}
	for userID := range b.UserNodes {
		for key := range b.UserNodes[userID] {
			keys[key] = true
		}
	}
	diff := map[string]bool{}
	for key := range keys {
		if nodeKeyUsersDiffer(a, b, key) {
			diff[key] = true
		}
	}
	return sortedNodeKeys(diff)
}

// nodeKeyUsersDiffer reports whether the granted user set for one node key
// differs between two snapshots.
func nodeKeyUsersDiffer(a, b *core.EffectiveAccessSnapshot, key string) bool {
	usersA := map[int64]bool{}
	for userID, grants := range a.UserNodes {
		if _, ok := grants[key]; ok {
			usersA[userID] = true
		}
	}
	usersB := map[int64]bool{}
	for userID, grants := range b.UserNodes {
		if _, ok := grants[key]; ok {
			usersB[userID] = true
		}
	}
	if len(usersA) != len(usersB) {
		return true
	}
	for userID := range usersA {
		if !usersB[userID] {
			return true
		}
	}
	return false
}

// guardAssignableNodeDelete validates that deletion is not racing an
// in-flight subscription change. Live references are removed as part of the
// deletion transaction (or by the caller immediately before deleting a
// non-path resource), so a published plan no longer blocks topology cleanup.
func (s *Server) guardAssignableNodeDelete(ctx context.Context, nodeType model.AssignableNodeType, nodeID int64) (store.PlanNodeReferences, error) {
	refs, err := s.store.PlanNodeReferences(ctx, nodeType, nodeID)
	if err != nil {
		return refs, err
	}
	if len(refs.Pending) > 0 {
		names := make([]string, 0, len(refs.Pending))
		seen := map[string]bool{}
		for _, ref := range refs.Pending {
			if seen[ref.Name] {
				continue
			}
			names = append(names, ref.Name)
			seen[ref.Name] = true
		}
		return refs, fmt.Errorf("subscription plan(s) are still applying a change: %s; retry after it finishes", strings.Join(names, ", "))
	}
	seenPlans := map[int64]bool{}
	for _, ref := range append(append([]store.PlanNodeReference{}, refs.Active...), refs.Draft...) {
		if seenPlans[ref.PlanID] {
			continue
		}
		seenPlans[ref.PlanID] = true
		plan, err := s.store.GetSubscriptionPlan(ctx, ref.PlanID)
		if err != nil {
			return refs, err
		}
		if plan.PendingRevisionID != 0 {
			return refs, fmt.Errorf("subscription plan(s) are still applying a change: %s; retry after it finishes", plan.Name)
		}
	}
	return refs, nil
}
