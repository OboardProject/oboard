package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/OboardProject/oboard/internal/model"
)

var ErrNodeMetadataConflict = errors.New("node metadata revision conflict")

func (s *Store) ListNodeMetadata(ctx context.Context) (map[string]model.AssignableNodeMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `select node_type,node_id,display_name_override,lock_version,created_by,updated_by,created_at,updated_at from assignable_node_metadata`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.AssignableNodeMetadata{}
	for rows.Next() {
		item, err := scanNodeMetadata(rows)
		if err != nil {
			return nil, err
		}
		out[planRevisionNodeKey(item.NodeType, item.NodeID)] = item
	}
	return out, rows.Err()
}

func (s *Store) GetNodeMetadata(ctx context.Context, nodeType model.AssignableNodeType, nodeID int64) (*model.AssignableNodeMetadata, error) {
	row := s.db.QueryRowContext(ctx, `select node_type,node_id,display_name_override,lock_version,created_by,updated_by,created_at,updated_at from assignable_node_metadata where node_type=? and node_id=?`, nodeType, nodeID)
	item, err := scanNodeMetadata(row)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNodeMetadata(row rowScanner) (model.AssignableNodeMetadata, error) {
	var item model.AssignableNodeMetadata
	var override sql.NullString
	var createdBy, updatedBy sql.NullInt64
	var createdAt, updatedAt string
	if err := row.Scan(&item.NodeType, &item.NodeID, &override, &item.LockVersion, &createdBy, &updatedBy, &createdAt, &updatedAt); err != nil {
		return item, err
	}
	if override.Valid {
		value := override.String
		item.DisplayNameOverride = &value
	}
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
	return item, nil
}

func (s *Store) UpsertNodeMetadata(ctx context.Context, nodeType model.AssignableNodeType, nodeID int64, displayNameOverride *string, expectedLockVersion int64, actorID *int64) (*model.AssignableNodeMetadata, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, `select lock_version from assignable_node_metadata where node_type=? and node_id=?`, nodeType, nodeID).Scan(&current)
	ts := now()
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if expectedLockVersion != 0 {
			return nil, ErrNodeMetadataConflict
		}
		if _, err := tx.ExecContext(ctx, `insert into assignable_node_metadata(node_type,node_id,display_name_override,lock_version,created_by,updated_by,created_at,updated_at) values(?,?,?,1,?,?,?,?)`, nodeType, nodeID, displayNameOverride, actorID, actorID, ts, ts); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	default:
		if expectedLockVersion != current {
			return nil, ErrNodeMetadataConflict
		}
		res, err := tx.ExecContext(ctx, `update assignable_node_metadata set display_name_override=?,lock_version=lock_version+1,updated_by=?,updated_at=? where node_type=? and node_id=? and lock_version=?`, displayNameOverride, actorID, ts, nodeType, nodeID, current)
		if err != nil {
			return nil, err
		}
		if changed, _ := res.RowsAffected(); changed != 1 {
			return nil, ErrNodeMetadataConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetNodeMetadata(ctx, nodeType, nodeID)
}

func (s *Store) CountCurrentPlanNameStates(ctx context.Context, nodeType model.AssignableNodeType, nodeID int64) (inherited, overridden int, err error) {
	err = s.db.QueryRowContext(ctx, `select coalesce(sum(case when n.display_name_override is null then 1 else 0 end),0),coalesce(sum(case when n.display_name_override is not null then 1 else 0 end),0) from subscription_plan_revision_nodes n join subscription_plans p on p.current_revision_id=n.revision_id where n.node_type=? and n.node_id=?`, nodeType, nodeID).Scan(&inherited, &overridden)
	return
}
