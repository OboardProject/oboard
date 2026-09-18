package store

import (
	"context"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

// Snell v6 hardened presets carry PSKs whose first-record bit density is
// verified against the sing-snell v6 profile derivation (oboard-agent
// third_party/sing-snell). The pinned values below must change together with
// a re-verification whenever the sing-snell v6 profile derivation changes.
const (
	snellV6HardenedMidPSK  = "DnkcNIVi2DdZSJ7T93njADhLHrnLlBmR" // density ≈ 0.680
	snellV6HardenedHighPSK = "zezjz37_ZOtdOvNcb9zpyK3jg4E-y12_" // density ≈ 0.736
)

// TestSnellV6HardenedPresetSeeds verifies the built-in v6 hardening ladder:
// 标准 (low, random PSK), 加固-中 (mid, density-verified PSK), 加固-高
// (high, density-verified PSK with the widest batching-dilution margin).
func TestSnellV6HardenedPresetSeeds(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	profiles, err := db.ListSnellProfiles(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byName := make(map[string]model.SnellProfile, len(profiles))
	for _, p := range profiles {
		byName[p.Name] = p
	}

	mid, ok := byName["Snell v6 加固-中（抗 DPI）"]
	if !ok {
		t.Fatalf("hardened-mid preset missing; got %d profiles", len(profiles))
	}
	if !mid.Builtin || !mid.Enabled {
		t.Fatalf("hardened-mid preset must be builtin and enabled")
	}
	if mid.Version != 6 || mid.Mode != "default" || mid.ObfsMode != "none" {
		t.Fatalf("hardened-mid preset wrong parameters: %+v", mid)
	}
	if mid.PSK != snellV6HardenedMidPSK {
		t.Fatalf("hardened-mid PSK drifted from the density-verified value; re-verify against sing-snell v6 before changing")
	}

	high, ok := byName["Snell v6 加固-高（抗 DPI）"]
	if !ok {
		t.Fatalf("hardened-high preset missing")
	}
	if !high.Builtin || !high.Enabled {
		t.Fatalf("hardened-high preset must be builtin and enabled")
	}
	if high.Version != 6 || high.Mode != "default" || high.ObfsMode != "none" {
		t.Fatalf("hardened-high preset wrong parameters: %+v", high)
	}
	if high.PSK != snellV6HardenedHighPSK {
		t.Fatalf("hardened-high PSK drifted from the density-verified value; re-verify against sing-snell v6 before changing")
	}

	standard, ok := byName["Snell v6 标准"]
	if !ok {
		t.Fatalf("standard v6 preset missing")
	}
	if standard.PSK != "" {
		t.Fatalf("standard v6 preset must keep an empty PSK (random generation)")
	}
}

// TestSnellV6LegacyTestTagRename verifies that databases seeded with the
// legacy "（测试）"-suffixed v6 preset names are renamed to the current names
// without duplicating rows.
func TestSnellV6LegacyTestTagRename(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ctx := context.Background()

	// Simulate a legacy database by renaming the current rows back.
	for _, rename := range [][2]string{
		{"Snell v6 标准", "Snell v6 标准（测试）"},
		{"Snell v6 unshaped", "Snell v6 unshaped（测试）"},
		{"Snell v6 unsafe-raw", "Snell v6 unsafe-raw（测试）"},
	} {
		if _, err := db.db.ExecContext(ctx, `update snell_profiles set name=? where builtin=1 and name=?`, rename[1], rename[0]); err != nil {
			t.Fatalf("seed legacy name: %v", err)
		}
	}

	// Re-run the migration by calling the store migration path directly.
	if err := db.migrateSnellV6StandardRemark(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	profiles, err := db.ListSnellProfiles(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byName := make(map[string]model.SnellProfile, len(profiles))
	for _, p := range profiles {
		byName[p.Name] = p
	}
	for _, name := range []string{"Snell v6 标准", "Snell v6 unshaped", "Snell v6 unsafe-raw"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("renamed preset %q missing after migration", name)
		}
	}
	for _, legacy := range []string{"Snell v6 标准（测试）", "Snell v6 unshaped（测试）", "Snell v6 unsafe-raw（测试）"} {
		if _, ok := byName[legacy]; ok {
			t.Fatalf("legacy preset %q still present after migration", legacy)
		}
	}
}
