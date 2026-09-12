package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/capability"
	"github.com/OboardProject/oboard/internal/model"
)

func TestChangesetReplayBindsOriginalRequest(t *testing.T) {
	ctx := context.Background()
	db := openAutomationTestStore(t)
	s := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(s)
	p := application.Principal{ID: "replay", Scopes: []string{"servers:onboard"}}
	original := CreateRequest{IdempotencyKey: "create", Reason: "create server", BaseRevisions: json.RawMessage(`{"server:7":"1"}`), Operations: []OperationRequest{{Capability: "servers.onboard", Input: json.RawMessage(`{"name":"first","port":443}`), ResourceRefs: json.RawMessage(`{"server_ids":[7]}`), SecretRefs: []string{"secret-a"}}}}
	first, err := s.Create(ctx, p, original)
	if err != nil {
		t.Fatal(err)
	}
	// Recreate the service to require persisted request identity, not a process cache.
	s = NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(s)
	reordered := original
	reordered.Operations = append([]OperationRequest(nil), original.Operations...)
	reordered.Operations[0].Input = json.RawMessage(`{ "port":443, "name":"first" }`)
	replay, err := s.Create(ctx, p, reordered)
	if err != nil || replay.ID != first.ID {
		t.Fatalf("same request replay=%v err=%v", replay, err)
	}
	for name, mutate := range map[string]func(*CreateRequest){
		"input":            func(r *CreateRequest) { r.Operations[0].Input = json.RawMessage(`{"name":"second","port":443}`) },
		"revision":         func(r *CreateRequest) { r.BaseRevisions = json.RawMessage(`{"server:7":"2"}`) },
		"references":       func(r *CreateRequest) { r.Operations[0].ResourceRefs = json.RawMessage(`{"server_ids":[8]}`) },
		"secrets":          func(r *CreateRequest) { r.Operations[0].SecretRefs = []string{"secret-b"} },
		"reason":           func(r *CreateRequest) { r.Reason = "different intent" },
		"auto_apply":       func(r *CreateRequest) { r.AutoApply = true },
		"empty_operations": func(r *CreateRequest) { r.Operations = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			changed.Operations = append([]OperationRequest(nil), original.Operations...)
			mutate(&changed)
			if result, err := s.Create(ctx, p, changed); err == nil || result != nil {
				t.Fatalf("different request returned old result: %v err=%v", result, err)
			} else if name != "empty_operations" && !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("wrong conflict error: %v", err)
			}
		})
	}
	revoked := p
	revoked.Scopes = nil
	if result, err := s.Create(ctx, revoked, original); err == nil || result != nil {
		t.Fatalf("revoked principal replay=%v err=%v", result, err)
	}
	first.Status = model.ChangesetSucceeded
	first.Result = json.RawMessage(`{"saved":true}`)
	if err := db.UpdateAutomationChangeset(ctx, first); err != nil {
		t.Fatal(err)
	}
	replay, err = s.Create(ctx, p, original)
	if err != nil || replay.Status != model.ChangesetSucceeded {
		t.Fatalf("committed replay=%v err=%v", replay, err)
	}
}

func TestChangesetConcurrentReplayAndResourceRecheck(t *testing.T) {
	ctx := context.Background()
	db := openAutomationTestStore(t)
	s := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(s)
	p := application.Principal{ID: "concurrent", Scopes: []string{"servers:onboard"}}
	r := CreateRequest{IdempotencyKey: "race", Operations: []OperationRequest{{Capability: "servers.onboard", Input: json.RawMessage(`{}`)}}}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			item, err := s.Create(ctx, p, r)
			if err != nil {
				t.Errorf("concurrent create: %v", err)
				return
			}
			ids <- item.ID
		})
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first != "" && id != first {
			t.Fatalf("duplicate changesets: %s and %s", first, id)
		}
		first = id
	}
	s.RegisterValidator("servers.onboard", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		return nil, errors.New("creation forbidden")
	})
	restricted := p
	restricted.ResourceFilter = json.RawMessage(`{"servers":{"mode":"none","allow_create":false}}`)
	if result, err := s.Create(ctx, restricted, r); err == nil || result != nil {
		t.Fatal("resource restriction bypassed by replay")
	}
	s.SetReplayAuthorizer(func(context.Context, application.Principal, model.AutomationOperation) error {
		return errors.New("grant revoked")
	})
	if result, err := s.Create(ctx, p, r); err == nil || result != nil {
		t.Fatal("current grant check bypassed by replay")
	}
}

func TestChangesetConcurrentDifferentRequestsHaveOneWinner(t *testing.T) {
	s := NewService(openAutomationTestStore(t), capability.NewCatalog())
	registerAutomationTestCapability(s)
	p := application.Principal{ID: "different-race", Scopes: []string{"servers:onboard"}}
	var wg sync.WaitGroup
	winners := make(chan string, 8)
	for i := range 8 {
		wg.Go(func() {
			r := CreateRequest{IdempotencyKey: "race", Operations: []OperationRequest{{Capability: "servers.onboard", Input: json.RawMessage(fmt.Sprintf(`{"name":"server-%d"}`, i))}}}
			item, err := s.Create(context.Background(), p, r)
			if errors.Is(err, ErrIdempotencyConflict) {
				return
			}
			if err != nil {
				t.Errorf("create: %v", err)
				return
			}
			winners <- item.ID
		})
	}
	wg.Wait()
	close(winners)
	if len(winners) != 1 {
		t.Fatalf("accepted %d different requests for one key", len(winners))
	}
}

func TestChangesetInputPreservesIntegerIdentity(t *testing.T) {
	db := openAutomationTestStore(t)
	s := NewService(db, capability.NewCatalog())
	registerAutomationTestCapability(s)
	p := application.Principal{ID: "integers", Scopes: []string{"servers:onboard"}}
	r := CreateRequest{IdempotencyKey: "integer", Operations: []OperationRequest{{Capability: "servers.onboard", Input: json.RawMessage(`{"id":9007199254740993}`)}}}
	first, err := s.Create(context.Background(), p, r)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Operations[0].Input) != `{"id":9007199254740993}` {
		t.Fatalf("integer changed: %s", first.Operations[0].Input)
	}
	r.Operations[0].Input = json.RawMessage(`{"id":9007199254740992}`)
	if _, err := s.Create(context.Background(), p, r); err == nil {
		t.Fatal("distinct integer request replayed")
	}
}
