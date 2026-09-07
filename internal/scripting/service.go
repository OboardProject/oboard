package scripting

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	store  *store.Store
	rbac   *authorization.RBAC
	now    func() time.Time
}

func NewService(db *store.Store, rbac *authorization.RBAC) *Service {
	return &Service{store: db, rbac: rbac, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Settings(ctx context.Context) Settings {
	values, _ := s.store.ListSettings(ctx)
	timeout := atoiDefault(values[model.ScriptSettingMaxTimeoutSeconds], MaxTimeoutSeconds)
	if timeout <= 0 || timeout > MaxTimeoutSeconds {
		timeout = MaxTimeoutSeconds
	}
	concurrency := atoiDefault(values[model.ScriptSettingMaxConcurrency], DefaultControllerConcurrency)
	if concurrency <= 0 {
		concurrency = DefaultControllerConcurrency
	}
	return Settings{
		Enabled:            values[model.ScriptSettingEnabled] == "true",
		HostActionsEnabled: values[model.ScriptSettingHostActionsEnabled] == "true",
		SchedulerPaused:    values[model.ScriptSettingSchedulerPaused] == "true",
		RecoveryGeneration: int64(atoiDefault(values[model.ScriptSettingRecoveryGeneration], 1)),
		MaxConcurrency:     concurrency,
		MaxTimeoutSeconds:  timeout,
	}
}

func (s *Service) UpdateSettings(ctx context.Context, actor application.Principal, next Settings) error {
	if err := s.requireAdmin(actor, "scripts.settings"); err != nil {
		return err
	}
	if next.MaxConcurrency < 1 || next.MaxConcurrency > 8 {
		return Coded(codeInvalidInput, "max_concurrency is out of range")
	}
	if next.MaxTimeoutSeconds < 5 || next.MaxTimeoutSeconds > MaxTimeoutSeconds {
		return Coded(codeInvalidInput, "max_timeout_seconds is out of range")
	}
	return s.store.SetSettings(ctx, map[string]string{
		model.ScriptSettingEnabled:            strconv.FormatBool(next.Enabled),
		model.ScriptSettingHostActionsEnabled: strconv.FormatBool(next.HostActionsEnabled),
		model.ScriptSettingSchedulerPaused:    strconv.FormatBool(next.SchedulerPaused),
		model.ScriptSettingMaxConcurrency:     strconv.Itoa(next.MaxConcurrency),
		model.ScriptSettingMaxTimeoutSeconds:  strconv.Itoa(next.MaxTimeoutSeconds),
	})
}

func (s *Service) CreateScript(ctx context.Context, actor application.Principal, name, description string) (model.Script, error) {
	if err := s.require(actor, "scripts.draft"); err != nil {
		return model.Script{}, err
	}
	if actor.UserID == nil {
		return model.Script{}, Coded(codePermissionDenied, "script ownership requires a user")
	}
	item := model.Script{Name: strings.TrimSpace(name), Description: strings.TrimSpace(description), OwnerUserID: *actor.UserID, Status: model.ScriptStatusDraft}
	if item.Name == "" {
		return model.Script{}, Coded(codeInvalidInput, "name is required")
	}
	if err := s.store.CreateScript(ctx, &item); err != nil {
		return model.Script{}, err
	}
	return item, nil
}

func (s *Service) UpdateScript(ctx context.Context, actor application.Principal, id int64, name, description, status, expectedUpdatedAt string) (model.Script, error) {
	if err := s.require(actor, "scripts.draft"); err != nil {
		return model.Script{}, err
	}
	item, err := s.store.GetScript(ctx, id)
	if err != nil {
		return model.Script{}, err
	}
	if expectedUpdatedAt != "" && !item.UpdatedAt.UTC().Equal(parseRFC3339(expectedUpdatedAt)) {
		return model.Script{}, ErrConflict
	}
	if name != "" {
		item.Name = name
	}
	if description != "" || description == "" && name != "" {
		item.Description = description
	}
	if status != "" {
		if item.Status == model.ScriptStatusArchived && status != model.ScriptStatusArchived {
			return model.Script{}, Coded(codeInvalidInput, "archived scripts cannot be re-enabled")
		}
		item.Status = status
	}
	if err := s.store.UpdateScript(ctx, &item, item.UpdatedAt); err != nil {
		return model.Script{}, err
	}
	return item, nil
}

func (s *Service) GetScript(ctx context.Context, actor application.Principal, id int64) (model.Script, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return model.Script{}, err
	}
	return s.store.GetScript(ctx, id)
}

func (s *Service) ListScripts(ctx context.Context, actor application.Principal, status string, limit int) ([]model.Script, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return nil, err
	}
	return s.store.ListScripts(ctx, status, limit)
}

