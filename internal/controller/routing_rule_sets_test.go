package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/ruleset"
	"github.com/OboardProject/oboard/internal/store"
)

func testSourceRuleSet(url string) model.RoutingRuleSet {
	return model.RoutingRuleSet{Name: "test", URL: url, Format: model.RoutingRuleSetFormatSingBoxSource}
}

func TestFetchRoutingRuleSetConditionalRequestAndNotModified(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != `"revision-1"` || r.Header.Get("If-Modified-Since") == "" {
			t.Errorf("conditional headers: %#v", r.Header)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()
	item := testSourceRuleSet(server.URL)
	item.Revision = "revision-1"
	item.ETag = `"revision-1"`
	item.LastModified = time.Now().UTC().Format(http.TimeFormat)
	result, err := fetchRoutingRuleSetHTTP(t.Context(), item, true, server.Client())
	if err != nil || !result.notModified || result.revision != item.Revision {
		t.Fatalf("conditional result=%#v error=%v", result, err)
	}
}

func TestFetchRoutingRuleSetRejectsLimitTimeoutAndCrossOriginRedirect(t *testing.T) {
	t.Run("limit", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", ruleset.MaxContentSize+1)))
		}))
		defer server.Close()
		if _, err := fetchRoutingRuleSetHTTP(t.Context(), testSourceRuleSet(server.URL), false, server.Client()); err == nil || !strings.Contains(err.Error(), "8 MiB") {
			t.Fatalf("content limit error=%v", err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write([]byte(`{"version":1,"rules":[{"domain":["example.com"]}]}`))
		}))
		defer server.Close()
		client := server.Client()
		client.Timeout = 10 * time.Millisecond
		if _, err := fetchRoutingRuleSetHTTP(t.Context(), testSourceRuleSet(server.URL), false, client); err == nil || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("timeout error=%v", err)
		}
	})

	t.Run("cross origin redirect", func(t *testing.T) {
		target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"version":1,"rules":[{"domain":["example.com"]}]}`))
		}))
		defer target.Close()
		source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		defer source.Close()
		client := source.Client()
		client.CheckRedirect = routingRuleSetRedirectPolicy
		if _, err := fetchRoutingRuleSetHTTP(t.Context(), testSourceRuleSet(source.URL), false, client); err == nil || !strings.Contains(err.Error(), "crossed origins") {
			t.Fatalf("redirect error=%v", err)
		}
	})
}

func TestRefreshRoutingRuleSetFailureRetainsLastSuccessfulSnapshot(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Add(-time.Hour)
	item := model.RoutingRuleSet{Name: "stable", URL: "https://rules.example/stable.json", Format: model.RoutingRuleSetFormatSingBoxSource, Content: []byte(`{"version":1,"rules":[{"domain":["example.com"]}]}`), Revision: "stable-revision", Status: model.RoutingRuleSetStatusReady, LastAttemptAt: &now, LastSuccessAt: &now}
	if err := db.CreateRoutingRuleSet(t.Context(), &item); err != nil {
		t.Fatal(err)
	}
	server := &Server{store: db, routingRuleSetFetcher: func(context.Context, model.RoutingRuleSet, bool) (*fetchedRoutingRuleSet, error) {
		return nil, errors.New("upstream unavailable")
	}}
	refreshed, changed, err := server.refreshRoutingRuleSet(t.Context(), item.ID)
	if err == nil || changed || refreshed.Revision != item.Revision || string(refreshed.Content) != string(item.Content) || refreshed.Status != model.RoutingRuleSetStatusError || refreshed.LastSuccessAt == nil {
		t.Fatalf("refresh result=%#v changed=%v error=%v", refreshed, changed, err)
	}
	persisted, err := db.GetRoutingRuleSet(t.Context(), item.ID)
	if err != nil || persisted.Revision != item.Revision || string(persisted.Content) != string(item.Content) {
		t.Fatalf("persisted snapshot=%#v error=%v", persisted, err)
	}
}

func TestResolvePublicMetadataHostRejectsRuleSetPrivateAndMetadataAddresses(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "::1", "fd00:ec2::254"} {
		if _, err := resolvePublicMetadataHost(t.Context(), host); err == nil {
			t.Fatalf("private or metadata host %q was accepted", host)
		}
	}
}

func TestRoutingRuleSetRESTLifecycleAndCrossStagePlacement(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	server := newTestServer(db, "test-secret", "")
	revision := "revision-1"
	server.routingRuleSetFetcher = func(context.Context, model.RoutingRuleSet, bool) (*fetchedRoutingRuleSet, error) {
		return &fetchedRoutingRuleSet{content: []byte(`{"version":1,"rules":[{"domain":["example.com"]}]}`), revision: revision, etag: `"` + revision + `"`}, nil
	}
	handler := server.Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	created := request(t, handler, http.MethodPost, "/api/v2/ui/routing-rule-sets", token, map[string]any{"name": "shared", "url": "https://rules.example/shared.json", "format": model.RoutingRuleSetFormatSingBoxSource}, http.StatusCreated)
	ruleSetID := int64(created["routing_rule_set"].(map[string]any)["id"].(float64))
	listed := request(t, handler, http.MethodGet, "/api/v2/ui/routing-rule-sets", token, nil, http.StatusOK)["routing_rule_sets"].([]any)
	if len(listed) != 1 {
		t.Fatalf("rule set list = %#v", listed)
	}
	request(t, handler, http.MethodPatch, "/api/v2/ui/routing-rule-sets/"+itoa(ruleSetID), token, map[string]any{"name": "shared-renamed", "url": "https://rules.example/shared.json", "format": model.RoutingRuleSetFormatSingBoxSource}, http.StatusOK)
	revision = "revision-2"
	refreshed := request(t, handler, http.MethodPost, "/api/v2/ui/routing-rule-sets/"+itoa(ruleSetID)+"/refresh", token, map[string]any{}, http.StatusOK)
	if changed, _ := refreshed["changed"].(bool); !changed {
		t.Fatalf("refresh did not report changed content: %#v", refreshed)
	}
	request(t, handler, http.MethodDelete, "/api/v2/ui/routing-rule-sets/"+itoa(ruleSetID), token, nil, http.StatusOK)

	serverA := &model.Server{Name: "A", PublicIPv4: "1.1.1.1", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30100, Status: model.ServerOnline}
	serverB := &model.Server{Name: "B", PublicIPv4: "8.8.8.8", ListenIP: "0.0.0.0", PortRangeStart: 31000, PortRangeEnd: 31100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, serverB); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	serverBID := serverB.ID
	step := &model.ProxyPathStep{PathID: path.ID, Position: 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &serverBID, ConfigJSON: `{}`}
	if err := db.CreateProxyPathStep(ctx, step); err != nil {
		t.Fatal(err)
	}
	createRule := func(name string, stageStepID any, action model.RouteAction) int64 {
		t.Helper()
		body := map[string]any{"scope": model.RoutingRuleScopePathStage, "proxy_path_id": path.ID, "sort_position": 0, "match_source": model.RoutingMatchSourceInline, "name": name, "match_json": `{"domain":["` + name + `.example"]}`, "action": action, "enabled": true}
		if stageStepID != nil {
			body["stage_step_id"] = stageStepID
		}
		response := request(t, handler, http.MethodPost, "/api/v2/ui/routing-rules", token, body, http.StatusCreated)
		return int64(response["routing_rule"].(map[string]any)["id"].(float64))
	}
	ruleAID := createRule("at-a", nil, model.RouteActionDirect)
	ruleBID := createRule("at-b", step.ID, model.RouteActionBlock)
	request(t, handler, http.MethodPost, "/api/v2/ui/routing-rules/place", token, map[string]any{"proxy_path_id": path.ID, "placements": []map[string]any{{"rule_id": ruleBID, "sort_position": 0}, {"rule_id": ruleAID, "stage_step_id": step.ID, "sort_position": 0}}}, http.StatusOK)
	movedA, err := db.GetRoutingRule(ctx, ruleAID)
	if err != nil {
		t.Fatal(err)
	}
	movedB, err := db.GetRoutingRule(ctx, ruleBID)
	if err != nil {
		t.Fatal(err)
	}
	if movedA.ServerID != serverB.ID || movedA.StageStepID == nil || movedB.ServerID != serverA.ID || movedB.StageStepID != nil {
		t.Fatalf("REST placement did not move rules across stages: A=%#v B=%#v", movedA, movedB)
	}
	syncedResponse := request(t, handler, http.MethodPost, "/api/v2/ui/routing-rules", token, map[string]any{
		"scope": model.RoutingRuleScopePathStage, "proxy_path_id": path.ID, "sort_position": 1,
		"sync_source_rule_id": movedA.ID, "sync_enabled": true, "action": model.RouteActionBlock, "enabled": true,
	}, http.StatusCreated)
	syncedID := int64(syncedResponse["routing_rule"].(map[string]any)["id"].(float64))
	copyResponse := request(t, handler, http.MethodPost, "/api/v2/ui/routing-rules", token, map[string]any{
		"scope": model.RoutingRuleScopePathStage, "proxy_path_id": path.ID, "sort_position": 2,
		"sync_source_rule_id": movedA.ID, "sync_enabled": false, "action": model.RouteActionDirect, "enabled": true,
	}, http.StatusCreated)
	copyID := int64(copyResponse["routing_rule"].(map[string]any)["id"].(float64))
	movedA, _ = db.GetRoutingRule(ctx, movedA.ID)
	movedA.Name = "shared-updated"
	movedA.MatchJSON = `{"domain":["shared-updated.example"]}`
	request(t, handler, http.MethodPatch, "/api/v2/ui/routing-rules/"+itoa(movedA.ID), token, movedA, http.StatusOK)
	syncedRule, _ := db.GetRoutingRule(ctx, syncedID)
	independentRule, _ := db.GetRoutingRule(ctx, copyID)
	if syncedRule.Name != movedA.Name || syncedRule.MatchJSON != movedA.MatchJSON || syncedRule.Action != model.RouteActionBlock || syncedRule.SyncGroupID == "" {
		t.Fatalf("REST synchronized copy did not share only match fields: %#v", syncedRule)
	}
	if independentRule.Name == movedA.Name || independentRule.SyncGroupID != "" {
		t.Fatalf("REST one-time copy remained synchronized: %#v", independentRule)
	}
}

func TestRoutingRuleSetPeriodicRefreshOnlyAttemptsDueItems(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Truncate(time.Second)
	dueAt, freshAt := now.Add(-25*time.Hour), now.Add(-time.Hour)
	due := model.RoutingRuleSet{Name: "due", URL: "https://rules.example/due.json", Format: model.RoutingRuleSetFormatSingBoxSource, Content: []byte(`{"version":1,"rules":[]}`), Revision: "due-revision", Status: model.RoutingRuleSetStatusReady, LastAttemptAt: &dueAt, LastSuccessAt: &dueAt}
	fresh := model.RoutingRuleSet{Name: "fresh", URL: "https://rules.example/fresh.json", Format: model.RoutingRuleSetFormatSingBoxSource, Content: []byte(`{"version":1,"rules":[]}`), Revision: "fresh-revision", Status: model.RoutingRuleSetStatusReady, LastAttemptAt: &freshAt, LastSuccessAt: &freshAt}
	if err := db.CreateRoutingRuleSet(t.Context(), &due); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRoutingRuleSet(t.Context(), &fresh); err != nil {
		t.Fatal(err)
	}
	var calls []int64
	server := &Server{store: db, routingRuleSetFetcher: func(_ context.Context, item model.RoutingRuleSet, conditional bool) (*fetchedRoutingRuleSet, error) {
		if !conditional {
			t.Fatal("periodic refresh was not conditional")
		}
		calls = append(calls, item.ID)
		return &fetchedRoutingRuleSet{notModified: true, revision: item.Revision}, nil
	}}
	server.refreshDueRoutingRuleSets(t.Context(), now)
	if len(calls) != 1 || calls[0] != due.ID {
		t.Fatalf("periodic refresh calls = %v, want only %d", calls, due.ID)
	}
	persistedFresh, err := db.GetRoutingRuleSet(t.Context(), fresh.ID)
	if err != nil || persistedFresh.LastAttemptAt == nil || !persistedFresh.LastAttemptAt.Equal(freshAt) {
		t.Fatalf("fresh rule set was modified: %#v error=%v", persistedFresh, err)
	}
}

func TestRoutingRuleTargetPathValidationRequiresSharedPrefixAndRejectsCycles(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	servers := []model.Server{{Name: "A", Status: model.ServerOnline}, {Name: "B", Status: model.ServerOnline}, {Name: "C", Status: model.ServerOnline}, {Name: "D", Status: model.ServerOnline}}
	for index := range servers {
		if err := db.CreateServer(ctx, &servers[index]); err != nil {
			t.Fatal(err)
		}
	}
	root := &model.Inbound{ServerID: servers[0].ID, Name: "root", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	otherRoot := &model.Inbound{ServerID: servers[0].ID, Name: "other", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 444, ConfigJSON: `{}`, Enabled: true}
	for _, inbound := range []*model.Inbound{root, otherRoot} {
		if err := db.CreateInbound(ctx, inbound); err != nil {
			t.Fatal(err)
		}
	}
	createPath := func(inboundID int64, ids ...int64) (model.ProxyPath, []model.ProxyPathStep) {
		t.Helper()
		path := model.ProxyPath{InboundID: inboundID, Kind: model.ProxyPathKindChain, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: fmt.Sprintf("path-%d", len(ids)), Enabled: true}
		if err := db.CreateProxyPath(ctx, &path); err != nil {
			t.Fatal(err)
		}
		steps := make([]model.ProxyPathStep, 0, len(ids))
		for index, serverID := range ids {
			id := serverID
			step := model.ProxyPathStep{PathID: path.ID, Position: index + 1, NodeType: model.ProxyPathStepServerInbound, TransportMode: model.ProxyPathTransportSingBox, ServerID: &id, ConfigJSON: `{}`}
			if err := db.CreateProxyPathStep(ctx, &step); err != nil {
				t.Fatal(err)
			}
			steps = append(steps, step)
		}
		return path, steps
	}
	fallback, fallbackSteps := createPath(root.ID, servers[1].ID, servers[2].ID)
	target, _ := createPath(root.ID, servers[1].ID, servers[3].ID)
	wrongPrefix, _ := createPath(root.ID, servers[2].ID, servers[3].ID)
	wrongRoot, _ := createPath(otherRoot.ID, servers[1].ID, servers[3].ID)
	server := newTestServer(db, "test-secret", "")
	if err := server.validateRoutingRuleTargetPath(ctx, fallback.ID, &fallbackSteps[0].ID, target.ID, 0); err != nil {
		t.Fatalf("compatible target path rejected: %v", err)
	}
	if err := server.validateRoutingRuleTargetPath(ctx, fallback.ID, &fallbackSteps[0].ID, wrongPrefix.ID, 0); err == nil {
		t.Fatal("target path with a different prefix was accepted")
	}
	if err := server.validateRoutingRuleTargetPath(ctx, fallback.ID, &fallbackSteps[0].ID, wrongRoot.ID, 0); err == nil {
		t.Fatal("target path from another root inbound was accepted")
	}
	targetID, fallbackID := target.ID, fallback.ID
	cycleRule := &model.RoutingRule{ServerID: servers[1].ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &targetID, StageStepID: &[]int64{dbStepIDAt(t, db, target.ID, 1)}[0], MatchSource: model.RoutingMatchSourceInline, Name: "cycle", MatchJSON: `{}`, Action: model.RouteActionProxyPath, TargetProxyPathID: &fallbackID, Enabled: true}
	if err := db.CreateRoutingRule(ctx, cycleRule); err != nil {
		t.Fatal(err)
	}
	if err := server.validateRoutingRuleTargetPath(ctx, fallback.ID, &fallbackSteps[0].ID, target.ID, 0); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("routing target cycle was not rejected: %v", err)
	}
}

func dbStepIDAt(t *testing.T, db *store.Store, pathID int64, position int) int64 {
	t.Helper()
	steps, err := db.ListProxyPathStepsForPath(t.Context(), pathID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.Position == position {
			return step.ID
		}
	}
	t.Fatalf("path %d has no step %d", pathID, position)
	return 0
}

func TestRoutingRuleSetRefreshQueuesOnlyReferencingServers(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := t.Context()
	serverA := &model.Server{Name: "A", AgentID: "agent-a", PublicIPv4: "1.1.1.1", ListenIP: "0.0.0.0", PortRangeStart: 30000, PortRangeEnd: 30100, Status: model.ServerOnline}
	serverB := &model.Server{Name: "B", AgentID: "agent-b", PublicIPv4: "8.8.8.8", ListenIP: "0.0.0.0", PortRangeStart: 31000, PortRangeEnd: 31100, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, serverA); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, serverB); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: serverA.ID, Name: "entry", Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: `{}`, Enabled: true}
	if err := db.CreateInbound(ctx, inbound); err != nil {
		t.Fatal(err)
	}
	path := &model.ProxyPath{InboundID: inbound.ID, Kind: model.ProxyPathKindDirect, NameMode: model.ProxyPathNameAuto, ExitRegionMode: "auto", Secret: "path-secret", Enabled: true}
	if err := db.CreateProxyPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	set := &model.RoutingRuleSet{Name: "shared", URL: "https://rules.example/shared.json", Format: model.RoutingRuleSetFormatSingBoxSource, Content: []byte(`{"version":1,"rules":[]}`), Revision: "revision-1", Status: model.RoutingRuleSetStatusReady}
	if err := db.CreateRoutingRuleSet(ctx, set); err != nil {
		t.Fatal(err)
	}
	pathID, setID := path.ID, set.ID
	rule := &model.RoutingRule{ServerID: serverA.ID, Scope: model.RoutingRuleScopePathStage, ProxyPathID: &pathID, SortPosition: 0, MatchSource: model.RoutingMatchSourceRuleSet, RuleSetID: &setID, Name: "remote", MatchJSON: `{}`, Action: model.RouteActionDirect, Enabled: true}
	if err := db.CreateRoutingRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	server := newTestServer(db, "test-secret", "")
	server.routingRuleSetFetcher = func(context.Context, model.RoutingRuleSet, bool) (*fetchedRoutingRuleSet, error) {
		return &fetchedRoutingRuleSet{content: []byte(`{"version":1,"rules":[{"domain":["example.com"]}]}`), revision: "revision-2"}, nil
	}
	if _, changed, err := server.refreshRoutingRuleSet(ctx, set.ID); err != nil || !changed {
		t.Fatalf("refresh changed=%v error=%v", changed, err)
	}
	tasks, err := db.ListTasks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ServerID != serverA.ID || tasks[0].Type != model.AgentTaskTypeApplyCoreConfig {
		t.Fatalf("targeted refresh tasks = %#v", tasks)
	}
}
