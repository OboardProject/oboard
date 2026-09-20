package controller

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginpackage"
	"github.com/OboardProject/oboard/internal/security"
)

// PackageZIP is base64 in JSON. Upload provenance is fixed by the controller;
// callers cannot claim a repository or commit for arbitrary uploaded bytes.
type pluginPackageUploadInput struct {
	PackageZIP     []byte `json:"package_zip"`
	ExpectedSHA256 string `json:"expected_sha256"`
	Confirm        bool   `json:"confirm"`
}

func (s *Server) registerPluginPackageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/plugins/packages/preview", s.apiAuth(s.apiV1PluginPackagePreview, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugins/packages/install", s.apiAuth(s.apiV1PluginPackageInstall, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugins/github/preview", s.apiAuth(s.apiV1PluginGitHubPreview, model.RoleOperator))
	mux.HandleFunc("/api/v1/plugins/github/install", s.apiAuth(s.apiV1PluginGitHubInstall, model.RoleOperator))
	for _, suffix := range []string{"versions", "versions/activate", "config", "ui", "uninstall"} {
		mux.HandleFunc("/api/v1/plugins/{id}/"+suffix, s.apiAuth(s.apiV1PluginPackageItem, model.RoleOperator))
	}
	mux.HandleFunc("/api/v1/plugins/{id}/secrets/{name}", s.apiAuth(s.apiV1PluginPackageSecret, model.RoleAdmin))
}

func (s *Server) apiV1PluginPackagePreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, pluginpackage.MaxUploadSize*4/3+4096)
	var input pluginPackageUploadInput
	if !decodeV2(w, r, &input) {
		return
	}
	actor, _ := apiPrincipal(r)
	result, err := s.plugins.PreviewPackage(r.Context(), actor, input.PackageZIP)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, result)
}

func (s *Server) apiV1PluginPackageInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, pluginpackage.MaxUploadSize*4/3+4096)
	var input pluginPackageUploadInput
	if !decodeV2(w, r, &input) {
		return
	}
	result, err := s.applyPluginPackageWrite(r, "plugins.packages.install", input)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, result)
}

// setPluginPackageSecret is called only by the authenticated Web administrator
// write endpoint. An empty value deletes a secret; no secret read API exists.
func (s *Server) setPluginPackageSecret(ctx context.Context, actor application.Principal, pluginID int64, name, value string) error {
	if actor.Role != model.RoleAdmin || !actor.Interactive || actor.Type != model.APIPrincipalOAuth || actor.ClientName != "oboard-web" {
		return plugin.ErrPermissionDenied
	}
	if len(value) > 64<<10 {
		return plugin.Coded(model.PluginErrorLimitExceeded, "secret exceeds 64 KiB")
	}
	encrypted := ""
	if value != "" {
		var err error
		encrypted, err = security.EncryptSecret(s.sessionSecret, "plugin-secret", value)
		if err != nil {
			return err
		}
	}
	return s.plugins.SetPackageSecretEncrypted(ctx, actor, pluginID, name, encrypted)
}

type pluginGitHubInput struct {
	RepositoryURL  string `json:"repository_url"`
	Ref            string `json:"ref,omitempty"`
	Commit         string `json:"commit,omitempty"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty"`
	Confirm        bool   `json:"confirm,omitempty"`
}

