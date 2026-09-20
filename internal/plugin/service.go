package plugin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/authorization"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type Settings struct {
	Enabled            bool
	HostActionsEnabled bool
	SchedulerPaused    bool
	RecoveryGeneration int64
	MaxConcurrency     int
	MaxTimeoutSeconds  int
}

type Service struct {
	store          *store.Store
	rbac           *authorization.RBAC
	now            func() time.Time
	callerResolver CallerResolver
	packageFetcher packageFetcher
}

func NewService(db *store.Store, rbac *authorization.RBAC) *Service {
	return &Service{store: db, rbac: rbac, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Settings(ctx context.Context) Settings {
	values, _ := s.store.ListSettings(ctx)
	timeout := atoiDefault(values[model.PluginSettingMaxTimeoutSeconds], MaxTimeoutSeconds)
	if timeout <= 0 || timeout > MaxTimeoutSeconds {
		timeout = MaxTimeoutSeconds
	}
	concurrency := atoiDefault(values[model.PluginSettingMaxConcurrency], DefaultControllerConcurrency)
	if concurrency <= 0 {
		concurrency = DefaultControllerConcurrency
	}
	return Settings{
		Enabled:            values[model.PluginSettingEnabled] == "true",
		HostActionsEnabled: values[model.PluginSettingHostActionsEnabled] == "true",
		SchedulerPaused:    values[model.PluginSettingSchedulerPaused] == "true",
		RecoveryGeneration: int64(atoiDefault(values[model.PluginSettingRecoveryGeneration], 1)),
		MaxConcurrency:     concurrency,
		MaxTimeoutSeconds:  timeout,
	}
}

func (s *Service) UpdateSettings(ctx context.Context, actor application.Principal, next Settings) error {
	if err := s.requireAdmin(actor, "plugins.settings"); err != nil {
		return err
	}
	if next.MaxConcurrency < 1 || next.MaxConcurrency > 8 {
		return Coded(codeInvalidInput, "max_concurrency is out of range")
	}
	if next.MaxTimeoutSeconds < 5 || next.MaxTimeoutSeconds > MaxTimeoutSeconds {
		return Coded(codeInvalidInput, "max_timeout_seconds is out of range")
	}
	return s.store.SetSettings(ctx, map[string]string{
		model.PluginSettingEnabled:            strconv.FormatBool(next.Enabled),
		model.PluginSettingHostActionsEnabled: strconv.FormatBool(next.HostActionsEnabled),
		model.PluginSettingSchedulerPaused:    strconv.FormatBool(next.SchedulerPaused),
		model.PluginSettingMaxConcurrency:     strconv.Itoa(next.MaxConcurrency),
		model.PluginSettingMaxTimeoutSeconds:  strconv.Itoa(next.MaxTimeoutSeconds),
	})
}

func (s *Service) CreatePlugin(ctx context.Context, actor application.Principal, name, description string) (model.Plugin, error) {
	if err := s.require(actor, "plugins.draft"); err != nil {
		return model.Plugin{}, err
	}
	if actor.UserID == nil {
		return model.Plugin{}, Coded(codePermissionDenied, "plugin ownership requires a user")
	}
	item := model.Plugin{Name: strings.TrimSpace(name), Description: strings.TrimSpace(description), OwnerUserID: *actor.UserID, Status: model.PluginStatusDraft}
	if item.Name == "" {
		return model.Plugin{}, Coded(codeInvalidInput, "name is required")
	}
	if err := s.store.CreatePlugin(ctx, &item); err != nil {
		return model.Plugin{}, err
	}
	return item, nil
}

func (s *Service) UpdatePlugin(ctx context.Context, actor application.Principal, id int64, name, description, status, expectedUpdatedAt string) (model.Plugin, error) {
	if err := s.require(actor, "plugins.draft"); err != nil {
		return model.Plugin{}, err
	}
	item, err := s.store.GetPlugin(ctx, id)
	if err != nil {
		return model.Plugin{}, err
	}
	if expectedUpdatedAt != "" && !item.UpdatedAt.UTC().Equal(parseRFC3339(expectedUpdatedAt)) {
		return model.Plugin{}, ErrConflict
	}
	if name != "" {
		item.Name = name
	}
	if description != "" || description == "" && name != "" {
		item.Description = description
	}
	if status != "" {
		if item.Status == model.PluginStatusArchived && status != model.PluginStatusArchived {
			return model.Plugin{}, Coded(codeInvalidInput, "archived plugins cannot be re-enabled")
		}
		item.Status = status
	}
	if err := s.store.UpdatePlugin(ctx, &item, item.UpdatedAt); err != nil {
		return model.Plugin{}, err
	}
	return item, nil
}

func (s *Service) GetPlugin(ctx context.Context, actor application.Principal, id int64) (model.Plugin, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return model.Plugin{}, err
	}
	return s.store.GetPlugin(ctx, id)
}

func (s *Service) ListPlugins(ctx context.Context, actor application.Principal, status string, limit int) ([]model.Plugin, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return nil, err
	}
	return s.store.ListPlugins(ctx, status, limit)
}

