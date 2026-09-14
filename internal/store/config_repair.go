package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// This file is the only write path that may correct a record whose stored state
// the normal management writes already reject.
//
// CreateInbound and UpdateInbound validate the whole record before persisting
// it. That is correct for management writes, and it is exactly why a row that
// is already invalid cannot be repaired through them: the operator's correction
// is refused together with the pre-existing defect. The repair writes below
// gate on the *resulting* state instead of the current one, so a document can
// move from invalid to valid but never from valid to invalid.

// ErrRepairNotAnImprovement reports a repair whose result would still be
// rejected. The caller keeps the original row and the finding stays open.
var ErrRepairNotAnImprovement = errors.New("repaired configuration is still invalid")

// RepairInboundConfigJSON replaces one inbound's protocol document.
//
// The replacement is validated on its own. Nothing about the current stored
// document is consulted, which is the whole point: the row being unreadable by
// the normal validator is the condition this exists for.
func (s *Store) RepairInboundConfigJSON(ctx context.Context, inboundID int64, configJSON string) error {
	inbound, err := s.getInboundForRepair(ctx, inboundID)
	if err != nil {
		return err
	}
	candidate := inbound
	candidate.ConfigJSON = configJSON
	if err := core.ValidateStoredInbound(candidate); err != nil {
		return fmt.Errorf("%w: %v", ErrRepairNotAnImprovement, err)
	}
	res, err := s.db.ExecContext(ctx, `update inbounds set config_json=?,updated_at=? where id=?`, configJSON, now(), inboundID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// SetInboundEnabledForRepair flips an inbound off (or back on) without
// re-validating the document. Disabling is the escape hatch for a record that
// cannot be normalized: the rest of the fleet has to be able to deploy again
// while the operator decides what to do with this one.
func (s *Store) SetInboundEnabledForRepair(ctx context.Context, inboundID int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `update inbounds set enabled=?,updated_at=? where id=?`, boolInt(enabled), now(), inboundID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// SetProxyPathEnabledForRepair flips a proxy path off (or back on).
func (s *Store) SetProxyPathEnabledForRepair(ctx context.Context, pathID int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `update proxy_paths set enabled=?,updated_at=? where id=?`, boolInt(enabled), now(), pathID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// SetRoutingRuleEnabledForRepair flips a routing rule off (or back on).
func (s *Store) SetRoutingRuleEnabledForRepair(ctx context.Context, ruleID int64, enabled bool) error {
	res, err := s.db.ExecContext(ctx, `update routing_rules set enabled=?,updated_at=? where id=?`, boolInt(enabled), now(), ruleID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// DeleteProxyPathStepForRepair removes one step. It is used only for a step
// whose owning path no longer exists, so there is no surviving topology to keep
// consistent.
func (s *Store) DeleteProxyPathStepForRepair(ctx context.Context, stepID int64) error {
	res, err := s.db.ExecContext(ctx, `delete from proxy_path_steps where id=?`, stepID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// DeleteRoutingRuleForRepair removes one routing rule.
func (s *Store) DeleteRoutingRuleForRepair(ctx context.Context, ruleID int64) error {
	res, err := s.db.ExecContext(ctx, `delete from routing_rules where id=?`, ruleID)
	if err != nil {
		return err
	}
	return requireOneRow(res)
}

// ProxyPathReferenceCount reports how many live rows still point at a proxy
// path. A delete repair refuses while this is non-zero, so cleaning up one
// orphan can never break a topology that is still in use.
func (s *Store) ProxyPathReferenceCount(ctx context.Context, pathID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		select
			(select count(*) from routing_rules where target_proxy_path_id=? or proxy_path_id=?) +
			(select count(*) from proxy_paths where branch_source_step_id in (select id from proxy_path_steps where path_id=?))
	`, pathID, pathID, pathID).Scan(&count)
	return count, err
}

// getInboundForRepair reads the columns the repair validation needs. It
// deliberately does not go through ListInbounds: the repair path must work on a
// row whose joined display state may itself be incomplete.
func (s *Store) getInboundForRepair(ctx context.Context, inboundID int64) (model.Inbound, error) {
	var v model.Inbound
	var enabled, tls int
	var certificateID sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		select id,server_id,name,protocol,listen_ip,port,tls,config_json,enabled,
			coalesce((select mode from inbound_certificate_bindings where inbound_id=inbounds.id),''),
			(select certificate_id from inbound_certificate_bindings where inbound_id=inbounds.id),
			coalesce((select server_name from inbound_certificate_bindings where inbound_id=inbounds.id),'')
		from inbounds where id=?`, inboundID).
		Scan(&v.ID, &v.ServerID, &v.Name, &v.Protocol, &v.ListenIP, &v.Port, &tls, &v.ConfigJSON, &enabled,
			&v.CertificateMode, &certificateID, &v.CertificateDomain)
	if err != nil {
		return model.Inbound{}, err
	}
	v.TLS = tls != 0
	v.Enabled = enabled != 0
	if certificateID.Valid {
		id := certificateID.Int64
		v.CertificateID = &id
	}
	return v, nil
}

func requireOneRow(res sql.Result) error {
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}
