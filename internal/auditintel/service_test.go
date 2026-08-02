package auditintel

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestEvaluateUserCreatesDeterministicIncidentWithoutQueuingAI(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	user := &model.User{Username: "audit-user", PasswordHash: "x", Role: model.RoleViewer, Status: "active", ProxyUUID: "00000000-0000-4000-8000-000000000001", ProxyPassword: "password", SubscriptionToken: "subscription-token"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "audit-server", ListenIP: "0.0.0.0", Status: model.ServerOnline, ConnectionAuditEnabled: true}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateAIProvider(ctx, &model.AIProvider{ID: "provider", Name: "provider", BaseURL: "http://127.0.0.1", Model: "model", CredentialEncrypted: "encrypted", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	reports := make([]model.ConnectionAuditReport, 0, 100)
	for index := 0; index < 100; index++ {
		reports = append(reports, model.ConnectionAuditReport{ReportID: fmt.Sprintf("report-%03d", index), ServerID: server.ID, UserID: user.ID, SourceIP: "203.0.113.10", SourceCountryCode: "US", Network: "tcp", Destination: fmt.Sprintf("target-%03d.example.com", index), DestinationPort: 1000 + index, ConnectionCount: 10, ActivePeak: 60, StartedAt: now.Add(-2 * time.Second), EndedAt: now})
	}
	if _, err := db.AddConnectionAuditReports(ctx, reports); err != nil {
		t.Fatal(err)
	}
	service := New(db, "test-anonymization-key")
	incident, err := service.EvaluateUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if incident == nil || incident.RuleScore < 50 || incident.Status != "open" {
		t.Fatalf("unexpected incident: %#v", incident)
	}
	second, err := service.EvaluateUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.ID != incident.ID {
		t.Fatalf("fingerprint did not deduplicate incident: first=%#v second=%#v", incident, second)
	}
	items, err := db.ListAuditIncidents(ctx, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("incidents=%#v err=%v", items, err)
	}
	reviews, err := db.ListAuditReviews(ctx, 10)
	if err != nil || len(reviews) != 0 {
		t.Fatalf("rule incident unexpectedly queued AI reviews=%#v err=%v", reviews, err)
	}
}

func TestIncidentBundleIdentityAndBaselineAreDeidentified(t *testing.T) {
	service := &Service{anonymizationKey: []byte("first-key")}
	first := service.anonymizedUserID(1)
	if first == (&Service{anonymizationKey: []byte("second-key")}).anonymizedUserID(1) || first == service.anonymizedUserID(2) {
		t.Fatal("audit user pseudonym is not keyed per installation and user")
	}
	baseline := baselineJSON([]model.AuditFeatureSnapshot{{ID: "secret-snapshot-id", UserID: 42, Window: "15m", FeatureVersion: 1, Fingerprint: "secret-fingerprint", Features: []byte(`{"connection_count":3}`)}})
	text := string(baseline)
	for _, forbidden := range []string{`"user_id"`, "secret-snapshot-id", "secret-fingerprint"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("baseline leaked %q: %s", forbidden, text)
		}
	}
}
