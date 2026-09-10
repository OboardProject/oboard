package controller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/perfload"
	"github.com/OboardProject/oboard/internal/security"
)

// steadyStateReport is the machine-readable before/after artifact for the
// steady-state Agent path. It separates computation from database work: SQL
// statements and write transactions are counted independently of wall time, so
// "fewer statements" is never mistaken for "less CPU".
type steadyStateReport struct {
	Spec               perfload.Spec   `json:"spec"`
	Cycles             int             `json:"cycles_per_server"`
	Reports            int             `json:"reports"`
	SQLStatements      int64           `json:"sql_statements"`
	SQLWriteTx         int64           `json:"sql_write_transactions"`
	AllocBytes         uint64          `json:"alloc_bytes"`
	AllocObjects       uint64          `json:"alloc_objects"`
	WallMS             float64         `json:"wall_ms"`
	HotPath            hotPathSnapshot `json:"hot_path"`
	BaselineSQL        int64           `json:"baseline_sql_statements"`
	BaselineSQLWriteTx int64           `json:"baseline_sql_write_transactions"`
	BaselineWallMS     float64         `json:"baseline_wall_ms"`
	GeneratedAt        time.Time       `json:"generated_at"`
	Notes              []string        `json:"notes"`
	AuditDisabledFleet bool            `json:"audit_disabled_fleet"`
}

// steadyStateScale selects the fixture size. The default stays small so the
// regression harness is cheap; OBOARD_PERF_SCALE=xlarge or huge reproduces the
// 500 and 1000 server shapes for a reported measurement run.
func steadyStateScale() perfload.Scale {
	switch os.Getenv("OBOARD_PERF_SCALE") {
	case "medium":
		return perfload.ScaleMedium
	case "large":
		return perfload.ScaleLarge
	case "xlarge":
		return perfload.ScaleXLarge
	case "huge":
		return perfload.ScaleHuge
	default:
		return perfload.ScaleSmall
	}
}

