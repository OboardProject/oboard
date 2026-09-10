package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func openHealthSampleStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "health-sample.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createHealthSampleServer(t *testing.T, db *Store) *model.Server {
	t.Helper()
	server := &model.Server{Name: "sample-node", AgentID: "sample-agent", Status: model.ServerOnline}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	return server
}

func healthWindow(at time.Time) model.ServerTrafficWindow {
	start := at.UTC().Truncate(24 * time.Hour)
	return model.ServerTrafficWindow{Key: start.Format("2006-01-02"), Start: start, End: start.Add(24 * time.Hour)}
}

// TestMetricSampleAdmissionSkipsEarlyReports proves a health report that cannot
// produce a sample yet does not issue the conditional INSERT, and that a report
// which is genuinely due still samples.
func TestMetricSampleAdmissionSkipsEarlyReports(t *testing.T) {
	ctx := context.Background()
	db := openHealthSampleStore(t)
	server := createHealthSampleServer(t, db)
	interval := db.metricSampleMinInterval
	if interval <= 0 {
		interval = defaultMetricSampleMinInterval
	}
	base := time.Now().UTC().Truncate(time.Second)

	first, err := db.ApplyHealthReport(ctx, server.ID, model.HealthReport{Status: model.ServerOnline, Timestamp: base}, healthWindow(base))
	if err != nil {
		t.Fatal(err)
	}
	if !first.SampleInserted {
		t.Fatal("the first report must record a sample")
	}

	statements := db.SQLStatementCount()
	for i := range 20 {
		at := base.Add(time.Duration(i+1) * time.Second)
		result, err := db.ApplyHealthReport(ctx, server.ID, model.HealthReport{Status: model.ServerOnline, Timestamp: at}, healthWindow(at))
		if err != nil {
			t.Fatal(err)
		}
		if result.SampleInserted {
			t.Fatalf("report %d inserted a sample %s after the last one", i, at.Sub(base))
		}
	}
	// Each of those reports still does its own health and telemetry work; the
	// point is that none of them asked the database about a sample.
	perReport := (db.SQLStatementCount() - statements) / 20
	if perReport > 4 {
		t.Fatalf("an early report issued %d statements, want no sample statement on top of the health path", perReport)
	}

	// A due report samples again.
	due := base.Add(interval + time.Second)
	result, err := db.ApplyHealthReport(ctx, server.ID, model.HealthReport{Status: model.ServerOnline, Timestamp: due}, healthWindow(due))
	if err != nil {
		t.Fatal(err)
	}
	if !result.SampleInserted {
		t.Fatalf("a report %s after the last sample was suppressed", due.Sub(base))
	}
}

// TestMetricSampleAdmissionDefersToTheDatabase proves the cache only ever
// suppresses, never admits: a fresh process, a changed policy, and a report
// timestamped before the recorded window all fall through to the database.
func TestMetricSampleAdmissionDefersToTheDatabase(t *testing.T) {
	db := openHealthSampleStore(t)
	at := time.Now().UTC()
	interval := 2 * time.Minute

	// No record at all: a restart must not suppress anything.
	if !db.metricSampleDue(1, at, interval) {
		t.Fatal("a server with no admission record must reach the database")
	}
	db.metricSampleMinInterval = interval
	db.noteMetricSampleInserted(1, at)
	if db.metricSampleDue(1, at.Add(time.Second), interval) {
		t.Fatal("a report inside the interval should be suppressed")
	}
	// A changed sampling policy invalidates the recorded window.
	if !db.metricSampleDue(1, at.Add(time.Second), 30*time.Second) {
		t.Fatal("a changed sampling interval must invalidate the record")
	}
	// A report from before the record was built is never suppressed by it.
	if !db.metricSampleDue(1, at.Add(-time.Hour), interval) {
		t.Fatal("a late or backwards-clock report must reach the database")
	}
	// A deleted server leaves nothing behind.
	db.ForgetMetricSampleAdmission(1)
	if !db.metricSampleDue(1, at.Add(time.Second), interval) {
		t.Fatal("a forgotten server must reach the database")
	}
}

