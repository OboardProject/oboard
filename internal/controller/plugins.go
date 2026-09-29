package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/plugingithub"
	"github.com/OboardProject/oboard/internal/pluginpackage"
	"github.com/OboardProject/oboard/internal/store"
)

const pluginPackageBodyLimit = 4 << 20

func (s *Server) registerPluginRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/plugins", s.apiAuth(s.apiV1Plugins, model.RoleViewer))
	mux.HandleFunc("/api/v1/plugins/", s.apiAuth(s.apiV1PluginItem, model.RoleViewer))
	mux.HandleFunc("/api/v1/plugin-instances/", s.apiAuth(s.apiV1PluginInstance, model.RoleViewer))
	mux.HandleFunc("/api/v1/plugin-schedules/", s.apiAuth(s.apiV1PluginSchedule, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugin-runs", s.apiAuth(s.apiV1PluginRuns, model.RoleViewer))
	mux.HandleFunc("/api/v1/plugin-runs/", s.apiAuth(s.apiV1PluginRun, model.RoleViewer))
	mux.HandleFunc("/api/v1/plugin-runtime", s.apiAuth(s.apiV1PluginRuntime, model.RoleViewer))
}

func pluginOK(w http.ResponseWriter, r *http.Request, status int, data any) {
	v2Write(w, r, status, data, nil)
}

// pluginFail writes the structured plugin error, including per-field issues
// for environment and manifest validation.
func pluginFail(w http.ResponseWriter, r *http.Request, err error) {
	coded := plugin.AsError(err)
	requestID := requestID(r)
	body := map[string]any{"code": coded.Code, "message": coded.Message, "request_id": requestID}
	if coded.Field != "" || len(coded.Issues) > 0 {
		body["details"] = map[string]any{"field": coded.Field, "issues": coded.Issues}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestID)
	w.WriteHeader(plugin.HTTPStatus(err))
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}

func pluginDecode(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		pluginFail(w, r, plugin.Fail(plugin.CodeInvalidArgument, "请求 JSON 无效"))
		return false
	}
	return true
}

func pluginIDPart(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, plugin.Fail(plugin.CodeInvalidArgument, "invalid id")
	}
	return id, nil
}

// PackageSourceInput selects where a package comes from.
type PackageSourceInput struct {
	Kind          string `json:"kind"`
	PackageBase64 string `json:"package_base64,omitempty"`
	RepositoryURL string `json:"repository_url,omitempty"`
	Ref           string `json:"ref,omitempty"`
	Commit        string `json:"commit,omitempty"`
}

// resolvePackageCandidate validates a package from an upload or a pinned
// GitHub commit. Nothing is executed; the source is only compiled.
func (s *Server) resolvePackageCandidate(ctx context.Context, source PackageSourceInput, install bool) (plugin.PackageCandidate, error) {
	var pkg *pluginpackage.Package
	var err error
	candidate := plugin.PackageCandidate{}
	switch source.Kind {
	case "upload":
		data, decodeErr := base64.StdEncoding.DecodeString(source.PackageBase64)
		if decodeErr != nil {
			return candidate, plugin.Fail(plugin.CodeInvalidPackage, "package_base64 is not valid base64")
		}
		pkg, err = pluginpackage.Parse(data)
		candidate.SourceKind = model.PluginSourceUpload
	case "github":
		ref := source.Ref
		if install {
			if len(source.Commit) != 40 {
				return candidate, plugin.Fail(plugin.CodeInvalidArgument, "install requires the reviewed 40-character commit")
			}
			ref = source.Commit
		}
		fetched, fetchErr := plugingithub.Fetch(ctx, source.RepositoryURL, ref)
		if fetchErr != nil {
			return candidate, plugin.Fail(plugin.CodeInvalidPackage, "GitHub 仓库读取失败："+fetchErr.Error())
		}
		pkg, err = pluginpackage.Validate(fetched.Files)
		candidate.SourceKind = model.PluginSourceGitHub
		candidate.SourceRepository, candidate.SourceCommit = fetched.Repository, fetched.Commit
		if install && fetched.Commit != source.Commit {
			return candidate, plugin.Fail(plugin.CodeConflict, "仓库提交与预览不一致，请重新预览")
		}
	default:
		return candidate, plugin.Fail(plugin.CodeInvalidArgument, "source.kind must be upload or github")
	}
	if err != nil {
		return candidate, err
	}
	candidate.Manifest, candidate.Source, candidate.Icon = pkg.Manifest, pkg.Source, pkg.Icon
	candidate.Readme, candidate.License, candidate.SHA256 = pkg.Readme, pkg.License, pkg.SHA256
	candidate.PublisherIdentity, candidate.PublisherName, candidate.SignatureState = pkg.Publisher.Identity, pkg.Publisher.Name, pkg.SignatureState
	if pkg.SignatureState != model.PluginSignatureVerified {
		candidate.PublisherIdentity, candidate.SignatureState = model.PluginPublisherLocal, model.PluginSignatureUnsigned
	}
	return candidate, nil
}

