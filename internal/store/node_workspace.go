package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	defaultNodeGroupName = "OBoard"
	defaultOutputName    = "默认组合"
	maxNodeGroupsPerUser = 50
	maxOutputsPerUser    = 50
	maxImportedNodesUser = 5000
	// maxSubscriptionOutputFiltersBytes bounds the persisted filter pipeline.
	maxSubscriptionOutputFiltersBytes = 8192
)

func marshalSubscriptionOutputFilters(filters []model.SubscriptionOutputFilter) (string, error) {
	raw, err := json.Marshal(filters)
	if err != nil {
		return "", errors.New("invalid subscription output filters")
	}
	if len(raw) > maxSubscriptionOutputFiltersBytes {
		return "", errors.New("subscription output filters are too large")
	}
	return string(raw), nil
}

func parseSubscriptionOutputFilters(raw string) []model.SubscriptionOutputFilter {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var filters []model.SubscriptionOutputFilter
	if err := json.Unmarshal([]byte(raw), &filters); err != nil {
		return nil
	}
	return filters
}

type nodeWorkspaceExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func ensureNodeWorkspaceDefaultsForUser(ctx context.Context, exec nodeWorkspaceExecutor, userID int64, ts string) error {
	if userID <= 0 {
		return errors.New("invalid user")
	}
	if _, err := exec.ExecContext(ctx, `insert into node_groups(user_id,kind,system_key,name,position,created_at,updated_at)
		select ?,?,?,?,?,?,? where not exists(select 1 from node_groups where user_id=? and system_key='oboard')`, userID, model.NodeGroupOBoard, "oboard", defaultNodeGroupName, 0, ts, ts, userID); err != nil {
		return err
	}
	if _, err := exec.ExecContext(ctx, `insert into subscription_outputs(user_id,name,is_default,enabled,created_at,updated_at)
		select ?,?,1,1,?,? where not exists(select 1 from subscription_outputs where user_id=? and is_default=1)`, userID, defaultOutputName, ts, ts, userID); err != nil {
		return err
	}
	_, err := exec.ExecContext(ctx, `insert into subscription_output_groups(output_id,group_id,position)
		select o.id,g.id,0 from subscription_outputs o join node_groups g on g.user_id=o.user_id and g.system_key='oboard'
		where o.user_id=? and o.is_default=1 and not exists(select 1 from subscription_output_groups x where x.output_id=o.id)`, userID)
	return err
}

