package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/scripting"
)

func (s *Server) registerScriptRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/scripts", s.apiAuth(s.apiV1Scripts, model.RoleOperator))
	mux.HandleFunc("/api/v1/scripts/import", s.apiAuth(s.apiV1ScriptImport, model.RoleOperator))
	mux.HandleFunc("/api/v1/scripts/", s.apiAuth(s.apiV1ScriptItem, model.RoleOperator))
	mux.HandleFunc("/api/v1/script-triggers", s.apiAuth(s.apiV1ScriptTriggers, model.RoleOperator))
	mux.HandleFunc("/api/v1/script-triggers/", s.apiAuth(s.apiV1ScriptTriggerItem, model.RoleOperator))
	mux.HandleFunc("/api/v1/script-grants", s.apiAuth(s.apiV1ScriptGrants, model.RoleAdmin))
	mux.HandleFunc("/api/v1/script-grants/", s.apiAuth(s.apiV1ScriptGrantItem, model.RoleAdmin))
	mux.HandleFunc("/api/v1/script-runs/", s.apiAuth(s.apiV1ScriptRunItem, model.RoleOperator))
	mux.HandleFunc("/api/v1/script-runtime/status", s.apiAuth(s.apiV1ScriptRuntime, model.RoleOperator))
	mux.HandleFunc("/api/v1/server-script-policies", s.apiAuth(s.apiV1ServerScriptPolicies, model.RoleAdmin))
}

func (s *Server) apiV1Scripts(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	switch r.Method {
	case http.MethodGet:
		items, err := s.scripts.ListScripts(r.Context(), principal, strings.TrimSpace(r.URL.Query().Get("status")), intQuery(r, "limit", 100))
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"scripts": items})
	case http.MethodPost:
		var input struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if !decodeV2(w, r, &input) {
			return
		}
		item, err := s.scripts.CreateScript(r.Context(), principal, input.Name, input.Description)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusCreated, map[string]any{"script": item})
	default:
		method(w)
	}
}

