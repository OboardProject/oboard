package controller

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"net/http"
	"strconv"
	"time"
)

type runtimeSecurityOperation struct {
	ServerID int64  `json:"server_id"`
	Mode     string `json:"mode,omitempty"`
}

func (s *Server) readRuntimeSecurity(ctx context.Context, p application.Principal, id int64) (map[string]any, error) {
	if id <= 0 || !p.AllowsInt64("server_ids", id) {
		return nil, errors.New("authorized server_id is required")
	}
	server, err := s.store.GetServer(ctx, id)
	if err != nil {
		return nil, err
	}
	desired, err := s.store.RuntimeSecurityDesired(ctx, id)
	if err != nil {
		return nil, err
	}
	report, err := s.store.RuntimeSecurityReport(ctx, id)
	if err != nil {
		return nil, err
	}
	value := map[string]any{"server_id": id, "desired": desired, "report": report, "online": server.Status == model.ServerOnline, "queued": false, "requires_restart": true}
	if task, err := s.store.ActiveTaskByServerType(ctx, id, model.AgentTaskTypeRuntimeSecurity); err == nil {
		value["task_id"] = task.ID
		value["task_status"] = task.Status
		value["queued"] = true
	}
	if task, err := s.store.LatestTaskByServerType(ctx, id, model.AgentTaskTypeRuntimeSecurity); err == nil {
		value["last_task_id"], value["last_task_status"] = task.ID, task.Status
	}
	return value, nil
}
func decodeRuntimeSecurityOperation(raw json.RawMessage, modeRequired bool) (runtimeSecurityOperation, error) {
	var req runtimeSecurityOperation
	if err := strictAutomationInput(raw, &req); err != nil {
		return req, err
	}
	if req.ServerID <= 0 {
		return req, errors.New("positive server_id is required")
	}
	if modeRequired {
		if req.Mode != "standard" && req.Mode != "enhanced" {
			return req, errors.New("mode must be standard or enhanced")
		}
	} else if req.Mode != "" {
		return req, errors.New("check does not accept mode")
	}
	return req, nil
}
func (s *Server) registerRuntimeSecurityOperations() {
	for _, action := range []string{"update", "check"} {
		name := "servers.runtime_security." + action
		validate := func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
			req, err := decodeRuntimeSecurityOperation(raw, action == "update")
			if err != nil {
				return nil, err
			}
			if !p.AllowsInt64("server_ids", req.ServerID) {
				return nil, errors.New("authorized server_id is required")
			}
			if _, err := s.store.GetServer(ctx, req.ServerID); err != nil {
				return nil, err
			}
			report, err := s.store.RuntimeSecurityReport(ctx, req.ServerID)
			if err != nil {
				return nil, err
			}
			if req.Mode == "standard" && report != nil && report.LocalPolicy == model.RemoteAccessModeHardened {
				return nil, errors.New("本地 Hardened 策略禁止远程降级；请由主机管理员在本地处理")
			}
			return map[string]any{"server_id": req.ServerID, "mode": req.Mode, "requires_restart": action == "update"}, nil
		}
		s.automation.RegisterValidator(name, validate)
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (map[string]string, error) {
			req, err := decodeRuntimeSecurityOperation(raw, action == "update")
			if err != nil || !p.AllowsInt64("server_ids", req.ServerID) {
				return nil, errors.New("authorized server_id is required")
			}
			desired, err := s.store.RuntimeSecurityDesired(ctx, req.ServerID)
			if err != nil {
				return nil, err
			}
			return map[string]string{"runtime_security:" + strconv.FormatInt(req.ServerID, 10): strconv.FormatInt(desired.Revision, 10)}, nil
		})
		s.automation.Register(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
			if _, err := validate(ctx, p, raw); err != nil {
				return nil, err
			}
			req, _ := decodeRuntimeSecurityOperation(raw, action == "update")
			s.runtimeSecurityQueueMu.Lock()
			defer s.runtimeSecurityQueueMu.Unlock()
			server, err := s.store.GetServer(ctx, req.ServerID)
			if err != nil {
				return nil, err
			}
			desired, err := s.store.RuntimeSecurityDesired(ctx, req.ServerID)
			if err != nil {
				return nil, err
			}
			if server.Status == model.ServerOnline && server.AgentID != "" && !serverSupportsCapability(*server, model.RuntimeSecurityCapability) {
				return nil, errors.New("请先更新 Agent，当前节点不支持运行安全检查")
			}
			if action == "update" {
				// Explicit reapply gets a new revision; Changeset idempotency prevents a
				// transport retry from performing this operation twice.
				revision := time.Now().UnixMilli()
				if revision <= desired.Revision {
					revision = desired.Revision + 1
				}
				desired = model.RuntimeSecurityRequest{Mode: req.Mode, Revision: revision}
				if err := s.store.SaveRuntimeSecurityDesired(ctx, req.ServerID, desired); err != nil {
					return nil, err
				}
			}
			if action == "check" {
				desired = model.RuntimeSecurityRequest{}
			}
			if server.Status == model.ServerOnline && server.AgentID != "" {
				if !serverSupportsCapability(*server, model.RuntimeSecurityCapability) {
					return nil, errors.New("请先更新 Agent，当前节点不支持运行安全检查")
				}
				if _, err := s.queueRuntimeSecurity(ctx, *server, desired); err != nil {
					return nil, err
				}
			} else if action == "check" {
				return nil, errors.New("Agent 离线，暂时无法检查")
			}
			return s.readRuntimeSecurity(ctx, p, req.ServerID)
		})
	}
}
func (s *Server) queueRuntimeSecurity(ctx context.Context, server model.Server, desired model.RuntimeSecurityRequest) (model.AgentTask, error) {
	active, err := s.store.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeRuntimeSecurity)
	if err == nil {
		return *active, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.AgentTask{}, err
	}
	if desired.Revision > 0 {
		latest, readErr := s.store.LatestTaskByServerType(ctx, server.ID, model.AgentTaskTypeRuntimeSecurity)
		if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
			return model.AgentTask{}, readErr
		}
		if readErr == nil {
			var attempted model.RuntimeSecurityRequest
			if json.Unmarshal([]byte(latest.PayloadJSON), &attempted) == nil && attempted.Revision >= desired.Revision {
				return *latest, nil
			}
		}
	}
	return s.queueAgentTask(ctx, server.ID, model.AgentTaskTypeRuntimeSecurity, desired, 0)
}
func (s *Server) recordRuntimeSecurity(ctx context.Context, server *model.Server, report *model.RuntimeSecurityReport) {
	if report == nil || !validRuntimeSecurityReport(*report) {
		return
	}
	s.runtimeSecurityQueueMu.Lock()
	defer s.runtimeSecurityQueueMu.Unlock()
	previousReport, err := s.store.RuntimeSecurityReport(ctx, server.ID)
	if err != nil {
		return
	}
	if previousReport != nil && (report.Revision < previousReport.Revision || report.CheckedAt.Before(previousReport.CheckedAt)) {
		return
	}
	raw, _ := json.Marshal(report)
	hash := sha256.Sum256(append([]byte(server.AgentID), raw...))
	if previous, ok := s.runtimeSecurityReports.Load(server.ID); !ok || previous != hash {
		if err := s.store.SaveRuntimeSecurityReport(ctx, server.ID, *report); err != nil {
			return
		}
		s.runtimeSecurityReports.Store(server.ID, hash)
	}
	desired, err := s.store.RuntimeSecurityDesired(ctx, server.ID)
	if err == nil && server.Status == model.ServerOnline && desired.Revision > report.Revision && serverSupportsCapability(*server, model.RuntimeSecurityCapability) {
		_, _ = s.queueRuntimeSecurity(ctx, *server, desired)
	}
}
func validRuntimeSecurityReport(report model.RuntimeSecurityReport) bool {
	if report.Revision < 0 || len(report.Checks) > 64 || len(report.Capabilities) > 32 || report.CheckedAt.IsZero() || report.CheckedAt.After(time.Now().Add(2*time.Minute)) {
		return false
	}
	if report.DesiredMode != "standard" && report.DesiredMode != "enhanced" {
		return false
	}
	if report.ActualMode != "standard" && report.ActualMode != "enhanced" && report.ActualMode != "unknown" {
		return false
	}
	switch report.State {
	case "standard", "applying", "enhanced", "partial", "unsupported", "failed":
	default:
		return false
	}
	if len(report.ErrorCode) > 128 || len(report.Phase) > 32 || len(report.Platform) > 32 || len(report.LocalPolicy) > 32 {
		return false
	}
	for _, item := range report.Capabilities {
		if len(item) > 64 {
			return false
		}
	}
	for _, check := range report.Checks {
		if len(check.ID) > 64 || len(check.Category) > 32 || len(check.Severity) > 16 || len(check.Message) > 512 || len(check.Remedy) > 512 {
			return false
		}
		switch check.Status {
		case "passed", "warning", "failed", "unsupported", "unknown":
		default:
			return false
		}
	}
	return true
}
func (s *Server) apiRuntimeSecurity(w http.ResponseWriter, r *http.Request, p application.Principal, id int64) {
	if r.Method == http.MethodGet {
		value, err := s.readRuntimeSecurity(r.Context(), p, id)
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
		Mode      string `json:"mode"`
		Check     bool   `json:"check"`
		RequestID string `json:"request_id"`
	}
	if !decodeV2(w, r, &input) {
		return
	}
	if len(input.RequestID) < 8 || len(input.RequestID) > 80 {
		v2Error(w, r, 400, "request_id_required", "需要请求标识")
		return
	}
	name := "servers.runtime_security.update"
	if input.Check {
		name = "servers.runtime_security.check"
		if input.Mode != "" {
			v2Error(w, r, 400, "invalid_input", "检查不接受模式变更")
			return
		}
	}
	if _, allowed := s.capabilities.Authorize(p, name); !allowed {
		v2Error(w, r, 403, "capability_denied", "没有运行安全管理权限")
		return
	}
	raw, _ := json.Marshal(runtimeSecurityOperation{ServerID: id, Mode: input.Mode})
	changeset, err := s.applyConfirmedChangeset(r.Context(), p, []automation.OperationRequest{{Capability: name, Input: raw, ResourceRefs: json.RawMessage("{}")}}, fmt.Sprintf("runtime-security:%d:%s", id, input.RequestID), "服务器运行安全操作")
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	value, err := s.readRuntimeSecurity(r.Context(), p, id)
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if changeset == nil || changeset.Status != model.ChangesetSucceeded {
		v2Error(w, r, http.StatusConflict, "changeset_not_applied", "运行安全操作未应用，请查看操作审批与执行记录")
		return
	}
	value["changeset"] = changeset
	v2Write(w, r, http.StatusAccepted, value, nil)
}

func (s *Server) reconcileRuntimeSecurityDesired(ctx context.Context, server *model.Server) {
	s.runtimeSecurityQueueMu.Lock()
	defer s.runtimeSecurityQueueMu.Unlock()
	desired, err := s.store.RuntimeSecurityDesired(ctx, server.ID)
	if err != nil || desired.Revision == 0 {
		return
	}
	report, err := s.store.RuntimeSecurityReport(ctx, server.ID)
	if err != nil {
		return
	}
	if report != nil && report.Revision >= desired.Revision {
		return
	}
	if server.Status == model.ServerOnline && serverSupportsCapability(*server, model.RuntimeSecurityCapability) {
		_, _ = s.queueRuntimeSecurity(ctx, *server, desired)
	}
}
