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