func (s *Service) SaveDraft(ctx context.Context, actor application.Principal, pluginID int64, source string, manifestJSON json.RawMessage) (model.PluginRevision, error) {
	if err := s.require(actor, "plugins.draft"); err != nil {
		return model.PluginRevision{}, err
	}
	if err := ValidateSource(source); err != nil {
		return model.PluginRevision{}, err
	}
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		return model.PluginRevision{}, err
	}
	author := int64(0)
	if actor.UserID != nil {
		author = *actor.UserID
	}
	rev := model.PluginRevision{
		PluginID: pluginID, Status: model.PluginRevisionDraft, SchemaVersion: manifest.SchemaVersion,
		Runtime: manifest.Runtime, SDKVersion: manifest.SDKVersion, Source: source,
		SourceDigest: RevisionDigest(source, manifest), ManifestJSON: MustJSON(manifest), AuthorUserID: author,
	}
	if err := s.store.SavePluginDraft(ctx, &rev); err != nil {
		return model.PluginRevision{}, err
	}
	return rev, nil
}

func (s *Service) Publish(ctx context.Context, actor application.Principal, pluginID, revisionID int64) (model.PluginRevision, error) {
	if err := s.require(actor, "plugins.publish"); err != nil {
		return model.PluginRevision{}, err
	}
	rev, err := s.store.GetPluginRevision(ctx, revisionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.PluginRevision{}, ErrNotFound
		}
		return model.PluginRevision{}, err
	}
	if rev.PluginID != pluginID {
		return model.PluginRevision{}, ErrNotFound
	}
	if _, err := ParseManifest(rev.ManifestJSON); err != nil {
		return model.PluginRevision{}, err
	}
	if err := ValidateSource(rev.Source); err != nil {
		return model.PluginRevision{}, err
	}
	publisher := int64(0)
	if actor.UserID != nil {
		publisher = *actor.UserID
	}
	return s.store.PublishPluginRevision(ctx, pluginID, revisionID, publisher)
}

func (s *Service) ValidateRevision(ctx context.Context, actor application.Principal, pluginID int64, source string, manifestJSON, params json.RawMessage) (map[string]any, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return nil, err
	}
	if source == "" && pluginID > 0 {
		if draft, err := s.store.GetPluginDraft(ctx, pluginID); err == nil {
			source = draft.Source
			if len(manifestJSON) == 0 {
				manifestJSON = draft.ManifestJSON
			}
		}
	}
	errs := []string{}
	if err := ValidateSource(source); err != nil {
		errs = append(errs, err.Error())
	}
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		errs = append(errs, err.Error())
	} else if err := ValidateParams(manifest.Params, params); err != nil {
		errs = append(errs, err.Error())
	}
	return map[string]any{"valid": len(errs) == 0, "errors": errs, "source_compiled": false}, nil
}

func (s *Service) CreateTrigger(ctx context.Context, actor application.Principal, item model.PluginTriggerBinding) (model.PluginTriggerBinding, error) {
	if err := s.require(actor, "plugins.triggers"); err != nil {
		return model.PluginTriggerBinding{}, err
	}
	spec, err := ParseTriggerSpec(item.SpecJSON)
	if err != nil {
		return model.PluginTriggerBinding{}, err
	}
	rev, err := s.store.GetPluginRevision(ctx, item.RevisionID)
	if err != nil || rev.PluginID != item.PluginID || rev.Status != model.PluginRevisionPublished {
		return model.PluginTriggerBinding{}, Coded(codeInvalidInput, "trigger must bind a published revision")
	}
	item.Enabled = false
	if actor.UserID != nil {
		item.CreatedByUserID = *actor.UserID
	}
	if err := s.store.CreatePluginTrigger(ctx, &item); err != nil {
		return model.PluginTriggerBinding{}, err
	}
	if item.Kind != model.PluginTriggerEvent {
		if slots, err := NextSlots(spec, s.now(), 1); err == nil && len(slots) > 0 {
			_ = s.store.UpsertPluginTriggerState(ctx, model.PluginTriggerState{BindingID: item.ID, Armed: true, NextDueAt: &slots[0]})
		}
	}
	return item, nil
}

