package plugin

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
)

// Derived instance statuses shown by the Web and MCP.
const (
	StatusReady                    = "ready"
	StatusDegraded                 = "degraded"
	StatusAutoPaused               = "auto_paused"
	StatusDisabled                 = "disabled"
	StatusPluginDisabled           = "plugin_disabled"
	StatusPermissionReviewRequired = "permission_review_required"
	StatusNotPublished             = "not_published"
)

const maxSchedulesPerInstance = 8

type InstallationView struct {
	ID                int64             `json:"id"`
	PluginID          string            `json:"plugin_id"`
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	Enabled           bool              `json:"enabled"`
	Published         bool              `json:"published"`
	HasDraft          bool              `json:"has_draft"`
	Version           string            `json:"version,omitempty"`
	PackageID         int64             `json:"package_id,omitempty"`
	PublisherIdentity string            `json:"publisher_identity"`
	PublisherName     string            `json:"publisher_name,omitempty"`
	SignatureState    string            `json:"signature_state,omitempty"`
	SourceKind        string            `json:"source_kind,omitempty"`
	SourceRepository  string            `json:"source_repository,omitempty"`
	SourceCommit      string            `json:"source_commit,omitempty"`
	Icon              string            `json:"icon,omitempty"`
	Instances         []InstanceSummary `json:"instances"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

type InstanceSummary struct {
	ID                       int64      `json:"id"`
	Name                     string     `json:"name"`
	Enabled                  bool       `json:"enabled"`
	Status                   string     `json:"status"`
	ConfigStatus             string     `json:"config_status"`
	AutoPaused               bool       `json:"auto_paused"`
	PermissionReviewRequired bool       `json:"permission_review_required"`
	FailureStreak            int        `json:"failure_streak"`
	LastRunAt                *time.Time `json:"last_run_at,omitempty"`
	LastSuccessAt            *time.Time `json:"last_success_at,omitempty"`
	LastErrorCode            string     `json:"last_error_code,omitempty"`
}

type VersionView struct {
	PackageID         int64     `json:"package_id"`
	Version           string    `json:"version"`
	SHA256            string    `json:"sha256"`
	PublisherIdentity string    `json:"publisher_identity"`
	PublisherName     string    `json:"publisher_name,omitempty"`
	SignatureState    string    `json:"signature_state"`
	SourceKind        string    `json:"source_kind"`
	SourceRepository  string    `json:"source_repository,omitempty"`
	SourceCommit      string    `json:"source_commit,omitempty"`
	Active            bool      `json:"active"`
	CreatedAt         time.Time `json:"created_at"`
}

type DraftView struct {
	Manifest  string     `json:"manifest"`
	Source    string     `json:"source"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type InstallationDetail struct {
	InstallationView
	Manifest    *Manifest        `json:"manifest,omitempty"`
	Permissions []PermissionLine `json:"permissions"`
	HTTPHosts   []string         `json:"http_hosts"`
	Forbidden   []string         `json:"forbidden"`
	Readme      string           `json:"readme,omitempty"`
	License     string           `json:"license,omitempty"`
	Versions    []VersionView    `json:"versions"`
	Draft       *DraftView       `json:"draft,omitempty"`
	Source      string           `json:"source,omitempty"`
	CanDevelop  bool             `json:"can_develop"`
	CanInstall  bool             `json:"can_install"`
	Instances   []InstanceDetail `json:"instance_details"`
}

type SecretStatus struct {
	Configured bool       `json:"configured"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

type GrantView struct {
	PackageID    int64                      `json:"package_id"`
	Current      bool                       `json:"current"`
	Capabilities map[string]CapabilityGrant `json:"capabilities"`
	Revision     int64                      `json:"revision"`
	ApprovedAt   time.Time                  `json:"approved_at"`
}

type StateUsage struct {
	Keys     int `json:"keys"`
	Bytes    int `json:"bytes"`
	MaxKeys  int `json:"max_keys"`
	MaxBytes int `json:"max_bytes"`
}

type InstanceDetail struct {
	InstanceSummary
	InstallationID int64                      `json:"installation_id"`
	Revision       int64                      `json:"revision"`
	Issues         []FieldIssue               `json:"issues"`
	Values         map[string]json.RawMessage `json:"values"`
	Custom         []CustomVar                `json:"custom"`
	Secrets        map[string]SecretStatus    `json:"secrets"`
	Grant          *GrantView                 `json:"grant,omitempty"`
	Schedules      []model.PluginSchedule     `json:"schedules"`
	State          StateUsage                 `json:"state"`
	CreatedAt      time.Time                  `json:"created_at"`
	UpdatedAt      time.Time                  `json:"updated_at"`
}

type loadedInstallation struct {
	installation model.PluginInstallation
	pkg          *model.PluginPackage
	manifest     *Manifest
}

func (s *Service) loadInstallation(ctx context.Context, id int64) (loadedInstallation, error) {
	installation, err := s.store.GetPluginInstallation(ctx, id)
	if err != nil {
		return loadedInstallation{}, storeError(err)
	}
	out := loadedInstallation{installation: installation}
	if installation.ActivePackageID > 0 {
		pkg, err := s.store.GetPluginPackage(ctx, installation.ActivePackageID)
		if err != nil {
			return out, storeError(err)
		}
		manifest, err := ParseManifest(pkg.ManifestJSON)
		if err != nil {
			return out, err
		}
		out.pkg, out.manifest = &pkg, &manifest
	}
	return out, nil
}

func (s *Service) installationView(loaded loadedInstallation, instances []InstanceSummary) InstallationView {
	i := loaded.installation
	view := InstallationView{ID: i.ID, PluginID: i.PluginKey, Name: i.Name, Description: i.Description, Enabled: i.Enabled, HasDraft: len(i.DraftManifestJSON) > 0, PublisherIdentity: i.PublisherIdentity, Instances: instances, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
	if loaded.pkg != nil {
		p := loaded.pkg
		view.Published, view.Version, view.PackageID = true, p.Version, p.ID
		view.PublisherName, view.SignatureState, view.SourceKind = p.PublisherName, p.SignatureState, p.SourceKind
		view.SourceRepository, view.SourceCommit, view.Icon = p.SourceRepository, p.SourceCommit, IconDataURL(p.IconPNG)
	}
	return view
}

// evaluateInstance computes configuration status and derived status against
// current reality: existing servers, current grant and configured secrets.
func (s *Service) evaluateInstance(ctx context.Context, loaded loadedInstallation, instance model.PluginInstance, settingsEnabled bool) (InstanceSummary, []FieldIssue, *Grant, map[string]bool) {
	summary := InstanceSummary{ID: instance.ID, Name: instance.Name, Enabled: instance.Enabled, AutoPaused: instance.AutoPaused, PermissionReviewRequired: instance.PermissionReviewRequired, FailureStreak: instance.FailureStreak, LastRunAt: instance.LastRunAt, LastSuccessAt: instance.LastSuccessAt, LastErrorCode: instance.LastErrorCode}
	secretNames, _ := s.store.ListPluginSecretNames(ctx, instance.ID)
	configured := map[string]bool{}
	for name := range secretNames {
		configured[name] = true
	}
	if loaded.manifest == nil {
		summary.Status, summary.ConfigStatus = StatusNotPublished, ConfigOK
		return summary, []FieldIssue{}, nil, configured
	}
	var grant *Grant
	if record, err := s.store.GetPluginGrant(ctx, instance.ID); err == nil && record.PackageID == loaded.pkg.ID {
		parsed, _ := ParseGrant(record.GrantJSON)
		grant = &parsed
	}
	values, custom := decodeEnvironment(instance)
	var granted map[int64]bool
	if loaded.manifest.UsesServerResources() {
		granted = map[int64]bool{}
		if grant != nil {
			granted = grant.ServerUnion()
		}
	}
	status, issues := EvaluateEnvironment(EvaluationInput{
		Manifest: *loaded.manifest, Values: values, Custom: custom, SecretsConfigured: configured,
		ServerExists:   func(id int64) bool { _, ok := s.host.Server(ctx, id); return ok },
		GrantedServers: granted,
	})
	summary.ConfigStatus = status
	switch {
	case !loaded.installation.Enabled || !settingsEnabled:
		summary.Status = StatusPluginDisabled
	case !instance.Enabled:
		summary.Status = StatusDisabled
	case instance.PermissionReviewRequired:
		summary.Status = StatusPermissionReviewRequired
	case status != ConfigOK:
		summary.Status = status
	case instance.AutoPaused:
		summary.Status = StatusAutoPaused
	case instance.FailureStreak >= DegradedFailureStreak:
		summary.Status = StatusDegraded
	default:
		summary.Status = StatusReady
	}
	return summary, issues, grant, configured
}

func decodeEnvironment(instance model.PluginInstance) (map[string]json.RawMessage, []CustomVar) {
	values := map[string]json.RawMessage{}
	_ = json.Unmarshal(instance.ValuesJSON, &values)
	custom := []CustomVar{}
	_ = json.Unmarshal(instance.CustomJSON, &custom)
	return values, custom
}

func (s *Service) ListInstallations(ctx context.Context, actor application.Principal) ([]InstallationView, error) {
	if err := s.require(actor, PermRead); err != nil {
		return nil, err
	}
	installations, err := s.store.ListPluginInstallations(ctx)
	if err != nil {
		return nil, storeError(err)
	}
	settings := s.Settings(ctx)
	out := make([]InstallationView, 0, len(installations))
	for _, installation := range installations {
		loaded, err := s.loadInstallation(ctx, installation.ID)
		if err != nil {
			continue
		}
		instances, _ := s.store.ListPluginInstances(ctx, installation.ID)
		summaries := make([]InstanceSummary, 0, len(instances))
		for _, instance := range instances {
			summary, _, _, _ := s.evaluateInstance(ctx, loaded, instance, settings.Enabled)
			summaries = append(summaries, summary)
		}
		out = append(out, s.installationView(loaded, summaries))
	}
	return out, nil
}

func (s *Service) GetInstallation(ctx context.Context, actor application.Principal, id int64) (InstallationDetail, error) {
	if err := s.require(actor, PermRead); err != nil {
		return InstallationDetail{}, err
	}
	loaded, err := s.loadInstallation(ctx, id)
	if err != nil {
		return InstallationDetail{}, err
	}
	settings := s.Settings(ctx)
	instances, err := s.store.ListPluginInstances(ctx, id)
	if err != nil {
		return InstallationDetail{}, storeError(err)
	}
	detail := InstallationDetail{Permissions: []PermissionLine{}, HTTPHosts: []string{}, Forbidden: ForbiddenCapabilityGroups(), Versions: []VersionView{}, Instances: []InstanceDetail{}}
	summaries := []InstanceSummary{}
	for _, instance := range instances {
		item, err := s.instanceDetail(ctx, loaded, instance, settings.Enabled)
		if err != nil {
			return InstallationDetail{}, err
		}
		detail.Instances = append(detail.Instances, item)
		summaries = append(summaries, item.InstanceSummary)
	}
	detail.InstallationView = s.installationView(loaded, summaries)
	if loaded.manifest != nil {
		detail.Manifest = loaded.manifest
		detail.Permissions = permissionLines(*loaded.manifest)
		if loaded.manifest.HTTP != nil {
			detail.HTTPHosts = append(detail.HTTPHosts, loaded.manifest.HTTP.Hosts...)
		}
		detail.Readme, detail.License = loaded.pkg.Readme, loaded.pkg.License
	}
	packages, err := s.store.ListPluginPackages(ctx, id)
	if err != nil {
		return InstallationDetail{}, storeError(err)
	}
	for _, pkg := range packages {
		detail.Versions = append(detail.Versions, VersionView{PackageID: pkg.ID, Version: pkg.Version, SHA256: pkg.SHA256, PublisherIdentity: pkg.PublisherIdentity, PublisherName: pkg.PublisherName, SignatureState: pkg.SignatureState, SourceKind: pkg.SourceKind, SourceRepository: pkg.SourceRepository, SourceCommit: pkg.SourceCommit, Active: pkg.ID == loaded.installation.ActivePackageID, CreatedAt: pkg.CreatedAt})
	}
	detail.CanDevelop = s.Can(actor, PermDevelop)
	detail.CanInstall = s.Can(actor, PermInstall)
	if detail.CanDevelop {
		if loaded.pkg != nil {
			detail.Source = loaded.pkg.Source
		}
		if len(loaded.installation.DraftManifestJSON) > 0 {
			detail.Draft = &DraftView{Manifest: string(loaded.installation.DraftManifestJSON), Source: loaded.installation.DraftSource, UpdatedAt: loaded.installation.DraftUpdatedAt}
		}
	}
	return detail, nil
}

func (s *Service) instanceDetail(ctx context.Context, loaded loadedInstallation, instance model.PluginInstance, settingsEnabled bool) (InstanceDetail, error) {
	summary, issues, _, _ := s.evaluateInstance(ctx, loaded, instance, settingsEnabled)
	values, custom := decodeEnvironment(instance)
	detail := InstanceDetail{InstanceSummary: summary, InstallationID: instance.InstallationID, Revision: instance.Revision, Issues: issues, Values: values, Custom: custom, Secrets: map[string]SecretStatus{}, Schedules: []model.PluginSchedule{}, CreatedAt: instance.CreatedAt, UpdatedAt: instance.UpdatedAt}
	secretNames, err := s.store.ListPluginSecretNames(ctx, instance.ID)
	if err != nil {
		return detail, storeError(err)
	}
	expected := map[string]bool{}
	if loaded.manifest != nil {
		for _, field := range loaded.manifest.Environment {
			if field.Type == EnvSecret {
				expected[field.Name] = true
			}
		}
	}
	for _, item := range custom {
		if item.Type == EnvSecret {
			expected[item.Name] = true
		}
	}
	for name := range expected {
		status := SecretStatus{}
		if updated, ok := secretNames[name]; ok {
			status.Configured = true
			updatedAt := updated
			status.UpdatedAt = &updatedAt
		}
		detail.Secrets[name] = status
	}
	if record, err := s.store.GetPluginGrant(ctx, instance.ID); err == nil {
		grant, _ := ParseGrant(record.GrantJSON)
		current := loaded.pkg != nil && record.PackageID == loaded.pkg.ID
		detail.Grant = &GrantView{PackageID: record.PackageID, Current: current, Capabilities: grant.Capabilities, Revision: record.Revision, ApprovedAt: record.ApprovedAt}
	}
	schedules, err := s.store.ListPluginSchedules(ctx, instance.ID)
	if err != nil {
		return detail, storeError(err)
	}
	detail.Schedules = schedules
	keys, bytes, _ := s.store.PluginStateUsage(ctx, instance.ID)
	detail.State = StateUsage{Keys: keys, Bytes: bytes, MaxKeys: MaxStateKeys, MaxBytes: MaxStateTotalBytes}
	return detail, nil
}

func (s *Service) loadInstance(ctx context.Context, instanceID int64) (loadedInstallation, model.PluginInstance, error) {
	instance, err := s.store.GetPluginInstance(ctx, instanceID)
	if err != nil {
		return loadedInstallation{}, instance, storeError(err)
	}
	loaded, err := s.loadInstallation(ctx, instance.InstallationID)
	return loaded, instance, err
}

func (s *Service) GetInstance(ctx context.Context, actor application.Principal, instanceID int64) (InstanceDetail, error) {
	if err := s.require(actor, PermRead); err != nil {
		return InstanceDetail{}, err
	}
	loaded, instance, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return InstanceDetail{}, err
	}
	return s.instanceDetail(ctx, loaded, instance, s.Settings(ctx).Enabled)
}

func validInstanceName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 || !utf8.ValidString(name) || hasControl(name, false) {
		return "", FailField(CodeInvalidArgument, "name", "实例名称必填，最多 64 字节")
	}
	return name, nil
}

// CreateInstance adds a disabled, ungranted instance.
func (s *Service) CreateInstance(ctx context.Context, actor application.Principal, installationID int64, name string) (InstanceDetail, error) {
	if err := s.require(actor, PermConfigure); err != nil {
		return InstanceDetail{}, err
	}
	name, err := validInstanceName(name)
	if err != nil {
		return InstanceDetail{}, err
	}
	loaded, err := s.loadInstallation(ctx, installationID)
	if err != nil {
		return InstanceDetail{}, err
	}
	instances, _ := s.store.ListPluginInstances(ctx, installationID)
	if len(instances) >= 32 {
		return InstanceDetail{}, Fail(CodeLimitExceeded, "每个插件最多 32 个实例")
	}
	instance := model.PluginInstance{InstallationID: installationID, Name: name}
	if err := s.store.CreatePluginInstance(ctx, &instance); err != nil {
		if storeError(err) == ErrConflict {
			return InstanceDetail{}, FailField(CodeConflict, "name", "实例名称已存在")
		}
		return InstanceDetail{}, storeError(err)
	}
	return s.instanceDetail(ctx, loaded, instance, s.Settings(ctx).Enabled)
}

type InstanceUpdate struct {
	Name    *string `json:"name,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	Resume  bool    `json:"resume,omitempty"`
}

// UpdateInstance renames, enables/disables or resumes an auto-paused
// instance. Enabling or disabling is an administrator decision.
func (s *Service) UpdateInstance(ctx context.Context, actor application.Principal, instanceID int64, update InstanceUpdate) (InstanceDetail, error) {
	if err := s.require(actor, PermConfigure); err != nil {
		return InstanceDetail{}, err
	}
	if update.Enabled != nil {
		if err := s.require(actor, PermInstall); err != nil {
			return InstanceDetail{}, err
		}
	}
	loaded, instance, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return InstanceDetail{}, err
	}
	name, enabled := instance.Name, instance.Enabled
	if update.Name != nil {
		if name, err = validInstanceName(*update.Name); err != nil {
			return InstanceDetail{}, err
		}
	}
	if update.Enabled != nil {
		enabled = *update.Enabled
	}
	if err := s.store.UpdatePluginInstanceSettings(ctx, instanceID, name, enabled, update.Resume); err != nil {
		if storeError(err) == ErrConflict {
			return InstanceDetail{}, FailField(CodeConflict, "name", "实例名称已存在")
		}
		return InstanceDetail{}, storeError(err)
	}
	if !enabled {
		_ = s.store.CancelPluginRunsForInstance(ctx, instanceID, "插件实例已停用")
	}
	instance, err = s.store.GetPluginInstance(ctx, instanceID)
	if err != nil {
		return InstanceDetail{}, storeError(err)
	}
	return s.instanceDetail(ctx, loaded, instance, s.Settings(ctx).Enabled)
}

