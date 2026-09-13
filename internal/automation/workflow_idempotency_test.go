package automation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
)

func TestWorkflowReplayBindsRequestAndCurrentAccess(t *testing.T) {
	ctx := context.Background()
	db := openAutomationTestStore(t)
	p := &model.APIPrincipal{ID: "workflow-replay", Name: "workflow replay", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(ctx, p); err != nil {
		t.Fatal(err)
	}
	principal := application.Principal{ID: p.ID, Type: p.Type, Scopes: p.Scopes}
	s := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(s)
	changeset := createAutomationTestChangeset(t, s, principal, "workflow-original", nil)
	other := createAutomationTestChangeset(t, s, principal, "workflow-other", nil)
	r := StartWorkflowRequest{IdempotencyKey: "workflow-request", ChangesetID: changeset.ID, Reason: "track save"}
	first, err := s.StartWorkflow(ctx, principal, r)
	if err != nil {
		t.Fatal(err)
	}
	// State-derived installation readiness is not a second client intent.
	r.ExternalAction = true
	s = NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(s)
	replayed, err := s.StartWorkflow(ctx, principal, r)
	if err != nil || replayed.ID != first.ID {
		t.Fatalf("replay=%v err=%v", replayed, err)
	}
	for name, mutate := range map[string]func(*StartWorkflowRequest){
		"changeset": func(r *StartWorkflowRequest) { r.ChangesetID = other.ID },
		"kind":      func(r *StartWorkflowRequest) { r.Kind = "deployment" },
		"reason":    func(r *StartWorkflowRequest) { r.Reason = "another intent" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := r
			mutate(&changed)
			if got, err := s.StartWorkflow(ctx, principal, changed); got != nil || !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("content conflict returned %v, %v", got, err)
			}
		})
	}
	revoked := principal
	revoked.Scopes = nil
	if got, err := s.StartWorkflow(ctx, revoked, r); got != nil || err == nil {
		t.Fatalf("revoked replay=%v err=%v", got, err)
	}
	if got, err := s.GetWorkflow(ctx, revoked, first.ID); got != nil || err == nil {
		t.Fatalf("revoked result query=%v err=%v", got, err)
	}
	s.SetReplayAuthorizer(func(context.Context, application.Principal, model.AutomationOperation) error {
		return errors.New("grant revoked")
	})
	if got, err := s.StartWorkflow(ctx, principal, r); got != nil || err == nil {
		t.Fatalf("grant replay=%v err=%v", got, err)
	}
	if got, err := s.GetWorkflow(ctx, principal, first.ID); got != nil || err == nil {
		t.Fatalf("grant query=%v err=%v", got, err)
	}
}

func TestConcurrentWorkflowRequestsHaveOneWinner(t *testing.T) {
	ctx := context.Background()
	db := openAutomationTestStore(t)
	p := &model.APIPrincipal{ID: "workflow-race", Name: "workflow race", Type: model.APIPrincipalServiceAccount, Enabled: true, Scopes: []string{"servers:onboard"}, ResourceFilter: json.RawMessage(`{}`), RateLimitPerMinute: 60, MaxConcurrency: 2}
	if err := db.CreateAPIPrincipal(ctx, p); err != nil {
		t.Fatal(err)
	}
	principal := application.Principal{ID: p.ID, Type: p.Type, Scopes: p.Scopes}
	s := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(s)
	changeset := createAutomationTestChangeset(t, s, principal, "workflow-race", nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	winnerID, winnerReason := "", ""
	for i := range 8 {
		wg.Go(func() {
			reason := "first"
			if i%2 != 0 {
				reason = "second"
			}
			got, err := s.StartWorkflow(ctx, principal, StartWorkflowRequest{IdempotencyKey: "same-key", ChangesetID: changeset.ID, Reason: reason})
			if errors.Is(err, ErrIdempotencyConflict) {
				return
			}
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if got.Reason != reason {
				t.Errorf("wrong request result: requested=%s got=%s", reason, got.Reason)
			}
			if winnerID != "" && (winnerID != got.ID || winnerReason != got.Reason) {
				t.Errorf("multiple winners: %s and %s", winnerID, got.ID)
			}
			winnerID, winnerReason = got.ID, got.Reason
		})
	}
	wg.Wait()
	if winnerID == "" {
		t.Fatal("no request succeeded")
	}
}
