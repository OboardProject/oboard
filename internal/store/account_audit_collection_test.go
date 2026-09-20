package store

import (
	"context"
	"github.com/OboardProject/oboard/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestAuditCollectionRestartExpiryAndRevision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "collection.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u := &model.User{Username: "collection", Role: model.RoleViewer, Status: "active"}
	if err := s.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c, err := s.AuditCollection(ctx)
	if err != nil || c.Mode != "light" {
		t.Fatalf("default %+v %v", c, err)
	}
	c.Diagnostics = []model.AuditDiagnostic{{Scope: "user", ID: u.ID, Until: at.Add(time.Hour)}}
	saved, err := s.SetAuditCollection(ctx, c, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SetAuditCollection(ctx, c, at); err == nil {
		t.Fatal("stale revision accepted")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err = s.AuditCollection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Revision != saved.Revision || c.Effective(1, at).Mode != "diagnostic" {
		t.Fatal(c)
	}
	if e := c.Effective(1, at.Add(time.Hour)); e.Mode != "light" || e.DiagnosticUntil != nil {
		t.Fatal(e)
	}
}
func TestAuditCollectionLimitsAndScope(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := model.AuditCollectionConfig{Mode: "light", Diagnostics: []model.AuditDiagnostic{{Scope: "node", ID: 5, Until: at.Add(time.Hour)}}}
	if err := ValidateAuditCollection(c, at); err != nil {
		t.Fatal(err)
	}
	if c.Effective(4, at).Mode != "light" || c.Effective(5, at).Mode != "diagnostic" {
		t.Fatal("scope leak")
	}
	c.Diagnostics[0].Until = at.Add(time.Hour + time.Second)
	if ValidateAuditCollection(c, at) == nil {
		t.Fatal("unbounded TTL")
	}
	c.Diagnostics = nil
	for i := 1; i <= 9; i++ {
		c.Diagnostics = append(c.Diagnostics, model.AuditDiagnostic{Scope: "user", ID: int64(i), Until: at.Add(time.Minute)})
	}
	if ValidateAuditCollection(c, at) == nil {
		t.Fatal("capacity not enforced")
	}
	c.Diagnostics = nil
	c.Mode = "strict"
	if ValidateAuditCollection(c, at) == nil {
		t.Fatal("sensitivity used as collection")
	}
}
func TestAuditCollectionDetailBudget(t *testing.T) {
	s, node, user := newMaintenanceTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.SetAuditCollection(ctx, model.AuditCollectionConfig{Mode: "standard"}, at); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `with recursive n(x) as (select 1 union all select x+1 from n where x<2000) insert into connection_audit_reports(report_id,server_id,user_id,source_ip,network,connection_count,collection_started_at,collection_ended_at,started_at,ended_at,created_at) select 'budget-'||x,?,?,'203.0.113.1','tcp',1,?,?,?,?,? from n`, node.ID, user.ID, at, at, at, at, at); err != nil {
		t.Fatal(err)
	}
	allowed, err := AuditDetailAllowedTx(ctx, tx, node.ID, user.ID, at)
	if err != nil || allowed {
		t.Fatalf("capacity allowed=%v err=%v", allowed, err)
	}
}

func TestAuditDetailBatchBudget(t *testing.T) {
	s, node, user := newMaintenanceTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC()
	if _, err := s.SetAuditCollection(ctx, model.AuditCollectionConfig{Mode: "standard"}, at); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `with recursive n(x) as (select 1 union all select x+1 from n where x<1999) insert into connection_audit_reports(report_id,server_id,user_id,source_ip,network,connection_count,collection_started_at,collection_ended_at,started_at,ended_at,created_at) select 'batch-'||x,?,?,'203.0.113.1','tcp',1,?,?,?,?,? from n`, node.ID, user.ID, at, at, at, at, at); err != nil {
		t.Fatal(err)
	}
	b, err := newAuditDetailBudget(ctx, tx, at)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := b.allowed(ctx, tx, node.ID, user.ID); err != nil || !ok {
		t.Fatalf("initial allowance: %v %v", ok, err)
	}
	// A nil transaction proves repeated allowance checks do not issue SQL.
	for range 500 {
		if ok, err := b.allowed(ctx, nil, node.ID, user.ID); err != nil || !ok {
			t.Fatalf("cached allowance: %v %v", ok, err)
		}
	}
	b.inserted(user.ID)
	if b.globalRemaining != model.AuditDetailGlobalLimit-2000 || b.userRemaining[user.ID] != 0 {
		t.Fatalf("incorrect remaining budget: %+v", b)
	}
	if ok, err := b.allowed(ctx, nil, node.ID, user.ID); err != nil || ok {
		t.Fatalf("exhausted allowance: %v %v", ok, err)
	}
	light := &auditDetailBudget{}
	if ok, err := light.allowed(ctx, nil, node.ID, user.ID); err != nil || ok || light.globalLoaded {
		t.Fatalf("light counted details: %v %v", ok, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	report := model.ConnectionAuditReport{ReportID: "batch-1", ServerID: node.ID, UserID: user.ID, SourceIP: "203.0.113.1", Network: "tcp", ConnectionCount: 1, CollectionStartedAt: at, CollectionEndedAt: at, StartedAt: at, EndedAt: at}
	reports := []model.ConnectionAuditReport{report}
	report.ReportID = "batch-new"
	reports = append(reports, report, report)
	report.ReportID = "batch-overflow"
	reports = append(reports, report)
	result, err := s.AddConnectionAuditReportsResult(ctx, reports)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AcceptedReportIDs) != 4 || len(result.InsertedReportIDs) != 1 || result.InsertedReportIDs[0] != "batch-new" || len(result.DiscardedDetailIDs) != 1 || result.DiscardedDetailIDs[0] != "batch-overflow" {
		t.Fatalf("duplicate consumed budget or was discarded: %+v", result)
	}
}

func TestAuditCollectionLightDoesNotPersistDetails(t *testing.T) {
	s, _ := accountAuditFixture(t)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	allowed, err := AuditDetailAllowedTx(ctx, tx, 1, 1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || allowed {
		t.Fatalf("light detail allowed=%v err=%v", allowed, err)
	}
}
