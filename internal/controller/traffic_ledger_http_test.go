package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAgentTrafficLedgerLostACKDoesNotDoubleBill(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	body := ledgerTrafficBody(user.ID, inbound.ID, "tr-lost", 0, 100, 0, 200)
	first := postAgentTraffic(t, h, server.AgentID, "token-a", body, http.StatusOK)
	if first["protocol_version"] != nil {
		t.Fatalf("protocol_version should be omitted: %#v", first["protocol_version"])
	}
	second := postAgentTraffic(t, h, server.AgentID, "token-a", body, http.StatusOK)
	reports := second["accepted_reports"].([]any)
	if len(reports) != 1 || reports[0].(map[string]any)["status"] != "duplicate" {
		t.Fatalf("retry = %#v", second["accepted_reports"])
	}
	stored, err := db.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 300 {
		t.Fatalf("used = %d", stored.TrafficUsedBytes)
	}
}

func TestAgentTrafficLedgerOverlapAndGapAreNotBilled(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-base", 0, 200, 0, 200), http.StatusOK)
	overlap := postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-overlap", 150, 300, 150, 300), http.StatusOK)
	if overlap["accepted_reports"].([]any)[0].(map[string]any)["status"] != "checkpoint_overlap" {
		t.Fatalf("overlap = %#v", overlap["accepted_reports"])
	}
	gap := postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-gap", 300, 400, 300, 400), http.StatusOK)
	if gap["accepted_reports"].([]any)[0].(map[string]any)["status"] != "checkpoint_gap" {
		t.Fatalf("gap = %#v", gap["accepted_reports"])
	}
	stored, err := db.GetUser(context.Background(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 400 {
		t.Fatalf("used = %d, want 400 from the accepted 200/200 only", stored.TrafficUsedBytes)
	}
}

func TestAgentTrafficLedgerTransparentPathStillBillsProcessingServer(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	servers := []*model.Server{
		{Name: "forward-source", AgentID: "agent-source", AgentTokenHash: security.HashSecret("token-source"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010},
		{Name: "processing", AgentID: "agent-processing", AgentTokenHash: security.HashSecret("token-processing"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 20000, PortRangeEnd: 20010},
		{Name: "downstream", AgentID: "agent-downstream", AgentTokenHash: security.HashSecret("token-downstream"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30010},
	}
	for _, server := range servers {
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	root := &model.Inbound{ServerID: servers[0].ID, Name: "root", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10001, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, root); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111155", ProxyPassword: "password-a", SubscriptionToken: "subscription-token-v2"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{Name: "source-forward-processing", InboundID: root.ID, Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	grantTestPlanNode(t, db, user.ID, model.AssignableNodeProxyPath, path.ID)
	processingID := servers[1].ID
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportPortForward, ProcessingRole: true, ServerID: &processingID, ConfigJSON: "{}"}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()
	body := map[string]any{
		"reports": []map[string]any{{
			"report_id": "tr-path", "source": "core", "stream_id": "ts_path", "counter_epoch": "ce_1",
			"user_id": user.ID, "inbound_id": root.ID, "path_id": path.ID,
			"from_upload_bytes": 0, "to_upload_bytes": 10, "from_download_bytes": 0, "to_download_bytes": 20,
		}},
	}
	postAgentTraffic(t, h, servers[0].AgentID, "token-source", body, http.StatusForbidden)
	postAgentTraffic(t, h, servers[2].AgentID, "token-downstream", body, http.StatusForbidden)
	ok := postAgentTraffic(t, h, servers[1].AgentID, "token-processing", body, http.StatusOK)
	if ok["accepted_reports"].([]any)[0].(map[string]any)["status"] != "accepted" {
		t.Fatalf("processing ledger = %#v", ok)
	}
	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 30 {
		t.Fatalf("processing ledger traffic = %d", stored.TrafficUsedBytes)
	}
}

func TestAgentTrafficRejectsDeltaItems(t *testing.T) {
	db, server, _, _, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	postAgentTraffic(t, h, server.AgentID, "token-a", map[string]any{
		"items": []map[string]any{{"report_id": "legacy", "user_id": 1, "inbound_id": 1, "upload_bytes": 10, "download_bytes": 20}},
	}, http.StatusBadRequest)
}

func TestTrafficLedgerAPIAndReconcile(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	ctx := context.Background()
	user := &model.User{Username: "ledger", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111166", ProxyPassword: "password", SubscriptionToken: "ledger-token"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	out := request(t, h, http.MethodGet, "/api/v1/ui/users/"+strconv.FormatInt(user.ID, 10)+"/traffic-ledger", token, nil, http.StatusOK)
	if out["traffic_ledger"] == nil {
		t.Fatalf("missing ledger: %#v", out)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/traffic-ledger/reconcile", token, map[string]any{"user_id": user.ID}, http.StatusOK)
}

func trafficLedgerHTTPFixture(t *testing.T) (*store.Store, *model.Server, *model.Inbound, *model.User, http.Handler) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	server := &model.Server{Name: "node-a", AgentID: "agent-a", AgentTokenHash: security.HashSecret("token-a"), Status: model.ServerOnline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "a", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 10001, Enabled: true, ConfigJSON: "{}"}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "alice", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111144", ProxyPassword: "password-a", SubscriptionToken: "subscription-token-v2-http"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	return db, server, inbound, user, newTestServer(db, "test-secret", "").Handler()
}

func ledgerTrafficBody(userID, inboundID int64, reportID string, fromUp, toUp, fromDown, toDown int64) map[string]any {
	return ledgerTrafficBodyWithPath(userID, inboundID, nil, reportID, fromUp, toUp, fromDown, toDown)
}

func ledgerTrafficBodyWithPath(userID, inboundID int64, pathID *int64, reportID string, fromUp, toUp, fromDown, toDown int64) map[string]any {
	report := map[string]any{
		"report_id": reportID, "source": "core", "stream_id": "ts_core", "counter_epoch": "ce_1",
		"user_id": userID, "inbound_id": inboundID,
		"from_upload_bytes": fromUp, "to_upload_bytes": toUp, "from_download_bytes": fromDown, "to_download_bytes": toDown,
	}
	if pathID != nil {
		report["path_id"] = *pathID
	}
	return map[string]any{"reports": []map[string]any{report}}
}

func postAgentTraffic(t *testing.T, h http.Handler, agentID, token string, body map[string]any, want int) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/traffic-reports", bytes.NewReader(encoded))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, want, rr.Body.String())
	}
	var response map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &response)
	return response
}

// Cross-tenant ownership is a request-level boundary, and a batch that also
// carries valid reports does not soften it: a correct Agent only ever holds the
// configuration for its own server, so it can never produce one of these.
func TestAgentTrafficCrossServerReportFailsTheWholeBatch(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	other := &model.Server{Name: "other-node", AgentID: "other-agent", AgentTokenHash: security.HashSecret("other-token"), ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherInbound := &model.Inbound{ServerID: other.ID, Name: "other-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, otherInbound); err != nil {
		t.Fatal(err)
	}
	valid := ledgerTrafficBody(user.ID, inbound.ID, "tr-valid", 0, 100, 0, 200)["reports"].([]map[string]any)[0]
	crossServer := ledgerTrafficBody(user.ID, otherInbound.ID, "tr-cross-server", 0, 100, 0, 200)["reports"].([]map[string]any)[0]
	postAgentTraffic(t, h, server.AgentID, "token-a", map[string]any{"reports": []map[string]any{valid, crossServer}}, http.StatusForbidden)

	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 0 {
		t.Fatalf("a refused batch billed %d bytes", stored.TrafficUsedBytes)
	}
}

// One unbound pair must not hold the rest of the ledger hostage: the valid
// reports in the same batch are still accounted, and the response that carries
// the traffic policies still lands.
func TestAgentTrafficUnboundPairDoesNotPoisonTheBatch(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	other := &model.User{Username: "unbound-batch", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "55555555-5555-4555-8555-555555555555", ProxyPassword: "unbound-batch-password"}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	valid := ledgerTrafficBody(user.ID, inbound.ID, "tr-valid", 0, 100, 0, 200)["reports"].([]map[string]any)[0]
	unbound := ledgerTrafficBody(other.ID, inbound.ID, "tr-unbound-batch", 0, 100, 0, 200)["reports"].([]map[string]any)[0]
	response := postAgentTraffic(t, h, server.AgentID, "token-a", map[string]any{"reports": []map[string]any{valid, unbound}}, http.StatusOK)
	assertTrafficRejection(t, response, "tr-unbound-batch", "binding_removed")

	accepted, _ := response["accepted_report_ids"].([]any)
	found := false
	for _, id := range accepted {
		if id == "tr-valid" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the valid report in the batch was not accounted: %#v", response)
	}
	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TrafficUsedBytes != 300 {
		t.Fatalf("bound user billed %d bytes, want 300", stored.TrafficUsedBytes)
	}
	// The unbound pair is never billed, no matter which batch carries it.
	unboundUser, err := db.GetUser(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unboundUser.TrafficUsedBytes != 0 {
		t.Fatalf("unbound user billed %d bytes", unboundUser.TrafficUsedBytes)
	}
}

// Only cross-tenant ownership fails the request; everything else is either a
// per-report rejection or a malformed-report 400.
func TestTrafficReportFailureStatusOnlyRefusesCrossTenantOwnership(t *testing.T) {
	if got := trafficReportFailureStatus(errTrafficForbidden); got != http.StatusForbidden {
		t.Fatalf("cross-server ownership status = %d, want 403", got)
	}
	if got := trafficReportFailureStatus(errors.New("malformed report")); got != http.StatusBadRequest {
		t.Fatalf("a malformed report status = %d, want 400", got)
	}
}