// draftCandidate turns an editor draft into a local package candidate.
func draftCandidate(input plugin.DraftInput) (plugin.PackageCandidate, error) {
	pkg, err := pluginpackage.Validate(map[string][]byte{plugin.ManifestFile: input.Manifest, plugin.EntryFile: []byte(input.Source)})
	if err != nil {
		return plugin.PackageCandidate{}, err
	}
	return plugin.PackageCandidate{Manifest: pkg.Manifest, Source: pkg.Source, SHA256: pkg.SHA256, PublisherIdentity: model.PluginPublisherLocal, SignatureState: model.PluginSignatureUnsigned, SourceKind: model.PluginSourceEditor}, nil
}

// pluginCatalogView is the capability and environment catalog for the Web
// permission review and editor.
func pluginCatalogView() map[string]any {
	return map[string]any{
		"capabilities":      plugin.CapabilityCatalog(),
		"forbidden":         plugin.ForbiddenCapabilityGroups(),
		"environment_types": []string{plugin.EnvString, plugin.EnvText, plugin.EnvInteger, plugin.EnvNumber, plugin.EnvBoolean, plugin.EnvSelect, plugin.EnvMultiSelect, plugin.EnvServer, plugin.EnvServers, plugin.EnvSecret, plugin.EnvURL, plugin.EnvDuration, plugin.EnvJSON},
		"custom_types":      []string{plugin.EnvString, plugin.EnvText, plugin.EnvInteger, plugin.EnvNumber, plugin.EnvBoolean, plugin.EnvServer, plugin.EnvServers, plugin.EnvSecret, plugin.EnvURL, plugin.EnvDuration, plugin.EnvJSON},
		"events":            []string{plugin.EventServerOnline, plugin.EventServerOffline},
		"runtime":           plugin.RuntimeJS,
	}
}

