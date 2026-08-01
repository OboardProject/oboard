package controller

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestHealthzChecksRequestSchemas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := newTestServer(db, "test-secret", "").Handler()

	healthy := httptest.NewRecorder()
	h.ServeHTTP(healthy, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if healthy.Code != http.StatusOK {
		t.Fatalf("healthy status = %d body=%s", healthy.Code, healthy.Body.String())
	}

	dropStoreTelemetryColumn(t, path, "time_check_source")
	unhealthy := httptest.NewRecorder()
	h.ServeHTTP(unhealthy, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if unhealthy.Code != http.StatusServiceUnavailable {
		t.Fatalf("unhealthy status = %d body=%s", unhealthy.Code, unhealthy.Body.String())
	}
	if strings.Contains(unhealthy.Body.String(), "time_check_source") || strings.Contains(strings.ToLower(unhealthy.Body.String()), "sql") {
		t.Fatalf("health response leaked storage details: %s", unhealthy.Body.String())
	}
}

func TestAgentAuthDistinguishesCredentialsFromStoreFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oboard.sqlite")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{
		Name:           "agent-auth-node",
		AgentID:        "agent-auth-id",
		AgentTokenHash: security.HashSecret("agent-auth-token"),
		Status:         model.ServerOnline,
	}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	h := newTestServer(db, "test-secret", "").Handler()

	wrongToken := agentTrafficRequest(server.AgentID, "wrong-token")
	wrongTokenResponse := httptest.NewRecorder()
	h.ServeHTTP(wrongTokenResponse, wrongToken)
	if wrongTokenResponse.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status = %d body=%s", wrongTokenResponse.Code, wrongTokenResponse.Body.String())
	}

	dropStoreTelemetryColumn(t, path, "time_check_error")
	storeFailure := httptest.NewRecorder()
	h.ServeHTTP(storeFailure, agentTrafficRequest(server.AgentID, "agent-auth-token"))
	if storeFailure.Code != http.StatusInternalServerError {
		t.Fatalf("store failure status = %d body=%s", storeFailure.Code, storeFailure.Body.String())
	}
	if strings.Contains(storeFailure.Body.String(), "time_check_error") || strings.Contains(strings.ToLower(storeFailure.Body.String()), "sql") {
		t.Fatalf("agent response leaked storage details: %s", storeFailure.Body.String())
	}
}

func dropStoreTelemetryColumn(t *testing.T, path, column string) {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`alter table server_telemetry drop column ` + column); err != nil {
		t.Fatalf("drop %s: %v", column, err)
	}
}

func agentTrafficRequest(agentID, token string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent/traffic-reports", bytes.NewReader([]byte(`{"items":[]}`)))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("X-Agent-ID", agentID)
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}
