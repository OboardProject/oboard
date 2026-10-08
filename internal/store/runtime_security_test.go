package store

import (
	"context"
	"github.com/OboardProject/oboard/internal/model"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeSecurityPersistenceAndIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "security.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	desired := model.RuntimeSecurityRequest{Mode: "enhanced", Revision: 42}
	if err := s.SaveRuntimeSecurityDesired(ctx, 1, desired); err != nil {
		t.Fatal(err)
	}
	other, err := s.RuntimeSecurityDesired(ctx, 2)
	if err != nil || other.Mode != "standard" || other.Revision != 0 {
		t.Fatal(other, err)
	}
	rev := s.SettingsRevision()
	report := model.RuntimeSecurityReport{Revision: 42, DesiredMode: "enhanced", ActualMode: "standard", State: "failed", CheckedAt: time.Now().UTC()}
	if err := s.SaveRuntimeSecurityReport(ctx, 1, report); err != nil {
		t.Fatal(err)
	}
	var updated string
	if err := s.db.QueryRowContext(ctx, "select updated_at from app_settings where key=?", runtimeSecurityKey(1, "report")).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRuntimeSecurityReport(ctx, 1, report); err != nil {
		t.Fatal(err)
	}
	var next string
	if err := s.db.QueryRowContext(ctx, "select updated_at from app_settings where key=?", runtimeSecurityKey(1, "report")).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if updated != next || s.SettingsRevision() != rev {
		t.Fatal("unchanged report caused invalidation or write")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.RuntimeSecurityDesired(ctx, 1)
	if err != nil || got != desired {
		t.Fatal(got, err)
	}
	stored, err := s.RuntimeSecurityReport(ctx, 1)
	if err != nil || stored == nil || stored.State != "failed" {
		t.Fatal(stored, err)
	}
}
