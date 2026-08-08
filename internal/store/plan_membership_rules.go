package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) ListPlanRevisionMembershipPolicy(ctx context.Context, revisionID int64) ([]model.PlanMembershipRule, []model.PlanNodeExclusion, error) {
	rules, err := listPlanRevisionRules(ctx, s.db, revisionID)
	if err != nil {
		return nil, nil, err
	}
	exclusions, err := listPlanRevisionExclusions(ctx, s.db, revisionID)
	return rules, exclusions, err
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listPlanRevisionRules(ctx context.Context, q queryer, revisionID int64) ([]model.PlanMembershipRule, error) {
	rows, err := q.QueryContext(ctx, `select id,revision_id,rule_id,kind,scope_key,created_at from subscription_plan_revision_rules where revision_id=? order by rule_id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PlanMembershipRule{}
	for rows.Next() {
		var item model.PlanMembershipRule
		var createdAt string
		if err := rows.Scan(&item.ID, &item.RevisionID, &item.RuleID, &item.Kind, &item.ScopeKey, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func listPlanRevisionExclusions(ctx context.Context, q queryer, revisionID int64) ([]model.PlanNodeExclusion, error) {
	rows, err := q.QueryContext(ctx, `select revision_id,node_type,node_id,created_at from subscription_plan_revision_node_exclusions where revision_id=? order by node_type,node_id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PlanNodeExclusion{}
	for rows.Next() {
		var item model.PlanNodeExclusion
		var createdAt string
		if err := rows.Scan(&item.RevisionID, &item.NodeType, &item.NodeID, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func insertPlanRevisionMembershipPolicyTx(ctx context.Context, tx *sql.Tx, revisionID int64, rules []model.PlanMembershipRule, exclusions []model.PlanNodeExclusion, ts string) error {
	for _, item := range rules {
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_rules(revision_id,rule_id,kind,scope_key,created_at) values(?,?,?,?,?)`, revisionID, item.RuleID, item.Kind, item.ScopeKey, ts); err != nil {
			return err
		}
	}
	for _, item := range exclusions {
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_node_exclusions(revision_id,node_type,node_id,created_at) values(?,?,?,?)`, revisionID, item.NodeType, item.NodeID, ts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetPlanRuleReconcileState(ctx context.Context, planID int64) (*model.PlanRuleReconcileState, error) {
	var item model.PlanRuleReconcileState
	var reconciled sql.NullString
	var updated string
	err := s.db.QueryRowContext(ctx, `select plan_id,catalog_digest,desired_digest,status,last_error,last_reconciled_at,updated_at from subscription_plan_rule_reconcile_states where plan_id=?`, planID).Scan(&item.PlanID, &item.CatalogDigest, &item.DesiredDigest, &item.Status, &item.LastError, &reconciled, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if reconciled.Valid {
		value := parseTime(reconciled.String)
		item.LastReconciledAt = &value
	}
	item.UpdatedAt = parseTime(updated)
	return &item, nil
}

func (s *Store) SetPlanRuleReconcileState(ctx context.Context, item model.PlanRuleReconcileState) error {
	var reconciled any
	if item.LastReconciledAt != nil {
		reconciled = item.LastReconciledAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `insert into subscription_plan_rule_reconcile_states(plan_id,catalog_digest,desired_digest,status,last_error,last_reconciled_at,updated_at) values(?,?,?,?,?,?,?) on conflict(plan_id) do update set catalog_digest=excluded.catalog_digest,desired_digest=excluded.desired_digest,status=excluded.status,last_error=excluded.last_error,last_reconciled_at=excluded.last_reconciled_at,updated_at=excluded.updated_at`, item.PlanID, item.CatalogDigest, item.DesiredDigest, item.Status, item.LastError, reconciled, now())
	return err
}
