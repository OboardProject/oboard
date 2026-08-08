package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

var ErrNodeOrderTemplateConflict = errors.New("node order template revision conflict")

func marshalNodeOrderTemplatePolicy(policy model.NodeOrderTemplatePolicy) (string, error) {
	raw, err := json.Marshal(policy)
	return string(raw), err
}

func (s *Store) ListNodeOrderTemplates(ctx context.Context, includeArchived bool) ([]model.NodeOrderTemplate, error) {
	query := `select t.id,t.name,t.description,t.enabled,t.revision,t.policy_json,t.created_by,t.updated_by,t.created_at,t.updated_at,count(distinct p.id) from node_order_templates t left join subscription_plan_revisions r on r.order_template_id=t.id left join subscription_plans p on p.current_revision_id=r.id`
	if !includeArchived {
		query += ` where t.enabled=1`
	}
	query += ` group by t.id order by t.enabled desc,t.updated_at desc,t.id desc`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNodeOrderTemplates(rows)
}

func (s *Store) GetNodeOrderTemplate(ctx context.Context, id int64) (*model.NodeOrderTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `select t.id,t.name,t.description,t.enabled,t.revision,t.policy_json,t.created_by,t.updated_by,t.created_at,t.updated_at,count(distinct p.id) from node_order_templates t left join subscription_plan_revisions r on r.order_template_id=t.id left join subscription_plans p on p.current_revision_id=r.id where t.id=? group by t.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanNodeOrderTemplates(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func scanNodeOrderTemplates(rows *sql.Rows) ([]model.NodeOrderTemplate, error) {
	out := []model.NodeOrderTemplate{}
	for rows.Next() {
		var item model.NodeOrderTemplate
		var enabled int
		var policyJSON, createdAt, updatedAt string
		var createdBy, updatedBy sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Name, &item.Description, &enabled, &item.Revision, &policyJSON, &createdBy, &updatedBy, &createdAt, &updatedAt, &item.UsageCount); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(policyJSON), &item.Policy); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		if createdBy.Valid {
			value := createdBy.Int64
			item.CreatedBy = &value
		}
		if updatedBy.Valid {
			value := updatedBy.Int64
			item.UpdatedBy = &value
		}
		item.CreatedAt = parseTime(createdAt)
		item.UpdatedAt = parseTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CreateNodeOrderTemplate(ctx context.Context, item *model.NodeOrderTemplate) error {
	policyJSON, err := marshalNodeOrderTemplatePolicy(item.Policy)
	if err != nil {
		return err
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into node_order_templates(name,description,enabled,revision,policy_json,created_by,updated_by,created_at,updated_at) values(?,?,?,1,?,?,?,?,?)`, strings.TrimSpace(item.Name), strings.TrimSpace(item.Description), boolInt(item.Enabled), policyJSON, item.CreatedBy, item.UpdatedBy, ts, ts)
	if err != nil {
		return err
	}
	item.ID, err = res.LastInsertId()
	if err != nil {
		return err
	}
	saved, err := s.GetNodeOrderTemplate(ctx, item.ID)
	if err != nil {
		return err
	}
	*item = *saved
	return nil
}

func (s *Store) UpdateNodeOrderTemplate(ctx context.Context, id, expectedRevision int64, name, description string, policy model.NodeOrderTemplatePolicy, actorID *int64) (*model.NodeOrderTemplate, error) {
	policyJSON, err := marshalNodeOrderTemplatePolicy(policy)
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx, `update node_order_templates set name=?,description=?,policy_json=?,revision=revision+1,updated_by=?,updated_at=? where id=? and revision=?`, strings.TrimSpace(name), strings.TrimSpace(description), policyJSON, actorID, now(), id, expectedRevision)
	if err != nil {
		return nil, err
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		if _, loadErr := s.GetNodeOrderTemplate(ctx, id); errors.Is(loadErr, sql.ErrNoRows) {
			return nil, sql.ErrNoRows
		}
		return nil, ErrNodeOrderTemplateConflict
	}
	return s.GetNodeOrderTemplate(ctx, id)
}

func (s *Store) ArchiveNodeOrderTemplate(ctx context.Context, id, expectedRevision int64, actorID *int64) (*model.NodeOrderTemplate, error) {
	res, err := s.db.ExecContext(ctx, `update node_order_templates set enabled=0,revision=revision+1,updated_by=?,updated_at=? where id=? and revision=?`, actorID, now(), id, expectedRevision)
	if err != nil {
		return nil, err
	}
	if changed, _ := res.RowsAffected(); changed != 1 {
		return nil, ErrNodeOrderTemplateConflict
	}
	return s.GetNodeOrderTemplate(ctx, id)
}
