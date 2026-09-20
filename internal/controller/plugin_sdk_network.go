package controller

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/pluginnetwork"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) PluginNetworkRequest(ctx context.Context, principal application.Principal, run model.PluginRun, request pluginnetwork.Request, policy pluginnetwork.Policy, secret string) (pluginnetwork.Response, error) {
	empty := pluginnetwork.Response{}
	if principal.ID != "plugin:"+run.UUID || run.Mode != model.PluginRunModeLive || run.GrantID == nil {
		return empty, plugin.ErrPermissionDenied
	}
	effective, err := s.plugins.EffectivePrincipal(ctx, run)
	if err != nil || !effective.HasScope(plugin.SDKNetworkRequest) {
		return empty, plugin.ErrPermissionDenied
	}
	revision, err := s.store.GetPluginRevision(ctx, run.RevisionID)
	if err != nil {
		return empty, plugin.ErrPermissionDenied
	}
	manifest, err := plugin.ParseManifest(revision.ManifestJSON)
	if err != nil {
		return empty, plugin.ErrPermissionDenied
	}
	declared := false
	for _, ref := range manifest.Secrets {
		if ref.Name == secret {
			declared = true
		}
	}
	grant, err := s.store.GetPluginGrant(ctx, *run.GrantID)
	if err != nil {
		return empty, plugin.ErrPermissionDenied
	}
	var constraints struct {
		Secrets []string `json:"secrets"`
	}
	if json.Unmarshal(grant.ConstraintsJSON, &constraints) != nil || !declared || !containsString(constraints.Secrets, secret) {
		return empty, plugin.ErrPermissionDenied
	}
	cipher, err := s.store.GetPluginInstallationSecretEncrypted(ctx, run.PluginID, secret)
	if err != nil {
		return empty, plugin.ErrPermissionDenied
	}
	value, err := security.DecryptSecret(s.sessionSecret, "plugin-secret", cipher)
	if err != nil || value == "" || strings.ContainsAny(value, "\r\n") {
		return empty, plugin.ErrPermissionDenied
	}
	headers := make(map[string]string, len(request.Headers)+1)
	for name, value := range request.Headers {
		if strings.EqualFold(name, "Authorization") {
			return empty, plugin.ErrPermissionDenied
		}
		headers[name] = value
	}
	headers["Authorization"] = "Bearer " + value
	request.Headers = headers
	response, err := pluginnetwork.Do(ctx, request, policy)
	if err != nil {
		return empty, plugin.Coded(model.PluginErrorInvalidInput, "network request failed")
	}
	// A remote endpoint can echo its Authorization header in arbitrary encodings.
	// Secret-bearing integrations expose status only, never response content.
	return pluginnetwork.Response{Status: response.Status}, nil
}
