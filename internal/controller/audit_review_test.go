package controller

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/aiprovider"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestAuditAIReviewsAreAdminOnlyAndIdempotent(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "audit-review-controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	handler := newTestServer(db, "test-secret", "").Handler()
	request(t, handler, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	request(t, handler, http.MethodPost, "/api/v2/ui/users", adminToken, map[string]any{"username": "operator", "password": "long-operator-password", "role": "operator", "status": "active"}, http.StatusCreated)
	operatorLogin := request(t, handler, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "operator", "password": "long-operator-password"}, http.StatusOK)
	operatorToken := operatorLogin["token"].(string)

	request(t, handler, http.MethodGet, "/api/v2/ui/audit/ai-reviews", operatorToken, nil, http.StatusForbidden)
	page := request(t, handler, http.MethodGet, "/api/v2/ui/page-data?page=audit", adminToken, nil, http.StatusOK)
	if _, ok := page["users"]; !ok {
		t.Fatal("admin audit page omitted selectable users")
	}

	provider := request(t, handler, http.MethodPost, "/api/v2/ai/providers", adminToken, map[string]any{
		"name": "local", "base_url": "http://127.0.0.1:11434/v1", "model": "test", "api_key": "secret", "enabled": true,
	}, http.StatusCreated)["data"].(map[string]any)
	storedProvider, err := db.GetAIProvider(context.Background(), provider["id"].(string))
	if err != nil || len(storedProvider.Endpoints) != 1 {
		t.Fatalf("provider endpoints=%#v err=%v", storedProvider, err)
	}
	endpoint := storedProvider.Endpoints[0]
	capability := &model.AIProviderCapability{
		ProviderProfileVersion:  model.AuditProviderProfileVersion,
		ProviderID:              storedProvider.ID,
		EndpointID:              endpoint.ID,
		APIStyle:                endpoint.APIStyle,
		Model:                   "test",
		ConfigDigest:            aiprovider.ConfigDigest(aiprovider.RuntimeEndpoint{ID: endpoint.ID, BaseURL: endpoint.BaseURL, APIStyle: aiprovider.APIStyle(endpoint.APIStyle), AuthMode: endpoint.AuthMode}, "test"),
		TestedAt:                time.Now().UTC(),
		AuditGrade:              model.AuditProviderGradeA,
		StructuredOutput:        model.AuditProviderStructuredJSONSchema,
		OutputMode:              model.AuditOutputModeStrictSchema,
		SchemaSuccessRate:       1.0,
		UsageSupported:          true,
		FinishReasonSupported:   true,
		MaxVerifiedOutputTokens: 4096,
	}
	if err := db.UpsertAIProviderEndpointCapability(context.Background(), capability); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"request_id": "request-idempotent", "provider_id": provider["id"],
		"scope":          map[string]any{"users": map[string]any{"mode": "all", "ids": []int64{}}, "servers": map[string]any{"mode": "all", "ids": []int64{}}},
		"evidence_types": []string{"subscription", "connection", "destination"},
		"time_range":     map[string]any{"mode": "preset", "preset": "24h"},
	}
	first := request(t, handler, http.MethodPost, "/api/v2/ui/audit/ai-reviews", adminToken, body, http.StatusAccepted)["ai_audit_review"].(map[string]any)
	second := request(t, handler, http.MethodPost, "/api/v2/ui/audit/ai-reviews", adminToken, body, http.StatusAccepted)["ai_audit_review"].(map[string]any)
	if first["id"] != second["id"] {
		t.Fatalf("idempotent IDs differ: %v != %v", first["id"], second["id"])
	}
	reviewID := first["id"].(string)
	request(t, handler, http.MethodGet, "/api/v2/ui/audit/ai-reviews/"+reviewID, adminToken, nil, http.StatusOK)
	request(t, handler, http.MethodGet, "/api/v2/ui/audit/ai-reviews/"+reviewID+"/evidence", adminToken, nil, http.StatusOK)
	request(t, handler, http.MethodGet, "/api/v2/ui/audit/ai-reviews/"+reviewID+"/jobs", adminToken, nil, http.StatusOK)
	request(t, handler, http.MethodPost, "/api/v2/ui/audit/ai-reviews/"+reviewID+"/cancel", adminToken, map[string]any{}, http.StatusOK)
}

func TestAuditReviewTimeRangeValidation(t *testing.T) {
	nowTime := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	for _, preset := range []string{"1h", "24h", "7d", "30d"} {
		start, end, err := auditReviewTimeRange("preset", preset, nil, nil, nowTime)
		if err != nil || !end.Equal(nowTime) || !start.Before(end) {
			t.Fatalf("preset %s start=%s end=%s err=%v", preset, start, end, err)
		}
	}
	if _, _, err := auditReviewTimeRange("preset", "90d", nil, nil, nowTime); err == nil {
		t.Fatal("invalid preset was accepted")
	}
	start, end := nowTime.Add(-2*time.Hour), nowTime.Add(-time.Hour)
	gotStart, gotEnd, err := auditReviewTimeRange("custom", "", &start, &end, nowTime)
	if err != nil || !gotStart.Equal(start) || !gotEnd.Equal(end) {
		t.Fatalf("custom range start=%s end=%s err=%v", gotStart, gotEnd, err)
	}
	if _, _, err := auditReviewTimeRange("custom", "", nil, &end, nowTime); err == nil {
		t.Fatal("incomplete custom range was accepted")
	}
}
