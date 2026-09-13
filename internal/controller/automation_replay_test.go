package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

func TestAutomationIdempotencyConflictSurfaces(t *testing.T) {
	err := fmt.Errorf("submit: %w", automation.ErrIdempotencyConflict)
	w := httptest.NewRecorder()
	v2HandleError(w, httptest.NewRequest(http.MethodPost, "/api/v1/changesets", nil), err)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"idempotency_conflict"`) {
		t.Fatalf("HTTP conflict: %d %s", w.Code, w.Body.String())
	}
	result := fastPathCodedError(err, true, "retry")
	if result == nil || result.Error == nil || result.Error.Code != mcpauth.CodeIdempotencyConflict || result.Error.Recoverable {
		t.Fatalf("MCP conflict=%+v", result)
	}
}

func TestWorkflowReadUsesCurrentReadGrantAndResourceBoundary(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	app := newTestServer(db, "test-secret", "")
	ctx, principal := mcpGrantContext(context.Background(), model.RoleAdmin, mcpauth.AccessOperate)
	if err := db.CreateAPIPrincipal(ctx, &model.APIPrincipal{ID: principal.ID, Name: "workflow reader", Type: model.APIPrincipalServiceAccount, Enabled: true, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "read-result", Status: model.ServerOffline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	grant := *principal.GrantPolicy
	grant.ResourceBoundary.Resources = map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll, IncludeFuture: true}}
	ctx = context.WithValue(ctx, mcpGrantPrincipalContextKey{}, mcpauth.GrantPrincipal{Grant: grant, Role: principal.Role})
	input, _ := json.Marshal(map[string]any{"server_id": server.ID, "changes": map[string]any{"name": "saved-name"}})
	changeset, err := app.automation.Create(ctx, principal, automation.CreateRequest{IdempotencyKey: "read-result", Operations: []automation.OperationRequest{{Capability: "servers.update", Input: input}}})
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := app.automation.StartWorkflow(ctx, principal, automation.StartWorkflowRequest{IdempotencyKey: "read-result", ChangesetID: changeset.ID})
	if err != nil {
		t.Fatal(err)
	}
	principal.AccessLevel, grant.AccessLevel = mcpauth.AccessRead, mcpauth.AccessRead
	ctx = context.WithValue(ctx, mcpGrantPrincipalContextKey{}, mcpauth.GrantPrincipal{Grant: grant, Role: principal.Role})
	if got, err := app.automation.GetWorkflow(ctx, principal, workflow.ID); err != nil || got.ID != workflow.ID {
		t.Fatalf("read grant could not confirm result: %v %v", got, err)
	}
	if err := app.authorizeAutomationReplay(ctx, principal, changeset.Operations[0]); err == nil {
		t.Fatal("read grant could replay a write")
	}
	grant.ResourceBoundary.Resources = map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionNone}}
	ctx = context.WithValue(ctx, mcpGrantPrincipalContextKey{}, mcpauth.GrantPrincipal{Grant: grant, Role: principal.Role})
	if got, err := app.automation.GetWorkflow(ctx, principal, workflow.ID); got != nil || err == nil {
		t.Fatalf("removed resource remained readable: %v %v", got, err)
	}
	grant.ResourceBoundary.Resources = map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionAll, IncludeFuture: true}}
	revoked := time.Now().UTC()
	grant.RevokedAt = &revoked
	ctx = context.WithValue(ctx, mcpGrantPrincipalContextKey{}, mcpauth.GrantPrincipal{Grant: grant, Role: principal.Role})
	if got, err := app.automation.GetWorkflow(ctx, principal, workflow.ID); got != nil || err == nil {
		t.Fatalf("revoked grant remained readable: %v %v", got, err)
	}
}
