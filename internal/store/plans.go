package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// ErrPlanRevisionConflict is returned when a plan mutation carries an
// expected_revision that no longer matches the stored plan revision. Callers
// must re-preview before retrying.
var ErrPlanRevisionConflict = errors.New("plan revision conflict: the plan changed since preview; re-preview and retry")

func marshalOrderPolicy(p model.SubscriptionNodeOrderPolicy) string {
	raw, err := json.Marshal(p)
	if err != nil {
		return model.DefaultSubscriptionNodeOrderPolicyJSON()
	}
	return string(raw)
}

func parseOrderPolicy(raw string) model.SubscriptionNodeOrderPolicy {
	if strings.TrimSpace(raw) == "" {
		return model.DefaultSubscriptionNodeOrderPolicy()
	}
	var p model.SubscriptionNodeOrderPolicy
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return model.DefaultSubscriptionNodeOrderPolicy()
	}
	if p.Version == 0 || strings.TrimSpace(string(p.Mode)) == "" {
		return model.DefaultSubscriptionNodeOrderPolicy()
	}
	if p.EntryRegionOrderMode == "" {
		p.EntryRegionOrderMode = model.SubscriptionNodeEntryRegionOrderInheritExit
	}
	if strings.TrimSpace(string(p.ManualSeed)) == "" {
		p.ManualSeed = model.SubscriptionNodeOrderExitRegion
	}
	if p.NewNodePlacement == "" {
		p.NewNodePlacement = model.SubscriptionNodePlacementPending
	}
	if p.UnmatchedPlacement == "" {
		p.UnmatchedPlacement = model.SubscriptionNodePlacementPending
	}
	return p
}

func planRevisionNodeKey(nodeType model.AssignableNodeType, nodeID int64) string {
	return string(nodeType) + ":" + strconv.FormatInt(nodeID, 10)
}

