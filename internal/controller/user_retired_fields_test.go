package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
)

func TestUserWritesRejectRetiredDeviceFields(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	created := request(t, handler, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "ordinary", "password": "very-secure-password"}, http.StatusCreated)
	id := int64(created["user"].(map[string]any)["id"].(float64))
	ctx := context.Background()
	admin, err := db.GetUserByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	user, err := db.GetUser(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	user.DeviceLimit = 7
	user.LegacyProxyEnabled = false
	user.LegacyProxyEnabledSet = true
	if err := db.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"device_limit", "legacy_proxy_enabled"} {
		for _, value := range []any{nil, false, 0, 2} {
			payload := map[string]any{"username": "rejected", field: value}
			request(t, handler, http.MethodPost, "/api/v1/ui/users", token, payload, http.StatusBadRequest)
			request(t, handler, http.MethodPatch, "/api/v1/ui/users/"+itoa(id), token, map[string]any{field: value}, http.StatusBadRequest)
			create, _ := json.Marshal(map[string]any{"user": payload})
			update, _ := json.Marshal(map[string]any{"user_id": id, "changes": map[string]any{field: value}})
			if _, _, err := server.userCreateAutomationCandidate(ctx, principal, create); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("create accepted %s: %v", field, err)
			}
			if _, _, err := server.userUpdateAutomationCandidate(ctx, principal, update); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("update accepted %s: %v", field, err)
			}
			for name, input := range map[string]json.RawMessage{"users.create": create, "users.update": update} {
				if _, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: name, Input: input}}}); err == nil {
					t.Fatalf("catalog accepted %s in %s", field, name)
				}
			}
		}
	}
	request(t, handler, http.MethodPatch, "/api/v1/ui/users/"+itoa(id), token, map[string]any{"nickname": "updated"}, http.StatusOK)
	applyAutomationChangeset(t, server, principal, "preserve-retired-user-fields", automation.OperationRequest{Capability: "users.update", Input: json.RawMessage(`{"user_id":` + itoa(id) + `,"changes":{"nickname":"mcp updated"}}`)})
	saved, err := db.GetUser(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if saved.DeviceLimit != 7 || saved.LegacyProxyEnabled || saved.Nickname != "mcp updated" {
		t.Fatalf("ordinary update changed historical fields: limit=%d enabled=%v nickname=%s", saved.DeviceLimit, saved.LegacyProxyEnabled, saved.Nickname)
	}
}