func (s *Service) DeleteInstance(ctx context.Context, actor application.Principal, instanceID int64) error {
	if err := s.require(actor, PermInstall); err != nil {
		return err
	}
	return storeError(s.store.DeletePluginInstance(ctx, instanceID))
}

// SaveEnvironment is the save-validate phase. The environment is
// configuration only: nothing saved here changes what the plugin may do.
func (s *Service) SaveEnvironment(ctx context.Context, actor application.Principal, instanceID, expectedRevision int64, input EnvironmentInput) (InstanceDetail, error) {
	if err := s.require(actor, PermConfigure); err != nil {
		return InstanceDetail{}, err
	}
	loaded, instance, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return InstanceDetail{}, err
	}
	if loaded.manifest == nil {
		return InstanceDetail{}, Fail(CodeConflict, "插件还没有已发布的版本")
	}
	if expectedRevision != instance.Revision {
		return InstanceDetail{}, ErrConflict
	}
	values, custom, issues := NormalizeEnvironment(*loaded.manifest, input)
	for id := range serverIDsOf(*loaded.manifest, values, custom) {
		if _, ok := s.host.Server(ctx, id); !ok {
			issues = append(issues, FieldIssue{Field: "servers", Code: CodeServerNotFound, Message: "所选服务器不存在"})
			break
		}
	}
	if len(issues) > 0 {
		return InstanceDetail{}, &Error{Code: CodeInvalidEnvironment, Message: "配置未通过校验", Issues: issues}
	}
	_, previousCustom := decodeEnvironment(instance)
	if _, err := s.store.UpdatePluginInstanceEnvironment(ctx, instanceID, expectedRevision, mustMarshal(values), mustMarshal(custom)); err != nil {
		return InstanceDetail{}, storeError(err)
	}
	keep := map[string]bool{}
	for _, item := range custom {
		if item.Type == EnvSecret {
			keep[item.Name] = true
		}
	}
	for _, item := range previousCustom {
		if item.Type == EnvSecret && !keep[item.Name] {
			if _, declared := loaded.manifest.EnvField(item.Name); !declared {
				_ = s.store.DeletePluginSecret(ctx, instanceID, item.Name)
			}
		}
	}
	instance, err = s.store.GetPluginInstance(ctx, instanceID)
	if err != nil {
		return InstanceDetail{}, storeError(err)
	}
	return s.instanceDetail(ctx, loaded, instance, s.Settings(ctx).Enabled)
}

