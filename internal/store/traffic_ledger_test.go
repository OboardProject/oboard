package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestTrafficLedgerV2IsIdempotentAfterLostACK(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1 << 30}
	report := v2Report("tr2-lost-ack", server.ID, user.ID, 0, 100, 0, 300)
	first, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{report}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.AcceptedReports) != 1 || first.AcceptedReports[0].Status != trafficAcceptAccepted {
		t.Fatalf("first commit = %#v", first.AcceptedReports)
	}
	second, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{report}})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.AcceptedReports) != 1 || second.AcceptedReports[0].Status != trafficAcceptDuplicate {
		t.Fatalf("retry commit = %#v", second.AcceptedReports)
	}
	stored, err := s.GetTrafficPeriod(ctx, user.ID, period.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Upload != 100 || stored.Download != 300 {
		t.Fatalf("period totals = %+v, want 100/300", stored)
	}
}

func TestTrafficLedgerV2SameRangeDifferentReportIDIsCovered(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1 << 30}
	first := v2Report("tr2-range-a", server.ID, user.ID, 0, 100, 0, 200)
	if _, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{first}}); err != nil {
		t.Fatal(err)
	}
	second := v2Report("tr2-range-b", server.ID, user.ID, 0, 100, 0, 200)
	result, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{second}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AcceptedReports) != 1 || result.AcceptedReports[0].Status != trafficAcceptCovered {
		t.Fatalf("same range = %#v", result.AcceptedReports)
	}
	stored, err := s.GetTrafficPeriod(ctx, user.ID, period.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Upload != 100 || stored.Download != 200 {
		t.Fatalf("period totals = %+v", stored)
	}
}

func TestTrafficLedgerV2RejectsOverlapAndGap(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1 << 30}
	if _, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{v2Report("tr2-base", server.ID, user.ID, 0, 200, 0, 200)}}); err != nil {
		t.Fatal(err)
	}
	overlap, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{v2Report("tr2-overlap", server.ID, user.ID, 150, 300, 150, 300)}})
	if err != nil {
		t.Fatal(err)
	}
	if overlap.AcceptedReports[0].Status != trafficAcceptOverlap {
		t.Fatalf("overlap status = %q", overlap.AcceptedReports[0].Status)
	}
	gap, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{v2Report("tr2-gap", server.ID, user.ID, 300, 400, 300, 400)}})
	if err != nil {
		t.Fatal(err)
	}
	if gap.AcceptedReports[0].Status != trafficAcceptGap {
		t.Fatalf("gap status = %q", gap.AcceptedReports[0].Status)
	}
	stored, err := s.GetTrafficPeriod(ctx, user.ID, period.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Upload != 200 || stored.Download != 200 {
		t.Fatalf("rejected ranges must not bill: %+v", stored)
	}
}

func TestTrafficLedgerV2RecoversFromControllerCheckpoint(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1 << 30}
	if _, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{v2Report("tr2-8g", server.ID, user.ID, 0, 8, 0, 8)}}); err != nil {
		t.Fatal(err)
	}
	result, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{
		ServerID: server.ID,
		Streams: []model.TrafficStreamObservation{{
			Source: "core", StreamID: "ts_core", CounterEpoch: "ce_1", PeriodKey: "2026-08", UserID: user.ID, CurrentUpload: 10, CurrentDownload: 10,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.StreamCheckpoints) != 1 || result.StreamCheckpoints[0].AcceptedUpload != 8 || result.StreamCheckpoints[0].AcceptedDownload != 8 {
		t.Fatalf("checkpoint = %#v", result.StreamCheckpoints)
	}
	follow, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{v2Report("tr2-8-to-10", server.ID, user.ID, 8, 10, 8, 10)}})
	if err != nil {
		t.Fatal(err)
	}
	if follow.AcceptedReports[0].Status != trafficAcceptAccepted {
		t.Fatalf("recovery range = %#v", follow.AcceptedReports)
	}
	stored, err := s.GetTrafficPeriod(ctx, user.ID, period.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Upload != 10 || stored.Download != 10 {
		t.Fatalf("recovered totals = %+v, want only 8 then 2 more", stored)
	}
}

func TestTrafficLedgerV2NewEpochStartsAtZero(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1 << 30}
	first := v2Report("tr2-e1", server.ID, user.ID, 0, 10, 0, 10)
	if _, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{first}}); err != nil {
		t.Fatal(err)
	}
	second := v2Report("tr2-e2", server.ID, user.ID, 0, 1, 0, 1)
	second.CounterEpoch = "ce_2"
	result, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{second}})
	if err != nil {
		t.Fatal(err)
	}
	if result.AcceptedReports[0].Status != trafficAcceptAccepted {
		t.Fatalf("new epoch = %#v", result.AcceptedReports)
	}
	stored, err := s.GetTrafficPeriod(ctx, user.ID, period.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Upload != 11 || stored.Download != 11 {
		t.Fatalf("epoch rollover totals = %+v", stored)
	}
}

