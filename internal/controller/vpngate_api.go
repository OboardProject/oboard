package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

type vpngatePoolInput struct {
	ID              int64    `json:"id,omitempty"`
	Name            string   `json:"name"`
	Countries       []string `json:"countries,omitempty"`
	OperatorASNs    []string `json:"operator_asns,omitempty"`
	Network         string   `json:"network,omitempty"`
	SelectMode      string   `json:"select_mode,omitempty"`
	MaxRTTMS        int      `json:"max_rtt_ms,omitempty"`
	MinQualityScore int      `json:"min_quality_score,omitempty"`
	HotStandby      int      `json:"hot_standby,omitempty"`
	PoolSize        int      `json:"pool_size,omitempty"`
	SwitchPolicy    string   `json:"switch_policy,omitempty"`
	RefreshMinutes  int      `json:"refresh_minutes,omitempty"`
	Enabled         *bool    `json:"enabled,omitempty"`
}

func (v vpngatePoolInput) model() model.VPNGatePool {
	enabled := true
	if v.Enabled != nil {
		enabled = *v.Enabled
	}
	return model.VPNGatePool{ID: v.ID, Name: v.Name, Countries: v.Countries, OperatorASNs: v.OperatorASNs, Network: v.Network, SelectMode: v.SelectMode, MaxRTTMS: v.MaxRTTMS, MinQualityScore: v.MinQualityScore, HotStandby: v.HotStandby, PoolSize: v.PoolSize, SwitchPolicy: v.SwitchPolicy, RefreshMinutes: v.RefreshMinutes, Enabled: enabled}
}

type vpngateIDInput struct {
	ID             int64 `json:"id"`
	RefreshCatalog bool  `json:"refresh_catalog,omitempty"`
}

