package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func newDriftRecoveryFixture(t *testing.T) (*store.Store, *Server, *model.Server) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "drift-node", AgentID: "drift-agent", AgentTokenHash: security.HashSecret("drift-token"), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	return db, srv, server
}

// syncedAtVersion drives a server into the synced state at one config version.
func syncedAtVersion(t *testing.T, db *store.Store, serverID int64, revision uint64, configVersion, taskID int64, digest string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.MarkConfigurationSyncPending(ctx, revision, []int64{serverID}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, serverID, revision); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncQueued(ctx, serverID, revision, configVersion, taskID, digest); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, serverID, configVersion, true, ""); err != nil {
		t.Fatal(err)
	}
}

// An Agent reports drift on every heartbeat until the recovery lands. Only the
// first report may open a recovery; the rest must not reopen the one already in
// flight, or the fleet rebuilds the same deployment forever.
func TestRepeatedDriftReportsOpenOneRecovery(t *testing.T) {
	db, _, server := newDriftRecoveryFixture(t)
	ctx := context.Background()
	syncedAtVersion(t, db, server.ID, 31, 301, 11, "expected-digest")

	opened := 0
	for i := 0; i < 20; i++ {
		created, err := db.MarkConfigurationSyncDrift(ctx, server.ID, 31)
		if err != nil {
			t.Fatal(err)
		}
		if created {
			opened++
		}
		// The reconciler claims the pending row and queues one deployment.
		if i == 0 {
			if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 31); err != nil || !ok {
				t.Fatalf("claim=%v err=%v", ok, err)
			}
			if err := db.MarkConfigurationSyncQueued(ctx, server.ID, 31, 302, 12, "recovery-digest"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if opened != 1 {
		t.Fatalf("repeated drift reports opened %d recoveries, want 1", opened)
	}
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "queued" || state.LastConfigVersion != 302 {
		t.Fatalf("in-flight recovery was reopened: %#v", state)
	}
}

// Once the recovery deployment succeeds the server is synced and a matching
// convergence report must leave it alone.
func TestDriftRecoverySettlesWithoutLooping(t *testing.T) {
	db, srv, server := newDriftRecoveryFixture(t)
	ctx := context.Background()
	syncedAtVersion(t, db, server.ID, 31, 301, 11, "expected-digest")
	if _, err := db.MarkConfigurationSyncDrift(ctx, server.ID, 31); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.ClaimConfigurationSync(ctx, server.ID, 31); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	if err := db.MarkConfigurationSyncQueued(ctx, server.ID, 31, 302, 12, "recovery-digest"); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkConfigurationSyncResult(ctx, server.ID, 302, true, ""); err != nil {
		t.Fatal(err)
	}

	srv.reconcileAgentAppliedState(ctx, server.ID, model.HealthReport{Status: model.ServerOnline, AppliedConfigVersion: 302, AppliedConfigDigest: "recovery-digest", Timestamp: time.Now().UTC()})
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "synced" {
		t.Fatalf("converged server did not stay synced: %#v", state)
	}
}

// A convergence report that was produced before the deployment succeeded still
// names the older config version. Acting on it would redeploy a server that has
// just converged.
func TestStaleConvergenceReportDoesNotReopenSyncedServer(t *testing.T) {
	db, srv, server := newDriftRecoveryFixture(t)
	ctx := context.Background()
	syncedAtVersion(t, db, server.ID, 31, 301, 11, "expected-digest")

	srv.reconcileAgentAppliedState(ctx, server.ID, model.HealthReport{Status: model.ServerOnline, AppliedConfigVersion: 300, AppliedConfigDigest: "previous-digest", Timestamp: time.Now().UTC()})
	state, err := db.ConfigurationSyncState(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State != "synced" {
		t.Fatalf("stale convergence report reopened a synced server: %#v", state)
	}
}

// Accounting reports are not a configuration trigger. A traffic or connection
// audit report must never advance the desired state or queue a deployment,
// even when Controller rejects part of the batch.
func TestAgentAccountingReportsNeverTriggerDeployment(t *testing.T) {
	fixture := newAuditReportFixture(t)
	ctx := context.Background()
	before, err := fixture.db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeStates, err := fixture.db.ListAllConfigurationSyncStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeTasks := agentTaskCount(t, fixture.db, fixture.server.ID)

	poisoned := fixture.validItem("audit-poison")
	poisoned["network"] = "sctp"
	postAgentAudit(t, fixture.handler, fixture.server.AgentID, "audit-token", []map[string]any{fixture.validItem("audit-good"), poisoned}, http.StatusOK)

	after, err := fixture.db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("accounting report advanced the configuration revision: %d -> %d", before, after)
	}
	afterStates, err := fixture.db.ListAllConfigurationSyncStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterStates) != len(beforeStates) {
		t.Fatalf("accounting report created %d configuration sync state(s)", len(afterStates)-len(beforeStates))
	}
	if got := agentTaskCount(t, fixture.db, fixture.server.ID); got != beforeTasks {
		t.Fatalf("accounting report queued %d agent task(s)", got-beforeTasks)
	}
}

func TestAgentTrafficReportNeverTriggersDeployment(t *testing.T) {
	db, server, inbound, user, h := trafficLedgerHTTPFixture(t)
	defer db.Close()
	ctx := context.Background()
	before, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeTasks := agentTaskCount(t, db, server.ID)

	postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-no-deploy", 0, 100, 0, 200), http.StatusOK)
	stored, err := db.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	stored.Status = "inactive"
	if err := db.UpdateUser(ctx, stored); err != nil {
		t.Fatal(err)
	}
	postAgentTraffic(t, h, server.AgentID, "token-a", ledgerTrafficBody(user.ID, inbound.ID, "tr-no-deploy-2", 100, 200, 200, 300), http.StatusOK)

	after, err := db.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("traffic report advanced the configuration revision: %d -> %d", before, after)
	}
	if got := agentTaskCount(t, db, server.ID); got != beforeTasks {
		t.Fatalf("traffic report queued %d agent task(s)", got-beforeTasks)
	}
	states, err := db.ListAllConfigurationSyncStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("traffic report created configuration sync state: %#v", states)
	}
}

func agentTaskCount(t *testing.T, db *store.Store, serverID int64) int {
	t.Helper()
	tasks, err := db.ListTasks(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, task := range tasks {
		if task.ServerID == serverID {
			count++
		}
	}
	return count
}
