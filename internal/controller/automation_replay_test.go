package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/mcpauth"
)

func TestAutomationIdempotencyConflictSurfaces(t *testing.T) {
	err := fmt.Errorf("submit: %w", automation.ErrIdempotencyConflict)
	w := httptest.NewRecorder()
	v2HandleError(w, httptest.NewRequest(http.MethodPost, "/api/v1/changesets", nil), err)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"idempotency_conflict"`) {
		t.Fatalf("HTTP conflict: %d %s", w.Code, w.Body.String())
	}
	result := fastPathCodedError(err, true, "retry")
	if result == nil || result.Error == nil || result.Error.Code != mcpauth.CodeIdempotencyConflict || result.Error.Recoverable {
		t.Fatalf("MCP conflict=%+v", result)
	}
}