type vpngateCatalogInput struct {
	Country string `json:"country,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type vpngatePoolView struct {
	model.VPNGatePool
	InUse   bool                      `json:"in_use"`
	Servers []model.VPNGatePoolServer `json:"servers"`
}

func (s *Server) queryVPNGate(ctx context.Context, p application.Principal, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "vpngate.overview":
		if err := strictAutomationInput(raw, &struct{}{}); err != nil {
			return nil, err
		}
		return s.vpngateOverview(ctx, p)
	case "vpngate.catalog.list":
		var input vpngateCatalogInput
		if err := strictAutomationInput(raw, &input); err != nil {
			return nil, err
		}
		return s.vpngateCatalogList(ctx, input)
	}
	return nil, fmt.Errorf("unknown capability %s", name)
}

func (s *Server) vpngateOverview(ctx context.Context, p application.Principal) (any, error) {
	pools, err := s.store.ListVPNGatePools(ctx)
	if err != nil {
		return nil, err
	}
	states, err := s.store.ListVPNGatePoolServers(ctx)
	if err != nil {
		return nil, err
	}
	catalog, err := s.store.GetVPNGateCatalogState(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]vpngatePoolView, 0, len(pools))
	for _, pool := range pools {
		inUse, err := s.store.VPNGatePoolReferenced(ctx, pool.ID)
		if err != nil {
			return nil, err
		}
		view := vpngatePoolView{VPNGatePool: pool, InUse: inUse, Servers: []model.VPNGatePoolServer{}}
		for _, state := range states {
			if state.PoolID == pool.ID && p.AllowsInt64("server_ids", state.ServerID) {
				s.vpngateOverlayStatus(&state)
				view.Servers = append(view.Servers, state)
			}
		}
		views = append(views, view)
	}
	configured := map[string]bool{}
	for _, h := range s.ipDataService(ctx).Health() {
		configured[h.Name] = h.Configured
	}
	return map[string]any{
		"pools":     views,
		"catalog":   catalog,
		"providers": configured,
	}, nil
}

func (s *Server) vpngateCatalogList(ctx context.Context, input vpngateCatalogInput) (any, error) {
	country := strings.ToUpper(strings.TrimSpace(input.Country))
	limit := input.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	nodes, err := s.store.ListVPNGateNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.VPNGateNode, 0, 500)
	for _, node := range nodes {
		if country != "" && node.CountryCode != country {
			continue
		}
		out = append(out, node)
		if len(out) >= limit {
			break
		}
	}
	return map[string]any{"nodes": out, "total": len(nodes)}, nil
}

func (s *Server) registerVPNGateOperations() {
	s.automation.Register("vpngate.pools.save", func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var input vpngatePoolInput
		if err := strictAutomationInput(raw, &input); err != nil {
			return nil, err
		}
		pool := input.model()
		if pool.ID == 0 {
			if err := s.store.CreateVPNGatePool(ctx, &pool); err != nil {
				return nil, err
			}
		} else {
			if _, err := s.store.GetVPNGatePool(ctx, pool.ID); err != nil {
				return nil, errors.New("出口池不存在")
			}
			if err := s.store.UpdateVPNGatePool(ctx, &pool); err != nil {
				return nil, err
			}
			s.vpngateRequestReselect(ctx, pool.ID)
		}
		s.WakeVPNGate()
		return map[string]any{"id": pool.ID}, nil
	})
	s.automation.RegisterValidator("vpngate.pools.save", func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var input vpngatePoolInput
		if err := strictAutomationInput(raw, &input); err != nil {
			return nil, err
		}
		pool := input.model()
		if err := core.NormalizeVPNGatePool(&pool); err != nil {
			return nil, err
		}
		if pool.ID != 0 {
			if _, err := s.store.GetVPNGatePool(ctx, pool.ID); err != nil {
				return nil, errors.New("出口池不存在")
			}
		}
		return map[string]any{"id": pool.ID}, nil
	})
	s.automation.Register("vpngate.pools.delete", func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var input vpngateIDInput
		if err := strictAutomationInput(raw, &input); err != nil {
			return nil, err
		}
		if err := s.store.DeleteVPNGatePool(ctx, input.ID); err != nil {
			return nil, err
		}
		return map[string]any{"id": input.ID}, nil
	})
	s.automation.RegisterValidator("vpngate.pools.delete", func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var input vpngateIDInput
		if err := strictAutomationInput(raw, &input); err != nil {
			return nil, err
		}
		if _, err := s.store.GetVPNGatePool(ctx, input.ID); err != nil {
			return nil, errors.New("出口池不存在")
		}
		inUse, err := s.store.VPNGatePoolReferenced(ctx, input.ID)
		if err != nil {
			return nil, err
		}
		if inUse {
			return nil, errors.New("出口池仍被代理拓扑使用，请先移除相关出口节点")
		}
		return map[string]any{"id": input.ID}, nil
	})
	s.automation.Register("vpngate.pools.reselect", func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var input vpngateIDInput
		if err := strictAutomationInput(raw, &input); err != nil {
			return nil, err
		}
		if _, err := s.store.GetVPNGatePool(ctx, input.ID); err != nil {
			return nil, errors.New("出口池不存在")
		}
		if input.RefreshCatalog {
			if err := s.vpngateRefreshCatalog(ctx, true); err != nil {
				return nil, err
			}
		}
		s.vpngateRequestReselect(ctx, input.ID)
		s.WakeVPNGate()
		return map[string]any{"id": input.ID}, nil
	})
	s.automation.RegisterValidator("vpngate.pools.reselect", func(ctx context.Context, p application.Principal, raw json.RawMessage) (any, error) {
		var input vpngateIDInput
		if err := strictAutomationInput(raw, &input); err != nil {
			return nil, err
		}
		if _, err := s.store.GetVPNGatePool(ctx, input.ID); err != nil {
			return nil, errors.New("出口池不存在")
		}
		return map[string]any{"id": input.ID}, nil
	})
}

// vpngateRequestReselect makes every server using the pool probe on the next
// worker pass. The worker, not this caller, rewrites the selection state.
func (s *Server) vpngateRequestReselect(_ context.Context, poolID int64) {
	runtime := s.vpngateState()
	runtime.inbox.Lock()
	runtime.reselect[poolID] = true
	runtime.inbox.Unlock()
}

func (s *Server) apiVPNGate(w http.ResponseWriter, r *http.Request) {
	p, _ := apiPrincipal(r)
	if r.Method == http.MethodGet {
		name := "vpngate.overview"
		raw := json.RawMessage(`{}`)
		if r.URL.Path == "/api/v1/vpngate/catalog" {
			name = "vpngate.catalog.list"
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			raw, _ = json.Marshal(vpngateCatalogInput{Country: r.URL.Query().Get("country"), Limit: limit})
		}
		if _, ok := s.capabilities.Authorize(p, name); !ok {
			v2Error(w, r, 403, "capability_denied", "没有读取权限")
			return
		}
		value, err := s.queryVPNGate(r.Context(), p, name, raw)
		if err != nil {
			v2HandleError(w, r, err)
			return
		}
		v2Write(w, r, 200, value, nil)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/vpngate" {
		v2Error(w, r, 405, "method_not_allowed", "不支持该方法")
		return
	}
	if !p.Interactive {
		v2Error(w, r, 403, "changeset_required", "请通过能力与 Changeset 修改 VPNGate 出口池")
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
	if (req.Action != "save" && req.Action != "delete" && req.Action != "reselect") || len(req.RequestID) < 8 || len(req.RequestID) > 80 {
		v2Error(w, r, 400, "invalid_input", "操作或请求标识无效")
		return
	}
	name := "vpngate.pools." + req.Action
	if _, ok := s.capabilities.Authorize(p, name); !ok {
		v2Error(w, r, 403, "capability_denied", "没有管理权限")
		return
	}
	result, err := s.applyConfirmedChangeset(r.Context(), p, []automation.OperationRequest{{Capability: name, Input: req.Input, ResourceRefs: json.RawMessage(`{}`)}}, fmt.Sprintf("vpngate:%s", req.RequestID), "VPNGate 出口池")
	if err != nil {
		v2HandleError(w, r, err)
		return
	}
	if result == nil || result.Status != model.ChangesetSucceeded {
		v2Error(w, r, 409, "changeset_not_applied", "操作未应用，请查看审批记录")
		return
	}
	v2Write(w, r, 200, map[string]any{"applied": true, "result": result.Result}, nil)
}

// vpngateCanvasPool is the compact pool view the topology canvas needs: a
// name to label the exit and, per exit server, whether traffic can leave.
type vpngateCanvasPool struct {
	ID      int64                       `json:"id"`
	Name    string                      `json:"name"`
	Enabled bool                        `json:"enabled"`
	Servers map[int64]vpngateCanvasExit `json:"servers"`
}

type vpngateCanvasExit struct {
	Members  int    `json:"members"`
	Healthy  int    `json:"healthy"`
	EgressIP string `json:"egress_ip,omitempty"`
	Country  string `json:"country_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) vpngateCanvasPools(pools []model.VPNGatePool, states []model.VPNGatePoolServer) []vpngateCanvasPool {
	out := make([]vpngateCanvasPool, 0, len(pools))
	for _, pool := range pools {
		view := vpngateCanvasPool{ID: pool.ID, Name: pool.Name, Enabled: pool.Enabled, Servers: map[int64]vpngateCanvasExit{}}
		for _, state := range states {
			if state.PoolID != pool.ID {
				continue
			}
			s.vpngateOverlayStatus(&state)
			exit := vpngateCanvasExit{Members: len(state.Members), Error: state.LastError}
			if state.Status != nil {
				exit.Healthy = state.Status.Healthy
			}
			if len(state.Members) > 0 {
				exit.EgressIP = state.Members[0].EgressIP
				exit.Country = state.Members[0].CountryCode
			}
			view.Servers[state.ServerID] = exit
		}
		out = append(out, view)
	}
	return out
}