func (s *Server) apiV1Plugins(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	switch r.Method {
	case http.MethodGet:
		items, err := s.plugins.ListInstallations(r.Context(), principal)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"plugins": items})
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
	ctx := r.Context()
	switch parts[0] {
	case "catalog":
		if !s.plugins.Can(principal, plugin.PermRead) {
			pluginFail(w, r, plugin.ErrPermissionDenied)
			return
		}
		pluginOK(w, r, http.StatusOK, pluginCatalogView())
		return
	case "servers":
		servers, err := s.plugins.ServerOptions(ctx, principal)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"servers": servers})
		return
	case "drafts":
		s.apiV1PluginDrafts(w, r, principal, parts[1:])
		return
	case "packages":
		s.apiV1PluginPackages(w, r, principal, parts[1:])
		return
	}
	id, err := pluginIDPart(parts[0])
	if err != nil {
		pluginFail(w, r, err)
		return
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			detail, err := s.plugins.GetInstallation(ctx, principal, id)
			if err != nil {
				pluginFail(w, r, err)
				return
			}
			pluginOK(w, r, http.StatusOK, detail)
		case http.MethodPatch:
			var input struct {
				Enabled bool `json:"enabled"`
			}
			if !pluginDecode(w, r, &input, 1<<16) {
				return
			}
			if err := s.plugins.SetInstallationEnabled(ctx, principal, id, input.Enabled); err != nil {
				pluginFail(w, r, err)
				return
			}
			s.publishRealtime("plugins")
			detail, err := s.plugins.GetInstallation(ctx, principal, id)
			if err != nil {
				pluginFail(w, r, err)
				return
			}
			pluginOK(w, r, http.StatusOK, detail)
		default:
			method(w)
		}
		return
	}
	switch {
	case parts[1] == "uninstall" && r.Method == http.MethodPost:
		var input struct {
			Confirm bool `json:"confirm"`
		}
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		if !input.Confirm {
			pluginFail(w, r, plugin.Fail(plugin.CodeInvalidArgument, "confirm is required"))
			return
		}
		if err := s.plugins.Uninstall(ctx, principal, id); err != nil {
			pluginFail(w, r, err)
			return
		}
		s.publishRealtime("plugins")
		pluginOK(w, r, http.StatusOK, map[string]any{"uninstalled": true})
	case parts[1] == "draft" && len(parts) == 2 && r.Method == http.MethodPut:
		var input struct {
			Manifest string `json:"manifest"`
			Source   string `json:"source"`
		}
		if !pluginDecode(w, r, &input, 2<<20) {
			return
		}
		if err := s.plugins.SaveDraft(ctx, principal, id, plugin.DraftInput{Manifest: json.RawMessage(input.Manifest), Source: input.Source}); err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"saved": true})
	case parts[1] == "draft" && len(parts) == 3 && parts[2] == "publish" && r.Method == http.MethodPost:
		var input struct {
			Confirm bool `json:"confirm"`
		}
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		if !input.Confirm {
			pluginFail(w, r, plugin.Fail(plugin.CodeInvalidArgument, "confirm is required"))
			return
		}
		result, err := s.plugins.Publish(ctx, principal, id, draftCandidate)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		s.publishRealtime("plugins")
		pluginOK(w, r, http.StatusOK, result)
	case parts[1] == "versions" && len(parts) == 4 && parts[3] == "activate" && r.Method == http.MethodPost:
		packageID, err := pluginIDPart(parts[2])
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		var input struct {
			Confirm bool `json:"confirm"`
		}
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		if !input.Confirm {
			pluginFail(w, r, plugin.Fail(plugin.CodeInvalidArgument, "confirm is required"))
			return
		}
		result, err := s.plugins.ActivateVersion(ctx, principal, id, packageID)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		s.publishRealtime("plugins")
		pluginOK(w, r, http.StatusOK, result)
	case parts[1] == "instances" && len(parts) == 2 && r.Method == http.MethodPost:
		var input struct {
			Name string `json:"name"`
		}
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		instance, err := s.plugins.CreateInstance(ctx, principal, id, input.Name)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		s.publishRealtime("plugins")
		pluginOK(w, r, http.StatusCreated, instance)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) apiV1PluginDrafts(w http.ResponseWriter, r *http.Request, principal application.Principal, parts []string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input struct {
		Manifest string `json:"manifest"`
		Source   string `json:"source"`
	}
	if !pluginDecode(w, r, &input, 2<<20) {
		return
	}
	draft := plugin.DraftInput{Manifest: json.RawMessage(input.Manifest), Source: input.Source}
	switch {
	case len(parts) == 0:
		installation, err := s.plugins.CreateDraftPlugin(r.Context(), principal, draft)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		s.publishRealtime("plugins")
		pluginOK(w, r, http.StatusCreated, map[string]any{"plugin_id": installation.ID})
	case len(parts) == 1 && parts[0] == "diagnose":
		result, err := s.plugins.Diagnose(r.Context(), principal, draft, pluginpackage.CheckSource)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, result)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) apiV1PluginPackages(w http.ResponseWriter, r *http.Request, principal application.Principal, parts []string) {
	if r.Method != http.MethodPost || len(parts) != 1 {
		method(w)
		return
	}
	switch parts[0] {
	case "preview":
		var input struct {
			Source PackageSourceInput `json:"source"`
		}
		if !pluginDecode(w, r, &input, pluginPackageBodyLimit) {
			return
		}
		if !s.plugins.Can(principal, plugin.PermInstall) {
			pluginFail(w, r, plugin.Fail(plugin.CodePermissionDenied, "仅交互式管理员可安装插件"))
			return
		}
		candidate, err := s.resolvePackageCandidate(r.Context(), input.Source, false)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		preview, err := s.plugins.PreviewPackage(r.Context(), principal, candidate)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, preview)
	case "install":
		var input struct {
			Source         PackageSourceInput `json:"source"`
			ExpectedSHA256 string             `json:"expected_sha256"`
			Confirm        bool               `json:"confirm"`
		}
		if !pluginDecode(w, r, &input, pluginPackageBodyLimit) {
			return
		}
		if !input.Confirm {
			pluginFail(w, r, plugin.Fail(plugin.CodeInvalidArgument, "confirm is required"))
			return
		}
		if !s.plugins.Can(principal, plugin.PermInstall) {
			pluginFail(w, r, plugin.Fail(plugin.CodePermissionDenied, "仅交互式管理员可安装插件"))
			return
		}
		candidate, err := s.resolvePackageCandidate(r.Context(), input.Source, true)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		result, err := s.plugins.InstallPackage(r.Context(), principal, candidate, input.ExpectedSHA256)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		s.publishRealtime("plugins")
		pluginOK(w, r, http.StatusOK, result)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) apiV1PluginInstance(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/plugin-instances/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := pluginIDPart(parts[0])
	if err != nil {
		pluginFail(w, r, err)
		return
	}
	ctx := r.Context()
	respond := func(value any, err error) {
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		if r.Method != http.MethodGet {
			s.publishRealtime("plugins")
		}
		pluginOK(w, r, http.StatusOK, value)
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			respond(s.plugins.GetInstance(ctx, principal, id))
		case http.MethodPatch:
			var input plugin.InstanceUpdate
			if !pluginDecode(w, r, &input, 1<<16) {
				return
			}
			respond(s.plugins.UpdateInstance(ctx, principal, id, input))
		case http.MethodDelete:
			respond(map[string]any{"deleted": true}, s.plugins.DeleteInstance(ctx, principal, id))
		default:
			method(w)
		}
		return
	}
	switch {
	case parts[1] == "environment" && r.Method == http.MethodPut:
		var input struct {
			ExpectedRevision int64                      `json:"expected_revision"`
			Values           map[string]json.RawMessage `json:"values"`
			Custom           []plugin.CustomVar         `json:"custom"`
		}
		if !pluginDecode(w, r, &input, 1<<20) {
			return
		}
		respond(s.plugins.SaveEnvironment(ctx, principal, id, input.ExpectedRevision, plugin.EnvironmentInput{Values: input.Values, Custom: input.Custom}))
	case parts[1] == "secrets" && len(parts) == 3 && r.Method == http.MethodPut:
		var input struct {
			Value string `json:"value"`
		}
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		err := s.plugins.SetSecret(ctx, principal, id, parts[2], input.Value)
		respond(map[string]any{"configured": input.Value != ""}, err)
	case parts[1] == "grant" && r.Method == http.MethodPut:
		var input struct {
			ExpectedRevision int64        `json:"expected_revision"`
			Grant            plugin.Grant `json:"grant"`
		}
		if !pluginDecode(w, r, &input, 1<<18) {
			return
		}
		respond(s.plugins.SetGrant(ctx, principal, id, input.ExpectedRevision, input.Grant))
	case parts[1] == "grant" && r.Method == http.MethodDelete:
		respond(map[string]any{"revoked": true}, s.plugins.RevokeGrant(ctx, principal, id))
	case parts[1] == "runs" && r.Method == http.MethodPost:
		var input struct {
			IdempotencyKey string `json:"idempotency_key"`
		}
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		run, err := s.plugins.RunManually(ctx, principal, id, input.IdempotencyKey)
		respond(map[string]any{"run": run}, err)
	case parts[1] == "runs" && r.Method == http.MethodGet:
		runs, err := s.plugins.ListRuns(ctx, principal, store.PluginRunFilter{InstanceID: id, BeforeID: int64(intQuery(r, "before_id", 0)), Limit: intQuery(r, "limit", 50)})
		respond(map[string]any{"runs": runs}, err)
	case parts[1] == "schedules" && r.Method == http.MethodPost:
		var input plugin.ScheduleInput
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		respond(s.plugins.CreateSchedule(ctx, principal, id, input))
	case parts[1] == "state" && r.Method == http.MethodGet:
		entries, usage, err := s.plugins.ListState(ctx, principal, id)
		respond(map[string]any{"entries": entries, "usage": usage}, err)
	case parts[1] == "state" && r.Method == http.MethodDelete:
		respond(map[string]any{"deleted": true}, s.plugins.ClearState(ctx, principal, id, r.URL.Query().Get("key")))
	case parts[1] == "audit" && r.Method == http.MethodGet:
		events, err := s.plugins.ListAudit(ctx, principal, id, int64(intQuery(r, "before_id", 0)), intQuery(r, "limit", 100))
		respond(map[string]any{"events": events}, err)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) apiV1PluginSchedule(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/plugin-schedules/")
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	id, err := pluginIDPart(parts[0])
	if err != nil {
		pluginFail(w, r, err)
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var input plugin.ScheduleInput
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		item, err := s.plugins.UpdateSchedule(r.Context(), principal, id, input)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, item)
	case http.MethodDelete:
		if err := s.plugins.DeleteSchedule(r.Context(), principal, id); err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"deleted": true})
	default:
		method(w)
	}
}

