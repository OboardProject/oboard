package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/version"
)

func (s *Server) mcpLocalhostProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		localAddr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if !ok || localAddr == nil || !mcpIsLoopbackAddress(localAddr.String()) || mcpIsLoopbackAddress(r.Host) {
			next.ServeHTTP(w, r)
			return
		}

		base, err := s.publicBaseURL(r.Context())
		if err == nil {
			if parsed, parseErr := url.Parse(base); parseErr == nil && mcpAuthoritiesEqual(parsed.Scheme, parsed.Host, r.Host) {
				next.ServeHTTP(w, r)
				return
			}
		}
		http.Error(w, fmt.Sprintf("Forbidden: invalid Host header %q", r.Host), http.StatusForbidden)
	})
}

func (s *Server) mcpOriginProtection(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.mcpOriginAllowed(r) {
			http.Error(w, "Forbidden: invalid Origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) mcpOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	if s.allowedOrigins[origin] {
		return true
	}
	originAuthority, ok := canonicalMCPAuthority(parsed.Scheme, parsed.Host)
	if !ok {
		return false
	}
	base, err := s.publicBaseURL(r.Context())
	if err == nil {
		if public, parseErr := url.Parse(base); parseErr == nil {
			if baseAuthority, baseOK := canonicalMCPAuthority(public.Scheme, public.Host); baseOK && originAuthority == baseAuthority {
				return true
			}
		}
	}
	return false
}

func mcpIsLoopbackAddress(authority string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(authority))
	if err != nil {
		host = strings.Trim(strings.TrimSpace(authority), "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func mcpAuthoritiesEqual(scheme, configured, requested string) bool {
	configuredAuthority, ok := canonicalMCPAuthority(scheme, configured)
	if !ok {
		return false
	}
	requestedAuthority, ok := canonicalMCPAuthority(scheme, requested)
	return ok && configuredAuthority == requestedAuthority
}

func canonicalMCPAuthority(scheme, authority string) (string, bool) {
	parsed, err := url.Parse(strings.ToLower(scheme) + "://" + strings.TrimSpace(authority))
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return "", false
	}
	port := parsed.Port()
	if port == "" {
		switch strings.ToLower(scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", false
		}
	}
	return net.JoinHostPort(host, port), true
}

func mcpPrincipal(ctx context.Context) (application.Principal, error) {
	principal, ok := ctx.Value(apiPrincipalContextKey{}).(application.Principal)
	if !ok || principal.ID == "" {
		return application.Principal{}, errors.New("authenticated OBoard principal is required")
	}
	return principal, nil
}

func (s *Server) recordToolCall(ctx context.Context, principal application.Principal, name string, arguments any, result string, classification capability.DataClassification) {
	encoded, _ := json.Marshal(arguments)
	sum := sha256.Sum256(encoded)
	id, err := security.RandomToken(18)
	if err != nil {
		return
	}
	_ = s.store.CreateToolCallAudit(ctx, &model.ToolCallAudit{ID: "tca_" + id, PrincipalID: principal.ID, ClientName: principal.ClientName, Capability: name, DataClassification: string(classification), AffectedResources: json.RawMessage(`{}`), RequestID: "mcp_" + id, ArgumentsHash: hex.EncodeToString(sum[:]), Result: result, SourceIP: principal.SourceIP.String()})
}

func (s *Server) mcpBootstrapContext(ctx context.Context) (map[string]any, error) {
	principal, err := mcpPrincipal(ctx)
	if err != nil {
		return nil, err
	}
	servers := 0
	online := 0
	items, listErr := s.application.ListServers(ctx, principal)
	if listErr == nil {
		servers = len(items)
		for _, item := range items {
			if item.Status == model.ServerOnline {
				online++
			}
		}
	}
	return map[string]any{
		"controller":               map[string]any{"name": "OBoard", "version": version.Version, "base_path": s.basePathState().Current},
		"principal":                map[string]any{"name": principal.Name, "client": principal.ClientName, "grant_id": principal.GrantID, "access_level": principal.AccessLevel, "scopes": principal.Scopes},
		"inventory":                map[string]any{"servers_total": servers, "servers_online": online},
		"fast_path":                map[string]any{"primary_tool": "oboard_task", "commit_tool": "oboard_commit_task", "advanced_tools_only_after": "fallback_required"},
		"workflow_rules":           map[string]any{"write_via_changeset": true, "execution_via_workflow": true, "ssh_supported": false, "shell_supported": false, "admin_deletion_supported": false, "risk4_auto_approval": false},
		"recommended_next_actions": []string{"For a normal request, call oboard_task directly with the user's goal"},
	}, nil
}
