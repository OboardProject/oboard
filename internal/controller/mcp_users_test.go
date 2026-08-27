package controller

import (
	"context"
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func userAutomationPrincipal(t *testing.T, db *store.Store, userID int64) application.Principal {
	t.Helper()
	user, err := db.GetUser(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
}

func TestOperatorAutomationProtectsAdministratorAccounts(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	operator := &model.User{Username: "operator", PasswordHash: "unused", Role: model.RoleOperator, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	for _, user := range []*model.User{admin, operator} {
		if err := db.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	principal := application.HumanPrincipal(*operator, model.RoleOperator, netip.MustParseAddr("127.0.0.1"))

	if _, _, err := server.userCreateAutomationCandidate(ctx, principal, json.RawMessage(`{"user":{"username":"member","role":"viewer"}}`)); err != nil {
		t.Fatalf("operator could not create an ordinary user: %v", err)
	}
	if _, _, err := server.userCreateAutomationCandidate(ctx, principal, json.RawMessage(`{"user":{"username":"blocked","role":"admin"}}`)); err == nil {
		t.Fatal("operator unexpectedly created an administrator candidate")
	}
	update, _ := json.Marshal(map[string]any{"user_id": admin.ID, "changes": map[string]any{"nickname": "blocked"}})
	if _, _, err := server.userUpdateAutomationCandidate(ctx, principal, update); err == nil {
		t.Fatal("operator unexpectedly updated an administrator candidate")
	}
}

func applyAutomationChangeset(t *testing.T, server *Server, principal application.Principal, idempotencyKey string, operations ...automation.OperationRequest) {
	t.Helper()
	ctx := context.Background()
	draft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: operations})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: idempotencyKey, BaseRevisions: base, Operations: operations})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Apply(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
}

func TestUserCreateUpdateDeleteThroughChangeset(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBootstrapAdmin(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)

	createInput := json.RawMessage(`{"user":{"username":"mcp_user","nickname":"MCP 用户","role":"viewer","speed_limit_mbps":10,"traffic_limit_bytes":10485760,"device_limit":2}}`)
	draft, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "users.create", Input: createInput}}})
	if err != nil {
		t.Fatalf("validate users.create: %v", err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "users-create", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "users.create", Input: createInput}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Apply(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	created, err := db.GetUserByUsername(ctx, "mcp_user")
	if err != nil {
		t.Fatalf("created user not found: %v", err)
	}
	if created.Role != model.RoleViewer || created.SpeedLimitMbps != 10 || created.DeviceLimit != 2 {
		t.Fatalf("unexpected created user: %#v", created)
	}

	updateInput, _ := json.Marshal(map[string]any{"user_id": created.ID, "changes": map[string]any{"role": "operator", "status": "disabled"}})
	draft, err = server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "users.update", Input: updateInput}}})
	if err != nil {
		t.Fatalf("validate users.update: %v", err)
	}
	base, _ = json.Marshal(draft.ExpectedRevisions)
	changeset, err = server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "users-update", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "users.update", Input: updateInput}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Apply(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	updated, err := db.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != model.RoleOperator || updated.Status != "disabled" {
		t.Fatalf("unexpected updated user: %#v", updated)
	}

	deleteInput, _ := json.Marshal(map[string]any{"user_id": created.ID, "confirm": true})
	draft, err = server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "users.delete", Input: deleteInput}}})
	if err != nil {
		t.Fatalf("validate users.delete: %v", err)
	}
	base, _ = json.Marshal(draft.ExpectedRevisions)
	changeset, err = server.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "users-delete", BaseRevisions: base, Operations: []automation.OperationRequest{{Capability: "users.delete", Input: deleteInput}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(ctx, principal, changeset.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Apply(ctx, principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetUser(ctx, created.ID); err == nil {
		t.Fatal("deleted user still exists")
	}
}

func TestUserBootstrapAdminDeleteIsRejected(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	if err := db.SetBootstrapAdmin(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	input, _ := json.Marshal(map[string]any{"user_id": admin.ID, "confirm": true})
	if _, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "users.delete", Input: input}}}); err == nil {
		t.Fatal("bootstrap admin delete passed validation")
	}
}

func TestUserGroupAndMemberCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "member", PasswordHash: "unused", Role: model.RoleViewer, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)

	createInput := json.RawMessage(`{"user_group":{"name":"VIP 组","description":"测试","role":"operator","enabled":true}}`)
	applyAutomationChangeset(t, server, principal, "groups-create", automation.OperationRequest{Capability: "user_groups.create", Input: createInput})
	groups, err := db.ListUserGroups(ctx)
	if err != nil || len(groups) == 0 {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
	var group *model.UserGroup
	for index := range groups {
		if groups[index].Name == "VIP 组" {
			group = &groups[index]
			break
		}
	}
	if group == nil {
		t.Fatalf("group not found: %#v", groups)
	}
	memberInput, _ := json.Marshal(map[string]any{"group_id": group.ID, "user_id": user.ID, "enabled": true})
	applyAutomationChangeset(t, server, principal, "members-set", automation.OperationRequest{Capability: "user_group_members.set", Input: memberInput})
	members, err := db.ListUserGroupMembers(ctx)
	if err != nil || len(members) == 0 {
		t.Fatalf("members=%#v err=%v", members, err)
	}
	if members[0].UserID != user.ID || !members[0].Enabled {
		t.Fatalf("unexpected member: %#v", members[0])
	}
	updateInput, _ := json.Marshal(map[string]any{"group_id": group.ID, "changes": map[string]any{"enabled": false}})
	applyAutomationChangeset(t, server, principal, "groups-update", automation.OperationRequest{Capability: "user_groups.update", Input: updateInput})
	updated, err := db.GetUserGroup(ctx, group.ID)
	if err != nil || updated.Enabled {
		t.Fatalf("group was not disabled: %#v err=%v", updated, err)
	}
	deleteInput, _ := json.Marshal(map[string]any{"group_id": group.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "groups-delete", automation.OperationRequest{Capability: "user_groups.delete", Input: deleteInput})
	if _, err := db.GetUserGroup(ctx, group.ID); err == nil {
		t.Fatal("deleted group still exists")
	}
}

func TestUserDeviceRevokeAndRename(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	device := &model.UserDevice{ID: "dev_test", DeviceIDHash: "hash", UserID: admin.ID, Name: "原名称", TokenHash: "tok", TokenPrefix: "obd_abc", CredentialEpoch: 1}
	if err := db.CreateUserDevice(ctx, device); err != nil {
		t.Fatal(err)
	}
	renameInput, _ := json.Marshal(map[string]any{"user_id": admin.ID, "device_id": device.ID, "name": "新名称"})
	applyAutomationChangeset(t, server, principal, "device-rename", automation.OperationRequest{Capability: "user_devices.update", Input: renameInput})
	renamed, err := db.GetUserDevice(ctx, admin.ID, device.ID)
	if err != nil || renamed.Name != "新名称" {
		t.Fatalf("device not renamed: %#v err=%v", renamed, err)
	}
	revokeInput, _ := json.Marshal(map[string]any{"user_id": admin.ID, "device_id": device.ID, "revoked": true})
	applyAutomationChangeset(t, server, principal, "device-revoke", automation.OperationRequest{Capability: "user_devices.revoke", Input: revokeInput})
	revoked, err := db.GetUserDevice(ctx, admin.ID, device.ID)
	if err != nil || revoked.Status != "revoked" || !revoked.SubscriptionSuspended || revoked.ProxyAccessState != "reject_new" {
		t.Fatalf("device not revoked: %#v err=%v", revoked, err)
	}
}

func TestUserDomainReadCapabilities(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	admin := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	payload, err := server.application.Query(ctx, principal, "users.get", json.RawMessage(`{"id":`+strconv.FormatInt(admin.ID, 10)+`}`))
	if err != nil {
		t.Fatalf("users.get: %v", err)
	}
	encoded, _ := json.Marshal(payload)
	if strings.Contains(string(encoded), "proxy_uuid") || strings.Contains(string(encoded), "subscription_token") || strings.Contains(string(encoded), "proxy_password") {
		t.Fatalf("users.get leaked credentials: %s", encoded)
	}
	if _, err := server.application.Query(ctx, principal, "user_groups.list", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("user_groups.list: %v", err)
	}
	if _, err := server.application.Query(ctx, principal, "user_group_members.list", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("user_group_members.list: %v", err)
	}
	if _, err := server.application.Query(ctx, principal, "user_devices.list", json.RawMessage(`{"user_id":`+strconv.FormatInt(admin.ID, 10)+`}`)); err != nil {
		t.Fatalf("user_devices.list: %v", err)
	}
}
