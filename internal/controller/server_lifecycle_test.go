package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestAgentInstallCommandInlinesBBRLiteral(t *testing.T) {
	enabled := agentInstallCommand("https://panel.example.com", agentInstallBBRValue(true))
	disabled := agentInstallCommand("https://panel.example.com/", agentInstallBBRValue(false))
	for _, command := range []string{enabled, disabled} {
		if strings.Contains(command, "${OBOARD_INSTALL_BBR") {
			t.Fatalf("command still interpolates BBR template: %s", command)
		}
	}
	if !strings.Contains(enabled, "OBOARD_INSTALL_BBR='1'") {
		t.Fatalf("enabled command=%s", enabled)
	}
	if !strings.Contains(disabled, "OBOARD_INSTALL_BBR='0'") {
		t.Fatalf("disabled command=%s", disabled)
	}
}

func TestRESTRejectsDuplicateServerName(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	h := newTestServer(db, "test-secret", "").Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "SJC"}, http.StatusCreated)
	serverID := int64(created["server"].(map[string]any)["id"].(float64))
	conflict := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "sjc"}, http.StatusConflict)
	encoded, _ := json.Marshal(conflict)
	if !strings.Contains(string(encoded), "servers.enrollment.issue") {
		t.Fatalf("duplicate create=%#v", conflict)
	}

	other := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{"name": "LAX"}, http.StatusCreated)
	otherID := int64(other["server"].(map[string]any)["id"].(float64))
	renameConflict := request(t, h, http.MethodPatch, "/api/v1/ui/servers/"+itoa(otherID), token, map[string]any{"name": "SJC"}, http.StatusConflict)
	encodedRename, _ := json.Marshal(renameConflict)
	if !strings.Contains(string(encodedRename), "已存在") {
		t.Fatalf("rename conflict=%#v", renameConflict)
	}
	request(t, h, http.MethodPatch, "/api/v1/ui/servers/"+itoa(serverID), token, map[string]any{"name": "SJC", "offline_notify_enabled": false}, http.StatusOK)
}

func TestServerOnboardUniquenessEnrollmentIssueAndDelete(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))
	onboard, _ := json.Marshal(map[string]any{"server": map[string]any{"name": "SJC"}, "issue_enrollment_token": false})
	applyAutomationChangeset(t, server, principal, "onboard-sjc", automation.OperationRequest{Capability: "servers.onboard", Input: onboard})
	items, err := db.ListServers(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("servers=%#v err=%v", items, err)
	}
	existing := items[0]
	if _, err := server.automation.ValidateDraft(ctx, principal, automation.DraftValidationRequest{Operations: []automation.OperationRequest{{Capability: "servers.onboard", Input: onboard}}}); err == nil || !strings.Contains(err.Error(), "servers.enrollment.issue") {
		t.Fatalf("duplicate onboard error=%v", err)
	}

	issue, _ := json.Marshal(map[string]any{"server_id": existing.ID})
	applied := applyAutomationChangesetResult(t, server, principal, "reissue-sjc", automation.OperationRequest{Capability: "servers.enrollment.issue", Input: issue})
	if !strings.Contains(string(applied.Result), `"enrollment_token"`) {
		t.Fatalf("enrollment issue omitted token: %s", applied.Result)
	}
	persisted, err := server.automation.Get(ctx, applied.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mustJSON(t, persisted), `"enrollment_token":`) {
		t.Fatalf("persisted changeset retained token: %s", persisted.Result)
	}

	deleteInput, _ := json.Marshal(map[string]any{"server_id": existing.ID, "confirm": true})
	applyAutomationChangeset(t, server, principal, "delete-sjc", automation.OperationRequest{Capability: "servers.delete", Input: deleteInput})
	if _, err := db.GetServer(ctx, existing.ID); err == nil {
		t.Fatal("deleted server still exists")
	}
}

func TestServerOnboardingPlanEmptyNameAndReissue(t *testing.T) {
	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	ctx := context.Background()
	user := &model.User{Username: "admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	principal := application.HumanPrincipal(*user, model.RoleAdmin, netip.MustParseAddr("127.0.0.1"))

	empty, err := server.application.PlanServerOnboarding(ctx, principal, json.RawMessage(`{}`))
	if err != nil || empty.Valid || len(empty.Warnings) == 0 || !strings.Contains(empty.Warnings[0], `"name"`) {
		t.Fatalf("empty plan=%#v err=%v", empty, err)
	}

	created := &model.Server{Name: "SJC", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerUnknown}
	if err := db.CreateServer(ctx, created); err != nil {
		t.Fatal(err)
	}
	existing, err := server.application.PlanServerOnboarding(ctx, principal, json.RawMessage(`{"name":"SJC"}`))
	if err != nil || !existing.Valid || len(existing.Candidates) != 1 {
		t.Fatalf("existing plan=%#v err=%v", existing, err)
	}
	if existing.Candidates[0]["action"] != "reissue_enrollment" {
		t.Fatalf("existing candidates=%#v", existing.Candidates)
	}
	rawSuggested, _ := json.Marshal(existing.SuggestedChangeset)
	if !strings.Contains(string(rawSuggested), `"servers.enrollment.issue"`) {
		t.Fatalf("suggested=%s", rawSuggested)
	}

	unique, err := server.application.PlanServerOnboarding(ctx, principal, json.RawMessage(`{"name":"NRT","region_code":"JP"}`))
	if err != nil || !unique.Valid {
		t.Fatalf("unique plan=%#v err=%v", unique, err)
	}
	serverInput, _ := unique.Candidates[0]["server"].(map[string]any)
	if int(toFloat64(serverInput["port_range_end"])) != core.DefaultPublicPortRangeEnd || int(toFloat64(serverInput["internal_port_range_end"])) != core.DefaultInternalPortRangeEnd {
		t.Fatalf("unique ports=%#v", serverInput)
	}
}

func TestFastPathDuplicateNameAsksToReissueEnrollment(t *testing.T) {
	db, _, session, _, closeServer := newMCPTestEnvironment(t, "operate", []string{"oboard:read", "oboard:operate"})
	defer closeServer()
	ctx := context.Background()
	existing := model.Server{Name: "SJC", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerUnknown}
	if err := db.CreateServer(ctx, &existing); err != nil {
		t.Fatal(err)
	}

	needs := fastPathCall(t, session, "oboard_task", map[string]any{"intent": "server.onboard", "goal": "新增 SJC 服务器", "params": map[string]any{"name": "SJC"}})
	if needs["status"] != "needs_input" {
		t.Fatalf("duplicate onboard=%#v", needs)
	}
	questions, _ := needs["data"].(map[string]any)["questions"].([]any)
	if len(questions) == 0 {
		t.Fatalf("questions=%#v", needs)
	}
	questionJSON, _ := json.Marshal(questions[0])
	if !strings.Contains(string(questionJSON), "confirm_reissue") {
		t.Fatalf("questions=%#v", needs)
	}
	continuationID := needs["data"].(map[string]any)["continuation_id"].(string)
	ready := fastPathCall(t, session, "oboard_task", map[string]any{"continuation_id": continuationID, "params": map[string]any{"confirm_reissue": true}})
	if ready["status"] != "ready" {
		t.Fatalf("reissue ready=%#v", ready)
	}
	summary, _ := ready["data"].(map[string]any)["summary"].(map[string]any)
	if summary["action"] != "reissue_enrollment" {
		t.Fatalf("summary=%#v", summary)
	}
}

func toFloat64(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case int:
		return float64(typed)
	default:
		return 0
	}
}
