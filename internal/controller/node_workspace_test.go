package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func openNodeWorkspaceTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNodeWorkspaceOwnershipImportOutputAndShare(t *testing.T) {
	db := openNodeWorkspaceTestStore(t)
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminToken := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	first := request(t, handler, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "first", "password": "first-user-password", "role": "none", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	second := request(t, handler, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "second", "password": "second-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	firstID := int64(first["id"].(float64))
	secondID := int64(second["id"].(float64))
	firstToken := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "first", "password": "first-user-password"}, http.StatusOK)["token"].(string)

	workspace := request(t, handler, http.MethodGet, "/api/v1/ui/node-workspace", firstToken, nil, http.StatusOK)
	groups := workspace["node_groups"].([]any)
	outputs := workspace["subscription_outputs"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["kind"] != "oboard" || len(outputs) != 1 || outputs[0].(map[string]any)["is_default"] != true {
		t.Fatalf("unexpected defaults: groups=%#v outputs=%#v", groups, outputs)
	}
	request(t, handler, http.MethodGet, "/api/v1/ui/node-workspace?user_id="+itoa(secondID), firstToken, nil, http.StatusForbidden)
	managed := request(t, handler, http.MethodGet, "/api/v1/ui/node-workspace?user_id="+itoa(firstID), adminToken, nil, http.StatusOK)
	if managed["subject"].(map[string]any)["username"] != "first" {
		t.Fatalf("admin perspective subject = %#v", managed["subject"])
	}

	created := request(t, handler, http.MethodPost, "/api/v1/ui/node-groups", firstToken, map[string]any{
		"name": "机场 A", "kind": "manual", "content": "ss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Manual-One\ntrojan://secret@8.8.8.8:443?sni=example.com#Trojan-One",
	}, http.StatusCreated)
	encodedCreated, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedCreated), "secret") || strings.Contains(string(encodedCreated), "pass\"") || strings.Contains(string(encodedCreated), "\"raw\"") {
		t.Fatalf("manual import response leaked node configuration: %s", encodedCreated)
	}
	if !strings.Contains(string(encodedCreated), "fingerprint") {
		t.Fatalf("manual import response omitted sanitized node summaries: %s", encodedCreated)
	}
	groupID := int64(created["node_group"].(map[string]any)["id"].(float64))
	library := request(t, handler, http.MethodGet, "/api/v1/ui/node-library", firstToken, nil, http.StatusOK)["nodes"].([]any)
	if len(library) != 2 {
		t.Fatalf("private node library count = %d, want 2", len(library))
	}
	nodeID := library[0].(map[string]any)["id"].(string)
	if !library[0].(map[string]any)["editable"].(bool) {
		t.Fatal("manual node should be editable")
	}
	shared := request(t, handler, http.MethodPost, "/api/v1/ui/node-library/share", firstToken, map[string]any{"node_id": nodeID}, http.StatusOK)
	if shared["url"] == "" {
		t.Fatal("node share URL is empty")
	}
	renamed := request(t, handler, http.MethodPatch, "/api/v1/ui/node-library/"+nodeID, firstToken, map[string]any{"name": "Renamed-One"}, http.StatusOK)["node"].(map[string]any)
	if renamed["name"] != "Renamed-One" || renamed["enabled"] != true {
		t.Fatalf("renamed node = %#v", renamed)
	}
	replaced := request(t, handler, http.MethodPatch, "/api/v1/ui/node-library/"+nodeID, firstToken, map[string]any{"content": "ss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Replaced-One"}, http.StatusOK)["node"].(map[string]any)
	if replaced["name"] != "Replaced-One" || replaced["protocol"] != "shadowsocks" {
		t.Fatalf("replaced node = %#v", replaced)
	}
	request(t, handler, http.MethodPatch, "/api/v1/ui/node-library/"+nodeID, firstToken, map[string]any{"content": "trojan://bad@1.1.1.1:443#Bad\nss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Extra"}, http.StatusBadRequest)

	output := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-outputs", firstToken, map[string]any{"name": "私人组合", "group_ids": []int64{groupID}}, http.StatusCreated)["subscription_output"].(map[string]any)
	outputID := int64(output["id"].(float64))
	preview := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-outputs/"+itoa(outputID)+"/preview", firstToken, map[string]any{"format": "sing-box"}, http.StatusOK)["preview"].(map[string]any)
	if len(preview["nodes"].([]any)) != 2 || preview["content"] == "" {
		t.Fatalf("unexpected output preview: %#v", preview)
	}
	if _, leaked := preview["nodes"].([]any)[0].(map[string]any)["raw"]; leaked {
		t.Fatalf("output preview leaked raw node config: %#v", preview["nodes"])
	}
	duplicate := request(t, handler, http.MethodPost, "/api/v1/ui/node-groups", firstToken, map[string]any{
		"name": "重复来源", "kind": "manual", "content": "ss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Duplicate",
	}, http.StatusCreated)
	duplicateID := int64(duplicate["node_group"].(map[string]any)["id"].(float64))
	request(t, handler, http.MethodPatch, "/api/v1/ui/subscription-outputs/"+itoa(outputID), firstToken, map[string]any{"name": "私人组合", "group_ids": []int64{groupID, duplicateID}}, http.StatusOK)
	previewResponse := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-outputs/"+itoa(outputID)+"/preview", firstToken, map[string]any{"format": "sing-box"}, http.StatusOK)
	if previewResponse["deduplicated_count"] != float64(1) {
		t.Fatalf("deduplicated_count = %#v, want 1", previewResponse["deduplicated_count"])
	}

	request(t, handler, http.MethodPost, "/api/v1/ui/node-groups", firstToken, map[string]any{"name": "blocked", "kind": "remote", "url": "https://127.0.0.1/sub"}, http.StatusBadRequest)
}