func (s *Service) SaveDraft(ctx context.Context, actor application.Principal, scriptID int64, source string, manifestJSON json.RawMessage) (model.ScriptRevision, error) {
	if err := s.require(actor, "scripts.draft"); err != nil {
		return model.ScriptRevision{}, err
	}
	if err := ValidateSource(source); err != nil {
		return model.ScriptRevision{}, err
	}
	manifest, err := ParseManifest(manifestJSON)
	if err != nil {
		return model.ScriptRevision{}, err
	}
	author := int64(0)
	if actor.UserID != nil {
		author = *actor.UserID
	}
	rev := model.ScriptRevision{
		ScriptID: scriptID, Status: model.ScriptRevisionDraft, SchemaVersion: manifest.SchemaVersion,
		Runtime: manifest.Runtime, SDKVersion: manifest.SDKVersion, Source: source,
		SourceDigest: SourceDigest(source), ManifestJSON: MustJSON(manifest), AuthorUserID: author,
	}
	if err := s.store.SaveScriptDraft(ctx, &rev); err != nil {
		return model.ScriptRevision{}, err
	}
	return rev, nil
}

func (s *Service) Publish(ctx context.Context, actor application.Principal, scriptID, revisionID int64) (model.ScriptRevision, error) {
	if err := s.require(actor, "scripts.publish"); err != nil {
		return model.ScriptRevision{}, err
	}
	rev, err := s.store.GetScriptRevision(ctx, revisionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ScriptRevision{}, ErrNotFound
		}
		return model.ScriptRevision{}, err
	}
	if rev.ScriptID != scriptID {
		return model.ScriptRevision{}, ErrNotFound
	}
	if _, err := ParseManifest(rev.ManifestJSON); err != nil {
		return model.ScriptRevision{}, err
	}
	if err := ValidateSource(rev.Source); err != nil {
		return model.ScriptRevision{}, err
	}
	publisher := int64(0)
	if actor.UserID != nil {
		publisher = *actor.UserID
	}
	return s.store.PublishScriptRevision(ctx, scriptID, revisionID, publisher)
}