func (s *Server) apiV1ScriptItem(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/scripts/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		scriptErr(w, r, scripting.Coded(model.ScriptErrorInvalidInput, "invalid script id"))
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			item, err := s.scripts.GetScript(r.Context(), principal, id)
			if err != nil {
				scriptErr(w, r, err)
				return
			}
			draft, _ := s.store.GetScriptDraft(r.Context(), id)
			revs, _ := s.store.ListScriptRevisions(r.Context(), id)
			var published *model.ScriptRevision
			for i := range revs {
				if revs[i].Status == model.ScriptRevisionPublished {
					published = &revs[i]
					break
				}
			}
			scriptOK(w, r, http.StatusOK, map[string]any{"script": item, "draft": draft, "published": published, "revisions": revs})
		case http.MethodPatch:
			var input struct {
				Name              string `json:"name"`
				Description       string `json:"description"`
				Status            string `json:"status"`
				ExpectedUpdatedAt string `json:"expected_updated_at"`
			}
			if !decodeV2(w, r, &input) {
				return
			}
			item, err := s.scripts.UpdateScript(r.Context(), principal, id, input.Name, input.Description, input.Status, input.ExpectedUpdatedAt)
			if err != nil {
				scriptErr(w, r, err)
				return
			}
			scriptOK(w, r, http.StatusOK, map[string]any{"script": item})
		default:
			method(w)
		}
		return
	}
	switch parts[1] {
	case "revisions":
		if r.Method == http.MethodGet {
			items, err := s.scripts.ListRevisions(r.Context(), principal, id)
			if err != nil {
				scriptErr(w, r, err)
				return
			}
			scriptOK(w, r, http.StatusOK, map[string]any{"revisions": items})
			return
		}
		if r.Method == http.MethodPost {
			var input struct {
				Source   string          `json:"source"`
				Manifest json.RawMessage `json:"manifest"`
			}
			if !decodeV2(w, r, &input) {
				return
			}
			rev, err := s.scripts.SaveDraft(r.Context(), principal, id, input.Source, input.Manifest)
			if err != nil {
				scriptErr(w, r, err)
				return
			}
			scriptOK(w, r, http.StatusOK, map[string]any{"revision": rev})
			return
		}
	case "validate":
		var input struct {
			Source   string          `json:"source"`
			Manifest json.RawMessage `json:"manifest"`
			Params   json.RawMessage `json:"params"`
		}
		if r.Method != http.MethodPost || !decodeV2(w, r, &input) {
			if r.Method != http.MethodPost {
				method(w)
			}
			return
		}
		result, err := s.scripts.ValidateRevision(r.Context(), principal, id, input.Source, input.Manifest, input.Params)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, result)
		return
	case "simulate":
		var input struct {
			RevisionID     int64           `json:"revision_id"`
			Params         json.RawMessage `json:"params"`
			Env            json.RawMessage `json:"env"`
			IdempotencyKey string          `json:"idempotency_key"`
		}
		if r.Method != http.MethodPost || !decodeV2(w, r, &input) {
			if r.Method != http.MethodPost {
				method(w)
			}
			return
		}
		run, err := s.scripts.EnqueueSimulate(r.Context(), principal, id, input.RevisionID, input.Params, input.Env, input.IdempotencyKey)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusAccepted, map[string]any{"run": run})
		return
	case "runs":
		if r.Method == http.MethodGet {
			items, err := s.scripts.ListRuns(r.Context(), principal, id, intQuery(r, "limit", 50))
			if err != nil {
				scriptErr(w, r, err)
				return
			}
			scriptOK(w, r, http.StatusOK, map[string]any{"runs": items})
			return
		}
		if r.Method == http.MethodPost {
			var input struct {
				RevisionID     int64           `json:"revision_id"`
				Params         json.RawMessage `json:"params"`
				Env            json.RawMessage `json:"env"`
				IdempotencyKey string          `json:"idempotency_key"`
			}
			if !decodeV2(w, r, &input) {
				return
			}
			run, err := s.scripts.EnqueueManualRun(r.Context(), principal, id, input.RevisionID, input.Params, input.Env, input.IdempotencyKey, model.ScriptRunModeLive)
			if err != nil {
				scriptErr(w, r, err)
				return
			}
			scriptOK(w, r, http.StatusAccepted, map[string]any{"run": run})
			return
		}
	case "export":
		bundle, err := s.scripts.Export(r.Context(), principal, id)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, bundle)
		return
	case "publish":
		var input struct {
			RevisionID int64 `json:"revision_id"`
		}
		if r.Method != http.MethodPost || !decodeV2(w, r, &input) {
			if r.Method != http.MethodPost {
				method(w)
			}
			return
		}
		rev, err := s.scripts.Publish(r.Context(), principal, id, input.RevisionID)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"revision": rev})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) apiV1ScriptTriggers(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		scriptID, _ := strconv.ParseInt(r.URL.Query().Get("script_id"), 10, 64)
		items, err := s.scripts.ListTriggers(r.Context(), principal, scriptID)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"triggers": items})
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input model.ScriptTriggerBinding
	if !decodeV2(w, r, &input) {
		return
	}
	item, err := s.scripts.CreateTrigger(r.Context(), principal, input)
	if err != nil {
		scriptErr(w, r, err)
		return
	}
	scriptOK(w, r, http.StatusCreated, map[string]any{"trigger": item})
}

func (s *Server) apiV1ScriptTriggerItem(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/script-triggers/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		scriptErr(w, r, scripting.Coded(model.ScriptErrorInvalidInput, err.Error()))
		return
	}
	if r.Method == http.MethodGet {
		item, err := s.scripts.GetTrigger(r.Context(), principal, id)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		state, _ := s.store.GetScriptTriggerState(r.Context(), id)
		spec, _ := scripting.ParseTriggerSpec(item.SpecJSON)
		slots, _ := scripting.NextSlots(spec, time.Now().UTC(), 5)
		scriptOK(w, r, http.StatusOK, map[string]any{"trigger": item, "state": state, "next_slots": slots})
		return
	}
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	var input model.ScriptTriggerBinding
	if !decodeV2(w, r, &input) {
		return
	}
	input.ID = id
	expected := int64Query(r, "expected_binding_revision", 0)
	if expected == 0 {
		expected = input.BindingRevision
	}
	item, err := s.scripts.UpdateTrigger(r.Context(), principal, input, expected)
	if err != nil {
		scriptErr(w, r, err)
		return
	}
	scriptOK(w, r, http.StatusOK, map[string]any{"trigger": item})
}