func (s *Service) UpdateTrigger(ctx context.Context, actor application.Principal, item model.PluginTriggerBinding, expectedRevision int64) (model.PluginTriggerBinding, error) {
	if err := s.require(actor, "plugins.triggers"); err != nil {
		return model.PluginTriggerBinding{}, err
	}
	current, err := s.store.GetPluginTrigger(ctx, item.ID)
	if err != nil {
		return model.PluginTriggerBinding{}, err
	}
	if item.Name == "" {
		item.Name = current.Name
	}
	if item.Kind == "" {
		item.Kind = current.Kind
	}
	if len(item.SpecJSON) == 0 {
		item.SpecJSON = current.SpecJSON
	}
	if item.RevisionID == 0 {
		item.RevisionID = current.RevisionID
	}
	if len(item.ParamsJSON) == 0 {
		item.ParamsJSON = current.ParamsJSON
	}
	if len(item.EnvJSON) == 0 {
		item.EnvJSON = current.EnvJSON
	}
	item.PluginID = current.PluginID
	spec, err := ParseTriggerSpec(item.SpecJSON)
	if err != nil {
		return model.PluginTriggerBinding{}, err
	}
	if err := s.store.UpdatePluginTrigger(ctx, &item, expectedRevision); err != nil {
		return model.PluginTriggerBinding{}, err
	}
	state, _ := s.store.GetPluginTriggerState(ctx, item.ID)
	state.CurrentCycleKey = ""
	if item.Kind != model.PluginTriggerEvent {
		if slots, err := NextSlots(spec, s.now(), 1); err == nil && len(slots) > 0 {
			state.NextDueAt = &slots[0]
		}
	}
	_ = s.store.UpsertPluginTriggerState(ctx, state)
	return item, nil
}