func (s *Server) apiV1PluginRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	principal, _ := apiPrincipal(r)
	runs, err := s.plugins.ListRuns(r.Context(), principal, store.PluginRunFilter{InstallationID: int64(intQuery(r, "plugin_id", 0)), InstanceID: int64(intQuery(r, "instance_id", 0)), BeforeID: int64(intQuery(r, "before_id", 0)), Limit: intQuery(r, "limit", 50)})
	if err != nil {
		pluginFail(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, map[string]any{"runs": runs})
}

func (s *Server) apiV1PluginRun(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	parts := pathParts(r.URL.Path, "/api/v1/plugin-runs/")
	if len(parts) == 0 {
		http.NotFound(w, r)
		return
	}
	id, err := pluginIDPart(parts[0])
	if err != nil {
		pluginFail(w, r, err)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		run, err := s.plugins.GetRun(r.Context(), principal, id)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"run": run})
	case len(parts) == 2 && parts[1] == "logs" && r.Method == http.MethodGet:
		logs, err := s.plugins.ListRunLogs(r.Context(), principal, id, int64(intQuery(r, "after_seq", 0)))
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, map[string]any{"logs": logs})
	case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
		run, err := s.plugins.CancelRun(r.Context(), principal, id)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		s.publishRealtime("plugins")
		pluginOK(w, r, http.StatusOK, map[string]any{"run": run})
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) apiV1PluginRuntime(w http.ResponseWriter, r *http.Request) {
	principal, _ := apiPrincipal(r)
	switch r.Method {
	case http.MethodGet:
		status, err := s.plugins.RuntimeStatus(r.Context(), principal)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		pluginOK(w, r, http.StatusOK, status)
	case http.MethodPatch:
		var input plugin.SettingsUpdate
		if !pluginDecode(w, r, &input, 1<<16) {
			return
		}
		status, err := s.plugins.UpdateSettings(r.Context(), principal, input)
		if err != nil {
			pluginFail(w, r, err)
			return
		}
		s.plugins.Wake()
		pluginOK(w, r, http.StatusOK, status)
	default:
		method(w)
	}
}

