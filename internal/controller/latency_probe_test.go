package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestLatencyProbeTargetsFilterAndLimit(t *testing.T) {
	resource := latencyProbeResource{Version: "v1", Provinces: map[string]map[string][]string{
		"广东": {"中国电信": {"192.0.2.1", "192.0.2.2"}, "中国联通": {"192.0.2.3"}},
		"浙江": {"中国电信": {"192.0.2.4"}},
	}}
	server := model.Server{LatencyProbeProvinces: []string{"广东"}, LatencyProbeCarriers: []string{"中国电信"}, LatencyProbeMaxTargets: 1}
	targets := latencyProbeTargets(resource, server)
	if len(targets) != 1 || targets[0].Province != "广东" || targets[0].Carrier != "中国电信" || targets[0].IP != "192.0.2.1" {
		t.Fatalf("targets = %#v", targets)
	}
}

func TestLatencyProbeTargetsDistributeLimitAcrossProvincesAndCarriers(t *testing.T) {
	resource := latencyProbeResource{Version: "v1", Provinces: map[string]map[string][]string{
		"安徽": {"中国电信": {"192.0.2.1"}, "中国联通": {"192.0.2.2"}},
		"广东": {"中国电信": {"192.0.2.3"}, "中国联通": {"192.0.2.4"}},
		"浙江": {"中国电信": {"192.0.2.5"}, "中国联通": {"192.0.2.6"}},
	}}
	targets := latencyProbeTargets(resource, model.Server{LatencyProbeMaxTargets: 3})
	if len(targets) != 3 {
		t.Fatalf("targets = %#v", targets)
	}
	provinces := map[string]bool{}
	carriers := map[string]bool{}
	for _, target := range targets {
		provinces[target.Province] = true
		carriers[target.Carrier] = true
	}
	if len(provinces) != 3 || len(carriers) != 2 {
		t.Fatalf("limit distribution provinces=%#v carriers=%#v targets=%#v", provinces, carriers, targets)
	}
}

func TestLoadLatencyProbeResourceValidationAndCache(t *testing.T) {
	firstBody := []byte(`{"广东":{"中国电信":["192.0.2.1"]}}`)
	resource, err := parseLatencyProbeResource(firstBody, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	changed, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["192.0.2.2"]}}`), time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if resource.Version == changed.Version || resource.UpdatedAt.Equal(changed.UpdatedAt) {
		t.Fatalf("resource versions did not change: first=%#v changed=%#v", resource, changed)
	}
	if _, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["2001:db8::1"]}}`), time.Now()); err == nil {
		t.Fatal("IPv6 resource target was accepted")
	}
	if _, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["127.0.0.1"]}}`), time.Now()); err == nil {
		t.Fatal("non-public resource target was accepted")
	}
	latencyProbeCache.Lock()
	latencyProbeCache.resource = resource
	latencyProbeCache.fetched = time.Now()
	latencyProbeCache.Unlock()
	cached, err := loadLatencyProbeResource(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if cached.Version != resource.Version || cached.Provinces["广东"]["中国电信"][0] != "192.0.2.1" {
		t.Fatalf("cached resource = %#v", cached)
	}
}

func TestLoadLatencyProbeResourceCoalescesConcurrentRefresh(t *testing.T) {
	var requests atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	latencyProbeResourceFetcher = func(context.Context) (latencyProbeResource, error) {
		requests.Add(1)
		once.Do(func() { close(entered) })
		<-release
		return parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["192.0.2.1"]}}`), time.Now())
	}
	resetLatencyProbeCacheForTest()
	t.Cleanup(func() {
		latencyProbeResourceFetcher = fetchLatencyProbeResource
		resetLatencyProbeCacheForTest()
	})

	results := make(chan error, 2)
	go func() { _, err := loadLatencyProbeResource(context.Background(), false); results <- err }()
	<-entered
	go func() { _, err := loadLatencyProbeResource(context.Background(), true); results <- err }()
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("resource requests = %d, want 1", requests.Load())
	}
}

func TestLatencyProbeManualRunDeduplicatesAndStoresCallback(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "latency-controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "latency-edge", AgentID: "latency-agent", AgentTokenHash: security.HashSecret("latency-token"), Status: model.ServerOnline, LatencyProbeEnabled: true, LatencyProbeSampleCount: 3, LatencyProbeMaxTargets: 8}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	resource, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["192.0.2.1"]}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	latencyProbeCache.Lock()
	latencyProbeCache.resource = resource
	latencyProbeCache.fetched = time.Now()
	latencyProbeCache.Unlock()

	app := newTestServer(db, "test-secret", "")
	firstReq := httptest.NewRequest(http.MethodPost, "/api/v1/servers/1/latency-probe", nil)
	first := httptest.NewRecorder()
	app.serverLatencyProbe(first, firstReq, server.ID)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d body=%s", first.Code, first.Body.String())
	}
	second := httptest.NewRecorder()
	app.serverLatencyProbe(second, httptest.NewRequest(http.MethodPost, "/api/v1/servers/1/latency-probe", nil), server.ID)
	if second.Code != http.StatusAccepted {
		t.Fatalf("second status = %d body=%s", second.Code, second.Body.String())
	}
	var secondBody map[string]any
	if err := json.Unmarshal(second.Body.Bytes(), &secondBody); err != nil || secondBody["existing"] != true {
		t.Fatalf("second body = %s err=%v", second.Body.String(), err)
	}
	task, err := db.ActiveTaskByServerType(ctx, server.ID, model.AgentTaskTypeProbeLatencyTargets)
	if err != nil {
		t.Fatal(err)
	}
	var plan model.LatencyProbeTargetsPlan
	if err := json.Unmarshal([]byte(task.PayloadJSON), &plan); err != nil {
		t.Fatal(err)
	}
	report := model.LatencyProbeResultReport{ResourceVersion: plan.ResourceVersion, CheckedAt: time.Now().UTC(), Items: []model.LatencyProbeResult{{ProbeID: plan.Targets[0].ProbeID, Province: plan.Targets[0].Province, Carrier: plan.Targets[0].Carrier, IP: plan.Targets[0].IP, Available: true, LatencyMS: 12, MinLatencyMS: 10, P95LatencyMS: 14, JitterMS: 2, SampleCount: 3, SuccessCount: 3}}}
	raw, _ := json.Marshal(report)
	if err := app.applyLatencyProbeTaskResult(ctx, server.ID, *task, "succeeded", string(raw)); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListLatencyProbeResults(ctx, server.ID, 10)
	if err != nil || len(items) != 1 || items[0].LatencyMS != 12 {
		t.Fatalf("stored results = %#v err=%v", items, err)
	}
}

