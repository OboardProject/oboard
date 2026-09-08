package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestCreateServerRollsBackInitializationFailure(t *testing.T) {
	for _, stage := range []string{"server_telemetry", "server_latency_probe_settings", "server_dns_policies", "initial_traffic", "missing_dns_default"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			query := `create trigger fail_create before insert on ` + stage + ` begin select raise(abort, 'injected failure'); end`
			if stage == "initial_traffic" {
				query = `create trigger fail_create before update of period_key on server_telemetry begin select raise(abort, 'injected failure'); end`
			} else if stage == "missing_dns_default" {
				query = `update dns_lists set enabled=0 where protected=1`
			}
			if _, err := db.db.ExecContext(ctx, query); err != nil {
				t.Fatal(err)
			}
			revision, _ := db.ConfigurationRevision(ctx)
			server := model.Server{Name: "rollback", LatencyProbeEnabled: true}
			window := model.ServerTrafficWindow{Key: "2026-09-01", Start: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
			if err := db.CreateServerWithTraffic(ctx, &server, 1234, window); err == nil {
				t.Fatal("expected initialization failure")
			}
			if server.ID != 0 {
				t.Fatalf("failed create returned id %d", server.ID)
			}
			for _, table := range []string{"servers", "server_telemetry", "server_latency_probe_settings", "server_connectivity_events", "server_dns_policies"} {
				var count int
				if err := db.db.QueryRowContext(ctx, `select count(*) from `+table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("partial creation in %s: count=%d err=%v", table, count, err)
				}
			}
			if after, _ := db.ConfigurationRevision(ctx); after != revision {
				t.Fatalf("failed creation advanced revision from %d to %d", revision, after)
			}
			if stage == "missing_dns_default" {
				_, err = db.db.ExecContext(ctx, `update dns_lists set enabled=1 where protected=1`)
			} else {
				_, err = db.db.ExecContext(ctx, `drop trigger fail_create`)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := db.SQLWriteTransactionCount()
			if err := db.CreateServerWithTraffic(ctx, &server, 1234, window); err != nil {
				t.Fatal(err)
			}
			if count := db.SQLWriteTransactionCount() - before; count != 1 {
				t.Fatalf("creation used %d transactions, want 1", count)
			}
			stored, err := db.GetServer(ctx, server.ID)
			if err != nil || stored.TrafficUploadBytes != 1234 || !stored.LatencyProbeEnabled {
				t.Fatalf("created server=%+v err=%v", stored, err)
			}
		})
	}
}

func TestDeleteServerRollsBackRoutingAndTelemetry(t *testing.T) {
	ctx := context.Background()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, inboundID, _, _ := proxyPathTruncationFixture(t, db)
	inbound, err := db.GetInbound(ctx, inboundID)
	if err != nil {
		t.Fatal(err)
	}
	serverID := inbound.ServerID
	if _, err := db.db.ExecContext(ctx, `create trigger fail_delete before delete on servers begin select raise(abort, 'injected failure'); end`); err != nil {
		t.Fatal(err)
	}
	revision, _ := db.ConfigurationRevision(ctx)
	if err := db.DeleteServer(ctx, serverID); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("delete error=%v", err)
	}
	steps, err := db.ListProxyPathSteps(ctx)
	if err != nil || len(steps) != 2 {
		t.Fatalf("failed delete truncated routing: steps=%d err=%v", len(steps), err)
	}
	for _, table := range []string{"server_telemetry", "server_latency_probe_settings", "server_dns_policies", "inbounds"} {
		var count int
		if err := db.db.QueryRowContext(ctx, `select count(*) from `+table+` where server_id=?`, serverID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("failed delete changed %s: count=%d err=%v", table, count, err)
		}
	}
	if after, _ := db.ConfigurationRevision(ctx); after != revision {
		t.Fatalf("failed delete advanced revision from %d to %d", revision, after)
	}
	if _, err := db.db.ExecContext(ctx, `drop trigger fail_delete`); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteServer(ctx, serverID); err != nil {
		t.Fatal(err)
	}
	steps, err = db.ListProxyPathSteps(ctx)
	if err != nil || len(steps) != 0 {
		t.Fatalf("deleted server left path steps=%d err=%v", len(steps), err)
	}
	if _, err := db.GetServer(ctx, serverID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("deleted server still exists")
	}
	if err := db.DeleteServer(ctx, serverID); err != nil {
		t.Fatalf("retry deletion: %v", err)
	}
}