func TestNodeWorkspaceAutomationPreservesEnabledAndEnforcesPerspective(t *testing.T) {
	db := openNodeWorkspaceTestStore(t)
	server := newTestServer(db, "test-secret", "")
	first := &model.User{Username: "first", PasswordHash: "x", Status: "active", Role: model.RoleNone}
	second := &model.User{Username: "second", PasswordHash: "x", Status: "active", Role: model.RoleViewer}
	if err := db.CreateUser(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	output, err := db.GetDefaultSubscriptionOutput(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	output.Enabled = false
	if err := db.SaveSubscriptionOutput(t.Context(), output); err != nil {
		t.Fatal(err)
	}
	principal := application.Principal{ID: "first", UserID: &first.ID, Role: model.RoleNone}
	input, _ := json.Marshal(map[string]any{"user_id": first.ID, "output_id": output.ID, "name": "仍然关闭", "group_ids": output.GroupIDs})
	if _, err := server.applyNodeWorkspaceOperation(t.Context(), principal, "subscription_outputs.save", input); err != nil {
		t.Fatal(err)
	}
	updated, err := db.GetSubscriptionOutput(t.Context(), first.ID, output.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Fatal("omitting enabled unexpectedly enabled the subscription output")
	}
	enabled := true
	input, _ = json.Marshal(map[string]any{"user_id": first.ID, "output_id": output.ID, "name": "已开启", "group_ids": output.GroupIDs, "enabled": enabled})
	if _, err := server.applyNodeWorkspaceOperation(t.Context(), principal, "subscription_outputs.save", input); err != nil {
		t.Fatal(err)
	}
	previewInput, _ := json.Marshal(map[string]any{"user_id": first.ID, "output_id": output.ID, "format": "sing-box"})
	mcpPreview, err := server.queryManagementCapability(t.Context(), principal, "subscription_outputs.preview", previewInput)
	if err != nil {
		t.Fatal(err)
	}
	encodedPreview, _ := json.Marshal(mcpPreview)
	if strings.Contains(string(encodedPreview), "\"content\"") || strings.Contains(string(encodedPreview), "\"raw\"") {
		t.Fatalf("MCP preview leaked subscription material: %s", encodedPreview)
	}
	manual := &model.NodeGroup{UserID: first.ID, Kind: model.NodeGroupManual, Name: "MCP 手动"}
	if err := db.CreateNodeGroup(t.Context(), manual); err != nil {
		t.Fatal(err)
	}
	if err := db.AddManualImportedNodes(t.Context(), first.ID, manual.ID, []model.ImportedNode{{UserID: first.ID, GroupID: manual.ID, Protocol: model.PrivateProtocolTrojan, Name: "Original", Fingerprint: "mcp-fp", ConfigEncrypted: "enc"}}); err != nil {
		t.Fatal(err)
	}
	imported, err := db.ListImportedNodes(t.Context(), first.ID)
	if err != nil || len(imported) != 1 {
		t.Fatalf("imported=%#v err=%v", imported, err)
	}
	editInput, _ := json.Marshal(map[string]any{"user_id": first.ID, "node_id": "private:" + itoa(imported[0].ID), "name": "MCP Renamed", "enabled": false})
	if _, err := server.validateNodeWorkspaceOperation(t.Context(), principal, "node_library.update", editInput); err != nil {
		t.Fatal(err)
	}
	if _, err := server.applyNodeWorkspaceOperation(t.Context(), principal, "node_library.update", editInput); err != nil {
		t.Fatal(err)
	}
	edited, err := db.GetImportedNode(t.Context(), first.ID, imported[0].ID)
	if err != nil || edited.Name != "MCP Renamed" || edited.Enabled {
		t.Fatalf("edited node=%#v err=%v", edited, err)
	}
	badEdit, _ := json.Marshal(map[string]any{"user_id": first.ID, "node_id": "private:" + itoa(imported[0].ID), "content": "trojan://bad@1.1.1.1:443#Bad\nss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Extra"})
	if _, err := server.validateNodeWorkspaceOperation(t.Context(), principal, "node_library.update", badEdit); err == nil {
		t.Fatal("automation accepted multi-node edit content")
	}
	operator := application.Principal{ID: "operator", UserID: &first.ID, Role: model.RoleOperator}
	otherInput, _ := json.Marshal(map[string]any{"user_id": second.ID})
	if _, err := server.queryManagementCapability(t.Context(), operator, "node_groups.list", otherInput); err == nil {
		t.Fatal("operator unexpectedly accessed another user's node groups")
	}
	admin := application.Principal{ID: "admin", Role: model.RoleAdmin}
	if _, err := server.queryManagementCapability(t.Context(), admin, "node_groups.list", otherInput); err != nil {
		t.Fatalf("administrator user perspective failed: %v", err)
	}
}

func TestNodeWorkspaceMCPResourcesUseUserScopedTemplates(t *testing.T) {
	server := newTestServer(openNodeWorkspaceTestStore(t), "test-secret", "")
	want := map[string]string{
		"oboard://users/{id}/node-library":         "node_library.list",
		"oboard://users/{id}/node-groups":          "node_groups.list",
		"oboard://users/{id}/node-sources":         "node_sources.list",
		"oboard://users/{id}/subscription-outputs": "subscription_outputs.list",
	}
	for _, resource := range server.mcpResourceTemplateDefs() {
		if capability, ok := want[resource.uri]; ok {
			if resource.capability != capability || resource.kind != "query_node_workspace" {
				t.Fatalf("resource %s = %#v", resource.uri, resource)
			}
			delete(want, resource.uri)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing node workspace MCP resources: %#v", want)
	}
}

func TestNodeWorkspaceInvalidProfileIsRejected(t *testing.T) {
	db := openNodeWorkspaceTestStore(t)
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	created := request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := created["user"].(map[string]any)["subscription_token"].(string)
	request(t, handler, http.MethodGet, "/api/v1/subscriptions/"+token+"?format=sing-box&profile_id=999999", "", nil, http.StatusNotFound)
}

func TestNodeWorkspaceProfileParticipatesInSubscriptionETag(t *testing.T) {
	db := openNodeWorkspaceTestStore(t)
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	created := request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	user := created["user"].(map[string]any)
	subscriptionToken := user["subscription_token"].(string)
	adminToken := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	workspace := request(t, handler, http.MethodGet, "/api/v1/ui/node-workspace", adminToken, nil, http.StatusOK)
	defaultOutputID := int64(workspace["subscription_outputs"].([]any)[0].(map[string]any)["id"].(float64))
	groupID := int64(workspace["node_groups"].([]any)[0].(map[string]any)["id"].(float64))
	second := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-outputs", adminToken, map[string]any{"name": "同内容组合", "group_ids": []int64{groupID}}, http.StatusCreated)["subscription_output"].(map[string]any)
	secondOutputID := int64(second["id"].(float64))
	fetchETag := func(query string) string {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+subscriptionToken+"?format=sing-box"+query, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("subscription %q status=%d body=%s", query, recorder.Code, recorder.Body.String())
		}
		return recorder.Header().Get("ETag")
	}
	defaultETag := fetchETag("")
	if explicit := fetchETag("&profile_id=" + itoa(defaultOutputID)); explicit != defaultETag {
		t.Fatalf("implicit and explicit default ETags differ: %q != %q", defaultETag, explicit)
	}
	if other := fetchETag("&profile_id=" + itoa(secondOutputID)); other == defaultETag {
		t.Fatalf("different profiles with identical content reused ETag %q", other)
	}
}

func TestSubscriptionOutputFilterRESTValidationAndPreviewStats(t *testing.T) {
	db := openNodeWorkspaceTestStore(t)
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	workspace := request(t, handler, http.MethodGet, "/api/v1/ui/node-workspace", token, nil, http.StatusOK)
	oboardGroupID := int64(workspace["node_groups"].([]any)[0].(map[string]any)["id"].(float64))

	created := request(t, handler, http.MethodPost, "/api/v1/ui/node-groups", token, map[string]any{
		"name": "机场 B", "kind": "manual", "content": "ss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Filter-Keep\ntrojan://secret@8.8.8.8:443?sni=example.com#Filter-Drop",
	}, http.StatusCreated)
	manualGroupID := int64(created["node_group"].(map[string]any)["id"].(float64))

	output := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-outputs", token, map[string]any{
		"name": "过滤组合", "group_ids": []int64{manualGroupID}, "filters": []map[string]any{
			{"type": "drop_name", "value": "Drop$"},
			{"type": "keep_protocol", "value": "shadowsocks"},
		},
	}, http.StatusCreated)["subscription_output"].(map[string]any)
	outputID := int64(output["id"].(float64))
	savedFilters := output["filters"].([]any)
	if len(savedFilters) != 2 || savedFilters[0].(map[string]any)["type"] != "drop_name" {
		t.Fatalf("created output filters = %#v", savedFilters)
	}

	preview := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-outputs/"+itoa(outputID)+"/preview", token, map[string]any{"format": "sing-box"}, http.StatusOK)
	nodes := preview["preview"].(map[string]any)["nodes"].([]any)
	if len(nodes) != 1 || nodes[0].(map[string]any)["name"] != "Filter-Keep" {
		t.Fatalf("filtered preview nodes = %#v", nodes)
	}
	stats := preview["preview"].(map[string]any)["filter_stats"].([]any)
	if len(stats) != 2 || stats[0].(map[string]any)["dropped"] != float64(1) || stats[1].(map[string]any)["dropped"] != float64(0) {
		t.Fatalf("filter_stats = %#v", stats)
	}
	if preview["preview"].(map[string]any)["filter_dropped"] != float64(1) {
		t.Fatalf("filter_dropped = %#v", preview["preview"].(map[string]any)["filter_dropped"])
	}

	// PATCH with filters replaces the pipeline; omitting filters keeps it.
	patched := request(t, handler, http.MethodPatch, "/api/v1/ui/subscription-outputs/"+itoa(outputID), token, map[string]any{"name": "过滤组合", "group_ids": []int64{manualGroupID}}, http.StatusOK)["subscription_output"].(map[string]any)
	if len(patched["filters"].([]any)) != 2 {
		t.Fatalf("omitting filters cleared the pipeline: %#v", patched["filters"])
	}
	cleared := request(t, handler, http.MethodPatch, "/api/v1/ui/subscription-outputs/"+itoa(outputID), token, map[string]any{"name": "不过滤", "group_ids": []int64{manualGroupID}, "filters": []map[string]any{}}, http.StatusOK)["subscription_output"].(map[string]any)
	if len(cleared["filters"].([]any)) != 0 {
		t.Fatalf("explicit empty filters not applied: %#v", cleared["filters"])
	}

	// Invalid rules are rejected with 400 and never saved.
	invalidBodies := []map[string]any{
		{"name": "x", "group_ids": []int64{manualGroupID}, "filters": []map[string]any{{"type": "keep_name", "value": "(unclosed"}}},
		{"name": "x", "group_ids": []int64{manualGroupID}, "filters": []map[string]any{{"type": "keep_protocol", "value": "wireguard"}}},
		{"name": "x", "group_ids": []int64{manualGroupID}, "filters": []map[string]any{{"type": "keep_region", "value": "JPX"}}},
		{"name": "x", "group_ids": []int64{manualGroupID}, "filters": []map[string]any{{"type": "keep_all", "value": "a"}}},
		{"name": "x", "group_ids": []int64{manualGroupID}, "filters": []map[string]any{{"type": "drop_group", "value": "999999"}}},
		{"name": "x", "group_ids": []int64{oboardGroupID}, "filters": []map[string]any{{"type": "drop_name", "value": ""}}},
	}
	for index, body := range invalidBodies {
		request(t, handler, http.MethodPatch, "/api/v1/ui/subscription-outputs/"+itoa(outputID), token, body, http.StatusBadRequest)
		_ = index
	}
	// A group rule referencing one of the user's own groups stays valid.
	request(t, handler, http.MethodPatch, "/api/v1/ui/subscription-outputs/"+itoa(outputID), token, map[string]any{"name": "x", "group_ids": []int64{manualGroupID}, "filters": []map[string]any{{"type": "drop_group", "value": itoa(oboardGroupID)}}}, http.StatusOK)
}