func (s *Server) apiV1ScriptGrants(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		scriptID, _ := strconv.ParseInt(r.URL.Query().Get("script_id"), 10, 64)
		items, err := s.scripts.ListGrants(r.Context(), principal, scriptID)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"grants": items})
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input model.ScriptGrant
	if !decodeV2(w, r, &input) {
		return
	}
	item, err := s.scripts.CreateGrant(r.Context(), principal, input)
	if err != nil {
		scriptErr(w, r, err)
		return
	}
	scriptOK(w, r, http.StatusCreated, map[string]any{"grant": item})
}

func (s *Server) apiV1ScriptGrantItem(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/script-grants/")
	if len(parts) < 2 || parts[1] != "revoke" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		scriptErr(w, r, scripting.Coded(model.ScriptErrorInvalidInput, err.Error()))
		return
	}
	if err := s.scripts.RevokeGrant(r.Context(), principal, id); err != nil {
		scriptErr(w, r, err)
		return
	}
	scriptOK(w, r, http.StatusOK, map[string]any{"revoked": true})
}

func (s *Server) apiV1ScriptRunItem(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/script-runs/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		scriptErr(w, r, scripting.Coded(model.ScriptErrorInvalidInput, err.Error()))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		run, err := s.scripts.GetRun(r.Context(), principal, id)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"run": run})
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		run, err := s.scripts.CancelRun(r.Context(), principal, id)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"run": run})
		return
	}
	if len(parts) == 2 && parts[1] == "logs" && r.Method == http.MethodGet {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		items, err := s.scripts.ListRunLogs(r.Context(), principal, id, after, intQuery(r, "limit", 200))
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"logs": items})
		return
	}
	if len(parts) == 2 && parts[1] == "actions" && r.Method == http.MethodGet {
		items, err := s.scripts.ListRunActions(r.Context(), principal, id)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"actions": items})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) apiV1ScriptRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		principal, _ := apiPrincipal(r)
		status, err := s.scripts.RuntimeStatus(r.Context(), principal, s.scriptIsolation, s.scriptWorkerConnected.Load())
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"status": status})
		return
	}
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	var input scripting.Settings
	if !decodeV2(w, r, &input) {
		return
	}
	principal, _ := apiPrincipal(r)
	if err := s.scripts.UpdateSettings(r.Context(), principal, input); err != nil {
		scriptErr(w, r, err)
		return
	}
	status, _ := s.scripts.RuntimeStatus(r.Context(), principal, s.scriptIsolation, s.scriptWorkerConnected.Load())
	scriptOK(w, r, http.StatusOK, map[string]any{"status": status})
}