func serverIDsOf(manifest Manifest, values map[string]json.RawMessage, custom []CustomVar) map[int64]bool {
	set := map[int64]bool{}
	for _, id := range ServerIDsInEnvironment(manifest, values, custom) {
		set[id] = true
	}
	return set
}

const maxSecretBytes = 8 << 10

// SetSecret stores a secret value encrypted with the Controller session
// secret. Only names declared as secret fields or custom secret variables are
// accepted, and the value is never returned by any API.
func (s *Service) SetSecret(ctx context.Context, actor application.Principal, instanceID int64, name, value string) error {
	if err := s.require(actor, PermAuthorize); err != nil {
		return err
	}
	loaded, instance, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return err
	}
	if !secretNameExpected(loaded.manifest, instance, name) {
		return FailField(CodeInvalidArgument, name, "该名称不是此实例声明的密钥")
	}
	if value == "" {
		return storeError(s.store.DeletePluginSecret(ctx, instanceID, name))
	}
	if len(value) > maxSecretBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return FailField(CodeInvalidArgument, name, "密钥最多 8 KiB 的 UTF-8 文本")
	}
	encrypted, err := s.host.EncryptSecret(value)
	if err != nil {
		return Fail(CodeInternal, "无法加密密钥")
	}
	return storeError(s.store.SetPluginSecret(ctx, instanceID, name, encrypted))
}

