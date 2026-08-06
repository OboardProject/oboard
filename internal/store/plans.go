package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) ListSubscriptionPlans(ctx context.Context) ([]model.SubscriptionPlan, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,description,enabled,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,revision,active_revision,draft_revision,created_at,updated_at from subscription_plans order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionPlans(rows)
}

func (s *Store) GetSubscriptionPlan(ctx context.Context, id int64) (*model.SubscriptionPlan, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,description,enabled,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,revision,active_revision,draft_revision,created_at,updated_at from subscription_plans where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanSubscriptionPlans(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func scanSubscriptionPlans(rows *sql.Rows) ([]model.SubscriptionPlan, error) {
	var out []model.SubscriptionPlan
	for rows.Next() {
		var v model.SubscriptionPlan
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &enabled, &v.SpeedLimitMbps, &v.TrafficLimitBytes, &v.TrafficResetMode, &v.TrafficResetDay, &v.Revision, &v.ActiveRevision, &v.DraftRevision, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateSubscriptionPlan(ctx context.Context, v *model.SubscriptionPlan) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into subscription_plans(name,description,enabled,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,revision,active_revision,draft_revision,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, v.Name, v.Description, boolInt(v.Enabled), v.SpeedLimitMbps, v.TrafficLimitBytes, v.TrafficResetMode, v.TrafficResetDay, v.Revision, v.ActiveRevision, v.DraftRevision, ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateSubscriptionPlan(ctx context.Context, v *model.SubscriptionPlan) error {
	_, err := s.db.ExecContext(ctx, `update subscription_plans set name=?,description=?,enabled=?,speed_limit_mbps=?,traffic_limit_bytes=?,traffic_reset_mode=?,traffic_reset_day=?,revision=?,active_revision=?,draft_revision=?,updated_at=? where id=?`, v.Name, v.Description, boolInt(v.Enabled), v.SpeedLimitMbps, v.TrafficLimitBytes, v.TrafficResetMode, v.TrafficResetDay, v.Revision, v.ActiveRevision, v.DraftRevision, now(), v.ID)
	return err
}

// DeleteSubscriptionPlan removes the plan; plan nodes and user bindings are
// removed by foreign-key cascades, so affected users fall back to deny-by-default.
func (s *Store) DeleteSubscriptionPlan(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `delete from subscription_plans where id=?`, id)
	return err
}

func (s *Store) ListPlanNodes(ctx context.Context, planID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select id,plan_id,node_type,node_id,display_group,source_type,source_rule_id,enabled,created_at,updated_at from subscription_plan_nodes where plan_id=? order by node_type,node_id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionPlanNodes(rows)
}

func (s *Store) ListAllPlanNodes(ctx context.Context) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select id,plan_id,node_type,node_id,display_group,source_type,source_rule_id,enabled,created_at,updated_at from subscription_plan_nodes where enabled=1 order by plan_id,node_type,node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionPlanNodes(rows)
}

// ListPlansForNode returns enabled plan-node rows referencing a node. The
// returned rows are joined with their (possibly disabled) plan for callers that
// need to distinguish plan-level suspension.
func (s *Store) ListPlansForNode(ctx context.Context, nodeType string, nodeID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select pn.id,pn.plan_id,pn.node_type,pn.node_id,pn.display_group,pn.source_type,pn.source_rule_id,pn.enabled,pn.created_at,pn.updated_at from subscription_plan_nodes pn join subscription_plans p on p.id=pn.plan_id where pn.node_type=? and pn.node_id=? and pn.enabled=1 order by pn.plan_id`, nodeType, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionPlanNodes(rows)
}

func scanSubscriptionPlanNodes(rows *sql.Rows) ([]model.SubscriptionPlanNode, error) {
	var out []model.SubscriptionPlanNode
	for rows.Next() {
		var v model.SubscriptionPlanNode
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.PlanID, &v.NodeType, &v.NodeID, &v.DisplayGroup, &v.SourceType, &v.SourceRuleID, &enabled, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

// AddPlanNodes upserts the given node rows into a plan in one transaction and
// bumps the plan revision once. Existing rows keep their identity but adopt the
// new display group and enabled state.
func (s *Store) AddPlanNodes(ctx context.Context, planID int64, nodes []model.SubscriptionPlanNode) error {
	if len(nodes) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for i := range nodes {
		v := &nodes[i]
		v.PlanID = planID
		v.SourceType = model.PlanNodeSourceExplicit
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_nodes(plan_id,node_type,node_id,display_group,source_type,source_rule_id,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?) on conflict(plan_id,node_type,node_id) do update set display_group=excluded.display_group,enabled=excluded.enabled,updated_at=excluded.updated_at`, v.PlanID, v.NodeType, v.NodeID, v.DisplayGroup, v.SourceType, v.SourceRuleID, boolInt(v.Enabled), ts, ts); err != nil {
			return err
		}
	}
	if err := bumpPlanRevisionTx(ctx, tx, planID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// RemovePlanNodes deletes the given nodes from a plan in one transaction. An
// empty nodeType/nodeID pair matches nothing.
func (s *Store) RemovePlanNodes(ctx context.Context, planID int64, nodes []model.SubscriptionPlanNode) error {
	if len(nodes) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, v := range nodes {
		if _, err := tx.ExecContext(ctx, `delete from subscription_plan_nodes where plan_id=? and node_type=? and node_id=?`, planID, v.NodeType, v.NodeID); err != nil {
			return err
		}
	}
	if err := bumpPlanRevisionTx(ctx, tx, planID, now()); err != nil {
		return err
	}
	return tx.Commit()
}

// ReplacePlanNodes atomically replaces the full node set of a plan and bumps the
// plan revision once. This is the batch operation used for "replace plan nodes".
func (s *Store) ReplacePlanNodes(ctx context.Context, planID int64, nodes []model.SubscriptionPlanNode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `delete from subscription_plan_nodes where plan_id=?`, planID); err != nil {
		return err
	}
	ts := now()
	for i := range nodes {
		v := &nodes[i]
		v.PlanID = planID
		v.SourceType = model.PlanNodeSourceExplicit
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_nodes(plan_id,node_type,node_id,display_group,source_type,source_rule_id,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?)`, v.PlanID, v.NodeType, v.NodeID, v.DisplayGroup, v.SourceType, v.SourceRuleID, boolInt(v.Enabled), ts, ts); err != nil {
			return err
		}
	}
	if err := bumpPlanRevisionTx(ctx, tx, planID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// PublishPlanRevision marks the current draft revision as the active revision.
// The subscription generator switches to the active revision only after it
// consumes plan data; publishing is the explicit two-phase-release boundary.
func (s *Store) PublishPlanRevision(ctx context.Context, planID int64) error {
	_, err := s.db.ExecContext(ctx, `update subscription_plans set active_revision=draft_revision,updated_at=? where id=?`, now(), planID)
	return err
}

func bumpPlanRevisionTx(ctx context.Context, tx *sql.Tx, planID int64, ts string) error {
	res, err := tx.ExecContext(ctx, `update subscription_plans set revision=revision+1,draft_revision=revision+1,updated_at=? where id=?`, ts, planID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("subscription plan %d not found", planID)
	}
	return nil
}

func (s *Store) GetActiveUserPlanBinding(ctx context.Context, userID int64) (*model.UserPlanBinding, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,plan_id,enabled,starts_at,expires_at,assigned_by,created_at,updated_at from user_plan_bindings where user_id=? and enabled=1 order by id desc limit 1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUserPlanBindings(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) ListActiveUserPlanBindings(ctx context.Context) ([]model.UserPlanBinding, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,plan_id,enabled,starts_at,expires_at,assigned_by,created_at,updated_at from user_plan_bindings where enabled=1 order by user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPlanBindings(rows)
}

func (s *Store) ListUserPlanBindingsForPlan(ctx context.Context, planID int64) ([]model.UserPlanBinding, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,plan_id,enabled,starts_at,expires_at,assigned_by,created_at,updated_at from user_plan_bindings where plan_id=? and enabled=1 order by user_id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPlanBindings(rows)
}

func scanUserPlanBindings(rows *sql.Rows) ([]model.UserPlanBinding, error) {
	var out []model.UserPlanBinding
	for rows.Next() {
		var v model.UserPlanBinding
		var enabled int
		var startsAt, expiresAt sql.NullString
		var assignedBy sql.NullInt64
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.UserID, &v.PlanID, &enabled, &startsAt, &expiresAt, &assignedBy, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		if startsAt.Valid {
			t := parseTime(startsAt.String)
			v.StartsAt = &t
		}
		if expiresAt.Valid {
			t := parseTime(expiresAt.String)
			v.ExpiresAt = &t
		}
		if assignedBy.Valid {
			v.AssignedBy = &assignedBy.Int64
		}
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetUserPlanBindings switches each listed user to the given plan in one
// transaction. A binding with PlanID==0 removes the active binding. Exactly one
// enabled binding can exist per user (partial unique index), so each switch
// disables the previous binding before inserting the new one.
func (s *Store) SetUserPlanBindings(ctx context.Context, bindings []model.UserPlanBinding) error {
	if len(bindings) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for _, v := range bindings {
		if _, err := tx.ExecContext(ctx, `update user_plan_bindings set enabled=0,updated_at=? where user_id=? and enabled=1`, ts, v.UserID); err != nil {
			return err
		}
		if v.PlanID == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `insert into user_plan_bindings(user_id,plan_id,enabled,starts_at,expires_at,assigned_by,created_at,updated_at) values(?,?,1,?,?,?,?,?)`, v.UserID, v.PlanID, nilTime(v.StartsAt), nilTime(v.ExpiresAt), v.AssignedBy, ts, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) CreateUserNodeException(ctx context.Context, v *model.UserNodeException) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	res, err := s.db.ExecContext(ctx, `insert into user_node_exceptions(user_id,node_type,node_id,effect,reason,expires_at,created_by,created_at) values(?,?,?,?,?,?,?,?)`, v.UserID, v.NodeType, v.NodeID, v.Effect, v.Reason, v.ExpiresAt.UTC().Format(time.RFC3339Nano), v.CreatedBy, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateUserNodeException(ctx context.Context, v *model.UserNodeException) error {
	_, err := s.db.ExecContext(ctx, `update user_node_exceptions set user_id=?,node_type=?,node_id=?,effect=?,reason=?,expires_at=? where id=?`, v.UserID, v.NodeType, v.NodeID, v.Effect, v.Reason, v.ExpiresAt.UTC().Format(time.RFC3339Nano), v.ID)
	return err
}

func (s *Store) DeleteUserNodeException(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `delete from user_node_exceptions where id=?`, id)
	return err
}

func (s *Store) ListUserNodeExceptions(ctx context.Context) ([]model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,expires_at,created_by,created_at from user_node_exceptions order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

func (s *Store) ListUserNodeExceptionsForUser(ctx context.Context, userID int64) ([]model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,expires_at,created_by,created_at from user_node_exceptions where user_id=? order by id desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

func (s *Store) ListUserNodeExceptionsForNode(ctx context.Context, nodeType string, nodeID int64) ([]model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,expires_at,created_by,created_at from user_node_exceptions where node_type=? and node_id=? order by id desc`, nodeType, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

// DeleteExpiredUserNodeExceptions removes exceptions whose expiry has passed.
// It returns the number of removed rows.
func (s *Store) DeleteExpiredUserNodeExceptions(ctx context.Context, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `delete from user_node_exceptions where expires_at <= ?`, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanUserNodeExceptions(rows *sql.Rows) ([]model.UserNodeException, error) {
	var out []model.UserNodeException
	for rows.Next() {
		var v model.UserNodeException
		var createdBy sql.NullInt64
		var expiresAt, ca string
		if err := rows.Scan(&v.ID, &v.UserID, &v.NodeType, &v.NodeID, &v.Effect, &v.Reason, &expiresAt, &createdBy, &ca); err != nil {
			return nil, err
		}
		v.ExpiresAt = parseTime(expiresAt)
		if createdBy.Valid {
			v.CreatedBy = &createdBy.Int64
		}
		v.CreatedAt = parseTime(ca)
		out = append(out, v)
	}
	return out, rows.Err()
}
