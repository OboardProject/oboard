package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetStorageDiagnostics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diag.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	at := time.Now().UTC().Truncate(time.Second)
	if err := s.recordMaintenanceResult(ctx, at, MaintenanceResult{
		ConnectionAuditsDeleted: 3,
		FreePagesReclaimed:      12,
		NeedsCatchUp:            false,
	}); err != nil {
		t.Fatal(err)
	}

	diag, err := s.GetStorageDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diag.ConnectionAuditRetentionHours != ConnectionAuditRetentionHours || ConnectionAuditRetentionHours != 30*24 {
		t.Fatalf("retention hours = %d, want %d", diag.ConnectionAuditRetentionHours, 30*24)
	}
	if diag.DBBytes <= 0 {
		info, _ := os.Stat(path)
		t.Fatalf("db_bytes = %d, file info=%v", diag.DBBytes, info)
	}
	if diag.LastMaintenanceAt == "" {
		t.Fatal("expected last_maintenance_at")
	}
	if n, ok := diag.LastMaintenanceSummary["connection_audits_deleted"].(float64); !ok || n != 3 {
		t.Fatalf("summary deleted = %#v", diag.LastMaintenanceSummary)
	}
	if path := diag.AuditRollupState["read_path"]; path != "raw" && path != "hourly" && path != "" {
		t.Fatalf("unexpected read_path %#v", diag.AuditRollupState)
	}
}
