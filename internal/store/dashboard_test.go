package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// dashboardTimelineFixture seeds a realistic task history: old noise rows with
// the lowest ids, then a spread deployment, interleaved deployment/probe
// batches, batchable pairs, and non-batchable singles. Task ids correlate with
// activity recency like real history, so the adaptive reduced fetch converges
// on the newest pages instead of scanning the whole table.
func dashboardTimelineFixture(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	server := &model.Server{Name: "tl-node", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	second := &model.Server{Name: "tl-node-2", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, second); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	sequence := 0
	insert := func(configVersion int64, serverID int64, taskType string, createdAt, updatedAt time.Time) {
		sequence++
		task := &model.AgentTask{
			ServerID: serverID, Type: taskType, PayloadJSON: "{}", Status: "succeeded",
			ConfigVersion: configVersion, Nonce: fmt.Sprintf("n-%d", sequence),
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := s.SetTaskStateForTest(ctx, task.ID, "succeeded", updatedAt); err != nil {
			t.Fatal(err)
		}
		raw := createdAt.UTC().Format(time.RFC3339Nano)
		if _, err := s.db.ExecContext(ctx, `update agent_tasks set created_at=? where id=?`, raw, task.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Old noise with the lowest ids: enough rows to exceed the 300-row
	// reference timeline so the reduced fetch is the only thing being tested.
	for index := 0; index < 320; index++ {
		insert(0, server.ID, model.AgentTaskTypeCollectLogs, base.Add(-48*time.Hour+time.Duration(index)*time.Second), base.Add(-48*time.Hour+time.Duration(index)*time.Second))
	}
	// A deployment whose members are spread across ids; its updated_at (-16h)
	// is recent enough to hold the sixth timeline slot ahead of the
	// apply_core_config singles.
	insert(11, server.ID, model.AgentTaskTypeApplyDeployment, base.Add(-30*time.Hour), base.Add(-16*time.Hour))
	insert(11, second.ID, model.AgentTaskTypeApplyDeployment, base.Add(-30*time.Hour+time.Minute), base.Add(-16*time.Hour+time.Minute))
	insert(11, server.ID, model.AgentTaskTypeApplyDeployment, base.Add(-30*time.Hour+2*time.Minute), base.Add(-16*time.Hour+2*time.Minute))
	// Deployment v9: 8 servers, created together but ids interleaved with the
	// probe batch below by inserting in alternating order.
	for index := 0; index < 8; index++ {
		serverID := server.ID
		if index%2 == 0 {
			serverID = second.ID
		}
		insert(9, serverID, model.AgentTaskTypeApplyDeployment, base.Add(time.Duration(index)*time.Minute), base.Add(time.Duration(index)*time.Minute+30*time.Second))
		// Interleave a probe_inbounds task with an older created_at but a
		// newer updated_at so id order and activity order diverge.
		insert(0, server.ID, model.AgentTaskTypeProbeInbounds, base.Add(-10*time.Hour+time.Duration(index)*time.Second), base.Add(5*time.Hour))
	}
	// Deployment v10 supersedes v9: two tasks, both newer.
	insert(10, server.ID, model.AgentTaskTypeApplyDeployment, base.Add(6*time.Hour), base.Add(6*time.Hour+time.Minute))
	insert(10, second.ID, model.AgentTaskTypeApplyDeployment, base.Add(6*time.Hour+10*time.Second), base.Add(6*time.Hour+2*time.Minute))
	// Batchable pairs: two detect_mtu tasks in the same two-minute window and
	// one update_agent_config pair that must stay one batch.
	insert(0, server.ID, model.AgentTaskTypeDetectMTU, base.Add(7*time.Hour), base.Add(7*time.Hour+5*time.Second))
	insert(0, second.ID, model.AgentTaskTypeDetectMTU, base.Add(7*time.Hour+30*time.Second), base.Add(7*time.Hour+15*time.Second))
	insert(0, server.ID, model.AgentTaskTypeUpdateAgentConfig, base.Add(8*time.Hour), base.Add(8*time.Hour+time.Minute))
	insert(0, second.ID, model.AgentTaskTypeUpdateAgentConfig, base.Add(8*time.Hour+5*time.Second), base.Add(8*time.Hour+2*time.Minute))
	// Non-batchable singles with older activity: they must never enter the top
	// six groups.
	for index := 0; index < 7; index++ {
		insert(0, server.ID, model.AgentTaskTypeApplyCoreConfig, base.Add(-time.Duration(24-index)*time.Hour), base.Add(-time.Duration(17+index)*time.Hour))
	}
}

// TestDashboardTaskTimelineParity proves ListDashboardTaskTimeline returns
// enough rows that grouping them yields exactly the same most recent six
// timeline groups as grouping the full 300-row reference timeline.
func TestDashboardTaskTimelineParity(t *testing.T) {
	s, err := Open(t.TempDir() + "/oboard.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dashboardTimelineFixture(t, s)
	ctx := context.Background()

	reference, err := s.ListTaskTimeline(ctx, 300)
	if err != nil {
		t.Fatal(err)
	}
	reduced, err := s.ListDashboardTaskTimeline(ctx, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(reduced) >= len(reference) {
		t.Fatalf("ListDashboardTaskTimeline returned %d rows, reference %d; expected a strict reduction", len(reduced), len(reference))
	}
	want := timelineGroups(reference)
	if len(want) > 6 {
		want = want[:6]
	}
	got := timelineGroups(reduced)
	if len(got) > 6 {
		got = got[:6]
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("timeline group parity mismatch:\n got=%+v\nwant=%+v", got, want)
	}
}

// TestDashboardSummaryAggregationParity proves the aggregated Dashboard query
// produces the same summary as the previous eleven single-row queries.
func TestDashboardSummaryAggregationParity(t *testing.T) {
	s, err := Open(t.TempDir() + "/oboard.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	servers := []*model.Server{
		{Name: "online", ListenIP: "0.0.0.0", Status: model.ServerOnline},
		{Name: "offline", ListenIP: "0.0.0.0", Status: model.ServerOffline},
		{Name: "degraded", ListenIP: "0.0.0.0", Status: model.ServerDegraded},
		{Name: "unknown", ListenIP: "0.0.0.0", Status: model.ServerUnknown},
	}
	for _, server := range servers {
		if err := s.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	active := &model.User{Username: "active", PasswordHash: "x", Status: "active"}
	inactive := &model.User{Username: "inactive", PasswordHash: "x", Status: "disabled"}
	for _, user := range []*model.User{active, inactive} {
		if err := s.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	for _, period := range []struct {
		user   *model.User
		key    string
		start  time.Time
		upload int64
	}{
		{active, "2026-08", now.Add(-24 * time.Hour), 1000},
		{active, "2026-09", now.Add(-2 * time.Hour), 500},
		{inactive, "2026-09", now.Add(-3 * time.Hour), 250},
	} {
		if _, err := s.db.ExecContext(ctx, `insert into traffic_periods(user_id,period_key,started_at,ends_at,upload_bytes,download_bytes,traffic_limit_bytes,state,updated_at) values(?,?,?,?,?,?,0,'active',?)`,
			period.user.ID, period.key, period.start.UTC().Format(time.RFC3339Nano), period.start.Add(24*time.Hour).UTC().Format(time.RFC3339Nano), period.upload, period.upload, now.UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	tasks := []struct {
		server *model.Server
		status string
		config int64
	}{
		{servers[0], "pending", 2},
		{servers[1], "running", 2},
		{servers[2], "failed", 2},
		{servers[3], "rollback_failed", 3},
		{servers[0], "succeeded", 4},
	}
	for index, task := range tasks {
		item := &model.AgentTask{ServerID: task.server.ID, Type: model.AgentTaskTypeApplyDeployment, PayloadJSON: "{}", Status: task.status, ConfigVersion: task.config, Nonce: fmt.Sprintf("sum-%d", index)}
		if err := s.CreateTask(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := legacyDashboardSummary(ctx, t, s)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregated Dashboard differs from legacy queries:\n got=%+v\nwant=%+v", got, want)
	}
}

// legacyDashboardSummary is the previous eleven-query Dashboard implementation
// kept as the reference for the aggregation parity test.
func legacyDashboardSummary(ctx context.Context, t *testing.T, s *Store) model.DashboardSummary {
	t.Helper()
	var d model.DashboardSummary
	queries := []struct {
		query string
		dest  []any
	}{
		{`select count(*) from servers`, []any{&d.ServersTotal}},
		{`select count(*) from servers where status='online'`, []any{&d.ServersOnline}},
		{`select count(*) from servers where status='offline'`, []any{&d.ServersOffline}},
		{`select count(*) from servers where status='degraded'`, []any{&d.ServersDegraded}},
		{`select count(*) from users`, []any{&d.UsersTotal}},
		{`select count(*) from users where status='active'`, []any{&d.UsersActive}},
		{`select coalesce(sum(p.upload_bytes),0),coalesce(sum(p.download_bytes),0)
			from traffic_periods p
			join (select user_id,max(started_at) as started_at from traffic_periods group by user_id) latest
			  on latest.user_id=p.user_id and latest.started_at=p.started_at`, []any{&d.TrafficUpload, &d.TrafficDownload}},
		{`select count(*) from agent_tasks where status='pending'`, []any{&d.PendingTasks}},
		{`select count(*) from agent_tasks where status='running'`, []any{&d.RunningTasks}},
		{`select count(*) from agent_tasks where status in ('failed','rollback_failed')`, []any{&d.FailedTasks}},
		{`select coalesce(max(config_version),0) from agent_tasks`, []any{&d.LastConfigVersion}},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query).Scan(item.dest...); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// TestDashboardIndexesUsedByTimeoutAndTraffic proves the new indexes serve the
// timeout scan, the latest-period traffic join, and the timeline projection.
func TestDashboardIndexesUsedByTimeoutAndTraffic(t *testing.T) {
	s, err := Open(t.TempDir() + "/oboard.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	indexes := map[string]string{
		"idx_tasks_status_updated":         `select id from agent_tasks where status='pending' and updated_at < '2026-01-01T00:00:00Z' order by id`,
		"idx_tasks_config_version":         `select max(config_version) from agent_tasks`,
		"idx_traffic_periods_user_started": `select user_id,max(started_at) from traffic_periods group by user_id`,
	}
	for index, query := range indexes {
		plan, err := queryPlan(ctx, s, query)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range plan {
			if strings.Contains(row, "USING INDEX "+index) || strings.Contains(row, "USING COVERING INDEX "+index) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("query did not use index %s:\n%s", index, plan)
		}
	}
}

func queryPlan(ctx context.Context, s *Store, query string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `explain query plan `+query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id, parent, notUsed, detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			return nil, err
		}
		out = append(out, detail)
	}
	return out, rows.Err()
}
