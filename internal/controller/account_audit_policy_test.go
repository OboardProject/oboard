package controller

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/auditrisk"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAccountAuditPolicyChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	s := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "policy-admin", Role: model.RoleAdmin, Status: "active"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	p := userAutomationPrincipal(t, db, admin.ID)
	c, err := db.GetAccountAuditPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	old := c
	c.Policy.MinimumBytes = 32768
	c.Policy.MinimumSlices = 4
	c.Resources.Connections = &store.AuditResourceThreshold{Start: 0, Full: 100, Unit: "connections"}
	raw, _ := json.Marshal(c)
	applyAutomationChangeset(t, s, p, "policy-configure", automation.OperationRequest{Capability: "audit.policy.update", Input: raw})
	saved, err := db.GetAccountAuditPolicy(ctx)
	if err != nil || saved.Revision != 1 || saved.Policy.Version == old.Policy.Version || saved.Policy.MinimumBytes != 32768 || saved.Policy.MinimumSlices != 4 {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	if _, err := db.SetAccountAuditPolicy(ctx, c, time.Now()); err == nil {
		t.Fatal("stale revision accepted")
	}
	invalid := saved
	invalid.Policy.SourceCapacity = 33
	if err := invalid.Policy.Validate(); err == nil {
		t.Fatal("pure policy exceeds collector capacity")
	}
	if err := store.ValidateAccountAuditPolicy(invalid); err == nil {
		t.Fatal("collector capacity exceeded")
	}
	invalid = saved
	invalid.Resources.Connections = &store.AuditResourceThreshold{Start: 0, Full: 1, Unit: "bytes/second"}
	if err := store.ValidateAccountAuditPolicy(invalid); err == nil {
		t.Fatal("wrong unit accepted")
	}
	for _, denied := range []application.Principal{{Role: model.Role("user")}, {Role: model.RoleAdmin, ResourceFilter: json.RawMessage(`{"user_ids":[1]}`)}} {
		if _, err := s.queryAccountAuditPolicy(ctx, denied); err == nil {
			t.Fatal("restricted principal accepted")
		}
	}
	if _, err := s.queryManagementCapability(ctx, p, "audit.policy.get", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	rows := []auditrisk.SourceActivity{{SourceGroup: "source", Bytes: 16384, Bitmap: 7}}
	before, err := auditrisk.AggregateMinute(rows, true, old.Policy)
	if err != nil {
		t.Fatal(err)
	}
	after, err := auditrisk.AggregateMinute(rows, true, saved.Policy)
	if err != nil || before.Qualified != 1 || after.Qualified != 0 {
		t.Fatalf("minimum bytes not applied: %+v %+v %v", before, after, err)
	}
	rows[0].Bytes = 32768
	after, err = auditrisk.AggregateMinute(rows, true, saved.Policy)
	if err != nil || after.Qualified != 0 {
		t.Fatal("minimum slices not applied")
	}
	rows[0].Bitmap = 15
	after, err = auditrisk.AggregateMinute(rows, true, saved.Policy)
	if err != nil || after.Qualified != 1 {
		t.Fatal("valid source excluded")
	}
}
