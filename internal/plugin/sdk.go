package plugin

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginnetwork"
)

const (
	SDKManagementQuery   = "management.query"
	SDKManagementPreview = "management.preview"
	SDKManagementApply   = "management.apply"
	SDKNetworkRequest    = "network.request"
	SDKConfigGet         = "config.get"
)

type CallerResolver func(context.Context, model.PluginRun) (application.Principal, error)

// SetCallerResolver must be wired before accepting worker requests. A missing
// resolver never permits a user-triggered run to borrow a plugin's authority.
func (s *Service) SetCallerResolver(resolve CallerResolver) { s.callerResolver = resolve }

// ManagementCapabilityAllowed is deliberately narrower than the catalog. New
// catalog operations do not automatically become plugin operations.
func ManagementCapabilityAllowed(name string) bool {
	switch name {
	case "inventory.read", "servers.list", "servers.get", "servers.metrics.read", "servers.latency_probes.read", "servers.connectivity.read", "servers.connectivity.sla", "servers.connectivity.events", "servers.dns_policy.get", "deployments.plan", "deployments.apply", "servers.update", "inbounds.create", "inbounds.update", "inbounds.delete", "proxy_paths.create", "proxy_paths.update", "proxy_paths.delete":
		return true
	default:
		return false
	}
}

type ManagementRequest struct {
	Capability        string            `json:"capability"`
	Input             map[string]any    `json:"input"`
	Reason            string            `json:"reason,omitempty"`
	ExpectedRevisions map[string]string `json:"expected_revisions,omitempty"`
}

type ManagementHost interface {
	PluginManagement(context.Context, application.Principal, model.PluginRun, model.PluginRunAction, string, ManagementRequest) (map[string]any, error)
}

// NetworkSecretHost injects only an explicitly declared and granted secret into
// the already restricted request. Implementations must not return plaintext
// secrets or upstream bodies to logs. Missing support fails closed.
type NetworkSecretHost interface {
	PluginNetworkRequest(context.Context, application.Principal, model.PluginRun, pluginnetwork.Request, pluginnetwork.Policy, string) (pluginnetwork.Response, error)
}

func (s *Service) callerAllows(caller application.Principal, sdk string) bool {
	if name, ok := strings.CutPrefix(sdk, "management:"); ok {
		catalog := capability.NewCatalog()
		desc, allowed := catalog.Authorize(caller, name)
		return allowed && ManagementCapabilityAllowed(name) && !desc.AdminOnly && desc.PrivilegeClass == "" && (caller.Role == "" || caller.Role == model.RoleNone || catalog.RBAC().Allows(caller.Role, desc.RBACPermission))
	}
	var scope string
	switch sdk {
	case model.PluginSDKServersGet, model.PluginSDKServersList, model.PluginSDKServersStatus, model.PluginSDKMetricsLatest:
		scope = "servers:read"
	case model.PluginSDKIncidentsGet:
		scope = "audit:read"
	case model.PluginSDKServicesStatus, model.PluginSDKServicesRestart:
		return caller.Role == model.RoleAdmin || (caller.Role == "" || caller.Role == model.RoleNone) && caller.HasScope("remote_operations:execute")
	case model.PluginSDKHostPoweroff, model.PluginSDKHostReboot:
		return caller.Role == model.RoleAdmin
	case model.PluginSDKNotificationsSend:
		scope = "notifications:write"
	case SDKConfigGet, SDKManagementQuery, SDKManagementPreview, SDKManagementApply, SDKNetworkRequest, model.PluginSDKOperationsGet, model.PluginSDKOperationsWait, model.PluginSDKStateGet, model.PluginSDKStateCAS:
		return true
	default:
		return false
	}
	if !caller.HasScope(scope) {
		return false
	}
	if sdk == model.PluginSDKNotificationsSend && caller.Role != "" && caller.Role != model.RoleNone && s.rbac != nil {
		return s.rbac.Allows(caller.Role, "notifications.channels.update")
	}
	return true
}

func sameOptionalID(a, b *int64) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func sameRunRequest(a, b model.PluginRun) bool {
	if a.PluginID != b.PluginID || a.RevisionID != b.RevisionID || a.CallerPrincipal != b.CallerPrincipal || a.Mode != b.Mode || a.TriggerKind != b.TriggerKind || !sameOptionalID(a.BindingID, b.BindingID) {
		return false
	}
	var x, y any
	if json.Unmarshal(a.SnapshotJSON, &x) != nil || json.Unmarshal(b.SnapshotJSON, &y) != nil {
		return false
	}
	dx, ex := DigestJSON(x)
	dy, ey := DigestJSON(y)
	return ex == nil && ey == nil && dx == dy
}

func BindingDigest(binding model.PluginTriggerBinding) string {
	digest, _ := DigestJSON(map[string]any{"id": binding.ID, "plugin_id": binding.PluginID, "revision_id": binding.RevisionID, "binding_revision": binding.BindingRevision, "kind": binding.Kind, "spec": json.RawMessage(orEmptyJSON(binding.SpecJSON)), "params": json.RawMessage(orEmptyJSON(binding.ParamsJSON)), "env": json.RawMessage(orEmptyJSON(binding.EnvJSON))})
	return digest
}

func effectiveNetworkPolicy(manifest model.PluginManifest, grant model.PluginGrant) (pluginnetwork.Policy, error) {
	var declaration struct {
		Network pluginnetwork.Policy `json:"network"`
	}
	var constraints struct {
		Network pluginnetwork.Policy `json:"network"`
	}
	if json.Unmarshal(MustJSON(manifest), &declaration) != nil || json.Unmarshal(grant.ConstraintsJSON, &constraints) != nil {
		return pluginnetwork.Policy{}, ErrPermissionDenied
	}
	a, b := declaration.Network, constraints.Network
	if pluginnetwork.ValidatePolicy(a) != nil || pluginnetwork.ValidatePolicy(b) != nil {
		return pluginnetwork.Policy{}, ErrPermissionDenied
	}
	result := pluginnetwork.Policy{AllowedOrigins: intersectStrings(a.AllowedOrigins, b.AllowedOrigins), AllowedMethods: intersectStrings(a.AllowedMethods, b.AllowedMethods), MaxRequestBytes: minPositive(a.MaxRequestBytes, b.MaxRequestBytes), MaxResponseBytes: minPositive(a.MaxResponseBytes, b.MaxResponseBytes)}
	if pluginnetwork.ValidatePolicy(result) != nil {
		return result, ErrPermissionDenied
	}
	return result, nil
}

func minPositive(a, b int64) int64 {
	if a == 0 {
		return b
	}
	if b == 0 || a < b {
		return a
	}
	return b
}