func (s *Service) CreateGrant(ctx context.Context, actor application.Principal, grant model.PluginGrant) (model.PluginGrant, error) {
	if err := s.requireAdmin(actor, "plugins.authorize"); err != nil {
		return model.PluginGrant{}, err
	}
	rev, err := s.store.GetPluginRevision(ctx, grant.RevisionID)
	if err != nil || rev.Status != model.PluginRevisionPublished {
		return model.PluginGrant{}, Coded(codeInvalidInput, "grants bind a published revision")
	}
	if grant.SourceDigest != "" && grant.SourceDigest != rev.SourceDigest {
		return model.PluginGrant{}, Coded(codeInvalidInput, "grant source digest does not match the published revision")
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil || RevisionDigest(rev.Source, manifest) != rev.SourceDigest {
		return model.PluginGrant{}, ErrPermissionDenied
	}
	var requested []string
	if json.Unmarshal(grant.CapabilitiesJSON, &requested) != nil || len(requested) == 0 {
		return model.PluginGrant{}, Coded(codeInvalidInput, "grant capabilities are required")
	}
	for _, name := range requested {
		if !containsString(manifest.Capabilities, name) {
			return model.PluginGrant{}, Coded(codeInvalidInput, "grant exceeds declared capabilities")
		}
	}
	var constraints model.PluginGrantConstraints
	if len(grant.ConstraintsJSON) > 0 && string(grant.ConstraintsJSON) != "null" {
		if err := strictJSON(grant.ConstraintsJSON, &constraints); err != nil {
			return model.PluginGrant{}, Coded(codeInvalidInput, "unsupported grant constraints")
		}
	}
	for _, name := range constraints.Secrets {
		declared := false
		for _, ref := range manifest.Secrets {
			if ref.Name == name {
				declared = true
				break
			}
		}
		if !declared || !containsString(requested, SDKNetworkRequest) {
			return model.PluginGrant{}, Coded(codeInvalidInput, "secret grant exceeds declaration")
		}
	}
	if constraints.Network != nil && !containsString(requested, SDKNetworkRequest) {
		return model.PluginGrant{}, Coded(codeInvalidInput, "network constraints require network.request")
	}
	if containsString(requested, SDKNetworkRequest) {
		if _, err := effectiveNetworkPolicy(manifest, grant); err != nil {
			return model.PluginGrant{}, err
		}
	}
	if grant.ExpiresAt != nil && !grant.ExpiresAt.After(s.now()) {
		return model.PluginGrant{}, Coded(codeInvalidInput, "grant has expired")
	}
	grant.SourceDigest = rev.SourceDigest
	if grant.BindingID != nil {
		binding, err := s.store.GetPluginTrigger(ctx, *grant.BindingID)
		if err != nil {
			return model.PluginGrant{}, err
		}
		if binding.PluginID != rev.PluginID || binding.RevisionID != rev.ID {
			return model.PluginGrant{}, ErrPermissionDenied
		}
		grant.BindingDigest = BindingDigest(binding)
	}
	if actor.UserID != nil {
		grant.ApprovedByUserID = *actor.UserID
	}
	grant.PluginID = rev.PluginID
	if grant.GrantRevision <= 0 {
		grant.GrantRevision = 1
	}
	if err := validateGrantScope(grant); err != nil {
		return model.PluginGrant{}, err
	}
	if err := s.store.CreatePluginGrant(ctx, &grant); err != nil {
		return model.PluginGrant{}, err
	}
	return grant, nil
}

func (s *Service) RevokeGrant(ctx context.Context, actor application.Principal, id int64) error {
	if err := s.requireAdmin(actor, "plugins.authorize"); err != nil {
		return err
	}
	return s.store.RevokePluginGrant(ctx, id)
}

func (s *Service) EnqueueManualRun(ctx context.Context, actor application.Principal, pluginID, revisionID int64, params, env json.RawMessage, idempotencyKey, mode string) (model.PluginRun, error) {
	if err := s.require(actor, "plugins.execute"); err != nil {
		return model.PluginRun{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return model.PluginRun{}, Coded(codeInvalidInput, "idempotency_key is required")
	}
	if mode == "" {
		mode = model.PluginRunModeLive
	}
	if mode != model.PluginRunModeLive && mode != model.PluginRunModeSimulate {
		return model.PluginRun{}, Coded(codeInvalidInput, "invalid run mode")
	}
	rev, err := s.store.GetPluginRevision(ctx, revisionID)
	if err != nil || rev.PluginID != pluginID {
		return model.PluginRun{}, ErrNotFound
	}
	if mode == model.PluginRunModeLive && rev.Status != model.PluginRevisionPublished {
		return model.PluginRun{}, Coded(codeApprovalRequired, "live runs require a published revision")
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil {
		return model.PluginRun{}, err
	}
	if err := ValidateParams(manifest.Params, params); err != nil {
		return model.PluginRun{}, err
	}
	item, err := s.store.GetPlugin(ctx, pluginID)
	if err != nil || item.Status == model.PluginStatusArchived || mode == model.PluginRunModeLive && item.Status != model.PluginStatusEnabled {
		return model.PluginRun{}, ErrPermissionDenied
	}
	settings := s.Settings(ctx)
	if mode == model.PluginRunModeLive && !settings.Enabled {
		return model.PluginRun{}, Coded(codeRuntimeUnavailable, "plugin execution is disabled")
	}
	var grantID *int64
	if mode == model.PluginRunModeLive {
		grants, err := s.store.ListActivePluginGrants(ctx, pluginID, revisionID, nil)
		if err != nil || len(grants) == 0 {
			return model.PluginRun{}, ErrApprovalRequired
		}
		grantID = &grants[0].ID
	}
	caller := actor.ID
	run := model.PluginRun{
		UUID: "srun_" + randomID(), PluginID: pluginID, RevisionID: revisionID, GrantID: grantID,
		CallerPrincipal: caller, TriggerKind: "manual", IdempotencyKey: actor.ID + ":" + idempotencyKey,
		Status: model.PluginRunQueued, Mode: mode, RecoveryGen: settings.RecoveryGeneration,
		SnapshotJSON: MustJSON(map[string]any{
			"params":           json.RawMessage(orEmptyJSON(params)),
			"env":              json.RawMessage(orEmptyJSON(env)),
			"source_digest":    rev.SourceDigest,
			"manifest":         json.RawMessage(rev.ManifestJSON),
			"caller_id":        actor.ID,
			"caller_grant_id":  actor.GrantID,
			"caller_source_ip": actor.SourceIP.String(),
			"caller_role":      string(actor.Role),
			"caller_type":      actor.Type,
			"caller_filter":    json.RawMessage(orEmptyJSON(actor.ResourceFilter)),
		}),
	}
	if existing, getErr := s.store.GetPluginRunByIdempotency(ctx, run.IdempotencyKey); getErr == nil {
		if !sameRunRequest(existing, run) {
			return model.PluginRun{}, ErrIdempotencyConflict
		}
		return existing, nil
	}
	active, err := s.store.CountActivePluginRuns(ctx, pluginID)
	if err != nil {
		return model.PluginRun{}, err
	}
	if active >= DefaultPluginConcurrency && mode == model.PluginRunModeLive {
		return model.PluginRun{}, Coded(codeLimitExceeded, "plugin concurrency is 1")
	}
	queued, err := s.store.CountPluginRuns(ctx, model.PluginRunQueued, model.PluginRunRunning)
	if err != nil {
		return model.PluginRun{}, err
	}
	if queued >= settings.MaxConcurrency && mode == model.PluginRunModeLive {
		return model.PluginRun{}, Coded(codeLimitExceeded, "controller run concurrency is exhausted")
	}
	if err := s.store.CreatePluginRun(ctx, &run); err != nil {
		if store.IsPluginRunIdempotent(err) {
			existing, getErr := s.store.GetPluginRunByIdempotency(ctx, run.IdempotencyKey)
			if getErr == nil {
				if !sameRunRequest(existing, run) {
					return model.PluginRun{}, ErrIdempotencyConflict
				}
				return existing, nil
			}
		}
		return model.PluginRun{}, err
	}
	return run, nil
}

func (s *Service) EnqueueTriggerRun(ctx context.Context, binding model.PluginTriggerBinding, key, triggerKind string, snapshot map[string]any) (model.PluginRun, bool, error) {
	settings := s.Settings(ctx)
	if !settings.Enabled || settings.SchedulerPaused || !binding.Enabled {
		return model.PluginRun{}, false, nil
	}
	plugin, err := s.store.GetPlugin(ctx, binding.PluginID)
	if err != nil || plugin.Status != model.PluginStatusEnabled {
		return model.PluginRun{}, false, nil
	}
	rev, err := s.store.GetPluginRevision(ctx, binding.RevisionID)
	if err != nil || rev.PluginID != binding.PluginID || rev.Status != model.PluginRevisionPublished {
		return model.PluginRun{}, false, nil
	}
	grants, err := s.store.ListActivePluginGrants(ctx, binding.PluginID, binding.RevisionID, &binding.ID)
	if err != nil || len(grants) == 0 {
		return model.PluginRun{}, false, ErrApprovalRequired
	}
	active, _ := s.store.CountActivePluginRuns(ctx, binding.PluginID)
	if active >= DefaultPluginConcurrency {
		return model.PluginRun{}, false, Coded(codeLimitExceeded, "overlap")
	}
	grantID := grants[0].ID
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	snapshot["params"] = json.RawMessage(orEmptyJSON(binding.ParamsJSON))
	snapshot["env"] = json.RawMessage(orEmptyJSON(binding.EnvJSON))
	snapshot["source_digest"] = rev.SourceDigest
	snapshot["binding_revision"] = binding.BindingRevision
	run := model.PluginRun{
		UUID: "srun_" + randomID(), PluginID: binding.PluginID, RevisionID: binding.RevisionID,
		BindingID: &binding.ID, GrantID: &grantID, TriggerKind: triggerKind, IdempotencyKey: key,
		Status: model.PluginRunQueued, Mode: model.PluginRunModeLive, RecoveryGen: settings.RecoveryGeneration,
		SnapshotJSON: MustJSON(snapshot),
	}
	if err := s.store.CreatePluginRun(ctx, &run); err != nil {
		if store.IsPluginRunIdempotent(err) {
			existing, getErr := s.store.GetPluginRunByIdempotency(ctx, key)
			if getErr == nil {
				if !sameRunRequest(existing, run) {
					return model.PluginRun{}, false, ErrIdempotencyConflict
				}
				return existing, false, nil
			}
		}
		return model.PluginRun{}, false, err
	}
	return run, true, nil
}

func (s *Service) CancelRun(ctx context.Context, actor application.Principal, id int64) (model.PluginRun, error) {
	if err := s.require(actor, "plugins.cancel"); err != nil {
		return model.PluginRun{}, err
	}
	if err := s.store.CancelPluginRun(ctx, id); err != nil {
		return model.PluginRun{}, err
	}
	return s.store.GetPluginRun(ctx, id)
}

func (s *Service) GetRun(ctx context.Context, actor application.Principal, id int64) (model.PluginRun, error) {
	if err := s.require(actor, "plugins.logs"); err != nil && s.require(actor, "plugins.read") != nil {
		return model.PluginRun{}, ErrPermissionDenied
	}
	return s.store.GetPluginRun(ctx, id)
}

func (s *Service) EffectivePrincipal(ctx context.Context, run model.PluginRun) (application.Principal, error) {
	return s.effectivePrincipal(ctx, run, false)
}

// EffectiveChangesetPrincipal rechecks durable authorization after a successful
// runner has exited. It grants no worker lease and cannot authorize SDK calls.
func (s *Service) EffectiveChangesetPrincipal(ctx context.Context, run model.PluginRun) (application.Principal, error) {
	if run.Mode != model.PluginRunModeLive {
		return application.Principal{}, ErrPermissionDenied
	}
	return s.effectivePrincipal(ctx, run, true)
}

func (s *Service) effectivePrincipal(ctx context.Context, run model.PluginRun, deferred bool) (application.Principal, error) {
	current, err := s.store.GetPluginRunByUUID(ctx, run.UUID)
	if err != nil || current.ID != run.ID || current.LeaseGeneration != run.LeaseGeneration || current.LeaseOwner != run.LeaseOwner {
		return application.Principal{}, ErrPermissionDenied
	}
	run = current
	if deferred && run.Mode != model.PluginRunModeLive {
		return application.Principal{}, ErrPermissionDenied
	}
	settings := s.Settings(ctx)
	active := run.Status == model.PluginRunRunning && run.LeaseUntil != nil && run.LeaseUntil.After(s.now())
	if (!active && !(deferred && run.Status == model.PluginRunSucceeded)) || run.RecoveryGen != settings.RecoveryGeneration {
		return application.Principal{}, Coded(codeCancelled, "run is no longer active")
	}
	if run.Mode == model.PluginRunModeLive && !settings.Enabled {
		return application.Principal{}, ErrPermissionDenied
	}
	rev, err := s.store.GetPluginRevision(ctx, run.RevisionID)
	if err != nil {
		return application.Principal{}, err
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil {
		return application.Principal{}, err
	}
	plugin, err := s.store.GetPlugin(ctx, run.PluginID)
	if err != nil {
		return application.Principal{}, err
	}
	if rev.PluginID != plugin.ID || rev.SourceDigest != RevisionDigest(rev.Source, manifest) ||
		(run.Mode == model.PluginRunModeLive && (plugin.Status != model.PluginStatusEnabled || rev.Status != model.PluginRevisionPublished)) || plugin.Status == model.PluginStatusArchived {
		return application.Principal{}, ErrPermissionDenied
	}
	var snapshot struct {
		SourceDigest string `json:"source_digest"`
	}
	if json.Unmarshal(run.SnapshotJSON, &snapshot) != nil || snapshot.SourceDigest != rev.SourceDigest {
		return application.Principal{}, ErrPermissionDenied
	}
	filter := json.RawMessage(`{"servers":{"mode":"none"},"users":{"mode":"none"},"proxy_paths":{"mode":"none"},"subscription_plans":{"mode":"none"},"destructive_operations":false}`)
	scopes := append([]string{}, manifest.Capabilities...)
	if run.GrantID != nil {
		grant, err := s.store.GetPluginGrant(ctx, *run.GrantID)
		if err != nil || grant.RevokedAt != nil {
			return application.Principal{}, ErrPermissionDenied
		}
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(s.now()) {
			return application.Principal{}, ErrPermissionDenied
		}
		if grant.PluginID != run.PluginID || grant.RevisionID != run.RevisionID || !sameOptionalID(grant.BindingID, run.BindingID) {
			return application.Principal{}, ErrPermissionDenied
		}
		if run.BindingID != nil {
			binding, err := s.store.GetPluginTrigger(ctx, *run.BindingID)
			if err != nil || !binding.Enabled || binding.PluginID != run.PluginID || binding.RevisionID != run.RevisionID || grant.BindingDigest != BindingDigest(binding) {
				return application.Principal{}, ErrPermissionDenied
			}
		}
		if grant.SourceDigest != rev.SourceDigest {
			return application.Principal{}, Coded(codePermissionDenied, "grant does not match the current revision digest")
		}
		if len(grant.ResourceScopeJSON) > 0 {
			filter = grant.ResourceScopeJSON
		}
		var granted []string
		if json.Unmarshal(grant.CapabilitiesJSON, &granted) != nil {
			return application.Principal{}, ErrPermissionDenied
		}
		scopes = intersectStrings(scopes, granted)
	} else if run.Mode == model.PluginRunModeLive {
		return application.Principal{}, ErrApprovalRequired
	}
	if run.CallerPrincipal != "" {
		if s.callerResolver == nil {
			return application.Principal{}, ErrPermissionDenied
		}
		caller, err := s.callerResolver(ctx, run)
		if err != nil || caller.ID != run.CallerPrincipal || caller.Type == model.APIPrincipalPlugin || s.require(caller, "plugins.execute") != nil {
			return application.Principal{}, ErrPermissionDenied
		}
		if len(caller.ResourceFilter) > 0 && string(caller.ResourceFilter) != "{}" && string(caller.ResourceFilter) != "null" {
			filter = intersectResourceFilters(filter, caller.ResourceFilter)
		}
		allowed := scopes[:0]
		for _, scope := range scopes {
			if s.callerAllows(caller, scope) {
				allowed = append(allowed, scope)
			}
		}
		scopes = allowed
	} else if run.BindingID == nil {
		return application.Principal{}, ErrPermissionDenied
	}
	owner := plugin.OwnerUserID
	return application.PluginPrincipal(run.UUID, plugin.Name, &owner, scopes, filter), nil
}

func (s *Service) require(actor application.Principal, permission string) error {
	if actor.Type == model.APIPrincipalPlugin {
		return ErrPermissionDenied
	}
	adminOnly := permission == "plugins.authorize" || permission == "plugins.settings" || permission == "plugins.host_power"
	if adminOnly && actor.Role != model.RoleAdmin {
		return ErrPermissionDenied
	}
	if actor.Role != "" && actor.Role != model.RoleNone && (s.rbac == nil || !s.rbac.Allows(actor.Role, permission)) {
		return ErrPermissionDenied
	}
	if actor.HasScope(permissionToScope(permission)) || actor.AccessLevel != "" && s.rbac != nil && s.rbac.Allows(actor.Role, permission) {
		return nil
	}
	return ErrPermissionDenied
}

func (s *Service) requireAdmin(actor application.Principal, permission string) error {
	if actor.Role != model.RoleAdmin || !actor.Interactive || actor.Type != model.APIPrincipalOAuth || actor.ClientName != "oboard-web" {
		return ErrPermissionDenied
	}
	return s.require(actor, permission)
}

func permissionToScope(permission string) string {
	switch permission {
	case "plugins.read", "plugins.logs":
		return "plugins:read"
	case "plugins.draft":
		return "plugins:write"
	case "plugins.publish":
		return "plugins:publish"
	case "plugins.execute":
		return "plugins:execute"
	case "plugins.cancel":
		return "plugins:cancel"
	case "plugins.triggers":
		return "plugins:triggers"
	case "plugins.authorize":
		return "plugins:authorize"
	case "plugins.settings":
		return "plugins:settings"
	default:
		return permission
	}
}

func atoiDefault(raw string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return n
}

func parseRFC3339(raw string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, raw)
	}
	return t.UTC()
}

func randomID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func orEmptyJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func intersectStrings(left, right []string) []string {
	set := map[string]bool{}
	for _, item := range right {
		set[item] = true
	}
	out := []string{}
	for _, item := range left {
		if set[item] {
			out = append(out, item)
		}
	}
	return out
}

func (s *Service) ListRevisions(ctx context.Context, actor application.Principal, pluginID int64) ([]model.PluginRevision, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return nil, err
	}
	return s.store.ListPluginRevisions(ctx, pluginID)
}

func (s *Service) GetRevision(ctx context.Context, actor application.Principal, id int64) (model.PluginRevision, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return model.PluginRevision{}, err
	}
	return s.store.GetPluginRevision(ctx, id)
}

