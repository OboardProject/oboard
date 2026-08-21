package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestServerExpiryColumnsMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "server-expiry.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &model.Server{Name: "expiry-node", AgentID: "expiry-agent", AgentTokenHash: "expiry-token", Status: model.ServerOnline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"expires_at", "renewal_cycle", "auto_renew_enabled", "expiry_notify_enabled", "last_auto_renewed_at"} {
		if _, err := raw.Exec(`alter table server_telemetry drop column ` + column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatalf("open with previous expiry schema: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err := s.CheckHealth(ctx); err != nil {
		t.Fatalf("health check after migration: %v", err)
	}

	columns := map[string]bool{}
	rows, err := s.db.QueryContext(ctx, `select name from pragma_table_info('server_telemetry')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"expires_at", "renewal_cycle", "auto_renew_enabled", "expiry_notify_enabled", "last_auto_renewed_at"} {
		if !columns[column] {
			t.Errorf("missing migrated column %q", column)
		}
	}

	var cycle string
	var autoRenew, expiryNotify int
	var expiresAt, lastRenewed sql.NullString
	if err := s.db.QueryRowContext(ctx, `select expires_at,renewal_cycle,auto_renew_enabled,expiry_notify_enabled,last_auto_renewed_at from server_telemetry where server_id=?`, server.ID).Scan(&expiresAt, &cycle, &autoRenew, &expiryNotify, &lastRenewed); err != nil {
		t.Fatal(err)
	}
	if cycle != "monthly" || autoRenew != 0 || expiryNotify != 1 || expiresAt.Valid || lastRenewed.Valid {
		t.Fatalf("migrated defaults = cycle=%q auto=%d notify=%d expires=%v last=%v", cycle, autoRenew, expiryNotify, expiresAt.Valid, lastRenewed.Valid)
	}
}

func TestServerExpiryRoundTripAndRenewalState(t *testing.T) {
	ctx := context.Background()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	server := &model.Server{Name: "lease", AgentID: "lease-agent", AgentTokenHash: "lease-token", Status: model.ServerOnline, AutoRenewEnabled: true, ExpiryNotifyEnabled: false, RenewalCycle: model.ServerRenewalCycleQuarterly}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	expiry := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := s.ExtendServerExpiry(ctx, server.ID, expiry); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(expiry) {
		t.Fatalf("expiry after extend = %v, want %v", stored.ExpiresAt, expiry)
	}
	if stored.RenewalCycle != model.ServerRenewalCycleQuarterly || stored.AutoRenewEnabled != true || stored.ExpiryNotifyEnabled != false {
		t.Fatalf("expiry settings changed unexpectedly: %#v", stored)
	}

	renewedAt := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	if err := s.MarkServerAutoRenewed(ctx, server.ID, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), renewedAt); err != nil {
		t.Fatal(err)
	}
	stored, err = s.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExpiresAt == nil || !stored.ExpiresAt.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("expiry after auto renew = %v", stored.ExpiresAt)
	}
	if stored.LastAutoRenewedAt == nil || !stored.LastAutoRenewedAt.Equal(renewedAt) {
		t.Fatalf("last auto renewed = %v, want %v", stored.LastAutoRenewedAt, renewedAt)
	}
}
