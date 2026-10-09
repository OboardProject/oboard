package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"time"
)

func runtimeSecurityKey(id int64, kind string) string {
	return fmt.Sprintf("server_runtime_security.%d.%s", id, kind)
}
func (s *Store) RuntimeSecurityDesired(ctx context.Context, id int64) (model.RuntimeSecurityRequest, error) {
	value := model.RuntimeSecurityRequest{Mode: "standard"}
	raw, err := s.GetSetting(ctx, runtimeSecurityKey(id, "desired"))
	if err != nil {
		return value, err
	}
	if raw != "" {
		err = json.Unmarshal([]byte(raw), &value)
	}
	return value, err
}
func (s *Store) SaveRuntimeSecurityDesired(ctx context.Context, server model.Server, value model.RuntimeSecurityRequest) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.saveRuntimeSecurityValue(ctx, server, "desired", raw)
}
func (s *Store) RuntimeSecurityReport(ctx context.Context, id int64) (*model.RuntimeSecurityReport, error) {
	raw, err := s.GetSetting(ctx, runtimeSecurityKey(id, "report"))
	if err != nil || raw == "" {
		return nil, err
	}
	var value model.RuntimeSecurityReport
	err = json.Unmarshal([]byte(raw), &value)
	return &value, err
}
func (s *Store) SaveRuntimeSecurityReport(ctx context.Context, server model.Server, value model.RuntimeSecurityReport) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.saveRuntimeSecurityValue(ctx, server, "report", raw)
}

func runtimeSecurityServerTx(ctx context.Context, tx *sql.Tx, server model.Server) error {
	var created, agent string
	if err := tx.QueryRowContext(ctx, "select created_at,agent_id from servers where id=?", server.ID).Scan(&created, &agent); err != nil {
		return err
	}
	if server.CreatedAt.IsZero() || !parseTime(created).Equal(server.CreatedAt) || agent != server.AgentID {
		return ErrServerRevisionConflict
	}
	deleting, err := serverDeletionClaimedTx(ctx, tx, server.ID)
	if err != nil {
		return err
	}
	if deleting {
		return ErrServerDeleting
	}
	return nil
}
func (s *Store) saveRuntimeSecurityValue(ctx context.Context, server model.Server, kind string, raw []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := runtimeSecurityServerTx(ctx, tx, server); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at where app_settings.value<>excluded.value", runtimeSecurityKey(server.ID, kind), string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) CreateRuntimeSecurityTask(ctx context.Context, server model.Server, task *model.AgentTask) error {
	if task.Type != model.AgentTaskTypeRuntimeSecurity || task.ServerID != server.ID {
		return errors.New("invalid runtime security task")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := runtimeSecurityServerTx(ctx, tx, server); err != nil {
		return err
	}
	ts := now()
	id, err := operationTaskInsert(ctx, tx, *task, ts)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	task.ID = id
	task.CreatedAt = parseTime(ts)
	task.UpdatedAt = task.CreatedAt
	return nil
}

func (s *Store) LatestRuntimeSecurityAttempt(ctx context.Context, serverID, revision int64) (*model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, "select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where server_id=? and type=? and json_valid(payload_json) and json_extract(payload_json,'$.revision')>=? order by id desc limit 1", serverID, model.AgentTaskTypeRuntimeSecurity, revision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}
