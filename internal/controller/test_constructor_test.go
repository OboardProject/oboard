package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// snapshotBindingsFromData resolves the effective proxy-path bindings from one
// routing snapshot.
func snapshotBindingsFromData(data store.FullRoutingConfig) []model.ProxyPathUser {
	snapshot := core.BuildEffectiveAccessSnapshot(core.EffectiveAccessInput{
		Users:             data.Users,
		Bindings:          data.PlanBindings,
		Plans:             data.SubscriptionPlans,
		PlanNodes:         data.ActivePlanNodes,
		Exceptions:        data.UserNodeExceptions,
		Paths:             data.ProxyPaths,
		Steps:             data.ProxyPathSteps,
		Inbounds:          data.Inbounds,
		ExternalOutbounds: data.ExternalOutbounds,
		Now:               time.Now(),
	})
	return snapshot.ProxyPathUserBindings()
}

func newTestServer(store *store.Store, sessionSecret, staticDir string) *Server {
	return New(store, sessionSecret, staticDir, "", nil)
}

// grantTestPlanInboundNode binds the user to a fresh plan containing the
// inbound node so the effective access snapshot authorizes the user.
func grantTestPlanInboundNode(t *testing.T, db *store.Store, userID, inboundID int64) {
	t.Helper()
	grantTestPlanNode(t, db, userID, model.AssignableNodeInbound, inboundID)
}

// grantTestPlanNode binds the user to a fresh plan containing one node.
func grantTestPlanNode(t *testing.T, db *store.Store, userID int64, nodeType model.AssignableNodeType, nodeID int64) {
	t.Helper()
	ctx := context.Background()
	plan := &model.SubscriptionPlan{Name: fmt.Sprintf("test-plan-%s-%d", nodeType, nodeID), Enabled: true}
	if err := db.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{{NodeType: nodeType, NodeID: nodeID}}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: userID, PlanID: plan.ID}}); err != nil {
		t.Fatal(err)
	}
}
