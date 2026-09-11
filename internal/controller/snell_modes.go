package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"net/http"
	"strings"
)

var errSnellModeForbidden = errors.New("inbound outside authorized server boundary")

type snellModeRequest struct {
	InboundID     int64  `json:"inbound_id"`
	ListenerMode  string `json:"listener_mode"`
	PreviewDigest string `json:"preview_digest,omitempty"`
}

func (s *Server) previewSnellMode(ctx context.Context, principal application.Principal, req snellModeRequest) (core.SnellListenerPreview, *model.Inbound, error) {
	var empty core.SnellListenerPreview
	inbound, err := s.store.GetInbound(ctx, req.InboundID)
	if err != nil {
		return empty, nil, err
	}
	if !model.HasManagementAccess(principal.Role) || !principal.AllowsInt64("server_ids", inbound.ServerID) {
		return empty, nil, errSnellModeForbidden
	}
	server, err := s.store.GetServer(ctx, inbound.ServerID)
	if err != nil {
		return empty, nil, err
	}
	s.annotateOneServerDeliveryStatus(ctx, server)
	data, err := s.store.FullRoutingConfigData(ctx)
	if err != nil {
		return empty, nil, err
	}
	data, err = s.loadProxyCredentialData(ctx, data)
	if err != nil {
		return empty, nil, err
	}
	bindings, pathBindings, policies, err := s.runtimeAccessBindings(ctx, data)
	if err != nil {
		return empty, nil, err
	}
	preview, err := core.PreviewSnellListener(*inbound, *server, req.ListenerMode, data.Users, core.ConfigOptions{Servers: data.Servers, Inbounds: data.Inbounds, InboundUsers: bindings, ProxyPathUsers: pathBindings, ProxyPaths: data.ProxyPaths, ProxyPathSteps: data.ProxyPathSteps, UserPolicies: policies, PortLedger: core.NewProxyPathPortLedger(data.ProxyPathPortAllocations)})
	if err != nil {
		return preview, nil, err
	}
	next := *inbound
	var cfg map[string]any
	_ = json.Unmarshal([]byte(next.ConfigJSON), &cfg)
	if cfg == nil {
		cfg = map[string]any{}
	}
	cfg["listener_mode"] = req.ListenerMode
	raw, _ := json.Marshal(cfg)
	next.ConfigJSON = string(raw)
	next, err = normalizeInboundAutomationCandidate(next, inbound)
	if err != nil {
		return preview, nil, err
	}
	if err = s.validateInboundAutomationCandidate(ctx, principal, &next); err != nil {
		return preview, nil, err
	}
	revision, err := s.store.RoutingTopologyRevision(ctx)
	if err != nil {
		return preview, nil, err
	}
	type credentialScope struct {
		ID          int64
		Status      string
		Credentials []model.ProxyCredential
	}
	scopes := make([]credentialScope, 0, len(data.Users))
	for _, u := range data.Users {
		scopes = append(scopes, credentialScope{u.ID, u.Status, u.ProxyCredentials})
	}
	payload, _ := json.Marshal(struct {
		Preview        core.SnellListenerPreview
		Revision       string
		InboundUpdated string
		Credentials    []credentialScope
		Bindings       any
		PathBindings   any
	}{preview, revision, inbound.UpdatedAt.String(), scopes, bindings, pathBindings})
	preview.PreviewDigest = fmt.Sprintf("%x", sha256.Sum256(payload))
	return preview, &next, nil
}
func (s *Server) applySnellMode(ctx context.Context, principal application.Principal, req snellModeRequest) (any, error) {
	preview, next, err := s.previewSnellMode(ctx, principal, req)
	if err != nil {
		return nil, err
	}
	if !preview.CapabilityReady {
		return nil, core.ErrSnellUnsupported
	}
	if req.PreviewDigest == "" || req.PreviewDigest != preview.PreviewDigest {
		return nil, core.ErrSnellModeChange
	}
	if err = s.store.UpdateInbound(ctx, next); err != nil {
		return nil, err
	}
	return map[string]any{"inbound": automationInboundView(*next), "preview": preview, "requires_deployment": preview.RequiresRestart}, nil
}
func (s *Server) registerSnellModeOperations() {
	const name = "inbounds.listener_mode.apply"
	s.automation.RegisterValidator(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var req snellModeRequest
		if err := strictAutomationInput(raw, &req); err != nil {
			return nil, err
		}
		preview, _, err := s.previewSnellMode(ctx, p, req)
		if err == nil && (req.PreviewDigest == "" || req.PreviewDigest != preview.PreviewDigest) {
			err = core.ErrSnellModeChange
		}
		if err == nil && !preview.CapabilityReady {
			err = core.ErrSnellUnsupported
		}
		return preview, err
	})
	s.automation.RegisterRevisionResolver(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (map[string]string, error) {
		var req snellModeRequest
		if err := strictAutomationInput(raw, &req); err != nil {
			return nil, err
		}
		preview, _, err := s.previewSnellMode(ctx, p, req)
		if err != nil {
			return nil, err
		}
		return s.inboundAutomationRevisions(ctx, p, req.InboundID, preview.ServerID)
	})
	s.automation.Register(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var req snellModeRequest
		if err := strictAutomationInput(raw, &req); err != nil {
			return nil, err
		}
		return s.applySnellMode(ctx, p, req)
	})
}
func (s *Server) snellModeHTTP(w http.ResponseWriter, r *http.Request, inboundID int64, apply bool) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req snellModeRequest
	if !decode(w, r, &req) {
		return
	}
	req.InboundID = inboundID
	principal := subscriptionCustomPathPrincipal(r)
	var out any
	var err error
	if apply {
		out, err = s.applySnellMode(r.Context(), principal, req)
	} else {
		out, _, err = s.previewSnellMode(r.Context(), principal, req)
	}
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, errSnellModeForbidden) {
			status = http.StatusForbidden
		}
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		write(w, status, map[string]any{"error": snellErrorCode(err), "message": err.Error()})
		return
	}
	if apply {
		auditReq(s, r, "snell.listener_mode", "inbound", fmt.Sprint(inboundID))
		s.signalConfigurationReconcile()
	}
	write(w, http.StatusOK, out)
}
func snellErrorCode(err error) string {
	for _, code := range []string{"snell_multi_psk_unsupported", "snell_duplicate_psk", "snell_credential_limit_exceeded", "snell_unsafe_mode_not_allowed", "snell_listener_mode_change_required", "snell_runtime_not_confirmed"} {
		if strings.Contains(err.Error(), code) {
			return code
		}
	}
	return "validation_failed"
}
func validateSnellModeMutation(current, next model.Inbound) error {
	if current.Protocol == model.ProtocolSnell && next.Protocol == model.ProtocolSnell && core.SnellListenerMode(current) != core.SnellListenerMode(next) {
		return core.ErrSnellModeChange
	}
	return nil
}
