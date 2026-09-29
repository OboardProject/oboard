package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// PackageCandidate is a validated package plus where it came from. It is
// produced by pluginpackage/plugingithub/editor and never executed here.
type PackageCandidate struct {
	Manifest          Manifest
	Source            string
	Icon              []byte
	Readme            string
	License           string
	SHA256            string
	PublisherIdentity string
	PublisherName     string
	SignatureState    string
	SourceKind        string
	SourceRepository  string
	SourceCommit      string
}

// PackagePreview is shown before an administrator confirms an install or
// update. It lists every permission the version asks for and what changed.
type PackagePreview struct {
	PluginID          string                `json:"plugin_id"`
	Name              string                `json:"name"`
	Version           string                `json:"version"`
	Description       string                `json:"description"`
	SHA256            string                `json:"sha256"`
	PublisherIdentity string                `json:"publisher_identity"`
	PublisherName     string                `json:"publisher_name"`
	SignatureState    string                `json:"signature_state"`
	SourceKind        string                `json:"source_kind"`
	SourceRepository  string                `json:"source_repository,omitempty"`
	SourceCommit      string                `json:"source_commit,omitempty"`
	Manifest          Manifest              `json:"manifest"`
	Permissions       []PermissionLine      `json:"permissions"`
	HTTPHosts         []string              `json:"http_hosts"`
	Forbidden         []string              `json:"forbidden"`
	Existing          *ExistingInstallation `json:"existing,omitempty"`
	Diff              PermissionDiff        `json:"diff"`
	Action            string                `json:"action"`
	Blocked           string                `json:"blocked,omitempty"`
}

type ExistingInstallation struct {
	InstallationID    int64  `json:"installation_id"`
	Version           string `json:"version"`
	PublisherIdentity string `json:"publisher_identity"`
}