func secretNameExpected(manifest *Manifest, instance model.PluginInstance, name string) bool {
	if manifest != nil {
		if field, ok := manifest.EnvField(name); ok {
			return field.Type == EnvSecret
		}
	}
	_, custom := decodeEnvironment(instance)
	for _, item := range custom {
		if item.Name == name && item.Type == EnvSecret {
			return true
		}
	}
	return false
}

// SetGrant records the administrator decision for the active version.
func (s *Service) SetGrant(ctx context.Context, actor application.Principal, instanceID, expectedRevision int64, grant Grant) (InstanceDetail, error) {
	if err := s.require(actor, PermAuthorize); err != nil {
		return InstanceDetail{}, err
	}
	loaded, _, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return InstanceDetail{}, err
	}
	if loaded.manifest == nil {
		return InstanceDetail{}, Fail(CodeConflict, "插件还没有已发布的版本")
	}
	serverExists := func(id int64) bool { _, ok := s.host.Server(ctx, id); return ok }
	channelExists := func(id int64) bool { return s.host.NotificationChannelExists(ctx, id) }
	userExists := func(id int64) bool { _, ok := s.host.User(ctx, id); return ok }
	planExists := func(id int64) bool { _, ok := s.host.Plan(ctx, id); return ok }
	if err := ValidateGrant(*loaded.manifest, &grant, serverExists, channelExists, userExists, planExists); err != nil {
		return InstanceDetail{}, err
	}
	if req := manifestServerRequirement(*loaded.manifest); req != nil && req.Min > 0 {
		hasServerCap := false
		for name := range grant.Capabilities {
			if spec, _ := LookupCapability(name); spec.Resource == ResourceServer {
				hasServerCap = true
			}
		}
		if hasServerCap && len(grant.ServerUnion()) < req.Min {
			return InstanceDetail{}, FailField(CodeInvalidArgument, "capabilities", "授权的服务器数量少于插件声明的最低要求")
		}
	}
	record := model.PluginGrant{InstanceID: instanceID, PackageID: loaded.pkg.ID, GrantJSON: grant.CanonicalJSON(), ApprovedByUserID: actorUserID(actor)}
	if _, err := s.store.SetPluginGrant(ctx, record, expectedRevision); err != nil {
		return InstanceDetail{}, storeError(err)
	}
	return s.GetInstance(ctx, actor, instanceID)
}

