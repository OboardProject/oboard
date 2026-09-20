package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
)

func (s *Server) registerPluginRoutes(mux *http.ServeMux) {
	s.registerPluginPackageRoutes(mux)
	mux.HandleFunc("/api/v1/plugins", s.apiAuth(s.apiV1Plugins, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugins/import", s.apiAuth(s.apiV1PluginImport, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugins/", s.apiAuth(s.apiV1PluginItem, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugin-triggers", s.apiAuth(s.apiV1PluginTriggers, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugin-triggers/", s.apiAuth(s.apiV1PluginTriggerItem, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugin-grants", s.apiAuth(s.apiV1PluginGrants, model.RoleAdmin))
	mux.HandleFunc("/api/v1/plugin-grants/", s.apiAuth(s.apiV1PluginGrantItem, model.RoleAdmin))
	mux.HandleFunc("/api/v1/plugin-runs/", s.apiAuth(s.apiV1PluginRunItem, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugin-runtime/status", s.apiAuth(s.apiV1PluginRuntime, model.RoleOperator))
	mux.HandleFunc("/api/v1/server-plugin-policies", s.apiAuth(s.apiV1ServerPluginPolicies, model.RoleAdmin))
}

func (s *Server) apiV1Plugins(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	switch r.Method {
	case http.MethodGet:
		items, err := s.plugins.ListPlugins(r.Context(), principal, strings.TrimSpace(r.URL.Query().Get("status")), intQuery(r, "limit", 100))
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"plugins": items})
	case http.MethodPost:
		var input struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if !decodeV2(w, r, &input) {
			return
		}
		item, err := s.plugins.CreatePlugin(r.Context(), principal, input.Name, input.Description)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusCreated, map[string]any{"plugin": item})
	default:
		method(w)
	}
}

func (s *Server) apiV1PluginItem(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/plugins/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, "invalid plugin id"))
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			item, err := s.plugins.GetPlugin(r.Context(), principal, id)
			if err != nil {
				pluginErr(w, r, err)
				return
			}
			draft, _ := s.store.GetPluginDraft(r.Context(), id)
			revs, _ := s.store.ListPluginRevisions(r.Context(), id)
			var published *model.PluginRevision
			for i := range revs {
				if revs[i].Status == model.PluginRevisionPublished {
					published = &revs[i]
					break
				}
			}
			pluginOK(w, r, http.StatusOK, map[string]any{"plugin": item, "draft": draft, "published": published, "revisions": revs})
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
			item, err := s.plugins.UpdatePlugin(r.Context(), principal, id, input.Name, input.Description, input.Status, input.ExpectedUpdatedAt)
			if err != nil {
				pluginErr(w, r, err)
				return
			}
			pluginOK(w, r, http.StatusOK, map[string]any{"plugin": item})
		default:
			method(w)
		}
		return
	}
	switch parts[1] {
	case "revisions":
		if r.Method == http.MethodGet {
			items, err := s.plugins.ListRevisions(r.Context(), principal, id)
			if err != nil {
				pluginErr(w, r, err)
				return
			}
			pluginOK(w, r, http.StatusOK, map[string]any{"revisions": items})
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
			rev, err := s.plugins.SaveDraft(r.Context(), principal, id, input.Source, input.Manifest)
			if err != nil {
				pluginErr(w, r, err)
				return
			}
			pluginOK(w, r, http.StatusOK, map[string]any{"revision": rev})
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
		result, err := s.plugins.ValidateRevision(r.Context(), principal, id, input.Source, input.Manifest, input.Params)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, result)
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
		run, err := s.plugins.EnqueueSimulate(r.Context(), principal, id, input.RevisionID, input.Params, input.Env, input.IdempotencyKey)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusAccepted, map[string]any{"run": run})
		return
	case "runs":
		if r.Method == http.MethodGet {
			items, err := s.plugins.ListRuns(r.Context(), principal, id, intQuery(r, "limit", 50))
			if err != nil {
				pluginErr(w, r, err)
				return
			}
			pluginOK(w, r, http.StatusOK, map[string]any{"runs": items})
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
			run, err := s.plugins.EnqueueManualRun(r.Context(), principal, id, input.RevisionID, input.Params, input.Env, input.IdempotencyKey, model.PluginRunModeLive)
			if err != nil {
				pluginErr(w, r, err)
				return
			}
			pluginOK(w, r, http.StatusAccepted, map[string]any{"run": run})
			return
		}
	case "export":
		bundle, err := s.plugins.Export(r.Context(), principal, id)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, bundle)
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
		rev, err := s.plugins.Publish(r.Context(), principal, id, input.RevisionID)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"revision": rev})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) apiV1PluginTriggers(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		pluginID, _ := strconv.ParseInt(r.URL.Query().Get("plugin_id"), 10, 64)
		items, err := s.plugins.ListTriggers(r.Context(), principal, pluginID)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"triggers": items})
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input model.PluginTriggerBinding
	if !decodeV2(w, r, &input) {
		return
	}
	item, err := s.plugins.CreateTrigger(r.Context(), principal, input)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusCreated, map[string]any{"trigger": item})
}

