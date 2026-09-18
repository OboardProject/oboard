package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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

func TestSnellV6LegacyTestTagRename(t *testing.T) {
	for _, state := range []string{"previous-release", "failed-upgrade", "custom-name-collision"} {
		t.Run(state, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "oboard.sqlite")
			db, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if db != nil {
					db.Close()
				}
			}()
			ctx := context.Background()
			fixture, err := os.ReadFile("testdata/snell_profiles_52ae6bd.sql")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.ExecContext(ctx, `delete from snell_profiles; delete from sqlite_sequence where name='snell_profiles';`+string(fixture)); err != nil {
				t.Fatal(err)
			}
			server := model.Server{Name: "migration-node", ChainSecret: "migration-test", Status: model.ServerOffline}
			if err := db.CreateServer(ctx, &server); err != nil {
				t.Fatal(err)
			}
			names := []string{"Snell v6 标准", "Snell v6 unshaped", "Snell v6 unsafe-raw"}
			ids := make(map[string]int64)
			for i, name := range names {
				var id int64
				if err := db.db.QueryRowContext(ctx, `select id from snell_profiles where name=?`, name+"（测试）").Scan(&id); err != nil {
					t.Fatal(err)
				}
				ids[name] = id
				config := fmt.Sprintf(`{"version":6,"snell_profile_id":%d,"psk":"migration-test-psk"}`, id)
				if _, err := db.db.ExecContext(ctx, `insert into inbounds(server_id,name,protocol,listen_ip,port,config_json,created_at,updated_at) values(?,?,'snell','0.0.0.0',?,?, '2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, server.ID, name, 6100+i, config); err != nil {
					t.Fatal(err)
				}
				if state != "previous-release" {
					builtin := 1
					if state == "custom-name-collision" {
						builtin = 0
					}
					if _, err := db.db.ExecContext(ctx, `insert into snell_profiles(name,version,psk,mode,remark,builtin,created_at,updated_at) select ?,version,'collision-test-psk',mode,'preserve collision',?,created_at,updated_at from snell_profiles where id=?`, name, builtin, id); err != nil {
						t.Fatal(err)
					}
				}
			}
			for reopen := 0; reopen < 2; reopen++ {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				db, err = Open(path)
				if err != nil {
					t.Fatalf("reopen %d: %v", reopen, err)
				}
				var count int
				if err := db.db.QueryRowContext(ctx, `select count(*) from snell_profiles`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				wantCount := 7
				if state != "previous-release" {
					wantCount += 3
				}
				if count != wantCount {
					t.Fatalf("profile count = %d, want %d", count, wantCount)
				}
				for _, name := range names {
					profile, err := db.GetSnellProfile(ctx, ids[name])
					if err != nil {
						t.Fatal(err)
					}
					wantName := name
					if state != "previous-release" {
						wantName += "（测试）"
					}
					if profile.Name != wantName || profile.PSK != "" {
						t.Fatalf("original profile changed: %+v", profile)
					}
					var reference int64
					if err := db.db.QueryRowContext(ctx, `select json_extract(config_json,'$.snell_profile_id') from inbounds where name=?`, name).Scan(&reference); err != nil {
						t.Fatal(err)
					}
					if reference != ids[name] {
						t.Fatalf("inbound reference changed: %d", reference)
					}
					if state != "previous-release" {
						var psk, remark string
						if err := db.db.QueryRowContext(ctx, `select psk,remark from snell_profiles where name=?`, name).Scan(&psk, &remark); err != nil {
							t.Fatal(err)
						}
						if psk != "collision-test-psk" || remark != "preserve collision" {
							t.Fatal("colliding profile overwritten")
						}
					}
				}
			}
		})
	}
}
