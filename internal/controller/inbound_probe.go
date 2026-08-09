package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	inboundProbeSamples  = 5
	inboundProbeInterval = 300 * time.Millisecond
	inboundProbeTimeout  = 3 * time.Second
	inboundProbePeriod   = 5 * time.Minute
)

func buildInboundProbePlan(version int64, server model.Server, inbounds []model.Inbound) model.InboundProbePlan {
	plan := model.InboundProbePlan{
		Version: version, ServerID: server.ID, SampleCount: inboundProbeSamples,
		IntervalMS: int(inboundProbeInterval / time.Millisecond), TimeoutMS: int(inboundProbeTimeout / time.Millisecond),
	}
	for _, inbound := range inbounds {
		// SSH restricted-proxy inbounds are served by the Agent's dedicated
		// listener, not sing-box. They must not be probed as regular inbounds.
		if inbound.ServerID != server.ID || !inbound.Enabled || inbound.Protocol == model.ProtocolSSH {
			continue
		}
		host := strings.TrimSpace(core.ResolveEntryAddress(inbound, server))
		ports, err := core.MieruInboundPorts(inbound)
		if host == "" || err != nil {
			continue
		}
		for index, port := range ports {
			sampleCount := 1
			if index == 0 {
				sampleCount = inboundProbeSamples
			}
			plan.EntryTargets = append(plan.EntryTargets, model.InboundProbeTarget{
				InboundID: inbound.ID, Name: inbound.Name, Protocol: inbound.Protocol, Host: host,
				ListenIP: inbound.ListenIP, Port: port, Transport: controllerProbeTransport(inbound),
				SampleCount: sampleCount,
			})
		}
	}
	return plan
}

func controllerProbeTransport(inbound model.Inbound) string {
	switch inbound.Protocol {
	case model.ProtocolHY2:
		return "udp"
	case model.ProtocolSS, model.ProtocolSocks:
		return "tcp_udp"
	case model.ProtocolMieru:
		if core.MieruInboundTransport(inbound) == "UDP" {
			return "udp"
		}
		return "tcp"
	default:
		return "tcp"
	}
}

func (s *Server) createControllerInboundProbeTask(ctx context.Context, applyTaskID, forwardTaskID int64, plan model.InboundProbePlan) (model.AgentTask, error) {
	payload, err := json.Marshal(plan)
	if err != nil {
		return model.AgentTask{}, err
	}
	nonce, err := security.RandomToken(12)
	if err != nil {
		return model.AgentTask{}, err
	}
	task := model.AgentTask{
		ServerID: plan.ServerID, Type: model.AgentTaskTypeProbeInboundsExternal, PayloadJSON: string(payload),
		Status: "running", ResultJSON: `{"message":"等待入口配置生效后由主控探测"}`,
		ConfigVersion: plan.Version, Nonce: nonce,
	}
	if err := s.store.CreateTask(ctx, &task); err != nil {
		return model.AgentTask{}, err
	}
	go s.runControllerInboundProbeTask(context.WithoutCancel(ctx), task.ID, []int64{applyTaskID, forwardTaskID}, plan)
	return task, nil
}

func (s *Server) runControllerInboundProbeTask(parent context.Context, taskID int64, dependencyTaskIDs []int64, plan model.InboundProbePlan) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
	defer cancel()
	for _, dependencyTaskID := range dependencyTaskIDs {
		if dependencyTaskID == 0 {
			continue
		}
		ticker := time.NewTicker(400 * time.Millisecond)
		for {
			dependencyTask, err := s.store.GetTask(ctx, dependencyTaskID)
			if err != nil {
				ticker.Stop()
				s.completeControllerProbeFailure(ctx, taskID, "读取下发任务状态失败："+err.Error())
				return
			}
			switch dependencyTask.Status {
			case "succeeded":
				ticker.Stop()
				goto NEXT_DEPENDENCY
			case "failed", "rollback_failed":
				ticker.Stop()
				s.completeControllerProbeFailure(ctx, taskID, "配置下发失败，未执行公网端口探测")
				return
			}
			select {
			case <-ctx.Done():
				ticker.Stop()
				s.completeControllerProbeFailure(ctx, taskID, "等待配置下发完成超时")
				return
			case <-ticker.C:
			}
		}
	NEXT_DEPENDENCY:
	}
	time.Sleep(800 * time.Millisecond)
	results := s.probeInboundPlan(ctx, plan, "controller_external")
	failed := 0
	for _, result := range results {
		if err := s.store.AddInboundProbeResult(ctx, result); err != nil {
			failed++
			continue
		}
		if !result.Available {
			failed++
		}
	}
	resultJSON, _ := json.Marshal(map[string]any{"message": "主控入口端口探测完成", "probes": results})
	status := "succeeded"
	if failed > 0 {
		status = "failed"
	}
	_ = s.completeTaskWithNotification(ctx, taskID, status, string(resultJSON))
}

