package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestListAutomationChangesetsIncludesOperations(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	changeset := &model.AutomationChangeset{
		ID: "chg_list_operations", PrincipalID: "principal-1", Status: model.ChangesetDraft,
		Reason: "list operations", IdempotencyKey: "list-operations", BaseRevisions: json.RawMessage(`{}`),
		Validation: json.RawMessage(`{}`), BlastRadius: json.RawMessage(`{}`), ExpiresAt: now.Add(time.Hour),
		Operations: []model.AutomationOperation{{
			ID: "op_list_operations", Position: 0, Capability: "servers.onboard", Input: json.RawMessage(`{}`),
			ResourceRefs: json.RawMessage(`{}`), Status: "pending",
		}},
	}
	if err := db.CreateAutomationChangeset(context.Background(), changeset); err != nil {
		t.Fatal(err)
	}

	items, err := db.ListAutomationChangesets(context.Background(), changeset.PrincipalID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("changesets = %d, want 1", len(items))
	}
	if len(items[0].Operations) != 1 || items[0].Operations[0].Capability != "servers.onboard" {
		t.Fatalf("operations = %#v, want persisted operation", items[0].Operations)
	}
}
