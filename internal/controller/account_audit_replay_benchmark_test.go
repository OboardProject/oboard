package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

type auditReplayScenario struct {
	name             string
	disabled         bool
	sources, readers int
	missingNode      bool
}

var auditReplayScenarios = []auditReplayScenario{
	{name: "audit_disabled", disabled: true, sources: 8},
	{name: "base_plus_rules", sources: 8},
	{name: "multiple_snapshot_readers", sources: 8, readers: 4},
	{name: "high_cardinality", sources: 128},
	{name: "missing_node", sources: 8, missingNode: true},
}

type auditReplayLatency struct {
	Count int   `json:"count"`
	P50   int64 `json:"p50_ns"`
	P95   int64 `json:"p95_ns"`
	P99   int64 `json:"p99_ns"`
}

func auditReplayPercentiles(samples []int64) auditReplayLatency {
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	result := auditReplayLatency{Count: len(samples)}
	if len(samples) > 0 {
		percentile := func(p int) int64 { return samples[(len(samples)*p+99)/100-1] }
		result.P50, result.P95, result.P99 = percentile(50), percentile(95), percentile(99)
	}
	return result
}

type auditReplayResult struct {
	Scenario         string             `json:"scenario"`
	Seed             int64              `json:"seed"`
	Clock            string             `json:"clock"`
	Scope            string             `json:"scope"`
	Reports          int                `json:"reports"`
	Sources          int                `json:"sources_per_report"`
	Readers          int                `json:"snapshot_readers"`
	ElapsedNS        int64              `json:"elapsed_ns"`
	ReportsPerSecond float64            `json:"receiver_reports_per_second_end_to_end"`
	Receiver         auditReplayLatency `json:"receiver_latency"`
	Query            auditReplayLatency `json:"snapshot_query_latency"`
	Pipeline         auditReplayLatency `json:"apply_and_rules_latency"`
	AllocatedBytes   uint64             `json:"process_total_alloc_bytes_delta"`
	Allocations      uint64             `json:"process_mallocs_delta"`
	HeapBefore       uint64             `json:"process_heap_alloc_bytes_before"`
	HeapAfter        uint64             `json:"process_heap_alloc_bytes_after"`
	GoroutinesBefore int                `json:"process_goroutines_before"`
	GoroutinesAfter  int                `json:"process_goroutines_after"`
	Applied          int                `json:"applied_batches"`
	DirtyAfter       int                `json:"dirty_accounts_after"`
	WorkAfter        int                `json:"audit_work_rows_after"`
	DBStatements     *int64             `json:"db_statements"`
	Instrumentation  string             `json:"instrumentation_limits"`
}