// runControllerInboundProbeTelemetry keeps the useful outside-in verification
// without creating another user-visible task for a single deployment.
func (s *Server) runControllerInboundProbeTelemetry(parent context.Context, plan model.InboundProbePlan) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	timer := time.NewTimer(800 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	for _, result := range s.probeInboundPlan(ctx, plan, "controller_external") {
		if err := s.store.AddInboundProbeResult(ctx, result); err != nil {
			log.Printf("store deployment external inbound probe for server %d: %v", plan.ServerID, err)
		}
	}
}

func (s *Server) completeControllerProbeFailure(ctx context.Context, taskID int64, message string) {
	result, _ := json.Marshal(map[string]any{"message": message, "error": message})
	_ = s.completeTaskWithNotification(ctx, taskID, "failed", string(result))
}

func (s *Server) probeInboundPlan(ctx context.Context, plan model.InboundProbePlan, mode string) []model.InboundProbeResult {
	results := make([]model.InboundProbeResult, 0, len(plan.EntryTargets))
	for _, target := range plan.EntryTargets {
		result := controllerProbeTarget(ctx, plan.Version, target, mode)
		result.ServerID = plan.ServerID
		results = append(results, result)
	}
	return results
}

func controllerProbeTarget(ctx context.Context, version int64, target model.InboundProbeTarget, mode string) model.InboundProbeResult {
	sampleCount := target.SampleCount
	if sampleCount <= 0 {
		sampleCount = inboundProbeSamples
	}
	result := model.InboundProbeResult{
		InboundID: target.InboundID, ConfigVersion: version, Mode: mode, Transport: target.Transport,
		Endpoint: net.JoinHostPort(strings.Trim(target.Host, "[]"), fmt.Sprint(target.Port)), ResultJSON: "{}",
	}
	if target.Transport == "udp" {
		latencies, failures := controllerUDPSignals(ctx, target.Host, target.Port, sampleCount, inboundProbeInterval, inboundProbeTimeout)
		applyControllerProbeStats(&result, latencies, sampleCount)
		result.Available = result.SuccessCount >= requiredControllerProbeSuccesses(sampleCount)
		result.Confirmed = false
		result.ResultJSON = controllerProbeJSON(map[string]any{
			"kind": "udp_signal", "confidence": "signal_only", "latencies_ms": latencies, "failures": failures,
		})
		if !result.Available {
			result.Error = fmt.Sprintf("公网 UDP 发包仅成功 %d/%d 次", result.SuccessCount, sampleCount)
		}
		return result
	}
	latencies, failures := controllerTCPSamples(ctx, target.Host, target.Port, sampleCount, inboundProbeInterval, inboundProbeTimeout)
	applyControllerProbeStats(&result, latencies, sampleCount)
	result.Available = result.SuccessCount >= requiredControllerProbeSuccesses(sampleCount)
	result.Confirmed = true
	result.ResultJSON = controllerProbeJSON(map[string]any{"kind": "tcp_connect", "latencies_ms": latencies, "failures": failures})
	if !result.Available {
		result.Error = fmt.Sprintf("公网 TCP 建连仅成功 %d/%d 次", result.SuccessCount, sampleCount)
	}
	return result
}

func controllerTCPSamples(ctx context.Context, host string, port, count int, interval, timeout time.Duration) ([]int64, []string) {
	latencies := make([]int64, 0, count)
	failures := make([]string, 0)
	dialer := net.Dialer{Timeout: timeout}
	address := net.JoinHostPort(strings.Trim(host, "[]"), fmt.Sprint(port))
	for i := 0; i < count; i++ {
		started := time.Now()
		conn, err := dialer.DialContext(ctx, "tcp", address)
		elapsed := time.Since(started).Milliseconds()
		if err != nil {
			failures = append(failures, err.Error())
		} else {
			latencies = append(latencies, elapsed)
			_ = conn.Close()
		}
		if i+1 < count {
			select {
			case <-ctx.Done():
				return latencies, append(failures, ctx.Err().Error())
			case <-time.After(interval):
			}
		}
	}
	return latencies, failures
}

