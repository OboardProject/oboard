package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/ipquality"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/plugin"
	"github.com/OboardProject/oboard/internal/store"
	"net/http"
	"os"
	"strconv"
	"time"
)

type probeMonitorInput struct {
	ID              int64  `json:"id,omitempty"`
	ServerID        int64  `json:"server_id"`
	Revision        int64  `json:"revision,omitempty"`
	Name            string `json:"name,omitempty"`
	Kind            string `json:"kind,omitempty"`
	Target          string `json:"target,omitempty"`
	Mode            string `json:"mode,omitempty"`
	Port            int    `json:"port,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	Enabled         bool   `json:"enabled"`
	Before          int64  `json:"before,omitempty"`
}

func (v probeMonitorInput) model() model.ProbeMonitor {
	return model.ProbeMonitor{ID: v.ID, ServerID: v.ServerID, Revision: v.Revision, Name: v.Name, Kind: v.Kind, Target: v.Target, Mode: v.Mode, Port: v.Port, IntervalSeconds: v.IntervalSeconds, Enabled: v.Enabled}
}

func (s *Server) probeMonitorBoundary(ctx context.Context, p application.Principal, input probeMonitorInput) (*model.ProbeMonitor, *model.Server, error) {
	if input.ServerID <= 0 || !p.AllowsInt64("server_ids", input.ServerID) {
		return nil, nil, errors.New("服务器超出授权范围")
	}
	server, err := s.store.GetServer(ctx, input.ServerID)
	if err != nil {
		return nil, nil, err
	}
	if input.ID == 0 {
		return nil, server, nil
	}
	tasks, err := s.store.ListProbeMonitors(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, task := range tasks {
		if task.ID == input.ID && task.ServerID == input.ServerID {
			return &task, server, nil
		}
	}
	return nil, nil, errors.New("监控任务不存在")
}
func (s *Server) queryProbeMonitors(ctx context.Context, p application.Principal, name string, raw json.RawMessage) (any, error) {
	if name == "probe_monitors.list" {
		if err := strictAutomationInput(raw, &struct{}{}); err != nil {
			return nil, err
		}
		tasks, err := s.store.ListProbeMonitors(ctx)
		if err != nil {
			return nil, err
		}
		visible := []model.ProbeMonitor{}
		for _, task := range tasks {
			if p.AllowsInt64("server_ids", task.ServerID) {
				visible = append(visible, task)
			}
		}
		return map[string]any{"monitors": visible, "providers": map[string]bool{"ipinfo": os.Getenv("OBOARD_IPINFO_TOKEN") != "", "proxycheck": true, "abuseipdb": os.Getenv("OBOARD_ABUSEIPDB_KEY") != ""}}, nil
	}
	var input probeMonitorInput
	if err := strictAutomationInput(raw, &input); err != nil {
		return nil, err
	}
	if input.ID <= 0 {
		return nil, errors.New("任务 ID 无效")
	}
	if _, _, err := s.probeMonitorBoundary(ctx, p, input); err != nil {
		return nil, err
	}
	versions, err := s.store.ProbeRouteHistory(ctx, input.ID, input.Before)
	return map[string]any{"versions": versions}, err
}
func (s *Server) registerProbeMonitorOperations() {
	for _, action := range []string{"save", "delete", "run"} {
		name := "probe_monitors." + action
		validate := func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
			var input probeMonitorInput
			if err := strictAutomationInput(raw, &input); err != nil {
				return nil, err
			}
			current, _, err := s.probeMonitorBoundary(ctx, p, input)
			if err != nil {
				return nil, err
			}
			if input.ID < 0 || input.Before != 0 {
				return nil, errors.New("任务参数无效")
			}
			if current != nil && input.Revision != current.Revision {
				return nil, errors.New("任务已改变，请刷新")
			}
			if action != "save" && current == nil {
				return nil, errors.New("任务不存在")
			}
			if action == "save" {
				task := input.model()
				if err = store.ValidateProbeMonitor(&task); err != nil {
					return nil, err
				}
				if task.Kind == "route" {
					if _, err = plugin.ValidateNetworkTarget(task.Target); err != nil {
						return nil, err
					}
				} else if task.Target != "" && !ipquality.PublicIP(task.Target) {
					return nil, errors.New("IP 质量监控只接受公网 IP")
				}
				if current != nil && (task.Target != current.Target || task.Mode != current.Mode || task.Kind != current.Kind || task.Port != current.Port) {
					return nil, errors.New("更换目标或协议请新建任务，以保留原路由历史")
				}
			}
			return map[string]any{"server_id": input.ServerID, "id": input.ID}, nil
		}
		s.automation.RegisterValidator(name, validate)
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (map[string]string, error) {
			var input probeMonitorInput
			if err := strictAutomationInput(raw, &input); err != nil {
				return nil, err
			}
			task, server, err := s.probeMonitorBoundary(ctx, p, input)
			if err != nil {
				return nil, err
			}
			refs := map[string]string{"probe_server:" + strconv.FormatInt(server.ID, 10): server.CreatedAt.UTC().Format(time.RFC3339Nano) + ":" + server.AgentID}
			if task != nil {
				refs["probe_monitor:"+strconv.FormatInt(task.ID, 10)] = strconv.FormatInt(task.Revision, 10)
			}
			return refs, nil
		})
		s.automation.Register(name, func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
			if _, err := validate(ctx, p, raw); err != nil {
				return nil, err
			}
			var input probeMonitorInput
			_ = json.Unmarshal(raw, &input)
			_, server, err := s.probeMonitorBoundary(ctx, p, input)
			if err != nil {
				return nil, err
			}
			if approved, ok := automation.ApprovedResourceRevision(ctx, "probe_server:"+strconv.FormatInt(server.ID, 10)); ok && approved != server.CreatedAt.UTC().Format(time.RFC3339Nano)+":"+server.AgentID {
				return nil, errors.New("服务器身份已改变")
			}
			if action == "save" {
				task := input.model()
				if err = store.ValidateProbeMonitor(&task); err != nil {
					return nil, err
				}
				if err = s.store.SaveProbeMonitor(ctx, *server, &task); err != nil {
					return nil, err
				}
				return map[string]any{"id": task.ID, "revision": task.Revision}, nil
			}
			err = s.store.MutateProbeMonitor(ctx, input.ID, input.Revision, action)
			return map[string]any{"id": input.ID, "revision": input.Revision}, err
		})
	}
}

func (s *Server) apiProbeMonitors(w http.ResponseWriter, r *http.Request) {
	p, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		name := "probe_monitors.list"
		raw := json.RawMessage(`{}`)
		if r.URL.Query().Get("id") != "" {
			name = "probe_monitors.history"
			id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
			sid, _ := strconv.ParseInt(r.URL.Query().Get("server_id"), 10, 64)
			before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
			raw, _ = json.Marshal(probeMonitorInput{ID: id, ServerID: sid, Before: before})
		}
		if _, ok := s.capabilities.Authorize(p, name); !ok {
			v2Error(w, r, 403, "capability_denied", "没有读取权限")
			return
		}
		value, err := s.queryProbeMonitors(r.Context(), p, name, raw)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, 200, value, nil)
		return
	}
	if r.Method != http.MethodPost {
		v2Error(w, r, 405, "method_not_allowed", "不支持该方法")
		return
	}
	if !p.Interactive {
		v2Error(w, r, 403, "changeset_required", "请通过能力与 Changeset 修改监控任务")
		return
	}
	var req struct {
		Action    string          `json:"action"`
		RequestID string          `json:"request_id"`
		Input     json.RawMessage `json:"input"`
	}
	if !decodeV2(w, r, &req) {
		return
	}
	if (req.Action != "save" && req.Action != "run" && req.Action != "delete") || len(req.RequestID) < 8 || len(req.RequestID) > 80 {
		v2Error(w, r, 400, "invalid_input", "操作或请求标识无效")
		return
	}
	name := "probe_monitors." + req.Action
	if _, ok := s.capabilities.Authorize(p, name); !ok {
		v2Error(w, r, 403, "capability_denied", "没有管理权限")
		return
	}
	result, err := s.applyConfirmedChangeset(r.Context(), p, []automation.OperationRequest{{Capability: name, Input: req.Input, ResourceRefs: json.RawMessage(`{}`)}}, fmt.Sprintf("probe-monitor:%s", req.RequestID), "拨测监控任务")
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if result == nil || result.Status != model.ChangesetSucceeded {
		v2Error(w, r, 409, "changeset_not_applied", "操作未应用，请查看审批记录")
		return
	}
	v2Write(w, r, 200, map[string]bool{"applied": true}, nil)
}