// Uses the authenticated HTTP receiver and real SQLite, not a proxy data plane.
// Fixture creation is excluded from JSON measurements; Go benchmark totals include it.
func runAccountAuditReplay(t testing.TB, scenario auditReplayScenario, rounds int) auditReplayResult {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "replay.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.InitAccountActivityPipelineSchema(ctx))
	enableTestAudit(t, db)
	if scenario.disabled {
		must(db.SetSetting(ctx, settingAuditEnabled, "false"))
	}
	user := &model.User{Username: "replay-account", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111111", ProxyPassword: "fixture-only"}
	must(db.CreateUser(ctx, user))
	nodeCount := 1
	if scenario.missingNode {
		nodeCount = 2
	}
	var sender *model.Server
	var entry *model.Inbound
	var nodes []model.SubscriptionPlanNode
	for i := 0; i < nodeCount; i++ {
		node := &model.Server{Name: fmt.Sprintf("replay-node-%d", i), AgentID: fmt.Sprintf("replay-agent-%d", i), AgentTokenHash: security.HashSecret("replay-token"), ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true, KernelCapabilities: []string{"account_activity_v1"}}
		must(db.CreateServer(ctx, node))
		inbound := &model.Inbound{ServerID: node.ID, Name: fmt.Sprintf("replay-entry-%d", i), Protocol: model.ProtocolVLESS, ListenIP: "0.0.0.0", Port: 443, ConfigJSON: "{}", Enabled: true}
		must(db.CreateInbound(ctx, inbound))
		nodes = append(nodes, model.SubscriptionPlanNode{NodeType: model.AssignableNodeInbound, NodeID: inbound.ID})
		if i == 0 {
			sender, entry = node, inbound
		}
	}
	plan := &model.SubscriptionPlan{Name: "replay-plan", Enabled: true}
	must(db.CreateSubscriptionPlan(ctx, plan, nodes))
	must(db.SetUserPlanBindings(ctx, []model.UserPlanBinding{{UserID: user.ID, PlanID: plan.ID}}))
	sut := newTestServer(db, "replay-secret", "")
	start := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	const seed int64 = 20260920
	random := rand.New(rand.NewSource(seed))
	payloads := make([][]byte, rounds)
	for i := range payloads {
		report := accountActivityWireReport{CollectorStartedAt: start.Add(-time.Minute).UnixNano(), CollectorBootID: "0123456789abcdef0123456789abcdef", StreamType: "kernel", Sequence: int64(i + 1), MinuteUnix: start.Add(time.Duration(i) * time.Minute).Unix(), ClockState: "aligned", Complete: true}
		for j := 0; j < scenario.sources; j++ {
			report.Items = append(report.Items, accountActivityWireItem{UserID: user.ID, InboundID: entry.ID, SourcePrefix: fmt.Sprintf("8.8.%d.0/24", j), ActivityBits: 7, UploadBytes: int64(16384 + random.Intn(1024))})
		}
		payloads[i], err = json.Marshal(report)
		must(err)
	}
	result := auditReplayResult{Scenario: scenario.name, Seed: seed, Clock: start.Format(time.RFC3339), Scope: "in-process HTTP receiver + SQLite apply/rules + snapshot queries; not proxy throughput, production RSS, or accuracy", Reports: rounds, Sources: scenario.sources, Readers: scenario.readers, Instrumentation: "SQL statement hook unavailable (null); work/dirty rows are bounded final observations, not queue high-water marks; MemStats are process-wide, not RSS; request limiter uses real time"}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result.GoroutinesBefore = runtime.NumGoroutine()
	wall := time.Now()
	receiverTimes, pipelineTimes := make([]int64, 0, rounds), make([]int64, 0, rounds)
	queryTimes := make([][]int64, scenario.readers)
	readerErrors := make([]error, scenario.readers)
	jobs := make([]chan struct{}, scenario.readers)
	var readers sync.WaitGroup
	for i := range jobs {
		jobs[i] = make(chan struct{}, rounds)
		readers.Add(1)
		go func(i int) {
			defer readers.Done()
			for range jobs[i] {
				began := time.Now()
				_, err := db.ListAccountAuditSnapshots(ctx, store.AccountAuditQuery{UserID: user.ID, Limit: 1})
				queryTimes[i] = append(queryTimes[i], time.Since(began).Nanoseconds())
				if err != nil {
					readerErrors[i] = err
				}
			}
		}(i)
	}
	// Always reap readers, including on a fatal assertion below.
	defer func() {
		for _, job := range jobs {
			close(job)
		}
		readers.Wait()
	}()
	for i, payload := range payloads {
		at := start.Add(time.Duration(i) * time.Minute)
		now := at.Add(61 * time.Second)
		if !scenario.disabled {
			must(sut.recordAccountActivityExpectations(ctx, at))
		}
		for _, job := range jobs {
			job <- struct{}{}
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/account-activity", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer replay-token")
		req.Header.Set("X-Agent-ID", sender.AgentID)
		rr := httptest.NewRecorder()
		began := time.Now()
		sut.agentAccountActivityAt(rr, req, now)
		receiverTimes = append(receiverTimes, time.Since(began).Nanoseconds())
		if rr.Code != http.StatusOK {
			t.Fatalf("replay receiver status %d: %s", rr.Code, rr.Body.String())
		}
		if scenario.disabled && !bytes.Contains(rr.Body.Bytes(), []byte(`"accepted":false`)) {
			t.Fatal("disabled receiver accepted report")
		}
		began = time.Now()
		applied, err := db.ApplyPendingAccountActivity(ctx, 64, now)
		must(err)
		wantApplied := 1
		if scenario.disabled {
			wantApplied = 0
		}
		if applied != wantApplied {
			t.Fatalf("applied %d batches, want %d", applied, wantApplied)
		}
		result.Applied += applied
		if !scenario.disabled {
			must(sut.queueAccountActivityDirty(ctx))
			_, err = sut.evaluateAccountAuditWork(ctx, now, 0)
			must(err)
		}
		pipelineTimes = append(pipelineTimes, time.Since(began).Nanoseconds())
	}
	// Finish exactly the scheduled reads before sampling process counters.
	for _, job := range jobs {
		close(job)
	}
	readers.Wait()
	jobs = nil
	for _, err := range readerErrors {
		must(err)
	}
	result.ElapsedNS = time.Since(wall).Nanoseconds()
	runtime.ReadMemStats(&after)
	result.GoroutinesAfter = runtime.NumGoroutine()
	result.AllocatedBytes, result.Allocations = after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs
	result.HeapBefore, result.HeapAfter = before.HeapAlloc, after.HeapAlloc
	result.ReportsPerSecond = float64(rounds) / (float64(result.ElapsedNS) / 1e9)
	result.Receiver = auditReplayPercentiles(receiverTimes)
	result.Pipeline = auditReplayPercentiles(pipelineTimes)
	var queries []int64
	for _, samples := range queryTimes {
		queries = append(queries, samples...)
	}
	result.Query = auditReplayPercentiles(queries)
	dirty, err := db.ListAccountActivityDirty(ctx, 100)
	must(err)
	result.DirtyAfter = len(dirty)
	work, err := db.ListAccountAuditWork(ctx, 0, 100)
	must(err)
	result.WorkAfter = len(work)
	return result
}

func TestAccountAuditReplaySmoke(t *testing.T) {
	for _, scenario := range auditReplayScenarios {
		t.Run(scenario.name, func(t *testing.T) {
			result := runAccountAuditReplay(t, scenario, 2)
			if result.Receiver.Count != 2 || result.Query.Count != 2*scenario.readers {
				t.Fatalf("incomplete replay: %+v", result)
			}
		})
	}
}

func BenchmarkAccountAuditReplay(b *testing.B) {
	for _, scenario := range auditReplayScenarios {
		b.Run(scenario.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				result := runAccountAuditReplay(b, scenario, 32)
				encoded, err := json.Marshal(result)
				if err != nil {
					b.Fatal(err)
				}
				fmt.Printf("AUDIT_REPLAY_JSON %s\n", encoded)
			}
		})
	}
}
