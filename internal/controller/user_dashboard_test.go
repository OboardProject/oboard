package controller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestUserDashboardOverviewUsesEffectivePlanNodesAndTraffic(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	user := &model.User{Username: "dashboard-user", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "dashboard-uuid", ProxyPassword: "dashboard-password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	plan := &model.SubscriptionPlan{Name: "dashboard-plan", Enabled: true, TrafficLimitBytes: 1000, TrafficResetMode: model.TrafficResetMonthly, TrafficResetDay: 1}
	if err := db.CreateSubscriptionPlan(ctx, plan, []model.SubscriptionPlanNode{
		{NodeType: model.AssignableNodeExternalOutbound, NodeID: 10},
		{NodeType: model.AssignableNodeExternalOutbound, NodeID: 11},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: plan.ID}}); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(time.Hour)
	for _, exception := range []*model.UserNodeException{
		{UserID: user.ID, NodeType: model.AssignableNodeExternalOutbound, NodeID: 11, Effect: model.UserNodeExceptionDeny, Reason: "deny", ExpiresAt: &expiresAt},
		{UserID: user.ID, NodeType: model.AssignableNodeExternalOutbound, NodeID: 12, Effect: model.UserNodeExceptionAllow, Reason: "allow", ExpiresAt: &expiresAt},
	} {
		if err := db.CreateUserNodeException(ctx, exception); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSetting(ctx, settingAuditEnabled, "false"); err != nil {
		t.Fatal(err)
	}

	overview, err := newTestServer(db, "test-secret", "").userDashboardOverview(ctx, *user)
	if err != nil {
		t.Fatal(err)
	}
	if overview.AssignedNodeCount != 2 {
		t.Fatalf("assigned nodes = %d, want 2", overview.AssignedNodeCount)
	}
	if !overview.HasActivePlan || overview.AccountStatus != userDashboardStatusNormal || len(overview.StatusReasons) != 0 {
		t.Fatalf("unexpected account status: %#v", overview)
	}
	if overview.Traffic.LimitBytes != 1000 || overview.Traffic.UsedBytes != 0 || overview.Traffic.QuotaState != "active" || overview.Traffic.PeriodEnd == "" {
		t.Fatalf("unexpected traffic summary: %#v", overview.Traffic)
	}
	if overview.Audit.Enabled || overview.Audit.Risk {
		t.Fatalf("disabled audit reported a result: %#v", overview.Audit)
	}
}

func TestUserDashboardStatusAndRiskThresholds(t *testing.T) {
	overview := userDashboardOverview{
		HasActivePlan: false,
		Traffic:       userDashboardTraffic{QuotaState: "quota_exceeded"},
		Audit:         userDashboardAudit{Enabled: true, Risk: true},
	}
	user := model.User{Status: "disabled", SubscriptionSuspended: true}
	want := []string{
		userDashboardReasonAccountInactive,
		userDashboardReasonNoActivePlan,
		userDashboardReasonSubscriptionSuspended,
		userDashboardReasonQuotaExceeded,
		userDashboardReasonAuditRisk,
	}
	got := userDashboardStatusReasons(user, overview)
	if len(got) != len(want) {
		t.Fatalf("status reasons = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("status reasons = %#v, want %#v", got, want)
		}
	}
	for _, test := range []struct {
		connection, subscription int
		want                     bool
	}{
		{54, 0, false},
		{55, 0, true},
		{40, 40, false},
		{50, 25, true},
	} {
		if got := userDashboardAuditRisk(test.connection, test.subscription); got != test.want {
			t.Fatalf("audit risk (%d,%d) = %v, want %v", test.connection, test.subscription, got, test.want)
		}
	}
}

func TestUserDashboardPageDataIsSelfScopedForViewerAndNone(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()

	for _, role := range []model.Role{model.RoleViewer, model.RoleNone} {
		user := &model.User{Username: "dashboard-" + string(role), PasswordHash: "unused", Role: role, Status: "active", ProxyUUID: "uuid-" + string(role), ProxyPassword: "password-" + string(role), SubscriptionToken: "secret-" + string(role)}
		if err := db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
		token, err := security.SignSession("test-secret", security.TokenClaims{Subject: user.ID, Role: string(role), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		page := request(t, handler, "GET", "/api/v1/ui/page-data?page=dashboard", token, nil, 200)
		overview, ok := page["user_overview"].(map[string]any)
		if !ok || overview["assigned_node_count"] != float64(0) || overview["account_status"] != userDashboardStatusAttention {
			t.Fatalf("%s dashboard overview = %#v", role, page["user_overview"])
		}
		for _, forbidden := range []string{"summary", "servers", "users", "subscription_plans", "agent_tasks", "connection_audit", "settings"} {
			if _, exists := page[forbidden]; exists {
				t.Fatalf("%s dashboard leaked %s: %#v", role, forbidden, page)
			}
		}
		encoded := page["current_user"].(map[string]any)
		for _, secret := range []string{"proxy_uuid", "proxy_password", "subscription_token"} {
			if _, exists := encoded[secret]; exists {
				t.Fatalf("%s dashboard leaked %s: %#v", role, secret, encoded)
			}
		}
	}
}

func TestUserDashboardAnnouncementsAreSelfScoped(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin := &model.User{Username: "announcement-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "admin-uuid", ProxyPassword: "admin-password"}
	target := &model.User{Username: "announcement-target", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "target-uuid", ProxyPassword: "target-password"}
	other := &model.User{Username: "announcement-other", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "other-uuid", ProxyPassword: "other-password"}
	for _, user := range []*model.User{admin, target, other} {
		if err := db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	for _, announcement := range []*model.NotificationAnnouncement{
		{ActorUserID: admin.ID, ActorName: "系统管理员", Title: "目标用户旧公告", Body: "较早发布", UserIDs: []int64{target.ID}},
		{ActorUserID: admin.ID, ActorName: "系统管理员", Title: "其他用户公告", Body: "不能泄露给目标用户", UserIDs: []int64{other.ID}},
		{ActorUserID: admin.ID, ActorName: "系统管理员", Title: "目标用户公告", Body: "仅目标用户可见", UserIDs: []int64{target.ID}},
	} {
		if err := db.CreateNotificationAnnouncement(ctx, announcement); err != nil {
			t.Fatal(err)
		}
	}
	token, err := security.SignSession("test-secret", security.TokenClaims{Subject: target.ID, Role: string(model.RoleViewer), ClientBinding: sessionClientBinding("test-secret", ""), Expiry: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	page := request(t, newTestServer(db, "test-secret", "").Handler(), "GET", "/api/v1/ui/page-data?page=dashboard", token, nil, 200)
	items, ok := page["user_announcements"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("target announcements = %#v", page["user_announcements"])
	}
	item := items[0].(map[string]any)
	if item["title"] != "目标用户公告" || item["body"] != "仅目标用户可见" || item["actor_name"] != "系统管理员" {
		t.Fatalf("unexpected announcement = %#v", item)
	}
	for _, privateField := range []string{"actor_user_id", "user_ids", "queued_count"} {
		if _, exists := item[privateField]; exists {
			t.Fatalf("announcement leaked %s: %#v", privateField, item)
		}
	}
	if older := items[1].(map[string]any); older["title"] != "目标用户旧公告" {
		t.Fatalf("announcements are not newest first: %#v", items)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), "其他用户公告") || strings.Contains(string(encoded), "不能泄露给目标用户") {
		t.Fatalf("dashboard leaked another user's announcement: %s", encoded)
	}
}
