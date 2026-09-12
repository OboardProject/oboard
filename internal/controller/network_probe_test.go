package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
)

const networkProbePlanFixture = `{"version":42,"resource_version":"public","mode":"icmp","enabled":true,"interval_seconds":60,"sample_count":1,"targets":[{"measurement_revision":"89b7099310e3e6bb0c397e74554e195c5dd4ab9d5721debe16fcee8c7439f05d","probe_id":"public-cloudflare","kind":"public","host":"cp.cloudflare.com","port":0},{"measurement_revision":"ba9d5697f6b3909a03a4eb9d637808d3cf732fe082996a0a16ecc9c727cf9cc4","probe_id":"t1-0","kind":"custom","task_id":1,"task_name":"TCP","mode":"tcp","host":"example.com","port":8443,"interval_seconds":300},{"measurement_revision":"ec7ef1d03ab170a3e28e4d5585c8c09e13aac260d1ba123226b9074179c2d97d","probe_id":"t2-0","kind":"custom","task_id":2,"task_name":"Ping","mode":"icmp","host":"1.1.1.1","ip":"1.1.1.1","port":0,"interval_seconds":60},{"measurement_revision":"5730cc66616c07bda6839306f7f7ee63957b14bd92584ccceb99c4f61b213c33","probe_id":"t3-0","kind":"custom","task_id":3,"task_name":"HTTP","mode":"http","url":"https://example.com/health","host":"example.com","port":443,"interval_seconds":900}]}`

func TestNetworkProbeMixedMethodWirePlan(t *testing.T) {
	var plan model.LatencyProbeTargetsPlan
	if err := json.Unmarshal([]byte(networkProbePlanFixture), &plan); err != nil {
		t.Fatal(err)
	}
	tasks := []model.LatencyProbeTask{
		{ID: 1, Name: "TCP", Method: "tcp", Address: "example.com", Port: 8443, IntervalSeconds: 300, Enabled: true},
		{ID: 2, Name: "Ping", Method: "icmp", Address: "1.1.1.1", IntervalSeconds: 60, Enabled: true},
		{ID: 3, Name: "HTTP", Method: "http", Address: "https://example.com/health", IntervalSeconds: 900, Enabled: true},
	}
	targets := latencyProbeTargets(latencyProbeResource{}, model.Server{LatencyProbeMode: "icmp", LatencyProbeMaxTargets: 64}, tasks)
	if !reflect.DeepEqual(targets, plan.Targets) {
		t.Fatalf("wire plan mismatch: %#v", targets)
	}
	if got := latencyProbeTargets(latencyProbeResource{}, model.Server{LatencyProbeMode: "icmp", LatencyProbeMaxTargets: 2}, tasks); len(got) != 2 {
		t.Fatalf("target cap ignored: %#v", got)
	}
}

