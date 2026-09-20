package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAccountAuditStatusSmoke(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "status-admin", Role: model.RoleAdmin, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.MarkAccountAuditDirty(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	p := userAutomationPrincipal(t, db, user.ID)
	if _, ok := s.capabilities.Authorize(p, "audit.status.read"); !ok {
		t.Fatal("missing capability")
	}
	raw, err := s.queryManagementCapability(ctx, p, "audit.status.read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	got := raw.(accountAuditStatusResponse)
	if got.Status != "pending" || got.LastSnapshotTime != nil || got.PendingEvaluations == nil || *got.PendingEvaluations != 1 || got.PendingInboxReports == nil || *got.PendingInboxReports != 0 || got.ActionMode != "alert_only" || got.OldestBacklogAgeSeconds != nil {
		t.Fatalf("unexpected status: %+v", got)
	}
	p.ResourceFilter = json.RawMessage(fmt.Sprintf(`{"users":{"mode":"selected","ids":[%d]}}`, user.ID+1))
	raw, err = s.queryManagementCapability(ctx, p, "audit.status.read", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	scoped := raw.(accountAuditStatusResponse)
	if scoped.PendingInboxReports != nil || scoped.PendingInboxBytes != nil || scoped.PendingEvaluations == nil || *scoped.PendingEvaluations != 0 {
		t.Fatalf("scope leaked: %+v", scoped)
	}
	w := httptest.NewRecorder()
	s.accountAuditStatusUI(w, httptest.NewRequest("GET", "/api/v1/audit/status", nil))
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HTTP status %d", w.Code)
	}
	work, err := db.ListAccountAuditWork(ctx, 0, 10)
	if err != nil || len(work) != 1 {
		t.Fatalf("read consumed work: %v %v", work, err)
	}
}
