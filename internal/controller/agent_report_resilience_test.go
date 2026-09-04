package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type auditReportFixture struct {
	db      *store.Store
	server  *model.Server
	inbound *model.Inbound
	user    *model.User
	handler http.Handler
}

func newAuditReportFixture(t *testing.T) auditReportFixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	server := &model.Server{
		Name: "audit-node", AgentID: "audit-agent", AgentTokenHash: security.HashSecret("audit-token"),
		ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true,
	}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "audit-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "33333333-3333-4333-8333-333333333333", ProxyPassword: "audit-password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, db, user.ID, inbound.ID)
	return auditReportFixture{db: db, server: server, inbound: inbound, user: user, handler: newTestServer(db, "test-secret", "").Handler()}
}

func (f auditReportFixture) validItem(reportID string) map[string]any {
	nowTime := time.Now().UTC()
	return map[string]any{
		"report_id": reportID, "user_id": f.user.ID, "inbound_id": f.inbound.ID,
		"source_ip": "198.51.100.7", "network": "tcp", "destination": "example.com", "destination_port": 443,
		"connection_count": 1, "active_peak": 1, "active_at_end": 0,
		"closed_count": 1, "duration_total_ms": 250, "duration_max_ms": 250, "duration_le_1s_count": 1, "presence_sequence": 1,
		"bucket_capacity": 4096, "collection_started_at": nowTime.Add(-time.Minute).Format(time.RFC3339Nano), "collection_ended_at": nowTime.Format(time.RFC3339Nano),
		"started_at": nowTime.Add(-time.Second).Format(time.RFC3339Nano), "ended_at": nowTime.Format(time.RFC3339Nano),
	}
}

type auditReportResponse struct {
	Accepted  []string `json:"accepted_report_ids"`
	Discarded []struct {
		ReportID string `json:"report_id"`
		Reason   string `json:"reason"`
	} `json:"discarded_reports"`
}

func postAgentAudit(t *testing.T, handler http.Handler, agentID, token string, items []map[string]any, want int) auditReportResponse {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/connection-reports", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, want, rr.Body.String())
	}
	var response auditReportResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &response)
	return response
}