// TestSteadyStateHeartbeatCost measures the repeated per-heartbeat work of a
// fleet whose configuration does not change: probe plan generation, remote
// access capability persistence and the audit-disabled presence cleanup. It is
// the P0 baseline artifact; it asserts only the structural property the
// optimization exists for, and leaves absolute numbers to the report.
func TestSteadyStateHeartbeatCost(t *testing.T) {
	ctx := context.Background()
	spec := perfload.SpecFor(steadyStateScale())
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "steady-state-secret", "")

	servers := make([]*model.Server, 0, spec.Servers)
	for i := range spec.Servers {
		server := &model.Server{
			Name: "steady-node-" + strconvFormatInt(int64(i)), AgentID: "steady-agent-" + strconvFormatInt(int64(i)),
			AgentTokenHash: security.HashSecret("steady-token"), Status: model.ServerOnline,
			LatencyProbeEnabled: true, LatencyProbeMode: model.LatencyProbeModeTCP,
		}
		if err := db.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
		fresh, err := db.GetServer(ctx, server.ID)
		if err != nil {
			t.Fatal(err)
		}
		servers = append(servers, fresh)
	}

	report := model.RemoteAccessReport{
		Capabilities: []string{model.RemoteAccessCapabilityExec, model.RemoteAccessCapabilityInteractiveMCP},
		LocalMode:    model.RemoteAccessModeStandard,
		LocalAllow:   model.RemoteAccessLocalAllow{RemoteTerminal: true},
	}
	heartbeat := func(server *model.Server) {
		if _, err := srv.cachedLatencyProbePlanForServer(ctx, *server); err != nil {
			t.Fatal(err)
		}
		if err := srv.persistRemoteAccessStatus(ctx, server.ID, server.AgentID, report); err != nil {
			t.Fatal(err)
		}
		srv.syncConnectionAuditPresence(ctx, server, false)
	}

	// One warm cycle stands in for the first hello of each Agent; the measured
	// window is the steady state that follows it.
	for _, server := range servers {
		heartbeat(server)
	}

	const cycles = 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	sqlBefore := db.SQLStatementCount()
	txBefore := db.SQLWriteTransactionCount()
	hotBefore := srv.hotPathMetrics()
	start := time.Now()
	for range cycles {
		for _, server := range servers {
			heartbeat(server)
		}
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	hotAfter := srv.hotPathMetrics()

	// The same steady window driven through the uncached paths, so the report
	// carries its own before/after pair from one run on one machine.
	baselineSQL := db.SQLStatementCount()
	baselineTx := db.SQLWriteTransactionCount()
	baselineStart := time.Now()
	for range cycles {
		for _, server := range servers {
			if _, err := srv.latencyProbePlanForServer(ctx, *server); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertServerRemoteAccessStatus(ctx, server.ID, report); err != nil {
				t.Fatal(err)
			}
			if err := db.ClearConnectionPresenceForServer(ctx, server.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	baselineElapsed := time.Since(baselineStart)

	statements := baselineSQL - sqlBefore
	transactions := baselineTx - txBefore

	out := steadyStateReport{
		Spec:               spec,
		Cycles:             cycles,
		Reports:            cycles * len(servers),
		SQLStatements:      statements,
		SQLWriteTx:         transactions,
		AllocBytes:         after.TotalAlloc - before.TotalAlloc,
		AllocObjects:       after.Mallocs - before.Mallocs,
		WallMS:             float64(elapsed.Microseconds()) / 1000,
		BaselineSQL:        db.SQLStatementCount() - baselineSQL,
		BaselineSQLWriteTx: db.SQLWriteTransactionCount() - baselineTx,
		BaselineWallMS:     float64(baselineElapsed.Microseconds()) / 1000,
		HotPath: hotPathSnapshot{
			RemoteAccessWritten:      hotAfter.RemoteAccessWritten - hotBefore.RemoteAccessWritten,
			RemoteAccessSkipped:      hotAfter.RemoteAccessSkipped - hotBefore.RemoteAccessSkipped,
			PresenceClearPerformed:   hotAfter.PresenceClearPerformed - hotBefore.PresenceClearPerformed,
			PresenceClearSkipped:     hotAfter.PresenceClearSkipped - hotBefore.PresenceClearSkipped,
			ProbePlanHit:             hotAfter.ProbePlanHit - hotBefore.ProbePlanHit,
			ProbePlanRebuilt:         hotAfter.ProbePlanRebuilt - hotBefore.ProbePlanRebuilt,
			ProbePlanVersionAssigned: hotAfter.ProbePlanVersionAssigned - hotBefore.ProbePlanVersionAssigned,
		},
		GeneratedAt:        time.Now().UTC(),
		AuditDisabledFleet: true,
		Notes: []string{
			"Steady state only: the warm cycle before the measured window is excluded.",
			"Wall time is not a CPU measurement; take a pprof CPU profile for that.",
			"SQL counters come from the Store, so a statement that changed no row is still counted.",
			"Set OBOARD_PERF_SCALE=xlarge or huge for the 500 and 1000 server shapes.",
		},
	}
	outDir := filepath.Join("..", "..", "..", "dist", "test")
	_ = os.MkdirAll(outDir, 0o755)
	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(outDir, "steady-state-heartbeat-"+spec.Name+".json")
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		t.Logf("could not write report to %s: %v", outPath, err)
	} else {
		t.Logf("wrote %s", outPath)
	}
	t.Logf("steady state %s: reports=%d sql=%d tx=%d alloc=%dB wall=%.1fms (uncached baseline sql=%d tx=%d wall=%.1fms)",
		spec.Describe(), out.Reports, out.SQLStatements, out.SQLWriteTx, out.AllocBytes, out.WallMS,
		out.BaselineSQL, out.BaselineSQLWriteTx, out.BaselineWallMS)

	// The structural property this phase exists for: an unchanged fleet does no
	// repeated database work per heartbeat.
	if statements != 0 {
		t.Fatalf("steady-state heartbeats issued %d SQL statements for an unchanged fleet", statements)
	}
	if transactions != 0 {
		t.Fatalf("steady-state heartbeats started %d write transactions for an unchanged fleet", transactions)
	}
	if out.HotPath.ProbePlanRebuilt != 0 {
		t.Fatalf("steady-state heartbeats rebuilt %d probe plans", out.HotPath.ProbePlanRebuilt)
	}
}

// authorizationRoundReport is the machine-readable artifact for one full fleet
// authorization round.
type authorizationRoundReport struct {
	Spec               perfload.Spec `json:"spec"`
	Servers            int           `json:"servers"`
	Entries            int           `json:"projection_entries"`
	EntriesVisited     int           `json:"entries_visited_per_round"`
	EntriesIfUnindexed int           `json:"entries_visited_per_round_unindexed"`
	IssueSQL           int64         `json:"issue_sql_statements"`
	IssueWriteTx       int64         `json:"issue_sql_write_transactions"`
	IssueWallMS        float64       `json:"issue_wall_ms"`
	IssueAllocBytes    uint64        `json:"issue_alloc_bytes"`
	RenewSQL           int64         `json:"renew_sql_statements"`
	RenewWriteTx       int64         `json:"renew_sql_write_transactions"`
	RenewWallMS        float64       `json:"renew_wall_ms"`
	RenewAllocBytes    uint64        `json:"renew_alloc_bytes"`
	GeneratedAt        time.Time     `json:"generated_at"`
	Notes              []string      `json:"notes"`
}

// TestAuthorizationRoundCost measures one full fleet authorization round: the
// first pass issues, the second renews inside the reuse window. It also asserts
// the structural property P2 exists for - a round visits each credential once
// in total rather than once per server.
func TestAuthorizationRoundCost(t *testing.T) {
	ctx := context.Background()
	spec := perfload.SpecFor(steadyStateScale())
	fixture := newAuthorizationScaleFixture(t, spec.Servers)

	projection, err := fixture.srv.authorizationProjection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	visited := 0
	for _, server := range fixture.servers {
		visited += len(projection.byServer[server.ID])
	}
	unindexed := len(projection.entries) * len(fixture.servers)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	issueSQL := fixture.db.SQLStatementCount()
	issueTx := fixture.db.SQLWriteTransactionCount()
	start := time.Now()
	for _, server := range fixture.servers {
		if _, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err != nil {
			t.Fatal(err)
		}
	}
	issueWall := time.Since(start)
	runtime.ReadMemStats(&after)
	report := authorizationRoundReport{
		Spec: spec, Servers: len(fixture.servers), Entries: len(projection.entries),
		EntriesVisited: visited, EntriesIfUnindexed: unindexed,
		IssueSQL:        fixture.db.SQLStatementCount() - issueSQL,
		IssueWriteTx:    fixture.db.SQLWriteTransactionCount() - issueTx,
		IssueWallMS:     float64(issueWall.Microseconds()) / 1000,
		IssueAllocBytes: after.TotalAlloc - before.TotalAlloc,
	}

	runtime.GC()
	runtime.ReadMemStats(&before)
	renewSQL := fixture.db.SQLStatementCount()
	renewTx := fixture.db.SQLWriteTransactionCount()
	start = time.Now()
	for _, server := range fixture.servers {
		if _, err := fixture.srv.currentAuthorizationLease(ctx, server.ID); err != nil {
			t.Fatal(err)
		}
	}
	renewWall := time.Since(start)
	runtime.ReadMemStats(&after)
	report.RenewSQL = fixture.db.SQLStatementCount() - renewSQL
	report.RenewWriteTx = fixture.db.SQLWriteTransactionCount() - renewTx
	report.RenewWallMS = float64(renewWall.Microseconds()) / 1000
	report.RenewAllocBytes = after.TotalAlloc - before.TotalAlloc
	report.GeneratedAt = time.Now().UTC()
	report.Notes = []string{
		"Fixed-global-credentials axis: one inbound and one user per server.",
		"Issue pass is a cold ledger; renew pass is inside the reuse window.",
		"entries_visited_per_round_unindexed is what the same round cost before the per-server index.",
		"Wall time is not a CPU measurement; take a pprof CPU profile for that.",
	}

	outDir := filepath.Join("..", "..", "..", "dist", "test")
	_ = os.MkdirAll(outDir, 0o755)
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "authorization-round-"+spec.Name+".json"), raw, 0o644); err != nil {
		t.Logf("could not write report: %v", err)
	}
	t.Logf("authorization round %s: servers=%d entries=%d visited=%d (unindexed %d) issue[sql=%d tx=%d wall=%.1fms] renew[sql=%d tx=%d wall=%.1fms]",
		spec.Name, report.Servers, report.Entries, report.EntriesVisited, report.EntriesIfUnindexed,
		report.IssueSQL, report.IssueWriteTx, report.IssueWallMS, report.RenewSQL, report.RenewWriteTx, report.RenewWallMS)

	if visited != len(projection.entries) {
		t.Fatalf("a full round visited %d entries for %d credentials; each credential must be visited once", visited, len(projection.entries))
	}
	if report.RenewWriteTx != 0 {
		t.Fatalf("renewals inside the reuse window opened %d write transactions", report.RenewWriteTx)
	}
}

