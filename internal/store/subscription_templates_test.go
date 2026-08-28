package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

func TestSubscriptionClientTemplatesStartAsBuiltin(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "templates.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	items, err := s.ListSubscriptionClientTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != len(core.ConcreteSubscriptionFormats()) {
		t.Fatalf("listed %d templates, want %d", len(items), len(core.ConcreteSubscriptionFormats()))
	}
	for _, item := range items {
		if item.Source != "builtin" || item.Revision != 0 || strings.TrimSpace(item.Content) == "" {
			t.Fatalf("builtin template not composed: %#v", item)
		}
	}
}

func TestSubscriptionClientTemplatePutResetAndConflict(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "templates.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	current, err := s.GetSubscriptionClientTemplate(ctx, model.SubscriptionFormatMihomo)
	if err != nil {
		t.Fatal(err)
	}
	custom := strings.Replace(current.Content, "mixed-port: 7890", "mixed-port: 17890", 1)
	saved, err := s.PutSubscriptionClientTemplate(ctx, model.SubscriptionFormatMihomo, custom, current.Revision, 7)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Source != "custom" || saved.Revision != 1 || saved.Content != custom {
		t.Fatalf("saved template = %#v", saved)
	}
	if _, err := s.PutSubscriptionClientTemplate(ctx, model.SubscriptionFormatMihomo, custom, current.Revision, 7); err != ErrSubscriptionTemplateConflict {
		t.Fatalf("stale revision error = %v", err)
	}
	tasks, err := s.ListTasks(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Fatalf("template write queued agent tasks: %#v", tasks)
	}
	reset, err := s.ResetSubscriptionClientTemplate(ctx, model.SubscriptionFormatMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if reset.Source != "builtin" || reset.Content != current.Content {
		t.Fatalf("reset template = %#v", reset)
	}
}

func TestSubscriptionTemplateAndAuditColumnsMigrateFromPreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "templates-migrate.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`drop table if exists subscription_client_templates`,
		`alter table subscription_pull_audits drop column requested_format`,
		`alter table subscription_pull_audits drop column auto_detected`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("open previous schema: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListSubscriptionClientTemplates(ctx)
	if err != nil || len(items) == 0 {
		t.Fatalf("migrated templates = %d err=%v", len(items), err)
	}
}