const planSelectSQL = `select p.id,p.name,p.description,p.enabled,p.lock_version,coalesce(p.current_revision_id,0),coalesce(p.latest_revision_id,0),coalesce(p.pending_revision_id,0),p.revision,coalesce(p.active_revision_id,0),coalesce(p.draft_revision_id,0),p.created_at,p.updated_at,
	coalesce(cr.speed_limit_mbps,0),coalesce(cr.traffic_limit_bytes,0),coalesce(cr.traffic_reset_mode,'monthly'),coalesce(cr.traffic_reset_day,1) from subscription_plans p left join subscription_plan_revisions cr on cr.id=p.current_revision_id`

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
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &enabled, &v.LockVersion, &v.CurrentRevisionID, &v.LatestRevisionID, &v.PendingRevisionID, &v.Revision, &v.ActiveRevisionID, &v.DraftRevisionID, &ca, &ua, &v.SpeedLimitMbps, &v.TrafficLimitBytes, &v.TrafficResetMode, &v.TrafficResetDay); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		// Legacy aliases so older callers keep working during the phased
		// migration: the old revision counter maps to lock_version and the old
		// active pointer maps to the current revision.
		if v.Revision == 0 {
			v.Revision = v.LockVersion
		}
		if v.ActiveRevisionID == 0 {
			v.ActiveRevisionID = v.CurrentRevisionID
		}
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
	res, err := tx.ExecContext(ctx, `insert into subscription_plans(name,description,enabled,revision,lock_version,current_revision_id,latest_revision_id,pending_revision_id,created_at,updated_at) values(?,?,?,1,1,null,null,null,?,?)`, v.Name, v.Description, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	planID, err := res.LastInsertId()
	if err != nil {
		return err
	}
	revisionID, err := insertPlanRevisionTx(ctx, tx, planID, model.PlanRevisionActive, v.SpeedLimitMbps, v.TrafficLimitBytes, v.TrafficResetMode, v.TrafficResetDay, nil, marshalOrderPolicy(model.NewSubscriptionNodeOrderPolicy()), model.PlanChangeKindCreate, "", 0, ts, ts)
	if err != nil {
		return err
	}
	if err := insertPlanRevisionNodesTx(ctx, tx, revisionID, nodes, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set current_revision_id=?,latest_revision_id=?,active_revision_id=? where id=?`, revisionID, revisionID, revisionID, planID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	v.ID = planID
	v.LockVersion = 1
	v.CurrentRevisionID = revisionID
	v.LatestRevisionID = revisionID
	v.ActiveRevisionID = revisionID
	v.Revision = 1
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	return nil
}

func insertPlanRevisionTx(ctx context.Context, tx *sql.Tx, planID int64, status model.PlanRevisionStatus, speed int, traffic int64, mode string, day int, createdBy *int64, policyJSON, changeKind, changeSummary string, basedOn int64, createdAt, activatedAt string) (int64, error) {
	if strings.TrimSpace(mode) == "" {
		mode = "monthly"
	}
	if day <= 0 {
		day = 1
	}
	if policyJSON == "" {
		policyJSON = model.DefaultSubscriptionNodeOrderPolicyJSON()
	}
	var versionNo int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(version_no),0)+1 from subscription_plan_revisions where plan_id=?`, planID).Scan(&versionNo); err != nil {
		return 0, err
	}
	if basedOn == 0 {
		if err := tx.QueryRowContext(ctx, `select coalesce(max(id),0) from subscription_plan_revisions where plan_id=?`, planID).Scan(&basedOn); err != nil {
			return 0, err
		}
	}
	res, err := tx.ExecContext(ctx, `insert into subscription_plan_revisions(plan_id,revision,version_no,status,based_on_revision_id,change_kind,change_summary,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,node_order_policy_json,created_by,created_at,activated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, planID, versionNo, versionNo, string(status), basedOn, changeKind, changeSummary, speed, traffic, mode, day, policyJSON, createdBy, createdAt, nullEmpty(activatedAt))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func insertPlanRevisionNodesTx(ctx context.Context, tx *sql.Tx, revisionID int64, nodes []model.SubscriptionPlanNode, ts string) error {
	for _, v := range nodes {
		if v.SourceType == "" {
			v.SourceType = model.PlanNodeSourceExplicit
		}
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,created_at) values(?,?,?,?,?,?,?,?,?)`, revisionID, v.NodeType, v.NodeID, v.DisplayGroup, v.SourceType, v.SourceRuleID, nullInt(v.SortPosition), v.DisplayNameOverride, ts); err != nil {
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

// DeleteSubscriptionPlan unbinds every enabled user from the plan, then
// physically removes it. Users themselves are left in place; the foreign-key
// cascade drops revisions, revision nodes, and remaining binding rows.
func (s *Store) DeleteSubscriptionPlan(ctx context.Context, id int64) error {
	return s.deleteSubscriptionPlanTx(ctx, id, 0)
}

// DetachAndDeleteSubscriptionPlan is the access-change finalize helper: it
// unbinds users, detaches this change from the plan so the row survives the
// cascade, then deletes the plan.
func (s *Store) DetachAndDeleteSubscriptionPlan(ctx context.Context, planID, changeID int64) error {
	return s.deleteSubscriptionPlanTx(ctx, planID, changeID)
}

func (s *Store) deleteSubscriptionPlanTx(ctx context.Context, planID, changeID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `update user_plan_bindings set enabled=0,updated_at=? where plan_id=? and enabled=1`, ts, planID); err != nil {
		return err
	}
	if changeID > 0 {
		if _, err := tx.ExecContext(ctx, `update access_changes set source_plan_id=null,updated_at=? where id=?`, ts, changeID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from subscription_plans where id=?`, planID); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearUserPlanBindingsForPlan disables every enabled binding of the plan
// without deleting user records. Pending and active rows are both cleared so
// the snapshot stops granting the plan immediately.
func (s *Store) ClearUserPlanBindingsForPlan(ctx context.Context, planID int64) error {
	_, err := s.db.ExecContext(ctx, `update user_plan_bindings set enabled=0,updated_at=? where plan_id=? and enabled=1`, now(), planID)
	return err
}

// ListEnabledUserPlanBindingsForPlan returns active and pending enabled
// bindings for one plan, including not-yet-expired rows.
func (s *Store) ListEnabledUserPlanBindingsForPlan(ctx context.Context, planID int64) ([]model.UserPlanBinding, error) {
	rows, err := s.db.QueryContext(ctx, userPlanBindingSelect+` where plan_id=? and enabled=1 and status in ('active','pending') and (expires_at is null or expires_at > ?) order by user_id`, planID, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPlanBindings(rows)
}

// HasOpenAccessChangeForPlan reports whether a prepare/activate/finalize
// access change still references this plan.
func (s *Store) HasOpenAccessChangeForPlan(ctx context.Context, planID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from access_changes where source_plan_id=? and status in ('preparing','activating','finalizing')`, planID).Scan(&n)
	return n > 0, err
}

func (s *Store) HasPendingUserPlanBindingsForPlan(ctx context.Context, planID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from user_plan_bindings where plan_id=? and enabled=1 and status='pending'`, planID).Scan(&n)
	return n > 0, err
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
	var currentRevisionID int64
	if err := tx.QueryRowContext(ctx, `select coalesce(current_revision_id,0) from subscription_plans where id=?`, id).Scan(&currentRevisionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	if currentRevisionID == 0 {
		return nil, fmt.Errorf("subscription plan %d has no current revision", id)
	}
	var speed int
	var traffic int64
	var mode string
	var day int
	var policyJSON string
	var createdBy, orderTemplateID, orderSourcePlanID, orderSourceRevisionID sql.NullInt64
	var orderTemplateRevision int64
	var orderSourceMode string
	if err := tx.QueryRowContext(ctx, `select speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,node_order_policy_json,created_by,order_template_id,order_template_revision,order_source_plan_id,order_source_revision_id,order_source_mode from subscription_plan_revisions where id=?`, currentRevisionID).Scan(&speed, &traffic, &mode, &day, &policyJSON, &createdBy, &orderTemplateID, &orderTemplateRevision, &orderSourcePlanID, &orderSourceRevisionID, &orderSourceMode); err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx, `insert into subscription_plans(name,description,enabled,revision,lock_version,current_revision_id,latest_revision_id,pending_revision_id,created_at,updated_at) values(?,?,1,1,1,null,null,null,?,?)`, newName, "", ts, ts)
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
	revisionID, err := insertPlanRevisionTx(ctx, tx, planID, model.PlanRevisionActive, speed, traffic, mode, day, by, policyJSON, model.PlanChangeKindClone, "基于方案副本", 0, ts, ts)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set order_template_id=?,order_template_revision=?,order_source_plan_id=?,order_source_revision_id=?,order_source_mode=? where id=?`, nullableInt64(orderTemplateID), orderTemplateRevision, nullableInt64(orderSourcePlanID), nullableInt64(orderSourceRevisionID), orderSourceMode, revisionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,created_at) select ?,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,? from subscription_plan_revision_nodes where revision_id=?`, revisionID, ts, currentRevisionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_rules(revision_id,rule_id,kind,scope_key,created_at) select ?,rule_id,kind,scope_key,? from subscription_plan_revision_rules where revision_id=?`, revisionID, ts, currentRevisionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_node_exclusions(revision_id,node_type,node_id,created_at) select ?,node_type,node_id,? from subscription_plan_revision_node_exclusions where revision_id=?`, revisionID, ts, currentRevisionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set current_revision_id=?,latest_revision_id=?,active_revision_id=? where id=?`, revisionID, revisionID, revisionID, planID); err != nil {
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

// PublishPlanRevisionGuarded publishes the draft only when both the active
// revision and the draft revision are still the exact ones the change was
// previewed against. Access changes call this at activation time so a
// concurrent publish or draft edit fails the change instead of activating a
// different node set than the one that was prepared.
func (s *Store) PublishPlanRevisionGuarded(ctx context.Context, id, expectedActiveRevisionID, expectedDraftID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var draftID, activeID int64
	if err := tx.QueryRowContext(ctx, `select draft_revision_id,coalesce(active_revision_id,0) from subscription_plans where id=?`, id).Scan(&draftID, &activeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, sql.ErrNoRows
		}
		return 0, err
	}
	if activeID != expectedActiveRevisionID {
		return 0, ErrPlanRevisionConflict
	}
	if draftID != expectedDraftID {
		return 0, ErrPlanRevisionConflict
	}
	if draftID == 0 {
		return 0, errors.New("subscription plan has no draft revision to publish")
	}
	ts := now()
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

// SetSubscriptionPlanEnabled flips the plan's enabled flag. Disabling a plan
// removes every bound user's nodes from the effective snapshot immediately;
// credential removal is carried by an access change finalize.
func (s *Store) SetSubscriptionPlanEnabled(ctx context.Context, id int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `update subscription_plans set enabled=?,updated_at=? where id=?`, boolInt(enabled), now(), id)
	return err
}

func (s *Store) ListPlanRevisions(ctx context.Context, planID int64) ([]model.SubscriptionPlanRevision, error) {
	rows, err := s.db.QueryContext(ctx, `select id,plan_id,revision,version_no,status,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,node_order_policy_json,order_template_id,order_template_revision,order_source_plan_id,order_source_revision_id,order_source_mode,created_by,created_at,activated_at,based_on_revision_id,change_kind,change_summary,activation_change_id from subscription_plan_revisions where plan_id=? order by version_no desc`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisions(rows)
}

func (s *Store) GetPlanRevision(ctx context.Context, planID, revisionID int64) (*model.SubscriptionPlanRevision, error) {
	rows, err := s.db.QueryContext(ctx, `select id,plan_id,revision,version_no,status,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,node_order_policy_json,order_template_id,order_template_revision,order_source_plan_id,order_source_revision_id,order_source_mode,created_by,created_at,activated_at,based_on_revision_id,change_kind,change_summary,activation_change_id from subscription_plan_revisions where id=? and plan_id=?`, revisionID, planID)
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

// ListPlanRevisionCreatedTimes returns each revision's immutable creation time
// so list views can render timestamp version labels without an N+1 lookup.
func (s *Store) ListPlanRevisionCreatedTimes(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `select id,created_at from subscription_plan_revisions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var createdAt string
		if err := rows.Scan(&id, &createdAt); err != nil {
			return nil, err
		}
		out[id] = parseTime(createdAt)
	}
	return out, rows.Err()
}

func scanPlanRevisions(rows *sql.Rows) ([]model.SubscriptionPlanRevision, error) {
	var out []model.SubscriptionPlanRevision
	for rows.Next() {
		var v model.SubscriptionPlanRevision
		var createdBy, orderTemplateID, orderSourcePlanID, orderSourceRevisionID sql.NullInt64
		var ca string
		var activatedAt sql.NullString
		var policyJSON string
		var basedOn sql.NullInt64
		var activationChangeID sql.NullInt64
		if err := rows.Scan(&v.ID, &v.PlanID, &v.Revision, &v.VersionNo, &v.Status, &v.SpeedLimitMbps, &v.TrafficLimitBytes, &v.TrafficResetMode, &v.TrafficResetDay, &policyJSON, &orderTemplateID, &v.OrderTemplateRevision, &orderSourcePlanID, &orderSourceRevisionID, &v.OrderSourceMode, &createdBy, &ca, &activatedAt, &basedOn, &v.ChangeKind, &v.ChangeSummary, &activationChangeID); err != nil {
			return nil, err
		}
		v.NodeOrderPolicy = parseOrderPolicy(policyJSON)
		if orderTemplateID.Valid {
			value := orderTemplateID.Int64
			v.OrderTemplateID = &value
		}
		if orderSourcePlanID.Valid {
			value := orderSourcePlanID.Int64
			v.OrderSourcePlanID = &value
		}
		if orderSourceRevisionID.Valid {
			value := orderSourceRevisionID.Int64
			v.OrderSourceRevisionID = &value
		}
		if v.VersionNo == 0 {
			v.VersionNo = v.Revision
		}
		if basedOn.Valid {
			v.BasedOnRevisionID = basedOn.Int64
		}
		if activationChangeID.Valid {
			id := activationChangeID.Int64
			v.ActivationChangeID = &id
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
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.sort_position,n.display_name_override,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id where n.revision_id=? order by n.node_type,n.node_id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// ListActivePlanNodes returns the node set of the plan's current revision.
func (s *Store) ListActivePlanNodes(ctx context.Context, planID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.sort_position,n.display_name_override,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id join subscription_plans p on p.current_revision_id=r.id where p.id=? order by n.node_type,n.node_id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// ListDraftPlanNodes returns the node set of the plan's draft revision, or an
// empty slice when no draft exists.
func (s *Store) ListDraftPlanNodes(ctx context.Context, planID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.sort_position,n.display_name_override,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id join subscription_plans p on p.draft_revision_id=r.id where p.id=? order by n.node_type,n.node_id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// ListAllPlanNodes returns the current-revision node set of every plan. This is
// the node set the effective access snapshot resolves from.
func (s *Store) ListAllPlanNodes(ctx context.Context) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.sort_position,n.display_name_override,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id join subscription_plans p on p.current_revision_id=r.id where p.enabled=1 order by p.id,n.node_type,n.node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// ListPlansForNode returns current-revision plan-node rows referencing a node.
func (s *Store) ListPlansForNode(ctx context.Context, nodeType string, nodeID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.sort_position,n.display_name_override,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id join subscription_plans p on p.current_revision_id=r.id where n.node_type=? and n.node_id=? order by p.id`, nodeType, nodeID)
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
		var sortPosition sql.NullInt64
		var displayNameOverride sql.NullString
		if err := rows.Scan(&v.PlanID, &v.ID, &v.RevisionID, &v.NodeType, &v.NodeID, &v.DisplayGroup, &v.SourceType, &v.SourceRuleID, &sortPosition, &displayNameOverride, &ca); err != nil {
			return nil, err
		}
		v.Enabled = true
		if sortPosition.Valid {
			position := int(sortPosition.Int64)
			v.SortPosition = &position
		}
		if displayNameOverride.Valid {
			value := displayNameOverride.String
			v.DisplayNameOverride = &value
		}
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
	var policyJSON string
	var orderTemplateID, orderSourcePlanID, orderSourceRevisionID sql.NullInt64
	var orderTemplateRevision int64
	var orderSourceMode string
	if err := tx.QueryRowContext(ctx, `select speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,node_order_policy_json,order_template_id,order_template_revision,order_source_plan_id,order_source_revision_id,order_source_mode from subscription_plan_revisions where id=? and plan_id=?`, revisionID, planID).Scan(&speed, &traffic, &mode, &day, &policyJSON, &orderTemplateID, &orderTemplateRevision, &orderSourcePlanID, &orderSourceRevisionID, &orderSourceMode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, sql.ErrNoRows
		}
		return 0, err
	}
	draftID, err := replacePlanDraftTx(ctx, tx, planID, ts)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set speed_limit_mbps=?,traffic_limit_bytes=?,traffic_reset_mode=?,traffic_reset_day=?,node_order_policy_json=?,order_template_id=?,order_template_revision=?,order_source_plan_id=?,order_source_revision_id=?,order_source_mode=? where id=?`, speed, traffic, mode, day, policyJSON, nullableInt64(orderTemplateID), orderTemplateRevision, nullableInt64(orderSourcePlanID), nullableInt64(orderSourceRevisionID), orderSourceMode, draftID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_nodes where revision_id=?`, draftID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,created_at) select ?,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,? from subscription_plan_revision_nodes where revision_id=?`, draftID, ts, revisionID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_rules where revision_id=?`, draftID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_node_exclusions where revision_id=?`, draftID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_rules(revision_id,rule_id,kind,scope_key,created_at) select ?,rule_id,kind,scope_key,? from subscription_plan_revision_rules where revision_id=?`, draftID, ts, revisionID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_node_exclusions(revision_id,node_type,node_id,created_at) select ?,node_type,node_id,? from subscription_plan_revision_node_exclusions where revision_id=?`, draftID, ts, revisionID); err != nil {
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
			if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,display_name_override,created_at) values(?,?,?,?,?,?,?,?) on conflict(revision_id,node_type,node_id) do update set display_group=excluded.display_group,source_type=excluded.source_type,source_rule_id=excluded.source_rule_id`, draftID, v.NodeType, v.NodeID, v.DisplayGroup, v.SourceType, v.SourceRuleID, v.DisplayNameOverride, ts); err != nil {
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
		existing, err := listDraftRevisionNodesTx(ctx, tx, draftID)
		if err != nil {
			return err
		}
		keep := map[string]model.SubscriptionPlanNode{}
		for _, row := range existing {
			keep[planRevisionNodeKey(row.NodeType, row.NodeID)] = row
		}
		seen := map[string]bool{}
		for _, v := range nodes {
			key := planRevisionNodeKey(v.NodeType, v.NodeID)
			seen[key] = true
			if _, ok := keep[key]; ok {
				// Keep the row and its manual position; refresh mutable fields.
				if _, err := tx.ExecContext(ctx, `update subscription_plan_revision_nodes set display_group=?,source_type=?,source_rule_id=? where revision_id=? and node_type=? and node_id=?`, v.DisplayGroup, v.SourceType, v.SourceRuleID, draftID, v.NodeType, v.NodeID); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,display_name_override,created_at) values(?,?,?,?,?,?,?,?)`, draftID, v.NodeType, v.NodeID, v.DisplayGroup, v.SourceType, v.SourceRuleID, v.DisplayNameOverride, ts); err != nil {
				return err
			}
		}
		for key, row := range keep {
			if seen[key] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_nodes where revision_id=? and node_type=? and node_id=?`, draftID, row.NodeType, row.NodeID); err != nil {
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
	var policyJSON string
	var createdBy, orderTemplateID, orderSourcePlanID, orderSourceRevisionID sql.NullInt64
	var orderTemplateRevision int64
	var orderSourceMode string
	if activeID != 0 {
		if err := tx.QueryRowContext(ctx, `select speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,node_order_policy_json,created_by,order_template_id,order_template_revision,order_source_plan_id,order_source_revision_id,order_source_mode from subscription_plan_revisions where id=?`, activeID).Scan(&speed, &traffic, &mode, &day, &policyJSON, &createdBy, &orderTemplateID, &orderTemplateRevision, &orderSourcePlanID, &orderSourceRevisionID, &orderSourceMode); err != nil {
			return 0, err
		}
	}
	var by *int64
	if createdBy.Valid {
		by = &createdBy.Int64
	}
	draftID, err := insertPlanRevisionTx(ctx, tx, planID, model.PlanRevisionDraft, speed, traffic, mode, day, by, policyJSON, "", "", 0, ts, "")
	if err != nil {
		return 0, err
	}
	if activeID != 0 {
		if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set order_template_id=?,order_template_revision=?,order_source_plan_id=?,order_source_revision_id=?,order_source_mode=? where id=?`, nullableInt64(orderTemplateID), orderTemplateRevision, nullableInt64(orderSourcePlanID), nullableInt64(orderSourceRevisionID), orderSourceMode, draftID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,created_at) select ?,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,? from subscription_plan_revision_nodes where revision_id=?`, draftID, ts, activeID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_rules(revision_id,rule_id,kind,scope_key,created_at) select ?,rule_id,kind,scope_key,? from subscription_plan_revision_rules where revision_id=?`, draftID, ts, activeID); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_node_exclusions(revision_id,node_type,node_id,created_at) select ?,node_type,node_id,? from subscription_plan_revision_node_exclusions where revision_id=?`, draftID, ts, activeID); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set draft_revision_id=? where id=?`, draftID, planID); err != nil {
		return 0, err
	}
	return draftID, nil
}

func listDraftRevisionNodesTx(ctx context.Context, tx *sql.Tx, revisionID int64) ([]model.SubscriptionPlanNode, error) {
	rows, err := tx.QueryContext(ctx, `select r.plan_id,n.id,n.revision_id,n.node_type,n.node_id,n.display_group,n.source_type,n.source_rule_id,n.sort_position,n.display_name_override,n.created_at from subscription_plan_revision_nodes n join subscription_plan_revisions r on r.id=n.revision_id where n.revision_id=? order by n.node_type,n.node_id`, revisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPlanRevisionNodes(rows)
}

// GetPlanRevisionOrdering returns the ordering policy and full node set
// (including manual positions) of one revision for the ordering editor.
func (s *Store) GetPlanRevisionOrdering(ctx context.Context, planID, revisionID int64) (*model.SubscriptionPlanRevision, []model.SubscriptionPlanNode, error) {
	revision, err := s.GetPlanRevision(ctx, planID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	nodes, err := s.ListPlanRevisionNodes(ctx, revisionID)
	if err != nil {
		return nil, nil, err
	}
	return revision, nodes, nil
}

// ---------------------------------------------------------------------------
// Immutable plan versions
// ---------------------------------------------------------------------------

// ErrPlanVersionApplying is returned when a plan already has a live version
// being applied to agents. A failed, unactivated version is superseded by the
// next save so stale deployment failures do not block newer desired state.
var ErrPlanVersionApplying = errors.New("plan version is still applying; retry after it settles")

// PlanSettingsMutation carries limit changes for a new version. Nil fields
// keep the base value.
type PlanSettingsMutation struct {
	SpeedLimitMbps    *int
	TrafficLimitBytes *int64
	TrafficResetMode  *string
	TrafficResetDay   *int
}

// PlanMetaMutation carries plan identity changes (name, description, enabled)
// that are saved atomically with the version. Nil fields keep the current
// value. Meta-only saves update the plan row without creating a version.
type PlanMetaMutation struct {
	Name        *string
	Description *string
	Enabled     *bool
}

// PlanNodesMutation carries a node-set change for a new version.
type PlanNodesMutation struct {
	Op              string // add | remove | replace
	Nodes           []model.SubscriptionPlanNode
	ReplaceSnapshot bool // restore the supplied presentation fields verbatim
}

// PlanOrderingMutation carries ordering changes for a new version.
type PlanOrderingMutation struct {
	Policy                model.SubscriptionNodeOrderPolicy
	ManualOrder           []string
	ClearManualPositions  bool
	SetTemplateProvenance bool
	OrderTemplateID       *int64
	OrderTemplateRevision int64
	SetSourceProvenance   bool
	OrderSourcePlanID     *int64
	OrderSourceRevisionID *int64
	OrderSourceMode       string
}

// PlanMembershipPolicyMutation replaces the rule and exclusion snapshot in a
// new immutable version. Nodes must be the server-resolved desired membership.
type PlanMembershipPolicyMutation struct {
	Rules      []model.PlanMembershipRule
	Exclusions []model.PlanNodeExclusion
	Nodes      []model.SubscriptionPlanNode
}

type PlanNodePresentationMutation struct {
	DisplayNameOverrides map[string]*string
}

// PlanVersionMutation is the full description of one new immutable plan
// version. The version is always derived from the latest saved snapshot;
// BaseRevisionID and ExpectedLockVersion are the optimistic concurrency
// guards.
type PlanVersionMutation struct {
	BaseRevisionID      int64
	ExpectedLockVersion int64
	Meta                *PlanMetaMutation
	Settings            *PlanSettingsMutation
	Nodes               *PlanNodesMutation
	Ordering            *PlanOrderingMutation
	MembershipPolicy    *PlanMembershipPolicyMutation
	NodePresentation    *PlanNodePresentationMutation
	ChangeKind          string
	ChangeSummary       string
	CreatedBy           *int64
}

// CreatePlanVersionResult describes the outcome of a version save.
type CreatePlanVersionResult struct {
	Revision           model.SubscriptionPlanRevision
	Nodes              []model.SubscriptionPlanNode
	ChangeClass        string // presentation_only | authorization
	NoChange           bool
	EffectiveNow       bool
	RequiresDeployment bool
	LockVersion        int64
	CurrentRevisionID  int64
	LatestRevisionID   int64
	PendingRevisionID  int64
}

// CreatePlanVersion saves one immutable version derived from the plan's latest
// snapshot. Presentation-only versions (ordering, display groups) become the
// current version immediately and never touch agents; authorization versions
// (node set or limits) set the pending pointer and must be activated through
// ActivatePlanVersionGuarded by the access-change pipeline. An identical
// candidate returns NoChange without creating a version.
func (s *Store) CreatePlanVersion(ctx context.Context, planID int64, mutation PlanVersionMutation) (*CreatePlanVersionResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now()
	var lockVersion, currentID, latestID, pendingID int64
	var planName, planDescription string
	var planEnabled int
	if err := tx.QueryRowContext(ctx, `select lock_version,coalesce(current_revision_id,0),coalesce(latest_revision_id,0),coalesce(pending_revision_id,0),name,description,enabled from subscription_plans where id=?`, planID).Scan(&lockVersion, &currentID, &latestID, &pendingID, &planName, &planDescription, &planEnabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, err
	}
	if latestID == 0 {
		return nil, fmt.Errorf("subscription plan %d has no saved version", planID)
	}
	if mutation.ExpectedLockVersion > 0 && mutation.ExpectedLockVersion != lockVersion {
		return nil, ErrPlanRevisionConflict
	}
	baseID := latestID
	if mutation.BaseRevisionID > 0 {
		if mutation.BaseRevisionID != latestID {
			return nil, ErrPlanRevisionConflict
		}
		baseID = mutation.BaseRevisionID
	}
	supersededFailedChangeID := int64(0)
	if pendingID != 0 {
		err := tx.QueryRowContext(ctx, `select id from access_changes where source_plan_id=? and candidate_revision_id=? and status='failed' and activated_at is null and change_type in ('plan_publish','plan_restore') order by id desc limit 1`, planID, pendingID).Scan(&supersededFailedChangeID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPlanVersionApplying
		}
		if err != nil {
			return nil, err
		}
		res, err := tx.ExecContext(ctx, `update access_changes set status='cancelled',updated_at=? where id=? and status='failed' and activated_at is null`, ts, supersededFailedChangeID)
		if err != nil {
			return nil, err
		}
		if affected, err := res.RowsAffected(); err != nil {
			return nil, err
		} else if affected != 1 {
			return nil, ErrPlanVersionApplying
		}
		pendingID = 0
	}
	metaChanged := false
	if mutation.Meta != nil {
		if mutation.Meta.Name != nil && *mutation.Meta.Name != planName {
			planName = *mutation.Meta.Name
			metaChanged = true
		}
		if mutation.Meta.Description != nil && *mutation.Meta.Description != planDescription {
			planDescription = *mutation.Meta.Description
			metaChanged = true
		}
		if mutation.Meta.Enabled != nil && boolInt(*mutation.Meta.Enabled) != planEnabled {
			planEnabled = boolInt(*mutation.Meta.Enabled)
			metaChanged = true
		}
	}
	base, baseNodes, err := loadPlanRevisionSnapshotTx(ctx, tx, planID, baseID)
	if err != nil {
		return nil, err
	}
	candidate := base
	candidateNodes := clonePlanNodeSlice(baseNodes)
	baseRules, err := listPlanRevisionRules(ctx, tx, baseID)
	if err != nil {
		return nil, err
	}
	baseExclusions, err := listPlanRevisionExclusions(ctx, tx, baseID)
	if err != nil {
		return nil, err
	}
	candidateRules := append([]model.PlanMembershipRule(nil), baseRules...)
	candidateExclusions := append([]model.PlanNodeExclusion(nil), baseExclusions...)
	if mutation.Settings != nil {
		applySettingsMutation(&candidate, mutation.Settings)
	}
	if mutation.Nodes != nil {
		candidateNodes, err = applyNodesMutation(candidateNodes, mutation.Nodes)
		if err != nil {
			return nil, err
		}
	}
	if mutation.MembershipPolicy != nil {
		candidateRules = append([]model.PlanMembershipRule(nil), mutation.MembershipPolicy.Rules...)
		candidateExclusions = append([]model.PlanNodeExclusion(nil), mutation.MembershipPolicy.Exclusions...)
		candidateNodes = clonePlanNodeSlice(mutation.MembershipPolicy.Nodes)
	}
	if mutation.Ordering != nil {
		candidate.NodeOrderPolicy = parseOrderPolicy(marshalOrderPolicy(mutation.Ordering.Policy))
		if mutation.Ordering.SetTemplateProvenance {
			candidate.OrderTemplateID = mutation.Ordering.OrderTemplateID
			candidate.OrderTemplateRevision = mutation.Ordering.OrderTemplateRevision
		}
		if mutation.Ordering.SetSourceProvenance {
			candidate.OrderSourcePlanID = mutation.Ordering.OrderSourcePlanID
			candidate.OrderSourceRevisionID = mutation.Ordering.OrderSourceRevisionID
			candidate.OrderSourceMode = mutation.Ordering.OrderSourceMode
		}
		if mutation.Ordering.ClearManualPositions {
			for i := range candidateNodes {
				candidateNodes[i].SortPosition = nil
			}
		}
		if candidate.NodeOrderPolicy.Mode == model.SubscriptionNodeOrderManual {
			positionByKey, err := manualPositionsByKey(candidateNodes, normalizeManualOrderKeys(mutation.Ordering.ManualOrder))
			if err != nil {
				return nil, err
			}
			for i := range candidateNodes {
				key := planRevisionNodeKey(candidateNodes[i].NodeType, candidateNodes[i].NodeID)
				if position, ok := positionByKey[key]; ok {
					position := position
					candidateNodes[i].SortPosition = &position
				} else {
					candidateNodes[i].SortPosition = nil
				}
			}
		}
	}
	if mutation.NodePresentation != nil {
		for i := range candidateNodes {
			key := planRevisionNodeKey(candidateNodes[i].NodeType, candidateNodes[i].NodeID)
			if value, ok := mutation.NodePresentation.DisplayNameOverrides[key]; ok {
				candidateNodes[i].DisplayNameOverride = cloneStringPointer(value)
			}
		}
	}
	if candidate.SpeedLimitMbps < 0 || candidate.TrafficLimitBytes < 0 {
		return nil, fmt.Errorf("plan limits must be >= 0")
	}
	contentChanged := planVersionDigest(candidate, candidateNodes, candidateRules, candidateExclusions) != planVersionDigest(base, baseNodes, baseRules, baseExclusions)
	if !contentChanged {
		if supersededFailedChangeID != 0 {
			// Saving the same desired snapshot is an explicit retry after the
			// operator fixed the underlying server or topology. Keep the
			// immutable revision, replace the failed access change, and let the
			// caller create fresh tasks for it.
			newLock := lockVersion + 1
			if _, err := tx.ExecContext(ctx, `update subscription_plans set name=?,description=?,enabled=?,pending_revision_id=?,lock_version=?,updated_at=? where id=?`, planName, planDescription, planEnabled, latestID, newLock, ts, planID); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			revision, err := s.GetPlanRevision(ctx, planID, latestID)
			if err != nil {
				return nil, err
			}
			nodes, err := s.ListPlanRevisionNodes(ctx, latestID)
			if err != nil {
				return nil, err
			}
			return &CreatePlanVersionResult{
				Revision:           *revision,
				Nodes:              nodes,
				ChangeClass:        "authorization",
				RequiresDeployment: true,
				LockVersion:        newLock,
				CurrentRevisionID:  currentID,
				LatestRevisionID:   latestID,
				PendingRevisionID:  latestID,
			}, nil
		}
		if metaChanged {
			// Identity-only save: no version is created but the plan row is
			// updated and the lock advances so stale previews conflict.
			newLock := lockVersion + 1
			if _, err := tx.ExecContext(ctx, `update subscription_plans set name=?,description=?,enabled=?,lock_version=?,updated_at=? where id=?`, planName, planDescription, planEnabled, newLock, ts, planID); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &CreatePlanVersionResult{
				NoChange:          true,
				LockVersion:       newLock,
				CurrentRevisionID: currentID,
				LatestRevisionID:  latestID,
				PendingRevisionID: pendingID,
			}, nil
		}
		return &CreatePlanVersionResult{
			NoChange:          true,
			LockVersion:       lockVersion,
			CurrentRevisionID: currentID,
			LatestRevisionID:  latestID,
			PendingRevisionID: pendingID,
		}, nil
	}
	changeClass := "presentation_only"
	if currentID != 0 {
		current, currentNodes, err := loadPlanRevisionSnapshotTx(ctx, tx, planID, currentID)
		if err != nil {
			return nil, err
		}
		if !samePlanLimits(current, candidate) || !sameNodeMembership(currentNodes, candidateNodes) {
			changeClass = "authorization"
		}
	} else {
		changeClass = "authorization"
	}
	changeKind := mutation.ChangeKind
	if changeKind == "" {
		changeKind = model.PlanChangeKindMixed
	}
	activatedAt := ""
	if changeClass == "presentation_only" {
		activatedAt = ts
	}
	revisionID, err := insertPlanRevisionTx(ctx, tx, planID, model.PlanRevisionArchived, candidate.SpeedLimitMbps, candidate.TrafficLimitBytes, candidate.TrafficResetMode, candidate.TrafficResetDay, mutation.CreatedBy, marshalOrderPolicy(candidate.NodeOrderPolicy), changeKind, mutation.ChangeSummary, baseID, ts, activatedAt)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set order_template_id=?,order_template_revision=?,order_source_plan_id=?,order_source_revision_id=?,order_source_mode=? where id=?`, candidate.OrderTemplateID, candidate.OrderTemplateRevision, candidate.OrderSourcePlanID, candidate.OrderSourceRevisionID, candidate.OrderSourceMode, revisionID); err != nil {
		return nil, err
	}
	if err := insertPlanRevisionMembershipPolicyTx(ctx, tx, revisionID, candidateRules, candidateExclusions, ts); err != nil {
		return nil, err
	}
	for _, pn := range candidateNodes {
		if _, err := tx.ExecContext(ctx, `insert into subscription_plan_revision_nodes(revision_id,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,created_at) values(?,?,?,?,?,?,?,?,?)`, revisionID, pn.NodeType, pn.NodeID, pn.DisplayGroup, pn.SourceType, pn.SourceRuleID, nullInt(pn.SortPosition), pn.DisplayNameOverride, ts); err != nil {
			return nil, err
		}
	}
	newLock := lockVersion + 1
	planMetaSQL := `name=?,description=?,enabled=?`
	planMetaArgs := []any{planName, planDescription, planEnabled}
	if changeClass == "presentation_only" {
		if _, err := tx.ExecContext(ctx, `update subscription_plans set `+planMetaSQL+`,current_revision_id=?,latest_revision_id=?,lock_version=?,updated_at=? where id=?`, append(planMetaArgs, revisionID, revisionID, newLock, ts, planID)...); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `update subscription_plans set `+planMetaSQL+`,latest_revision_id=?,pending_revision_id=?,lock_version=?,updated_at=? where id=?`, append(planMetaArgs, revisionID, revisionID, newLock, ts, planID)...); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	revision, err := s.GetPlanRevision(ctx, planID, revisionID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.ListPlanRevisionNodes(ctx, revisionID)
	if err != nil {
		return nil, err
	}
	result := &CreatePlanVersionResult{
		Revision:           *revision,
		Nodes:              nodes,
		ChangeClass:        changeClass,
		EffectiveNow:       changeClass == "presentation_only",
		RequiresDeployment: changeClass == "authorization",
		LockVersion:        newLock,
		CurrentRevisionID:  currentID,
		LatestRevisionID:   revisionID,
		PendingRevisionID:  pendingID,
	}
	if changeClass == "presentation_only" {
		result.CurrentRevisionID = revisionID
	} else {
		result.PendingRevisionID = revisionID
	}
	return result, nil
}

// TrafficPeriodMigration carries the traffic-period rewrite applied to one
// user when a plan version that changes the reset cycle activates.
type TrafficPeriodMigration struct {
	UserID          int64
	SourcePeriodKey string
	TargetPeriodKey string
	TargetStart     time.Time
	TargetEnd       time.Time
	TrafficLimit    int64
}

func (s *Store) ActivatePlanVersionGuarded(ctx context.Context, planID, expectedCurrentRevisionID, candidateRevisionID, accessChangeID int64, migrations []TrafficPeriodMigration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentID, pendingID int64
	if err := tx.QueryRowContext(ctx, `select coalesce(current_revision_id,0),coalesce(pending_revision_id,0) from subscription_plans where id=?`, planID).Scan(&currentID, &pendingID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return sql.ErrNoRows
		}
		return err
	}
	if currentID != expectedCurrentRevisionID || pendingID != candidateRevisionID {
		return ErrPlanRevisionConflict
	}
	ts := now()
	for _, migration := range migrations {
		if err := migrateTrafficPeriodTx(ctx, tx, migration, ts); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plans set current_revision_id=?,pending_revision_id=null,lock_version=lock_version+1,updated_at=? where id=?`, candidateRevisionID, ts, planID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set activated_at=?,activation_change_id=? where id=?`, ts, accessChangeID, candidateRevisionID); err != nil {
		return err
	}
	return tx.Commit()
}