// RevokeGrant removes every capability from an instance immediately; the
// gateway refuses further calls of running runs on their next call.
func (s *Service) RevokeGrant(ctx context.Context, actor application.Principal, instanceID int64) error {
	if err := s.require(actor, PermAuthorize); err != nil {
		return err
	}
	if _, err := s.store.GetPluginInstance(ctx, instanceID); err != nil {
		return storeError(err)
	}
	return storeError(s.store.DeletePluginGrant(ctx, instanceID))
}

// ScheduleInput creates or updates one trigger.
type ScheduleInput struct {
	Kind     string `json:"kind"`
	Interval string `json:"interval,omitempty"`
	Cron     string `json:"cron,omitempty"`
	Timezone string `json:"timezone,omitempty"`
	Event    string `json:"event,omitempty"`
	Enabled  bool   `json:"enabled"`
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func validateSchedule(manifest Manifest, input ScheduleInput, now time.Time) (model.PluginSchedule, error) {
	item := model.PluginSchedule{Kind: input.Kind, Enabled: input.Enabled}
	switch input.Kind {
	case model.PluginTriggerInterval:
		if !manifest.Triggers.Schedule {
			return item, FailField(CodeInvalidArgument, "kind", "插件清单未声明定时触发")
		}
		d, canonical, err := ParseDuration(input.Interval)
		if err != nil || d < MinScheduleInterval || d > MaxScheduleInterval {
			return item, FailField(CodeInvalidArgument, "interval", "间隔需在 1m 到 30 天之间")
		}
		item.Interval = canonical
	case model.PluginTriggerCron:
		if !manifest.Triggers.Schedule {
			return item, FailField(CodeInvalidArgument, "kind", "插件清单未声明定时触发")
		}
		expr := strings.TrimSpace(input.Cron)
		if len(expr) > 120 {
			return item, FailField(CodeInvalidArgument, "cron", "cron 表达式过长")
		}
		if _, err := cronParser.Parse(expr); err != nil {
			return item, FailField(CodeInvalidArgument, "cron", "cron 表达式需为 5 段：分 时 日 月 周")
		}
		timezone := strings.TrimSpace(input.Timezone)
		if timezone == "" {
			timezone = "UTC"
		}
		if _, err := time.LoadLocation(timezone); err != nil {
			return item, FailField(CodeInvalidArgument, "timezone", "时区需为 IANA 名称，如 Asia/Shanghai")
		}
		item.Cron, item.Timezone = expr, timezone
	case model.PluginTriggerEvent:
		declared := false
		for _, event := range manifest.Triggers.Events {
			declared = declared || event == input.Event
		}
		if !declared {
			return item, FailField(CodeInvalidArgument, "event", "插件清单未声明该事件")
		}
		item.Event = input.Event
	default:
		return item, FailField(CodeInvalidArgument, "kind", "触发类型只能是 interval、cron 或 event")
	}
	if item.Enabled {
		item.NextDueAt = nextDue(item, now)
	}
	return item, nil
}

// nextDue computes the next slot. Intervals are aligned to wall-clock
// multiples so restarts never create bursts.
func nextDue(item model.PluginSchedule, after time.Time) *time.Time {
	switch item.Kind {
	case model.PluginTriggerInterval:
		d, _, err := ParseDuration(item.Interval)
		if err != nil {
			return nil
		}
		step := int64(d / time.Second)
		unix := after.Unix()
		next := time.Unix(unix-unix%step+step, 0).UTC()
		return &next
	case model.PluginTriggerCron:
		loc, err := time.LoadLocation(item.Timezone)
		if err != nil {
			return nil
		}
		schedule, err := cronParser.Parse(item.Cron)
		if err != nil {
			return nil
		}
		next := schedule.Next(after.In(loc)).UTC()
		if next.IsZero() {
			return nil
		}
		return &next
	default:
		return nil
	}
}

func (s *Service) CreateSchedule(ctx context.Context, actor application.Principal, instanceID int64, input ScheduleInput) (model.PluginSchedule, error) {
	if err := s.require(actor, PermConfigure); err != nil {
		return model.PluginSchedule{}, err
	}
	loaded, _, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return model.PluginSchedule{}, err
	}
	if loaded.manifest == nil {
		return model.PluginSchedule{}, Fail(CodeConflict, "插件还没有已发布的版本")
	}
	existing, _ := s.store.ListPluginSchedules(ctx, instanceID)
	if len(existing) >= maxSchedulesPerInstance {
		return model.PluginSchedule{}, Fail(CodeLimitExceeded, "每个实例最多 8 个触发器")
	}
	item, err := validateSchedule(*loaded.manifest, input, s.now())
	if err != nil {
		return model.PluginSchedule{}, err
	}
	item.InstanceID = instanceID
	if err := s.store.CreatePluginSchedule(ctx, &item); err != nil {
		return model.PluginSchedule{}, storeError(err)
	}
	return item, nil
}

