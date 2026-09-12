package store

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
)

func TestConfigurationChainSchemaBaseline(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "baseline.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var sqliteVersion, journal string
	var userVersion, synchronous int
	for query, target := range map[string]any{
		"select sqlite_version()": &sqliteVersion,
		"pragma user_version":     &userVersion,
		"pragma journal_mode":     &journal,
		"pragma synchronous":      &synchronous,
	} {
		if err := s.db.QueryRow(query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.db.Query(`select type,name,coalesce(sql,'') from sqlite_master order by type,name`)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	for rows.Next() {
		var kind, name, sql string
		if err := rows.Scan(&kind, &name, &sql); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%s\x00%s\x00%s\n", kind, name, sql)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	t.Logf("sqlite=%s user_version=%d journal=%s synchronous=%d schema_sha256=%x", sqliteVersion, userVersion, journal, synchronous, hash.Sum(nil))
	plan, err := s.db.Query(`explain query plan delete from server_metric_samples where server_id=?`, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	for plan.Next() {
		var id, parent, unused int
		var detail string
		if err := plan.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Logf("metric_cleanup_plan=%s", detail)
	}
	if err := plan.Err(); err != nil {
		t.Fatal(err)
	}
}

func BenchmarkServerDeleteHistory(b *testing.B) {
	for _, historyRows := range []int{0, 10000, 100000} {
		b.Run(fmt.Sprintf("metrics_%d", historyRows), func(b *testing.B) {
			b.ReportAllocs()
			b.StopTimer()
			s, err := Open(filepath.Join(b.TempDir(), "delete.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			for i := 0; i < b.N; i++ {
				server := &model.Server{Name: fmt.Sprintf("delete-%d", i), Status: model.ServerOffline, ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 20000}
				if err := s.CreateServer(ctx, server); err != nil {
					b.Fatal(err)
				}
				if historyRows > 0 {
					_, err = s.db.Exec(`with recursive samples(n) as (select 1 union all select n+1 from samples where n<?) insert into server_metric_samples(server_id,sampled_at) select ?,printf('2026-09-13T00:00:%06dZ',n) from samples`, historyRows, server.ID)
					if err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				err = s.DeleteServer(ctx, server.ID)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
