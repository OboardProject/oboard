package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestLatencyProbeTaskRoundTripAndAssignment(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "latency-probe-tasks.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first := &model.Server{Name: "task-edge-1"}
	second := &model.Server{Name: "task-edge-2"}
	if err := db.CreateServer(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, second); err != nil {
		t.Fatal(err)
	}

	task := model.LatencyProbeTask{Province: " 广东 ", Carrier: " 中国电信 ", Enabled: true, ServerIDs: []int64{second.ID, first.ID, first.ID, 0, first.ID + second.ID + 999}}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if task.ID == 0 || task.Name != "广东 · 中国电信" || task.Province != "广东" || task.Carrier != "中国电信" || task.IntervalSeconds != 300 {
		t.Fatalf("created task = %#v", task)
	}
	if len(task.ServerIDs) != 2 || task.ServerIDs[0] != first.ID || task.ServerIDs[1] != second.ID {
		t.Fatalf("assigned servers = %#v", task.ServerIDs)
	}

	duplicate := model.LatencyProbeTask{Name: "广东 · 中国电信", Province: "广东", Carrier: "中国移动", Enabled: true}
	if err := db.SaveLatencyProbeTask(ctx, &duplicate); err == nil {
		t.Fatal("duplicate task name was accepted")
	}
	if err := db.SaveLatencyProbeTask(ctx, &model.LatencyProbeTask{Name: "太快", Province: "广东", Carrier: "中国移动", IntervalSeconds: 5}); err == nil {
		t.Fatal("out-of-range interval was accepted")
	}
	if err := db.SaveLatencyProbeTask(ctx, &model.LatencyProbeTask{Name: "缺目标", Province: "广东"}); err == nil {
		t.Fatal("task without a carrier was accepted")
	}

	other := model.LatencyProbeTask{Name: "浙江联通", Province: "浙江", Carrier: "中国联通", IntervalSeconds: 60, Enabled: false, ServerIDs: []int64{first.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &other); err != nil {
		t.Fatal(err)
	}
	all, err := db.ListLatencyProbeTasks(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("tasks = %#v err=%v", all, err)
	}
	assigned, err := db.ListLatencyProbeTasksForServer(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(assigned) != 1 || assigned[0].ID != task.ID || assigned[0].ServerIDs[0] != first.ID {
		t.Fatalf("disabled task leaked into the server plan: %#v", assigned)
	}
	task.IntervalSeconds = 900
	task.ServerIDs = []int64{second.ID}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	reloaded, err := db.GetLatencyProbeTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.IntervalSeconds != 900 || len(reloaded.ServerIDs) != 1 || reloaded.ServerIDs[0] != second.ID {
		t.Fatalf("updated task = %#v", reloaded)
	}
	if remaining, err := db.ListLatencyProbeTasksForServer(ctx, first.ID); err != nil || len(remaining) != 0 {
		t.Fatalf("unassigned server still runs tasks: %#v err=%v", remaining, err)
	}

	if err := db.DeleteLatencyProbeTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteLatencyProbeTask(ctx, task.ID); err == nil {
		t.Fatal("deleting a missing task reported success")
	}
	var links int
	if err := db.db.QueryRowContext(ctx, `select count(*) from latency_probe_task_servers where task_id=?`, task.ID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("task assignment rows survived deletion: %d", links)
	}
}

// TestLatencyProbeTasksMigrateFromPerServerRegions upgrades from the real previous
// state: per-server `regions_json` selections with no probe task tables.
func TestLatencyProbeTasksMigrateFromPerServerRegions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "latency-probe-task-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	shared := &model.Server{Name: "legacy-shared", LatencyProbeEnabled: true}
	solo := &model.Server{Name: "legacy-solo", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, shared); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateServer(ctx, solo); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`update server_latency_probe_settings set interval_seconds=?,regions_json=? where server_id=?`, 600, `[{"province":"广东","carrier":"中国电信"},{"province":"浙江","carrier":"中国联通"}]`, shared.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`update server_latency_probe_settings set interval_seconds=?,regions_json=? where server_id=?`, 120, `[{"province":"广东","carrier":"中国电信"}]`, solo.ID); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`delete from app_settings where key='migration.controller-db-20260905-latency-probe-tasks'`,
		`delete from latency_probe_task_servers`,
		`delete from latency_probe_tasks`,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tasks, err := db.ListLatencyProbeTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("migrated tasks = %#v", tasks)
	}
	byName := map[string]model.LatencyProbeTask{}
	for _, task := range tasks {
		byName[task.Name] = task
	}
	telecom, ok := byName["广东 · 中国电信"]
	if !ok || telecom.Province != "广东" || telecom.Carrier != "中国电信" || !telecom.Enabled {
		t.Fatalf("shared target task = %#v", telecom)
	}
	// The shared target keeps the shortest legacy cadence of its servers.
	if telecom.IntervalSeconds != 120 {
		t.Fatalf("shared target interval = %d, want 120", telecom.IntervalSeconds)
	}
	if len(telecom.ServerIDs) != 2 || telecom.ServerIDs[0] != shared.ID || telecom.ServerIDs[1] != solo.ID {
		t.Fatalf("shared target servers = %#v", telecom.ServerIDs)
	}
	unicom, ok := byName["浙江 · 中国联通"]
	if !ok || unicom.IntervalSeconds != 600 || len(unicom.ServerIDs) != 1 || unicom.ServerIDs[0] != shared.ID {
		t.Fatalf("single target task = %#v", unicom)
	}

	var remaining int
	if err := db.db.QueryRowContext(ctx, `select count(*) from server_latency_probe_settings where regions_json<>'[]'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("legacy region selections survived the migration: %d", remaining)
	}
	var marker string
	if err := db.db.QueryRowContext(ctx, `select value from app_settings where key='migration.controller-db-20260905-latency-probe-tasks'`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker != "completed" {
		t.Fatalf("migration marker = %q", marker)
	}

	// Reopening must not duplicate tasks or reset an operator's later edits.
	telecom.IntervalSeconds = 3600
	if err := db.SaveLatencyProbeTask(ctx, &telecom); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	again, err := db.ListLatencyProbeTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 {
		t.Fatalf("re-running the migration changed the task set: %#v", again)
	}
	for _, task := range again {
		if task.Name == "广东 · 中国电信" && task.IntervalSeconds != 3600 {
			t.Fatalf("re-running the migration reverted an operator edit: %#v", task)
		}
	}
}

func TestLatencyProbeResultsCarryTaskIdentity(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "latency-probe-task-results.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "task-results", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.September, 5, 0, 0, 0, 0, time.UTC)
	report := model.LatencyProbeResultReport{ReportID: "task-report", ResourceVersion: "v1", CheckedAt: from.Add(30 * time.Second), Items: []model.LatencyProbeResult{
		{ProbeID: "t7-0", Kind: "regional", TaskID: 7, TaskName: "华南主线", Mode: "tcp", Province: "广东", Carrier: "中国电信", Host: "192.0.2.1", IP: "192.0.2.1", Port: 80, Available: true, LatencyMS: 10, MinLatencyMS: 10, P95LatencyMS: 10, SampleCount: 3, SuccessCount: 3},
		{ProbeID: "t8-0", Kind: "regional", TaskID: 8, TaskName: "华南备线", Mode: "tcp", Province: "广东", Carrier: "中国电信", Host: "192.0.2.2", IP: "192.0.2.2", Port: 80, Available: true, LatencyMS: 40, MinLatencyMS: 40, P95LatencyMS: 40, SampleCount: 3, SuccessCount: 3},
	}}
	if err := db.SaveLatencyProbeResults(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListLatencyProbeResults(ctx, server.ID, 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("results = %#v err=%v", items, err)
	}
	for _, item := range items {
		if item.TaskID == 0 || item.TaskName == "" {
			t.Fatalf("stored result lost its task identity: %#v", item)
		}
	}

	// Two tasks against the same province and carrier must stay separate series.
	points, _, err := db.ListRegionalLatencyPoints(ctx, server.ID, from, from.Add(time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("regional points = %#v, want one per task", points)
	}
	seen := map[int64]string{}
	for _, point := range points {
		seen[point.TaskID] = point.TaskName
	}
	if seen[7] != "华南主线" || seen[8] != "华南备线" {
		t.Fatalf("task series = %#v", seen)
	}
}

// TestLatencyProbeLegacyResultsFallBackToTargetLabel covers rows written before
// task identity existed: the chart must still group them by province and carrier.
func TestLatencyProbeLegacyResultsFallBackToTargetLabel(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "latency-probe-legacy-results.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "legacy-results", LatencyProbeEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, time.September, 5, 0, 0, 0, 0, time.UTC)
	ts := from.Add(10 * time.Second).Format(time.RFC3339Nano)
	if _, err := db.db.Exec(`insert into server_latency_probe_results(server_id,resource_version,probe_id,kind,mode,province,carrier,host,ip,port,available,latency_ms,sample_count,success_count,checked_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		server.ID, "legacy-v1", "广东-中国电信-0", "regional", "tcp", "广东", "中国电信", "192.0.2.1", "192.0.2.1", 80, 1, 21, 3, 3, ts, ts); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListLatencyProbeResults(ctx, server.ID, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("results = %#v err=%v", items, err)
	}
	if items[0].TaskID != 0 || items[0].TaskName != "广东 · 中国电信" {
		t.Fatalf("legacy result label = %#v", items[0])
	}
	points, _, err := db.ListRegionalLatencyPoints(ctx, server.ID, from, from.Add(time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].TaskID != 0 || points[0].TaskName != "广东 · 中国电信" {
		t.Fatalf("legacy regional points = %#v", points)
	}
}

func TestGetLatencyProbeTaskMissing(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "latency-probe-task-missing.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.GetLatencyProbeTask(context.Background(), 4242); err != sql.ErrNoRows {
		t.Fatalf("missing task error = %v, want sql.ErrNoRows", err)
	}
}
