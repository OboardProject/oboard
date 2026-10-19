package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
	"net/http"
	"strconv"
	"time"
)

type proxyDSCPOperation struct {
	ServerID int64 `json:"server_id"`
	Enabled  *bool `json:"enabled,omitempty"`
	Retry    bool  `json:"retry,omitempty"`
}

func (s *Server) readProxyDSCP(ctx context.Context, p application.Principal, id int64) (map[string]any, error) {
	if id <= 0 || !p.AllowsInt64("server_ids", id) {
		return nil, errors.New("authorized server_id required")
	}
	server, err := s.store.GetServer(ctx, id)
	if err != nil {
		return nil, err
	}
	desired, err := s.store.ProxyDSCPDesired(ctx, id)
	if err != nil {
		return nil, err
	}
	single, err := s.store.ProxyDSCPOverride(ctx, id)
	if err != nil {
		return nil, err
	}
	report, err := s.store.ProxyDSCPReport(ctx, id)
	if err != nil {
		return nil, err
	}
	global := settingBool(s.runtimeSettings(ctx), store.ProxyDSCPGlobalSetting, false)
	effective := global || single
	state := "pending"
	supported := serverSupportsCapability(*server, model.ProxyDSCPCapability) && serverSupportsCapability(*server, model.ProxyDSCPKernelCapability)
	if !supported {
		state = "unsupported"
	} else if server.Status != model.ServerOnline {
		state = "offline"
	} else if report != nil && report.Revision == desired.Revision && report.Enabled == effective {
		state = report.State
	}
	out := map[string]any{"server_id": id, "enabled": single, "global_enabled": global, "effective_enabled": effective, "desired": desired, "report": report, "state": state, "value": 46, "requires_restart": false}
	if task, e := s.store.LatestProxyDSCPAttempt(ctx, id, desired.Revision); e == nil && desired.Revision > 0 {
		out["task_id"] = task.ID
		out["task_status"] = task.Status
		if task.Status == "failed" && state == "pending" {
			out["state"] = "failed"
		}
	}
	return out, nil
}
func (s *Server) registerProxyDSCPOperations() {
	name := "servers.proxy_dscp.update"
	validate := func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var req proxyDSCPOperation
		if err := strictAutomationInput(raw, &req); err != nil {
			return nil, err
		}
		if req.ServerID <= 0 || !p.AllowsInt64("server_ids", req.ServerID) {
			return nil, errors.New("authorized server_id required")
		}
		if req.Enabled == nil && !req.Retry {
			return nil, errors.New("enabled or retry required")
		}
		if _, err := s.store.GetServer(ctx, req.ServerID); err != nil {
			return nil, err
		}
		return map[string]any{"server_id": req.ServerID, "requires_restart": false}, nil
	}
	s.automation.RegisterValidator(name, validate)
	s.automation.RegisterRevisionResolver(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (map[string]string, error) {
		if _, err := validate(ctx, p, raw); err != nil {
			return nil, err
		}
		var req proxyDSCPOperation
		_ = json.Unmarshal(raw, &req)
		server, err := s.store.GetServer(ctx, req.ServerID)
		if err != nil {
			return nil, err
		}
		return map[string]string{"proxy_dscp_instance:" + strconv.FormatInt(req.ServerID, 10): server.CreatedAt.UTC().Format(time.RFC3339Nano) + ":" + server.AgentID}, nil
	})
	s.automation.Register(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		if _, err := validate(ctx, p, raw); err != nil {
			return nil, err
		}
		var req proxyDSCPOperation
		_ = json.Unmarshal(raw, &req)
		server, err := s.store.GetServer(ctx, req.ServerID)
		if err != nil {
			return nil, err
		}
		instance := server.CreatedAt.UTC().Format(time.RFC3339Nano) + ":" + server.AgentID
		if approved, ok := automation.ApprovedResourceRevision(ctx, "proxy_dscp_instance:"+strconv.FormatInt(req.ServerID, 10)); ok && approved != instance {
			return nil, errors.New("approved server instance changed")
		}
		if _, err = s.store.SetServerProxyDSCP(ctx, *server, req.Enabled, req.Retry); err != nil {
			return nil, err
		}
		s.reconcileProxyDSCP(ctx, server, nil)
		return s.readProxyDSCP(ctx, p, server.ID)
	})
}
func (s *Server) reconcileProxyDSCP(ctx context.Context, server *model.Server, report *model.ProxyDSCPReport) {
	s.proxyDSCPMu.Lock()
	defer s.proxyDSCPMu.Unlock()
	if !s.runtimeSecurityServerCurrent(ctx, server) {
		return
	}
	desired, err := s.store.ProxyDSCPDesired(ctx, server.ID)
	if err != nil {
		return
	}
	global := settingBool(s.runtimeSettings(ctx), store.ProxyDSCPGlobalSetting, false)
	single, err := s.store.ProxyDSCPOverride(ctx, server.ID)
	if err != nil {
		return
	}
	if desired.Enabled != (global || single) || (desired.Revision == 0 && report != nil && report.Enabled) {
		desired, err = s.store.SetServerProxyDSCP(ctx, *server, nil, report != nil && report.Enabled)
		if err != nil {
			return
		}
	}
	if validProxyDSCPReport(report) {
		if err = s.store.ObserveProxyDSCPReport(ctx, *server, *report); err != nil {
			return
		}
		if report.Revision == desired.Revision && report.Enabled == desired.Enabled {
			return
		}
	}
	if desired.Revision == 0 || server.Status != model.ServerOnline || !serverSupportsCapability(*server, model.ProxyDSCPCapability) || !serverSupportsCapability(*server, model.ProxyDSCPKernelCapability) {
		return
	}
	// Binary capabilities alone do not mean the kernel local API is ready.
	if !validProxyDSCPReport(report) {
		return
	}
	if _, err = s.store.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeProxyDSCP); err == nil || !errors.Is(err, sql.ErrNoRows) {
		return
	}
	if _, err = s.store.LatestProxyDSCPAttempt(ctx, server.ID, desired.Revision); err == nil || !errors.Is(err, sql.ErrNoRows) {
		return
	}
	raw, _ := json.Marshal(desired)
	nonce, err := security.RandomToken(12)
	if err != nil {
		return
	}
	task := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeProxyDSCP, PayloadJSON: string(raw), Nonce: nonce, Status: "pending", ResultJSON: "{}"}
	if err = s.store.CreateProxyDSCPTask(ctx, *server, &task); err == nil {
		s.tasks.wake(server.ID)
	}
}
func validProxyDSCPReport(r *model.ProxyDSCPReport) bool {
	if r == nil || r.Value != 46 || r.Revision < 0 || len(r.Error) > 256 || len(r.Excluded) > 16 {
		return false
	}
	switch r.State {
	case "partial", "disabled", "unsupported":
	default:
		return false
	}
	for _, v := range r.Excluded {
		if len(v) > 64 {
			return false
		}
	}
	return true
}
func (s *Server) apiProxyDSCP(w http.ResponseWriter, r *http.Request, p application.Principal, id int64) {
	if r.Method == http.MethodGet {
		value, err := s.readProxyDSCP(r.Context(), p, id)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, 200, value, nil)
		return
	}
	if r.Method != http.MethodPost {
		v2Error(w, r, 405, "method_not_allowed", "不支持该请求方法")
		return
	}
	if !p.Interactive {
		v2Error(w, r, 403, "changeset_required", "自动化写入请使用对应能力和 Changeset")
		return
	}
	var input struct {
		Enabled   *bool  `json:"enabled"`
		Retry     bool   `json:"retry"`
		RequestID string `json:"request_id"`
	}
	if !decodeV2(w, r, &input) {
		return
	}
	if len(input.RequestID) < 8 || len(input.RequestID) > 80 {
		v2Error(w, r, 400, "request_id_required", "需要请求标识")
		return
	}
	name := "servers.proxy_dscp.update"
	if _, ok := s.capabilities.Authorize(p, name); !ok {
		v2Error(w, r, 403, "capability_denied", "没有代理 DSCP 管理权限")
		return
	}
	raw, _ := json.Marshal(proxyDSCPOperation{ServerID: id, Enabled: input.Enabled, Retry: input.Retry})
	cs, err := s.applyConfirmedChangeset(r.Context(), p, []automation.OperationRequest{{Capability: name, Input: raw, ResourceRefs: json.RawMessage("{}")}}, fmt.Sprintf("proxy-dscp:%d:%s", id, input.RequestID), "设置代理 DSCP")
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if cs == nil || cs.Status != model.ChangesetSucceeded {
		v2Error(w, r, 409, "changeset_not_applied", "DSCP 设置尚未应用，请查看审批记录")
		return
	}
	value, err := s.readProxyDSCP(r.Context(), p, id)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	v2Write(w, r, 202, value, nil)
}