func TestNetworkProbeCustomPlansDoNotFetchPresets(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	server := &model.Server{Name: "custom-node", AgentID: "custom-agent", Status: model.ServerOnline, LatencyProbeEnabled: true, LatencyProbeMode: "icmp"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := model.LatencyProbeTask{Name: "网站", Method: "http", Address: "https://example.com:8443/health", Enabled: true, ServerIDs: []int64{server.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	resetLatencyProbeCacheForTest()
	latencyProbeResourceFetcher = func(context.Context) (latencyProbeResource, error) {
		t.Error("custom task attempted to fetch presets")
		return latencyProbeResource{}, errors.New("offline")
	}
	t.Cleanup(func() { latencyProbeResourceFetcher = fetchLatencyProbeResource; resetLatencyProbeCacheForTest() })
	app := newTestServer(db, "test-secret", "")
	fresh, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.latencyProbePlanForServer(ctx, *fresh)
	if err != nil || len(plan.Targets) != 2 || plan.Targets[1].Mode != "http" || plan.Targets[1].Port != 8443 {
		t.Fatalf("plan: %#v %v", plan, err)
	}
	recorder := httptest.NewRecorder()
	app.serverLatencyProbe(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/servers/1/latency-probe", strings.NewReader(`{}`)), server.ID)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("manual probe: %d %s", recorder.Code, recorder.Body.String())
	}
	report := model.LatencyProbeResultReport{ReportID: "custom-report", ResourceVersion: plan.ResourceVersion, CheckedAt: time.Now().UTC()}
	for _, target := range plan.Targets {
		report.Items = append(report.Items, model.LatencyProbeResult{ProbeID: target.ProbeID, Kind: target.Kind, TaskID: target.TaskID, TaskName: "untrusted", Mode: string(effectiveLatencyTargetMode(target, plan.Mode)), Host: target.Host, IP: target.IP, Port: target.Port, SampleCount: 1, SuccessCount: 1, Available: true, LatencyMS: 12})
	}
	if err := validateAutonomousLatencyProbeReport(&report); err != nil {
		t.Fatal(err)
	}
	if err := app.resolveLatencyProbeReportTasks(ctx, server.ID, &report); err != nil {
		t.Fatal(err)
	}
	if report.Items[1].TaskName != task.Name {
		t.Fatal("Agent task label was trusted")
	}
	queued, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeProbeLatencyTargets)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(report)
	if err := app.applyLatencyProbeTaskResult(ctx, server.ID, *queued, "succeeded", string(raw)); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*model.LatencyProbeResult){
		func(item *model.LatencyProbeResult) { item.Mode = "tcp" },
		func(item *model.LatencyProbeResult) { item.Host = "other.example.com" },
		func(item *model.LatencyProbeResult) { item.Port = 443 },
		func(item *model.LatencyProbeResult) { item.TaskID++ },
		func(item *model.LatencyProbeResult) { item.ProbeID = "t999-0" },
	} {
		changed := report
		changed.Items = append([]model.LatencyProbeResult(nil), report.Items...)
		mutate(&changed.Items[1])
		if err := app.resolveLatencyProbeReportTasks(ctx, server.ID, &changed); err == nil {
			t.Fatalf("mismatched custom result accepted: %#v", changed.Items[1])
		}
	}
}

func TestNetworkProbeAutomationLifecycleAndBoundary(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	app := newTestServer(db, "test-secret", "")
	admin := &model.User{Username: "network-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "22222222-2222-4222-8222-222222222222", ProxyPassword: "unused"}
	if err := db.CreateUser(ctx, admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	server := &model.Server{Name: "network-node"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(fmt.Sprintf(`{"name":"HTTP health","method":"http","address":"https://example.com/health","server_ids":[%d]}`, server.ID))
	applyAutomationChangeset(t, app, principal, "network-create", automation.OperationRequest{Capability: "latency_probe_tasks.create", Input: input})
	tasks, err := db.ListLatencyProbeTasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %#v %v", tasks, err)
	}
	task := tasks[0]
	if task.Method != "http" || task.Address != "https://example.com/health" || task.Port != 0 {
		t.Fatalf("task fields lost: %#v", task)
	}
	update := json.RawMessage(fmt.Sprintf(`{"id":%d,"method":"tcp","address":"example.com","port":8443}`, task.ID))
	applyAutomationChangeset(t, app, principal, "network-update", automation.OperationRequest{Capability: "latency_probe_tasks.update", Input: update})
	got, err := db.GetLatencyProbeTask(ctx, task.ID)
	if err != nil || got.Method != "tcp" || got.Port != 8443 {
		t.Fatalf("update: %#v %v", got, err)
	}
	restricted := principal
	restricted.ResourceFilter, _ = json.Marshal(application.ResourceFilter{Servers: &application.ResourceSelection{Mode: "selected", IDs: []int64{server.ID + 1}}})
	if _, err := app.latencyProbeTaskBoundary(ctx, restricted, got.ID); err == nil {
		t.Fatal("task update bypassed server boundary")
	}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, model.LatencyProbeResultReport{ReportID: "delete-result", ResourceVersion: "v1", CheckedAt: time.Now().UTC(), Items: []model.LatencyProbeResult{{ProbeID: "custom", Kind: "custom", TaskID: task.ID}}}); err != nil {
		t.Fatal(err)
	}
	applyAutomationChangeset(t, app, principal, "network-delete", automation.OperationRequest{Capability: "latency_probe_tasks.delete", Input: json.RawMessage(fmt.Sprintf(`{"id":%d}`, task.ID))})
	if _, err := db.GetLatencyProbeTask(ctx, task.ID); err == nil {
		t.Fatal("deleted task survives")
	}
	if results, err := db.ListLatencyProbeResults(ctx, server.ID, 10); err != nil || len(results) != 0 {
		t.Fatalf("deleted task results survive: %#v %v", results, err)
	}
}

func TestNetworkProbeRESTTaskLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openControllerAutomationTestStore(t)
	app := newTestServer(db, "test-secret", "")
	create := httptest.NewRecorder()
	app.latencyProbeTasks(create, httptest.NewRequest(http.MethodPost, "/api/v1/ui/latency-probe-tasks", strings.NewReader(`{"method":"http","address":"https://example.com/health","name":"Web task","server_ids":[]}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var body struct {
		Task model.LatencyProbeTask `json:"latency_probe_task"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Task.Method != "http" || body.Task.Address != "https://example.com/health" {
		t.Fatalf("created task: %#v", body.Task)
	}
	patch := httptest.NewRecorder()
	app.latencyProbeTask(patch, httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/v1/latency-probe-tasks/%d", body.Task.ID), strings.NewReader(`{"method":"icmp","address":"1.1.1.1"}`)))
	if patch.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", patch.Code, patch.Body.String())
	}
	got, err := db.GetLatencyProbeTask(ctx, body.Task.ID)
	if err != nil || got.Method != "icmp" || got.Port != 0 {
		t.Fatalf("updated task: %#v %v", got, err)
	}
	invalid := httptest.NewRecorder()
	app.latencyProbeTask(invalid, httptest.NewRequest(http.MethodPatch, fmt.Sprintf("/api/v1/latency-probe-tasks/%d", body.Task.ID), strings.NewReader(`{"address":"127.0.0.1"}`)))
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("private target accepted: %d", invalid.Code)
	}
	got, err = db.GetLatencyProbeTask(ctx, body.Task.ID)
	if err != nil || got.Address != "1.1.1.1" {
		t.Fatal("invalid update changed saved task")
	}
	server := &model.Server{Name: "rest-probe-node"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, model.LatencyProbeResultReport{ReportID: "rest-delete", ResourceVersion: "v1", Items: []model.LatencyProbeResult{{ProbeID: "custom", Kind: "custom", TaskID: body.Task.ID}}}); err != nil {
		t.Fatal(err)
	}
	deleted := httptest.NewRecorder()
	app.latencyProbeTask(deleted, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/api/v1/latency-probe-tasks/%d", body.Task.ID), nil))
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if results, err := db.ListLatencyProbeResults(ctx, server.ID, 10); err != nil || len(results) != 0 {
		t.Fatalf("deleted task results survive: %#v %v", results, err)
	}
}

func TestNetworkProbePresetsWebAndMachine(t *testing.T) {
	resource, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["gd.example.com","1.1.1.1"]}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resetLatencyProbeCacheForTest()
	t.Cleanup(resetLatencyProbeCacheForTest)
	latencyProbeCache.Lock()
	latencyProbeCache.resource = resource
	latencyProbeCache.fetched = time.Now()
	latencyProbeCache.Unlock()
	app := newTestServer(openControllerAutomationTestStore(t), "test-secret", "")
	recorder := httptest.NewRecorder()
	app.latencyProbeResource(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/ui/latency-probe-resource", nil))
	if recorder.Code != 200 {
		t.Fatal(recorder.Body.String())
	}
	var web map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &web); err != nil {
		t.Fatal(err)
	}
	machine, err := app.queryManagementCapability(context.Background(), application.Principal{}, "latency_probe_tasks.targets", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(machine)
	var result map[string]any
	json.Unmarshal(raw, &result)
	if !reflect.DeepEqual(web["targets"], result["targets"]) || len(result["targets"].([]any)) != 2 {
		t.Fatalf("preset contracts differ: %s", raw)
	}
}
