package controller

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/OboardProject/oboard/internal/ipquality"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"log"
	"os"
	"sort"
	"time"
)

func (s *Server) StartProbeMonitors(ctx context.Context) {
	client := ipquality.New(ipquality.Config{IPinfo: os.Getenv("OBOARD_IPINFO_TOKEN"), Proxycheck: os.Getenv("OBOARD_PROXYCHECK_KEY"), AbuseIPDB: os.Getenv("OBOARD_ABUSEIPDB_KEY")})
	defer client.HTTP.CloseIdleConnections()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		tasks, err := s.store.ListProbeMonitors(ctx)
		if err == nil {
			sort.SliceStable(tasks, func(i, j int) bool { return tasks[i].CheckedAt < tasks[j].CheckedAt })
			processed := 0
			for _, task := range tasks {
				if ctx.Err() != nil {
					return
				}
				if !task.Enabled {
					continue
				}
				claimed, err := s.store.ClaimProbeMonitor(ctx, task, time.Now())
				if err != nil || !claimed {
					continue
				}
				runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
				status, message, result, route := s.runProbeMonitor(runCtx, client, task)
				cancel()
				if ctx.Err() != nil {
					return
				}
				if err := s.store.FinishProbeMonitor(ctx, task, status, message, result, route); err != nil {
					log.Printf("save probe monitor %d: %v", task.ID, err)
				}
				processed++
				if processed >= 8 {
					break
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) runProbeMonitor(ctx context.Context, client *ipquality.Client, task model.ProbeMonitor) (string, string, any, *model.ProbeRouteVersion) {
	server, err := s.store.GetServer(ctx, task.ServerID)
	if err != nil {
		return "failed", "服务器不存在", nil, nil
	}
	if task.Kind == "ip_quality" {
		ip := task.Target
		if ip == "" {
			ip = server.PublicIPv4
		}
		if !ipquality.PublicIP(ip) {
			return "failed", "请填写公网 IP 或先配置服务器公网 IPv4", nil, nil
		}
		results := []ipquality.Result{}
		successful := 0
		for _, provider := range []string{"ipinfo", "proxycheck", "abuseipdb"} {
			r := client.Lookup(ctx, provider, ip)
			results = append(results, r)
			if r.Status == "ok" {
				successful++
			}
		}
		result := map[string]any{"ip": ip, "providers": results}
		if successful == 0 {
			return "unknown", "所有数据源均未取得结果", result, nil
		}
		if successful < 3 {
			return "partial", "部分数据源未配置或查询失败", result, nil
		}
		return "ok", "", result, nil
	}
	if server.Status != model.ServerOnline || server.AgentID == "" {
		return "offline", "服务器离线", nil, nil
	}
	required := model.AgentCapabilityNetworkTraceICMP
	if task.Mode == "tcp" {
		required = model.AgentCapabilityNetworkTraceTCP
	}
	if !serverSupportsCapability(*server, model.AgentCapabilityRouteMonitor) || !serverSupportsCapability(*server, required) {
		return "unsupported", "请更新 Agent；当前平台不支持所选路由探测", nil, nil
	}
	trace, err := s.monitorTrace(ctx, *server, task)
	if err != nil {
		return "failed", err.Error(), nil, nil
	}
	path, err := monitorASNPath(trace)
	if err != nil {
		return "unknown", err.Error(), trace, nil
	}
	version := &model.ProbeRouteVersion{ASNPath: path, Trace: trace}
	return "ok", "", map[string]any{"asn_path": path, "trace": trace}, version
}

func (s *Server) monitorTrace(ctx context.Context, server model.Server, monitor model.ProbeMonitor) (model.NetworkTraceResult, error) {
	var trace model.NetworkTraceResult
	operationID, err := security.RandomToken(18)
	if err != nil {
		return trace, err
	}
	now := time.Now().UTC()
	payload := model.NetworkTraceTaskPayload{NetworkDiagnosticEnvelope: model.NetworkDiagnosticEnvelope{ProtocolVersion: model.NetworkDiagnosticProtocolVersion, OperationID: operationID, Origin: model.NetworkDiagnosticOriginMonitor, IssuedAt: now, ExpiresAt: now.Add(15 * time.Second)}, Target: monitor.Target, IPFamily: model.NetworkFamilyAuto, Mode: monitor.Mode, Port: monitor.Port, MaxHops: 30, QueriesPerHop: 1, PerHopTimeoutMS: 1000}
	raw, _ := json.Marshal(payload)
	nonce, err := security.RandomToken(12)
	if err != nil {
		return trace, err
	}
	task := model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeNetworkTrace, PayloadJSON: string(raw), Status: "pending", ResultJSON: "{}", Nonce: nonce}
	if err = s.store.CreateProbeMonitorTask(ctx, server, monitor, &task); err != nil {
		return trace, err
	}
	s.tasks.wake(server.ID)
	waitCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer stop()
		_ = s.store.SupersedePendingTask(cleanup, task.ID, "拨测已结束或超时")
	}()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := s.store.GetTask(waitCtx, task.ID)
		if err != nil {
			return trace, errors.New("读取拨测结果失败")
		}
		if current.Status == "succeeded" {
			if len(current.ResultJSON) > 128*1024 || json.Unmarshal([]byte(current.ResultJSON), &trace) != nil || trace.OperationID != operationID || trace.Target != monitor.Target || trace.Mode != monitor.Mode || trace.Port != monitor.Port {
				return trace, errors.New("拨测结果与请求不匹配")
			}
			return trace, nil
		}
		if current.Status == "failed" || current.Status == "superseded" || current.Status == "rollback_failed" {
			return trace, errors.New("服务器路由探测失败，请查看任务结果")
		}
		select {
		case <-waitCtx.Done():
			return trace, errors.New("服务器路由探测超时")
		case <-ticker.C:
		}
	}
}

func monitorASNPath(trace model.NetworkTraceResult) ([]string, error) {
	if trace.Provider != "NextTrace-API" {
		return nil, errors.New("缺少 NextTrace 路由归属数据")
	}
	if trace.GeographyError != "" {
		return nil, errors.New("NextTrace API 查询不完整，本次不判定变化")
	}
	byIP := map[string]string{}
	for _, hop := range trace.Hops {
		if len(hop.Geography) > model.TraceMaxQueriesPerHop {
			return nil, errors.New("路由归属数量无效")
		}
		for _, geo := range hop.Geography {
			belongs := false
			for _, ip := range hop.Addresses {
				if ip == geo.Address {
					belongs = true
				}
			}
			if !belongs || len(geo.ASN) > 12 {
				return nil, errors.New("路由归属与探测地址不匹配")
			}
			if prior, ok := byIP[geo.Address]; ok && prior != geo.ASN {
				return nil, errors.New("同一 IP 的 ASN 归属冲突")
			}
			byIP[geo.Address] = geo.ASN
		}
	}
	return ipquality.ASNPath(trace, func(ip string) (string, error) {
		asn, ok := byIP[ip]
		if !ok {
			return "", errors.New("missing NextTrace ASN")
		}
		return asn, nil
	})
}