func (s *Server) registerScriptAutomationOperations() {
	type idInput struct {
		ID         int64           `json:"id"`
		ScriptID   int64           `json:"script_id"`
		RevisionID int64           `json:"revision_id"`
		Name       string          `json:"name"`
		Description string         `json:"description"`
		Status     string          `json:"status"`
		Source     string          `json:"source"`
		Manifest   json.RawMessage `json:"manifest"`
		Params     json.RawMessage `json:"params"`
		Env        json.RawMessage `json:"env"`
		Kind       string          `json:"kind"`
		Spec       json.RawMessage `json:"spec"`
		Enabled    *bool           `json:"enabled"`
		IdempotencyKey string      `json:"idempotency_key"`
		ExpectedUpdatedAt string   `json:"expected_updated_at"`
		ExpectedBindingRevision int64 `json:"expected_binding_revision"`
		Capabilities json.RawMessage `json:"capabilities"`
		ResourceScope json.RawMessage `json:"resource_scope"`
		Constraints json.RawMessage `json:"constraints"`
		BindingID  *int64          `json:"binding_id"`
		HostActionsEnabled *bool   `json:"host_actions_enabled"`
		SchedulerPaused    *bool   `json:"scheduler_paused"`
		MaxConcurrency     int     `json:"max_concurrency"`
		MaxTimeoutSeconds  int     `json:"max_timeout_seconds"`
		ServerID           int64   `json:"server_id"`
		ScriptsEnabled     *bool   `json:"scripts_enabled"`
		ScriptsPowerEnabled *bool  `json:"scripts_power_enabled"`
	}
	register := func(name string, apply func(context.Context, application.Principal, idInput) (any, error)) {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			var request idInput
			if err := strictAutomationInput(input, &request); err != nil {
				return nil, err
			}
			return map[string]any{"accepted": true}, nil
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			var request idInput
			if err := strictAutomationInput(input, &request); err != nil {
				return nil, err
			}
			return apply(ctx, principal, request)
		})
	}
	register("scripts.create", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.CreateScript(ctx, principal, in.Name, in.Description)
		return map[string]any{"script": item}, err
	})
	register("scripts.update", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.UpdateScript(ctx, principal, in.ID, in.Name, in.Description, in.Status, in.ExpectedUpdatedAt)
		return map[string]any{"script": item}, err
	})
	register("scripts.revisions.save", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.SaveDraft(ctx, principal, in.ScriptID, in.Source, in.Manifest)
		return map[string]any{"revision": item}, err
	})
	register("scripts.revisions.publish", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.Publish(ctx, principal, in.ScriptID, in.RevisionID)
		return map[string]any{"revision": item}, err
	})
	register("scripts.simulate", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.EnqueueSimulate(ctx, principal, in.ScriptID, in.RevisionID, in.Params, in.Env, in.IdempotencyKey)
		return map[string]any{"run": item}, err
	})
	register("scripts.run", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.EnqueueManualRun(ctx, principal, in.ScriptID, in.RevisionID, in.Params, in.Env, in.IdempotencyKey, model.ScriptRunModeLive)
		return map[string]any{"run": item}, err
	})
	register("scripts.runs.cancel", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.CancelRun(ctx, principal, in.ID)
		return map[string]any{"run": item}, err
	})
	register("script_triggers.create", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.CreateTrigger(ctx, principal, model.ScriptTriggerBinding{ScriptID: in.ScriptID, RevisionID: in.RevisionID, Name: in.Name, Kind: in.Kind, SpecJSON: in.Spec, ParamsJSON: in.Params, EnvJSON: in.Env})
		return map[string]any{"trigger": item}, err
	})
	register("script_triggers.update", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item := model.ScriptTriggerBinding{ID: in.ID, RevisionID: in.RevisionID, Name: in.Name, SpecJSON: in.Spec, ParamsJSON: in.Params, EnvJSON: in.Env}
		if in.Enabled != nil {
			item.Enabled = *in.Enabled
		}
		updated, err := s.scripts.UpdateTrigger(ctx, principal, item, in.ExpectedBindingRevision)
		return map[string]any{"trigger": updated}, err
	})
	register("script_grants.create", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.scripts.CreateGrant(ctx, principal, model.ScriptGrant{ScriptID: in.ScriptID, RevisionID: in.RevisionID, BindingID: in.BindingID, CapabilitiesJSON: in.Capabilities, ResourceScopeJSON: in.ResourceScope, ConstraintsJSON: in.Constraints})
		return map[string]any{"grant_id": item.ID, "grant": item}, err
	})
	register("script_grants.revoke", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		err := s.scripts.RevokeGrant(ctx, principal, in.ID)
		return map[string]any{"revoked": err == nil}, err
	})
	register("script_runtime.settings.update", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		next := s.scripts.Settings(ctx)
		if in.Enabled != nil {
			next.Enabled = *in.Enabled
		}
		if in.HostActionsEnabled != nil {
			next.HostActionsEnabled = *in.HostActionsEnabled
		}
		if in.SchedulerPaused != nil {
			next.SchedulerPaused = *in.SchedulerPaused
		}
		if in.MaxConcurrency > 0 {
			next.MaxConcurrency = in.MaxConcurrency
		}
		if in.MaxTimeoutSeconds > 0 {
			next.MaxTimeoutSeconds = in.MaxTimeoutSeconds
		}
		if err := s.scripts.UpdateSettings(ctx, principal, next); err != nil {
			return nil, err
		}
		status, err := s.scripts.RuntimeStatus(ctx, principal, s.scriptIsolation, s.scriptWorkerConnected.Load())
		return map[string]any{"status": status}, err
	})
	register("servers.script_policy.update", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		policy := model.ServerScriptPolicy{ServerID: in.ServerID}
		if in.ScriptsEnabled != nil {
			policy.ScriptsEnabled = *in.ScriptsEnabled
		}
		if in.ScriptsPowerEnabled != nil {
			policy.ScriptsPowerEnabled = *in.ScriptsPowerEnabled
		}
		item, err := s.scripts.UpdateServerPolicy(ctx, principal, policy)
		return map[string]any{"policy": item}, err
	})
}