func migrateTrafficPeriodTx(ctx context.Context, tx *sql.Tx, migration TrafficPeriodMigration, ts string) error {
	if migration.UserID <= 0 || migration.SourcePeriodKey == "" || migration.TargetPeriodKey == "" || migration.SourcePeriodKey == migration.TargetPeriodKey {
		return nil
	}
	targetKey, err := resolveTrafficPeriodKeyTx(ctx, tx, migration.UserID, migration.TargetPeriodKey)
	if err != nil {
		return err
	}
	if targetKey == migration.SourcePeriodKey {
		token := strings.NewReplacer("-", "", ":", "", ".", "", "Z", "", "+", "").Replace(ts)
		targetKey = migration.TargetPeriodKey + "#migration-" + token
	}
	aliases, err := trafficPeriodAliasesTx(ctx, tx, migration.UserID, migration.SourcePeriodKey)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `insert or ignore into traffic_period_transitions(user_id,source_period_key,target_period_key,created_at) values(?,?,?,?)`, migration.UserID, migration.SourcePeriodKey, targetKey, ts)
	if err != nil {
		return err
	}
	inserted, err := res.RowsAffected()
	if err != nil || inserted == 0 {
		return err
	}
	for _, alias := range aliases {
		if _, err := tx.ExecContext(ctx, `update traffic_period_transitions set target_period_key=? where user_id=? and source_period_key=?`, targetKey, migration.UserID, alias); err != nil {
			return err
		}
	}
	var upload, download int64
	err = tx.QueryRowContext(ctx, `select upload_bytes,download_bytes from traffic_periods where user_id=? and period_key=?`, migration.UserID, migration.SourcePeriodKey).Scan(&upload, &download)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var existingUpload, existingDownload int64
	err = tx.QueryRowContext(ctx, `select upload_bytes,download_bytes from traffic_periods where user_id=? and period_key=?`, migration.UserID, targetKey).Scan(&existingUpload, &existingDownload)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	totalUpload, totalDownload := existingUpload+upload, existingDownload+download
	state := periodState(totalUpload+totalDownload, migration.TrafficLimit)
	_, err = tx.ExecContext(ctx, `insert into traffic_periods(user_id,period_key,started_at,ends_at,upload_bytes,download_bytes,traffic_limit_bytes,state,updated_at) values(?,?,?,?,?,?,?,?,?)
		on conflict(user_id,period_key) do update set upload_bytes=upload_bytes+excluded.upload_bytes,download_bytes=download_bytes+excluded.download_bytes,traffic_limit_bytes=excluded.traffic_limit_bytes,state=case when excluded.traffic_limit_bytes>0 and upload_bytes+download_bytes+excluded.upload_bytes+excluded.download_bytes>=excluded.traffic_limit_bytes then 'quota_exceeded' else 'active' end,updated_at=excluded.updated_at`, migration.UserID, targetKey, migration.TargetStart.UTC().Format(time.RFC3339Nano), migration.TargetEnd.UTC().Format(time.RFC3339Nano), upload, download, migration.TrafficLimit, state, ts)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update traffic_periods set upload_bytes=0,download_bytes=0,state='active',updated_at=? where user_id=? and period_key=?`, ts, migration.UserID, migration.SourcePeriodKey); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `update users set traffic_used_bytes=?,updated_at=? where id=?`, totalUpload+totalDownload, ts, migration.UserID)
	return err
}

func resolveTrafficPeriodKeyTx(ctx context.Context, tx *sql.Tx, userID int64, periodKey string) (string, error) {
	current := periodKey
	for range 16 {
		var next string
		err := tx.QueryRowContext(ctx, `select target_period_key from traffic_period_transitions where user_id=? and source_period_key=?`, userID, current).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return current, nil
		}
		if err != nil {
			return "", err
		}
		if next == "" || next == current {
			return current, nil
		}
		current = next
	}
	return "", errors.New("traffic period transition chain is too deep")
}

func trafficPeriodAliasesTx(ctx context.Context, tx *sql.Tx, userID int64, targetPeriodKey string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `select source_period_key from traffic_period_transitions where user_id=?`, userID)
	if err != nil {
		return nil, err
	}
	var candidates []string
	for rows.Next() {
		var source string
		if err := rows.Scan(&source); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		candidates = append(candidates, source)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	aliases := make([]string, 0, len(candidates))
	for _, source := range candidates {
		resolved, err := resolveTrafficPeriodKeyTx(ctx, tx, userID, source)
		if err != nil {
			return nil, err
		}
		if resolved == targetPeriodKey {
			aliases = append(aliases, source)
		}
	}
	return aliases, nil
}

// SetPlanRevisionActivationChange links an access change to a pending
// revision once the change is created.
func (s *Store) SetPlanRevisionActivationChange(ctx context.Context, planID, revisionID, accessChangeID int64) error {
	res, err := s.db.ExecContext(ctx, `update subscription_plan_revisions set activation_change_id=? where id=? and plan_id=?`, accessChangeID, revisionID, planID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func loadPlanRevisionSnapshotTx(ctx context.Context, tx *sql.Tx, planID, revisionID int64) (model.SubscriptionPlanRevision, []model.SubscriptionPlanNode, error) {
	var v model.SubscriptionPlanRevision
	var createdBy, orderTemplateID, orderSourcePlanID, orderSourceRevisionID sql.NullInt64
	var ca string
	var activatedAt sql.NullString
	var policyJSON string
	var basedOn sql.NullInt64
	var activationChangeID sql.NullInt64
	if err := tx.QueryRowContext(ctx, `select id,plan_id,revision,version_no,status,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,node_order_policy_json,order_template_id,order_template_revision,order_source_plan_id,order_source_revision_id,order_source_mode,created_by,created_at,activated_at,based_on_revision_id,change_kind,change_summary,activation_change_id from subscription_plan_revisions where id=? and plan_id=?`, revisionID, planID).Scan(&v.ID, &v.PlanID, &v.Revision, &v.VersionNo, &v.Status, &v.SpeedLimitMbps, &v.TrafficLimitBytes, &v.TrafficResetMode, &v.TrafficResetDay, &policyJSON, &orderTemplateID, &v.OrderTemplateRevision, &orderSourcePlanID, &orderSourceRevisionID, &v.OrderSourceMode, &createdBy, &ca, &activatedAt, &basedOn, &v.ChangeKind, &v.ChangeSummary, &activationChangeID); err != nil {
		return v, nil, err
	}
	v.NodeOrderPolicy = parseOrderPolicy(policyJSON)
	if orderTemplateID.Valid {
		value := orderTemplateID.Int64
		v.OrderTemplateID = &value
	}
	if orderSourcePlanID.Valid {
		value := orderSourcePlanID.Int64
		v.OrderSourcePlanID = &value
	}
	if orderSourceRevisionID.Valid {
		value := orderSourceRevisionID.Int64
		v.OrderSourceRevisionID = &value
	}
	if v.VersionNo == 0 {
		v.VersionNo = v.Revision
	}
	if basedOn.Valid {
		v.BasedOnRevisionID = basedOn.Int64
	}
	if activationChangeID.Valid {
		id := activationChangeID.Int64
		v.ActivationChangeID = &id
	}
	if createdBy.Valid {
		v.CreatedBy = &createdBy.Int64
	}
	v.CreatedAt = parseTime(ca)
	if activatedAt.Valid {
		t := parseTime(activatedAt.String)
		v.ActivatedAt = &t
	}
	rows, err := tx.QueryContext(ctx, `select id,revision_id,node_type,node_id,display_group,source_type,source_rule_id,sort_position,display_name_override,created_at from subscription_plan_revision_nodes where revision_id=? order by node_type,node_id`, revisionID)
	if err != nil {
		return v, nil, err
	}
	defer rows.Close()
	var nodes []model.SubscriptionPlanNode
	for rows.Next() {
		var pn model.SubscriptionPlanNode
		var sortPosition sql.NullInt64
		var nodeCA string
		var displayNameOverride sql.NullString
		if err := rows.Scan(&pn.ID, &pn.RevisionID, &pn.NodeType, &pn.NodeID, &pn.DisplayGroup, &pn.SourceType, &pn.SourceRuleID, &sortPosition, &displayNameOverride, &nodeCA); err != nil {
			return v, nil, err
		}
		if sortPosition.Valid {
			position := int(sortPosition.Int64)
			pn.SortPosition = &position
		}
		if displayNameOverride.Valid {
			value := displayNameOverride.String
			pn.DisplayNameOverride = &value
		}
		pn.CreatedAt = parseTime(nodeCA)
		nodes = append(nodes, pn)
	}
	if err := rows.Err(); err != nil {
		return v, nil, err
	}
	return v, nodes, nil
}

func clonePlanNodeSlice(in []model.SubscriptionPlanNode) []model.SubscriptionPlanNode {
	out := make([]model.SubscriptionPlanNode, len(in))
	copy(out, in)
	for i := range out {
		out[i].DisplayNameOverride = cloneStringPointer(out[i].DisplayNameOverride)
	}
	return out
}

func applySettingsMutation(v *model.SubscriptionPlanRevision, m *PlanSettingsMutation) {
	if m.SpeedLimitMbps != nil {
		v.SpeedLimitMbps = *m.SpeedLimitMbps
	}
	if m.TrafficLimitBytes != nil {
		v.TrafficLimitBytes = *m.TrafficLimitBytes
	}
	if m.TrafficResetMode != nil {
		v.TrafficResetMode = *m.TrafficResetMode
	}
	if m.TrafficResetDay != nil {
		v.TrafficResetDay = *m.TrafficResetDay
	}
}

func applyNodesMutation(nodes []model.SubscriptionPlanNode, m *PlanNodesMutation) ([]model.SubscriptionPlanNode, error) {
	byKey := map[string]model.SubscriptionPlanNode{}
	for _, pn := range nodes {
		byKey[planRevisionNodeKey(pn.NodeType, pn.NodeID)] = pn
	}
	switch m.Op {
	case "add":
		for _, pn := range m.Nodes {
			key := planRevisionNodeKey(pn.NodeType, pn.NodeID)
			if _, exists := byKey[key]; exists {
				// Upsert semantics: a node already in the set adopts the new
				// display group (and any other mutable fields) instead of
				// duplicating the row.
				existing := byKey[key]
				existing.DisplayGroup = pn.DisplayGroup
				if pn.SourceType != "" {
					existing.SourceType = pn.SourceType
				}
				if pn.SourceRuleID != 0 {
					existing.SourceRuleID = pn.SourceRuleID
				}
				if pn.DisplayNameOverride != nil {
					existing.DisplayNameOverride = cloneStringPointer(pn.DisplayNameOverride)
				}
				byKey[key] = existing
				continue
			}
			byKey[key] = pn
		}
	case "remove":
		for _, pn := range m.Nodes {
			delete(byKey, planRevisionNodeKey(pn.NodeType, pn.NodeID))
		}
	case "replace":
		replacement := map[string]model.SubscriptionPlanNode{}
		for _, pn := range m.Nodes {
			key := planRevisionNodeKey(pn.NodeType, pn.NodeID)
			// Surviving rows keep their sort_position; new rows start NULL.
			if existing, ok := byKey[key]; ok && !m.ReplaceSnapshot {
				pn.SortPosition = existing.SortPosition
				if pn.DisplayGroup == "" {
					pn.DisplayGroup = existing.DisplayGroup
				}
				if pn.SourceType == "" {
					pn.SourceType = existing.SourceType
				}
				pn.DisplayNameOverride = cloneStringPointer(existing.DisplayNameOverride)
			}
			replacement[key] = pn
		}
		byKey = replacement
	default:
		return nil, fmt.Errorf("op must be add, remove, or replace")
	}
	out := make([]model.SubscriptionPlanNode, 0, len(byKey))
	for _, pn := range byKey {
		out = append(out, pn)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.NodeType != b.NodeType {
			return a.NodeType < b.NodeType
		}
		return a.NodeID < b.NodeID
	})
	return out, nil
}

func PreviewPlanNodesMutation(nodes []model.SubscriptionPlanNode, mutation PlanNodesMutation) ([]model.SubscriptionPlanNode, error) {
	return applyNodesMutation(clonePlanNodeSlice(nodes), &mutation)
}

func normalizeManualOrderKeys(list []string) []string {
	out := make([]string, 0, len(list))
	for _, raw := range list {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		out = append(out, key)
	}
	return out
}

func manualPositionsByKey(nodes []model.SubscriptionPlanNode, manualOrder []string) (map[string]int, error) {
	byKey := map[string]bool{}
	for _, pn := range nodes {
		byKey[planRevisionNodeKey(pn.NodeType, pn.NodeID)] = true
	}
	out := map[string]int{}
	seen := map[string]bool{}
	for position, key := range manualOrder {
		if seen[key] {
			return nil, fmt.Errorf("%w: manual node order contains duplicate keys", ErrPlanOrderingInvalid)
		}
		seen[key] = true
		if !byKey[key] {
			return nil, fmt.Errorf("%w: manual node order contains %s which is not in the version node set", ErrPlanOrderingInvalid, key)
		}
		out[key] = position
	}
	return out, nil
}

func samePlanLimits(a, b model.SubscriptionPlanRevision) bool {
	return a.SpeedLimitMbps == b.SpeedLimitMbps &&
		a.TrafficLimitBytes == b.TrafficLimitBytes &&
		a.TrafficResetMode == b.TrafficResetMode &&
		a.TrafficResetDay == b.TrafficResetDay
}

func sameNodeMembership(a, b []model.SubscriptionPlanNode) bool {
	keys := func(nodes []model.SubscriptionPlanNode) map[string]bool {
		out := map[string]bool{}
		for _, pn := range nodes {
			out[planRevisionNodeKey(pn.NodeType, pn.NodeID)] = true
		}
		return out
	}
	ka, kb := keys(a), keys(b)
	if len(ka) != len(kb) {
		return false
	}
	for key := range ka {
		if !kb[key] {
			return false
		}
	}
	return true
}

func planVersionDigest(rev model.SubscriptionPlanRevision, nodes []model.SubscriptionPlanNode, rules []model.PlanMembershipRule, exclusions []model.PlanNodeExclusion) string {
	digestNodes := make([]model.SubscriptionPlanNode, len(nodes))
	copy(digestNodes, nodes)
	sort.Slice(digestNodes, func(i, j int) bool {
		return planRevisionNodeKey(digestNodes[i].NodeType, digestNodes[i].NodeID) < planRevisionNodeKey(digestNodes[j].NodeType, digestNodes[j].NodeID)
	})
	type nodeDigest struct {
		Key                 string  `json:"key"`
		DisplayGroup        string  `json:"display_group,omitempty"`
		SortPosition        *int    `json:"sort_position,omitempty"`
		DisplayNameOverride *string `json:"display_name_override,omitempty"`
		SourceType          string  `json:"source_type,omitempty"`
		SourceRuleID        int64   `json:"source_rule_id,omitempty"`
	}
	nds := make([]nodeDigest, 0, len(digestNodes))
	for _, pn := range digestNodes {
		nd := nodeDigest{Key: planRevisionNodeKey(pn.NodeType, pn.NodeID), DisplayGroup: pn.DisplayGroup, DisplayNameOverride: pn.DisplayNameOverride, SourceType: string(pn.SourceType), SourceRuleID: pn.SourceRuleID}
		if pn.SortPosition != nil {
			position := *pn.SortPosition
			nd.SortPosition = &position
		}
		nds = append(nds, nd)
	}
	type ruleDigest struct {
		RuleID   int64  `json:"rule_id"`
		Kind     string `json:"kind"`
		ScopeKey string `json:"scope_key"`
	}
	ruleValues := make([]ruleDigest, 0, len(rules))
	for _, rule := range rules {
		ruleValues = append(ruleValues, ruleDigest{RuleID: rule.RuleID, Kind: rule.Kind, ScopeKey: rule.ScopeKey})
	}
	sort.Slice(ruleValues, func(i, j int) bool { return ruleValues[i].RuleID < ruleValues[j].RuleID })
	exclusionValues := make([]string, 0, len(exclusions))
	for _, exclusion := range exclusions {
		exclusionValues = append(exclusionValues, planRevisionNodeKey(exclusion.NodeType, exclusion.NodeID))
	}
	sort.Strings(exclusionValues)
	raw, _ := json.Marshal(map[string]any{
		"limits":                   []any{rev.SpeedLimitMbps, rev.TrafficLimitBytes, rev.TrafficResetMode, rev.TrafficResetDay},
		"policy":                   rev.NodeOrderPolicy,
		"order_template_id":        rev.OrderTemplateID,
		"order_template_revision":  rev.OrderTemplateRevision,
		"order_source_plan_id":     rev.OrderSourcePlanID,
		"order_source_revision_id": rev.OrderSourceRevisionID,
		"order_source_mode":        rev.OrderSourceMode,
		"membership_rules":         ruleValues,
		"node_exclusions":          exclusionValues,
		"nodes":                    nds,
	})
	return string(raw)
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func nullableInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

// ErrPlanOrderingInvalid marks ordering validation failures that are client
// errors (duplicate keys, keys outside the draft node set).
var ErrPlanOrderingInvalid = errors.New("invalid plan ordering")

// UpdatePlanDraftOrdering writes the ordering policy and renumbers manual
// positions on the draft revision with optimistic concurrency. manualOrder is
// the complete ordered node key list for manual mode; every node not listed
// keeps a NULL position and sorts after the placed nodes using the manual seed.
// Returns the draft revision id.
func (s *Store) UpdatePlanDraftOrdering(ctx context.Context, planID, expectedRevision int64, policy model.SubscriptionNodeOrderPolicy, manualOrder []string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := checkPlanRevisionTx(ctx, tx, planID, expectedRevision); err != nil {
		return 0, err
	}
	ts := now()
	draftID, err := ensurePlanDraftTx(ctx, tx, planID, ts)
	if err != nil {
		return 0, err
	}
	existing, err := listDraftRevisionNodesTx(ctx, tx, draftID)
	if err != nil {
		return 0, err
	}
	nodesByKey := map[string]model.SubscriptionPlanNode{}
	for _, row := range existing {
		nodesByKey[planRevisionNodeKey(row.NodeType, row.NodeID)] = row
	}
	// Manual positions only exist in manual mode. Auto modes keep the last
	// manual positions so switching back can offer "continue with the last
	// manual order"; they are ignored by the auto comparators.
	if policy.Mode == model.SubscriptionNodeOrderManual {
		seen := map[string]bool{}
		for _, key := range manualOrder {
			if seen[key] {
				return 0, fmt.Errorf("%w: manual node order contains duplicate keys", ErrPlanOrderingInvalid)
			}
			seen[key] = true
			if _, ok := nodesByKey[key]; !ok {
				return 0, fmt.Errorf("%w: manual node order contains %s which is not in the draft node set", ErrPlanOrderingInvalid, key)
			}
		}
		if _, err := tx.ExecContext(ctx, `update subscription_plan_revision_nodes set sort_position=NULL where revision_id=?`, draftID); err != nil {
			return 0, err
		}
		for position, key := range manualOrder {
			row := nodesByKey[key]
			if _, err := tx.ExecContext(ctx, `update subscription_plan_revision_nodes set sort_position=? where revision_id=? and node_type=? and node_id=?`, position, draftID, row.NodeType, row.NodeID); err != nil {
				return 0, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set node_order_policy_json=? where id=?`, marshalOrderPolicy(policy), draftID); err != nil {
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

// GetEffectiveSubscriptionNodeOrdering loads the ordering policy and manual
// positions of the plan revision the user's effective binding grants at time
// at. A user without an enabled in-window binding gets nil values and the
// subscription generator falls back to the legacy system ordering.
func (s *Store) GetEffectiveSubscriptionNodeOrdering(ctx context.Context, userID int64, at time.Time) (*model.SubscriptionNodeOrderPolicy, map[string]int, error) {
	policy, positions, _, err := s.GetEffectiveSubscriptionNodePresentation(ctx, userID, at)
	return policy, positions, err
}

func (s *Store) GetEffectiveSubscriptionNodePresentation(ctx context.Context, userID int64, at time.Time) (*model.SubscriptionNodeOrderPolicy, map[string]int, map[string]*string, error) {
	binding, err := s.GetActiveUserPlanBinding(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, err
	}
	if binding.StartsAt != nil && binding.StartsAt.After(at) {
		return nil, nil, nil, nil
	}
	if binding.ExpiresAt != nil && !binding.ExpiresAt.After(at) {
		return nil, nil, nil, nil
	}
	plan, err := s.GetSubscriptionPlan(ctx, binding.PlanID)
	if err != nil {
		return nil, nil, nil, err
	}
	if !plan.Enabled || plan.CurrentRevisionID == 0 {
		return nil, nil, nil, nil
	}
	revision, err := s.GetPlanRevision(ctx, plan.ID, plan.CurrentRevisionID)
	if err != nil {
		return nil, nil, nil, err
	}
	nodes, err := s.ListPlanRevisionNodes(ctx, plan.CurrentRevisionID)
	if err != nil {
		return nil, nil, nil, err
	}
	positions := map[string]int{}
	names := map[string]*string{}
	for _, node := range nodes {
		key := planRevisionNodeKey(node.NodeType, node.NodeID)
		if node.SortPosition != nil {
			positions[key] = *node.SortPosition
		}
		if node.DisplayNameOverride != nil {
			names[key] = cloneStringPointer(node.DisplayNameOverride)
		}
	}
	policy := revision.NodeOrderPolicy
	return &policy, positions, names, nil
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
	rows, err := s.db.QueryContext(ctx, userPlanBindingSelect+` where user_id=? and enabled=1 order by id desc limit 1`, userID)
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
	rows, err := s.db.QueryContext(ctx, userPlanBindingSelect+` where enabled=1 and status='active' order by user_id`)
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
	rows, err := s.db.QueryContext(ctx, userPlanBindingSelect+` where enabled=1 and status='active' and (starts_at is null or starts_at <= ?) and (expires_at is null or expires_at > ?) order by user_id`, at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPlanBindings(rows)
}

func (s *Store) ListUserPlanBindingsForPlan(ctx context.Context, planID int64) ([]model.UserPlanBinding, error) {
	rows, err := s.db.QueryContext(ctx, userPlanBindingSelect+` where plan_id=? and enabled=1 and status='active' order by user_id`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPlanBindings(rows)
}

const userPlanBindingSelect = `select id,user_id,plan_id,enabled,starts_at,expires_at,traffic_reset_anchor_at,assigned_by,created_at,updated_at from user_plan_bindings`

func scanUserPlanBindings(rows *sql.Rows) ([]model.UserPlanBinding, error) {
	var out []model.UserPlanBinding
	for rows.Next() {
		var v model.UserPlanBinding
		var enabled int
		var startsAt, expiresAt, resetAnchorAt sql.NullString
		var assignedBy sql.NullInt64
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.UserID, &v.PlanID, &enabled, &startsAt, &expiresAt, &resetAnchorAt, &assignedBy, &ca, &ua); err != nil {
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
		if resetAnchorAt.Valid {
			t := parseTime(resetAnchorAt.String)
			v.TrafficResetAnchorAt = &t
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
// transaction with an active lifecycle status. A binding with PlanID==0 removes
// the active binding. Exactly one enabled binding can exist per user (partial
// unique index), so each switch disables the previous binding before inserting
// the new one.
func (s *Store) SetUserPlanBindings(ctx context.Context, bindings []model.UserPlanBinding) error {
	return s.setUserPlanBindings(ctx, bindings, "active")
}

// SetUserPlanBindingsPending is the two-phase variant: the new binding is
// stored as pending so the plan snapshot keeps ignoring it until the access
// change activation flips it to active.
func (s *Store) SetUserPlanBindingsPending(ctx context.Context, bindings []model.UserPlanBinding) error {
	return s.setUserPlanBindings(ctx, bindings, "pending")
}

func (s *Store) setUserPlanBindings(ctx context.Context, bindings []model.UserPlanBinding, status string) error {
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
		var anchor any
		if status == "active" {
			anchor = ts
			if v.StartsAt != nil {
				anchor = v.StartsAt.UTC().Format(time.RFC3339Nano)
			}
		}
		if _, err := tx.ExecContext(ctx, `insert into user_plan_bindings(user_id,plan_id,enabled,status,starts_at,expires_at,traffic_reset_anchor_at,assigned_by,created_at,updated_at) values(?,?,1,?,?,?,?,?,?,?)`, v.UserID, v.PlanID, status, nilTime(v.StartsAt), nilTime(v.ExpiresAt), anchor, v.AssignedBy, ts, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetUserPlanBindingsActive flips pending bindings to active. The lifecycle
// worker and change activation call this at the binding's effective time.
func (s *Store) SetUserPlanBindingsActive(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	ts := now()
	args = append(args, ts, ts)
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `update user_plan_bindings set status='active',traffic_reset_anchor_at=coalesce(traffic_reset_anchor_at,?),updated_at=? where id in (`+strings.Join(placeholders, ",")+`) and status='pending'`, args...) // #nosec G202 -- placeholders contains only generated question marks.
	return err
}

// SetUserPlanBindingsActiveForUsers flips pending bindings of the given users
// to active and records the deployed claim in the same statement, so the
// lifecycle worker never creates a second change for a binding the access
// change engine already activated.
func (s *Store) SetUserPlanBindingsActiveForUsers(ctx context.Context, userIDs []int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(userIDs))
	args := make([]any, 0, len(userIDs)+2)
	ts := now()
	args = append(args, ts, ts, ts)
	for _, id := range userIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `update user_plan_bindings set status='active',deployed_at=coalesce(deployed_at,?),traffic_reset_anchor_at=coalesce(traffic_reset_anchor_at,?),updated_at=? where user_id in (`+strings.Join(placeholders, ",")+`) and status='pending'`, args...) // #nosec G202 -- placeholders contains only generated question marks.
	return err
}

// AccessLifecycleNextDue returns the next future moment at which the
// authorization lifecycle needs to run, or nil when nothing is scheduled. It
// mirrors the four lifecycle scans: bindings whose window opens but were never
// deployed, bindings whose expiry was never synced, pending exceptions without
// an owning change, and active exceptions past expiry. A nil result lets the
// Controller sleep on the fallback interval instead of polling.
func (s *Store) AccessLifecycleNextDue(ctx context.Context, at time.Time) (*time.Time, error) {
	nowText := at.UTC().Format(time.RFC3339Nano)
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `select min(due) from (
		select min(coalesce(starts_at,?)) as due from user_plan_bindings where enabled=1 and deployed_at is null
		union all
		select min(expires_at) from user_plan_bindings where enabled=1 and expires_at is not null and expires_at != '' and deployed_at is not null and expiry_synced_at is null
		union all
		select min(coalesce(starts_at,?)) from user_node_exceptions where status='pending' and change_id is null and (expires_at is null or expires_at = '' or expires_at>?)
		union all
		select min(expires_at) from user_node_exceptions where status='active' and expires_at is not null and expires_at != '' and expiry_synced_at is null
	)`, nowText, nowText, nowText).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	due := parseTime(raw.String)
	if due.IsZero() {
		return nil, nil
	}
	return &due, nil
}

// ClaimBindingsDeployedForUsers claims every enabled binding of the users as
// deployed. Used when a scheduler-created change owns the bindings.
func (s *Store) ClaimBindingsDeployedForUsers(ctx context.Context, userIDs []int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(userIDs))
	args := make([]any, 0, len(userIDs)+1)
	args = append(args, now())
	for _, id := range userIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `update user_plan_bindings set deployed_at=coalesce(deployed_at,?) where user_id in (`+strings.Join(placeholders, ",")+`) and deployed_at is null`, args...) // #nosec G202 -- placeholders contains only generated question marks.
	return err
}

// ListEnabledUserPlanBindings returns every enabled binding for the given
// users, including future-dated and expired ones, so the access-change engine
// can compute old-union-new prepare projections.
func (s *Store) ListEnabledUserPlanBindings(ctx context.Context, userIDs []int64) ([]model.UserPlanBinding, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(userIDs))
	args := make([]any, 0, len(userIDs))
	for _, id := range userIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, userPlanBindingSelect+` where enabled=1 and user_id in (`+strings.Join(placeholders, ",")+`) order by user_id,id desc`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPlanBindings(rows)
}

// ListBindingsDueForDeploy returns enabled bindings whose time window already
// contains at but whose runtime state was never synced (deployed_at is NULL).
// The lifecycle worker turns each of them into an access change.
func (s *Store) ListBindingsDueForDeploy(ctx context.Context, at time.Time) ([]model.UserPlanBinding, error) {
	rows, err := s.db.QueryContext(ctx, userPlanBindingSelect+` where enabled=1 and deployed_at is null and (starts_at is null or starts_at <= ?) and (expires_at is null or expires_at > ?) order by id`, at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPlanBindings(rows)
}

// ClaimBindingsDeployed marks bindings as claimed by an access change so the
// lifecycle worker does not create a second change for them.
func (s *Store) ClaimBindingsDeployed(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, now())
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `update user_plan_bindings set deployed_at=? where id in (`+strings.Join(placeholders, ",")+`) and deployed_at is null`, args...) // #nosec G202 -- placeholders contains only generated question marks.
	return err
}