func (s *Server) apiV1PluginTriggerItem(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/plugin-triggers/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, err.Error()))
		return
	}
	if r.Method == http.MethodGet {
		item, err := s.plugins.GetTrigger(r.Context(), principal, id)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		state, _ := s.store.GetPluginTriggerState(r.Context(), id)
		spec, _ := plugin.ParseTriggerSpec(item.SpecJSON)
		slots, _ := plugin.NextSlots(spec, time.Now().UTC(), 5)
		pluginOK(w, r, http.StatusOK, map[string]any{"trigger": item, "state": state, "next_slots": slots})
		return
	}
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	var input model.PluginTriggerBinding
	if !decodeV2(w, r, &input) {
		return
	}
	input.ID = id
	expected := int64Query(r, "expected_binding_revision", 0)
	if expected == 0 {
		expected = input.BindingRevision
	}
	item, err := s.plugins.UpdateTrigger(r.Context(), principal, input, expected)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, map[string]any{"trigger": item})
}

func (s *Server) apiV1PluginGrants(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		pluginID, _ := strconv.ParseInt(r.URL.Query().Get("plugin_id"), 10, 64)
		items, err := s.plugins.ListGrants(r.Context(), principal, pluginID)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"grants": items})
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input model.PluginGrant
	if !decodeV2(w, r, &input) {
		return
	}
	item, err := s.plugins.CreateGrant(r.Context(), principal, input)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusCreated, map[string]any{"grant": item})
}

func (s *Server) apiV1PluginGrantItem(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/plugin-grants/")
	if len(parts) < 2 || parts[1] != "revoke" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, err.Error()))
		return
	}
	if err := s.plugins.RevokeGrant(r.Context(), principal, id); err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, map[string]any{"revoked": true})
}

func (s *Server) apiV1PluginRunItem(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/plugin-runs/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, err.Error()))
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		run, err := s.plugins.GetRun(r.Context(), principal, id)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"run": run})
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		run, err := s.plugins.CancelRun(r.Context(), principal, id)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"run": run})
		return
	}
	if len(parts) == 2 && parts[1] == "logs" && r.Method == http.MethodGet {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		items, err := s.plugins.ListRunLogs(r.Context(), principal, id, after, intQuery(r, "limit", 200))
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"logs": items})
		return
	}
	if len(parts) == 2 && parts[1] == "actions" && r.Method == http.MethodGet {
		items, err := s.plugins.ListRunActions(r.Context(), principal, id)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"actions": items})
		return
	}
	http.NotFound(w, r)
}