func (s *Service) ListTriggers(ctx context.Context, actor application.Principal, pluginID int64) ([]model.PluginTriggerBinding, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return nil, err
	}
	return s.store.ListPluginTriggers(ctx, pluginID)
}

func (s *Service) GetTrigger(ctx context.Context, actor application.Principal, id int64) (model.PluginTriggerBinding, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return model.PluginTriggerBinding{}, err
	}
	return s.store.GetPluginTrigger(ctx, id)
}

func (s *Service) ListGrants(ctx context.Context, actor application.Principal, pluginID int64) ([]model.PluginGrant, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return nil, err
	}
	return s.store.ListPluginGrants(ctx, pluginID)
}

func (s *Service) ListRuns(ctx context.Context, actor application.Principal, pluginID int64, limit int) ([]model.PluginRun, error) {
	if err := s.require(actor, "plugins.logs"); err != nil && s.require(actor, "plugins.read") != nil {
		return nil, ErrPermissionDenied
	}
	return s.store.ListPluginRuns(ctx, pluginID, limit)
}

func (s *Service) ListRunLogs(ctx context.Context, actor application.Principal, runID, afterSeq int64, limit int) ([]model.PluginRunLog, error) {
	if err := s.require(actor, "plugins.logs"); err != nil && s.require(actor, "plugins.read") != nil {
		return nil, ErrPermissionDenied
	}
	return s.store.ListPluginRunLogs(ctx, runID, afterSeq, limit)
}

