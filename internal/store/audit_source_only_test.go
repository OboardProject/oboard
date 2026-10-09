package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestAuditSourceOnlyMigratesPreviousData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	enableHistoricalAuditDetails(t, s)
	user := &model.User{Username: "audit", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active"}
	if err := s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "audit", ListenIP: "0.0.0.0", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute)
	report := meaningfulConnectionReport("previous", server.ID, user.ID, "", "198.51.100.8", "US", "ISP", at, at.Add(time.Second))
	if _, err := s.AddConnectionAuditReports(ctx, []model.ConnectionAuditReport{report}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "update connection_audit_reports set destination='private.example',destination_port=443,outbound_tag='exit-secret',outbound_type='direct' where report_id='previous'"); err != nil {
		t.Fatal(err)
	}
	provider := &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "https://api.example.com", Model: "model", CredentialEncrypted: "encrypted", Enabled: true}
	if err := s.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"target-review", "connection-review", "source-review", "job-review"} {
		review, job := auditReviewFixture(user.ID, provider.ID, id, id+"-job")
		review.ID = id
		job.ReviewID = id
		review.EvidenceTypes = []string{"connection"}
		payload := json.RawMessage("{\"source_ip\":\"198.51.100.8\"}")
		switch id {
		case "target-review":
			review.EvidenceTypes = append(review.EvidenceTypes, "destination")
		case "connection-review":
			payload = json.RawMessage("{\"recent_connections\":[{\"source_ip\":\"198.51.100.8\",\"destination\":\"private.example\"}]}")
		case "job-review":
			job.Input = json.RawMessage("{\"context\":{\"destination\":\"private.example\"}}")
		}
		evidence := []model.AuditReviewEvidence{{Ref: id + "-evidence", Kind: "context", UserID: &user.ID, Payload: payload}}
		if err := s.CreateAuditReview(ctx, review, evidence, []model.AuditReviewJob{job}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := &model.AuditFeatureSnapshot{ID: "old-target", UserID: user.ID, Window: "15m", WindowStartedAt: at, WindowEndedAt: at.Add(time.Second), FeatureVersion: 1, RuleScore: 60, Fingerprint: "old-target", Features: json.RawMessage("{\"source_ip_count\":1,\"destination_count\":100,\"destination_port_count\":30}")}
	if err := s.CreateAuditFeatureSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAuditIncident(ctx, &model.AuditIncident{ID: "old-incident", UserID: user.ID, Status: "open", Severity: "high", RuleScore: 60, Fingerprint: "old-target", LatestSnapshotID: snapshot.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		s, err = Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var source, destination, outbound, outboundType string
		var port int
		if err := s.db.QueryRowContext(ctx, "select source_ip,destination,destination_port,outbound_tag,outbound_type from connection_audit_reports where report_id='previous'").Scan(&source, &destination, &port, &outbound, &outboundType); err != nil {
			t.Fatal(err)
		}
		if source != report.SourceIP || destination != "" || port != 0 || outbound != "" || outboundType != "" {
			t.Fatalf("unexpected migrated source=%q destination=%q port=%d outbound=%q type=%q", source, destination, port, outbound, outboundType)
		}
		detail, err := s.ConnectionAuditUserDetail(ctx, user.ID, 24, DefaultAuditPolicy())
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"destination", "outbound", "private.example", "exit-secret"} {
			if strings.Contains(string(body), key) {
				t.Fatalf("audit detail retained %q: %s", key, body)
			}
		}
		if len(detail.Recent) != 1 || detail.Recent[0].SourceIP != source {
			t.Fatalf("source evidence lost: %#v", detail)
		}
		for _, id := range []string{"target-review", "connection-review", "job-review"} {
			if _, err := s.GetAuditReview(ctx, id); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("old review %s still exists: %v", id, err)
			}
			var count int
			if err := s.db.QueryRowContext(ctx, "select count(*) from ai_audit_review_jobs where review_id=?", id).Scan(&count); err != nil || count != 0 {
				t.Fatalf("old jobs retained count=%d err=%v", count, err)
			}
		}
		if _, err := s.GetAuditReview(ctx, "source-review"); err != nil {
			t.Fatalf("source review lost: %v", err)
		}
		if _, err := s.GetAuditIncident(ctx, "old-incident"); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("destination-based incident retained: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
