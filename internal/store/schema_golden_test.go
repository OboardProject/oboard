package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dumpFreshSchema renders the schema a brand-new installation produces.
//
// The R cleanup folds columns that were added by a migration back into their
// create table statement and then deletes the migration. That is only correct
// if a fresh install still ends up with exactly the same schema. This renders
// that schema in a stable form so the two states can be compared.
func dumpFreshSchema(t *testing.T) string {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "schema.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.MigrateDeferredIndexes(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := db.db.QueryContext(ctx, `select type,name,tbl_name from sqlite_master where name not like 'sqlite_%' order by type,name`)
	if err != nil {
		t.Fatal(err)
	}
	type object struct{ kind, name, table string }
	objects := []object{}
	for rows.Next() {
		var o object
		if err := rows.Scan(&o.kind, &o.name, &o.table); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, o)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()

	var out strings.Builder
	for _, o := range objects {
		if o.kind != "table" {
			fmt.Fprintf(&out, "%s %s on %s\n", o.kind, o.name, o.table)
			continue
		}
		cols, err := db.db.QueryContext(ctx, `select name,type,"notnull",dflt_value,pk from pragma_table_info(?)`, o.name)
		if err != nil {
			t.Fatal(err)
		}
		lines := []string{}
		for cols.Next() {
			var name, colType string
			var notNull, pk int
			var dflt *string
			if err := cols.Scan(&name, &colType, &notNull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			shown := "-"
			if dflt != nil {
				shown = *dflt
			}
			lines = append(lines, fmt.Sprintf("    %s %s notnull=%d default=%s pk=%d", name, colType, notNull, shown, pk))
		}
		if err := cols.Err(); err != nil {
			t.Fatal(err)
		}
		_ = cols.Close()
		// Column order is deliberately not sorted. Folding a migrated column
		// back into its create table must reproduce the position that
		// "alter table add column" gave it, and sorting here would hide a
		// reordering that changes what "select *" returns.
		fmt.Fprintf(&out, "table %s\n%s\n", o.name, strings.Join(lines, "\n"))
	}
	return out.String()
}

// TestFreshInstallSchemaIsUnchanged pins the schema a new installation builds.
//
// Regenerate the golden file only when a schema change is intended, with
// UPDATE_SCHEMA_GOLDEN=1, and review the diff in the commit.
func TestFreshInstallSchemaIsUnchanged(t *testing.T) {
	got := dumpFreshSchema(t)
	golden := filepath.Join("testdata", "fresh_schema.txt")
	if os.Getenv("UPDATE_SCHEMA_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Log("schema golden updated")
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read schema golden: %v (run with UPDATE_SCHEMA_GOLDEN=1 to create it)", err)
	}
	if string(want) != got {
		t.Fatalf("fresh install schema changed.\nIf this is intended, rerun with UPDATE_SCHEMA_GOLDEN=1 and review the diff.\n%s", firstSchemaDiff(string(want), got))
	}
}

func firstSchemaDiff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	limit := len(w)
	if len(g) > limit {
		limit = len(g)
	}
	for i := 0; i < limit; i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			return fmt.Sprintf("first difference at line %d\n  want: %s\n  got:  %s", i+1, a, b)
		}
	}
	return "(no line differs)"
}