// TestAccessSyncRecoveryScanCost measures the periodic recovery scan over a
// fleet that is already confirmed. It is the P3 artifact: the scan used to
// recompute every enrolled server unconditionally.
func TestAccessSyncRecoveryScanCost(t *testing.T) {
	ctx := context.Background()
	spec := perfload.SpecFor(steadyStateScale())
	fixture := newAuthorizationScaleFixture(t, spec.Servers)

	fixture.srv.reconcileAuthorizationSync(ctx, true)
	for _, server := range fixture.servers {
		lease, err := fixture.srv.currentAuthorizationLease(ctx, server.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.db.RecordAuthorizationConfirmation(ctx, server.ID, lease.Revision, lease.Sequence, lease.Digest, "boot-1"); err != nil {
			t.Fatal(err)
		}
	}
	fixture.srv.reconcileAuthorizationSync(ctx, true)

	statements := fixture.db.SQLStatementCount()
	transactions := fixture.db.SQLWriteTransactionCount()
	evaluated := fixture.srv.hotPath.authorizationSyncEvaluated.Load()
	skipped := fixture.srv.hotPath.authorizationSyncSkipped.Load()
	start := time.Now()
	const scans = 5
	for range scans {
		fixture.srv.reconcileAuthorizationSync(ctx, true)
	}
	elapsed := time.Since(start)

	report := map[string]any{
		"spec":              spec,
		"servers":           len(fixture.servers),
		"scans":             scans,
		"sql_statements":    fixture.db.SQLStatementCount() - statements,
		"sql_write_tx":      fixture.db.SQLWriteTransactionCount() - transactions,
		"servers_evaluated": fixture.srv.hotPath.authorizationSyncEvaluated.Load() - evaluated,
		"servers_skipped":   fixture.srv.hotPath.authorizationSyncSkipped.Load() - skipped,
		"wall_ms":           float64(elapsed.Microseconds()) / 1000,
		"generated_at":      time.Now().UTC(),
		"notes": []string{
			"Every server is confirmed for the current routing revision and no boundary passed.",
			"servers_evaluated is what the scan actually recomputed; it used to be servers x scans.",
			"Wall time is not a CPU measurement; take a pprof CPU profile for that.",
		},
	}
	outDir := filepath.Join("..", "..", "..", "dist", "test")
	_ = os.MkdirAll(outDir, 0o755)
	if raw, err := json.MarshalIndent(report, "", "  "); err == nil {
		if err := os.WriteFile(filepath.Join(outDir, "access-sync-recovery-"+spec.Name+".json"), raw, 0o644); err != nil {
			t.Logf("could not write report: %v", err)
		}
	}
	t.Logf("recovery scan %s: servers=%d scans=%d evaluated=%d skipped=%d sql=%d tx=%d wall=%.1fms (unoptimized would evaluate %d)",
		spec.Name, len(fixture.servers), scans, report["servers_evaluated"], report["servers_skipped"],
		report["sql_statements"], report["sql_write_tx"], report["wall_ms"], scans*len(fixture.servers))

	if report["servers_evaluated"].(int64) != 0 {
		t.Fatalf("a settled fleet still evaluated %v servers across %d scans", report["servers_evaluated"], scans)
	}
	if report["sql_write_tx"].(int64) != 0 {
		t.Fatalf("a settled recovery scan opened %v write transactions", report["sql_write_tx"])
	}
}
