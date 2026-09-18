package controller

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"golang.org/x/crypto/ssh"
)

func TestSubscriptionPublishesSavedNodesBeforeDeployment(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol model.Protocol
		config   string
		format   string
	}{
		{"mieru", model.ProtocolMieru, `{"transport":"TCP"}`, "mihomo"},
		{"snell-shared", model.ProtocolSnell, `{"version":4,"listener_mode":"shared_port","psk":"inbound-seed-psk-1234"}`, "surge"},
		{"snell-independent", model.ProtocolSnell, `{"version":4,"listener_mode":"per_identity_port","psk":"inbound-seed-psk-1234"}`, "surge"},
		{"ssh", model.ProtocolSSH, `{"exposure_confirmed":true,"exposure_confirmation_version":"ssh-inbound-v1","access_mode":"restricted_proxy"}`, "sing-box"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openControllerAutomationTestStore(t)
			srv := newTestServer(db, "test-secret", "")
			server := &model.Server{Name: "offline", PublicIPv4: "203.0.113.10", PortRangeStart: 40000, PortRangeEnd: 40100}
			if err := db.CreateServer(ctx, server); err != nil {
				t.Fatal(err)
			}
			user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "account-password", SubscriptionToken: "desired-subscription"}
			if err := db.CreateUser(ctx, user); err != nil {
				t.Fatal(err)
			}
			inbound := &model.Inbound{ServerID: server.ID, Name: tc.name, Protocol: tc.protocol, Port: 6160, ConfigJSON: tc.config, Enabled: true}
			if err := db.CreateInbound(ctx, inbound); err != nil {
				t.Fatal(err)
			}
			if tc.protocol == model.ProtocolSSH {
				_, private, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				public, err := ssh.NewPublicKey(private.Public())
				if err != nil {
					t.Fatal(err)
				}
				if err := db.ApplySSHDeploymentState(ctx, model.SSHServerHostKey{ServerID: server.ID, PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), Fingerprint: ssh.FingerprintSHA256(public), ConfigVersion: 1}, nil, 0); err != nil {
					t.Fatal(err)
				}
			}
			plan := &model.SubscriptionPlan{Name: "plan", Enabled: true}
			if err := db.CreateSubscriptionPlan(ctx, plan, nil); err != nil {
				t.Fatal(err)
			}
			if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: plan.ID}}); err != nil {
				t.Fatal(err)
			}
			result, err := srv.createPlanVersion(ctx, plan.ID, store.PlanVersionMutation{Nodes: &store.PlanNodesMutation{Op: "add", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: inbound.ID, Enabled: true}}}, ChangeKind: model.PlanChangeKindNodes})
			if err != nil {
				t.Fatal(err)
			}
			if !result.RequiresDeployment {
				t.Fatal("expected pending plan revision")
			}
			data, err := db.FullRoutingConfigData(ctx)
			if err != nil {
				t.Fatal(err)
			}
			active, err := srv.buildAccessSnapshot(ctx, data)
			if err != nil {
				t.Fatal(err)
			}
			if len(active.EffectiveNodeKeys(user.ID)) != 0 {
				t.Fatal("publication activated runtime access")
			}
			loaded, err := db.LoadProxyCredentials(ctx, srv.sessionSecret, []model.User{*user})
			if err != nil {
				t.Fatal(err)
			}
			pathID := int64(0)
			if tc.protocol == model.ProtocolSSH {
				pathID = core.SSHDirectBranchPathID(inbound.ID)
			}
			identity := core.UserCredentialForRoute(loaded[0], inbound.ID, pathID, tc.protocol)
			if identity.AuthorizationKey == "" {
				t.Fatal("save returned without persisted credentials")
			}
			lease, err := srv.currentAuthorizationLease(ctx, server.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(lease.Grants) != 0 {
				t.Fatal("published candidate received an early runtime lease")
			}
			before, err := db.ListProxyCredentials(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ports, err := db.ListProxyPathPortAllocations(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var body string
			for i := 0; i < 3; i++ {
				nodes, _, err := srv.workspaceSubscriptionNodes(ctx, *user, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(nodes) != 1 {
					t.Fatalf("pull %d returned %d nodes before deployment", i, len(nodes))
				}
				r := httptest.NewRecorder()
				srv.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/desired-subscription?format="+tc.format, nil))
				if r.Code != http.StatusOK {
					t.Fatalf("subscription HTTP status %d", r.Code)
				}
				if !strings.Contains(r.Body.String(), identity.ProxyPassword) {
					t.Fatal("subscription missing saved credential")
				}
				if i > 0 && r.Body.String() != body {
					t.Fatal("subscription changed without a desired-state change")
				}
				body = r.Body.String()
			}
			after, _ := db.ListProxyCredentials(ctx)
			afterPorts, _ := db.ListProxyPathPortAllocations(ctx)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(ports, afterPorts) {
				t.Fatal("subscription reads allocated credentials or ports")
			}
			if _, err := srv.createPlanVersion(ctx, plan.ID, store.PlanVersionMutation{Nodes: &store.PlanNodesMutation{Op: "remove", Nodes: []model.SubscriptionPlanNode{{NodeType: model.AssignableNodeInbound, NodeID: inbound.ID}}}, ChangeKind: model.PlanChangeKindNodes}); err != nil {
				t.Fatal(err)
			}
			nodes, _, err := srv.workspaceSubscriptionNodes(ctx, *user, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) != 0 {
				t.Fatal("removed desired grant remained in subscription")
			}
		})
	}
}