func TestSubscriptionOutputFiltersApplyToSubscriptionPull(t *testing.T) {
	db := openNodeWorkspaceTestStore(t)
	server := newTestServer(db, "test-secret", "")
	handler := server.Handler()
	created := request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	subscriptionToken := created["user"].(map[string]any)["subscription_token"].(string)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)
	request(t, handler, http.MethodPost, "/api/v1/ui/node-groups", token, map[string]any{
		"name": "机场 C", "kind": "manual", "content": "ss://YWVzLTEyOC1nY206cGFzcw@1.1.1.1:443#Keep-Me\ntrojan://secret@8.8.8.8:443?sni=example.com#Drop-Me",
	}, http.StatusCreated)

	output := request(t, handler, http.MethodPost, "/api/v1/ui/subscription-outputs", token, map[string]any{
		"name": "下拉过滤", "group_ids": []int64{int64(request(t, handler, http.MethodGet, "/api/v1/ui/node-workspace", token, nil, http.StatusOK)["node_groups"].([]any)[1].(map[string]any)["id"].(float64))},
		"filters": []map[string]any{{"type": "drop_name", "value": "Drop-Me"}},
	}, http.StatusCreated)["subscription_output"].(map[string]any)
	outputID := int64(output["id"].(float64))

	fetch := func() string {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/subscriptions/"+subscriptionToken+"?format=sing-box&profile_id="+itoa(outputID), nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("subscription status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}
	filteredBody := fetch()
	if !strings.Contains(filteredBody, "Keep-Me") || strings.Contains(filteredBody, "Drop-Me") {
		t.Fatalf("filtered subscription body = %s", filteredBody)
	}
	request(t, handler, http.MethodPatch, "/api/v1/ui/subscription-outputs/"+itoa(outputID), token, map[string]any{"name": "下拉过滤", "group_ids": []int64{int64(output["group_ids"].([]any)[0].(float64))}, "filters": []map[string]any{}}, http.StatusOK)
	unfilteredBody := fetch()
	if !strings.Contains(unfilteredBody, "Drop-Me") {
		t.Fatalf("cleared filters did not restore nodes: %s", unfilteredBody)
	}
}

