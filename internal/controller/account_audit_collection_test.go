package controller

import (
	"context"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
	"testing"
)

func TestAuditCollectionChangesetDelivery(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "collection-admin", Role: model.RoleAdmin, Status: "active"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	p := userAutomationPrincipal(t, db, admin.ID)
	applyAutomationChangeset(t, s, p, "collection-standard", automation.OperationRequest{Capability: "audit.collection.update", Input: json.RawMessage(`{"mode":"standard","revision":0,"diagnostics":[]}`)})
	c, err := db.AuditCollection(ctx)
	if err != nil || c.Mode != "standard" || c.Revision != 1 {
		t.Fatalf("config=%+v err=%v", c, err)
	}
	if e := s.effectiveAuditCollection(ctx, 1); e.Mode != "standard" || e.Revision != 1 {
		t.Fatalf("delivery=%+v", e)
	}
	if _, err := s.queryManagementCapability(ctx, p, "audit.collection.get", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestAuditCollectionAdministratorBoundary(t *testing.T) {
	for _, role := range []model.Role{model.RoleViewer, model.RoleOperator} {
		if auditCollectionPrincipal(application.Principal{Role: role}) == nil {
			t.Fatalf("accepted %s", role)
		}
	}
	if err := auditCollectionPrincipal(application.Principal{Role: model.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	if auditCollectionPrincipal(application.Principal{Role: model.RoleAdmin, ResourceFilter: json.RawMessage(`{"user_ids":[1]}`)}) == nil {
		t.Fatal("scoped admin changed global collection")
	}
	catalog := capability.NewCatalog()
	d, ok := catalog.Get("audit.collection.update")
	if !ok || !d.AdminOnly || !d.Executable || !d.MCPEnabled || d.ApprovalPolicy != "required" {
		t.Fatalf("unsafe descriptor %+v", d)
	}
}