// ListExpiredBindingsNeedingSync returns bindings whose window ended, whose
// runtime state was deployed, and whose removal was never finalized. The
// lifecycle worker creates removal changes for them.
func (s *Store) ListExpiredBindingsNeedingSync(ctx context.Context, at time.Time) ([]model.UserPlanBinding, error) {
	rows, err := s.db.QueryContext(ctx, userPlanBindingSelect+` where enabled=1 and expires_at is not null and expires_at <= ? and deployed_at is not null and expiry_synced_at is null order by id`, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserPlanBindings(rows)
}

// MarkBindingsExpirySynced records that a removal change for the expired
// bindings reached the runtime.
func (s *Store) MarkBindingsExpirySynced(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, now())
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `update user_plan_bindings set expiry_synced_at=? where id in (`+strings.Join(placeholders, ",")+`) and expiry_synced_at is null`, args...) // #nosec G202 -- placeholders contains only generated question marks.
	return err
}

func (s *Store) CreateUserNodeException(ctx context.Context, v *model.UserNodeException) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	if strings.TrimSpace(string(v.Status)) == "" {
		v.Status = model.UserNodeExceptionActive
	}
	res, err := s.db.ExecContext(ctx, `insert into user_node_exceptions(user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at) values(?,?,?,?,?,?,?,?,?,?)`, v.UserID, v.NodeType, v.NodeID, v.Effect, v.Reason, string(v.Status), nilTime(v.StartsAt), expiresAtDB(v.ExpiresAt), v.CreatedBy, ts)
	if err != nil {
		if isUniqueConstraintError(err) {
			return errors.New("该用户在此节点上已存在例外，请更新现有例外而不是重复创建")
		}
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

// UserNodeExceptionWrite is one row of a batch exception write. ID 0 inserts
// a new row, ID > 0 updates the existing row in place. Nil ExpiresAt means
// permanent authorization.
type UserNodeExceptionWrite struct {
	ID        int64
	UserID    int64
	NodeType  model.AssignableNodeType
	NodeID    int64
	Effect    model.UserNodeExceptionEffect
	Reason    string
	Status    model.UserNodeExceptionStatus
	StartsAt  *time.Time
	ExpiresAt *time.Time
	CreatedBy *int64
}

// ApplyUserNodeExceptionBatch writes every row in one transaction so a batch
// apply either fully lands or fully rolls back. Rows are returned with their
// final IDs and timestamps.
func (s *Store) ApplyUserNodeExceptionBatch(ctx context.Context, writes []UserNodeExceptionWrite) ([]model.UserNodeException, error) {
	if len(writes) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now()
	out := make([]model.UserNodeException, 0, len(writes))
	for _, w := range writes {
		status := strings.TrimSpace(string(w.Status))
		if status == "" {
			status = string(model.UserNodeExceptionActive)
		}
		if w.ID == 0 {
			res, err := tx.ExecContext(ctx, `insert into user_node_exceptions(user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at) values(?,?,?,?,?,?,?,?,?,?)`, w.UserID, w.NodeType, w.NodeID, w.Effect, w.Reason, status, nilTime(w.StartsAt), expiresAtDB(w.ExpiresAt), w.CreatedBy, ts)
			if err != nil {
				if isUniqueConstraintError(err) {
					return nil, errors.New("该用户在此节点上已存在例外，请更新现有例外而不是重复创建")
				}
				return nil, err
			}
			w.ID, _ = res.LastInsertId()
		} else {
			if _, err := tx.ExecContext(ctx, `update user_node_exceptions set user_id=?,node_type=?,node_id=?,effect=?,reason=?,status=?,starts_at=?,expires_at=? where id=?`, w.UserID, w.NodeType, w.NodeID, w.Effect, w.Reason, status, nilTime(w.StartsAt), expiresAtDB(w.ExpiresAt), w.ID); err != nil {
				return nil, err
			}
		}
		out = append(out, model.UserNodeException{
			ID:        w.ID,
			UserID:    w.UserID,
			NodeType:  w.NodeType,
			NodeID:    w.NodeID,
			Effect:    w.Effect,
			Reason:    w.Reason,
			Status:    model.UserNodeExceptionStatus(status),
			StartsAt:  w.StartsAt,
			ExpiresAt: w.ExpiresAt,
			CreatedBy: w.CreatedBy,
			CreatedAt: parseTime(ts),
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) UpdateUserNodeException(ctx context.Context, v *model.UserNodeException) error {
	if strings.TrimSpace(string(v.Status)) == "" {
		v.Status = model.UserNodeExceptionActive
	}
	_, err := s.db.ExecContext(ctx, `update user_node_exceptions set user_id=?,node_type=?,node_id=?,effect=?,reason=?,status=?,starts_at=?,expires_at=? where id=?`, v.UserID, v.NodeType, v.NodeID, v.Effect, v.Reason, string(v.Status), nilTime(v.StartsAt), expiresAtDB(v.ExpiresAt), v.ID)
	if err != nil && isUniqueConstraintError(err) {
		return errors.New("该用户在此节点上已存在例外，请更新现有例外而不是重复创建")
	}
	return err
}

func (s *Store) DeleteUserNodeException(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `delete from user_node_exceptions where id=?`, id)
	return err
}

func (s *Store) ListUserNodeExceptions(ctx context.Context) ([]model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at from user_node_exceptions order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

func (s *Store) ListUserNodeExceptionsForUser(ctx context.Context, userID int64) ([]model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at from user_node_exceptions where user_id=? order by id desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

func (s *Store) ListUserNodeExceptionsForNode(ctx context.Context, nodeType string, nodeID int64) ([]model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at from user_node_exceptions where node_type=? and node_id=? order by id desc`, nodeType, nodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

// DeleteExpiredUserNodeExceptions removes exceptions whose expiry has passed.
// Permanent rows with NULL or empty expires_at are never deleted. It returns the number
// of removed rows.
func (s *Store) DeleteExpiredUserNodeExceptions(ctx context.Context, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `delete from user_node_exceptions where expires_at is not null and expires_at != '' and expires_at <= ?`, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetUserNodeException loads one exception row.
func (s *Store) GetUserNodeException(ctx context.Context, id int64) (*model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at from user_node_exceptions where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUserNodeExceptions(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

// SetUserNodeExceptionStatus transitions one exception's audited lifecycle
// state. Rows are never physically deleted once created.
func (s *Store) SetUserNodeExceptionStatus(ctx context.Context, id int64, status model.UserNodeExceptionStatus) error {
	_, err := s.db.ExecContext(ctx, `update user_node_exceptions set status=? where id=?`, string(status), id)
	return err
}

// ListUserNodeExceptionsByStatus returns pending or active exceptions whose
// time window contains at, used by the lifecycle worker and change activation.
// Nil or empty ExpiresAt means permanent authorization and is always in window.
func (s *Store) ListUserNodeExceptionsByStatus(ctx context.Context, at time.Time, statuses ...model.UserNodeExceptionStatus) ([]model.UserNodeException, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	placeholders := make([]string, 0, len(statuses))
	args := make([]any, 0, len(statuses)+2)
	for _, status := range statuses {
		placeholders = append(placeholders, "?")
		args = append(args, string(status))
	}
	args = append(args, at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano))
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at from user_node_exceptions where status in (`+strings.Join(placeholders, ",")+`) and (starts_at is null or starts_at <= ?) and (expires_at is null or expires_at = '' or expires_at > ?) order by id`, args...) // #nosec G202 -- placeholders contains only generated question marks.
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

// ListActiveExceptionsExpired returns active exceptions whose expiry passed
// and that still need a removal change (expiry_synced_at is NULL). Permanent
// rows with NULL or empty expires_at are never considered expired.
func (s *Store) ListActiveExceptionsExpired(ctx context.Context, at time.Time) ([]model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at from user_node_exceptions where status='active' and expires_at is not null and expires_at != '' and expires_at <= ? and expiry_synced_at is null order by id`, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

// MarkExceptionsExpirySynced records that the removal change for the expired
// exceptions reached the runtime.
func (s *Store) MarkExceptionsExpirySynced(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, now())
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	_, err := s.db.ExecContext(ctx, `update user_node_exceptions set expiry_synced_at=? where id in (`+strings.Join(placeholders, ",")+`) and expiry_synced_at is null`, args...) // #nosec G202 -- placeholders contains only generated question marks.
	return err
}

// SetUserNodeExceptionChange records the access change that owns a pending
// exception so the lifecycle fallback never creates a second change for it.
func (s *Store) SetUserNodeExceptionChange(ctx context.Context, id, changeID int64) error {
	_, err := s.db.ExecContext(ctx, `update user_node_exceptions set change_id=? where id=?`, nullInt64(changeID), id)
	return err
}

// ListPendingExceptionsWithoutChange returns pending exceptions whose time
// window is open and that no access change owns yet (crash recovery fallback).
// Nil or empty ExpiresAt means permanent and passes the expiry check.
func (s *Store) ListPendingExceptionsWithoutChange(ctx context.Context, at time.Time) ([]model.UserNodeException, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,node_type,node_id,effect,reason,status,starts_at,expires_at,created_by,created_at from user_node_exceptions where status='pending' and change_id is null and (starts_at is null or starts_at <= ?) and (expires_at is null or expires_at = '' or expires_at > ?) order by id`, at.UTC().Format(time.RFC3339Nano), at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserNodeExceptions(rows)
}

func scanUserNodeExceptions(rows *sql.Rows) ([]model.UserNodeException, error) {
	var out []model.UserNodeException
	for rows.Next() {
		var v model.UserNodeException
		var createdBy sql.NullInt64
		var startsAt, expiresAt sql.NullString
		var ca string
		if err := rows.Scan(&v.ID, &v.UserID, &v.NodeType, &v.NodeID, &v.Effect, &v.Reason, &v.Status, &startsAt, &expiresAt, &createdBy, &ca); err != nil {
			return nil, err
		}
		if startsAt.Valid {
			t := parseTime(startsAt.String)
			v.StartsAt = &t
		}
		if expiresAt.Valid && strings.TrimSpace(expiresAt.String) != "" {
			t := parseTime(expiresAt.String)
			if !t.IsZero() {
				v.ExpiresAt = &t
			}
		}
		if createdBy.Valid {
			v.CreatedBy = &createdBy.Int64
		}
		v.CreatedAt = parseTime(ca)
		out = append(out, v)
	}
	return out, rows.Err()
}

func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") || strings.Contains(text, "constraint failed")
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
			return errors.Join(err, rows.Close())
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

// migratePlanVersionPointers upgrades the draft/active revision model to the
// immutable version model. It runs after migratePlanRevisions so the legacy
// active_revision_id/draft_revision_id columns exist and are backfilled.
// Existing plans map current = old active, latest = old draft (frozen as an
// unapplied legacy migration version) or old active when no draft exists, and
// lock_version = old revision counter. An upgrade never reorders an issued
// subscription because the ordering policy and node set of those revisions
// are untouched.
func (s *Store) migratePlanVersionPointers(ctx context.Context) error {
	// The old model's per-status uniqueness no longer holds: versions are
	// immutable and the plan pointer columns are the only invariants.
	if _, err := s.db.ExecContext(ctx, `drop index if exists idx_plan_revisions_one_active`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `drop index if exists idx_plan_revisions_one_draft`); err != nil {
		return err
	}
	// Backfill only once, guarded by the current_revision_id sentinel: plans
	// created by the new model already set the pointer columns.
	if _, err := s.db.ExecContext(ctx, `update subscription_plans set lock_version=revision,current_revision_id=active_revision_id,latest_revision_id=coalesce(draft_revision_id,active_revision_id),pending_revision_id=null where current_revision_id is null and active_revision_id is not null`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `update subscription_plan_revisions set version_no=revision where version_no is null`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `update subscription_plan_revisions set change_kind=?,change_summary=? where status='draft' and change_kind=''`, model.PlanChangeKindLegacyDraftMigration, "从旧草稿机制迁移的未应用版本"); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create unique index if not exists idx_subscription_plan_revision_version_no on subscription_plan_revisions(plan_id, version_no)`); err != nil {
		return err
	}
	return nil
}

// migrateUserNodeExceptionLifecycle upgrades the exception table to the
// audited lifecycle: status (pending/active/expired/revoked), starts_at and
// expiry_synced_at columns, one unique row per (user, node) and the indexes
// the lifecycle worker scans. Legacy rows become active with no start window.
func (s *Store) migrateUserNodeExceptionLifecycle(ctx context.Context) error {
	for _, column := range []struct {
		name string
		sql  string
	}{
		{"status", `alter table user_node_exceptions add column status text not null default 'active'`},
		{"starts_at", `alter table user_node_exceptions add column starts_at text`},
		{"expiry_synced_at", `alter table user_node_exceptions add column expiry_synced_at text`},
		{"change_id", `alter table user_node_exceptions add column change_id integer references access_changes(id) on delete set null`},
	} {
		if err := s.ensureColumn(ctx, "user_node_exceptions", column.name, column.sql); err != nil {
			return err
		}
	}
	// Dedupe overlapping rows before installing the unique key: keep the most
	// recent row per (user, node) so allow/deny conflicts are never silently
	// retained.
	if _, err := s.db.ExecContext(ctx, `delete from user_node_exceptions where id not in (select max(id) from user_node_exceptions group by user_id,node_type,node_id)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create unique index if not exists idx_user_node_exceptions_user_node on user_node_exceptions(user_id, node_type, node_id)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create index if not exists idx_user_node_exceptions_status on user_node_exceptions(status, expires_at)`); err != nil {
		return err
	}
	return nil
}

// migrateUserPlanBindingDeployTracking adds the columns the lifecycle worker
// uses to claim bindings for access changes and to record removal sync.
func (s *Store) migrateUserPlanBindingDeployTracking(ctx context.Context) error {
	for _, column := range []struct {
		name string
		sql  string
	}{
		{"status", `alter table user_plan_bindings add column status text not null default 'active'`},
		{"deployed_at", `alter table user_plan_bindings add column deployed_at text`},
		{"expiry_synced_at", `alter table user_plan_bindings add column expiry_synced_at text`},
		{"traffic_reset_anchor_at", `alter table user_plan_bindings add column traffic_reset_anchor_at text`},
	} {
		if err := s.ensureColumn(ctx, "user_plan_bindings", column.name, column.sql); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `update user_plan_bindings set traffic_reset_anchor_at=coalesce(deployed_at,starts_at,created_at) where traffic_reset_anchor_at is null and status='active'`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create index if not exists idx_user_plan_bindings_deploy on user_plan_bindings(deployed_at, starts_at, expires_at)`); err != nil {
		return err
	}
	return nil
}

// PlanNodeReference is one plan whose active or draft revision contains an
// assignable node.
type PlanNodeReference struct {
	PlanID int64  `json:"plan_id"`
	Name   string `json:"name"`
	Group  string `json:"group,omitempty"`
}

// PlanNodeReferences reports which plans reference an assignable node and how
// many user node exceptions point at it. Pending references cannot be removed
// while their access change is being applied.
type PlanNodeReferences struct {
	Active     []PlanNodeReference `json:"active"`
	Draft      []PlanNodeReference `json:"draft"`
	Pending    []PlanNodeReference `json:"pending"`
	Exceptions int                 `json:"exceptions"`
}

// PlanNodeReferences queries the live plan pointers rather than the legacy
// revision status column. New immutable plan versions leave old revisions
// archived, so status alone cannot identify the subscription's current set.
func (s *Store) PlanNodeReferences(ctx context.Context, nodeType model.AssignableNodeType, nodeID int64) (PlanNodeReferences, error) {
	refs, err := planNodeReferencesForQuery(ctx, s.db, nodeType, nodeID)
	if err != nil {
		return refs, err
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from user_node_exceptions where node_type=? and node_id=?`, string(nodeType), nodeID).Scan(&refs.Exceptions); err != nil {
		return refs, err
	}
	return refs, nil
}

func planNodeReferencesForQuery(ctx context.Context, q queryer, nodeType model.AssignableNodeType, nodeID int64) (PlanNodeReferences, error) {
	refs := PlanNodeReferences{}
	queries := []struct {
		pointer string
		out     *[]PlanNodeReference
	}{
		{pointer: "current_revision_id", out: &refs.Active},
		{pointer: "draft_revision_id", out: &refs.Draft},
		{pointer: "pending_revision_id", out: &refs.Pending},
	}
	for _, query := range queries {
		rows, err := q.QueryContext(ctx, `select p.id,p.name,coalesce(pn.display_group,'') from subscription_plan_revision_nodes pn join subscription_plans p on p.`+query.pointer+`=pn.revision_id where pn.node_type=? and pn.node_id=? order by p.id`, string(nodeType), nodeID)
		if err != nil {
			return refs, err
		}
		for rows.Next() {
			var ref PlanNodeReference
			if err := rows.Scan(&ref.PlanID, &ref.Name, &ref.Group); err != nil {
				return refs, errors.Join(err, rows.Close())
			}
			*query.out = append(*query.out, ref)
		}
		if err := rows.Err(); err != nil {
			return refs, errors.Join(err, rows.Close())
		}
		if err := rows.Close(); err != nil {
			return refs, err
		}
	}
	return refs, nil
}

// RemoveAssignableNodeFromPlans removes a deleted assignable node from every
// live plan pointer. Current revisions are replaced with a new immutable
// revision; legacy drafts are pruned in place so a later edit cannot restore a
// resource that no longer exists. The caller can use the returned references
// for an impact summary.
func (s *Store) RemoveAssignableNodeFromPlans(ctx context.Context, nodeType model.AssignableNodeType, nodeIDs ...int64) (PlanNodeReferences, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PlanNodeReferences{}, err
	}
	defer tx.Rollback()
	refs, err := removeAssignableNodeFromPlansTx(ctx, tx, nodeType, nodeIDs...)
	if err != nil {
		return refs, err
	}
	if err := tx.Commit(); err != nil {
		return refs, err
	}
	return refs, nil
}

func removeAssignableNodeFromPlansTx(ctx context.Context, tx *sql.Tx, nodeType model.AssignableNodeType, nodeIDs ...int64) (PlanNodeReferences, error) {
	ids := make(map[int64]struct{}, len(nodeIDs))
	for _, id := range nodeIDs {
		if id > 0 {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return PlanNodeReferences{}, nil
	}
	refsByPlan := map[int64]PlanNodeReference{}
	refs := PlanNodeReferences{}
	for id := range ids {
		one, err := planNodeReferencesForQuery(ctx, tx, nodeType, id)
		if err != nil {
			return refs, err
		}
		refs.Active = append(refs.Active, one.Active...)
		refs.Draft = append(refs.Draft, one.Draft...)
		refs.Pending = append(refs.Pending, one.Pending...)
	}
	for _, ref := range refs.Active {
		refsByPlan[ref.PlanID] = ref
	}
	for _, ref := range refs.Draft {
		if _, exists := refsByPlan[ref.PlanID]; !exists {
			refsByPlan[ref.PlanID] = ref
		}
	}
	for _, ref := range refs.Pending {
		if _, exists := refsByPlan[ref.PlanID]; !exists {
			refsByPlan[ref.PlanID] = ref
		}
	}
	if len(refs.Pending) > 0 {
		names := make([]string, 0, len(refs.Pending))
		seen := map[string]bool{}
		for _, ref := range refs.Pending {
			if !seen[ref.Name] {
				names = append(names, ref.Name)
				seen[ref.Name] = true
			}
		}
		return refs, fmt.Errorf("%w: subscription plan(s) still have a pending version: %s", ErrPlanVersionApplying, strings.Join(names, ", "))
	}

	planIDs := make([]int64, 0, len(refsByPlan))
	for planID := range refsByPlan {
		planIDs = append(planIDs, planID)
	}
	sort.Slice(planIDs, func(i, j int) bool { return planIDs[i] < planIDs[j] })
	idsList := make([]int64, 0, len(ids))
	for id := range ids {
		idsList = append(idsList, id)
	}
	for _, planID := range planIDs {
		var currentID, pendingID, draftID int64
		var planName string
		if err := tx.QueryRowContext(ctx, `select p.name,coalesce(p.current_revision_id,0),coalesce(p.pending_revision_id,0),coalesce(p.draft_revision_id,0) from subscription_plans p where p.id=?`, planID).Scan(&planName, &currentID, &pendingID, &draftID); err != nil {
			return refs, err
		}
		if pendingID != 0 {
			return refs, fmt.Errorf("%w: subscription plan %q is still applying", ErrPlanVersionApplying, planName)
		}
		currentChanged := false
		if currentID != 0 {
			revision, nodes, err := loadPlanRevisionSnapshotTx(ctx, tx, planID, currentID)
			if err != nil {
				return refs, err
			}
			candidate := make([]model.SubscriptionPlanNode, 0, len(nodes))
			for _, node := range nodes {
				if node.NodeType == nodeType {
					if _, remove := ids[node.NodeID]; remove {
						continue
					}
				}
				candidate = append(candidate, node)
			}
			if len(candidate) != len(nodes) {
				rules, err := listPlanRevisionRules(ctx, tx, currentID)
				if err != nil {
					return refs, err
				}
				exclusions, err := listPlanRevisionExclusions(ctx, tx, currentID)
				if err != nil {
					return refs, err
				}
				filteredExclusions := exclusions[:0]
				for _, exclusion := range exclusions {
					if exclusion.NodeType == nodeType {
						if _, remove := ids[exclusion.NodeID]; remove {
							continue
						}
					}
					filteredExclusions = append(filteredExclusions, exclusion)
				}
				ts := now()
				if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set status=? where plan_id=? and status=?`, string(model.PlanRevisionArchived), planID, string(model.PlanRevisionActive)); err != nil {
					return refs, err
				}
				newID, err := insertPlanRevisionTx(ctx, tx, planID, model.PlanRevisionActive, revision.SpeedLimitMbps, revision.TrafficLimitBytes, revision.TrafficResetMode, revision.TrafficResetDay, nil, marshalOrderPolicy(revision.NodeOrderPolicy), model.PlanChangeKindNodes, "节点删除时自动从订阅套餐移除", currentID, ts, ts)
				if err != nil {
					return refs, err
				}
				if _, err := tx.ExecContext(ctx, `update subscription_plan_revisions set order_template_id=?,order_template_revision=?,order_source_plan_id=?,order_source_revision_id=?,order_source_mode=? where id=?`, nullableInt64(sql.NullInt64{Int64: valueOrZero(revision.OrderTemplateID), Valid: revision.OrderTemplateID != nil}), revision.OrderTemplateRevision, nullableInt64(sql.NullInt64{Int64: valueOrZero(revision.OrderSourcePlanID), Valid: revision.OrderSourcePlanID != nil}), nullableInt64(sql.NullInt64{Int64: valueOrZero(revision.OrderSourceRevisionID), Valid: revision.OrderSourceRevisionID != nil}), revision.OrderSourceMode, newID); err != nil {
					return refs, err
				}
				if err := insertPlanRevisionMembershipPolicyTx(ctx, tx, newID, rules, filteredExclusions, ts); err != nil {
					return refs, err
				}
				if err := insertPlanRevisionNodesTx(ctx, tx, newID, candidate, ts); err != nil {
					return refs, err
				}
				if _, err := tx.ExecContext(ctx, `update subscription_plans set current_revision_id=?,latest_revision_id=?,active_revision_id=?,lock_version=lock_version+1,revision=revision+1,updated_at=? where id=?`, newID, newID, newID, ts, planID); err != nil {
					return refs, err
				}
				currentChanged = true
			}
		}
		if draftID != 0 {
			result, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_nodes where revision_id=? and node_type=? and node_id in (`+int64Placeholders(len(idsList))+`)`, append([]any{draftID, nodeType}, int64Args(idsList)...)...)
			if err != nil {
				return refs, err
			}
			if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_node_exclusions where revision_id=? and node_type=? and node_id in (`+int64Placeholders(len(idsList))+`)`, append([]any{draftID, nodeType}, int64Args(idsList)...)...); err != nil {
				return refs, err
			}
			removed, err := result.RowsAffected()
			if err != nil {
				return refs, err
			}
			if removed > 0 && !currentChanged {
				if _, err := tx.ExecContext(ctx, `update subscription_plans set lock_version=lock_version+1,revision=revision+1,updated_at=? where id=?`, now(), planID); err != nil {
					return refs, err
				}
			}
		}
	}
	return refs, nil
}