// One malformed bucket used to fail the whole batch with 400, so the Agent
// resent the same poisoned batch forever and never delivered the valid
// reports next to it.
func TestConnectionAuditPoisonedItemDoesNotBlockTheBatch(t *testing.T) {
	fixture := newAuditReportFixture(t)
	items := []map[string]any{}
	for i := 0; i < 10; i++ {
		items = append(items, fixture.validItem("good-"+string(rune('a'+i))))
	}
	// payload_last_at after ended_at is exactly the window the kernel used to
	// emit for a connection that outlived a drain.
	poisoned := fixture.validItem("poisoned-1")
	poisoned["upload_bytes"] = 100
	poisoned["download_bytes"] = 200
	poisoned["payload_first_at"] = poisoned["started_at"]
	poisoned["payload_last_at"] = time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	items = append(items, poisoned)

	response := postAgentAudit(t, fixture.handler, fixture.server.AgentID, "audit-token", items, http.StatusOK)
	if len(response.Discarded) != 1 || response.Discarded[0].ReportID != "poisoned-1" || response.Discarded[0].Reason != "invalid_payload_window" {
		t.Fatalf("poisoned report was not terminally discarded: %#v", response.Discarded)
	}
	accepted := map[string]bool{}
	for _, id := range response.Accepted {
		accepted[id] = true
	}
	if !accepted["poisoned-1"] {
		t.Fatal("discarded report must also be acknowledged so the Agent stops retrying it")
	}
	for i := 0; i < 10; i++ {
		id := "good-" + string(rune('a'+i))
		if !accepted[id] {
			t.Fatalf("valid report %s was lost with the poisoned one: %#v", id, response.Accepted)
		}
	}
	overview, err := fixture.db.ConnectionAuditOverview(context.Background(), 24, true, store.DefaultAuditPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if overview.ReportingUserCount != 1 {
		t.Fatalf("valid reports were not stored: %#v", overview)
	}
}

// A batch without any usable report identity is a protocol error, not a
// poisoned record, and must still fail the request.
func TestConnectionAuditWithoutReportIDStillFails(t *testing.T) {
	fixture := newAuditReportFixture(t)
	item := fixture.validItem("")
	postAgentAudit(t, fixture.handler, fixture.server.AgentID, "audit-token", []map[string]any{item}, http.StatusBadRequest)
}

// Terminal discard must not weaken the ownership boundary: a report about
// another server's inbound is still a security failure for the whole request.
func TestConnectionAuditCrossServerClaimStillFailsTheRequest(t *testing.T) {
	fixture := newAuditReportFixture(t)
	ctx := context.Background()
	other := &model.Server{Name: "other-node", AgentID: "other-agent", AgentTokenHash: security.HashSecret("other-token"), ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := fixture.db.CreateServer(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherInbound := &model.Inbound{ServerID: other.ID, Name: "other-entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 8443, ConfigJSON: "{}", Enabled: true}
	if err := fixture.db.CreateInbound(ctx, otherInbound); err != nil {
		t.Fatal(err)
	}
	grantTestPlanInboundNode(t, fixture.db, fixture.user.ID, otherInbound.ID)
	item := fixture.validItem("cross-server")
	item["inbound_id"] = otherInbound.ID
	postAgentAudit(t, fixture.handler, fixture.server.AgentID, "audit-token", []map[string]any{item}, http.StatusForbidden)
}

// A user removed after the Agent produced the report must be answered with a
// terminal rejection, not an endless 400.
func TestAgentTrafficRejectsReportForDeletedUser(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	if err := db.Delete(ctx, "users", user.ID); err != nil {
		t.Fatal(err)
	}
	response := postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-deleted-user", 0, 100, 0, 200), http.StatusOK)
	assertTrafficRejection(t, response, "tr-deleted-user", "user_deleted")
}

func TestAgentTrafficRejectsReportForInactiveUser(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = "inactive"
	if err := db.UpdateUser(ctx, stored); err != nil {
		t.Fatal(err)
	}
	response := postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-inactive-user", 0, 100, 0, 200), http.StatusOK)
	assertTrafficRejection(t, response, "tr-inactive-user", "user_inactive")
}

// An unbound pair on this server's own live inbound is a binding this
// Controller removed while the Agent still held traffic for it. Cross-tenant
// ownership was already refused earlier in validateIdentity, so this is
// terminal for the one report rather than a reason to fail the batch: a 403
// here would stall every other report and the policy response that renews
// traffic leases, until every lease on the server ran out.
func TestAgentTrafficUnboundUserIsRejectedPerReport(t *testing.T) {
	db, server, inbound, _, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	other := &model.User{Username: "unbound", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "44444444-4444-4444-8444-444444444444", ProxyPassword: "unbound-password"}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	response := postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(other.ID, inbound.ID, "tr-unbound", 0, 100, 0, 200), http.StatusOK)
	assertTrafficRejection(t, response, "tr-unbound", "binding_removed")
}

func TestAgentTrafficRejectsReportForDeletedInbound(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	if err := db.Delete(ctx, "inbounds", inbound.ID); err != nil {
		t.Fatal(err)
	}
	response := postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-deleted-inbound", 0, 100, 0, 200), http.StatusOK)
	assertTrafficRejection(t, response, "tr-deleted-inbound", "inbound_deleted")
}

// A terminal rejection must not charge the user anything.
func TestAgentTrafficRejectedReportIsNotBilled(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := stored.TrafficUsedBytes
	stored.Status = "inactive"
	if err := db.UpdateUser(ctx, stored); err != nil {
		t.Fatal(err)
	}
	postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-not-billed", 0, 100, 0, 200), http.StatusOK)
	after, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.TrafficUsedBytes != before {
		t.Fatalf("rejected report was billed: %d -> %d", before, after.TrafficUsedBytes)
	}
}

func assertTrafficRejection(t *testing.T, response map[string]any, reportID, reason string) {
	t.Helper()
	reports, ok := response["accepted_reports"].([]any)
	if !ok || len(reports) == 0 {
		t.Fatalf("no accepted_reports in response: %#v", response)
	}
	for _, raw := range reports {
		item, ok := raw.(map[string]any)
		if !ok || item["report_id"] != reportID {
			continue
		}
		if item["status"] != "rejected" || item["reason"] != reason {
			t.Fatalf("report %s = %#v, want rejected/%s", reportID, item, reason)
		}
		for _, id := range response["accepted_report_ids"].([]any) {
			if id == reportID {
				t.Fatalf("rejected report %s must not be listed as accepted", reportID)
			}
		}
		return
	}
	t.Fatalf("report %s missing from accepted_reports: %#v", reportID, reports)
}