func (s *Service) ListRunActions(ctx context.Context, actor application.Principal, runID int64) ([]model.PluginRunAction, error) {
	if err := s.require(actor, "plugins.logs"); err != nil && s.require(actor, "plugins.read") != nil {
		return nil, ErrPermissionDenied
	}
	return s.store.ListPluginRunActions(ctx, runID)
}

func (s *Service) EnqueueSimulate(ctx context.Context, actor application.Principal, pluginID, revisionID int64, params, env json.RawMessage, idempotencyKey string) (model.PluginRun, error) {
	if revisionID == 0 {
		draft, err := s.store.GetPluginDraft(ctx, pluginID)
		if err != nil {
			return model.PluginRun{}, err
		}
		revisionID = draft.ID
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		idempotencyKey = "simulate-" + randomID()
	}
	return s.EnqueueManualRun(ctx, actor, pluginID, revisionID, params, env, idempotencyKey, model.PluginRunModeSimulate)
}

func (s *Service) RuntimeStatus(ctx context.Context, actor application.Principal, isolation IsolationStatus, workerConnected bool, runtimeInstalled bool, installCommand string) (model.PluginRuntimeStatus, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return model.PluginRuntimeStatus{}, err
	}
	settings := s.Settings(ctx)
	queued, _ := s.store.CountPluginRuns(ctx, model.PluginRunQueued)
	active, _ := s.store.CountPluginRuns(ctx, model.PluginRunRunning)
	return model.PluginRuntimeStatus{
		Enabled:            settings.Enabled,
		HostActionsEnabled: settings.HostActionsEnabled,
		SchedulerPaused:    settings.SchedulerPaused,
		RecoveryGeneration: settings.RecoveryGeneration,
		RuntimeInstalled:   runtimeInstalled,
		InstallCommand:     installCommand,
		WorkerConnected:    workerConnected,
		IsolationAvailable: isolation.Available,
		IsolationMode:      isolation.Mode,
		IsolationReason:    isolation.Reason,
		ActiveRuns:         active,
		QueuedRuns:         queued,
		MaxConcurrency:     settings.MaxConcurrency,
	}, nil
}

