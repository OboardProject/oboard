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

func TestLatencyProbeTargetsUseExactProvinceCarrierPairs(t *testing.T) {
	resource := latencyProbeResource{Version: "v1", Provinces: map[string]map[string][]string{
		"广东": {"中国电信": {"192.0.2.1", "192.0.2.2"}, "中国联通": {"192.0.2.3"}},
		"浙江": {"中国电信": {"192.0.2.4"}, "中国联通": {"192.0.2.5"}},
	}}
	server := model.Server{LatencyProbeMaxTargets: 3}
	tasks := []model.LatencyProbeTask{
		{ID: 1, Name: "广东 · 中国电信", Province: "广东", Carrier: "中国电信", IntervalSeconds: 120, Enabled: true},
		{ID: 2, Name: "浙江 · 中国联通", Province: "浙江", Carrier: "中国联通", IntervalSeconds: 900, Enabled: true},
		{ID: 3, Name: "已停用", Province: "广东", Carrier: "中国联通", IntervalSeconds: 60},
	}
	targets := latencyProbeTargets(resource, server, tasks)
	if len(targets) != 3 || targets[0].Kind != "public" || targets[1].Province != "广东" || targets[1].Carrier != "中国电信" || targets[2].Province != "浙江" || targets[2].Carrier != "中国联通" {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[1].TaskID != 1 || targets[1].TaskName != "广东 · 中国电信" || targets[1].IntervalSeconds != 120 || targets[2].TaskID != 2 || targets[2].IntervalSeconds != 900 {
		t.Fatalf("task identity and cadence = %#v", targets)
	}
	for _, target := range targets {
		if (target.Province == "广东" && target.Carrier == "中国联通") || (target.Province == "浙江" && target.Carrier == "中国电信") {
			t.Fatalf("generated an unselected or disabled pair: %#v", target)
		}
	}
}

// TestLatencyProbeTargetsSeparateTasksOnOneTarget covers two tasks that watch the
// same province and carrier at different cadences: they must stay distinct probes.
func TestLatencyProbeTargetsSeparateTasksOnOneTarget(t *testing.T) {
	resource := latencyProbeResource{Version: "v1", Provinces: map[string]map[string][]string{
		"广东": {"中国电信": {"192.0.2.1"}},
	}}
	tasks := []model.LatencyProbeTask{
		{ID: 4, Name: "华南备线", Province: "广东", Carrier: "中国电信", IntervalSeconds: 1800, Enabled: true},
		{ID: 5, Name: "华南主线", Province: "广东", Carrier: "中国电信", IntervalSeconds: 60, Enabled: true},
	}
	targets := latencyProbeTargets(resource, model.Server{LatencyProbeMaxTargets: 8}, tasks)
	if len(targets) != 3 {
		t.Fatalf("targets = %#v", targets)
	}
	// Targets are ordered by task name, so 华南主线 precedes 华南备线.
	if targets[1].TaskID != 5 || targets[1].IntervalSeconds != 60 || targets[2].TaskID != 4 || targets[2].IntervalSeconds != 1800 {
		t.Fatalf("per-task cadence = %#v", targets)
	}
	if targets[1].ProbeID == targets[2].ProbeID {
		t.Fatalf("tasks on one target share a probe id: %#v", targets)
	}
}

func TestLatencyProbeTargetsKeepHostnameWithoutLiteralIP(t *testing.T) {
	resource := latencyProbeResource{Version: "v1", Provinces: map[string]map[string][]string{
		"广东": {"中国电信": {"gd-ct-v4.ip.zstaticcdn.com"}, "教育网": {"192.0.2.8"}},
	}}
	server := model.Server{LatencyProbeMaxTargets: 8}
	tasks := []model.LatencyProbeTask{
		{ID: 1, Name: "广东 · 中国电信", Province: "广东", Carrier: "中国电信", IntervalSeconds: 300, Enabled: true},
		{ID: 2, Name: "广东 · 教育网", Province: "广东", Carrier: "教育网", IntervalSeconds: 300, Enabled: true},
	}
	targets := latencyProbeTargets(resource, server, tasks)
	if len(targets) != 3 {
		t.Fatalf("targets = %#v", targets)
	}
	if targets[1].Host != "gd-ct-v4.ip.zstaticcdn.com" || targets[1].IP != "" || targets[1].Province != "广东" || targets[1].Carrier != "中国电信" {
		t.Fatalf("hostname target = %#v", targets[1])
	}
	if targets[2].Host != "192.0.2.8" || targets[2].IP != "192.0.2.8" || targets[2].Carrier != "教育网" {
		t.Fatalf("literal target = %#v", targets[2])
	}
}

func TestValidateAutonomousLatencyProbeReportAcceptsHostnameRegionalTarget(t *testing.T) {
	now := time.Now().UTC()
	report := model.LatencyProbeResultReport{
		ReportID: "agent-latency-1", ResourceVersion: "v1", CheckedAt: now,
		Items: []model.LatencyProbeResult{
			{ProbeID: "public-cloudflare", Kind: "public", Mode: "tcp", Host: "cp.cloudflare.com", Port: 443, Available: true, LatencyMS: 20, MinLatencyMS: 18, P95LatencyMS: 22, SampleCount: 3, SuccessCount: 3},
			{ProbeID: "广东-中国电信-0", Kind: "regional", Mode: "tcp", Province: "广东", Carrier: "中国电信", Host: "gd-ct-v4.ip.zstaticcdn.com", Port: 80, Available: true, LatencyMS: 30, MinLatencyMS: 28, P95LatencyMS: 32, SampleCount: 3, SuccessCount: 3},
			{ProbeID: "广东-教育网-0", Kind: "regional", Mode: "tcp", Province: "广东", Carrier: "教育网", Host: "192.0.2.8", IP: "192.0.2.8", Port: 80, Available: true, LatencyMS: 12, MinLatencyMS: 10, P95LatencyMS: 14, SampleCount: 3, SuccessCount: 3},
		},
	}
	if err := validateAutonomousLatencyProbeReport(&report); err != nil {
		t.Fatal(err)
	}
	invalid := report
	invalid.Items = append([]model.LatencyProbeResult(nil), report.Items...)
	invalid.Items[1].Host = "localhost"
	if err := validateAutonomousLatencyProbeReport(&invalid); err == nil {
		t.Fatal("localhost regional host was accepted")
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
	if _, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["localhost"]}}`), time.Now()); err == nil {
		t.Fatal("single-label hostname was accepted")
	}
	hostnameResource, err := parseLatencyProbeResource([]byte(`{"广东":{"中国电信":["gd-ct-v4.ip.zstaticcdn.com"]}}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if hostnameResource.Provinces["广东"]["中国电信"][0] != "gd-ct-v4.ip.zstaticcdn.com" {
		t.Fatalf("hostname resource = %#v", hostnameResource)
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
	server := &model.Server{Name: "latency-edge", AgentID: "latency-agent", AgentTokenHash: security.HashSecret("latency-token"), Status: model.ServerOnline, LatencyProbeEnabled: true, LatencyProbeMode: model.LatencyProbeModeICMP, LatencyProbeSampleCount: 3, LatencyProbeMaxTargets: 8}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	probeTask := model.LatencyProbeTask{Method: model.LatencyProbeModeICMP, Name: "广东 · 中国电信", Province: "广东", Carrier: "中国电信", IntervalSeconds: 300, Enabled: true, ServerIDs: []int64{server.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &probeTask); err != nil {
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
	target := plan.Targets[1]
	report := model.LatencyProbeResultReport{ReportID: "manual-report", ResourceVersion: plan.ResourceVersion, CheckedAt: time.Now().UTC(), Items: []model.LatencyProbeResult{{ProbeID: target.ProbeID, Kind: target.Kind, Mode: string(plan.Mode), Province: target.Province, Carrier: target.Carrier, Host: target.Host, IP: target.IP, Port: 0, Available: true, LatencyMS: 12, MinLatencyMS: 10, P95LatencyMS: 14, JitterMS: 2, SampleCount: 3, SuccessCount: 3}}}
	raw, _ := json.Marshal(report)
	if err := app.applyLatencyProbeTaskResult(ctx, server.ID, *task, "succeeded", string(raw)); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListLatencyProbeResults(ctx, server.ID, 10)
	if err != nil || len(items) != 1 || items[0].LatencyMS != 12 {
		t.Fatalf("stored results = %#v err=%v", items, err)
	}
	// Task identity comes from the queued plan, never from the Agent report.
	if items[0].TaskID != probeTask.ID || items[0].TaskName != probeTask.Name {
		t.Fatalf("stored result lost its task identity: %#v", items[0])
	}
}

// TestLatencyProbeAutonomousReportResolvesTasksFromAssignment proves an autonomous
// report can only name probe tasks the controller actually assigned to that server,
// and that the stored task name is the controller's, not the Agent's.
func TestLatencyProbeAutonomousReportResolvesTasksFromAssignment(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "latency-autonomous.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "latency-autonomous", LatencyProbeEnabled: true}
	other := &model.Server{Name: "latency-other", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, other); err != nil {
		t.Fatal(err)
	}
	assigned := model.LatencyProbeTask{Name: "华南主线", Province: "广东", Carrier: "中国电信", IntervalSeconds: 300, Enabled: true, ServerIDs: []int64{server.ID}}
	foreign := model.LatencyProbeTask{Name: "别人的任务", Province: "浙江", Carrier: "中国联通", IntervalSeconds: 300, Enabled: true, ServerIDs: []int64{other.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &assigned); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveLatencyProbeTask(ctx, &foreign); err != nil {
		t.Fatal(err)
	}
	app := newTestServer(db, "test-secret", "")

	report := model.LatencyProbeResultReport{ReportID: "auto-1", ResourceVersion: "v1", CheckedAt: time.Now().UTC(), Items: []model.LatencyProbeResult{
		{ProbeID: "public-cloudflare", Kind: "public", TaskID: 99, TaskName: "伪造", Mode: "tcp", Host: "cp.cloudflare.com", Port: 443, Available: true, LatencyMS: 20, MinLatencyMS: 18, P95LatencyMS: 22, SampleCount: 3, SuccessCount: 3},
		{ProbeID: "t1-0", Kind: "regional", TaskID: assigned.ID, TaskName: "Agent 自己编的名字", Mode: "tcp", Province: "广东", Carrier: "中国电信", Host: "192.0.2.1", IP: "192.0.2.1", Port: 80, Available: true, LatencyMS: 30, MinLatencyMS: 28, P95LatencyMS: 32, SampleCount: 3, SuccessCount: 3},
	}}
	if err := app.resolveLatencyProbeReportTasks(ctx, server.ID, &report); err != nil {
		t.Fatal(err)
	}
	if report.Items[0].TaskID != 0 || report.Items[0].TaskName != "" {
		t.Fatalf("public item kept a task identity: %#v", report.Items[0])
	}
	if report.Items[1].TaskName != assigned.Name {
		t.Fatalf("agent-supplied task name was trusted: %#v", report.Items[1])
	}

	unassigned := report
	unassigned.Items = append([]model.LatencyProbeResult(nil), report.Items...)
	unassigned.Items[1].TaskID = foreign.ID
	if err := app.resolveLatencyProbeReportTasks(ctx, server.ID, &unassigned); err == nil {
		t.Fatal("a task assigned to another server was accepted")
	}

	mismatched := report
	mismatched.Items = append([]model.LatencyProbeResult(nil), report.Items...)
	mismatched.Items[1].Carrier = "中国移动"
	if err := app.resolveLatencyProbeReportTasks(ctx, server.ID, &mismatched); err == nil {
		t.Fatal("a result whose target contradicts its task was accepted")
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
	if err := db.SaveLatencyProbeResults(ctx, allowed.ID, model.LatencyProbeResultReport{ReportID: "mcp-report", ResourceVersion: "v1", CheckedAt: time.Now().UTC(), Items: []model.LatencyProbeResult{{ProbeID: "p1", Kind: "regional", Mode: "icmp", Province: "广东", Carrier: "中国电信", Host: "192.0.2.1", IP: "192.0.2.1", Available: true, LatencyMS: 12, MinLatencyMS: 10, P95LatencyMS: 14, JitterMS: 2, SampleCount: 3, SuccessCount: 3}}}); err != nil {
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