func TestTrafficLedgerV2PeriodRolloverUsesIndependentEpochs(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	august := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08-01", StartedAt: time.Now().Add(-40 * 24 * time.Hour), EndsAt: time.Now().Add(-10 * 24 * time.Hour), Limit: 1 << 30}
	first := v2Report("tr2-aug", server.ID, user.ID, 0, 100, 0, 200)
	first.PeriodKey = august.PeriodKey
	first.CounterEpoch = "ce_aug"
	if _, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: august}, Reports: []model.TrafficReport{first}}); err != nil {
		t.Fatal(err)
	}
	september := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-09-01", StartedAt: time.Now().Add(-10 * 24 * time.Hour), EndsAt: time.Now().Add(20 * 24 * time.Hour), Limit: 1 << 30}
	second := v2Report("tr2-sep", server.ID, user.ID, 0, 40, 0, 60)
	second.PeriodKey = september.PeriodKey
	second.CounterEpoch = "ce_sep"
	result, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: september}, Reports: []model.TrafficReport{second}})
	if err != nil {
		t.Fatal(err)
	}
	if result.AcceptedReports[0].Status != trafficAcceptAccepted {
		t.Fatalf("new period = %#v", result.AcceptedReports)
	}
	oldPeriod, err := s.GetTrafficPeriod(ctx, user.ID, august.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if oldPeriod.Upload != 100 || oldPeriod.Download != 200 {
		t.Fatalf("august totals = %+v", oldPeriod)
	}
	newPeriod, err := s.GetTrafficPeriod(ctx, user.ID, september.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if newPeriod.Upload != 40 || newPeriod.Download != 60 {
		t.Fatalf("september totals = %+v", newPeriod)
	}
}

func TestTrafficLedgerV2PeriodMigrationInheritsSameEpochCheckpoint(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	source := model.TrafficPeriod{UserID: user.ID, PeriodKey: "old-cycle", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1 << 30}
	first := v2Report("tr2-old", server.ID, user.ID, 0, 100, 0, 100)
	first.PeriodKey = source.PeriodKey
	if _, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: source}, Reports: []model.TrafficReport{first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `insert into traffic_period_transitions(user_id,source_period_key,target_period_key,created_at) values(?,?,?,?)`, user.ID, source.PeriodKey, "new-cycle", now()); err != nil {
		t.Fatal(err)
	}
	target := model.TrafficPeriod{UserID: user.ID, PeriodKey: "new-cycle", StartedAt: source.StartedAt, EndsAt: source.EndsAt, Limit: 1 << 30}
	second := v2Report("tr2-migrated", server.ID, user.ID, 100, 120, 100, 130)
	second.PeriodKey = target.PeriodKey
	result, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: target}, Reports: []model.TrafficReport{second}})
	if err != nil {
		t.Fatal(err)
	}
	if result.AcceptedReports[0].Status != trafficAcceptAccepted {
		t.Fatalf("migrated range = %#v", result.AcceptedReports)
	}
	stored, err := s.GetTrafficPeriod(ctx, user.ID, target.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Upload != 20 || stored.Download != 30 {
		t.Fatalf("migrated increment = %+v, want only 20/30", stored)
	}
}

