package controller

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestSettingsIncludeStorageDiagnostics(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "storage-diag.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	token := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)["token"].(string)

	settingsResponse := request(t, handler, http.MethodGet, "/api/v1/ui/settings", token, nil, http.StatusOK)
	settings := settingsResponse["settings"].(map[string]any)
	if _, ok := settings[store.DatabaseLastMaintenanceAtSetting]; ok {
		t.Fatalf("raw last-maintenance setting must not leak: %#v", settings[store.DatabaseLastMaintenanceAtSetting])
	}
	raw, ok := settings["storage_diagnostics"].(map[string]any)
	if !ok {
		t.Fatalf("storage_diagnostics missing: %#v", settings["storage_diagnostics"])
	}
	if raw["connection_audit_retention_hours"] != float64(store.ConnectionAuditRetentionHours) {
		t.Fatalf("retention = %#v", raw["connection_audit_retention_hours"])
	}
	if _, ok := raw["db_bytes"]; !ok {
		t.Fatalf("db_bytes missing: %#v", raw)
	}
	rollup, _ := raw["audit_rollup_state"].(map[string]any)
	if rollup == nil {
		t.Fatalf("audit_rollup_state missing: %#v", raw)
	}
}