func (s *Server) apiV1PluginRuntime(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		principal, _ := apiPrincipal(r)
		status, err := s.loadPluginRuntimeStatus(r.Context(), principal)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"status": status})
		return
	}
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	var input struct {
		Enabled            *bool `json:"enabled"`
		HostActionsEnabled *bool `json:"host_actions_enabled"`
		SchedulerPaused    *bool `json:"scheduler_paused"`
		MaxConcurrency     int   `json:"max_concurrency"`
		MaxTimeoutSeconds  int   `json:"max_timeout_seconds"`
	}
	if !decodeV2(w, r, &input) {
		return
	}
	principal, _ := apiPrincipal(r)
	next := s.plugins.Settings(r.Context())
	if input.Enabled != nil {
		next.Enabled = *input.Enabled
	}
	if input.HostActionsEnabled != nil {
		next.HostActionsEnabled = *input.HostActionsEnabled
	}
	if input.SchedulerPaused != nil {
		next.SchedulerPaused = *input.SchedulerPaused
	}
	if input.MaxConcurrency > 0 {
		next.MaxConcurrency = input.MaxConcurrency
	}
	if input.MaxTimeoutSeconds > 0 {
		next.MaxTimeoutSeconds = input.MaxTimeoutSeconds
	}
	if err := s.ensurePluginRuntimeForEnable(next.Enabled); err != nil {
		pluginErr(w, r, err)
		return
	}
	if err := s.plugins.UpdateSettings(r.Context(), principal, next); err != nil {
		pluginErr(w, r, err)
		return
	}
	status, _ := s.loadPluginRuntimeStatus(r.Context(), principal)
	pluginOK(w, r, http.StatusOK, map[string]any{"status": status})
}

func (s *Server) registerPluginAutomationOperations() {
	s.registerPluginPackageAutomationOperations()
	type idInput struct {
		ID                      int64           `json:"id"`
		PluginID                int64           `json:"plugin_id"`
		RevisionID              int64           `json:"revision_id"`
		Name                    string          `json:"name"`
		Description             string          `json:"description"`
		Status                  string          `json:"status"`
		Source                  string          `json:"source"`
		Manifest                json.RawMessage `json:"manifest"`
		Params                  json.RawMessage `json:"params"`
		Env                     json.RawMessage `json:"env"`
		Kind                    string          `json:"kind"`
		Spec                    json.RawMessage `json:"spec"`
		Enabled                 *bool           `json:"enabled"`
		IdempotencyKey          string          `json:"idempotency_key"`
		ExpectedUpdatedAt       string          `json:"expected_updated_at"`
		ExpectedBindingRevision int64           `json:"expected_binding_revision"`
		Capabilities            json.RawMessage `json:"capabilities"`
		ResourceScope           json.RawMessage `json:"resource_scope"`
		Constraints             json.RawMessage `json:"constraints"`
		BindingID               *int64          `json:"binding_id"`
		HostActionsEnabled      *bool           `json:"host_actions_enabled"`
		SchedulerPaused         *bool           `json:"scheduler_paused"`
		MaxConcurrency          int             `json:"max_concurrency"`
		MaxTimeoutSeconds       int             `json:"max_timeout_seconds"`
		ServerID                int64           `json:"server_id"`
		PluginsEnabled          *bool           `json:"plugins_enabled"`
		PluginsPowerEnabled     *bool           `json:"plugins_power_enabled"`
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
	register("plugins.create", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.CreatePlugin(ctx, principal, in.Name, in.Description)
		return map[string]any{"plugin": item}, err
	})
	register("plugins.update", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.UpdatePlugin(ctx, principal, in.ID, in.Name, in.Description, in.Status, in.ExpectedUpdatedAt)
		return map[string]any{"plugin": item}, err
	})
	register("plugins.revisions.save", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.SaveDraft(ctx, principal, in.PluginID, in.Source, in.Manifest)
		return map[string]any{"revision": item}, err
	})
	register("plugins.revisions.publish", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.Publish(ctx, principal, in.PluginID, in.RevisionID)
		return map[string]any{"revision": item}, err
	})
	register("plugins.simulate", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.EnqueueSimulate(ctx, principal, in.PluginID, in.RevisionID, in.Params, in.Env, in.IdempotencyKey)
		return map[string]any{"run": item}, err
	})
	register("plugins.run", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.EnqueueManualRun(ctx, principal, in.PluginID, in.RevisionID, in.Params, in.Env, in.IdempotencyKey, model.PluginRunModeLive)
		return map[string]any{"run": item}, err
	})
	register("plugins.runs.cancel", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.CancelRun(ctx, principal, in.ID)
		return map[string]any{"run": item}, err
	})
	register("plugin_triggers.create", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.CreateTrigger(ctx, principal, model.PluginTriggerBinding{PluginID: in.PluginID, RevisionID: in.RevisionID, Name: in.Name, Kind: in.Kind, SpecJSON: in.Spec, ParamsJSON: in.Params, EnvJSON: in.Env})
		return map[string]any{"trigger": item}, err
	})
	register("plugin_triggers.update", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item := model.PluginTriggerBinding{ID: in.ID, RevisionID: in.RevisionID, Name: in.Name, SpecJSON: in.Spec, ParamsJSON: in.Params, EnvJSON: in.Env}
		if in.Enabled != nil {
			item.Enabled = *in.Enabled
		}
		updated, err := s.plugins.UpdateTrigger(ctx, principal, item, in.ExpectedBindingRevision)
		return map[string]any{"trigger": updated}, err
	})
	register("plugin_grants.create", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		item, err := s.plugins.CreateGrant(ctx, principal, model.PluginGrant{PluginID: in.PluginID, RevisionID: in.RevisionID, BindingID: in.BindingID, CapabilitiesJSON: in.Capabilities, ResourceScopeJSON: in.ResourceScope, ConstraintsJSON: in.Constraints})
		return map[string]any{"grant_id": item.ID, "grant": item}, err
	})
	register("plugin_grants.revoke", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		err := s.plugins.RevokeGrant(ctx, principal, in.ID)
		return map[string]any{"revoked": err == nil}, err
	})
	register("plugin_runtime.settings.update", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		next := s.plugins.Settings(ctx)
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
		if err := s.ensurePluginRuntimeForEnable(next.Enabled); err != nil {
			return nil, err
		}
		if err := s.plugins.UpdateSettings(ctx, principal, next); err != nil {
			return nil, err
		}
		status, err := s.loadPluginRuntimeStatus(ctx, principal)
		return map[string]any{"status": status}, err
	})
	register("servers.plugin_policy.update", func(ctx context.Context, principal application.Principal, in idInput) (any, error) {
		policy := model.ServerPluginPolicy{ServerID: in.ServerID}
		if in.PluginsEnabled != nil {
			policy.PluginsEnabled = *in.PluginsEnabled
		}
		if in.PluginsPowerEnabled != nil {
			policy.PluginsPowerEnabled = *in.PluginsPowerEnabled
		}
		item, err := s.plugins.UpdateServerPolicy(ctx, principal, policy)
		return map[string]any{"policy": item}, err
	})
}