func TestLatencyProbeAgentVersionGate(t *testing.T) {
	if latencyProbeAgentUpgradeRequired(model.Server{AgentBuild: ""}) || latencyProbeAgentUpgradeRequired(model.Server{AgentBuild: "dev"}) || latencyProbeAgentUpgradeRequired(model.Server{AgentBuild: agentBuildMinLatencyProbe}) {
		t.Fatal("supported or unknown Agent build requires an upgrade")
	}
	if !latencyProbeAgentUpgradeRequired(model.Server{AgentBuild: "20260811000000"}) {
		t.Fatal("known old Agent build was accepted")
	}
}

func TestLatencyProbeSchedulerWaitsAfterFailedTask(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "latency-retry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "latency-retry", AgentID: "latency-agent", AgentBuild: agentBuildMinLatencyProbe, Status: model.ServerOnline, LatencyProbeEnabled: true, LatencyProbeIntervalSeconds: 300, LatencyProbeSampleCount: 3, LatencyProbeMaxTargets: 8, LatencyProbeResourceVersion: "v1"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	resource, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["192.0.2.1"]}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resource.Version = "v1"
	latencyProbeCache.Lock()
	latencyProbeCache.resource = resource
	latencyProbeCache.fetched = time.Now()
	latencyProbeCache.Unlock()
	t.Cleanup(resetLatencyProbeCacheForTest)
	failed := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeProbeLatencyTargets, PayloadJSON: `{}`, Status: "failed", ResultJSON: `{}`, ConfigVersion: 1, Nonce: "failed-latency"}
	if err := db.CreateTask(ctx, failed); err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test-secret", "")
	if err := app.enqueueConfiguredLatencyProbe(ctx, *server, false); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks=%#v err=%v", tasks, err)
	}
}

func TestLatencyProbeMCPResourceHonorsServerBoundary(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "latency-mcp.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	allowed := &model.Server{Name: "allowed", LatencyProbeEnabled: true, LatencyProbeResourceVersion: "v1"}
	denied := &model.Server{Name: "denied"}
	if err := db.CreateServer(ctx, allowed); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, denied); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLatencyProbeResults(ctx, allowed.ID, model.LatencyProbeResultReport{ResourceVersion: "v1", Items: []model.LatencyProbeResult{{ProbeID: "p1", Province: "广东", Carrier: "中国电信", IP: "192.0.2.1", Available: true, LatencyMS: 12, MinLatencyMS: 10, P95LatencyMS: 14, JitterMS: 2, SampleCount: 3, SuccessCount: 3}}}); err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test-secret", "")
	boundary := mcpauth.ResourceBoundary{Version: mcpauth.ResourceBoundaryVersion, Resources: map[string]mcpauth.ResourceSelection{"server": {Selection: mcpauth.SelectionSelected, IDs: []string{strconv.FormatInt(allowed.ID, 10)}}}}
	grant := mcpauth.GrantPolicy{GrantID: "grant-latency", AccessLevel: mcpauth.AccessRead, ResourceBoundary: boundary, IssuedAt: time.Now().UTC()}
	principal := application.Principal{ID: "grant-latency", Role: model.RoleViewer, AccessLevel: mcpauth.AccessRead, GrantPolicy: &grant, ResourceFilter: application.ResourceFilterFromBoundary(boundary), SourceIP: netip.MustParseAddr("127.0.0.1")}
	grantPrincipal := mcpauth.GrantPrincipal{Grant: grant, Role: model.RoleViewer}
	ctx = context.WithValue(ctx, mcpGrantPrincipalContextKey{}, grantPrincipal)
	def := mcpResourceDef{uri: "oboard://servers/{id}/latency-probes", capability: "servers.get", template: true, kind: "query_server_latency"}
	payload, err := app.readMCPResource(ctx, principal, def, "oboard://servers/"+strconv.FormatInt(allowed.ID, 10)+"/latency-probes")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(payload)
	if !contains(encoded, `"resource_version":"v1"`) || !contains(encoded, `"probe_id":"p1"`) {
		t.Fatalf("latency resource = %s", encoded)
	}
	if _, err := app.readMCPResource(ctx, principal, def, "oboard://servers/"+strconv.FormatInt(denied.ID, 10)+"/latency-probes"); err == nil {
		t.Fatal("resource boundary allowed another server")
	}
}

func resetLatencyProbeCacheForTest() {
	latencyProbeCache.Lock()
	latencyProbeCache.resource = latencyProbeResource{}
	latencyProbeCache.fetched = time.Time{}
	latencyProbeCache.attempted = time.Time{}
	latencyProbeCache.refreshing = false
	latencyProbeCache.refreshed = nil
	latencyProbeCache.lastError = nil
	latencyProbeCache.Unlock()
}
