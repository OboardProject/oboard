package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const routingRuleSetSelectSQL = `select id,name,url,format,mihomo_behavior,etag,last_modified,content,revision,status,last_error,last_attempt_at,last_success_at,created_at,updated_at from routing_rule_sets`

func (s *Store) CreateRoutingRuleSet(ctx context.Context, value *model.RoutingRuleSet) error {
	ts := now()
	value.CreatedAt = parseTime(ts)
	value.UpdatedAt = value.CreatedAt
	result, err := s.db.ExecContext(ctx, `insert into routing_rule_sets(name,url,format,mihomo_behavior,etag,last_modified,content,revision,status,last_error,last_attempt_at,last_success_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, value.Name, value.URL, value.Format, value.MihomoBehavior, value.ETag, value.LastModified, value.Content, value.Revision, value.Status, value.LastError, timePtrString(value.LastAttemptAt), timePtrString(value.LastSuccessAt), ts, ts)
	if err != nil {
		return err
	}
	value.ID, _ = result.LastInsertId()
	return nil
}

func (s *Store) UpdateRoutingRuleSet(ctx context.Context, value *model.RoutingRuleSet) error {
	value.UpdatedAt = parseTime(now())
	result, err := s.db.ExecContext(ctx, `update routing_rule_sets set name=?,url=?,format=?,mihomo_behavior=?,etag=?,last_modified=?,content=?,revision=?,status=?,last_error=?,last_attempt_at=?,last_success_at=?,updated_at=? where id=?`, value.Name, value.URL, value.Format, value.MihomoBehavior, value.ETag, value.LastModified, value.Content, value.Revision, value.Status, value.LastError, timePtrString(value.LastAttemptAt), timePtrString(value.LastSuccessAt), value.UpdatedAt.UTC().Format(timeLayout), value.ID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return err
}

const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

func (s *Store) ListRoutingRuleSets(ctx context.Context) ([]model.RoutingRuleSet, error) {
	rows, err := s.db.QueryContext(ctx, routingRuleSetSelectSQL+` order by name collate nocase,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.RoutingRuleSet, 0)
	for rows.Next() {
		value, err := scanRoutingRuleSet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) GetRoutingRuleSet(ctx context.Context, id int64) (*model.RoutingRuleSet, error) {
	row := s.db.QueryRowContext(ctx, routingRuleSetSelectSQL+` where id=?`, id)
	value, err := scanRoutingRuleSet(row)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

type routingRuleSetScanner interface{ Scan(...any) error }

func scanRoutingRuleSet(scanner routingRuleSetScanner) (model.RoutingRuleSet, error) {
	var value model.RoutingRuleSet
	var attempt, success sql.NullString
	var created, updated string
	err := scanner.Scan(&value.ID, &value.Name, &value.URL, &value.Format, &value.MihomoBehavior, &value.ETag, &value.LastModified, &value.Content, &value.Revision, &value.Status, &value.LastError, &attempt, &success, &created, &updated)
	if err != nil {
		return value, err
	}
	if attempt.Valid {
		parsed := parseTime(attempt.String)
		value.LastAttemptAt = &parsed
	}
	if success.Valid {
		parsed := parseTime(success.String)
		value.LastSuccessAt = &parsed
	}
	value.CreatedAt = parseTime(created)
	value.UpdatedAt = parseTime(updated)
	return value, nil
}

func (s *Store) DeleteRoutingRuleSet(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `delete from routing_rule_sets where id=?`, id)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err == nil && rows == 0 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) ListServerIDsReferencingRoutingRuleSet(ctx context.Context, id int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select distinct server_id from routing_rules where enabled=1 and match_source=? and rule_set_id=? order by server_id`, model.RoutingMatchSourceRuleSet, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	serverIDs := make([]int64, 0)
	for rows.Next() {
		var serverID int64
		if err := rows.Scan(&serverID); err != nil {
			return nil, err
		}
		serverIDs = append(serverIDs, serverID)
	}
	return serverIDs, rows.Err()
}

func (s *Store) PlaceRoutingRules(ctx context.Context, pathID int64, placements []model.RoutingRulePlacement) error {
	if pathID <= 0 {
		return errors.New("proxy_path_id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var rootServerID int64
	if err := tx.QueryRowContext(ctx, `select i.server_id from proxy_paths p join inbounds i on i.id=p.inbound_id where p.id=?`, pathID).Scan(&rootServerID); err != nil {
		return err
	}
	stageServers := map[int64]int64{}
	rows, err := tx.QueryContext(ctx, `select id,server_id from proxy_path_steps where path_id=? and node_type='server_inbound' and server_id is not null`, pathID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var stepID, serverID int64
		if err := rows.Scan(&stepID, &serverID); err != nil {
			rows.Close()
			return err
		}
		stageServers[stepID] = serverID
	}
	if err := rows.Close(); err != nil {
		return err
	}

	existingRows, err := tx.QueryContext(ctx, `select id from routing_rules where scope='path_stage' and proxy_path_id=?`, pathID)
	if err != nil {
		return err
	}
	existing := map[int64]bool{}
	for existingRows.Next() {
		var id int64
		if err := existingRows.Scan(&id); err != nil {
			existingRows.Close()
			return err
		}
		existing[id] = true
	}
	if err := existingRows.Close(); err != nil {
		return err
	}
	if len(existing) != len(placements) {
		return errors.New("placements must include every rule in the proxy path")
	}

	seen := map[int64]bool{}
	positions := map[string][]int{}
	for _, placement := range placements {
		if !existing[placement.RuleID] || seen[placement.RuleID] {
			return fmt.Errorf("invalid or duplicate routing rule %d", placement.RuleID)
		}
		seen[placement.RuleID] = true
		serverID := rootServerID
		stageKey := "root"
		if placement.StageStepID != nil {
			var ok bool
			serverID, ok = stageServers[*placement.StageStepID]
			if !ok {
				return fmt.Errorf("proxy path step %d is not a controlled routing stage", *placement.StageStepID)
			}
			stageKey = fmt.Sprintf("%d", *placement.StageStepID)
		}
		if placement.SortPosition < 0 {
			return errors.New("sort_position cannot be negative")
		}
		positions[stageKey] = append(positions[stageKey], placement.SortPosition)
		if _, err := tx.ExecContext(ctx, `update routing_rules set server_id=?,stage_step_id=?,sort_position=?,updated_at=? where id=? and proxy_path_id=? and scope='path_stage'`, serverID, placement.StageStepID, placement.SortPosition, now(), placement.RuleID, pathID); err != nil {
			return err
		}
	}
	for stage, values := range positions {
		sort.Ints(values)
		for index, position := range values {
			if position != index {
				return fmt.Errorf("stage %s positions must be contiguous from zero", strings.TrimSpace(stage))
			}
		}
	}
	return tx.Commit()
}
