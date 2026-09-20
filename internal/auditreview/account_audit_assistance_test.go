package auditreview

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/auditrisk"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAccountAuditAssistanceWorkerBridge(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	actor := &model.User{Username: "assistance-admin", Role: model.RoleAdmin, Status: "active"}
	if err := db.CreateUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Minute)
	good := auditrisk.Dimension{State: auditrisk.Satisfied}
	snapshot := auditrisk.Snapshot{AccountID: actor.ID, Activity: &auditrisk.Score{Lower: 80, Upper: 90, Status: "complete"}, Policy: auditrisk.DefaultPolicy(), Versions: auditrisk.Versions{Model: "v1", Source: "v1", Baseline: "v1"}, Quality: auditrisk.Quality{IdentityTrusted: good, SourceUsable: good, Deduplicated: good, MeasurementValid: good, CapabilitySupported: good, HistoryComplete: good, CoverageComplete: good, Freshness: good, TimeAligned: good, SourceSetComplete: good}}
	for i := 0; i < 2; i++ {
		snapshot.AsOf = at.Add(time.Duration(i-1) * time.Minute)
		snapshot.WindowEnd = snapshot.AsOf
		snapshot.WindowStart = snapshot.AsOf.Add(-30 * time.Minute)
		if err := db.SaveAccountAuditSnapshot(ctx, snapshot, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := db.ListAccountAuditEvents(ctx, store.AccountAuditQuery{UserID: actor.ID})
	if err != nil {
		t.Fatal(err)
	}
	events := page.Items.([]store.AccountAuditEvent)
	if len(events) != 1 {
		t.Fatalf("events=%+v", events)
	}
	event := events[0]
	unavailable, err := db.QueueAccountAuditAssistance(ctx, actor.ID, event.ID, event.Revision, actor.ID, "", at)
	if err != nil || unavailable.Status != "unavailable" || unavailable.ErrorCode != "assistance_provider_unavailable" {
		t.Fatalf("unavailable=%+v err=%v", unavailable, err)
	}
	provider := testProviderFixture()
	if err := db.CreateAIProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	createTestProviderEndpoint(t, ctx, db, provider, testCapabilityFixture())
	queued, err := db.QueueAccountAuditAssistance(ctx, actor.ID, event.ID, event.Revision, actor.ID, provider.ID, at)
	if err != nil || queued.Status != "queued" {
		t.Fatalf("queued=%+v err=%v", queued, err)
	}
	if _, err := db.QueueAccountAuditAssistance(ctx, actor.ID+1, event.ID, event.Revision, actor.ID, provider.ID, at); err == nil {
		t.Fatal("cross-user request accepted")
	}
	if _, err := db.QueueAccountAuditAssistance(ctx, actor.ID, event.ID, event.Revision+1, actor.ID, provider.ID, at); err == nil {
		t.Fatal("stale revision accepted")
	}
	job, _, err := db.LeaseAuditReviewJob(ctx, "test-worker", at, time.Minute)
	if err != nil || job == nil {
		t.Fatalf("lease=%+v err=%v", job, err)
	}
	var input struct {
		Context struct {
			Snapshot auditrisk.Snapshot `json:"snapshot"`
		} `json:"context"`
	}
	if err := json.Unmarshal(job.Input, &input); err != nil {
		t.Fatal(err)
	}
	if len(job.Input) > 64<<10 || input.Context.Snapshot.Activity.Lower != 80 || input.Context.Snapshot.AccountID != 0 {
		t.Fatalf("invalid bounded masked snapshot: %s", job.Input)
	}
	service := testService(db)
	output, _ := json.Marshal(model.AuditUserFinding{SchemaVersion: model.AuditUserFindingSchemaVersion, SubjectRef: "account", BehaviorProfile: model.AuditBehaviorProfile{CurrentPattern: []string{"保存快照中的活动风险区间为80至90，不代表滥用概率。"}}, DataGaps: []string{"需要人工核实"}})
	if err := service.ValidateReport(ctx, job.ReviewID, job, output); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CompleteAuditReviewJob(ctx, "test-worker", job.ID, output, 10, 10, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.Advance(ctx, job.ReviewID); err != nil {
		t.Fatal(err)
	}
	result, err := db.GetAccountAuditAssistance(ctx, actor.ID, event.ID, event.Revision)
	if err != nil || result.Status != "succeeded" || string(result.Result) != string(output) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	// Human workflow changes do not change the persisted risk snapshot or re-run AI.
	if err := db.SetAccountAuditEventStatus(ctx, event.ID, event.Revision, "handled", "admin:1", "checked", time.Time{}, at); err != nil {
		t.Fatal(err)
	}
	replay, err := db.QueueAccountAuditAssistance(ctx, actor.ID, event.ID, event.Revision+1, actor.ID, "missing-provider", at)
	if err != nil || replay.ReviewID != queued.ReviewID || replay.Status != "succeeded" {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	jobs, err := db.ListAuditReviewJobs(ctx, queued.ReviewID, false)
	if err != nil || len(jobs) != 1 || jobs[0].Kind != "finding" {
		t.Fatalf("unexpected synthesis or duplicate jobs=%+v err=%v", jobs, err)
	}
}