// registerPluginAutomationOperations exposes the MCP-enabled executable
// plugin capabilities through validated Changesets.
func (s *Server) registerPluginAutomationOperations() {
	register := func(name string, apply func(context.Context, application.Principal, json.RawMessage) (any, error)) {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			var probe map[string]json.RawMessage
			if err := json.Unmarshal(input, &probe); err != nil {
				return nil, err
			}
			return map[string]any{"accepted": true}, nil
		})
		s.automation.Register(name, apply)
	}
	idOf := func(raw string) (int64, error) { return pluginIDPart(raw) }
	register("plugin_instances.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			PluginID string `json:"plugin_id"`
			Name     string `json:"name"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		id, err := idOf(in.PluginID)
		if err != nil {
			return nil, err
		}
		return s.plugins.CreateInstance(ctx, principal, id, in.Name)
	})
	register("plugin_instances.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			InstanceID string  `json:"instance_id"`
			Name       *string `json:"name"`
			Resume     bool    `json:"resume"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		id, err := idOf(in.InstanceID)
		if err != nil {
			return nil, err
		}
		return s.plugins.UpdateInstance(ctx, principal, id, plugin.InstanceUpdate{Name: in.Name, Resume: in.Resume})
	})
	register("plugin_instances.environment.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			InstanceID       string                     `json:"instance_id"`
			ExpectedRevision int64                      `json:"expected_revision"`
			Values           map[string]json.RawMessage `json:"values"`
			Custom           []plugin.CustomVar         `json:"custom"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		id, err := idOf(in.InstanceID)
		if err != nil {
			return nil, err
		}
		return s.plugins.SaveEnvironment(ctx, principal, id, in.ExpectedRevision, plugin.EnvironmentInput{Values: in.Values, Custom: in.Custom})
	})
	register("plugin_instances.run", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			InstanceID     string `json:"instance_id"`
			IdempotencyKey string `json:"idempotency_key"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		id, err := idOf(in.InstanceID)
		if err != nil {
			return nil, err
		}
		run, err := s.plugins.RunManually(ctx, principal, id, in.IdempotencyKey)
		return map[string]any{"run": run}, err
	})
	register("plugin_runs.cancel", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			RunID string `json:"run_id"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		id, err := idOf(in.RunID)
		if err != nil {
			return nil, err
		}
		run, err := s.plugins.CancelRun(ctx, principal, id)
		return map[string]any{"run": run}, err
	})
	scheduleInput := func(input json.RawMessage, idField string) (int64, plugin.ScheduleInput, error) {
		var in map[string]json.RawMessage
		if err := json.Unmarshal(input, &in); err != nil {
			return 0, plugin.ScheduleInput{}, err
		}
		var rawID string
		_ = json.Unmarshal(in[idField], &rawID)
		delete(in, idField)
		id, err := idOf(rawID)
		if err != nil {
			return 0, plugin.ScheduleInput{}, err
		}
		rest, _ := json.Marshal(in)
		var schedule plugin.ScheduleInput
		if err := strictAutomationInput(rest, &schedule); err != nil {
			return 0, plugin.ScheduleInput{}, err
		}
		return id, schedule, nil
	}
	register("plugin_schedules.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		id, schedule, err := scheduleInput(input, "instance_id")
		if err != nil {
			return nil, err
		}
		return s.plugins.CreateSchedule(ctx, principal, id, schedule)
	})
	register("plugin_schedules.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		id, schedule, err := scheduleInput(input, "schedule_id")
		if err != nil {
			return nil, err
		}
		return s.plugins.UpdateSchedule(ctx, principal, id, schedule)
	})
	register("plugin_schedules.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			ScheduleID string `json:"schedule_id"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		id, err := idOf(in.ScheduleID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true}, s.plugins.DeleteSchedule(ctx, principal, id)
	})
	register("plugin_instances.state.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			InstanceID string `json:"instance_id"`
			Key        string `json:"key"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		id, err := idOf(in.InstanceID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true}, s.plugins.ClearState(ctx, principal, id, in.Key)
	})
	register("plugins.drafts.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			Manifest json.RawMessage `json:"manifest"`
			Source   string          `json:"source"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		installation, err := s.plugins.CreateDraftPlugin(ctx, principal, plugin.DraftInput{Manifest: in.Manifest, Source: in.Source})
		return map[string]any{"plugin_id": strconv.FormatInt(installation.ID, 10)}, err
	})
	register("plugins.drafts.save", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var in struct {
			PluginID string          `json:"plugin_id"`
			Manifest json.RawMessage `json:"manifest"`
			Source   string          `json:"source"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		id, err := idOf(in.PluginID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"saved": true}, s.plugins.SaveDraft(ctx, principal, id, plugin.DraftInput{Manifest: in.Manifest, Source: in.Source})
	})
}

// queryPluginCapability answers the read-only MCP plugin capabilities.
func (s *Server) queryPluginCapability(ctx context.Context, principal application.Principal, name string, input json.RawMessage) (any, error) {
	decodeID := func(field string) (int64, error) {
		var values map[string]json.RawMessage
		if err := json.Unmarshal(orEmptyJSONObject(input), &values); err != nil {
			return 0, err
		}
		var raw string
		_ = json.Unmarshal(values[field], &raw)
		return pluginIDPart(raw)
	}
	optionalID := func(values map[string]json.RawMessage, field string) int64 {
		var raw string
		if json.Unmarshal(values[field], &raw) != nil || raw == "" {
			return 0
		}
		id, _ := strconv.ParseInt(raw, 10, 64)
		return id
	}
	switch name {
	case "plugins.list":
		items, err := s.plugins.ListInstallations(ctx, principal)
		return map[string]any{"plugins": items}, err
	case "plugins.get":
		id, err := decodeID("plugin_id")
		if err != nil {
			return nil, err
		}
		return s.plugins.GetInstallation(ctx, principal, id)
	case "plugins.catalog":
		if !s.plugins.Can(principal, plugin.PermRead) {
			return nil, plugin.ErrPermissionDenied
		}
		return pluginCatalogView(), nil
	case "plugins.runtime.status":
		return s.plugins.RuntimeStatus(ctx, principal)
	case "plugins.servers":
		servers, err := s.plugins.ServerOptions(ctx, principal)
		return map[string]any{"servers": servers}, err
	case "plugins.drafts.diagnose":
		var in struct {
			Manifest json.RawMessage `json:"manifest"`
			Source   string          `json:"source"`
		}
		if err := strictAutomationInput(input, &in); err != nil {
			return nil, err
		}
		return s.plugins.Diagnose(ctx, principal, plugin.DraftInput{Manifest: in.Manifest, Source: in.Source}, pluginpackage.CheckSource)
	case "plugin_instances.get":
		id, err := decodeID("instance_id")
		if err != nil {
			return nil, err
		}
		return s.plugins.GetInstance(ctx, principal, id)
	case "plugin_instances.state":
		id, err := decodeID("instance_id")
		if err != nil {
			return nil, err
		}
		entries, usage, err := s.plugins.ListState(ctx, principal, id)
		return map[string]any{"entries": entries, "usage": usage}, err
	case "plugin_instances.audit":
		var values map[string]json.RawMessage
		_ = json.Unmarshal(orEmptyJSONObject(input), &values)
		id, err := decodeID("instance_id")
		if err != nil {
			return nil, err
		}
		var limit int
		_ = json.Unmarshal(values["limit"], &limit)
		events, err := s.plugins.ListAudit(ctx, principal, id, optionalID(values, "before_id"), limit)
		return map[string]any{"events": events}, err
	case "plugin_runs.list":
		var values map[string]json.RawMessage
		_ = json.Unmarshal(orEmptyJSONObject(input), &values)
		var limit int
		_ = json.Unmarshal(values["limit"], &limit)
		runs, err := s.plugins.ListRuns(ctx, principal, store.PluginRunFilter{InstallationID: optionalID(values, "plugin_id"), InstanceID: optionalID(values, "instance_id"), BeforeID: optionalID(values, "before_id"), Limit: limit})
		return map[string]any{"runs": runs}, err
	case "plugin_runs.get":
		id, err := decodeID("run_id")
		if err != nil {
			return nil, err
		}
		run, err := s.plugins.GetRun(ctx, principal, id)
		return map[string]any{"run": run}, err
	case "plugin_runs.logs":
		id, err := decodeID("run_id")
		if err != nil {
			return nil, err
		}
		var values map[string]json.RawMessage
		_ = json.Unmarshal(orEmptyJSONObject(input), &values)
		var after int64
		_ = json.Unmarshal(values["after_seq"], &after)
		logs, err := s.plugins.ListRunLogs(ctx, principal, id, after)
		return map[string]any{"logs": logs}, err
	default:
		return nil, errors.New("unsupported query capability")
	}
}

func orEmptyJSONObject(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