func (s *Store) ensureNodeWorkspaceDefaults(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `select id from users order by id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	ts := now()
	for _, id := range ids {
		if err := ensureNodeWorkspaceDefaultsForUser(ctx, s.db, id, ts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) EnsureNodeWorkspaceDefaultsForUser(ctx context.Context, userID int64) error {
	return ensureNodeWorkspaceDefaultsForUser(ctx, s.db, userID, now())
}

func (s *Store) ListNodeGroups(ctx context.Context, userID int64) ([]model.NodeGroup, error) {
	rows, err := s.db.QueryContext(ctx, `select g.id,g.user_id,g.kind,g.system_key,g.name,g.position,
		(select count(*) from imported_nodes n where n.group_id=g.id and n.enabled=1),g.created_at,g.updated_at
		from node_groups g where g.user_id=? order by g.position,g.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NodeGroup{}
	for rows.Next() {
		var v model.NodeGroup
		var created, updated string
		if err := rows.Scan(&v.ID, &v.UserID, &v.Kind, &v.SystemKey, &v.Name, &v.Position, &v.NodeCount, &created, &updated); err != nil {
			return nil, err
		}
		v.CreatedAt, v.UpdatedAt = parseTime(created), parseTime(updated)
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) GetNodeGroup(ctx context.Context, userID, id int64) (*model.NodeGroup, error) {
	var v model.NodeGroup
	var created, updated string
	err := s.db.QueryRowContext(ctx, `select g.id,g.user_id,g.kind,g.system_key,g.name,g.position,
		(select count(*) from imported_nodes n where n.group_id=g.id and n.enabled=1),g.created_at,g.updated_at from node_groups g where g.user_id=? and g.id=?`, userID, id).
		Scan(&v.ID, &v.UserID, &v.Kind, &v.SystemKey, &v.Name, &v.Position, &v.NodeCount, &created, &updated)
	if err != nil {
		return nil, err
	}
	v.CreatedAt, v.UpdatedAt = parseTime(created), parseTime(updated)
	return &v, nil
}

func (s *Store) CreateNodeGroup(ctx context.Context, group *model.NodeGroup) error {
	if group == nil || group.UserID <= 0 || strings.TrimSpace(group.Name) == "" || len([]rune(strings.TrimSpace(group.Name))) > 80 {
		return errors.New("invalid node group")
	}
	if group.Kind != model.NodeGroupManual && group.Kind != model.NodeGroupRemote {
		return errors.New("invalid node group kind")
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into node_groups(user_id,kind,system_key,name,position,created_at,updated_at)
		select ?,?,'',?,coalesce(max(position),0)+1,?,? from node_groups where user_id=? having count(*)<?`, group.UserID, group.Kind, strings.TrimSpace(group.Name), ts, ts, group.UserID, maxNodeGroupsPerUser)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("node group limit reached")
	}
	group.ID, _ = res.LastInsertId()
	group.Name = strings.TrimSpace(group.Name)
	group.CreatedAt = parseTime(ts)
	group.UpdatedAt = group.CreatedAt
	return nil
}

func (s *Store) RenameNodeGroup(ctx context.Context, userID, id int64, name string) (*model.NodeGroup, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 80 {
		return nil, errors.New("node group name must be between 1 and 80 characters")
	}
	res, err := s.db.ExecContext(ctx, `update node_groups set name=?,updated_at=? where user_id=? and id=?`, name, now(), userID, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, sql.ErrNoRows
	}
	return s.GetNodeGroup(ctx, userID, id)
}

func (s *Store) DeleteNodeGroup(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `delete from node_groups where user_id=? and id=? and system_key=''`, userID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("system node group cannot be deleted")
	}
	return nil
}

func (s *Store) CreateNodeSource(ctx context.Context, source *model.NodeSource) error {
	if source == nil || source.UserID <= 0 || source.GroupID <= 0 || source.URLFingerprint == "" || source.URLEncrypted == "" {
		return errors.New("invalid node source")
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into node_sources(user_id,group_id,url_fingerprint,url_encrypted,status,created_at,updated_at)
		select ?,?,?,?,?,?,? where exists(select 1 from node_groups where id=? and user_id=? and kind='remote')`, source.UserID, source.GroupID, source.URLFingerprint, source.URLEncrypted, "pending", ts, ts, source.GroupID, source.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("invalid remote node group")
	}
	source.ID, _ = res.LastInsertId()
	source.Status = "pending"
	source.CreatedAt = parseTime(ts)
	source.UpdatedAt = source.CreatedAt
	return nil
}

func (s *Store) ListNodeSources(ctx context.Context, userID int64) ([]model.NodeSource, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,group_id,url_fingerprint,url_encrypted,etag,last_modified,status,last_error,last_attempt_at,last_success_at,created_at,updated_at from node_sources where user_id=? order by id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NodeSource{}
	for rows.Next() {
		v, err := scanNodeSource(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *v)
	}
	return items, rows.Err()
}

func (s *Store) ListNodeSourcesDueForRefresh(ctx context.Context, before time.Time, limit int) ([]model.NodeSource, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select id,user_id,group_id,url_fingerprint,url_encrypted,etag,last_modified,status,last_error,last_attempt_at,last_success_at,created_at,updated_at
		from node_sources where last_attempt_at is null or last_attempt_at<=? order by coalesce(last_attempt_at,'') limit ?`, before.UTC().Format(timeLayout), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NodeSource{}
	for rows.Next() {
		item, scanErr := scanNodeSource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *Store) GetNodeSource(ctx context.Context, userID, id int64) (*model.NodeSource, error) {
	return scanNodeSource(s.db.QueryRowContext(ctx, `select id,user_id,group_id,url_fingerprint,url_encrypted,etag,last_modified,status,last_error,last_attempt_at,last_success_at,created_at,updated_at from node_sources where user_id=? and id=?`, userID, id))
}

type nodeSourceScanner interface{ Scan(...any) error }

func scanNodeSource(row nodeSourceScanner) (*model.NodeSource, error) {
	var v model.NodeSource
	var attempt, success sql.NullString
	var created, updated string
	if err := row.Scan(&v.ID, &v.UserID, &v.GroupID, &v.URLFingerprint, &v.URLEncrypted, &v.ETag, &v.LastModified, &v.Status, &v.LastError, &attempt, &success, &created, &updated); err != nil {
		return nil, err
	}
	v.LastAttemptAt, v.LastSuccessAt = parseNullTime(attempt), parseNullTime(success)
	v.CreatedAt, v.UpdatedAt = parseTime(created), parseTime(updated)
	return &v, nil
}

func (s *Store) ReplaceSourceNodes(ctx context.Context, source model.NodeSource, nodes []model.ImportedNode, etag, lastModified string, at time.Time) error {
	if len(nodes) == 0 {
		return errors.New("source refresh has no valid nodes")
	}
	unique := nodes[:0]
	seen := map[string]bool{}
	for _, node := range nodes {
		if node.Fingerprint == "" || seen[node.Fingerprint] {
			continue
		}
		seen[node.Fingerprint] = true
		unique = append(unique, node)
	}
	if len(unique) == 0 {
		return errors.New("source refresh has no valid nodes")
	}
	nodes = unique
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var total int
	if err := tx.QueryRowContext(ctx, `select count(*) from imported_nodes where user_id=? and (source_id is null or source_id<>?)`, source.UserID, source.ID).Scan(&total); err != nil {
		return err
	}
	if total+len(nodes) > maxImportedNodesUser {
		return errors.New("imported node limit reached")
	}
	if _, err := tx.ExecContext(ctx, `delete from imported_nodes where source_id=? and user_id=?`, source.ID, source.UserID); err != nil {
		return err
	}
	ts := at.UTC().Format(timeLayout)
	for i := range nodes {
		n := &nodes[i]
		res, err := tx.ExecContext(ctx, `insert into imported_nodes(user_id,group_id,source_id,protocol,name,fingerprint,config_encrypted,position,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,1,?,?)`, source.UserID, source.GroupID, source.ID, n.Protocol, n.Name, n.Fingerprint, n.ConfigEncrypted, i, ts, ts)
		if err != nil {
			return err
		}
		n.ID, _ = res.LastInsertId()
	}
	if _, err := tx.ExecContext(ctx, `update node_sources set etag=?,last_modified=?,status='ready',last_error='',last_attempt_at=?,last_success_at=?,updated_at=? where id=? and user_id=?`, etag, lastModified, ts, ts, ts, source.ID, source.UserID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkNodeSourceFailed(ctx context.Context, userID, sourceID int64, message string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `update node_sources set status='error',last_error=?,last_attempt_at=?,updated_at=? where id=? and user_id=?`, message, at.UTC().Format(timeLayout), now(), sourceID, userID)
	return err
}

func (s *Store) MarkNodeSourceNotModified(ctx context.Context, userID, sourceID int64, at time.Time) error {
	ts := at.UTC().Format(timeLayout)
	_, err := s.db.ExecContext(ctx, `update node_sources set status='ready',last_error='',last_attempt_at=?,updated_at=? where id=? and user_id=?`, ts, ts, sourceID, userID)
	return err
}

func (s *Store) ListImportedNodes(ctx context.Context, userID int64) ([]model.ImportedNode, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,group_id,source_id,protocol,name,fingerprint,config_encrypted,position,enabled,created_at,updated_at from imported_nodes where user_id=? order by group_id,position,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.ImportedNode{}
	for rows.Next() {
		var v model.ImportedNode
		var source sql.NullInt64
		var enabled int
		var created, updated string
		if err := rows.Scan(&v.ID, &v.UserID, &v.GroupID, &source, &v.Protocol, &v.Name, &v.Fingerprint, &v.ConfigEncrypted, &v.Position, &enabled, &created, &updated); err != nil {
			return nil, err
		}
		if source.Valid {
			v.SourceID = &source.Int64
		}
		v.Enabled = enabled != 0
		v.CreatedAt, v.UpdatedAt = parseTime(created), parseTime(updated)
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) GetImportedNode(ctx context.Context, userID, id int64) (*model.ImportedNode, error) {
	var v model.ImportedNode
	var source sql.NullInt64
	var enabled int
	var created, updated string
	err := s.db.QueryRowContext(ctx, `select id,user_id,group_id,source_id,protocol,name,fingerprint,config_encrypted,position,enabled,created_at,updated_at from imported_nodes where user_id=? and id=?`, userID, id).Scan(&v.ID, &v.UserID, &v.GroupID, &source, &v.Protocol, &v.Name, &v.Fingerprint, &v.ConfigEncrypted, &v.Position, &enabled, &created, &updated)
	if err != nil {
		return nil, err
	}
	if source.Valid {
		v.SourceID = &source.Int64
	}
	v.Enabled = enabled != 0
	v.CreatedAt, v.UpdatedAt = parseTime(created), parseTime(updated)
	return &v, nil
}

func (s *Store) AddManualImportedNodes(ctx context.Context, userID, groupID int64, nodes []model.ImportedNode) error {
	if len(nodes) == 0 {
		return errors.New("no valid nodes")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `select count(*) from imported_nodes where user_id=?`, userID).Scan(&count); err != nil {
		return err
	}
	if count+len(nodes) > maxImportedNodesUser {
		return errors.New("imported node limit reached")
	}
	var kind string
	if err := tx.QueryRowContext(ctx, `select kind from node_groups where id=? and user_id=?`, groupID, userID).Scan(&kind); err != nil {
		return err
	}
	if kind != string(model.NodeGroupManual) {
		return errors.New("manual nodes require a manual group")
	}
	ts := now()
	for i := range nodes {
		n := &nodes[i]
		res, err := tx.ExecContext(ctx, `insert into imported_nodes(user_id,group_id,source_id,protocol,name,fingerprint,config_encrypted,position,enabled,created_at,updated_at) values(?,?,null,?,?,?,?,coalesce((select max(position)+1 from imported_nodes where group_id=?),0),1,?,?)`, userID, groupID, n.Protocol, n.Name, n.Fingerprint, n.ConfigEncrypted, groupID, ts, ts)
		if err != nil {
			return err
		}
		n.ID, _ = res.LastInsertId()
	}
	return tx.Commit()
}

func (s *Store) UpdateImportedNode(ctx context.Context, userID, id int64, value *model.ImportedNode) (*model.ImportedNode, error) {
	if value == nil || value.Protocol == "" || strings.TrimSpace(value.Name) == "" || value.Fingerprint == "" || value.ConfigEncrypted == "" {
		return nil, errors.New("invalid imported node")
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `update imported_nodes set protocol=?,name=?,fingerprint=?,config_encrypted=?,enabled=?,updated_at=? where id=? and user_id=? and source_id is null`, value.Protocol, strings.TrimSpace(value.Name), value.Fingerprint, value.ConfigEncrypted, boolInt(value.Enabled), ts, id, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, sql.ErrNoRows
	}
	return s.GetImportedNode(ctx, userID, id)
}

func (s *Store) ListSubscriptionOutputs(ctx context.Context, userID int64) ([]model.SubscriptionOutput, error) {
	rows, err := s.db.QueryContext(ctx, `select id,user_id,name,is_default,enabled,filters_json,created_at,updated_at from subscription_outputs where user_id=? order by is_default desc,id`, userID)
	if err != nil {
		return nil, err
	}
	items := []model.SubscriptionOutput{}
	for rows.Next() {
		var v model.SubscriptionOutput
		var def, en int
		var filters string
		var c, u string
		if err := rows.Scan(&v.ID, &v.UserID, &v.Name, &def, &en, &filters, &c, &u); err != nil {
			return nil, err
		}
		v.IsDefault, v.Enabled = def != 0, en != 0
		v.CreatedAt, v.UpdatedAt = parseTime(c), parseTime(u)
		v.Filters = parseSubscriptionOutputFilters(filters)
		items = append(items, v)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range items {
		groups, err := s.outputGroupIDs(ctx, items[i].ID)
		if err != nil {
			return nil, err
		}
		items[i].GroupIDs = groups
	}
	return items, nil
}
func (s *Store) outputGroupIDs(ctx context.Context, outputID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select group_id from subscription_output_groups where output_id=? order by position`, outputID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (s *Store) GetSubscriptionOutput(ctx context.Context, userID, id int64) (*model.SubscriptionOutput, error) {
	items, err := s.ListSubscriptionOutputs(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].ID == id {
			return &items[i], nil
		}
	}
	return nil, sql.ErrNoRows
}
func (s *Store) GetDefaultSubscriptionOutput(ctx context.Context, userID int64) (*model.SubscriptionOutput, error) {
	items, err := s.ListSubscriptionOutputs(ctx, userID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].IsDefault {
			return &items[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *Store) SaveSubscriptionOutput(ctx context.Context, value *model.SubscriptionOutput) error {
	if value == nil || value.UserID <= 0 || strings.TrimSpace(value.Name) == "" || len([]rune(strings.TrimSpace(value.Name))) > 80 || len(value.GroupIDs) == 0 || len(value.GroupIDs) > maxNodeGroupsPerUser {
		return errors.New("invalid subscription output")
	}
	filtersJSON, err := marshalSubscriptionOutputFilters(value.Filters)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	created := value.ID == 0
	if created {
		res, err := tx.ExecContext(ctx, `insert into subscription_outputs(user_id,name,is_default,enabled,filters_json,created_at,updated_at) select ?,?,0,1,?,?,? where (select count(*) from subscription_outputs where user_id=?)<?`, value.UserID, strings.TrimSpace(value.Name), filtersJSON, ts, ts, value.UserID, maxOutputsPerUser)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("subscription output limit reached")
		}
		value.ID, _ = res.LastInsertId()
	} else {
		res, err := tx.ExecContext(ctx, `update subscription_outputs set name=?,enabled=?,filters_json=?,updated_at=? where id=? and user_id=?`, strings.TrimSpace(value.Name), boolInt(value.Enabled), filtersJSON, ts, value.ID, value.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return sql.ErrNoRows
		}
		if _, err := tx.ExecContext(ctx, `delete from subscription_output_groups where output_id=?`, value.ID); err != nil {
			return err
		}
	}
	seen := map[int64]bool{}
	for pos, id := range value.GroupIDs {
		if seen[id] {
			return errors.New("duplicate node group")
		}
		seen[id] = true
		res, err := tx.ExecContext(ctx, `insert into subscription_output_groups(output_id,group_id,position) select ?,id,? from node_groups where id=? and user_id=?`, value.ID, pos, id, value.UserID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return errors.New("node group does not belong to user")
		}
	}
	value.Name = strings.TrimSpace(value.Name)
	if created {
		value.Enabled = true
	}
	return tx.Commit()
}

func (s *Store) DeleteSubscriptionOutput(ctx context.Context, userID, id int64) error {
	res, err := s.db.ExecContext(ctx, `delete from subscription_outputs where id=? and user_id=? and is_default=0`, id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("default subscription output cannot be deleted")
	}
	return nil
}