func (s *Server) apiV1PluginGitHubPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input pluginGitHubInput
	if !decodeV2(w, r, &input) {
		return
	}
	actor, _ := apiPrincipal(r)
	result, err := s.plugins.PreviewGitHubPackage(r.Context(), actor, input.RepositoryURL, input.Ref)
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, result)
}
func (s *Server) apiV1PluginGitHubInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var input pluginGitHubInput
	if !decodeV2(w, r, &input) {
		return
	}
	if input.Ref != "" {
		pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, "installation requires commit, not ref"))
		return
	}
	result, err := s.applyPluginPackageWrite(r, "plugins.github.install", map[string]any{"repository_url": input.RepositoryURL, "commit": input.Commit, "expected_sha256": input.ExpectedSHA256, "confirm": input.Confirm})
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, result)
}
func (s *Server) apiV1PluginPackageItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, "invalid plugin id"))
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/plugins/")
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	actor, _ := apiPrincipal(r)
	var result any
	switch parts[1] {
	case "versions":
		if len(parts) == 2 && r.Method == http.MethodGet {
			var versions []model.PluginPackageVersion
			versions, err = s.plugins.ListPackageVersions(r.Context(), actor, id)
			result = map[string]any{"versions": versions}
		} else if len(parts) == 3 && parts[2] == "activate" && r.Method == http.MethodPost {
			var in struct {
				RevisionID int64 `json:"revision_id"`
				Confirm    bool  `json:"confirm"`
			}
			if !decodeV2(w, r, &in) {
				return
			}
			result, err = s.applyPluginPackageWrite(r, "plugins.versions.activate", map[string]any{"plugin_id": strconv.FormatInt(id, 10), "revision_id": strconv.FormatInt(in.RevisionID, 10), "confirm": in.Confirm})
		} else {
			method(w)
			return
		}
	case "config":
		var installation model.PluginInstallation
		if r.Method == http.MethodGet {
			installation, err = s.plugins.GetPackageConfig(r.Context(), actor, id)
		} else if r.Method == http.MethodPut {
			var in struct {
				Config json.RawMessage `json:"config"`
			}
			if !decodeV2(w, r, &in) {
				return
			}
			result, err = s.applyPluginPackageWrite(r, "plugins.config.update", map[string]any{"plugin_id": strconv.FormatInt(id, 10), "config": in.Config})
		} else {
			method(w)
			return
		}
		if r.Method == http.MethodGet {
			result = map[string]any{"installation": installation}
		}
	case "ui":
		if r.Method != http.MethodGet {
			method(w)
			return
		}
		var ui json.RawMessage
		ui, err = s.plugins.GetPackageUI(r.Context(), actor, id)
		result = map[string]any{"ui": ui}
	case "uninstall":
		if r.Method != http.MethodPost {
			method(w)
			return
		}
		var in struct {
			KeepState *bool `json:"keep_state"`
			Confirm   bool  `json:"confirm"`
		}
		if !decodeV2(w, r, &in) {
			return
		}
		if in.KeepState == nil {
			pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, "keep_state is required"))
			return
		}
		result, err = s.applyPluginPackageWrite(r, "plugins.uninstall", map[string]any{"plugin_id": strconv.FormatInt(id, 10), "keep_state": *in.KeepState, "confirm": in.Confirm})
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, result)
}
func (s *Server) apiV1PluginPackageSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		method(w)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		pluginErr(w, r, plugin.Coded(model.PluginErrorInvalidInput, "invalid plugin id"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	var in struct {
		Value string `json:"value"`
	}
	if !decodeV2(w, r, &in) {
		return
	}
	actor, _ := apiPrincipal(r)
	if err = s.setPluginPackageSecret(r.Context(), actor, id, r.PathValue("name"), in.Value); err != nil {
		pluginErr(w, r, err)
		return
	}
	pluginOK(w, r, http.StatusOK, map[string]any{"saved": true})
}

// Web writes use the same validated, policy-checked Changeset handlers as MCP.
// Machine callers submit a Changeset explicitly rather than gaining Web approval.
func (s *Server) applyPluginPackageWrite(r *http.Request, name string, input any) (any, error) {
	actor, _ := apiPrincipal(r)
	if !actor.Interactive || actor.Type != model.APIPrincipalOAuth || actor.ClientName != "oboard-web" {
		return nil, plugin.ErrPermissionDenied
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	operations := []automation.OperationRequest{{Capability: name, Input: raw}}
	ctx := r.Context()
	draft, err := s.automation.ValidateDraft(ctx, actor, automation.DraftValidationRequest{Operations: operations})
	if err != nil {
		return nil, err
	}
	base, err := json.Marshal(draft.ExpectedRevisions)
	if err != nil {
		return nil, err
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = rand.Text()
	}
	item, err := s.automation.Create(ctx, actor, automation.CreateRequest{Reason: "插件包管理", IdempotencyKey: "plugin-package:" + key, BaseRevisions: base, Operations: operations})
	if err != nil {
		return nil, err
	}
	if item.Status != model.ChangesetSucceeded {
		item, err = s.automation.Validate(ctx, actor, item.ID)
		if err != nil {
			return nil, err
		}
		if item.Status == model.ChangesetAwaitingApproval {
			item, err = s.automation.Approve(ctx, actor, item.ID, "面板确认插件包操作")
			if err != nil {
				return nil, err
			}
		}
		if item.Status == model.ChangesetApproved {
			item, err = s.applyAutomationChangeset(ctx, actor, item.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	if item.Status != model.ChangesetSucceeded || len(item.Operations) != 1 {
		return nil, plugin.Coded(model.PluginErrorApprovalRequired, "plugin Changeset did not succeed")
	}
	return item.Operations[0].Result, nil
}

type pluginPackageCapabilityInput struct {
	PluginID       int64           `json:"plugin_id,string"`
	RevisionID     int64           `json:"revision_id,string"`
	PackageZIP     []byte          `json:"package_zip"`
	RepositoryURL  string          `json:"repository_url"`
	Ref            string          `json:"ref"`
	Commit         string          `json:"commit"`
	ExpectedSHA256 string          `json:"expected_sha256"`
	Confirm        bool            `json:"confirm"`
	KeepState      *bool           `json:"keep_state"`
	Config         json.RawMessage `json:"config"`
}

func (s *Server) queryPluginPackageCapability(ctx context.Context, actor application.Principal, name string, input json.RawMessage) (any, error) {
	var in pluginPackageCapabilityInput
	if err := strictAutomationInput(input, &in); err != nil {
		return nil, err
	}
	switch name {
	case "plugins.packages.preview":
		return s.plugins.PreviewPackage(ctx, actor, in.PackageZIP)
	case "plugins.github.preview":
		return s.plugins.PreviewGitHubPackage(ctx, actor, in.RepositoryURL, in.Ref)
	case "plugins.versions.list":
		items, err := s.plugins.ListPackageVersions(ctx, actor, in.PluginID)
		return map[string]any{"versions": items}, err
	case "plugins.config.get":
		item, err := s.plugins.GetPackageConfig(ctx, actor, in.PluginID)
		return map[string]any{"installation": item}, err
	case "plugins.ui.get":
		ui, err := s.plugins.GetPackageUI(ctx, actor, in.PluginID)
		return map[string]any{"ui": ui}, err
	default:
		return nil, errors.New("unsupported package query capability")
	}
}
func (s *Server) registerPluginPackageAutomationOperations() {
	for _, name := range []string{"plugins.packages.install", "plugins.github.install", "plugins.versions.activate", "plugins.config.update", "plugins.uninstall"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, actor application.Principal, input json.RawMessage) (any, error) {
			var in pluginPackageCapabilityInput
			if err := strictAutomationInput(input, &in); err != nil {
				return nil, err
			}
			if name == "plugins.packages.install" {
				preview, err := s.plugins.PreviewPackage(ctx, actor, in.PackageZIP)
				if err != nil {
					return nil, err
				}
				if !in.Confirm {
					return nil, plugin.Coded(model.PluginErrorInvalidInput, "confirm is required")
				}
				if in.ExpectedSHA256 != preview.SHA256 {
					return nil, plugin.ErrConflict
				}
				return preview, nil
			}
			if name == "plugins.github.install" {
				if in.Ref != "" || !in.Confirm || len(in.Commit) != 40 {
					return nil, plugin.Coded(model.PluginErrorInvalidInput, "immutable commit and confirm required")
				}
				preview, err := s.plugins.PreviewGitHubPackage(ctx, actor, in.RepositoryURL, in.Commit)
				if err != nil {
					return nil, err
				}
				if in.ExpectedSHA256 != preview.SHA256 || preview.Source == nil || preview.Source.Commit != in.Commit {
					return nil, plugin.ErrConflict
				}
				return preview, nil
			}
			if name == "plugins.versions.activate" {
				return s.plugins.ValidatePackageActivation(ctx, actor, in.PluginID, in.RevisionID, in.Confirm)
			}
			if name == "plugins.config.update" {
				return s.plugins.ValidatePackageConfig(ctx, actor, in.PluginID, in.Config)
			}
			if name == "plugins.uninstall" {
				if in.KeepState == nil || !in.Confirm {
					return nil, plugin.Coded(model.PluginErrorInvalidInput, "keep_state and confirm are required")
				}
				if !actor.AllowsDestructiveOperations() {
					return nil, plugin.ErrPermissionDenied
				}
			}
			installation, err := s.plugins.GetPackageConfig(ctx, actor, in.PluginID)
			return map[string]any{"installation": installation}, err
		})
		s.automation.Register(name, func(ctx context.Context, actor application.Principal, input json.RawMessage) (any, error) {
			var in pluginPackageCapabilityInput
			if err := strictAutomationInput(input, &in); err != nil {
				return nil, err
			}
			switch name {
			case "plugins.packages.install":
				return s.plugins.InstallPackage(ctx, actor, in.PackageZIP, in.ExpectedSHA256, in.Confirm, plugin.PackageSource{Kind: "upload"})
			case "plugins.github.install":
				if in.Ref != "" {
					return nil, plugin.Coded(model.PluginErrorInvalidInput, "installation requires commit, not ref")
				}
				return s.plugins.InstallGitHubPackage(ctx, actor, in.RepositoryURL, in.Commit, in.ExpectedSHA256, in.Confirm)
			case "plugins.versions.activate":
				item, err := s.plugins.ActivatePackageVersion(ctx, actor, in.PluginID, in.RevisionID, in.Confirm)
				return map[string]any{"installation": item}, err
			case "plugins.config.update":
				item, err := s.plugins.UpdatePackageConfig(ctx, actor, in.PluginID, in.Config)
				return map[string]any{"installation": item}, err
			case "plugins.uninstall":
				if in.KeepState == nil {
					return nil, plugin.Coded(model.PluginErrorInvalidInput, "keep_state is required")
				}
				err := s.plugins.UninstallPackage(ctx, actor, in.PluginID, *in.KeepState, in.Confirm)
				return map[string]any{"uninstalled": err == nil}, err
			}
			return nil, errors.New("unsupported package capability")
		})
	}
}