func (s *Service) Export(ctx context.Context, actor application.Principal, pluginID int64) (map[string]any, error) {
	if err := s.require(actor, "plugins.read"); err != nil {
		return nil, err
	}
	plugin, err := s.store.GetPlugin(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	rev, err := s.store.GetPluginDraft(ctx, pluginID)
	if err != nil {
		revs, listErr := s.store.ListPluginRevisions(ctx, pluginID)
		if listErr != nil || len(revs) == 0 {
			return nil, ErrNotFound
		}
		rev = revs[0]
	}
	return map[string]any{
		"schema_version": model.PluginSchemaVersion,
		"plugin":         map[string]any{"name": plugin.Name, "description": plugin.Description},
		"source":         rev.Source,
		"manifest":       json.RawMessage(rev.ManifestJSON),
	}, nil
}

func (s *Service) Import(ctx context.Context, actor application.Principal, payload json.RawMessage) (model.Plugin, model.PluginRevision, error) {
	if err := s.require(actor, "plugins.draft"); err != nil {
		return model.Plugin{}, model.PluginRevision{}, err
	}
	var bundle struct {
		SchemaVersion int `json:"schema_version"`
		Plugin        struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"plugin"`
		Source   string          `json:"source"`
		Manifest json.RawMessage `json:"manifest"`
	}
	if err := strictJSON(payload, &bundle); err != nil {
		return model.Plugin{}, model.PluginRevision{}, err
	}
	if bundle.SchemaVersion != 0 && bundle.SchemaVersion != model.PluginSchemaVersion {
		return model.Plugin{}, model.PluginRevision{}, Coded(codeInvalidInput, "unsupported export schema_version")
	}
	plugin, err := s.CreatePlugin(ctx, actor, bundle.Plugin.Name, bundle.Plugin.Description)
	if err != nil {
		return model.Plugin{}, model.PluginRevision{}, err
	}
	rev, err := s.SaveDraft(ctx, actor, plugin.ID, bundle.Source, bundle.Manifest)
	return plugin, rev, err
}

func (s *Service) UpdateServerPolicy(ctx context.Context, actor application.Principal, policy model.ServerPluginPolicy) (model.ServerPluginPolicy, error) {
	if err := s.requireAdmin(actor, "plugins.host_power"); err != nil {
		return model.ServerPluginPolicy{}, err
	}
	if err := s.store.UpsertServerPluginPolicy(ctx, policy); err != nil {
		return model.ServerPluginPolicy{}, err
	}
	return s.store.GetServerPluginPolicy(ctx, policy.ServerID)
}

func IsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func validateGrantScope(grant model.PluginGrant) error {
	var capabilities []string
	_ = json.Unmarshal(grant.CapabilitiesJSON, &capabilities)
	needsExplicitServers := false
	for _, name := range capabilities {
		if powerCapabilities[name] || name == model.PluginSDKServicesRestart {
			needsExplicitServers = true
			break
		}
	}
	if !needsExplicitServers {
		return nil
	}
	var filter application.ResourceFilter
	if json.Unmarshal(grant.ResourceScopeJSON, &filter) != nil || filter.Servers == nil {
		return Coded(codeInvalidInput, "management grants require an explicit selected server set")
	}
	if strings.ToLower(filter.Servers.Mode) != "selected" || len(filter.Servers.IDs) == 0 {
		return Coded(codeInvalidInput, "management grants cannot use an open or empty server set")
	}
	return nil
}

func intersectResourceFilters(left, right json.RawMessage) json.RawMessage {
	var a, b application.ResourceFilter
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return MustJSON(application.ResourceFilter{})
	}
	selection := func(x, y *application.ResourceSelection) *application.ResourceSelection {
		out := &application.ResourceSelection{Mode: "none"}
		if x == nil || y == nil {
			return out
		}
		out.AllowCreate = x.AllowCreate && y.AllowCreate
		if x.Mode == "all" && y.Mode == "all" {
			out.Mode = "all"
			return out
		}
		out.Mode = "selected"
		if x.Mode == "all" && y.Mode == "selected" {
			out.IDs = y.IDs
			return out
		}
		if y.Mode == "all" && x.Mode == "selected" {
			out.IDs = x.IDs
			return out
		}
		if x.Mode != "selected" || y.Mode != "selected" {
			out.Mode = "none"
			return out
		}
		for _, id := range x.IDs {
			for _, other := range y.IDs {
				if id == other {
					out.IDs = append(out.IDs, id)
					break
				}
			}
		}
		return out
	}
	out := application.ResourceFilter{Servers: selection(a.Servers, b.Servers), Users: selection(a.Users, b.Users), ProxyPaths: selection(a.ProxyPaths, b.ProxyPaths), SubscriptionPlans: selection(a.SubscriptionPlans, b.SubscriptionPlans), DestructiveOperations: a.DestructiveOperations && b.DestructiveOperations}
	return MustJSON(out)
}

func selectedServerIDs(raw json.RawMessage) ([]int64, bool) {
	var filter application.ResourceFilter
	if json.Unmarshal(raw, &filter) != nil || filter.Servers == nil {
		var flat struct {
			ServerIDs []int64 `json:"server_ids"`
		}
		if json.Unmarshal(raw, &flat) == nil && len(flat.ServerIDs) > 0 {
			return flat.ServerIDs, false
		}
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(filter.Servers.Mode)) {
	case "all":
		return nil, true
	case "selected":
		return filter.Servers.IDs, false
	default:
		return nil, false
	}
}

func explicitServerFilter(ids []int64) json.RawMessage {
	return MustJSON(application.ResourceFilter{Servers: &application.ResourceSelection{Mode: "selected", IDs: ids}})
}
