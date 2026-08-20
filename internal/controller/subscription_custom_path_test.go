package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestSubscriptionCustomPathAutomationRequiresApproval(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	admin := &model.User{Username: "automation-admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "automation-admin-uuid", ProxyPassword: "password", SubscriptionToken: "admin-token"}
	target := &model.User{Username: "automation-target", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "automation-target-uuid", ProxyPassword: "password", SubscriptionToken: "target-token"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(ctx, application.SubscriptionCustomPathModeSetting, string(model.SubscriptionCustomPathEnabled)); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	input, _ := json.Marshal(map[string]any{"user_id": target.ID, "alias": "automation-path", "delete": false})
	base, _ := json.Marshal(map[string]string{"subscription_custom_path:user:" + itoa(target.ID): target.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "custom-path", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "subscriptions.custom_paths.set_alias", Input: input}}})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := server.automation.Validate(ctx, principal, changeset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if validated.Status != model.ChangesetAwaitingApproval {
		t.Fatalf("validated status=%s", validated.Status)
	}
	if _, err := db.GetSubscriptionCustomPathForUser(ctx, target.ID); err == nil {
		t.Fatal("validation mutated the custom path")
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Apply(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	path, err := db.GetSubscriptionCustomPathForUser(ctx, target.ID)
	if err != nil || path.Alias != "automation-path" {
		t.Fatalf("applied path=%#v err=%v", path, err)
	}
}

func TestSubscriptionCustomPathSelfServiceAndLifecycle(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "custom-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(created["id"].(float64))
	persistentToken := created["subscription_token"].(string)
	groups := request(t, h, http.MethodGet, "/api/v1/ui/user-groups", adminToken, nil, http.StatusOK)["user_groups"].([]any)
	var usersGroupID int64
	for _, raw := range groups {
		group := raw.(map[string]any)
		if group["system_key"] == store.UserGroupSystemUsers {
			usersGroupID = int64(group["id"].(float64))
		}
	}
	if usersGroupID == 0 {
		t.Fatal("built-in users group not found")
	}
	request(t, h, http.MethodPatch, "/api/v1/ui/settings", adminToken, map[string]any{"subscription_custom_path_mode": "selective"}, http.StatusOK)
	request(t, h, http.MethodPatch, "/api/v1/ui/user-groups/"+itoa(usersGroupID)+"/subscription-custom-path-policy", adminToken, map[string]any{"mode": "allow"}, http.StatusOK)
	viewerToken := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "custom-user", "password": "long-user-password"}, http.StatusOK)["token"].(string)
	request(t, h, http.MethodPut, "/api/v1/ui/me/subscription-custom-path", viewerToken, map[string]any{"alias": "friendly-user"}, http.StatusOK)
	request(t, h, http.MethodPut, "/api/v1/ui/me/subscription-custom-path", viewerToken, map[string]any{"alias": "Bad Alias"}, http.StatusBadRequest)

	fetch := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		return response
	}
	if response := fetch("/s/friendly-user?format=sing-box"); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"outbounds"`) {
		t.Fatalf("custom subscription status=%d body=%s", response.Code, response.Body.String())
	}
	request(t, h, http.MethodPatch, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/policy", adminToken, map[string]any{"burn_after_read": true}, http.StatusOK)
	if response := fetch("/api/v1/subscriptions/" + persistentToken); response.Code != http.StatusOK || response.Header().Get("X-OBoard-Subscription") != "burned-after-read" {
		t.Fatalf("persistent burn status=%d headers=%#v", response.Code, response.Header())
	}
	if response := fetch("/s/friendly-user"); response.Code != http.StatusOK {
		t.Fatalf("custom path was burned with persistent token: %d %s", response.Code, response.Body.String())
	}
	request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/rotate", adminToken, map[string]any{}, http.StatusOK)
	if response := fetch("/s/friendly-user"); response.Code != http.StatusOK {
		t.Fatalf("custom path did not survive rotation: %d", response.Code)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(userID)+"/subscription-token/revoke", adminToken, map[string]any{}, http.StatusOK)
	if response := fetch("/s/friendly-user"); response.Code != http.StatusNotFound {
		t.Fatalf("custom path survived revocation: %d %s", response.Code, response.Body.String())
	}
	if got := requestLogPath("/s/private-alias"); got != "/s/[redacted]" {
		t.Fatalf("custom path log was not redacted: %q", got)
	}
}

func TestSubscriptionCustomPathHonorsBasePath(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := &model.User{Username: "base-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "base-user-uuid", ProxyPassword: "password", SubscriptionToken: "base-token"}
	if err := db.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSetting(context.Background(), application.SubscriptionCustomPathModeSetting, string(model.SubscriptionCustomPathEnabled)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetSubscriptionCustomPath(context.Background(), user.ID, "base-alias"); err != nil {
		t.Fatal(err)
	}
	h := New(db, "test-secret", "", "/hidden", nil).Handler()
	for path, want := range map[string]int{"/s/base-alias": http.StatusNotFound, "/hidden/s/base-alias": http.StatusOK, "/hidden/s/base-alias/": http.StatusNotFound, "/hidden/s/base/alias": http.StatusNotFound} {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != want {
			t.Errorf("GET %s status=%d want=%d body=%s", path, response.Code, want, response.Body.String())
		}
	}
}
