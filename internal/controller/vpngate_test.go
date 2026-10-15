package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/ipquality"
	"github.com/OboardProject/oboard/internal/model"
)

func vpngateTestQuality(asnByIP map[string]string) func(context.Context, string, string) ipquality.Result {
	return func(_ context.Context, provider, ip string) ipquality.Result {
		hosting := false
		risk := 5
		result := ipquality.Result{Provider: provider, Status: "ok", Country: "JP", ASN: asnByIP[ip], Organization: "test", Hosting: &hosting, Risk: &risk}
		if result.ASN == "" {
			result.ASN = "AS2516"
		}
		return result
	}
}

func TestVPNGateSelectionFlowFiltersOperatorAndFailsClosed(t *testing.T) {
	ctx := t.Context()
	db := openControllerAutomationTestStore(t)
	app := newTestServer(db, "test-secret", "")
	admin := &model.User{Username: "vpngate-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "44444444-4444-4444-8444-444444444444", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	p := userAutomationPrincipal(t, db, admin.ID)
	applyAutomationChangeset(t, app, p, "vpngate-create", automation.OperationRequest{Capability: "vpngate.pools.save", Input: json.RawMessage(`{"name":"日本 NTT","countries":["jp"],"operator_asns":["4713"],"hot_standby":1,"pool_size":2}`)})
	pools, err := db.ListVPNGatePools(ctx)
	if err != nil || len(pools) != 1 || pools[0].OperatorASNs[0] != "AS4713" {
		t.Fatalf("pool not created: %+v %v", pools, err)
	}
	pool := pools[0]

	server := &model.Server{Name: "vpngate-exit", AgentID: "agent-vpngate", AgentTokenHash: "hash", ChainSecret: "secret", ListenIP: "0.0.0.0", Status: model.ServerOnline, KernelCapabilities: []string{"vpngate_pool_v1"}}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	var entries []core.VPNGateCatalogEntry
	for _, ip := range []string{"219.100.37.1", "219.100.37.2", "219.100.37.3"} {
		entries = append(entries, core.VPNGateCatalogEntry{IP: ip, Port: 443, Network: "tcp", CountryCode: "JP", Score: 100, Spec: model.OpenVPNSpec{Server: ip, ServerPort: 443, Network: "tcp", Cipher: "AES-128-CBC", Auth: "SHA1", CA: "ca", Cert: "cert", Key: "key"}})
	}
	if _, err := db.UpsertVPNGateCatalog(ctx, entries, 0, "test", time.Now()); err != nil {
		t.Fatal(err)
	}
	app.vpngateState().lookup = vpngateTestQuality(map[string]string{
		"219.100.37.1": "AS4713", "219.100.37.2": "AS4713", "219.100.37.3": "AS4713",
		"153.1.1.1": "AS4713", "126.2.2.2": "AS17676",
	})
	nodes, err := db.ListVPNGateNodes(ctx)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("nodes %v %v", nodes, err)
	}
	memberOf := map[string]string{}
	for _, node := range nodes {
		memberOf[node.IP] = core.VPNGateMemberID(node.ID)
	}

	if err := app.vpngateReconcilePair(ctx, pool, *server, nodes, false); err != nil {
		t.Fatal(err)
	}
	state, err := db.GetVPNGatePoolServer(ctx, pool.ID, server.ID)
	if err != nil || state.ProbeTaskID == 0 {
		t.Fatalf("probe not queued: %+v %v", state, err)
	}
	probeTask, err := db.GetTask(ctx, state.ProbeTaskID)
	if err != nil || probeTask.Type != model.AgentTaskTypeProbeVPNGate {
		t.Fatalf("probe task %+v %v", probeTask, err)
	}
	var plan model.VPNGateProbePlan
	if err := json.Unmarshal([]byte(probeTask.PayloadJSON), &plan); err != nil || len(plan.Candidates) != 3 || plan.Tag != core.VPNGatePoolTag(pool.ID) {
		t.Fatalf("probe plan %+v %v", plan, err)
	}

	report := model.VPNGateProbeReport{PoolID: pool.ID, RequestID: plan.RequestID, Results: []model.VPNGateProbeResult{
		{ProbeID: memberOf["219.100.37.1"], Status: "succeeded", HandshakeMS: 700, RTTMS: []int64{120, 110, 130}, EgressIP: "153.1.1.1"},
		{ProbeID: memberOf["219.100.37.2"], Status: "succeeded", HandshakeMS: 600, RTTMS: []int64{60, 60, 60}, EgressIP: "126.2.2.2"},
		{ProbeID: memberOf["219.100.37.3"], Status: "failed", Stage: "handshake", Error: "server says: bad auth"},
	}}
	raw, _ := json.Marshal(report)
	if err := app.applyVPNGateProbeResult(ctx, server.ID, *probeTask, "succeeded", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := app.vpngateReconcilePair(ctx, pool, *server, nodes, false); err != nil {
		t.Fatal(err)
	}
	state, _ = db.GetVPNGatePoolServer(ctx, pool.ID, server.ID)
	if len(state.Members) != 1 || state.Members[0].MemberID != memberOf["219.100.37.1"] {
		t.Fatalf("only the matching operator may be selected even though another exit is faster: %+v", state.Members)
	}
	for _, candidate := range state.Candidates {
		if candidate.EgressIP == "126.2.2.2" && (candidate.Eligible || !strings.Contains(candidate.Reason, "运营商")) {
			t.Fatalf("other operator must be rejected by egress ASN: %+v", candidate)
		}
	}
	state, _ = db.GetVPNGatePoolServer(ctx, pool.ID, server.ID)
	applyTask, err := db.GetTask(ctx, state.ApplyTaskID)
	if err != nil || applyTask.Type != model.AgentTaskTypeApplyVPNGatePool {
		t.Fatalf("membership not sent: %+v %v", applyTask, err)
	}
	var membership model.VPNGatePoolPlan
	if err := json.Unmarshal([]byte(applyTask.PayloadJSON), &membership); err != nil || len(membership.Members) != 1 || membership.HotStandby != 1 || membership.Revision != state.Revision || membership.ContentDigest == "" {
		t.Fatalf("membership plan %+v %v", membership, err)
	}

	// Every candidate fails on the next round, including the live member:
	// the pool must receive an empty membership and refuse traffic.
	past := time.Now().Add(-time.Minute)
	state.NextRefreshAt = &past
	state.LastProbeAt = nil
	if err := db.SaveVPNGatePoolServer(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := app.vpngateReconcilePair(ctx, pool, *server, nodes, false); err != nil {
		t.Fatal(err)
	}
	state, _ = db.GetVPNGatePoolServer(ctx, pool.ID, server.ID)
	probeTask, _ = db.GetTask(ctx, state.ProbeTaskID)
	_ = json.Unmarshal([]byte(probeTask.PayloadJSON), &plan)
	failed := model.VPNGateProbeReport{PoolID: pool.ID, RequestID: plan.RequestID}
	for _, candidate := range plan.Candidates {
		failed.Results = append(failed.Results, model.VPNGateProbeResult{ProbeID: candidate.ProbeID, Status: "failed", Stage: "handshake", Error: "timeout"})
	}
	raw, _ = json.Marshal(failed)
	if err := app.applyVPNGateProbeResult(ctx, server.ID, *probeTask, "succeeded", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := app.vpngateReconcilePair(ctx, pool, *server, nodes, false); err != nil {
		t.Fatal(err)
	}
	state, _ = db.GetVPNGatePoolServer(ctx, pool.ID, server.ID)
	if len(state.Members) != 0 || !strings.Contains(state.LastError, "拒绝出站") {
		t.Fatalf("pool must fail closed: %+v", state)
	}
	applyTask, _ = db.GetTask(ctx, state.ApplyTaskID)
	_ = json.Unmarshal([]byte(applyTask.PayloadJSON), &membership)
	if len(membership.Members) != 0 || membership.Revision != state.Revision {
		t.Fatalf("empty membership not sent: %+v", membership)
	}

	// A stale report for an older request is ignored.
	stale := failed
	stale.RequestID = "stale"
	raw, _ = json.Marshal(stale)
	if err := app.applyVPNGateProbeResult(ctx, server.ID, *probeTask, "succeeded", string(raw)); err == nil {
		t.Fatal("a report that does not match its task must be rejected")
	}

	applyAutomationChangeset(t, app, p, "vpngate-delete", automation.OperationRequest{Capability: "vpngate.pools.delete", Input: json.RawMessage(fmt.Sprintf(`{"id":%d}`, pool.ID))})
	if pools, _ := db.ListVPNGatePools(ctx); len(pools) != 0 {
		t.Fatal("pool not deleted")
	}
}

func TestVPNGateHealthWakesWorkerWhenMembersDrop(t *testing.T) {
	ctx := t.Context()
	db := openControllerAutomationTestStore(t)
	app := newTestServer(db, "test-secret", "")
	tag := core.VPNGatePoolTag(4)
	app.recordVPNGateHealth(ctx, 9, []model.VPNGatePoolStatus{{Tag: tag, PoolID: 4, Revision: 2, Healthy: 2, Members: []model.VPNGateMemberStatus{{MemberID: "n1", State: "up"}, {MemberID: "n2", State: "up"}}}})
	select {
	case <-app.vpngateState().wake:
		t.Fatal("a healthy first report must not wake the worker")
	default:
	}
	app.recordVPNGateHealth(ctx, 9, []model.VPNGatePoolStatus{{Tag: tag, PoolID: 4, Revision: 2, Healthy: 0, Members: []model.VPNGateMemberStatus{{MemberID: "n1", State: "cooldown", LastError: strings.Repeat("x", 900)}}}})
	select {
	case <-app.vpngateState().wake:
	default:
		t.Fatal("losing healthy members must wake the selection worker")
	}
	state := &model.VPNGatePoolServer{PoolID: 4, ServerID: 9}
	app.vpngateOverlayStatus(state)
	if state.Status == nil || state.Status.Healthy != 0 || len(state.Status.Members[0].LastError) > 300 {
		t.Fatalf("health not kept or unbounded: %+v", state.Status)
	}
	app.recordVPNGateHealth(ctx, 9, []model.VPNGatePoolStatus{{Tag: "vpngate-999", PoolID: 5}})
	other := &model.VPNGatePoolServer{PoolID: 5, ServerID: 9}
	app.vpngateOverlayStatus(other)
	if other.Status != nil {
		t.Fatal("a status whose tag does not match its pool must be ignored")
	}
}
