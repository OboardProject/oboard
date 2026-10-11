package store

import (
	"context"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/model"
	"testing"
)

func TestProxyDSCPPolicyRemovedBeforeIDReuse(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	old := &model.Server{Name: "old-dscp"}
	if err := db.CreateServer(ctx, old); err != nil {
		t.Fatal(err)
	}
	enabled := true
	p, err := db.SetServerProxyDSCP(ctx, *old, &enabled, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveProxyDSCPReport(ctx, *old, model.ProxyDSCPReport{ProxyDSCPPolicy: p, State: "partial", Value: 46}); err != nil {
		t.Fatal(err)
	}
	if err = db.DeleteServer(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	next := &model.Server{Name: "replacement-dscp"}
	if err = db.CreateServer(ctx, next); err != nil {
		t.Fatal(err)
	}
	if next.ID != old.ID {
		t.Fatal("expected smallest free ID", next.ID, old.ID)
	}
	on, err := db.ProxyDSCPOverride(ctx, next.ID)
	if err != nil || on {
		t.Fatal("old override retained", err)
	}
	desired, err := db.ProxyDSCPDesired(ctx, next.ID)
	if err != nil || desired.Revision != 0 {
		t.Fatal("old desired retained", err)
	}
	report, err := db.ProxyDSCPReport(ctx, next.ID)
	if err != nil || report != nil {
		t.Fatal("old report retained", err)
	}
	if _, err = db.SetServerProxyDSCP(ctx, *old, &enabled, false); err == nil {
		t.Fatal("retired server updated replacement")
	}
}
func TestProxyDSCPTaskRejectsSupersededDesiredState(t *testing.T) {
	db := openTestStore(t)
	ctx := context.Background()
	node := &model.Server{Name: "task-dscp"}
	if err := db.CreateServer(ctx, node); err != nil {
		t.Fatal(err)
	}
	enabled := true
	p, err := db.SetServerProxyDSCP(ctx, *node, &enabled, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	enabled = false
	if _, err = db.SetServerProxyDSCP(ctx, *node, &enabled, false); err != nil {
		t.Fatal(err)
	}
	task := model.AgentTask{ServerID: node.ID, Type: model.AgentTaskTypeProxyDSCP, PayloadJSON: string(raw), Nonce: "stale-dscp", Status: "pending", ResultJSON: "{}"}
	if err = db.CreateProxyDSCPTask(ctx, *node, &task); err == nil {
		t.Fatal("obsolete desired task queued")
	}
}