func (s *Service) UpdateSchedule(ctx context.Context, actor application.Principal, scheduleID int64, input ScheduleInput) (model.PluginSchedule, error) {
	if err := s.require(actor, PermConfigure); err != nil {
		return model.PluginSchedule{}, err
	}
	current, err := s.store.GetPluginSchedule(ctx, scheduleID)
	if err != nil {
		return model.PluginSchedule{}, storeError(err)
	}
	loaded, _, err := s.loadInstance(ctx, current.InstanceID)
	if err != nil {
		return model.PluginSchedule{}, err
	}
	if loaded.manifest == nil {
		return model.PluginSchedule{}, Fail(CodeConflict, "插件还没有已发布的版本")
	}
	item, err := validateSchedule(*loaded.manifest, input, s.now())
	if err != nil {
		return model.PluginSchedule{}, err
	}
	item.ID, item.InstanceID = scheduleID, current.InstanceID
	if err := s.store.UpdatePluginSchedule(ctx, item); err != nil {
		return model.PluginSchedule{}, storeError(err)
	}
	return s.store.GetPluginSchedule(ctx, scheduleID)
}

func (s *Service) DeleteSchedule(ctx context.Context, actor application.Principal, scheduleID int64) error {
	if err := s.require(actor, PermConfigure); err != nil {
		return err
	}
	return storeError(s.store.DeletePluginSchedule(ctx, scheduleID))
}

