package controller

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// auditRiskEvaluationFixture retains historical multi-network device reports.
func auditRiskEvaluationFixture(t testing.TB) (*store.Store, *Server, int64) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := newTestServer(db, "audit-cost-secret", "")
	server := &model.Server{Name: "audit-cost-node", AgentID: "audit-cost-agent", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "audit-cost-user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reports := make([]model.ConnectionAuditReport, 0, 8)
	for i := range 8 {
		startedAt := now.Add(-90 * time.Second)
		reports = append(reports, model.ConnectionAuditReport{
			ReportID: fmt.Sprintf("cost-%d", i), ServerID: server.ID, UserID: user.ID, DeviceIDHash: "device-cost", CredentialEpoch: 1,
			SourceIP: fmt.Sprintf("12.%d.0.1", i+1), RouteID: fmt.Sprintf("cost-route-%d", i), SourceCountryCode: "CN", SourceCountry: "CN", SourceISP: fmt.Sprintf("ISP-%d", i), GeoDatabaseRevision: "test",
			Network: "tcp", ConnectionCount: 1, ClosedCount: 1, DurationTotalMS: 90000, DurationMaxMS: 90000, DurationGT20SCount: 1,
			UploadBytes: 1024, DownloadBytes: 1024, PayloadFirstAt: startedAt, PayloadLastAt: now, PresenceSequence: uint64(i + 1), ActivePeak: 1, BucketCapacity: 4096,
			CollectionStartedAt: startedAt, CollectionEndedAt: now, StartedAt: startedAt, EndedAt: now,
		})
	}
	if _, err := db.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	return db, srv, user.ID
}

func TestHistoricalConnectionAuditOnlyMarksAccountDirty(t *testing.T) {
	db, srv, userID := auditRiskEvaluationFixture(t)
	ctx := context.Background()

	enableTestAudit(t, db)
	if err := srv.evaluateConnectionAuditRisks(ctx, userID); err != nil {
		t.Fatal(err)
	}

	before := db.SQLStatementCount()
	if err := srv.evaluateConnectionAuditRisks(ctx, userID); err != nil {
		t.Fatal(err)
	}
	cost := db.SQLStatementCount() - before
	if cost > 6 {
		t.Fatalf("dirty marking cost %d statements, want <= 6 without history scans", cost)
	}
	work, err := db.ListAccountAuditWork(ctx, 0, 10)
	if err != nil || len(work) != 1 || work[0].UserID != userID {
		t.Fatalf("durable account work = %#v, %v", work, err)
	}
}

// The lease path used to open two write transactions per reissue (desired
// evaluation + sequence allocation). Both now commit as one.
func TestAuthorizationLeaseReissueOpensOneWriteTransaction(t *testing.T) {
	ctx := context.Background()
	db, srv, server, _, _ := hotPathFixture(t)
	lease, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Revision != 1 || lease.Sequence != 1 {
		t.Fatalf("first lease = %+v", lease)
	}
	// Age the issued lease past the reuse window so the next call reissues.
	state := srv.serverAuthorizationLeaseState(server.ID)
	state.mu.Lock()
	if state.lease == nil {
		state.mu.Unlock()
		t.Fatal("issued lease missing from the lease cache")
	}
	state.issuedAt = time.Now().UTC().Add(-time.Minute)
	state.mu.Unlock()

	before := db.SQLWriteTransactionCount()
	reissued, err := srv.currentAuthorizationLease(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reissued.Revision != lease.Revision || reissued.Sequence != lease.Sequence+1 {
		t.Fatalf("reissue = %+v, want same revision and next sequence", reissued)
	}
	if writes := db.SQLWriteTransactionCount() - before; writes != 1 {
		t.Fatalf("reissue opened %d write transactions, want 1", writes)
	}
}
