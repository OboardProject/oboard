package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginhttp"
	"net/netip"
	"strings"
	"time"
)

const probeMonitorColumns = "id,server_id,name,kind,target,mode,port,interval_seconds,enabled,revision,checked_at,status,error,result"

func (s *Store) ListProbeMonitors(ctx context.Context) ([]model.ProbeMonitor, error) {
	rows, err := s.db.QueryContext(ctx, "select "+probeMonitorColumns+" from probe_monitors order by id desc limit 1000")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProbeMonitor{}
	for rows.Next() {
		var v model.ProbeMonitor
		var raw string
		if err := rows.Scan(&v.ID, &v.ServerID, &v.Name, &v.Kind, &v.Target, &v.Mode, &v.Port, &v.IntervalSeconds, &v.Enabled, &v.Revision, &v.CheckedAt, &v.Status, &v.Error, &raw); err != nil {
			return nil, err
		}
		v.Result = json.RawMessage(raw)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) SaveProbeMonitor(ctx context.Context, server model.Server, v *model.ProbeMonitor) error {
	if err := ValidateProbeMonitor(v); err != nil {
		return err
	}
	if v.ServerID != server.ID {
		return errors.New("服务器身份不匹配")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = runtimeSecurityServerTx(ctx, tx, server); err != nil {
		return err
	}
	if v.ID == 0 {
		var count int
		if err = tx.QueryRowContext(ctx, "select count(*) from probe_monitors").Scan(&count); err != nil {
			return err
		}
		if count >= 1000 {
			return errors.New("最多支持 1000 个监控任务")
		}
		result, e := tx.ExecContext(ctx, "insert into probe_monitors(server_id,name,kind,target,mode,port,interval_seconds,enabled,revision) values(?,?,?,?,?,?,?,?,1)", v.ServerID, v.Name, v.Kind, v.Target, v.Mode, v.Port, v.IntervalSeconds, v.Enabled)
		if e != nil {
			return e
		}
		v.ID, err = result.LastInsertId()
		v.Revision = 1
	} else {
		result, e := tx.ExecContext(ctx, "update probe_monitors set name=?,interval_seconds=?,enabled=?,revision=revision+1,next_at=0 where id=? and server_id=? and kind=? and target=? and mode=? and port=? and revision=?", v.Name, v.IntervalSeconds, v.Enabled, v.ID, v.ServerID, v.Kind, v.Target, v.Mode, v.Port, v.Revision)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return errors.New("任务已改变；请刷新。更换目标、协议或服务器请新建任务")
		}
		v.Revision++
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MutateProbeMonitor(ctx context.Context, id, revision int64, action string) error {
	query := "delete from probe_monitors where id=? and revision=?"
	if action == "run" {
		query = "update probe_monitors set next_at=0 where id=? and revision=? and enabled=1"
	}
	result, err := s.db.ExecContext(ctx, query, id, revision)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("任务已改变或已停用，请刷新")
	}
	return nil
}

func (s *Store) ClaimProbeMonitor(ctx context.Context, v model.ProbeMonitor, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, "update probe_monitors set next_at=?,status='running',error='' where id=? and revision=? and enabled=1 and next_at<=?", now.Add(time.Duration(max(v.IntervalSeconds, 120))*time.Second).Unix(), v.ID, v.Revision, now.Unix())
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

func (s *Store) FinishProbeMonitor(ctx context.Context, v model.ProbeMonitor, status, message string, result any, route *model.ProbeRouteVersion) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if len(raw) > 128*1024 {
		return errors.New("监控结果过大")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := time.Now().UTC().Format(time.RFC3339Nano)
	changed, err := tx.ExecContext(ctx, "update probe_monitors set status=?,error=?,result=?,checked_at=? where id=? and revision=?", status, message, string(raw), at, v.ID, v.Revision)
	if err != nil {
		return err
	}
	n, _ := changed.RowsAffected()
	if n != 1 {
		return nil
	}
	if route != nil {
		path, _ := json.Marshal(route.ASNPath)
		trace, _ := json.Marshal(route.Trace)
		var previous string
		var id int64
		err = tx.QueryRowContext(ctx, "select id,asn_path from probe_route_versions where monitor_id=? order by id desc limit 1", v.ID).Scan(&id, &previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if previous == string(path) {
			_, err = tx.ExecContext(ctx, "update probe_route_versions set last_seen_at=? where id=?", at, id)
		} else {
			_, err = tx.ExecContext(ctx, "insert into probe_route_versions(monitor_id,checked_at,last_seen_at,asn_path,trace) values(?,?,?,?,?)", v.ID, at, at, string(path), string(trace))
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ProbeRouteHistory(ctx context.Context, id, before int64) ([]model.ProbeRouteVersion, error) {
	if before <= 0 {
		before = 1<<63 - 1
	}
	rows, err := s.db.QueryContext(ctx, "select id,checked_at,last_seen_at,asn_path,trace from probe_route_versions where monitor_id=? and id<? order by id desc limit 100", id, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProbeRouteVersion{}
	for rows.Next() {
		var v model.ProbeRouteVersion
		var path, trace string
		if err = rows.Scan(&v.ID, &v.CheckedAt, &v.LastSeenAt, &path, &trace); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(path), &v.ASNPath); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(trace), &v.Trace); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func ValidateProbeMonitor(v *model.ProbeMonitor) error {
	v.Name = strings.TrimSpace(v.Name)
	v.Target = strings.TrimSpace(v.Target)
	if v.ServerID <= 0 || v.Name == "" || len([]rune(v.Name)) > 60 {
		return errors.New("请选择服务器并填写不超过 60 字的名称")
	}
	if v.Kind != "route" && v.Kind != "ip_quality" {
		return errors.New("监控类型无效")
	}
	if ip, err := netip.ParseAddr(v.Target); err == nil {
		if !pluginhttp.PublicIP(ip) {
			return errors.New("目标必须为公网 IP")
		}
		v.Target = ip.Unmap().String()
	} else if v.Kind == "route" {
		if err := ValidateNetworkProbeAddress(v.Target, model.LatencyProbeModeICMP); err != nil {
			return err
		}
	} else if v.Target != "" {
		return errors.New("IP 质量监控只接受公网 IP")
	}
	minimum := 300
	if v.Kind == "ip_quality" {
		minimum = 3600
	}
	if v.IntervalSeconds < minimum || v.IntervalSeconds > 604800 {
		return errors.New("路由监控间隔为 5 分钟至 7 天，IP 质量间隔为 1 小时至 7 天")
	}
	if v.Kind == "route" {
		if v.Mode != "icmp" && v.Mode != "tcp" {
			return errors.New("路由协议必须为 ICMP 或 TCP")
		}
		if v.Mode == "tcp" && (v.Port < 1 || v.Port > 65535) {
			return errors.New("TCP 端口无效")
		}
		if v.Mode == "icmp" {
			v.Port = 0
		}
	} else {
		v.Mode = ""
		v.Port = 0
	}
	return nil
}

func (s *Store) CreateProbeMonitorTask(ctx context.Context, server model.Server, v model.ProbeMonitor, task *model.AgentTask) error {
	if task.Type != model.AgentTaskTypeNetworkTrace || task.ServerID != server.ID {
		return errors.New("invalid monitor task")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = runtimeSecurityServerTx(ctx, tx, server); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "select count(*) from probe_monitors where id=? and server_id=? and revision=? and enabled=1", v.ID, server.ID, v.Revision).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("监控任务已改变")
	}
	if err = tx.QueryRowContext(ctx, "select count(*) from agent_tasks where server_id=? and type in ('probe_network_trace','probe_network_ping','probe_network_tcp','probe_network_dns','probe_network_http') and status in ('pending','running')", server.ID).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("服务器已有网络诊断在执行")
	}
	ts := now()
	task.ID, err = operationTaskInsert(ctx, tx, *task, ts)
	if err != nil {
		return err
	}
	return tx.Commit()
}