func TestZeroLeaseAllocationDoesNotExceedGlobalRemaining(t *testing.T) {
	s, ctx, user, _ := openTrafficLedgerFixture(t)
	defer s.Close()
	servers := make([]model.Server, 3)
	for i := range servers {
		servers[i] = model.Server{Name: "lease-" + string(rune('a'+i)), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
		if err := s.CreateServer(ctx, &servers[i]); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.EnsureTrafficLeaseAllocation(ctx, servers[0].ID, user.ID, "2026-08", 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.EnsureTrafficLeaseAllocation(ctx, servers[1].ID, user.ID, "2026-08", 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	third, err := s.EnsureTrafficLeaseAllocation(ctx, servers[2].ID, user.ID, "2026-08", 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if first.RemainingBytes+second.RemainingBytes+third.RemainingBytes != 50 {
		t.Fatalf("unconsumed leases = %d+%d+%d", first.RemainingBytes, second.RemainingBytes, third.RemainingBytes)
	}
	if third.RemainingBytes != 0 {
		t.Fatalf("third lease = %#v, want 0", third)
	}
}

func TestTrafficLedgerV2MigratesFromPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "traffic-ledger.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := &model.User{Username: "migrate-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111199", ProxyPassword: "pass", SubscriptionToken: "migrate-sub"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "migrate-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1000}
	report := model.TrafficReport{ReportID: "legacy-v1", ServerID: server.ID, UserID: user.ID, PeriodKey: period.PeriodKey, Upload: 40, Download: 60, StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now()}
	if accepted, err := s.AddTrafficReports(ctx, []model.TrafficReport{report}, period); err != nil || len(accepted) != 1 {
		t.Fatalf("seed v1 report accepted=%v err=%v", accepted, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`drop index if exists idx_traffic_reports_range`,
		`drop index if exists idx_traffic_reports_v2_range`,
		`drop table if exists traffic_counter_streams`,
		`drop table if exists traffic_reconciliation_events`,
		`alter table traffic_reports drop column protocol_version`,
		`alter table traffic_reports drop column counter_source`,
		`alter table traffic_reports drop column stream_id`,
		`alter table traffic_reports drop column counter_epoch`,
		`alter table traffic_reports drop column from_upload_bytes`,
		`alter table traffic_reports drop column to_upload_bytes`,
		`alter table traffic_reports drop column from_download_bytes`,
		`alter table traffic_reports drop column to_download_bytes`,
		`alter table traffic_reports drop column accept_status`,
		`alter table traffic_leases drop column lease_revision`,
		`alter table traffic_leases drop column state`,
		`alter table traffic_leases drop column issued_at`,
		`alter table traffic_leases drop column last_synced_at`,
		`alter table traffic_leases drop column valid_until`,
		`alter table traffic_leases drop column released_at`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stored, err := s.GetTrafficPeriod(ctx, user.ID, period.PeriodKey)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Upload != 40 || stored.Download != 60 {
		t.Fatalf("migrated period = %+v", stored)
	}
	var protocol int
	if err := s.db.QueryRowContext(ctx, `select protocol_version from traffic_reports where report_id='legacy-v1'`).Scan(&protocol); err != nil {
		t.Fatal(err)
	}
	if protocol != 1 {
		t.Fatalf("legacy protocol_version = %d", protocol)
	}
	result, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{v2Report("tr2-after-migrate", server.ID, user.ID, 0, 5, 0, 5)}})
	if err != nil || result.AcceptedReports[0].Status != trafficAcceptAccepted {
		t.Fatalf("post-migration v2 commit = %#v err=%v", result.AcceptedReports, err)
	}
	var indexName string
	if err := s.db.QueryRowContext(ctx, `select name from sqlite_master where type='index' and name='idx_traffic_reports_range'`).Scan(&indexName); err != nil || indexName != "idx_traffic_reports_range" {
		t.Fatalf("current range index = %q err=%v", indexName, err)
	}
}

func TestTrafficLedgerCoversHistoricalProtocolVersionTwoRows(t *testing.T) {
	s, ctx, user, server := openTrafficLedgerFixture(t)
	defer s.Close()
	period := model.TrafficPeriod{UserID: user.ID, PeriodKey: "2026-08", StartedAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour), Limit: 1 << 30}
	first := v2Report("tr-historical", server.ID, user.ID, 0, 40, 0, 60)
	if _, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{first}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `update traffic_reports set protocol_version=2 where report_id=?`, first.ReportID); err != nil {
		t.Fatal(err)
	}
	retry := v2Report("tr-historical-retry", server.ID, user.ID, 0, 40, 0, 60)
	result, err := s.CommitTrafficLedger(ctx, TrafficLedgerCommit{ServerID: server.ID, Periods: map[int64]model.TrafficPeriod{user.ID: period}, Reports: []model.TrafficReport{retry}})
	if err != nil || len(result.AcceptedReports) != 1 || result.AcceptedReports[0].Status != trafficAcceptCovered {
		t.Fatalf("historical protocol_version=2 cover = %#v err=%v", result.AcceptedReports, err)
	}
}

func TestTrafficLeaseAllocationRaceDoesNotOverAllocate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lease-race.sqlite")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	third, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	ctx := context.Background()
	user := &model.User{Username: "race-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111188", ProxyPassword: "pass", SubscriptionToken: "race-sub"}
	if err := first.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	servers := make([]model.Server, 3)
	stores := []*Store{first, second, third}
	for i := range servers {
		servers[i] = model.Server{Name: "race-" + string(rune('a'+i)), ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
		if err := first.CreateServer(ctx, &servers[i]); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	allocations := make(chan TrafficLeaseAllocation, 3)
	var wg sync.WaitGroup
	for i, database := range stores {
		i, database := i, database
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			allocation, err := database.EnsureTrafficLeaseAllocation(ctx, servers[i].ID, user.ID, "2026-08", 100, 0)
			if err != nil {
				t.Errorf("allocation %d: %v", i, err)
				return
			}
			allocations <- allocation
		}()
	}
	close(start)
	wg.Wait()
	close(allocations)
	var total int64
	for allocation := range allocations {
		total += allocation.RemainingBytes
	}
	if total != 100 {
		t.Fatalf("concurrent remaining total = %d", total)
	}
}

func openTrafficLedgerFixture(t *testing.T) (*Store, context.Context, *model.User, *model.Server) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := &model.User{Username: "ledger-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111177", ProxyPassword: "pass", SubscriptionToken: "ledger-sub"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "ledger-server", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	return s, ctx, user, server
}

func v2Report(id string, serverID, userID, fromUp, toUp, fromDown, toDown int64) model.TrafficReport {
	return model.TrafficReport{
		ReportID: id, ServerID: serverID, UserID: userID, PeriodKey: "2026-08",
		CounterSource: "core", StreamID: "ts_core", CounterEpoch: "ce_1",
		FromUploadBytes: fromUp, ToUploadBytes: toUp, FromDownloadBytes: fromDown, ToDownloadBytes: toDown,
		StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now(),
	}
}