// TestHealthReportKeepsCounterHistoryUnderSampleAdmission proves the sampling
// admission cache never touches traffic accounting: every counter step is still
// accumulated, a counter reset does not lose the increment that preceded it,
// and a period rollover starts from zero.
func TestHealthReportKeepsCounterHistoryUnderSampleAdmission(t *testing.T) {
	ctx := context.Background()
	db := openHealthSampleStore(t)
	server := createHealthSampleServer(t, db)
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	report := func(at time.Time, up, down uint64) {
		t.Helper()
		if _, err := db.ApplyHealthReport(ctx, server.ID, model.HealthReport{
			Status: model.ServerOnline, Timestamp: at,
			NetworkTotalUploadBytes: up, NetworkTotalDownloadBytes: down,
		}, healthWindow(base)); err != nil {
			t.Fatal(err)
		}
	}
	report(base, 1_000, 2_000)
	// Growth in small steps: every step must be accumulated even though almost
	// all of these reports record no sample.
	for i := 1; i <= 10; i++ {
		report(base.Add(time.Duration(i)*time.Second), uint64(1_000+i*100), uint64(2_000+i*200))
	}
	// The counter resets to zero (Agent restart) and then grows again. The
	// increment before the reset must not be lost.
	report(base.Add(11*time.Second), 0, 0)
	report(base.Add(12*time.Second), 50, 60)

	fresh, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	// 10 steps of 100 up / 200 down accumulate before the reset; the post-reset
	// growth adds its own delta from the new baseline.
	if fresh.TrafficUploadBytes != 1_000+50 || fresh.TrafficDownloadBytes != 2_000+60 {
		t.Fatalf("counter history lost across the reset: up=%d down=%d", fresh.TrafficUploadBytes, fresh.TrafficDownloadBytes)
	}

	// A new billing period starts from zero rather than carrying the old total.
	next := base.Add(48 * time.Hour)
	if _, err := db.ApplyHealthReport(ctx, server.ID, model.HealthReport{
		Status: model.ServerOnline, Timestamp: next, NetworkTotalUploadBytes: 100, NetworkTotalDownloadBytes: 120,
	}, healthWindow(next)); err != nil {
		t.Fatal(err)
	}
	rolled, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolled.TrafficUploadBytes != 0 || rolled.TrafficDownloadBytes != 0 {
		t.Fatalf("period rollover carried the previous total: up=%d down=%d", rolled.TrafficUploadBytes, rolled.TrafficDownloadBytes)
	}
}

// TestHealthReportFoldsCapabilityIntoOneTransaction proves a changed capability
// is written by the health transaction itself rather than a second commit, and
// that passing no capability writes nothing.
func TestHealthReportFoldsCapabilityIntoOneTransaction(t *testing.T) {
	ctx := context.Background()
	db := openHealthSampleStore(t)
	server := createHealthSampleServer(t, db)
	at := time.Now().UTC()

	transactions := db.SQLWriteTransactionCount()
	report := model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityExec}, LocalMode: model.RemoteAccessModeStandard}
	if _, err := db.ApplyHealthReportWithOptions(ctx, server.ID, model.HealthReport{Status: model.ServerOnline, Timestamp: at}, healthWindow(at), ApplyHealthReportOptions{RemoteAccess: &report}); err != nil {
		t.Fatal(err)
	}
	if used := db.SQLWriteTransactionCount() - transactions; used != 1 {
		t.Fatalf("a health report with a capability change opened %d write transactions, want 1", used)
	}
	stored, err := db.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Capabilities) != 1 || stored.Capabilities[0] != model.RemoteAccessCapabilityExec {
		t.Fatalf("capability was not written by the health transaction: %+v", stored)
	}

	// No capability passed means the caller already established it is unchanged.
	transactions = db.SQLWriteTransactionCount()
	statements := db.SQLStatementCount()
	later := at.Add(time.Second)
	if _, err := db.ApplyHealthReportWithOptions(ctx, server.ID, model.HealthReport{Status: model.ServerOnline, Timestamp: later}, healthWindow(later), ApplyHealthReportOptions{}); err != nil {
		t.Fatal(err)
	}
	if used := db.SQLWriteTransactionCount() - transactions; used != 1 {
		t.Fatalf("an unchanged report opened %d write transactions, want 1", used)
	}
	after, err := db.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.UpdatedAt.Equal(stored.UpdatedAt) {
		t.Fatalf("an unchanged report rewrote the capability row: %s -> %s", stored.UpdatedAt, after.UpdatedAt)
	}
	_ = statements
}

// TestHealthReportFailureDoesNotAdvanceSampleAdmission proves a rolled-back
// health transaction never suppresses the next sample.
func TestHealthReportFailureDoesNotAdvanceSampleAdmission(t *testing.T) {
	ctx := context.Background()
	db := openHealthSampleStore(t)
	at := time.Now().UTC()
	// A server that does not exist fails inside the transaction.
	if _, err := db.ApplyHealthReport(ctx, 987654, model.HealthReport{Status: model.ServerOnline, Timestamp: at}, healthWindow(at)); err == nil {
		t.Fatal("expected the report for a missing server to fail")
	}
	interval := db.metricSampleMinInterval
	if interval <= 0 {
		interval = defaultMetricSampleMinInterval
	}
	if !db.metricSampleDue(987654, at, interval) {
		t.Fatal("a failed report advanced the sampling admission window")
	}
}
