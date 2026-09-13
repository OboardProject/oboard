package automation

import (
	"context"
	"encoding/json"
	"testing"
)

func TestApprovedResourceRevisionContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), approvedRevisionsContextKey{}, json.RawMessage(`{"server:7":"2026-09-13T00:00:00Z"}`))
	if value, ok := ApprovedResourceRevision(ctx, "server:7"); !ok || value != "2026-09-13T00:00:00Z" {
		t.Fatalf("value=%q ok=%v", value, ok)
	}
	if _, ok := ApprovedResourceRevision(ctx, "server:8"); ok {
		t.Fatal("invented resource revision")
	}
	if _, ok := ApprovedResourceRevision(context.Background(), "server:7"); ok {
		t.Fatal("invented context")
	}
}
