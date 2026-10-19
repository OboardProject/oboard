package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestServerDefaultBBRQdiscMigratesBooleanSwitch starts from a database that
// still stores the former boolean server_default_bbr_enabled install switch and
// verifies it becomes the qdisc choice without overriding an explicit one.
func TestServerDefaultBBRQdiscMigratesBooleanSwitch(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, legacy, existing, want string
	}{
		{name: "disabled", legacy: "false", want: "off"},
		{name: "enabled", legacy: "true", want: ""},
		{name: "explicit qdisc wins", legacy: "false", existing: "cake", want: "cake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "oboard.sqlite")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.SetSetting(ctx, legacyServerDefaultBBREnabledSetting, tc.legacy); err != nil {
				t.Fatal(err)
			}
			if tc.existing != "" {
				if err := db.SetSetting(ctx, ServerDefaultBBRQdiscSetting, tc.existing); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				db, err := Open(path)
				if err != nil {
					t.Fatalf("open attempt %d: %v", attempt, err)
				}
				settings, err := db.ListSettings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := settings[legacyServerDefaultBBREnabledSetting]; ok {
					t.Fatalf("attempt %d: legacy BBR switch survived", attempt)
				}
				if got := settings[ServerDefaultBBRQdiscSetting]; got != tc.want {
					t.Fatalf("attempt %d: qdisc = %q, want %q", attempt, got, tc.want)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
