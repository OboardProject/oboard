package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func explainPlan(t *testing.T, db *Store, query string, args ...any) string {
	t.Helper()
	rows, err := db.db.QueryContext(context.Background(), "explain query plan "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, notUsed int64
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plan
}

// The dashboard counts tasks by status on every panel load. As one aggregate it
// scanned the whole table - 635 ms on a production Controller with 28,520 rows.
func TestDashboardTaskCountsUseTheStatusIndex(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "dash-node", AgentID: "dash-agent", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"pending": 3, "running": 2, "failed": 5, "rollback_failed": 1, "succeeded": 7}
	for status, n := range want {
		for i := 0; i < n; i++ {
			task := &model.AgentTask{ServerID: server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: "{}", Status: status, ConfigVersion: int64(i + 1), Nonce: status + string(rune('a'+i))}
			if err := db.CreateTask(ctx, task); err != nil {
				t.Fatal(err)
			}
		}
	}

	summary, err := db.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PendingTasks != 3 || summary.RunningTasks != 2 {
		t.Fatalf("pending=%d running=%d, want 3 and 2", summary.PendingTasks, summary.RunningTasks)
	}
	// failed and rollback_failed are reported together, as before.
	if summary.FailedTasks != 6 {
		t.Fatalf("failed=%d, want 6 (5 failed plus 1 rollback_failed)", summary.FailedTasks)
	}
	if summary.LastConfigVersion == 0 {
		t.Fatal("last config version was not reported")
	}

	plan := explainPlan(t, db, `select count(*) from agent_tasks where status='failed'`)
	if !strings.Contains(plan, "idx_tasks_status_updated") {
		t.Fatalf("task counting does not use the status index:\n%s", plan)
	}
}

// Finding the newest task that carries an SSH plan ran with "type in (?,?)",
// which cost SQLite the index ordering: it evaluated json_type over every
// deployment payload for the server before sorting. Pinned to one type the
// index supplies the order and the walk stops at the first match.
func TestLatestSSHDeploymentLookupStopsAtTheFirstMatch(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "ssh-node", AgentID: "ssh-agent", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	add := func(taskType, status string, version int64, withSSH bool) {
		payload := `{"forward":{}}`
		if withSSH {
			payload = `{"ssh_inbounds":{"entries":[]}}`
		}
		task := &model.AgentTask{ServerID: server.ID, Type: taskType, PayloadJSON: payload, Status: status, ConfigVersion: version, Nonce: taskType + status + time.Now().Format("150405.000000000") + string(rune(version))}
		if err := db.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	// Older plans of both types, then plenty of newer tasks with no plan at
	// all: the answer must still be the newest task that carries one.
	add(model.AgentTaskTypeApplyCoreConfig, "succeeded", 10, true)
	add(model.AgentTaskTypeApplyDeployment, "failed", 20, true)
	add(model.AgentTaskTypeApplyCoreConfig, "succeeded", 30, true)
	for version := int64(40); version < 60; version++ {
		add(model.AgentTaskTypeApplyDeployment, "succeeded", version, false)
	}

	version, status, err := db.LatestSSHDeploymentTaskTermination(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if version != 30 || status != "succeeded" {
		t.Fatalf("latest ssh deployment = version %d status %q, want 30 succeeded", version, status)
	}

	// The newest plan being an apply_deployment must win over an older
	// apply_core_config, which is what merging the two per-type answers is for.
	add(model.AgentTaskTypeApplyDeployment, "failed", 70, true)
	version, status, err = db.LatestSSHDeploymentTaskTermination(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if version != 70 || status != "failed" {
		t.Fatalf("latest ssh deployment = version %d status %q, want 70 failed", version, status)
	}

	plan := explainPlan(t, db, `select config_version,id,status from agent_tasks
		where server_id=? and type=? and json_type(payload_json,'$.ssh_inbounds')='object'
		order by config_version desc,id desc limit 1`, server.ID, model.AgentTaskTypeApplyDeployment)
	if !strings.Contains(plan, "idx_tasks_server_type_version") {
		t.Fatalf("ssh plan lookup does not use the server/type/version index:\n%s", plan)
	}
	if strings.Contains(plan, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("ssh plan lookup still sorts every candidate:\n%s", plan)
	}
}
