package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestOperatorHasFullManagementAccessExceptAdministratorAccounts(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-operator-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorLogin := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-operator-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)

	request(t, h, http.MethodGet, "/api/v1/ui/settings", operatorToken, nil, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/ui/audit-logs", operatorToken, nil, http.StatusOK)
	request(t, h, http.MethodGet, "/api/v1/ui/page-data?page=users", operatorToken, nil, http.StatusOK)

	created := request(t, h, http.MethodPost, "/api/v1/ui/users", operatorToken, map[string]any{"username": "member", "password": "long-member-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	memberID := int64(created["user"].(map[string]any)["id"].(float64))
	request(t, h, http.MethodPatch, "/api/v1/ui/users/"+itoa(memberID), operatorToken, map[string]any{"nickname": "Member"}, http.StatusOK)

	admin, err := db.GetUserByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	request(t, h, http.MethodPost, "/api/v1/ui/users", operatorToken, map[string]any{"username": "second-admin", "password": "long-admin-password", "role": "admin", "status": "active"}, http.StatusForbidden)
	request(t, h, http.MethodPatch, "/api/v1/ui/users/"+itoa(memberID), operatorToken, map[string]any{"role": "admin"}, http.StatusForbidden)
	request(t, h, http.MethodPatch, "/api/v1/ui/users/"+itoa(admin.ID), operatorToken, map[string]any{"nickname": "blocked"}, http.StatusForbidden)
	request(t, h, http.MethodDelete, "/api/v1/ui/users/"+itoa(admin.ID), operatorToken, nil, http.StatusForbidden)
	request(t, h, http.MethodPost, "/api/v1/ui/users/"+itoa(admin.ID)+"/sessions/revoke", operatorToken, map[string]any{}, http.StatusForbidden)
	request(t, h, http.MethodPost, "/api/v1/ui/users/plan-assignment/apply", operatorToken, map[string]any{"user_ids": []int64{admin.ID}, "plan_id": 0}, http.StatusForbidden)
	request(t, h, http.MethodPost, "/api/v1/ui/user-groups", operatorToken, map[string]any{"name": "blocked-admins", "role": "admin", "enabled": true}, http.StatusForbidden)
	groups, err := db.ListUserGroups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		if group.SystemKey == store.UserGroupSystemAdmins {
			request(t, h, http.MethodPost, "/api/v1/ui/user-group-members", operatorToken, map[string]any{"group_id": group.ID, "user_id": memberID, "enabled": true}, http.StatusForbidden)
		}
	}

	member, err := db.GetUser(context.Background(), memberID)
	if err != nil {
		t.Fatal(err)
	}
	if member.Role != model.RoleViewer || member.Nickname != "Member" {
		t.Fatalf("operator's ordinary-user update was not preserved: %#v", member)
	}
}
