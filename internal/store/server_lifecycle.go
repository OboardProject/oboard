package store

import (
	"context"

	"github.com/OboardProject/oboard/internal/model"
)

// DeleteServer commits routing, plan, inbound and telemetry removal together.
func (s *Store) DeleteServer(ctx context.Context, serverID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := bumpLatencyRollupGeneration(ctx, tx); err != nil {
		return err
	}
	inboundIDs, err := queryInt64sTx(ctx, tx, `select id from inbounds where server_id=?`, serverID)
	if err != nil {
		return err
	}
	if _, err := removeAssignableNodeFromPlansTx(ctx, tx, model.AssignableNodeInbound, inboundIDs...); err != nil {
		return err
	}
	if err := cleanupRoutingForServerTx(ctx, tx, serverID); err != nil {
		return err
	}
	for _, inboundID := range inboundIDs {
		if err := deleteProxyPathsForInboundTx(ctx, tx, inboundID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `delete from inbound_probe_results where inbound_id=?`, inboundID); err != nil {
			return err
		}
	}
	for _, query := range []string{
		`delete from server_metric_samples where server_id=?`,
		`delete from server_connectivity_events where server_id=?`,
		`delete from server_telemetry where server_id=?`,
		`delete from inbounds where server_id=?`,
		`delete from servers where id=?`,
	} {
		if _, err := tx.ExecContext(ctx, query, serverID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateLatencyHistory(serverID)
	return nil
}
