package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"strconv"
	"time"
)

const ProxyDSCPGlobalSetting = "proxy_dscp_enabled"

func proxyDSCPKey(id int64, kind string) string {
	return fmt.Sprintf("server_proxy_dscp.%d.%s", id, kind)
}
func (s *Store) ProxyDSCPDesired(ctx context.Context, id int64) (model.ProxyDSCPPolicy, error) {
	var value model.ProxyDSCPPolicy
	raw, err := s.GetSetting(ctx, proxyDSCPKey(id, "desired"))
	if err != nil {
		return value, err
	}
	if raw != "" {
		err = json.Unmarshal([]byte(raw), &value)
	}
	return value, err
}
func (s *Store) ProxyDSCPOverride(ctx context.Context, id int64) (bool, error) {
	raw, err := s.GetSetting(ctx, proxyDSCPKey(id, "enabled"))
	return raw == "true", err
}
func (s *Store) ProxyDSCPReport(ctx context.Context, id int64) (*model.ProxyDSCPReport, error) {
	raw, err := s.GetSetting(ctx, proxyDSCPKey(id, "report"))
	if err != nil || raw == "" {
		return nil, err
	}
	var v model.ProxyDSCPReport
	err = json.Unmarshal([]byte(raw), &v)
	return &v, err
}
func proxyDSCPSettingTx(ctx context.Context, tx *sql.Tx, key string) (string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, "select value from app_settings where key=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return raw, err
}
func writeProxyDSCPTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, "insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at where app_settings.value<>excluded.value", key, value, now())
	return err
}
func reconcileProxyDSCPTx(ctx context.Context, tx *sql.Tx, id int64, force bool) (model.ProxyDSCPPolicy, error) {
	var p model.ProxyDSCPPolicy
	raw, err := proxyDSCPSettingTx(ctx, tx, proxyDSCPKey(id, "desired"))
	if err != nil {
		return p, err
	}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return p, err
		}
	}
	global, err := proxyDSCPSettingTx(ctx, tx, ProxyDSCPGlobalSetting)
	if err != nil {
		return p, err
	}
	single, err := proxyDSCPSettingTx(ctx, tx, proxyDSCPKey(id, "enabled"))
	if err != nil {
		return p, err
	}
	enabled := global == "true" || single == "true"
	if enabled != p.Enabled || force {
		p.Enabled = enabled
		p.Revision = max(time.Now().UnixMicro(), p.Revision+1)
		b, _ := json.Marshal(p)
		err = writeProxyDSCPTx(ctx, tx, proxyDSCPKey(id, "desired"), string(b))
	}
	return p, err
}
func setGlobalProxyDSCPTx(ctx context.Context, tx *sql.Tx, value string) error {
	if value != "true" && value != "false" {
		return errors.New("invalid DSCP setting")
	}
	if err := writeProxyDSCPTx(ctx, tx, ProxyDSCPGlobalSetting, value); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "select id from servers")
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = reconcileProxyDSCPTx(ctx, tx, id, false); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) SetServerProxyDSCP(ctx context.Context, server model.Server, enabled *bool, force bool) (model.ProxyDSCPPolicy, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ProxyDSCPPolicy{}, err
	}
	defer tx.Rollback()
	if err = runtimeSecurityServerTx(ctx, tx, server); err != nil {
		return model.ProxyDSCPPolicy{}, err
	}
	if enabled != nil {
		if err = writeProxyDSCPTx(ctx, tx, proxyDSCPKey(server.ID, "enabled"), strconv.FormatBool(*enabled)); err != nil {
			return model.ProxyDSCPPolicy{}, err
		}
	}
	p, err := reconcileProxyDSCPTx(ctx, tx, server.ID, force)
	if err != nil {
		return p, err
	}
	err = tx.Commit()
	if err == nil {
		s.settingsRevision.Add(1)
	}
	return p, err
}
func (s *Store) SaveProxyDSCPReport(ctx context.Context, server model.Server, report model.ProxyDSCPReport) error {
	return s.saveProxyDSCPReport(ctx, server, report, false)
}
func (s *Store) ObserveProxyDSCPReport(ctx context.Context, server model.Server, report model.ProxyDSCPReport) error {
	return s.saveProxyDSCPReport(ctx, server, report, true)
}
func (s *Store) saveProxyDSCPReport(ctx context.Context, server model.Server, report model.ProxyDSCPReport, observed bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = runtimeSecurityServerTx(ctx, tx, server); err != nil {
		return err
	}
	// Only reports for the current desired revision are authoritative.
	raw, err := proxyDSCPSettingTx(ctx, tx, proxyDSCPKey(server.ID, "desired"))
	if err != nil {
		return err
	}
	var desired model.ProxyDSCPPolicy
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &desired); err != nil {
			return err
		}
	}
	if !observed && (report.Revision != desired.Revision || report.Enabled != desired.Enabled) {
		return nil
	}
	b, _ := json.Marshal(report)
	if err = writeProxyDSCPTx(ctx, tx, proxyDSCPKey(server.ID, "report"), string(b)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateProxyDSCPTask(ctx context.Context, server model.Server, task *model.AgentTask) error {
	if task.Type != model.AgentTaskTypeProxyDSCP || task.ServerID != server.ID {
		return errors.New("invalid proxy DSCP task")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := runtimeSecurityServerTx(ctx, tx, server); err != nil {
		return err
	}
	var proposed, desired model.ProxyDSCPPolicy
	if err := json.Unmarshal([]byte(task.PayloadJSON), &proposed); err != nil {
		return err
	}
	raw, err := proxyDSCPSettingTx(ctx, tx, proxyDSCPKey(server.ID, "desired"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(raw), &desired); err != nil {
		return err
	}
	if proposed != desired || proposed.Revision <= 0 {
		return ErrServerRevisionConflict
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

func (s *Store) LatestProxyDSCPAttempt(ctx context.Context, serverID, revision int64) (*model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, "select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where server_id=? and type=? and json_valid(payload_json) and json_extract(payload_json,'$.revision')>=? order by id desc limit 1", serverID, model.AgentTaskTypeProxyDSCP, revision)
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
