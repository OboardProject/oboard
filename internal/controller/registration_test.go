package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func registerFrom(t *testing.T, h http.Handler, username, password, ip string, want int) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"username": username, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v2/ui/auth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":12345"
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != want {
		t.Fatalf("register %s: want %d got %d body=%s", username, want, rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRegistrationAuthorizationFlow(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-session-secret", "").Handler()
	ctx := context.Background()

	request(t, h, "POST", "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	adminLogin := request(t, h, "POST", "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)
	adminToken := adminLogin["token"].(string)

	// Registration is closed by default and the public status reflects it.
	closedStatus := request(t, h, "GET", "/api/v2/ui/auth/registration", "", nil, 200)
	if closedStatus["registration_enabled"] != false {
		t.Fatalf("registration status before enabling = %#v, want false", closedStatus)
	}
	request(t, h, "POST", "/api/v2/ui/auth/register", "", map[string]any{"username": "newbie", "password": "newbie-password-123"}, 403)

	// Admin enables registration.
	request(t, h, "POST", "/api/v2/ui/settings", adminToken, map[string]any{"registration_enabled": true}, 200)
	openStatus := request(t, h, "GET", "/api/v2/ui/auth/registration", "", nil, 200)
	if openStatus["registration_enabled"] != true {
		t.Fatalf("registration status after enabling = %#v, want true", openStatus)
	}

	// Invalid registrations are rejected.
	request(t, h, "POST", "/api/v2/ui/auth/register", "", map[string]any{"username": "ab", "password": "newbie-password-123"}, 400)
	request(t, h, "POST", "/api/v2/ui/auth/register", "", map[string]any{"username": "bad name!", "password": "newbie-password-123"}, 400)
	request(t, h, "POST", "/api/v2/ui/auth/register", "", map[string]any{"username": "newbie", "password": "short"}, 400)
	request(t, h, "POST", "/api/v2/ui/auth/register", "", map[string]any{"username": "seven", "password": "1234567"}, 400)
	request(t, h, "POST", "/api/v2/ui/auth/register", "", map[string]any{"username": "eight", "password": "12345678"}, 201)
	request(t, h, "POST", "/api/v2/ui/auth/register", "", map[string]any{"username": "__oboard_reserved", "password": "newbie-password-123"}, 400)

	// Successful registration gets role none and no user group.
	created := registerFrom(t, h, "newbie", "newbie-password-123", "198.51.100.10", 201)
	user := created["user"].(map[string]any)
	if user["role"] != "none" {
		t.Fatalf("registered user role = %#v, want none", user["role"])
	}

	// The registered user is not auto-added to the builtin users group.
	members, err := db.ListUserGroupMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		group, err := db.GetUserGroup(ctx, member.GroupID)
		if err != nil {
			t.Fatal(err)
		}
		if group.SystemKey == store.UserGroupSystemUsers {
			if member.UserID == int64(user["id"].(float64)) {
				t.Fatalf("registered user was auto-added to the builtin users group")
			}
		}
	}

	// Duplicate usernames conflict case-insensitively.
	request(t, h, "POST", "/api/v2/ui/auth/register", "", map[string]any{"username": "NEWBIE", "password": "newbie-password-123"}, 409)

	// The registered user can log in but has no panel permissions.
	userLogin := request(t, h, "POST", "/api/v2/ui/auth/login", "", map[string]any{"username": "newbie", "password": "newbie-password-123"}, 200)
	userToken := userLogin["token"].(string)
	if role := userLogin["user"].(map[string]any)["role"]; role != "none" {
		t.Fatalf("login effective role = %#v, want none", role)
	}
	request(t, h, "GET", "/api/v2/ui/me", userToken, nil, 200)
	request(t, h, "GET", "/api/v2/ui/me/authentication", userToken, nil, 200)
	request(t, h, "GET", "/api/v2/ui/page-data?page=account", userToken, nil, 200)
	request(t, h, "GET", "/api/v2/ui/page-data?page=dashboard", userToken, nil, 200)
	request(t, h, "GET", "/api/v2/ui/page-data?page=users", userToken, nil, 403)
	request(t, h, "GET", "/api/v2/ui/users", userToken, nil, 403)
	request(t, h, "GET", "/api/v2/ui/settings", userToken, nil, 403)
	request(t, h, "POST", "/api/v2/ui/auth/password", userToken, map[string]any{"current_password": "newbie-password-123", "new_password": "newbie-password-456"}, 200)

	// Admins cannot select the builtin administrators group as the default.
	groups, err := db.ListUserGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var adminGroupID int64
	var userGroupID int64
	for _, group := range groups {
		switch group.SystemKey {
		case store.UserGroupSystemAdmins:
			adminGroupID = group.ID
		case store.UserGroupSystemUsers:
			userGroupID = group.ID
		}
	}
	request(t, h, "POST", "/api/v2/ui/settings", adminToken, map[string]any{"registration_default_group_id": adminGroupID}, 400)

	// Set the builtin users group as the default registration group.
	request(t, h, "POST", "/api/v2/ui/settings", adminToken, map[string]any{"registration_default_group_id": userGroupID}, 200)

	// A new registration joins the default group and inherits its role.
	registerFrom(t, h, "member", "member-password-123", "198.51.100.11", 201)
	memberLogin := request(t, h, "POST", "/api/v2/ui/auth/login", "", map[string]any{"username": "member", "password": "member-password-123"}, 200)
	memberToken := memberLogin["token"].(string)
	if role := memberLogin["user"].(map[string]any)["role"]; role != "viewer" {
		t.Fatalf("default-group member effective role = %#v, want viewer", role)
	}
	request(t, h, "GET", "/api/v2/ui/page-data?page=subscriptions", memberToken, nil, 200)
	request(t, h, "GET", "/api/v2/ui/page-data?page=users", memberToken, nil, 403)

	// Clearing the default group works.
	request(t, h, "POST", "/api/v2/ui/settings", adminToken, map[string]any{"registration_default_group_id": 0}, 200)
	registerFrom(t, h, "solo", "solo-password-123", "198.51.100.12", 201)
	soloLogin := request(t, h, "POST", "/api/v2/ui/auth/login", "", map[string]any{"username": "solo", "password": "solo-password-123"}, 200)
	if role := soloLogin["user"].(map[string]any)["role"]; role != "none" {
		t.Fatalf("solo effective role = %#v, want none", role)
	}
}

func TestAdminCreatedNoneRoleUserGetsNoBuiltinGroup(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-session-secret", "").Handler()
	ctx := context.Background()

	request(t, h, "POST", "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 201)
	adminLogin := request(t, h, "POST", "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, 200)
	adminToken := adminLogin["token"].(string)

	request(t, h, "POST", "/api/v2/ui/users", adminToken, map[string]any{"username": "pending", "nickname": "", "password": "pending-password-123", "role": "none", "status": "active"}, 201)

	user, err := db.GetUserByUsername(ctx, "pending")
	if err != nil {
		t.Fatal(err)
	}
	if user.Role != model.RoleNone {
		t.Fatalf("admin-created user role = %q, want none", user.Role)
	}
	members, err := db.ListUserGroupMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.UserID == user.ID {
			t.Fatalf("admin-created none user has a group membership")
		}
	}
	effective, err := db.EffectiveUserRole(ctx, *user)
	if err != nil {
		t.Fatal(err)
	}
	if effective != model.RoleNone {
		t.Fatalf("effective role = %q, want none", effective)
	}
}