// PermissionLine is one human-readable permission for the review screen.
type PermissionLine struct {
	Capability  string `json:"capability"`
	Group       string `json:"group"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Risk        Risk   `json:"risk"`
	Resource    string `json:"resource,omitempty"`
}

func permissionLines(manifest Manifest) []PermissionLine {
	lines := []PermissionLine{}
	for _, name := range manifest.Capabilities {
		spec, ok := LookupCapability(name)
		if !ok {
			continue
		}
		lines = append(lines, PermissionLine{Capability: name, Group: spec.Group, Label: spec.Label, Description: spec.Description, Risk: spec.Risk, Resource: spec.Resource})
	}
	return lines
}

func (s *Service) PreviewPackage(ctx context.Context, actor application.Principal, candidate PackageCandidate) (PackagePreview, error) {
	if err := s.require(actor, PermInstall); err != nil {
		return PackagePreview{}, err
	}
	return s.preview(ctx, candidate)
}

func (s *Service) preview(ctx context.Context, candidate PackageCandidate) (PackagePreview, error) {
	m := candidate.Manifest
	preview := PackagePreview{
		PluginID: m.ID, Name: m.Name, Version: m.Version, Description: m.Description, SHA256: candidate.SHA256,
		PublisherIdentity: candidate.PublisherIdentity, PublisherName: candidate.PublisherName, SignatureState: candidate.SignatureState,
		SourceKind: candidate.SourceKind, SourceRepository: candidate.SourceRepository, SourceCommit: candidate.SourceCommit,
		Manifest: m, Permissions: permissionLines(m), HTTPHosts: []string{}, Forbidden: ForbiddenCapabilityGroups(), Action: "install",
	}
	if m.HTTP != nil {
		preview.HTTPHosts = append(preview.HTTPHosts, m.HTTP.Hosts...)
	}
	existing, err := s.store.GetPluginInstallationByKey(ctx, m.ID)
	switch {
	case notFound(err):
		preview.Diff = DiffPermissions(nil, m)
		return preview, nil
	case err != nil:
		return preview, storeError(err)
	}
	preview.Existing = &ExistingInstallation{InstallationID: existing.ID, PublisherIdentity: existing.PublisherIdentity}
	preview.Action = "update"
	var previous *Manifest
	if existing.ActivePackageID > 0 {
		if pkg, err := s.store.GetPluginPackage(ctx, existing.ActivePackageID); err == nil {
			preview.Existing.Version = pkg.Version
			if parsed, err := ParseManifest(pkg.ManifestJSON); err == nil {
				previous = &parsed
			}
			if pkg.SHA256 == candidate.SHA256 {
				preview.Blocked = "该版本已是当前版本"
			}
		}
	}
	preview.Diff = DiffPermissions(previous, m)
	if existing.PublisherIdentity != candidate.PublisherIdentity {
		preview.Blocked = "发布者身份与已安装插件不同。不同发布者的插件不能互相更新；如确需替换，请先卸载原插件。"
	}
	return preview, nil
}

// InstallResult reports the installation and whether new permissions now
// wait for an explicit review.
type InstallResult struct {
	InstallationID int64          `json:"installation_id"`
	PackageID      int64          `json:"package_id"`
	InstanceID     int64          `json:"instance_id,omitempty"`
	Action         string         `json:"action"`
	Diff           PermissionDiff `json:"diff"`
	ReviewRequired bool           `json:"review_required"`
}

// InstallPackage installs a new plugin (disabled, first instance ungranted)
// or updates an existing one from the same publisher. The confirmed digest
// must match the reviewed preview.
func (s *Service) InstallPackage(ctx context.Context, actor application.Principal, candidate PackageCandidate, expectedSHA256 string) (InstallResult, error) {
	if err := s.require(actor, PermInstall); err != nil {
		return InstallResult{}, err
	}
	if expectedSHA256 == "" || expectedSHA256 != candidate.SHA256 {
		return InstallResult{}, Fail(CodeConflict, "安装内容与预览不一致，请重新预览")
	}
	preview, err := s.preview(ctx, candidate)
	if err != nil {
		return InstallResult{}, err
	}
	if preview.Existing != nil && preview.Existing.PublisherIdentity != candidate.PublisherIdentity {
		return InstallResult{}, Fail(CodePublisherChanged, preview.Blocked)
	}
	pkg := model.PluginPackage{
		PluginKey: candidate.Manifest.ID, Version: candidate.Manifest.Version, SHA256: candidate.SHA256,
		ManifestJSON: candidate.Manifest.CanonicalJSON(), Source: candidate.Source, IconPNG: candidate.Icon,
		Readme: candidate.Readme, License: candidate.License, PublisherIdentity: candidate.PublisherIdentity,
		PublisherName: candidate.PublisherName, SignatureState: candidate.SignatureState, SourceKind: candidate.SourceKind,
		SourceRepository: candidate.SourceRepository, SourceCommit: candidate.SourceCommit, CreatedByUserID: actorUserID(actor),
	}
	if preview.Existing == nil {
		installation := model.PluginInstallation{PluginKey: candidate.Manifest.ID, Name: candidate.Manifest.Name, Description: candidate.Manifest.Description, PublisherIdentity: candidate.PublisherIdentity}
		instance, err := s.store.InstallPluginPackage(ctx, &installation, &pkg, "默认实例")
		if err != nil {
			return InstallResult{}, storeError(err)
		}
		return InstallResult{InstallationID: installation.ID, PackageID: pkg.ID, InstanceID: instance.ID, Action: "install", Diff: preview.Diff}, nil
	}
	if existing, err := s.store.GetPluginPackageBySHA(ctx, preview.Existing.InstallationID, candidate.SHA256); err == nil {
		pkg = existing
	}
	review, err := s.activate(ctx, preview.Existing.InstallationID, pkg, candidate.Manifest, false)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{InstallationID: preview.Existing.InstallationID, PackageID: pkg.ID, Action: "update", Diff: preview.Diff, ReviewRequired: review}, nil
}

// ActivateVersion switches to an already stored version (rollback or
// forward) with the same permission-diff rules as an update.
func (s *Service) ActivateVersion(ctx context.Context, actor application.Principal, installationID, packageID int64) (InstallResult, error) {
	if err := s.require(actor, PermInstall); err != nil {
		return InstallResult{}, err
	}
	pkg, err := s.store.GetPluginPackage(ctx, packageID)
	if err != nil || pkg.InstallationID != installationID {
		return InstallResult{}, ErrNotFound
	}
	manifest, err := ParseManifest(pkg.ManifestJSON)
	if err != nil {
		return InstallResult{}, err
	}
	installation, err := s.store.GetPluginInstallation(ctx, installationID)
	if err != nil {
		return InstallResult{}, storeError(err)
	}
	var previous *Manifest
	if current, err := s.store.GetPluginPackage(ctx, installation.ActivePackageID); err == nil {
		if parsed, err := ParseManifest(current.ManifestJSON); err == nil {
			previous = &parsed
		}
	}
	review, err := s.activate(ctx, installationID, pkg, manifest, false)
	if err != nil {
		return InstallResult{}, err
	}
	return InstallResult{InstallationID: installationID, PackageID: pkg.ID, Action: "activate", Diff: DiffPermissions(previous, manifest), ReviewRequired: review}, nil
}

// activate switches every instance to pkg. Grants are restricted to what the
// new manifest still declares; anything the new version adds is left
// ungranted and marks the instance for permission review. Removed variables
// are pruned so they are never delivered to the runtime again.
func (s *Service) activate(ctx context.Context, installationID int64, pkg model.PluginPackage, manifest Manifest, clearDraft bool) (bool, error) {
	installation, err := s.store.GetPluginInstallation(ctx, installationID)
	if err != nil {
		return false, storeError(err)
	}
	var previous *Manifest
	if installation.ActivePackageID > 0 {
		if current, err := s.store.GetPluginPackage(ctx, installation.ActivePackageID); err == nil {
			if parsed, err := ParseManifest(current.ManifestJSON); err == nil {
				previous = &parsed
			}
		}
	}
	diff := DiffPermissions(previous, manifest)
	instances, err := s.store.ListPluginInstances(ctx, installationID)
	if err != nil {
		return false, storeError(err)
	}
	activation := store.PluginActivation{InstallationID: installationID, Name: manifest.Name, Description: manifest.Description, ClearDraft: clearDraft, ExpectedPackage: installation.ActivePackageID}
	review := false
	for _, instance := range instances {
		values := map[string]json.RawMessage{}
		_ = json.Unmarshal(instance.ValuesJSON, &values)
		for name := range values {
			if _, declared := manifest.EnvField(name); !declared {
				delete(values, name)
			}
		}
		custom := []CustomVar{}
		_ = json.Unmarshal(instance.CustomJSON, &custom)
		kept := custom[:0]
		for _, item := range custom {
			if _, declared := manifest.EnvField(item.Name); !declared {
				kept = append(kept, item)
			}
		}
		item := store.PluginInstanceActivation{InstanceID: instance.ID, Values: mustMarshal(values), Custom: mustMarshal(kept), RequireReview: instance.PermissionReviewRequired}
		if grantRecord, err := s.store.GetPluginGrant(ctx, instance.ID); err == nil {
			grant, _ := ParseGrant(grantRecord.GrantJSON)
			item.HadGrant = true
			item.Grant = grant.RestrictTo(manifest).CanonicalJSON()
		}
		if diff.Expanded && previous != nil {
			item.RequireReview = true
			review = true
		}
		activation.Instances = append(activation.Instances, item)
	}
	if err := s.store.AddAndActivatePluginPackage(ctx, &pkg, activation); err != nil {
		return false, storeError(err)
	}
	return review, nil
}

// SetInstallationEnabled is the administrator switch for a whole plugin.
// Disabling cancels queued runs and stops running ones.
func (s *Service) SetInstallationEnabled(ctx context.Context, actor application.Principal, installationID int64, enabled bool) error {
	if err := s.require(actor, PermInstall); err != nil {
		return err
	}
	installation, err := s.store.GetPluginInstallation(ctx, installationID)
	if err != nil {
		return storeError(err)
	}
	if enabled && installation.ActivePackageID == 0 {
		return Fail(CodeConflict, "插件还没有已发布的版本")
	}
	if err := s.store.SetPluginInstallationEnabled(ctx, installationID, enabled); err != nil {
		return storeError(err)
	}
	if !enabled {
		return storeError(s.store.CancelPluginRunsForInstallation(ctx, installationID, "插件已停用"))
	}
	return nil
}

// Uninstall removes code, instances, configuration, grants, secrets, state
// and schedules at once and cancels unfinished runs. Run history and audit
// records are retained.
func (s *Service) Uninstall(ctx context.Context, actor application.Principal, installationID int64) error {
	if err := s.require(actor, PermInstall); err != nil {
		return err
	}
	return storeError(s.store.DeletePluginInstallation(ctx, installationID))
}

// --- editor ---

// DraftInput is an editor save. The draft is never executable; publishing
// turns it into an unsigned local package that still needs a grant.
type DraftInput struct {
	Manifest json.RawMessage `json:"manifest"`
	Source   string          `json:"source"`
}

// EditorDiagnostics are shown in the editor without executing plugin code.
type EditorDiagnostics struct {
	Valid       bool             `json:"valid"`
	Manifest    *Manifest        `json:"manifest,omitempty"`
	Issues      []FieldIssue     `json:"issues"`
	Undeclared  []string         `json:"undeclared_capabilities"`
	UnusedEnv   []string         `json:"unused_environment"`
	Permissions []PermissionLine `json:"permissions"`
}

// CreateDraftPlugin starts a new local plugin in the editor.
func (s *Service) CreateDraftPlugin(ctx context.Context, actor application.Principal, input DraftInput) (model.PluginInstallation, error) {
	if err := s.require(actor, PermDevelop); err != nil {
		return model.PluginInstallation{}, err
	}
	manifest, err := ParseManifest(input.Manifest)
	if err != nil {
		return model.PluginInstallation{}, err
	}
	if len(input.Source) > MaxSourceBytes {
		return model.PluginInstallation{}, FailField(CodeInvalidPackage, "main.js", "main.js exceeds 512 KiB")
	}
	installation := model.PluginInstallation{PluginKey: manifest.ID, Name: manifest.Name, Description: manifest.Description, PublisherIdentity: model.PluginPublisherLocal, DraftManifestJSON: input.Manifest, DraftSource: input.Source}
	if err := s.store.CreatePluginDraftInstallation(ctx, &installation); err != nil {
		if err == store.ErrPluginConflict {
			return installation, Fail(CodeConflict, "已存在相同 id 的插件")
		}
		return installation, storeError(err)
	}
	return installation, nil
}

// SaveDraft stores any text, even invalid, so work is never lost. Only local
// (unsigned) plugins can be edited: a signed publisher's plugin can never be
// modified in place.
func (s *Service) SaveDraft(ctx context.Context, actor application.Principal, installationID int64, input DraftInput) error {
	if err := s.require(actor, PermDevelop); err != nil {
		return err
	}
	installation, err := s.store.GetPluginInstallation(ctx, installationID)
	if err != nil {
		return storeError(err)
	}
	if installation.PublisherIdentity != model.PluginPublisherLocal {
		return Fail(CodePublisherChanged, "已签名发布者的插件不能在编辑器中修改")
	}
	if len(input.Manifest) > MaxManifestBytes || len(input.Source) > MaxSourceBytes {
		return Fail(CodeInvalidPackage, "draft is too large")
	}
	return storeError(s.store.SavePluginDraft(ctx, installationID, input.Manifest, input.Source))
}

// Publish validates the draft as a package, stores it as a new immutable
// local version and activates it with the normal permission-diff rules.
func (s *Service) Publish(ctx context.Context, actor application.Principal, installationID int64, check func(DraftInput) (PackageCandidate, error)) (InstallResult, error) {
	if err := s.require(actor, PermInstall); err != nil {
		return InstallResult{}, err
	}
	installation, err := s.store.GetPluginInstallation(ctx, installationID)
	if err != nil {
		return InstallResult{}, storeError(err)
	}
	if installation.PublisherIdentity != model.PluginPublisherLocal || len(installation.DraftManifestJSON) == 0 {
		return InstallResult{}, Fail(CodeConflict, "没有可发布的草稿")
	}
	candidate, err := check(DraftInput{Manifest: installation.DraftManifestJSON, Source: installation.DraftSource})
	if err != nil {
		return InstallResult{}, err
	}
	if candidate.Manifest.ID != installation.PluginKey {
		return InstallResult{}, FailField(CodeInvalidManifest, "id", "发布时不能修改插件 id")
	}
	candidate.SourceKind = model.PluginSourceEditor
	candidate.PublisherIdentity = model.PluginPublisherLocal
	candidate.SignatureState = model.PluginSignatureUnsigned
	pkg := model.PluginPackage{
		PluginKey: candidate.Manifest.ID, Version: candidate.Manifest.Version, SHA256: candidate.SHA256,
		ManifestJSON: candidate.Manifest.CanonicalJSON(), Source: candidate.Source, PublisherIdentity: model.PluginPublisherLocal,
		SignatureState: model.PluginSignatureUnsigned, SourceKind: model.PluginSourceEditor, CreatedByUserID: actorUserID(actor),
	}
	var previous *Manifest
	if installation.ActivePackageID > 0 {
		if current, err := s.store.GetPluginPackage(ctx, installation.ActivePackageID); err == nil {
			if parsed, err := ParseManifest(current.ManifestJSON); err == nil {
				previous = &parsed
			}
		}
	}
	firstRelease := installation.ActivePackageID == 0
	review, err := s.activate(ctx, installationID, pkg, candidate.Manifest, true)
	if err != nil {
		return InstallResult{}, err
	}
	result := InstallResult{InstallationID: installationID, Action: "publish", Diff: DiffPermissions(previous, candidate.Manifest), ReviewRequired: review}
	stored, err := s.store.GetPluginPackageBySHA(ctx, installationID, candidate.SHA256)
	if err == nil {
		result.PackageID = stored.ID
	}
	if firstRelease {
		instances, _ := s.store.ListPluginInstances(ctx, installationID)
		if len(instances) == 0 {
			instance := model.PluginInstance{InstallationID: installationID, Name: "默认实例"}
			if err := s.store.CreatePluginInstance(ctx, &instance); err == nil {
				result.InstanceID = instance.ID
			}
		}
	}
	return result, nil
}

// Diagnose returns editor diagnostics for a manifest/source pair without
// running any plugin code.
func (s *Service) Diagnose(ctx context.Context, actor application.Principal, input DraftInput, checkSource func(string) error) (EditorDiagnostics, error) {
	if err := s.require(actor, PermRead); err != nil {
		return EditorDiagnostics{}, err
	}
	out := EditorDiagnostics{Issues: []FieldIssue{}, Undeclared: []string{}, UnusedEnv: []string{}, Permissions: []PermissionLine{}}
	manifest, err := ParseManifest(input.Manifest)
	if err != nil {
		coded := AsError(err)
		out.Issues = append(out.Issues, FieldIssue{Field: "manifest." + coded.Field, Code: coded.Code, Message: coded.Message})
	} else {
		out.Manifest = &manifest
		out.Permissions = permissionLines(manifest)
		out.Undeclared, out.UnusedEnv = LintSource(manifest, input.Source)
	}
	if err := checkSource(input.Source); err != nil {
		coded := AsError(err)
		out.Issues = append(out.Issues, FieldIssue{Field: "main.js", Code: coded.Code, Message: coded.Message})
	}
	for _, name := range out.Undeclared {
		out.Issues = append(out.Issues, FieldIssue{Field: "main.js", Code: CodeCapabilityDenied, Message: "代码调用了未在清单声明的能力 " + name + "；运行时会被拒绝"})
	}
	out.Valid = len(out.Issues) == 0
	return out, nil
}

// IconDataURL renders a stored PNG icon for the Web without serving it as a
// same-origin resource.
func IconDataURL(icon []byte) string {
	if len(icon) == 0 {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(icon)
}
