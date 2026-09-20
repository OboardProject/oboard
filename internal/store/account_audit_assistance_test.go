package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/model"
)

func TestAccountAuditAssistanceSnapshotQueue(t *testing.T) {
	s, user := accountAuditFixture(t)
	ctx := context.Background()
	snapshot := accountAuditSnapshot(user, 10, 80)
	raw, _ := json.Marshal(snapshot)
	result, err := s.db.Exec(`INSERT INTO account_audit_events(user_id,risk_type,cycle,status,score,first_seen_at,last_seen_at,snapshot) VALUES(?,'activity',1,'pending',80,?,?,?)`, user, snapshot.AsOf.Format(time.RFC3339Nano), snapshot.AsOf.Format(time.RFC3339Nano), string(raw))
	if err != nil {
		t.Fatal(err)
	}
	event, _ := result.LastInsertId()
	unavailable, err := s.QueueAccountAuditAssistance(ctx, user, event, 1, user, "missing", snapshot.AsOf)
	if err != nil || unavailable.Status != "unavailable" || unavailable.ReviewID != "" {
		t.Fatalf("missing provider must be explicit unavailable: %+v %v", unavailable, err)
	}
	provider := &model.AIProvider{ID: "assistance", Name: "Assistance", DefaultModel: "m", Enabled: true}
	if err = s.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	endpoint := &model.AIProviderEndpoint{ID: "assist-endpoint", ProviderID: provider.ID, Name: "Primary", BaseURL: "https://example.com/v1", APIStyle: "openai_responses", AuthMode: "none", Enabled: true}
	if err = s.CreateAIProviderEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	digest := aiprovider.ConfigDigest(aiprovider.RuntimeEndpoint{BaseURL: endpoint.BaseURL, APIStyle: aiprovider.APIStyle(endpoint.APIStyle), AuthMode: endpoint.AuthMode}, "m")
	cap := &model.AIProviderCapability{ProviderProfileVersion: model.AuditProviderProfileVersion, ProviderID: provider.ID, EndpointID: endpoint.ID, Model: "m", ConfigDigest: digest, ConnectivityOK: true, AuthenticationOK: true, TextSupported: true, AuditReady: true, StructuredOutput: model.AuditProviderStructuredPromptedJSON, OutputMode: model.AuditOutputModeText}
	if err = s.UpsertAIProviderEndpointCapability(ctx, cap); err != nil {
		t.Fatal(err)
	}
	for revision := int64(1); revision <= 4; revision++ {
		snapshot.Features.DataRevision = uint64(revision)
		encoded, _ := json.Marshal(snapshot)
		if _, err := s.db.Exec(`UPDATE account_audit_events SET snapshot=? WHERE id=?`, string(encoded), event); err != nil {
			t.Fatal(err)
		}
		_, err = s.db.Exec(`INSERT INTO account_audit_workflow(event_id,revision) VALUES(?,?) ON CONFLICT(event_id) DO UPDATE SET revision=excluded.revision`, event, revision)
		if err != nil {
			t.Fatal(err)
		}
		queued, err := s.QueueAccountAuditAssistance(ctx, user, event, revision, user, provider.ID, snapshot.AsOf)
		if revision == 4 {
			if err == nil {
				t.Fatal("daily budget not enforced")
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		cached, err := s.QueueAccountAuditAssistance(ctx, user, event, revision, user, provider.ID, snapshot.AsOf)
		if err != nil || cached.ReviewID != queued.ReviewID {
			t.Fatalf("cache: %+v %v", cached, err)
		}
		jobs, err := s.ListAuditReviewJobs(ctx, queued.ReviewID, true)
		if err != nil || len(jobs) != 1 {
			t.Fatalf("jobs: %v %v", jobs, err)
		}
		var input map[string]json.RawMessage
		if err = json.Unmarshal(jobs[0].Input, &input); err != nil {
			t.Fatal(err)
		}
		if len(input["context"]) == 0 || len(input["pack"]) == 0 {
			t.Fatal("missing bounded snapshot")
		}
		if other, err := s.GetAccountAuditAssistance(ctx, user+1, event, revision); !errors.Is(err, sql.ErrNoRows) || other != nil {
			t.Fatalf("cross-account read: %v %v", other, err)
		}
	}
}