func TestSubscriptionOutputFilterAutomationSave(t *testing.T) {
	db := openNodeWorkspaceTestStore(t)
	server := newTestServer(db, "test-secret", "")
	first := &model.User{Username: "automation-filter", PasswordHash: "x", Status: "active", Role: model.RoleNone}
	if err := db.CreateUser(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	output, err := db.GetDefaultSubscriptionOutput(t.Context(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	principal := application.Principal{ID: "first", UserID: &first.ID, Role: model.RoleNone}
	groups, err := db.ListNodeGroups(t.Context(), first.ID)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%#v err=%v", groups, err)
	}
	validInput, _ := json.Marshal(map[string]any{
		"user_id": first.ID, "output_id": output.ID, "name": "过滤", "group_ids": output.GroupIDs,
		"filters": []map[string]any{{"type": "keep_region", "value": "JP"}, {"type": "keep_group", "value": itoa(groups[0].ID)}},
	})
	if _, err := server.validateNodeWorkspaceOperation(t.Context(), principal, "subscription_outputs.save", validInput); err != nil {
		t.Fatal(err)
	}
	if _, err := server.applyNodeWorkspaceOperation(t.Context(), principal, "subscription_outputs.save", validInput); err != nil {
		t.Fatal(err)
	}
	updated, err := db.GetSubscriptionOutput(t.Context(), first.ID, output.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Filters) != 2 || updated.Filters[0].Value != "JP" || updated.Filters[1].Value != itoa(groups[0].ID) {
		t.Fatalf("automation saved filters = %#v", updated.Filters)
	}
	invalidInput, _ := json.Marshal(map[string]any{
		"user_id": first.ID, "output_id": output.ID, "name": "过滤", "group_ids": output.GroupIDs,
		"filters": []map[string]any{{"type": "keep_name", "value": "["}},
	})
	if _, err := server.validateNodeWorkspaceOperation(t.Context(), principal, "subscription_outputs.save", invalidInput); err == nil {
		t.Fatal("automation accepted an invalid regex filter")
	}
	foreignGroupInput, _ := json.Marshal(map[string]any{
		"user_id": first.ID, "output_id": output.ID, "name": "过滤", "group_ids": output.GroupIDs,
		"filters": []map[string]any{{"type": "drop_group", "value": "12345"}},
	})
	if _, err := server.validateNodeWorkspaceOperation(t.Context(), principal, "subscription_outputs.save", foreignGroupInput); err == nil {
		t.Fatal("automation accepted a filter referencing a foreign node group")
	}
}
