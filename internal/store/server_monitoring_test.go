package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestServerMonitoringTargetsAndPacketCounts(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "monitoring.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "monitor", LatencyProbeEnabled: true}
	other := &model.Server{Name: "other", LatencyProbeEnabled: true}
	for _, item := range []*model.Server{server, other} {
		if err := db.CreateServer(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	task := model.LatencyProbeTask{Name: "selected", Method: model.LatencyProbeModeTCP, Address: "1.1.1.1", Port: 443, Enabled: true, IntervalSeconds: 120, ServerIDs: []int64{server.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	for i := range 25 {
		at := base.Add(time.Duration(i) * time.Minute)
		insertLatencyProbeResult(t, db, server.ID, fmt.Sprintf("public-%d", i), "public", 0, "", "tcp", "", "", true, 0, 3, 3, at)
		insertLatencyProbeResult(t, db, server.ID, fmt.Sprintf("task-a-%d", i), "custom", task.ID, task.Name, "tcp", "", "", true, 40, 3, 2, at)
		insertLatencyProbeResult(t, db, server.ID, fmt.Sprintf("task-b-%d", i), "custom", task.ID, task.Name, "tcp", "", "", true, 80, 2, 1, at)
		insertLatencyProbeResult(t, db, other.ID, fmt.Sprintf("other-%d", i), "public", 0, "", "tcp", "", "", false, 0, 3, 0, at)
	}
	read := func() map[int64]*model.ServerMonitoringDisplay {
		t.Helper()
		items, err := db.ListServers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AttachServerMonitoringDisplays(ctx, items); err != nil {
			t.Fatal(err)
		}
		out := map[int64]*model.ServerMonitoringDisplay{}
		for _, item := range items {
			out[item.ID] = item.MonitoringDisplay
		}
		return out
	}
	initial := read()[server.ID]
	if len(initial.Samples) != 20 || !initial.Samples[0].CheckedAt.Equal(base.Add(5*time.Minute)) || initial.Samples[0].LatencyMS == nil || *initial.Samples[0].LatencyMS != 0 {
		t.Fatalf("public readings = %#v", initial)
	}
	server.MonitoringTargetTaskID = task.ID
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	selected := read()
	point := selected[server.ID].Samples[19]
	if selected[server.ID].Name != "selected" || len(selected[server.ID].Samples) != 20 || point.SampleCount != 5 || point.SuccessCount != 3 || point.LatencyMS == nil || *point.LatencyMS != 160.0/3 {
		t.Fatalf("selected readings = %#v, point=%#v", selected, point)
	}
	if selected[other.ID].Samples[19].LatencyMS != nil || selected[other.ID].Samples[19].SuccessCount != 0 {
		t.Fatal("failed public probe became available")
	}
	task.Enabled = false
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if disabled := read()[server.ID]; disabled.Enabled || len(disabled.Samples) != 0 {
		t.Fatalf("disabled task fell back to public: %#v", disabled)
	}
	task.Enabled = true
	task.ServerIDs = []int64{other.ID}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if unassigned := read()[server.ID]; unassigned.Enabled || len(unassigned.Samples) != 0 {
		t.Fatal("unassigned task still displayed")
	}
	if err := db.DeleteLatencyProbeTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if deleted := read()[server.ID]; deleted.Enabled || len(deleted.Samples) != 0 {
		t.Fatal("deleted task fell back to public")
	}
}

func TestServerMonitoringMigratesPreviousSchemaAndPersistsOnRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "previous", LatencyProbeEnabled: true, LatencyProbeMode: model.LatencyProbeModeICMP, LatencyProbeIntervalSeconds: 300}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task := model.LatencyProbeTask{Name: "persisted", Method: model.LatencyProbeModeTCP, Address: "1.1.1.1", Port: 443, Enabled: true, IntervalSeconds: 120, ServerIDs: []int64{server.ID}}
	if err := db.SaveLatencyProbeTask(ctx, &task); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`alter table server_latency_probe_settings drop column monitoring_target_task_id`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MonitoringTargetTaskID != 0 || stored.Name != "previous" || stored.LatencyProbeMode != model.LatencyProbeModeICMP || stored.LatencyProbeIntervalSeconds != 300 {
		t.Fatalf("migration rewrote settings: %#v", stored)
	}
	stored.MonitoringTargetTaskID = task.ID
	if err := db.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stored, err = db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MonitoringTargetTaskID != task.ID {
		t.Fatal("selection lost on restart")
	}
	stored.MonitoringTargetTaskID = 0
	if err := db.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	stored, err = db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MonitoringTargetTaskID != 0 {
		t.Fatal("could not reset to public")
	}
}