// ServerOption feeds the server environment selector. The filter flags are UI
// conveniences, not authorization.
type ServerOption struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	RegionCode string `json:"region_code,omitempty"`
	Enrolled   bool   `json:"enrolled"`
	Online     bool   `json:"online"`
	IPv4       bool   `json:"ipv4"`
	IPv6       bool   `json:"ipv6"`
}

func (s *Service) ServerOptions(ctx context.Context, actor application.Principal) ([]ServerOption, error) {
	if err := s.require(actor, PermRead); err != nil {
		return nil, err
	}
	out := []ServerOption{}
	for _, server := range s.host.ListServers(ctx) {
		if !actor.AllowsInt64("server_ids", server.ID) {
			continue
		}
		out = append(out, ServerOption{ID: formatID(server.ID), Name: server.Name, RegionCode: server.RegionCode, Enrolled: server.Enrolled, Online: server.Online, IPv4: server.PublicIPv4 != "", IPv6: server.PublicIPv6 != ""})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// UserOption is one user the grant editor may select. It carries no credentials.
type UserOption struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname,omitempty"`
	Status   string `json:"status"`
}

// PlanOption is one subscription plan the grant editor may select.
type PlanOption struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (s *Service) UserOptions(ctx context.Context, actor application.Principal) ([]UserOption, error) {
	if err := s.require(actor, PermRead); err != nil {
		return nil, err
	}
	out := []UserOption{}
	for _, user := range s.host.ListUsers(ctx) {
		if !actor.AllowsInt64("user_ids", user.ID) {
			continue
		}
		out = append(out, UserOption{ID: formatID(user.ID), Username: user.Username, Nickname: user.Nickname, Status: user.Status})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Username) < strings.ToLower(out[j].Username) })
	return out, nil
}

