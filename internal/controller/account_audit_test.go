package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/OboardProject/oboard/internal/auditrisk"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAccountAuditReadContractAndScopes(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "audit-admin", Role: model.RoleAdmin, Status: "active"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	other := &model.User{Username: "audit-other", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, other); err != nil {
		t.Fatal(err)
	}
	p := userAutomationPrincipal(t, db, admin.ID)
	for _, view := range []string{"accounts", "events", "executions"} {
		name := "audit." + view + ".list"
		if _, ok := server.capabilities.Authorize(p, name); !ok {
			t.Fatalf("missing capability %s", name)
		}
		got, err := server.queryManagementCapability(ctx, p, name, json.RawMessage(`{"limit":10}`))
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		server.accountAuditUI(w, httptest.NewRequest("GET", "/api/v1/audit/"+view+"?limit=10", nil))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("bad HTTP %d", w.Code)
		}
		var expected, actual any
		raw, _ := json.Marshal(got)
		json.Unmarshal(raw, &expected)
		json.Unmarshal(w.Body.Bytes(), &actual)
		if !reflect.DeepEqual(expected, actual) {
			t.Fatalf("UI/MCP divergence: %s vs %s", raw, w.Body.String())
		}
	}
	good := auditrisk.Dimension{State: auditrisk.Satisfied}
	for minute := 1; minute <= 2; minute++ {
		when := time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
		snap := auditrisk.Snapshot{AccountID: admin.ID, Status: "evaluated", AsOf: when, WindowEnd: when, WindowStart: when.Add(-30 * time.Minute), Activity: &auditrisk.Score{Lower: 80, Upper: 80, Status: "complete"}, Quality: auditrisk.Quality{IdentityTrusted: good, SourceUsable: good, Deduplicated: good, MeasurementValid: good, TimeAligned: good, Freshness: good, CapabilitySupported: good}}
		if err := db.SaveAccountAuditSnapshot(ctx, snap, int64(minute)); err != nil {
			t.Fatal(err)
		}
	}
	events, err := db.ListAccountAuditEvents(ctx, store.AccountAuditQuery{})
	if err != nil {
		t.Fatal(err)
	}
	eventID := events.Items.([]store.AccountAuditEvent)[0].ID
	input := json.RawMessage(fmt.Sprintf(`{"event_id":%d}`, eventID))
	detail, err := server.queryManagementCapability(ctx, p, "audit.events.list", input)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	server.accountAuditUI(w, httptest.NewRequest("GET", fmt.Sprintf("/api/v1/audit/events?event_id=%d", eventID), nil))
	want, _ := json.Marshal(detail)
	var a, b any
	json.Unmarshal(want, &a)
	json.Unmarshal(w.Body.Bytes(), &b)
	if w.Code != 200 || !reflect.DeepEqual(a, b) || decodeAccountAuditEventRows(t, detail.(store.AccountAuditPage))[0].Snapshot == nil {
		t.Fatal("MCP/UI detail divergence")
	}
	event := events.Items.([]store.AccountAuditEvent)[0]
	reviewInput := json.RawMessage(fmt.Sprintf(`{"user_id":%d,"event_id":%d,"expected_revision":%d,"status":"handled","reason":"verified authorized usage"}`, admin.ID, eventID, event.Revision))
	if _, err := server.accountAuditReviewCandidate(ctx, p, reviewInput); err != nil {
		t.Fatal(err)
	}
	forged := json.RawMessage(fmt.Sprintf(`{"user_id":%d,"event_id":%d,"expected_revision":%d,"status":"handled","reason":"forged owner"}`, other.ID, eventID, event.Revision))
	if _, err := server.accountAuditReviewCandidate(ctx, p, forged); err == nil {
		t.Fatal("event owner mismatch accepted")
	}
	if err := db.SetAccountAuditEventStatus(ctx, eventID, event.Revision, "handled", "admin", "verified authorized usage", time.Time{}, time.Date(2026, 1, 1, 0, 3, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := server.accountAuditReviewCandidate(ctx, p, reviewInput); err == nil {
		t.Fatal("stale review accepted")
	}
	actions, err := server.queryManagementCapability(ctx, p, "audit.executions.list", input)
	if err != nil || len(actions.(store.AccountAuditPage).Items.([]store.AccountAuditAction)) != 1 {
		t.Fatal("real action not exposed", err)
	}
	p.ResourceFilter = json.RawMessage(fmt.Sprintf(`{"users":{"mode":"selected","ids":[%d]}}`, other.ID))
	if _, err := server.accountAuditReviewCandidate(ctx, p, reviewInput); err == nil {
		t.Fatal("unauthorized review accepted")
	}
	denied, err := server.queryManagementCapability(ctx, p, "audit.events.list", input)
	if err != nil || len(decodeAccountAuditEventRows(t, denied.(store.AccountAuditPage))) != 0 {
		t.Fatal("event detail scope leaked", err)
	}
	result, err := server.queryManagementCapability(ctx, p, "audit.accounts.list", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	rows := result.(store.AccountAuditPage).Items.([]store.AccountAuditRow)
	if len(rows) != 1 || rows[0].UserID != other.ID || rows[0].Snapshot != nil || rows[0].EvaluationStatus != "pending" {
		t.Fatalf("invalid scoped pending result %+v", rows)
	}
	p.ResourceFilter = json.RawMessage(`{"servers":{"mode":"all"}}`)
	result, err = server.queryManagementCapability(ctx, p, "audit.accounts.list", json.RawMessage(`{}`))
	if err != nil || len(result.(store.AccountAuditPage).Items.([]store.AccountAuditRow)) != 0 {
		t.Fatal("missing user scope leaked")
	}
}
func decodeAccountAuditEventRows(t *testing.T, page store.AccountAuditPage) []store.AccountAuditEvent {
	t.Helper()
	raw, err := json.Marshal(page.Items)
	if err != nil {
		t.Fatal(err)
	}
	var rows []store.AccountAuditEvent
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAccountAuditReadRejectsMalformedAndWrites(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	for _, target := range []string{"?limit=101", "?limit=bad", "?offset=-1", "?user_id=-1", "?status=bogus", "?event_id=-1", "?event_id=bad"} {
		w := httptest.NewRecorder()
		s.accountAuditUI(w, httptest.NewRequest("GET", "/api/v1/audit/accounts"+target, nil))
		if w.Code != 400 {
			t.Fatalf("accepted %s: %d", target, w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.accountAuditUI(w, httptest.NewRequest("POST", "/api/v1/audit/accounts", nil))
	if w.Code != 405 {
		t.Fatal("write accepted")
	}
}
