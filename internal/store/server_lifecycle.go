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
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`delete from server_metric_samples where server_id=?`, []any{serverID}},
		{`delete from server_connectivity_events where server_id=?`, []any{serverID}},
		{`delete from server_telemetry where server_id=?`, []any{serverID}},
		// Incidents and their publication isolations carry server_id without a
		// foreign key, so nothing removes them with the server. An open
		// incident for a node that no longer exists stays active forever: it
		// keeps showing in the console, keeps its isolations applied, and keeps
		// feeding notifications about a server the operator already deleted.
		// Isolations go first because they reference the incident with
		// `on delete restrict`.
		{`delete from node_publication_isolations where server_id=? or incident_id in (select id from node_incidents where server_id=?)`, []any{serverID, serverID}},
		{`delete from node_incidents where server_id=?`, []any{serverID}},
		{`delete from inbounds where server_id=?`, []any{serverID}},
		{`delete from servers where id=?`, []any{serverID}},
	} {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.invalidateLatencyHistory(serverID)
	return nil
}