func TestSubscriptionDesiredBindingsRespectTimeWindows(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	srv := newTestServer(db, "test-secret", "")
	now := time.Now()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	data := store.FullRoutingConfig{
		Users:                 []model.User{{ID: 1, Status: "active"}},
		Inbounds:              []model.Inbound{{ID: 1, Enabled: true, Protocol: model.ProtocolMieru}},
		SubscriptionPlans:     []model.SubscriptionPlan{{ID: 1, Enabled: true}},
		SubscriptionPlanNodes: []model.SubscriptionPlanNode{{PlanID: 1, NodeType: model.AssignableNodeInbound, NodeID: 1, Enabled: true}},
	}
	for _, tc := range []struct {
		name       string
		start, end *time.Time
		want       int
	}{{"pending-now", nil, nil, 1}, {"future", &future, nil, 0}, {"expired", nil, &past, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			data.SubscriptionPlanBindings = []model.UserPlanBinding{{UserID: 1, PlanID: 1, Enabled: true, StartsAt: tc.start, ExpiresAt: tc.end}}
			snap, err := srv.buildSubscriptionAccessSnapshot(ctx, data)
			if err != nil {
				t.Fatal(err)
			}
			if len(snap.EffectiveNodeKeys(1)) != tc.want {
				t.Fatal("subscription ignored assignment time window")
			}
		})
	}
}

func TestSubscriptionPublishesPendingAssignmentBeforeActivation(t *testing.T) {
	h, srv, token := setupPlansAPITestServer(t)
	server := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "pending-assignment", "entry_ip_mode": "custom", "entry_address": "203.0.113.10", "port_range_start": 40000, "port_range_end": 40100}, http.StatusCreated)["server"].(map[string]any)
	inbound := request(t, h, http.MethodPost, "/api/v1/ui/inbounds", token, map[string]any{"server_id": server["id"], "name": "mieru", "protocol": "mieru", "port": 6160, "config_json": `{"transport":"TCP"}`, "enabled": true}, http.StatusCreated)["inbound"].(map[string]any)
	user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "pending-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	plan := request(t, h, http.MethodPost, "/api/v1/ui/subscription-plans", token, map[string]any{"name": "pending-plan", "enabled": true, "nodes": []map[string]any{{"node_type": "inbound", "node_id": inbound["id"]}}}, http.StatusCreated)["subscription_plan"].(map[string]any)
	applied := request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", token, map[string]any{"user_ids": []int64{userID}, "plan_id": plan["id"], "deploy": true}, http.StatusOK)
	if applied["status"] != "preparing" {
		t.Fatal("assignment unexpectedly activated")
	}
	account, err := srv.store.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _, err := srv.workspaceSubscriptionNodes(context.Background(), *account, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("saved pending assignment returned %d subscription nodes", len(nodes))
	}
	data, err := srv.store.FullRoutingConfigData(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	active, err := srv.buildAccessSnapshot(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if len(active.EffectiveNodeKeys(userID)) != 0 {
		t.Fatal("publication bypassed runtime activation")
	}
}