func (s *Server) queryPluginCapability(ctx context.Context, principal application.Principal, name string, input json.RawMessage) (any, error) {
	switch name {
	case "plugins.packages.preview", "plugins.github.preview", "plugins.versions.list", "plugins.config.get", "plugins.ui.get":
		return s.queryPluginPackageCapability(ctx, principal, name, input)
	case "plugins.list":
		var request struct {
			Status string `json:"status"`
			Limit  int    `json:"limit"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		items, err := s.plugins.ListPlugins(ctx, principal, request.Status, request.Limit)
		return map[string]any{"plugins": items}, err
	case "plugins.get":
		var request struct {
			ID int64 `json:"id"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		item, err := s.plugins.GetPlugin(ctx, principal, request.ID)
		if err != nil {
			return nil, err
		}
		draft, _ := s.store.GetPluginDraft(ctx, request.ID)
		return map[string]any{"plugin": item, "draft": draft}, nil
	case "plugins.validate":
		var request struct {
			PluginID int64           `json:"plugin_id"`
			Source   string          `json:"source"`
			Manifest json.RawMessage `json:"manifest"`
			Params   json.RawMessage `json:"params"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		return s.plugins.ValidateRevision(ctx, principal, request.PluginID, request.Source, request.Manifest, request.Params)
	case "plugins.runs.get":
		var request struct {
			ID int64 `json:"id"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		item, err := s.plugins.GetRun(ctx, principal, request.ID)
		return map[string]any{"run": item}, err
	case "plugin_triggers.list":
		var request struct {
			PluginID int64 `json:"plugin_id"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		items, err := s.plugins.ListTriggers(ctx, principal, request.PluginID)
		return map[string]any{"triggers": items}, err
	case "plugin_runtime.status":
		status, err := s.loadPluginRuntimeStatus(ctx, principal)
		return map[string]any{"status": status}, err
	default:
		return nil, errors.New("unsupported query capability")
	}
}

func (s *Server) apiV1PluginImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	principal, _ := apiPrincipal(r)
	var payload json.RawMessage
	if !decodeV2(w, r, &payload) {
		return
	}
	plugin, rev, err := s.plugins.Import(r.Context(), principal, payload)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusCreated, map[string]any{"plugin": plugin, "revision": rev})
}

func (s *Server) apiV1ServerPluginPolicies(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		serverID, _ := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
		if serverID <= 0 {
			pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, "server_id is required"))
			return
		}
		item, err := s.store.GetServerPluginPolicy(r.Context(), serverID)
		if err != nil {
			pluginErr(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"policy": item})
		return
	}
	if r.Method != http.MethodPatch {
		method(w)
		return
	}
	var input model.ServerPluginPolicy
	if !decodeV2(w, r, &input) {
		return
	}
	item, err := s.plugins.UpdateServerPolicy(r.Context(), principal, input)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, map[string]any{"policy": item})
}

func (s *Server) pluginRuntimeInstalled() bool {
	if s.pluginWorkerConnected.Load() {
		return true
	}
	if _, err := os.Stat("/etc/systemd/system/oboard-plugin-worker.service"); err == nil {
		return true
	}
	_, err := os.Stat("/etc/init.d/oboard-plugin-worker")
	return err == nil
}

func (s *Server) pluginRuntimeInstallCommand() string {
	channel := strings.ToLower(strings.TrimSpace(os.Getenv("OBOARD_UPDATE_CHANNEL")))
	version := "latest"
	switch channel {
	case "dev", "development", "nightly":
		version = "dev"
	case "pinned":
		version = strings.TrimSpace(os.Getenv("OBOARD_VERSION"))
		if version == "" {
			version = "latest"
		}
	}
	return "curl --proto '=https' --tlsv1.2 -fsSL https://raw.githubusercontent.com/OboardProject/oboard/main/plugins/install.sh | sudo env OBOARD_ACTION=enable-plugins VERSION=" + version + " sh"
}

func (s *Server) loadPluginRuntimeStatus(ctx context.Context, principal application.Principal) (model.PluginRuntimeStatus, error) {
	installed := s.pluginRuntimeInstalled()
	command := ""
	if !installed {
		command = s.pluginRuntimeInstallCommand()
	}
	return s.plugins.RuntimeStatus(ctx, principal, s.pluginIsolation, s.pluginWorkerConnected.Load(), installed, command)
}

func (s *Server) ensurePluginRuntimeForEnable(enabled bool) error {
	if !enabled || s.pluginRuntimeInstalled() {
		return nil
	}
	return plugin.Coded(model.PluginErrorRuntimeUnavailable, "尚未安装插件运行环境，请先在主控主机上执行安装命令")
}

func pluginOK(w http.ResponseWriter, r *http.Request, status int, data any) {
	v2Write(w, r, status, data, nil)
}

func pluginErr(w http.ResponseWriter, r *http.Request, err error) {
	v2Error(w, r, pluginHTTPStatus(err), plugin.CodeOf(err), err.Error())
}

func pluginHTTPStatus(err error) int {
	switch plugin.CodeOf(err) {
	case "permission_denied", "approval_required":
		return http.StatusForbidden
	case "not_found":
		return http.StatusNotFound
	case "conflict", "idempotency_conflict":
		return http.StatusConflict
	case "runtime_unavailable":
		return http.StatusConflict
	case "invalid_input":
		return http.StatusBadRequest
	default:
		return http.StatusBadRequest
	}
}
