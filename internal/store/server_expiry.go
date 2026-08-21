package store

import (
	"context"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// ExtendServerExpiry advances a server's expiration date without touching the
// servers table or its routing/configuration revision triggers.
func (s *Store) ExtendServerExpiry(ctx context.Context, serverID int64, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `insert into server_telemetry(server_id,expires_at,renewal_cycle,auto_renew_enabled,expiry_notify_enabled,last_auto_renewed_at,updated_at) values(?,?,'monthly',0,1,NULL,?)
		on conflict(server_id) do update set expires_at=excluded.expires_at,updated_at=excluded.updated_at`,
		serverID, expiresAt.UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

// MarkServerAutoRenewed stores the automatically renewed expiration date and
// the moment the Controller performed the renewal.
func (s *Store) MarkServerAutoRenewed(ctx context.Context, serverID int64, nextExpiresAt, renewedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `insert into server_telemetry(server_id,expires_at,renewal_cycle,auto_renew_enabled,expiry_notify_enabled,last_auto_renewed_at,updated_at) values(?,?,'monthly',1,1,?,?)
		on conflict(server_id) do update set expires_at=excluded.expires_at,last_auto_renewed_at=excluded.last_auto_renewed_at,updated_at=excluded.updated_at`,
		serverID, nextExpiresAt.UTC().Format(time.RFC3339Nano), renewedAt.UTC().Format(time.RFC3339Nano), renewedAt.UTC().Format(time.RFC3339Nano))
	return err
}

// ServerRenewalCycle returns a normalized renewal cycle from a stored server.
func ServerRenewalCycle(value model.ServerRenewalCycle) model.ServerRenewalCycle {
	return normalizeRenewalCycle(value)
}