func (s *Server) queryScriptCapability(ctx context.Context, principal application.Principal, name string, input json.RawMessage) (any, error) {
	switch name {
	case "scripts.list":
		var request struct {
			Status string `json:"status"`
			Limit  int    `json:"limit"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		items, err := s.scripts.ListScripts(ctx, principal, request.Status, request.Limit)
		return map[string]any{"scripts": items}, err
	case "scripts.get":
		var request struct {
			ID int64 `json:"id"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		item, err := s.scripts.GetScript(ctx, principal, request.ID)
		if err != nil {
			return nil, err
		}
		draft, _ := s.store.GetScriptDraft(ctx, request.ID)
		return map[string]any{"script": item, "draft": draft}, nil
	case "scripts.validate":
		var request struct {
			ScriptID int64           `json:"script_id"`
			Source   string          `json:"source"`
			Manifest json.RawMessage `json:"manifest"`
			Params   json.RawMessage `json:"params"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return s.scripts.ValidateRevision(ctx, principal, request.ScriptID, request.Source, request.Manifest, request.Params)
	case "scripts.runs.get":
		var request struct {
			ID int64 `json:"id"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		item, err := s.scripts.GetRun(ctx, principal, request.ID)
		return map[string]any{"run": item}, err
	case "script_triggers.list":
		var request struct {
			ScriptID int64 `json:"script_id"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		items, err := s.scripts.ListTriggers(ctx, principal, request.ScriptID)
		return map[string]any{"triggers": items}, err
	case "script_runtime.status":
		status, err := s.scripts.RuntimeStatus(ctx, principal, s.scriptIsolation, s.scriptWorkerConnected.Load())
		return map[string]any{"status": status}, err
	default:
		return nil, errors.New("unsupported query capability")
	}
}

func (s *Server) apiV1ScriptImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	principal, _ := apiPrincipal(r)
	var payload json.RawMessage
	if !decodeV2(w, r, &payload) {
		return
	}
	script, rev, err := s.scripts.Import(r.Context(), principal, payload)
	if err != nil {
		scriptErr(w, r, err)
		return
	}
	scriptOK(w, r, http.StatusCreated, map[string]any{"script": script, "revision": rev})
}

func (s *Server) apiV1ServerScriptPolicies(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		serverID, _ := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
		if serverID <= 0 {
			scriptErr(w, r, scripting.Coded(model.ScriptErrorInvalidInput, "server_id is required"))
			return
		}
		item, err := s.store.GetServerScriptPolicy(r.Context(), serverID)
		if err != nil {
			scriptErr(w, r, err)
			return
		}
		scriptOK(w, r, http.StatusOK, map[string]any{"policy": item})
		return
	}
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	var input model.ServerScriptPolicy
	if !decodeV2(w, r, &input) {
		return
	}
	item, err := s.scripts.UpdateServerPolicy(r.Context(), principal, input)
	if err != nil {
		scriptErr(w, r, err)
		return
	}
	scriptOK(w, r, http.StatusOK, map[string]any{"policy": item})
}

func scriptOK(w http.ResponseWriter, r *http.Request, status int, data any) {
	v2Write(w, r, status, data, nil)
}

func scriptErr(w http.ResponseWriter, r *http.Request, err error) {
	v2Error(w, r, scriptHTTPStatus(err), scripting.CodeOf(err), err.Error())
}

func scriptHTTPStatus(err error) int {
	switch scripting.CodeOf(err) {
	case "permission_denied", "approval_required":
		return http.StatusForbidden
	case "not_found":
		return http.StatusNotFound
	case "conflict", "idempotency_conflict":
		return http.StatusConflict
	case "invalid_input":
		return http.StatusBadRequest
	default:
		return http.StatusBadRequest
	}
}