func controllerUDPSignals(ctx context.Context, host string, port, count int, interval, timeout time.Duration) ([]int64, []string) {
	latencies := make([]int64, 0, count)
	failures := make([]string, 0)
	address, err := net.ResolveUDPAddr("udp", net.JoinHostPort(strings.Trim(host, "[]"), fmt.Sprint(port)))
	if err != nil {
		return latencies, []string{err.Error()}
	}
	conn, err := net.DialUDP("udp", nil, address)
	if err != nil {
		return latencies, []string{err.Error()}
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	for i := 0; i < count; i++ {
		started := time.Now()
		_, err := conn.Write([]byte{0})
		if err != nil {
			failures = append(failures, err.Error())
		} else {
			latencies = append(latencies, time.Since(started).Milliseconds())
		}
		if i+1 < count {
			select {
			case <-ctx.Done():
				return latencies, append(failures, ctx.Err().Error())
			case <-time.After(interval):
			}
		}
	}
	return latencies, failures
}

func applyControllerProbeStats(result *model.InboundProbeResult, latencies []int64, total int) {
	result.SampleCount = total
	result.SuccessCount = len(latencies)
	if len(latencies) == 0 {
		return
	}
	var sum int64
	for _, value := range latencies {
		sum += value
	}
	result.LatencyMS = sum / int64(len(latencies))
	ordered := append([]int64(nil), latencies...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	result.MinLatencyMS = ordered[0]
	result.P95LatencyMS = ordered[int(math.Ceil(float64(len(ordered))*0.95))-1]
	if len(latencies) > 1 {
		var delta int64
		for i := 1; i < len(latencies); i++ {
			d := latencies[i] - latencies[i-1]
			if d < 0 {
				d = -d
			}
			delta += d
		}
		result.JitterMS = delta / int64(len(latencies)-1)
	}
}

func requiredControllerProbeSuccesses(total int) int { return total/2 + 1 }

func controllerProbeJSON(value any) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func (s *Server) inboundProbes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	items, err := s.store.ListInboundProbeResults(r.Context(), int64(intQuery(r, "server_id", 0)), int64(intQuery(r, "inbound_id", 0)), intQuery(r, "limit", 100))
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"inbound_probes": items})
}

func (s *Server) inboundProbeNow(w http.ResponseWriter, r *http.Request, inboundID int64) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	inbound, err := s.store.GetInbound(r.Context(), inboundID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	server, err := s.store.GetServer(r.Context(), inbound.ServerID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	version := time.Now().Unix()
	plan := buildInboundProbePlan(version, *server, []model.Inbound{*inbound})
	if len(plan.EntryTargets) == 0 {
		fail(w, errors.New("inbound has no probeable endpoint"), 400)
		return
	}
	localTask, err := s.queueAgentTask(r.Context(), server.ID, model.AgentTaskTypeProbeInbounds, plan, version)
	if err != nil {
		fail(w, err, 500)
		return
	}
	externalTask, err := s.createControllerInboundProbeTask(r.Context(), 0, 0, plan)
	if err != nil {
		fail(w, err, 500)
		return
	}
	auditReq(s, r, "probe", "inbound", fmt.Sprint(inboundID))
	write(w, http.StatusAccepted, map[string]any{"tasks": []model.AgentTask{localTask, externalTask}, "inbound": inbound})
}

func (s *Server) agentInboundProbes(w http.ResponseWriter, r *http.Request) {
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var result model.InboundProbeResult
	if !decode(w, r, &result) {
		return
	}
	inbound, err := s.store.GetInbound(r.Context(), result.InboundID)
	if err != nil || inbound.ServerID != server.ID {
		fail(w, errors.New("inbound does not belong to this agent"), http.StatusForbidden)
		return
	}
	result.ServerID = server.ID
	if result.Mode == "" {
		result.Mode = "agent_listener"
	}
	if result.ResultJSON == "" {
		result.ResultJSON = "{}"
	}
	if err := validJSONObject(result.ResultJSON); err != nil {
		fail(w, err, 400)
		return
	}
	if err := s.store.AddInboundProbeResult(r.Context(), result); err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"ok": true})
}

func (s *Server) schedulePeriodicInboundProbes(ctx context.Context) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return
	}
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return
	}
	recent, err := s.store.ListInboundProbeResults(ctx, 0, 0, 500)
	if err != nil {
		return
	}
	latest := map[int64]time.Time{}
	for _, result := range recent {
		if !strings.HasPrefix(result.Mode, "controller_external") {
			continue
		}
		if current := latest[result.InboundID]; result.CreatedAt.After(current) {
			latest[result.InboundID] = result.CreatedAt
		}
	}
	for _, server := range servers {
		if server.Status != model.ServerOnline {
			continue
		}
		due := make([]model.Inbound, 0)
		for _, inbound := range inbounds {
			if inbound.ServerID != server.ID || !inbound.Enabled {
				continue
			}
			if checked := latest[inbound.ID]; checked.IsZero() || time.Since(checked) >= inboundProbePeriod {
				due = append(due, inbound)
			}
		}
		if len(due) == 0 || !s.beginServerProbe(server.ID) {
			continue
		}
		plan := buildInboundProbePlan(time.Now().Unix(), server, due)
		go func(probeParent context.Context, serverID int64, plan model.InboundProbePlan) {
			defer s.endServerProbe(serverID)
			probeCtx, cancel := context.WithTimeout(probeParent, 90*time.Second)
			defer cancel()
			for _, result := range s.probeInboundPlan(probeCtx, plan, "controller_external_periodic") {
				_ = s.store.AddInboundProbeResult(probeCtx, result)
			}
		}(ctx, server.ID, plan)
	}
}

func (s *Server) beginServerProbe(serverID int64) bool {
	s.probeMu.Lock()
	defer s.probeMu.Unlock()
	if s.activeProbes[serverID] {
		return false
	}
	s.activeProbes[serverID] = true
	return true
}

func (s *Server) endServerProbe(serverID int64) {
	s.probeMu.Lock()
	delete(s.activeProbes, serverID)
	s.probeMu.Unlock()
}
