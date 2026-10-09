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
	server := model.Server{Name: "runtime-node", AgentID: "runtime-agent"}
	if err := s.CreateServer(ctx, &server); err != nil {
		t.Fatal(err)
	}
	desired := model.RuntimeSecurityRequest{Mode: "enhanced", Revision: 42}
	if err := s.SaveRuntimeSecurityDesired(ctx, server, desired); err != nil {
		t.Fatal(err)
	}
	other, err := s.RuntimeSecurityDesired(ctx, 2)
	if err != nil || other.Mode != "standard" || other.Revision != 0 {
		t.Fatal(other, err)
	}
	rev := s.SettingsRevision()
	report := model.RuntimeSecurityReport{Revision: 42, DesiredMode: "enhanced", ActualMode: "standard", State: "failed", CheckedAt: time.Now().UTC()}
	if err := s.SaveRuntimeSecurityReport(ctx, server, report); err != nil {
		t.Fatal(err)
	}
	var updated string
	if err := s.db.QueryRowContext(ctx, "select updated_at from app_settings where key=?", runtimeSecurityKey(1, "report")).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveRuntimeSecurityReport(ctx, server, report); err != nil {
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

func TestRuntimeSecurityRejectsRecycledServer(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "security.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := model.Server{Name: "old", AgentID: "old-agent"}
	if err := db.CreateServer(ctx, &old); err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRuntimeSecurityDesired(ctx, old, model.RuntimeSecurityRequest{Mode: "enhanced", Revision: 9}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteServer(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	fresh := model.Server{Name: "replacement", AgentID: "new-agent"}
	if err := db.CreateServer(ctx, &fresh); err != nil {
		t.Fatal(err)
	}
	if fresh.ID != old.ID {
		t.Fatal("fixture did not reuse server ID")
	}
	if err := db.SaveRuntimeSecurityDesired(ctx, old, model.RuntimeSecurityRequest{Mode: "enhanced", Revision: 99}); err == nil {
		t.Fatal("stale desired accepted")
	}
	if err := db.SaveRuntimeSecurityReport(ctx, old, model.RuntimeSecurityReport{State: "enhanced"}); err == nil {
		t.Fatal("stale report accepted")
	}
	task := model.AgentTask{ServerID: old.ID, Type: model.AgentTaskTypeRuntimeSecurity, Status: "pending", PayloadJSON: "{}", ResultJSON: "{}", Nonce: "test"}
	if err := db.CreateRuntimeSecurityTask(ctx, old, &task); err == nil {
		t.Fatal("stale task accepted")
	}
	desired, err := db.RuntimeSecurityDesired(ctx, fresh.ID)
	if err != nil || desired.Mode != "standard" || desired.Revision != 0 {
		t.Fatal(desired, err)
	}
	report, err := db.RuntimeSecurityReport(ctx, fresh.ID)
	if err != nil || report != nil {
		t.Fatal(report, err)
	}
	old = fresh
	old.CreatedAt = old.CreatedAt.Add(-time.Second)
	if err := db.SaveRuntimeSecurityDesired(ctx, old, desired); err == nil {
		t.Fatal("creation identity ignored")
	}
}
