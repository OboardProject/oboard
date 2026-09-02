package controller

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

func TestUserNodeAuthorizationCapabilitiesUseAccessChangeWorkflow(t *testing.T) {
	h, server, token, ids := setupOrderingTestTopology(t)
	user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "mcp-node-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	admin, err := server.store.GetUserByUsername(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	if err := server.store.CreateAPIPrincipal(t.Context(), &model.APIPrincipal{
		ID: principal.ID, OwnerUserID: &admin.ID, Name: principal.Name, Type: principal.Type,
		Enabled: true, Scopes: principal.Scopes, ResourceFilter: json.RawMessage(`{}`),
		RateLimitPerMinute: 60, MaxConcurrency: 2,
	}); err != nil {
		t.Fatal(err)
	}

	setInput, _ := json.Marshal(map[string]any{
		"user_ids": []int64{userID},
		"nodes":    []map[string]any{{"node_type": "proxy_path", "node_id": ids["p1"]}},
		"effect":   "allow",
		"reason":   "MCP 临时授权",
	})
	draft, err := server.automation.ValidateDraft(t.Context(), principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "user_node_authorizations.set", Input: setInput}}})
	if err != nil {
		t.Fatalf("validate authorization set: %v", err)
	}
	base, _ := json.Marshal(draft.ExpectedRevisions)
	changeset, err := server.automation.Create(t.Context(), principal, automation.CreateRequest{
		IdempotencyKey: "mcp-node-authorization-set", BaseRevisions: base,
		Operations: []automation.OperationRequest{{Capability: "user_node_authorizations.set", Input: setInput}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Validate(t.Context(), principal, changeset.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := server.automation.Approve(t.Context(), principal, changeset.ID, "approved in test"); err != nil {
		t.Fatal(err)
	}
	applied, err := server.automation.Apply(t.Context(), principal, changeset.ID)
	if err != nil || applied.Status != model.ChangesetSucceeded {
		t.Fatalf("apply authorization set: status=%v err=%v", applied.Status, err)
	}
	items, err := server.store.ListUserNodeExceptionsForUser(t.Context(), userID)
	if err != nil || len(items) != 1 || items[0].Status != model.UserNodeExceptionPending {
		t.Fatalf("stored authorization = %#v, err=%v", items, err)
	}
	changes, err := server.store.ListAccessChanges(t.Context(), 10)
	if err != nil || len(changes) == 0 || changes[0].ChangeType != model.AccessChangeExceptions {
		t.Fatalf("access changes = %#v, err=%v", changes, err)
	}
	if changes[0].CreatedBy == nil || *changes[0].CreatedBy != admin.ID {
		t.Fatalf("access change actor = %#v", changes[0].CreatedBy)
	}
	workflow, err := server.automation.StartWorkflow(t.Context(), principal, automation.StartWorkflowRequest{Kind: "access_change", IdempotencyKey: "track-node-authorization-set", ChangesetID: changeset.ID})
	if err != nil || workflow.Status != model.WorkflowWaitingForAgent {
		t.Fatalf("authorization workflow = %#v, err=%v", workflow, err)
	}
	driveAccessChange(t, server, token, changes[0].ID)
	workflow, err = server.automation.GetWorkflow(t.Context(), principal, workflow.ID)
	if err != nil || workflow.Status != model.WorkflowSucceeded {
		t.Fatalf("authorization workflow after finalize = %#v, err=%v", workflow, err)
	}

	listed, err := server.queryManagementCapability(t.Context(), principal, "user_node_authorizations.list", json.RawMessage(`{"node_type":"proxy_path","node_id":`+itoa(ids["p1"])+`}`))
	if err != nil {
		t.Fatal(err)
	}
	views := listed.(map[string]any)["authorizations"].([]assignableNodeAuthorizationView)
	if len(views) != 1 || views[0].Username != "mcp-node-user" || !views[0].Effective {
		t.Fatalf("authorization list = %#v", views)
	}

	revokeInput, _ := json.Marshal(map[string]any{
		"authorization_ids": []int64{items[0].ID}, "user_ids": []int64{userID},
		"nodes": []map[string]any{{"node_type": "proxy_path", "node_id": ids["p1"]}}, "confirm": true,
	})
	applyAutomationChangeset(t, server, principal, "mcp-node-authorization-revoke", automation.OperationRequest{Capability: "user_node_authorizations.revoke", Input: revokeInput})
	changes, err = server.store.ListAccessChanges(t.Context(), 10)
	if err != nil || len(changes) == 0 {
		t.Fatalf("revoke access changes = %#v, err=%v", changes, err)
	}
	driveAccessChange(t, server, token, changes[0].ID)
	revoked, err := server.store.GetUserNodeException(t.Context(), items[0].ID)
	if err != nil || revoked.Status != model.UserNodeExceptionRevoked {
		t.Fatalf("revoked authorization = %#v, err=%v", revoked, err)
	}
}

func TestUserNodeAuthorizationFastPathBuildsSetAndRevokeOperations(t *testing.T) {
	h, server, token, ids := setupOrderingTestTopology(t)
	user := request(t, h, http.MethodPost, "/api/v1/ui/users", token, map[string]any{"username": "recipe-user", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	userID := int64(user["id"].(float64))
	admin, err := server.store.GetUserByUsername(t.Context(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*admin, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))

	prepared, err := server.prepareUserNodeAuthorizationRecipe(t.Context(), principal, mcpTaskInput{
		Goal:       "给用户授权东京直连节点",
		TargetRefs: []string{"user:" + itoa(userID), "proxy_path:" + itoa(ids["p1"])},
		Params:     map[string]any{"reason": "试用"},
	})
	if err != nil || prepared.Status != "ready" || prepared.Operations[0].Capability != "user_node_authorizations.set" {
		t.Fatalf("set recipe = %#v, err=%v", prepared, err)
	}
	applyAutomationChangeset(t, server, principal, "recipe-node-authorization-set", automation.OperationRequest{
		Capability: prepared.Operations[0].Capability,
		Input:      mustJSONRaw(prepared.Operations[0].Input),
	})

	prepared, err = server.prepareUserNodeAuthorizationRecipe(t.Context(), principal, mcpTaskInput{
		Goal:       "撤销这个用户的节点授权",
		TargetRefs: []string{"user:" + itoa(userID), "proxy_path:" + itoa(ids["p1"])},
		Params:     map[string]any{"revoke": true},
	})
	if err != nil || prepared.Status != "ready" || prepared.Operations[0].Capability != "user_node_authorizations.revoke" {
		t.Fatalf("revoke recipe = %#v, err=%v", prepared, err)
	}
}

func mustJSONRaw(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