func valueOrZero(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func int64Placeholders(count int) string {
	return strings.TrimSuffix(strings.Repeat("?,", count), ",")
}

func int64Args(values []int64) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// RemovePlanNodeFromDraftRevisions drops one node from every draft revision
// that references it and bumps each affected plan revision so stale previews
// conflict. Active revisions are never modified.
func (s *Store) RemovePlanNodeFromDraftRevisions(ctx context.Context, nodeType model.AssignableNodeType, nodeID int64) error {
	rows, err := s.db.QueryContext(ctx, `select r.plan_id from subscription_plan_revision_nodes pn join subscription_plan_revisions r on r.id=pn.revision_id where pn.node_type=? and pn.node_id=? and r.status=?`, string(nodeType), nodeID, string(model.PlanRevisionDraft))
	if err != nil {
		return err
	}
	var planIDs []int64
	for rows.Next() {
		var planID int64
		if err := rows.Scan(&planID); err != nil {
			return errors.Join(err, rows.Close())
		}
		planIDs = append(planIDs, planID)
	}
	if err := rows.Err(); err != nil {
		return errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(planIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, planID := range planIDs {
		if _, err := tx.ExecContext(ctx, `delete from subscription_plan_revision_nodes where node_type=? and node_id=? and revision_id in (select draft_revision_id from subscription_plans where id=?)`, string(nodeType), nodeID, planID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update subscription_plans set revision=revision+1,updated_at=? where id=?`, now(), planID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
