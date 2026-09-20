package plugin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginpackage"
	"github.com/OboardProject/oboard/internal/pluginui"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/dop251/goja"
)

var packageIdentifier = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
var packageVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
var packageCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// PackageSource is trusted fetch provenance, never evidence supplied by a ZIP.
type PackageSource = model.PluginPackageSource

type validatedPackage struct {
	archive      *pluginpackage.Package
	metadata     model.PluginPackageMetadata
	manifest     model.PluginManifest
	configSchema json.RawMessage
}

func parsePackageManifest(raw json.RawMessage) (model.PluginPackageMetadata, model.PluginManifest, json.RawMessage, error) {
	var metadata model.PluginPackageMetadata
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return metadata, model.PluginManifest{}, nil, Coded(codeInvalidInput, "package manifest must be an object")
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return metadata, model.PluginManifest{}, nil, Coded(codeInvalidInput, "invalid package metadata")
	}
	if len(metadata.PluginID) > 128 || !packageIdentifier.MatchString(metadata.PluginID) || strings.TrimSpace(metadata.Name) == "" || len(metadata.Name) > 80 || strings.TrimSpace(metadata.Description) == "" || len(metadata.Description) > 2000 || len(metadata.Version) > 128 || !packageVersion.MatchString(metadata.Version) {
		return metadata, model.PluginManifest{}, nil, Coded(codeInvalidInput, "package requires valid plugin_id, name, version and description")
	}
	if _, ok := fields["ui_content_sha256"]; ok {
		return metadata, model.PluginManifest{}, nil, Coded(codeInvalidInput, "ui_content_sha256 is host-managed")
	}
	config, hasConfig := fields["config_schema"]
	manifest, err := ParseManifest(raw)
	if err != nil {
		return metadata, manifest, nil, err
	}
	if !hasConfig {
		config = manifest.Params
	}
	if err := validateParamSchema(config); err != nil {
		return metadata, manifest, nil, err
	}
	return metadata, manifest, config, nil
}
func validatePackage(data []byte) (validatedPackage, error) {
	var out validatedPackage
	archive, err := pluginpackage.Parse(data)
	if err != nil {
		return out, Coded(codeInvalidInput, err.Error())
	}
	metadata, manifest, config, err := parsePackageManifest(archive.Manifest)
	if err != nil {
		return out, err
	}
	if err := ValidateSource(string(archive.Source)); err != nil {
		return out, err
	}
	if _, err := goja.Compile("main.js", string(archive.Source), false); err != nil {
		return out, Coded(codeInvalidInput, "package JavaScript does not compile")
	}
	if _, err := pluginui.Parse(archive.UI); err != nil {
		return out, Coded(codeInvalidInput, err.Error())
	}
	if len(archive.UI) > 0 {
		sum := sha256.Sum256(archive.UI)
		manifest.UIContentSHA256 = hex.EncodeToString(sum[:])
	}
	return validatedPackage{archive, metadata, manifest, config}, nil
}

func (s *Service) requirePackagePermission(actor application.Principal, permission string) error {
	if actor.Type == model.APIPrincipalPlugin || s.rbac == nil || !s.rbac.Allows(actor.Role, permission) {
		return ErrPermissionDenied
	}
	if !actor.Interactive && actor.AccessLevel == "" && !actor.HasScope(permissionToScope(permission)) {
		return ErrPermissionDenied
	}
	return nil
}