func (s *Service) ValidateRevision(ctx context.Context, actor application.Principal, scriptID int64, source string, manifestJSON, params json.RawMessage) (map[string]any, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return nil, err
	}
	if source == "" && scriptID > 0 {
		if draft, err := s.store.GetScriptDraft(ctx, scriptID); err == nil {
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

func (s *Service) CreateTrigger(ctx context.Context, actor application.Principal, item model.ScriptTriggerBinding) (model.ScriptTriggerBinding, error) {
	if err := s.require(actor, "scripts.triggers"); err != nil {
		return model.ScriptTriggerBinding{}, err
	}
	spec, err := ParseTriggerSpec(item.SpecJSON)
	if err != nil {
		return model.ScriptTriggerBinding{}, err
	}
	rev, err := s.store.GetScriptRevision(ctx, item.RevisionID)
	if err != nil || rev.ScriptID != item.ScriptID || rev.Status != model.ScriptRevisionPublished {
		return model.ScriptTriggerBinding{}, Coded(codeInvalidInput, "trigger must bind a published revision")
	}
	item.Enabled = false
	if actor.UserID != nil {
		item.CreatedByUserID = *actor.UserID
	}
	if err := s.store.CreateScriptTrigger(ctx, &item); err != nil {
		return model.ScriptTriggerBinding{}, err
	}
	if item.Kind != model.ScriptTriggerEvent {
		if slots, err := NextSlots(spec, s.now(), 1); err == nil && len(slots) > 0 {
			_ = s.store.UpsertScriptTriggerState(ctx, model.ScriptTriggerState{BindingID: item.ID, Armed: true, NextDueAt: &slots[0]})
		}
	}
	return item, nil
}

func (s *Service) UpdateTrigger(ctx context.Context, actor application.Principal, item model.ScriptTriggerBinding, expectedRevision int64) (model.ScriptTriggerBinding, error) {
	if err := s.require(actor, "scripts.triggers"); err != nil {
		return model.ScriptTriggerBinding{}, err
	}
	current, err := s.store.GetScriptTrigger(ctx, item.ID)
	if err != nil {
		return model.ScriptTriggerBinding{}, err
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
	item.ScriptID = current.ScriptID
	spec, err := ParseTriggerSpec(item.SpecJSON)
	if err != nil {
		return model.ScriptTriggerBinding{}, err
	}
	if err := s.store.UpdateScriptTrigger(ctx, &item, expectedRevision); err != nil {
		return model.ScriptTriggerBinding{}, err
	}
	state, _ := s.store.GetScriptTriggerState(ctx, item.ID)
	state.CurrentCycleKey = ""
	if item.Kind != model.ScriptTriggerEvent {
		if slots, err := NextSlots(spec, s.now(), 1); err == nil && len(slots) > 0 {
			state.NextDueAt = &slots[0]
		}
	}
	_ = s.store.UpsertScriptTriggerState(ctx, state)
	return item, nil
}

func (s *Service) CreateGrant(ctx context.Context, actor application.Principal, grant model.ScriptGrant) (model.ScriptGrant, error) {
	if err := s.requireAdmin(actor, "scripts.authorize"); err != nil {
		return model.ScriptGrant{}, err
	}
	rev, err := s.store.GetScriptRevision(ctx, grant.RevisionID)
	if err != nil || rev.Status != model.ScriptRevisionPublished {
		return model.ScriptGrant{}, Coded(codeInvalidInput, "grants bind a published revision")
	}
	if grant.SourceDigest != "" && grant.SourceDigest != rev.SourceDigest {
		return model.ScriptGrant{}, Coded(codeInvalidInput, "grant source digest does not match the published revision")
	}
	grant.SourceDigest = rev.SourceDigest
	if grant.BindingID != nil {
		binding, err := s.store.GetScriptTrigger(ctx, *grant.BindingID)
		if err != nil {
			return model.ScriptGrant{}, err
		}
		grant.BindingDigest = fmt.Sprintf("%d:%d", binding.ID, binding.BindingRevision)
	}
	if actor.UserID != nil {
		grant.ApprovedByUserID = *actor.UserID
	}
	grant.ScriptID = rev.ScriptID
	if grant.GrantRevision <= 0 {
		grant.GrantRevision = 1
	}
	if err := validateGrantScope(grant); err != nil {
		return model.ScriptGrant{}, err
	}
	if err := s.store.CreateScriptGrant(ctx, &grant); err != nil {
		return model.ScriptGrant{}, err
	}
	return grant, nil
}

func (s *Service) RevokeGrant(ctx context.Context, actor application.Principal, id int64) error {
	if err := s.requireAdmin(actor, "scripts.authorize"); err != nil {
		return err
	}
	return s.store.RevokeScriptGrant(ctx, id)
}

func (s *Service) EnqueueManualRun(ctx context.Context, actor application.Principal, scriptID, revisionID int64, params, env json.RawMessage, idempotencyKey, mode string) (model.ScriptRun, error) {
	if err := s.require(actor, "scripts.execute"); err != nil {
		return model.ScriptRun{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return model.ScriptRun{}, Coded(codeInvalidInput, "idempotency_key is required")
	}
	if mode == "" {
		mode = model.ScriptRunModeLive
	}
	rev, err := s.store.GetScriptRevision(ctx, revisionID)
	if err != nil || rev.ScriptID != scriptID {
		return model.ScriptRun{}, ErrNotFound
	}
	if mode == model.ScriptRunModeLive && rev.Status != model.ScriptRevisionPublished {
		return model.ScriptRun{}, Coded(codeApprovalRequired, "live runs require a published revision")
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil {
		return model.ScriptRun{}, err
	}
	if err := ValidateParams(manifest.Params, params); err != nil {
		return model.ScriptRun{}, err
	}
	settings := s.Settings(ctx)
	if mode == model.ScriptRunModeLive && !settings.Enabled {
		return model.ScriptRun{}, Coded(codeRuntimeUnavailable, "script execution is disabled")
	}
	var grantID *int64
	if mode == model.ScriptRunModeLive {
		grants, err := s.store.ListActiveScriptGrants(ctx, scriptID, revisionID, nil)
		if err != nil || len(grants) == 0 {
			return model.ScriptRun{}, ErrApprovalRequired
		}
		grantID = &grants[0].ID
	}
	active, _ := s.store.CountActiveScriptRuns(ctx, scriptID)
	if active >= DefaultScriptConcurrency && mode == model.ScriptRunModeLive {
		return model.ScriptRun{}, Coded(codeLimitExceeded, "script concurrency is 1")
	}
	queued, _ := s.store.CountScriptRuns(ctx, model.ScriptRunQueued, model.ScriptRunRunning)
	if queued >= settings.MaxConcurrency && mode == model.ScriptRunModeLive {
		return model.ScriptRun{}, Coded(codeLimitExceeded, "controller run concurrency is exhausted")
	}
	caller := actor.ID
	run := model.ScriptRun{
		UUID: "srun_" + randomID(), ScriptID: scriptID, RevisionID: revisionID, GrantID: grantID,
		CallerPrincipal: caller, TriggerKind: "manual", IdempotencyKey: actor.ID + ":" + idempotencyKey,
		Status: model.ScriptRunQueued, Mode: mode, RecoveryGen: settings.RecoveryGeneration,
		SnapshotJSON: MustJSON(map[string]any{
			"params": json.RawMessage(orEmptyJSON(params)),
			"env":    json.RawMessage(orEmptyJSON(env)),
			"source_digest": rev.SourceDigest,
			"manifest":      json.RawMessage(rev.ManifestJSON),
			"caller_id":     actor.ID,
			"caller_role":   string(actor.Role),
			"caller_type":   actor.Type,
			"caller_filter": json.RawMessage(orEmptyJSON(actor.ResourceFilter)),
		}),
	}
	if err := s.store.CreateScriptRun(ctx, &run); err != nil {
		if store.IsScriptRunIdempotent(err) {
			existing, getErr := s.store.GetScriptRunByIdempotency(ctx, run.IdempotencyKey)
			if getErr == nil {
				if existing.RevisionID != revisionID || existing.Mode != mode {
					return model.ScriptRun{}, ErrIdempotencyConflict
				}
				return existing, nil
			}
		}
		return model.ScriptRun{}, err
	}
	return run, nil
}

func (s *Service) EnqueueTriggerRun(ctx context.Context, binding model.ScriptTriggerBinding, key, triggerKind string, snapshot map[string]any) (model.ScriptRun, bool, error) {
	settings := s.Settings(ctx)
	if !settings.Enabled || settings.SchedulerPaused {
		return model.ScriptRun{}, false, nil
	}
	script, err := s.store.GetScript(ctx, binding.ScriptID)
	if err != nil || script.Status != model.ScriptStatusEnabled {
		return model.ScriptRun{}, false, nil
	}
	rev, err := s.store.GetScriptRevision(ctx, binding.RevisionID)
	if err != nil || rev.Status != model.ScriptRevisionPublished {
		return model.ScriptRun{}, false, nil
	}
	grants, err := s.store.ListActiveScriptGrants(ctx, binding.ScriptID, binding.RevisionID, &binding.ID)
	if err != nil || len(grants) == 0 {
		return model.ScriptRun{}, false, ErrApprovalRequired
	}
	active, _ := s.store.CountActiveScriptRuns(ctx, binding.ScriptID)
	if active >= DefaultScriptConcurrency {
		return model.ScriptRun{}, false, Coded(codeLimitExceeded, "overlap")
	}
	grantID := grants[0].ID
	if snapshot == nil {
		snapshot = map[string]any{}
	}
	snapshot["params"] = json.RawMessage(orEmptyJSON(binding.ParamsJSON))
	snapshot["env"] = json.RawMessage(orEmptyJSON(binding.EnvJSON))
	snapshot["source_digest"] = rev.SourceDigest
	snapshot["binding_revision"] = binding.BindingRevision
	run := model.ScriptRun{
		UUID: "srun_" + randomID(), ScriptID: binding.ScriptID, RevisionID: binding.RevisionID,
		BindingID: &binding.ID, GrantID: &grantID, TriggerKind: triggerKind, IdempotencyKey: key,
		Status: model.ScriptRunQueued, Mode: model.ScriptRunModeLive, RecoveryGen: settings.RecoveryGeneration,
		SnapshotJSON: MustJSON(snapshot),
	}
	if err := s.store.CreateScriptRun(ctx, &run); err != nil {
		if store.IsScriptRunIdempotent(err) {
			existing, getErr := s.store.GetScriptRunByIdempotency(ctx, key)
			if getErr == nil {
				return existing, false, nil
			}
		}
		return model.ScriptRun{}, false, err
	}
	return run, true, nil
}

func (s *Service) CancelRun(ctx context.Context, actor application.Principal, id int64) (model.ScriptRun, error) {
	if err := s.require(actor, "scripts.cancel"); err != nil {
		return model.ScriptRun{}, err
	}
	if err := s.store.CancelScriptRun(ctx, id); err != nil {
		return model.ScriptRun{}, err
	}
	return s.store.GetScriptRun(ctx, id)
}

func (s *Service) GetRun(ctx context.Context, actor application.Principal, id int64) (model.ScriptRun, error) {
	if err := s.require(actor, "scripts.logs"); err != nil && s.require(actor, "scripts.read") != nil {
		return model.ScriptRun{}, ErrPermissionDenied
	}
	return s.store.GetScriptRun(ctx, id)
}

func (s *Service) EffectivePrincipal(ctx context.Context, run model.ScriptRun) (application.Principal, error) {
	rev, err := s.store.GetScriptRevision(ctx, run.RevisionID)
	if err != nil {
		return application.Principal{}, err
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil {
		return application.Principal{}, err
	}
	script, err := s.store.GetScript(ctx, run.ScriptID)
	if err != nil {
		return application.Principal{}, err
	}
	filter := json.RawMessage(`{"servers":{"mode":"none"},"users":{"mode":"none"},"proxy_paths":{"mode":"none"},"subscription_plans":{"mode":"none"},"destructive_operations":false}`)
	scopes := append([]string{}, manifest.Capabilities...)
	if run.GrantID != nil {
		grant, err := s.store.GetScriptGrant(ctx, *run.GrantID)
		if err != nil || grant.RevokedAt != nil {
			return application.Principal{}, ErrPermissionDenied
		}
		if grant.ExpiresAt != nil && !grant.ExpiresAt.After(s.now()) {
			return application.Principal{}, ErrPermissionDenied
		}
		if grant.SourceDigest != rev.SourceDigest {
			return application.Principal{}, Coded(codePermissionDenied, "grant does not match the current revision digest")
		}
		if len(grant.ResourceScopeJSON) > 0 {
			filter = grant.ResourceScopeJSON
		}
		var granted []string
		_ = json.Unmarshal(grant.CapabilitiesJSON, &granted)
		scopes = intersectStrings(scopes, granted)
	} else if run.Mode == model.ScriptRunModeLive {
		return application.Principal{}, ErrApprovalRequired
	}
	if run.CallerPrincipal != "" {
		var snap struct {
			CallerFilter json.RawMessage `json:"caller_filter"`
			CallerType   string          `json:"caller_type"`
		}
		_ = json.Unmarshal(run.SnapshotJSON, &snap)
		if snap.CallerType != string(model.APIPrincipalScript) && len(snap.CallerFilter) > 0 && string(snap.CallerFilter) != "{}" && string(snap.CallerFilter) != "null" {
			filter = intersectResourceFilters(filter, snap.CallerFilter)
		}
	}
	owner := script.OwnerUserID
	return application.ScriptPrincipal(run.UUID, script.Name, &owner, scopes, filter), nil
}

func (s *Service) require(actor application.Principal, permission string) error {
	if actor.Type == model.APIPrincipalScript {
		return ErrPermissionDenied
	}
	adminOnly := permission == "scripts.authorize" || permission == "scripts.settings" || permission == "scripts.host_power"
	if adminOnly && actor.Role != model.RoleAdmin {
		return ErrPermissionDenied
	}
	if s.rbac != nil && s.rbac.Allows(actor.Role, permission) {
		return nil
	}
	if actor.HasScope(permissionToScope(permission)) || actor.HasScope("*") {
		return nil
	}
	return ErrPermissionDenied
}

func (s *Service) requireAdmin(actor application.Principal, permission string) error {
	if actor.Role != model.RoleAdmin {
		return ErrPermissionDenied
	}
	return s.require(actor, permission)
}

func permissionToScope(permission string) string {
	switch permission {
	case "scripts.read", "scripts.logs":
		return "scripts:read"
	case "scripts.draft":
		return "scripts:write"
	case "scripts.publish":
		return "scripts:publish"
	case "scripts.execute":
		return "scripts:execute"
	case "scripts.cancel":
		return "scripts:cancel"
	case "scripts.triggers":
		return "scripts:triggers"
	case "scripts.authorize":
		return "scripts:authorize"
	case "scripts.settings":
		return "scripts:settings"
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

func (s *Service) ListRevisions(ctx context.Context, actor application.Principal, scriptID int64) ([]model.ScriptRevision, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return nil, err
	}
	return s.store.ListScriptRevisions(ctx, scriptID)
}

func (s *Service) GetRevision(ctx context.Context, actor application.Principal, id int64) (model.ScriptRevision, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return model.ScriptRevision{}, err
	}
	return s.store.GetScriptRevision(ctx, id)
}

func (s *Service) ListTriggers(ctx context.Context, actor application.Principal, scriptID int64) ([]model.ScriptTriggerBinding, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return nil, err
	}
	return s.store.ListScriptTriggers(ctx, scriptID)
}

func (s *Service) GetTrigger(ctx context.Context, actor application.Principal, id int64) (model.ScriptTriggerBinding, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return model.ScriptTriggerBinding{}, err
	}
	return s.store.GetScriptTrigger(ctx, id)
}

func (s *Service) ListGrants(ctx context.Context, actor application.Principal, scriptID int64) ([]model.ScriptGrant, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return nil, err
	}
	return s.store.ListScriptGrants(ctx, scriptID)
}

func (s *Service) ListRuns(ctx context.Context, actor application.Principal, scriptID int64, limit int) ([]model.ScriptRun, error) {
	if err := s.require(actor, "scripts.logs"); err != nil && s.require(actor, "scripts.read") != nil {
		return nil, ErrPermissionDenied
	}
	return s.store.ListScriptRuns(ctx, scriptID, limit)
}

func (s *Service) ListRunLogs(ctx context.Context, actor application.Principal, runID, afterSeq int64, limit int) ([]model.ScriptRunLog, error) {
	if err := s.require(actor, "scripts.logs"); err != nil && s.require(actor, "scripts.read") != nil {
		return nil, ErrPermissionDenied
	}
	return s.store.ListScriptRunLogs(ctx, runID, afterSeq, limit)
}

func (s *Service) ListRunActions(ctx context.Context, actor application.Principal, runID int64) ([]model.ScriptRunAction, error) {
	if err := s.require(actor, "scripts.logs"); err != nil && s.require(actor, "scripts.read") != nil {
		return nil, ErrPermissionDenied
	}
	return s.store.ListScriptRunActions(ctx, runID)
}

func (s *Service) EnqueueSimulate(ctx context.Context, actor application.Principal, scriptID, revisionID int64, params, env json.RawMessage, idempotencyKey string) (model.ScriptRun, error) {
	if revisionID == 0 {
		draft, err := s.store.GetScriptDraft(ctx, scriptID)
		if err != nil {
			return model.ScriptRun{}, err
		}
		revisionID = draft.ID
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		idempotencyKey = "simulate-" + randomID()
	}
	return s.EnqueueManualRun(ctx, actor, scriptID, revisionID, params, env, idempotencyKey, model.ScriptRunModeSimulate)
}

func (s *Service) RuntimeStatus(ctx context.Context, actor application.Principal, isolation IsolationStatus, workerConnected bool) (model.ScriptRuntimeStatus, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return model.ScriptRuntimeStatus{}, err
	}
	settings := s.Settings(ctx)
	queued, _ := s.store.CountScriptRuns(ctx, model.ScriptRunQueued)
	active, _ := s.store.CountScriptRuns(ctx, model.ScriptRunRunning)
	return model.ScriptRuntimeStatus{
		Enabled:            settings.Enabled,
		HostActionsEnabled: settings.HostActionsEnabled,
		SchedulerPaused:    settings.SchedulerPaused,
		RecoveryGeneration: settings.RecoveryGeneration,
		WorkerConnected:    workerConnected,
		IsolationAvailable: isolation.Available,
		IsolationMode:      isolation.Mode,
		IsolationReason:    isolation.Reason,
		ActiveRuns:         active,
		QueuedRuns:         queued,
		MaxConcurrency:     settings.MaxConcurrency,
	}, nil
}

func (s *Service) Export(ctx context.Context, actor application.Principal, scriptID int64) (map[string]any, error) {
	if err := s.require(actor, "scripts.read"); err != nil {
		return nil, err
	}
	script, err := s.store.GetScript(ctx, scriptID)
	if err != nil {
		return nil, err
	}
	rev, err := s.store.GetScriptDraft(ctx, scriptID)
	if err != nil {
		revs, listErr := s.store.ListScriptRevisions(ctx, scriptID)
		if listErr != nil || len(revs) == 0 {
			return nil, ErrNotFound
		}
		rev = revs[0]
	}
	return map[string]any{
		"schema_version": model.ScriptSchemaVersion,
		"script":         map[string]any{"name": script.Name, "description": script.Description},
		"source":         rev.Source,
		"manifest":       json.RawMessage(rev.ManifestJSON),
	}, nil
}

func (s *Service) Import(ctx context.Context, actor application.Principal, payload json.RawMessage) (model.Script, model.ScriptRevision, error) {
	if err := s.require(actor, "scripts.draft"); err != nil {
		return model.Script{}, model.ScriptRevision{}, err
	}
	var bundle struct {
		SchemaVersion int             `json:"schema_version"`
		Script        struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"script"`
		Source   string          `json:"source"`
		Manifest json.RawMessage `json:"manifest"`
	}
	if err := strictJSON(payload, &bundle); err != nil {
		return model.Script{}, model.ScriptRevision{}, err
	}
	if bundle.SchemaVersion != 0 && bundle.SchemaVersion != model.ScriptSchemaVersion {
		return model.Script{}, model.ScriptRevision{}, Coded(codeInvalidInput, "unsupported export schema_version")
	}
	script, err := s.CreateScript(ctx, actor, bundle.Script.Name, bundle.Script.Description)
	if err != nil {
		return model.Script{}, model.ScriptRevision{}, err
	}
	rev, err := s.SaveDraft(ctx, actor, script.ID, bundle.Source, bundle.Manifest)
	return script, rev, err
}

func (s *Service) UpdateServerPolicy(ctx context.Context, actor application.Principal, policy model.ServerScriptPolicy) (model.ServerScriptPolicy, error) {
	if err := s.requireAdmin(actor, "scripts.host_power"); err != nil {
		return model.ServerScriptPolicy{}, err
	}
	if err := s.store.UpsertServerScriptPolicy(ctx, policy); err != nil {
		return model.ServerScriptPolicy{}, err
	}
	return s.store.GetServerScriptPolicy(ctx, policy.ServerID)
}

func IsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func validateGrantScope(grant model.ScriptGrant) error {
	var capabilities []string
	_ = json.Unmarshal(grant.CapabilitiesJSON, &capabilities)
	needsExplicitServers := false
	for _, name := range capabilities {
		if powerCapabilities[name] || name == model.ScriptSDKServicesRestart {
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
	leftIDs, leftAll := selectedServerIDs(left)
	rightIDs, rightAll := selectedServerIDs(right)
	if leftAll && rightAll {
		return left
	}
	if leftAll {
		return explicitServerFilter(rightIDs)
	}
	if rightAll {
		return explicitServerFilter(leftIDs)
	}
	set := map[int64]bool{}
	for _, id := range rightIDs {
		set[id] = true
	}
	out := make([]int64, 0)
	for _, id := range leftIDs {
		if set[id] {
			out = append(out, id)
		}
	}
	return explicitServerFilter(out)
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
