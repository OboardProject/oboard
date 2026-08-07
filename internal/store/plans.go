package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// ErrPlanRevisionConflict is returned when a plan mutation carries an
// expected_revision that no longer matches the stored plan revision. Callers
// must re-preview before retrying.
var ErrPlanRevisionConflict = errors.New("plan revision conflict: the plan changed since preview; re-preview and retry")

const planSelectSQL = `select p.id,p.name,p.description,p.enabled,p.revision,coalesce(p.active_revision_id,0),coalesce(p.draft_revision_id,0),p.created_at,p.updated_at,
	coalesce(ar.speed_limit_mbps,0),coalesce(ar.traffic_limit_bytes,0),coalesce(ar.traffic_reset_mode,'monthly'),coalesce(ar.traffic_reset_day,1) from subscription_plans p left join subscription_plan_revisions ar on ar.id=p.active_revision_id`

func (s *Store) ListSubscriptionPlans(ctx context.Context) ([]model.SubscriptionPlan, error) {
	rows, err := s.db.QueryContext(ctx, planSelectSQL+` order by p.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionPlans(rows)
}

func (s *Store) GetSubscriptionPlan(ctx context.Context, id int64) (*model.SubscriptionPlan, error) {
	rows, err := s.db.QueryContext(ctx, planSelectSQL+` where p.id=?`, id)
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
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &enabled, &v.Revision, &v.ActiveRevisionID, &v.DraftRevisionID, &ca, &ua, &v.SpeedLimitMbps, &v.TrafficLimitBytes, &v.TrafficResetMode, &v.TrafficResetDay); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

// CreateSubscriptionPlan creates the plan, its first active revision and the
// revision node set in one transaction. Any validation failure leaves nothing
// behind.
func (s *Store) CreateSubscriptionPlan(ctx context.Context, v *model.SubscriptionPlan, nodes []model.SubscriptionPlanNode) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.ExecContext(ctx, `insert into subscription_plans(name,description,enabled,revision,created_at,updated_at) values(?,?,?,1,?,?)`, v.Name, v.Description, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	planID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	revisionID, err := insertPlanRevisionTx(ctx, tx, planID, 1, model.PlanRevisionActive, v.SpeedLimitMbps, v.TrafficLimitBytes, v.TrafficResetMode, v.TrafficResetDay, nil, ts, ts)
	if err != nil {
		return err
	}
	if err := insertPlanRevisionNodesTx(ctx, tx, revisionID, nodes, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set active_revision_id=? where id=?`, revisionID, planID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	v.ID = planID
	v.ActiveRevisionID = revisionID
	v.Revision = 1
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	return nil
}

func insertPlanRevisionTx(ctx context.Context, tx *sql.Tx, planID, revision int64, status model.PlanRevisionStatus, speed int, traffic int64, mode string, day int, createdBy *int64, createdAt, activatedAt string) (int64, error) {
	if strings.TrimSpace(mode) == "" {
		mode = "monthly"
	}
	if day <= 0 {
		day = 1
	}
	res, err := tx.ExecContext(ctx, `insert into subscription_plan_revisions(plan_id,revision,status,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_by,created_at,activated_at) values(?,?,?,?,?,?,?,?,?,?)`, planID, revision, string(status), speed, traffic, mode, day, createdBy, createdAt, nullEmpty(activatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func insertPlanRevisionNodesTx(ctx context.Context, tx *sql.Tx, revisionID int64, nodes []model.SubscriptionPlanNode, ts string) error {
	for _, v := range nodes {
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,created_at) values(?,?,?,?,?,?,?)`, revisionID, v.NodeType, v.NodeID, v.DisplayGroup, v.SourceType, v.SourceRuleID, ts); err != nil {
			return err
		}
	}
	return nil
}

// UpdateSubscriptionPlanMeta updates name/description/enabled on the plan row.
// Enabled is a plan-level state; limits and nodes live in revisions. Any change
// bumps the plan revision so stale previews conflict.
func (s *Store) UpdateSubscriptionPlanMeta(ctx context.Context, id, expectedRevision int64, name, description string, enabled *bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkPlanRevisionTx(ctx, tx, id, expectedRevision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set name=?,description=?,enabled=?,revision=revision+1,updated_at=? where id=?`, name, description, boolInt(enabled == nil || *enabled), now(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdatePlanDraftLimits changes the plan limits through the draft revision. It
// returns the draft revision id.
func (s *Store) UpdatePlanDraftLimits(ctx context.Context, id, expectedRevision int64, speed int, traffic int64, mode string, day int) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := checkPlanRevisionTx(ctx, tx, id, expectedRevision); err != nil {
		return 0, err
	}
	draftID, err := ensurePlanDraftTx(ctx, tx, id, now())
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set speed_limit_mbps=?,traffic_limit_bytes=?,traffic_reset_mode=?,traffic_reset_day=? where id=?`, speed, traffic, mode, day, draftID); err != nil {
		return 0, err
	}
	if err := bumpPlanRevisionTx(ctx, tx, id, now()); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return draftID, nil
}

// DeleteSubscriptionPlan physically removes the plan. Callers must first ensure
// there are no plan bindings; the foreign-key cascade then removes revisions,
// revision nodes and bindings together.
func (s *Store) DeleteSubscriptionPlan(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `delete from subscription_plans where id=?`, id)
	return err
}

// CloneSubscriptionPlan copies the active revision (limits and nodes) into a
// new enabled plan named newName.
func (s *Store) CloneSubscriptionPlan(ctx context.Context, id int64, newName string) (*model.SubscriptionPlan, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now()
	var activeRevisionID int64
	if err := tx.QueryRowContext(ctx, `select active_revision_id from subscription_plans where id=?`, id).Scan(&activeRevisionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	if activeRevisionID == 0 {
		return nil, fmt.Errorf("subscription plan %d has no active revision", id)
	}
	var speed int
	var traffic int64
	var mode string
	var day int
	var createdBy sql.NullInt64
	if err := tx.QueryRowContext(ctx, `select speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_by from subscription_plan_revisions where id=?`, activeRevisionID).Scan(&speed, &traffic, &mode, &day, &createdBy); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `insert into subscription_plans(name,description,enabled,revision,created_at,updated_at) values(?,?,1,1,?,?)`, newName, "", ts, ts)
	if err != nil {
		return nil, err
	}
	planID, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	var by *int64
	if createdBy.Valid {
		by = &createdBy.Int64
	}
	revisionID, err := insertPlanRevisionTx(ctx, tx, planID, 1, model.PlanRevisionActive, speed, traffic, mode, day, by, ts, ts)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,created_at) select ?,node_type,node_id,display_group,source_type,source_rule_id,? from subscription_plan_revision_nodes where revision_id=?`, revisionID, ts, activeRevisionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set active_revision_id=? where id=?`, revisionID, planID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetSubscriptionPlan(ctx, planID)
}

// PublishPlanRevision activates the draft revision atomically: the previous
// active revision is archived and the draft becomes active. Returns the new
// active revision id.
func (s *Store) PublishPlanRevision(ctx context.Context, id, expectedRevision int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := checkPlanRevisionTx(ctx, tx, id, expectedRevision); err != nil {
		return 0, err
	}
	ts := now()
	var draftID, activeID int64
	if err := tx.QueryRowContext(ctx, `select draft_revision_id,coalesce(active_revision_id,0) from subscription_plans where id=?`, id).Scan(&draftID, &activeID); err != nil {
		return 0, err
	}
	if draftID == 0 {
		return 0, errors.New("subscription plan has no draft revision to publish")
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set status=?,activated_at=NULL where id=?`, string(model.PlanRevisionArchived), activeID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set status=?,activated_at=? where id=?`, string(model.PlanRevisionActive), ts, draftID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set active_revision_id=?,draft_revision_id=NULL,revision=revision+1,updated_at=? where id=?`, draftID, ts, id); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return draftID, nil
}

func (s *Store) ListPlanRevisions(ctx context.Context, planID int64) ([]model.SubscriptionPlanRevision, error) {
	rows, err := s.db.QueryContext(ctx, `select id,plan_id,revision,status,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_by,created_at,activated_at from subscription_plan_revisions where plan_id=? order by revision desc`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisions(rows)
}

func (s *Store) GetPlanRevision(ctx context.Context, planID, revisionID int64) (*model.SubscriptionPlanRevision, error) {
	rows, err := s.db.QueryContext(ctx, `select id,plan_id,revision,status,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_by,created_at,activated_at from subscription_plan_revisions where id=? and plan_id=?`, revisionID, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanPlanRevisions(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func scanPlanRevisions(rows *sql.Rows) ([]model.SubscriptionPlanRevision, error) {
	var out []model.SubscriptionPlanRevision
	for rows.Next() {
		var v model.SubscriptionPlanRevision
		var createdBy sql.NullInt64
		var ca string
		var activatedAt sql.NullString
		if err := rows.Scan(&v.ID, &v.PlanID, &v.Revision, &v.Status, &v.SpeedLimitMbps, &v.TrafficLimitBytes, &v.TrafficResetMode, &v.TrafficResetDay, &createdBy, &ca, &activatedAt); err != nil {
			return nil, err
		}
		if createdBy.Valid {
			v.CreatedBy = &createdBy.Int64
		}
		v.CreatedAt = parseTime(ca)
		if activatedAt.Valid {
			t := parseTime(activatedAt.String)
			v.ActivatedAt = &t
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListPlanRevisionNodes returns the frozen node set of one revision.
func (s *Store) ListPlanRevisionNodes(ctx context.Context, revisionID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id where n.revision_id=? order by n.node_type,n.node_id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// ListActivePlanNodes returns the node set of the plan's active revision.
func (s *Store) ListActivePlanNodes(ctx context.Context, planID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id join subscription_plans p on p.active_revision_id=r.id where p.id=? order by n.node_type,n.node_id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// ListDraftPlanNodes returns the node set of the plan's draft revision, or an
// empty slice when no draft exists.
func (s *Store) ListDraftPlanNodes(ctx context.Context, planID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id join subscription_plans p on p.draft_revision_id=r.id where p.id=? order by n.node_type,n.node_id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// ListAllPlanNodes returns the active-revision node set of every plan. This is
// the node set the effective access snapshot resolves from.
func (s *Store) ListAllPlanNodes(ctx context.Context) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id join subscription_plans p on p.active_revision_id=r.id where p.enabled=1 order by p.id,n.node_type,n.node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// ListPlansForNode returns active-revision plan-node rows referencing a node.
func (s *Store) ListPlansForNode(ctx context.Context, nodeType string, nodeID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id join subscription_plans p on p.active_revision_id=r.id where n.node_type=? and n.node_id=? order by p.id`, nodeType, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

func scanPlanRevisionNodes(rows *sql.Rows) ([]model.SubscriptionPlanNode, error) {
	var out []model.SubscriptionPlanNode
	for rows.Next() {
		var v model.SubscriptionPlanNode
		var ca string
		if err := rows.Scan(&v.PlanID, &v.ID, &v.RevisionID, &v.NodeType, &v.NodeID, &v.DisplayGroup, &v.SourceType, &v.SourceRuleID, &ca); err != nil {
			return nil, err
		}
		v.Enabled = true
		v.CreatedAt = parseTime(ca)
		out = append(out, v)
	}
	return out, rows.Err()
}

// RestorePlanRevision creates a draft revision from a historical revision
// (limits and nodes) so the operator can publish it after review. Returns the
// new draft revision id.
func (s *Store) RestorePlanRevision(ctx context.Context, planID, revisionID, expectedRevision int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := checkPlanRevisionTx(ctx, tx, planID, expectedRevision); err != nil {
		return 0, err
	}
	ts := now()
	var speed int
	var traffic int64
	var mode string
	var day int
	if err := tx.QueryRowContext(ctx, `select speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day from subscription_plan_revisions where id=? and plan_id=?`, revisionID, planID).Scan(&speed, &traffic, &mode, &day); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, sql.ErrNoRows
		}
		return 0, err
	}
	draftID, err := replacePlanDraftTx(ctx, tx, planID, ts)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set speed_limit_mbps=?,traffic_limit_bytes=?,traffic_reset_mode=?,traffic_reset_day=? where id=?`, speed, traffic, mode, day, draftID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_nodes where revision_id=?`, draftID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,created_at) select ?,node_type,node_id,display_group,source_type,source_rule_id,? from subscription_plan_revision_nodes where revision_id=?`, draftID, ts, revisionID); err != nil {
		return 0, err
	}
	if err := bumpPlanRevisionTx(ctx, tx, planID, ts); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return draftID, nil
}

// SyncPlanDraftNodes applies add/remove/replace to the plan's draft revision
// with optimistic concurrency. The draft is created from the active revision on
// first edit, so the active snapshot stays immutable.
func (s *Store) SyncPlanDraftNodes(ctx context.Context, planID, expectedRevision int64, nodes []model.SubscriptionPlanNode, op string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkPlanRevisionTx(ctx, tx, planID, expectedRevision); err != nil {
		return err
	}
	ts := now()
	draftID, err := ensurePlanDraftTx(ctx, tx, planID, ts)
	if err != nil {
		return err
	}
	switch op {
	case "add":
		for _, v := range nodes {
			if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,created_at) values(?,?,?,?,?,?,?) on conflict(revision_id,node_type,node_id) do update set display_group=excluded.display_group,source_type=excluded.source_type,source_rule_id=excluded.source_rule_id`, draftID, v.NodeType, v.NodeID, v.DisplayGroup, v.SourceType, v.SourceRuleID, ts); err != nil {
				return err
			}
		}
	case "remove":
		for _, v := range nodes {
			if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_nodes where revision_id=? and node_type=? and node_id=?`, draftID, v.NodeType, v.NodeID); err != nil {
				return err
			}
		}
	case "replace":
		if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_nodes where revision_id=?`, draftID); err != nil {
			return err
		}
		for _, v := range nodes {
			if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,created_at) values(?,?,?,?,?,?,?)`, draftID, v.NodeType, v.NodeID, v.DisplayGroup, v.SourceType, v.SourceRuleID, ts); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("op must be add, remove, or replace")
	}
	if err := bumpPlanRevisionTx(ctx, tx, planID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

// EnsurePlanDraft creates the draft revision from the active revision when none
// exists and returns the draft revision id.
func (s *Store) EnsurePlanDraft(ctx context.Context, planID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	draftID, err := ensurePlanDraftTx(ctx, tx, planID, now())
	if err != nil {
		return 0, err
	}
	return draftID, tx.Commit()
}

func ensurePlanDraftTx(ctx context.Context, tx *sql.Tx, planID int64, ts string) (int64, error) {
	var draftID, activeID int64
	if err := tx.QueryRowContext(ctx, `select coalesce(draft_revision_id,0),coalesce(active_revision_id,0) from subscription_plans where id=?`, planID).Scan(&draftID, &activeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, sql.ErrNoRows
		}
		return 0, err
	}
	if draftID != 0 {
		return draftID, nil
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(revision),0)+1 from subscription_plan_revisions where plan_id=?`, planID).Scan(&revision); err != nil {
		return 0, err
	}
	var speed int
	var traffic int64
	var mode string
	var day int
	var createdBy sql.NullInt64
	if activeID != 0 {
		if err := tx.QueryRowContext(ctx, `select speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_by from subscription_plan_revisions where id=?`, activeID).Scan(&speed, &traffic, &mode, &day, &createdBy); err != nil {
			return 0, err
		}
	}
	var by *int64
	if createdBy.Valid {
		by = &createdBy.Int64
	}
	draftID, err := insertPlanRevisionTx(ctx, tx, planID, revision, model.PlanRevisionDraft, speed, traffic, mode, day, by, ts, "")
	if err != nil {
		return 0, err
	}
	if activeID != 0 {
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,created_at) select ?,node_type,node_id,display_group,source_type,source_rule_id,? from subscription_plan_revision_nodes where revision_id=?`, draftID, ts, activeID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set draft_revision_id=? where id=?`, draftID, planID); err != nil {
		return 0, err
	}
	return draftID, nil
}

// replacePlanDraftTx discards the current draft (if any) and creates a fresh
// empty draft revision. Used by restore.
func replacePlanDraftTx(ctx context.Context, tx *sql.Tx, planID int64, ts string) (int64, error) {
	var draftID int64
	if err := tx.QueryRowContext(ctx, `select coalesce(draft_revision_id,0) from subscription_plans where id=?`, planID).Scan(&draftID); err != nil {
		return 0, err
	}
	if draftID != 0 {
		if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revisions where id=?`, draftID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `update subscription_plans set draft_revision_id=NULL where id=?`, planID); err != nil {
			return 0, err
		}
	}
	return ensurePlanDraftTx(ctx, tx, planID, ts)
}

func checkPlanRevisionTx(ctx context.Context, tx *sql.Tx, planID, expectedRevision int64) error {
	if expectedRevision <= 0 {
		return nil
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `select revision from subscription_plans where id=?`, planID).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	if revision != expectedRevision {
		return ErrPlanRevisionConflict
	}
	return nil
}

func bumpPlanRevisionTx(ctx context.Context, tx *sql.Tx, planID int64, ts string) error {
	res, err := tx.ExecContext(ctx, `update subscription_plans set revision=revision+1,updated_at=? where id=?`, ts, planID)
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

// ListEffectiveUserPlanBindings returns enabled bindings whose time window
// contains at (starts_at <= at AND (expires_at IS NULL OR expires_at > at)).
// This is the binding set the plan authorization snapshot resolves from.
func (s *Store) ListEffectiveUserPlanBindings(ctx context.Context, at time.Time) ([]model.UserPlanBinding, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,plan_id,enabled,starts_at,expires_at,assigned_by,created_at,updated_at from user_plan_bindings where enabled=1 and (starts_at is null or starts_at <= ?) and (expires_at is null or expires_at > ?) order by user_id`, at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano))
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

// migratePlanRevisions upgrades databases created before the revision model:
// it adds the revision id columns and backfills one active revision per plan
// from the old plan-level limit columns and the legacy subscription_plan_nodes
// table. Fresh databases already have the new schema and skip the backfill.
func (s *Store) migratePlanRevisions(ctx context.Context) error {
	if err := s.ensureColumn(ctx, "subscription_plans", "active_revision_id", `alter table subscription_plans add column active_revision_id integer references subscription_plan_revisions(id) on delete set null`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "subscription_plans", "draft_revision_id", `alter table subscription_plans add column draft_revision_id integer references subscription_plan_revisions(id) on delete set null`); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `select id from subscription_plans where active_revision_id is null`)
	if err != nil {
		return err
	}
	type planID struct{ id int64 }
	var pending []planID
	for rows.Next() {
		var v planID
		if err := rows.Scan(&v.id); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, v)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	hasLegacyNodes := false
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='subscription_plan_nodes'`).Scan(&hasLegacyNodes); err != nil {
		return err
	}
	hasLegacyLimitColumns := false
	if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('subscription_plans') where name='speed_limit_mbps'`).Scan(&hasLegacyLimitColumns); err != nil {
		return err
	}
	for _, item := range pending {
		ts := now()
		speed, traffic, mode, day := 0, int64(0), "monthly", 1
		if hasLegacyLimitColumns {
			if err := s.db.QueryRowContext(ctx, `select coalesce(speed_limit_mbps,0),coalesce(traffic_limit_bytes,0),coalesce(traffic_reset_mode,'monthly'),coalesce(traffic_reset_day,1) from subscription_plans where id=?`, item.id).Scan(&speed, &traffic, &mode, &day); err != nil {
				return err
			}
		}
		var revision int64
		if hasLegacyLimitColumns {
			if err := s.db.QueryRowContext(ctx, `select coalesce(revision,0) from subscription_plans where id=?`, item.id).Scan(&revision); err != nil {
				return err
			}
		}
		if revision < 1 {
			revision = 1
		}
		res, err := s.db.ExecContext(ctx, `insert into subscription_plan_revisions(plan_id,revision,status,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_at,activated_at) values(?,?,?,?,?,?,?,?,?)`, item.id, revision, string(model.PlanRevisionActive), speed, traffic, mode, day, ts, ts)
		if err != nil {
			return err
		}
		revisionID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		if hasLegacyNodes {
			if _, err := s.db.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,created_at) select ?,node_type,node_id,display_group,source_type,source_rule_id,created_at from subscription_plan_nodes where plan_id=? and enabled=1`, revisionID, item.id); err != nil {
				return err
			}
		}
		if _, err := s.db.ExecContext(ctx, `update subscription_plans set active_revision_id=?,revision=?,updated_at=? where id=?`, revisionID, revision, ts, item.id); err != nil {
			return err
		}
	}
	return nil
}
