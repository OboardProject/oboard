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

// auditRiskEvaluationFixture is one enrolled server with one reporting user
// whose device carries concurrent multi-network traffic: the exact shape the
// coalesced audit risk queue evaluates after every accepted report batch.
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

// One coalesced evaluation used to run the shared-route scan three times and
// the risk-window report load twice (device action, notification, incident
// detail). The evaluation must now load that evidence once.
func TestConnectionAuditRiskEvaluationSharesEvidence(t *testing.T) {
	db, srv, userID := auditRiskEvaluationFixture(t)
	ctx := context.Background()

	// Warm every revision-keyed cache the pipeline consults so the measured
	// statements are the evaluation itself, not first-use cache builds.
	if err := srv.evaluateConnectionAuditRisks(ctx, userID); err != nil {
		t.Fatal(err)
	}

	before := db.SQLStatementCount()
	if err := srv.evaluateConnectionAuditRisks(ctx, userID); err != nil {
		t.Fatal(err)
	}
	cost := db.SQLStatementCount() - before
	// The evaluation still pays: probe episode rebuild, 24h overview batch,
	// subscription risk, incident detail, snapshot writes, notification
	// settings. The bound keeps the shared evidence from silently regressing
	// back to per-call full scans (the old path cost ~3x more statements).
	if cost > 60 {
		t.Fatalf("risk evaluation cost %d statements, want <= 60", cost)
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