func (s *Service) packageAccess(ctx context.Context, actor application.Principal, pluginID int64, permission string) error {
	if err := s.requirePackagePermission(actor, permission); err != nil {
		return err
	}
	if !actor.AllowsInt64("plugin_ids", pluginID) {
		return ErrPermissionDenied
	}
	item, err := s.store.GetPlugin(ctx, pluginID)
	if err != nil {
		return err
	}
	if actor.Role != model.RoleAdmin && (actor.UserID == nil || *actor.UserID != item.OwnerUserID) {
		return ErrPermissionDenied
	}
	return nil
}
func (s *Service) packagePreview(ctx context.Context, actor application.Principal, pkg validatedPackage) (model.PluginPackagePreview, error) {
	preview := model.PluginPackagePreview{Metadata: pkg.metadata, SHA256: pkg.archive.SHA256, SourceCompiled: true, HasUI: len(pkg.archive.UI) > 0}
	var previous []string
	installation, err := s.store.FindPluginInstallation(ctx, pkg.metadata.PluginID)
	if err == nil {
		if err = s.packageAccess(ctx, actor, installation.PluginID, "plugins.read"); err != nil {
			return preview, err
		}
		preview.ExistingPluginID, preview.ActiveRevisionID = installation.PluginID, installation.ActiveRevisionID
		if installation.ActiveRevisionID > 0 {
			version, err := s.store.GetPluginPackageVersion(ctx, installation.PluginID, installation.ActiveRevisionID)
			if err != nil {
				return preview, err
			}
			_, manifest, _, err := parsePackageManifest(version.ManifestJSON)
			if err != nil {
				return preview, err
			}
			previous = manifest.Capabilities
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return preview, err
	}
	preview.Capabilities = packageCapabilityDiff(previous, pkg.manifest.Capabilities)
	return preview, nil
}
func packageCapabilityDiff(previous, next []string) model.PluginCapabilityDiff {
	diff := model.PluginCapabilityDiff{Added: []string{}, Removed: []string{}, Unchanged: []string{}}
	before, after := map[string]bool{}, map[string]bool{}
	for _, v := range previous {
		before[v] = true
	}
	for _, v := range next {
		after[v] = true
	}
	for v := range after {
		if before[v] {
			diff.Unchanged = append(diff.Unchanged, v)
		} else {
			diff.Added = append(diff.Added, v)
		}
	}
	for v := range before {
		if !after[v] {
			diff.Removed = append(diff.Removed, v)
		}
	}
	sort.Strings(diff.Added)
	sort.Strings(diff.Removed)
	sort.Strings(diff.Unchanged)
	return diff
}

func (s *Service) PreviewPackage(ctx context.Context, actor application.Principal, data []byte) (model.PluginPackagePreview, error) {
	if err := s.requirePackagePermission(actor, "plugins.read"); err != nil {
		return model.PluginPackagePreview{}, err
	}
	pkg, err := validatePackage(data)
	if err != nil {
		return model.PluginPackagePreview{}, err
	}
	return s.packagePreview(ctx, actor, pkg)
}

func (s *Service) InstallPackage(ctx context.Context, actor application.Principal, data []byte, expectedSHA256 string, confirm bool, source PackageSource) (model.PluginPackageInstallResult, error) {
	var out model.PluginPackageInstallResult
	if err := s.requirePackagePermission(actor, "plugins.publish"); err != nil {
		return out, err
	}
	if !confirm {
		return out, Coded(codeInvalidInput, "confirm is required")
	}
	if actor.UserID == nil {
		return out, Coded(codePermissionDenied, "installation requires a user owner")
	}
	pkg, err := validatePackage(data)
	if err != nil {
		return out, err
	}
	if expectedSHA256 == "" || expectedSHA256 != pkg.archive.SHA256 {
		return out, ErrConflict
	}
	preview, err := s.packagePreview(ctx, actor, pkg)
	if err != nil {
		return out, err
	}
	if preview.ExistingPluginID > 0 {
		if err = s.packageAccess(ctx, actor, preview.ExistingPluginID, "plugins.publish"); err != nil {
			return out, err
		}
	} else if !actor.AllowsCreate("plugin") {
		return out, ErrPermissionDenied
	}
	if source.Kind == "" {
		source.Kind = "upload"
	}
	if source.Kind != "upload" && source.Kind != "github" {
		return out, Coded(codeInvalidInput, "invalid package source")
	}
	if source.Kind == "upload" && (source.Commit != "" || source.Repository != "") {
		return out, Coded(codeInvalidInput, "uploads cannot claim GitHub provenance")
	}
	if source.Kind == "github" && (!packageCommit.MatchString(source.Commit) || len(source.Repository) > 200 || !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(source.Repository)) {
		return out, Coded(codeInvalidInput, "GitHub source requires repository and immutable commit")
	}
	rev := model.PluginRevision{SchemaVersion: pkg.manifest.SchemaVersion, Runtime: pkg.manifest.Runtime, SDKVersion: pkg.manifest.SDKVersion, Source: string(pkg.archive.Source), SourceDigest: RevisionDigest(string(pkg.archive.Source), pkg.manifest), ManifestJSON: MustJSON(pkg.manifest), AuthorUserID: *actor.UserID}
	version := model.PluginPackageVersion{Version: pkg.metadata.Version, SHA256: pkg.archive.SHA256, ManifestJSON: pkg.archive.Manifest, UIJSON: pkg.archive.UI, SourceKind: source.Kind, SourceRepository: source.Repository, SourceCommit: source.Commit}
	out.Installation, out.Version, err = s.store.InstallPluginPackage(ctx, pkg.metadata, rev, version, preview.ExistingPluginID, preview.ActiveRevisionID)
	out.Capabilities = preview.Capabilities
	return out, packageStoreError(err)
}

func packageStoreError(err error) error {
	if errors.Is(err, store.ErrPluginPackageConflict) {
		return ErrConflict
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *Service) ListPackageVersions(ctx context.Context, actor application.Principal, pluginID int64) ([]model.PluginPackageVersion, error) {
	if err := s.packageAccess(ctx, actor, pluginID, "plugins.read"); err != nil {
		return nil, err
	}
	return s.store.ListPluginPackageVersions(ctx, pluginID)
}
func (s *Service) ActivatePackageVersion(ctx context.Context, actor application.Principal, pluginID, revisionID int64, confirm bool) (model.PluginInstallation, error) {
	installation, err := s.ValidatePackageActivation(ctx, actor, pluginID, revisionID, confirm)
	if err != nil {
		return model.PluginInstallation{}, err
	}
	if err = s.store.ActivatePluginPackageVersion(ctx, pluginID, revisionID, installation.UpdatedAt); err != nil {
		return model.PluginInstallation{}, packageStoreError(err)
	}
	return s.store.GetPluginInstallation(ctx, pluginID)
}

func (s *Service) ValidatePackageActivation(ctx context.Context, actor application.Principal, pluginID, revisionID int64, confirm bool) (model.PluginInstallation, error) {
	var empty model.PluginInstallation
	if err := s.packageAccess(ctx, actor, pluginID, "plugins.publish"); err != nil {
		return empty, err
	}
	if !confirm {
		return empty, Coded(codeInvalidInput, "confirm is required")
	}
	installation, err := s.store.GetPluginInstallation(ctx, pluginID)
	if err != nil {
		return empty, err
	}
	version, err := s.store.GetPluginPackageVersion(ctx, pluginID, revisionID)
	if err != nil {
		return empty, err
	}
	_, _, schema, err := parsePackageManifest(version.ManifestJSON)
	if err != nil {
		return empty, err
	}
	if err = ValidateParams(schema, installation.ConfigJSON); err != nil {
		return empty, err
	}
	_, manifest, _, err := parsePackageManifest(version.ManifestJSON)
	if err != nil {
		return empty, err
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(installation.ConfigJSON, &config) != nil {
		return empty, Coded(codeInvalidInput, "invalid saved config")
	}
	for _, secret := range manifest.Secrets {
		if _, ok := config[secret.Name]; ok {
			return empty, Coded(codeInvalidInput, "remove secret fields from non-sensitive config before activating")
		}
	}
	return installation, nil
}
func (s *Service) GetPackageConfig(ctx context.Context, actor application.Principal, pluginID int64) (model.PluginInstallation, error) {
	if err := s.packageAccess(ctx, actor, pluginID, "plugins.read"); err != nil {
		return model.PluginInstallation{}, err
	}
	return s.store.GetPluginInstallation(ctx, pluginID)
}
func (s *Service) UpdatePackageConfig(ctx context.Context, actor application.Principal, pluginID int64, config json.RawMessage) (model.PluginInstallation, error) {
	installation, err := s.ValidatePackageConfig(ctx, actor, pluginID, config)
	if err != nil {
		return model.PluginInstallation{}, err
	}
	if err = s.store.UpdatePluginInstallationConfig(ctx, pluginID, config, installation.UpdatedAt); err != nil {
		return model.PluginInstallation{}, packageStoreError(err)
	}
	return s.store.GetPluginInstallation(ctx, pluginID)
}

func (s *Service) ValidatePackageConfig(ctx context.Context, actor application.Principal, pluginID int64, config json.RawMessage) (model.PluginInstallation, error) {
	var empty model.PluginInstallation
	if err := s.packageAccess(ctx, actor, pluginID, "plugins.draft"); err != nil {
		return empty, err
	}
	if len(config) == 0 || len(config) > MaxParamsEnvBytes {
		return empty, Coded(codeInvalidInput, "config must be a JSON object of at most 64 KiB")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(config, &object) != nil || object == nil {
		return empty, Coded(codeInvalidInput, "config must be a JSON object")
	}
	installation, version, err := s.activePackage(ctx, pluginID)
	if err != nil {
		return empty, err
	}
	_, manifest, schema, err := parsePackageManifest(version.ManifestJSON)
	if err != nil {
		return empty, err
	}
	for _, secret := range manifest.Secrets {
		if _, ok := object[secret.Name]; ok {
			return empty, Coded(codeInvalidInput, "secrets must use the administrator secret endpoint")
		}
	}
	if err = ValidateParams(schema, config); err != nil {
		return empty, err
	}
	return installation, nil
}
func (s *Service) activePackage(ctx context.Context, pluginID int64) (model.PluginInstallation, model.PluginPackageVersion, error) {
	installation, err := s.store.GetPluginInstallation(ctx, pluginID)
	if err != nil {
		return installation, model.PluginPackageVersion{}, err
	}
	if !installation.Installed || installation.ActiveRevisionID == 0 {
		return installation, model.PluginPackageVersion{}, ErrNotFound
	}
	version, err := s.store.GetPluginPackageVersion(ctx, pluginID, installation.ActiveRevisionID)
	return installation, version, err
}
func (s *Service) GetPackageUI(ctx context.Context, actor application.Principal, pluginID int64) (json.RawMessage, error) {
	if err := s.packageAccess(ctx, actor, pluginID, "plugins.read"); err != nil {
		return nil, err
	}
	_, version, err := s.activePackage(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	return version.UIJSON, nil
}

// SetPackageSecretEncrypted is administrator-only and must not be exposed to MCP.
func (s *Service) SetPackageSecretEncrypted(ctx context.Context, actor application.Principal, pluginID int64, name, encrypted string) error {
	if !actor.Interactive || actor.Type != model.APIPrincipalOAuth || actor.ClientName != "oboard-web" {
		return ErrPermissionDenied
	}
	if err := s.requireAdmin(actor, "plugins.authorize"); err != nil {
		return err
	}
	if err := s.packageAccess(ctx, actor, pluginID, "plugins.authorize"); err != nil {
		return err
	}
	if len(encrypted) > 128<<10 {
		return Coded(codeLimitExceeded, "secret exceeds size limit")
	}
	installation, version, err := s.activePackage(ctx, pluginID)
	if err != nil {
		return err
	}
	_, manifest, _, err := parsePackageManifest(version.ManifestJSON)
	if err != nil {
		return err
	}
	declared := false
	for _, secret := range manifest.Secrets {
		if secret.Name == name {
			declared = true
			break
		}
	}
	if !declared {
		return Coded(codeInvalidInput, "secret is not declared")
	}
	return packageStoreError(s.store.SetPluginInstallationSecret(ctx, pluginID, name, encrypted, installation.UpdatedAt))
}
func (s *Service) UninstallPackage(ctx context.Context, actor application.Principal, pluginID int64, keepState, confirm bool) error {
	if !actor.AllowsDestructiveOperations() {
		return ErrPermissionDenied
	}
	for _, permission := range []string{"plugins.draft", "plugins.triggers", "plugins.cancel"} {
		if err := s.packageAccess(ctx, actor, pluginID, permission); err != nil {
			return err
		}
	}
	if !confirm {
		return Coded(codeInvalidInput, "confirm is required")
	}
	return packageStoreError(s.store.UninstallPluginPackage(ctx, pluginID, keepState))
}