func (s *Service) PlanOptions(ctx context.Context, actor application.Principal) ([]PlanOption, error) {
	if err := s.require(actor, PermRead); err != nil {
		return nil, err
	}
	out := []PlanOption{}
	for _, plan := range s.host.ListPlans(ctx) {
		if !actor.AllowsInt64("subscription_plan_ids", plan.ID) {
			continue
		}
		out = append(out, PlanOption{ID: formatID(plan.ID), Name: plan.Name, Enabled: plan.Enabled})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

// ListState is the storage tab: keys, versions and values of one instance.
func (s *Service) ListState(ctx context.Context, actor application.Principal, instanceID int64) ([]model.PluginStateEntry, StateUsage, error) {
	if err := s.require(actor, PermRead); err != nil {
		return nil, StateUsage{}, err
	}
	if _, err := s.store.GetPluginInstance(ctx, instanceID); err != nil {
		return nil, StateUsage{}, storeError(err)
	}
	entries, err := s.store.ListPluginState(ctx, instanceID, "", MaxStateKeys)
	if err != nil {
		return nil, StateUsage{}, storeError(err)
	}
	keys, bytes, _ := s.store.PluginStateUsage(ctx, instanceID)
	return entries, StateUsage{Keys: keys, Bytes: bytes, MaxKeys: MaxStateKeys, MaxBytes: MaxStateTotalBytes}, nil
}

// ClearState deletes one key (operator housekeeping).
func (s *Service) ClearState(ctx context.Context, actor application.Principal, instanceID int64, key string) error {
	if err := s.require(actor, PermConfigure); err != nil {
		return err
	}
	_, err := s.store.DeletePluginState(ctx, instanceID, key, nil)
	return storeError(err)
}

func (s *Service) ListAudit(ctx context.Context, actor application.Principal, instanceID, beforeID int64, limit int) ([]model.PluginAuditEvent, error) {
	if err := s.require(actor, PermRead); err != nil {
		return nil, err
	}
	items, err := s.store.ListPluginAuditEvents(ctx, instanceID, beforeID, limit)
	return items, storeError(err)
}

func formatID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
