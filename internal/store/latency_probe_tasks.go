package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	latencyProbeTaskMinInterval = 30
	latencyProbeTaskMaxInterval = 86400
	latencyProbeTaskMaxServers  = 512
	latencyProbeTaskMaxNameLen  = 60
)

func latencyProbeTargetLabel(province, carrier string) string {
	province = strings.TrimSpace(province)
	carrier = strings.TrimSpace(carrier)
	switch {
	case province == "" && carrier == "":
		return ""
	case province == "":
		return carrier
	case carrier == "":
		return province
	default:
		return province + " · " + carrier
	}
}

// LatencyProbeTargetLabel renders the default display label for a probe target.
func LatencyProbeTargetLabel(province, carrier string) string {
	return latencyProbeTargetLabel(province, carrier)
}

func normalizeLatencyProbeTask(task *model.LatencyProbeTask) error {
	if task == nil {
		return errors.New("延迟探测任务为空")
	}
	task.Province = strings.TrimSpace(task.Province)
	task.Carrier = strings.TrimSpace(task.Carrier)
	task.Address = strings.TrimSpace(task.Address)
	if task.Address == "" && (task.Province == "" || task.Carrier == "") {
		return errors.New("请填写目标地址或选择完整的省份和运营商")
	}
	if task.Method == "" {
		task.Method = model.LatencyProbeModeTCP
	}
	switch task.Method {
	case model.LatencyProbeModeTCP:
		if task.Port == 0 {
			task.Port = 80
		}
		if task.Port < 1 || task.Port > 65535 {
			return errors.New("TCP 端口需要在 1–65535 之间")
		}
	case model.LatencyProbeModeICMP:
		task.Port = 0
	case model.LatencyProbeModeHTTP:
		task.Port = 0
		if task.Address == "" {
			return errors.New("HTTP 探测需要填写完整 URL")
		}
	default:
		return errors.New("探测方式必须是 TCP、Ping 或 HTTP")
	}
	if task.Address != "" {
		if err := ValidateNetworkProbeAddress(task.Address, task.Method); err != nil {
			return err
		}
		if task.Method == model.LatencyProbeModeHTTP {
			u, _ := url.Parse(task.Address)
			u.Host = strings.ToLower(u.Host)
			task.Address = u.String()
		} else {
			task.Address = strings.ToLower(task.Address)
		}
		task.Province, task.Carrier = "", ""
	}
	task.Name = strings.TrimSpace(task.Name)
	if task.Name == "" {
		task.Name = latencyProbeTargetLabel(task.Province, task.Carrier)
		if task.Name == "" {
			task.Name = string([]rune(task.Address)[:min(len([]rune(task.Address)), latencyProbeTaskMaxNameLen)])
		}
	}
	if len([]rune(task.Name)) > latencyProbeTaskMaxNameLen {
		return errors.New("任务名称过长")
	}
	if task.IntervalSeconds == 0 {
		task.IntervalSeconds = 300
	}
	if task.IntervalSeconds < latencyProbeTaskMinInterval || task.IntervalSeconds > latencyProbeTaskMaxInterval {
		return errors.New("探测间隔需要在 30 秒到 86400 秒之间")
	}
	seen := make(map[int64]bool, len(task.ServerIDs))
	servers := make([]int64, 0, len(task.ServerIDs))
	for _, id := range task.ServerIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			servers = append(servers, id)
		}
	}
	if len(servers) > latencyProbeTaskMaxServers {
		return errors.New("单个任务关联的服务器过多")
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i] < servers[j] })
	task.ServerIDs = servers
	return nil
}

func (s *Store) scanLatencyProbeTasks(ctx context.Context, query string, args ...any) ([]model.LatencyProbeTask, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]model.LatencyProbeTask, 0)
	for rows.Next() {
		var task model.LatencyProbeTask
		var enabled int
		var createdAt, updatedAt string
		if err := rows.Scan(&task.ID, &task.Name, &task.Province, &task.Carrier, &task.IntervalSeconds, &task.Method, &task.Address, &task.Port, &enabled, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		task.Enabled = enabled == 1
		task.CreatedAt = parseTime(createdAt)
		task.UpdatedAt = parseTime(updatedAt)
		task.ServerIDs = []int64{}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s *Store) attachLatencyProbeTaskServers(ctx context.Context, tasks []model.LatencyProbeTask) error {
	if len(tasks) == 0 {
		return nil
	}
	byID := make(map[int64]*model.LatencyProbeTask, len(tasks))
	for i := range tasks {
		byID[tasks[i].ID] = &tasks[i]
	}
	rows, err := s.db.QueryContext(ctx, `select task_id,server_id from latency_probe_task_servers order by task_id,server_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID, serverID int64
		if err := rows.Scan(&taskID, &serverID); err != nil {
			return err
		}
		if task := byID[taskID]; task != nil {
			task.ServerIDs = append(task.ServerIDs, serverID)
		}
	}
	return rows.Err()
}

// ListLatencyProbeTasks returns every probe task with its assigned servers.
func (s *Store) ListLatencyProbeTasks(ctx context.Context) ([]model.LatencyProbeTask, error) {
	tasks, err := s.scanLatencyProbeTasks(ctx, `select id,name,province,carrier,interval_seconds,method,address,port,enabled,created_at,updated_at from latency_probe_tasks order by name,id`)
	if err != nil {
		return nil, err
	}
	if err := s.attachLatencyProbeTaskServers(ctx, tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetLatencyProbeTask loads one probe task by identifier.
func (s *Store) GetLatencyProbeTask(ctx context.Context, id int64) (*model.LatencyProbeTask, error) {
	tasks, err := s.scanLatencyProbeTasks(ctx, `select id,name,province,carrier,interval_seconds,method,address,port,enabled,created_at,updated_at from latency_probe_tasks where id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, sql.ErrNoRows
	}
	if err := s.attachLatencyProbeTaskServers(ctx, tasks); err != nil {
		return nil, err
	}
	return &tasks[0], nil
}

// ListLatencyProbeTasksForServer returns the enabled probe tasks assigned to one server.
func (s *Store) ListLatencyProbeTasksForServer(ctx context.Context, serverID int64) ([]model.LatencyProbeTask, error) {
	tasks, err := s.scanLatencyProbeTasks(ctx, `select t.id,t.name,t.province,t.carrier,t.interval_seconds,t.method,t.address,t.port,t.enabled,t.created_at,t.updated_at from latency_probe_tasks t join latency_probe_task_servers m on m.task_id=t.id where m.server_id=? and t.enabled=1 order by t.name,t.id`, serverID)
	if err != nil {
		return nil, err
	}
	for i := range tasks {
		tasks[i].ServerIDs = []int64{serverID}
	}
	return tasks, nil
}

// ValidateLatencyProbeTask normalizes a probe task and reports the same rejection
// reasons SaveLatencyProbeTask would raise, without writing anything.
func (s *Store) ValidateLatencyProbeTask(ctx context.Context, task *model.LatencyProbeTask) error {
	if err := normalizeLatencyProbeTask(task); err != nil {
		return err
	}
	var conflicts int
	if err := s.db.QueryRowContext(ctx, `select count(*) from latency_probe_tasks where name=? and id<>?`, task.Name, task.ID).Scan(&conflicts); err != nil {
		return err
	}
	if conflicts > 0 {
		return errors.New("已存在同名的延迟探测任务")
	}
	return nil
}

// SaveLatencyProbeTask creates or updates one probe task together with its server assignment.
func (s *Store) SaveLatencyProbeTask(ctx context.Context, task *model.LatencyProbeTask) error {
	if err := normalizeLatencyProbeTask(task); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var conflicts int
	if err := tx.QueryRowContext(ctx, `select count(*) from latency_probe_tasks where name=? and id<>?`, task.Name, task.ID).Scan(&conflicts); err != nil {
		return err
	}
	if conflicts > 0 {
		return errors.New("已存在同名的延迟探测任务")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if task.ID == 0 {
		result, err := tx.ExecContext(ctx, `insert into latency_probe_tasks(name,province,carrier,interval_seconds,method,address,port,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`, task.Name, task.Province, task.Carrier, task.IntervalSeconds, task.Method, task.Address, task.Port, boolInt(task.Enabled), now, now)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		task.ID = id
		task.CreatedAt = parseTime(now)
	} else {
		result, err := tx.ExecContext(ctx, `update latency_probe_tasks set name=?,province=?,carrier=?,interval_seconds=?,method=?,address=?,port=?,enabled=?,updated_at=? where id=?`, task.Name, task.Province, task.Carrier, task.IntervalSeconds, task.Method, task.Address, task.Port, boolInt(task.Enabled), now, task.ID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return sql.ErrNoRows
		}
	}
	task.UpdatedAt = parseTime(now)
	if _, err := tx.ExecContext(ctx, `delete from latency_probe_task_servers where task_id=?`, task.ID); err != nil {
		return err
	}
	stored := make([]int64, 0, len(task.ServerIDs))
	for _, serverID := range task.ServerIDs {
		var exists int
		if err := tx.QueryRowContext(ctx, `select count(*) from servers where id=?`, serverID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `insert or ignore into latency_probe_task_servers(task_id,server_id) values(?,?)`, task.ID, serverID); err != nil {
			return err
		}
		stored = append(stored, serverID)
	}
	task.ServerIDs = stored
	return tx.Commit()
}

// DeleteLatencyProbeTask removes one probe task and its server assignment.
func (s *Store) DeleteLatencyProbeTask(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `delete from latency_probe_tasks where id=?`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// migrateLatencyProbeTasks converts the former per-server regional selection into standalone probe tasks.
func (s *Store) migrateLatencyProbeTasks(ctx context.Context) error {
	const key = "migration.controller-db-20260905-latency-probe-tasks"
	var marker string
	if err := s.db.QueryRowContext(ctx, `select value from app_settings where key=?`, key).Scan(&marker); err == nil {
		return nil
	} else if err != sql.ErrNoRows {
		return err
	}
	if err := s.ensureColumn(ctx, "server_latency_probe_settings", "regions_json", `alter table server_latency_probe_settings add column regions_json text not null default '[]'`); err != nil {
		return err
	}
	type legacyTask struct {
		interval  int
		serverIDs []int64
	}
	rows, err := s.db.QueryContext(ctx, `select server_id,interval_seconds,regions_json from server_latency_probe_settings where trim(coalesce(regions_json,''))<>'' and regions_json<>'[]'`)
	if err != nil {
		return err
	}
	grouped := map[model.LatencyProbeRegion]*legacyTask{}
	order := []model.LatencyProbeRegion{}
	for rows.Next() {
		var serverID int64
		var interval int
		var regionsJSON string
		if err := rows.Scan(&serverID, &interval, &regionsJSON); err != nil {
			rows.Close()
			return err
		}
		var regions []model.LatencyProbeRegion
		if err := json.Unmarshal([]byte(regionsJSON), &regions); err != nil {
			continue
		}
		if interval < latencyProbeTaskMinInterval {
			interval = latencyProbeTaskMinInterval
		}
		if interval > latencyProbeTaskMaxInterval {
			interval = latencyProbeTaskMaxInterval
		}
		for _, region := range regions {
			region.Province = strings.TrimSpace(region.Province)
			region.Carrier = strings.TrimSpace(region.Carrier)
			if region.Province == "" || region.Carrier == "" {
				continue
			}
			entry := grouped[region]
			if entry == nil {
				entry = &legacyTask{interval: interval}
				grouped[region] = entry
				order = append(order, region)
			}
			if interval < entry.interval {
				entry.interval = interval
			}
			entry.serverIDs = append(entry.serverIDs, serverID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	sort.Slice(order, func(i, j int) bool {
		if order[i].Province == order[j].Province {
			return order[i].Carrier < order[j].Carrier
		}
		return order[i].Province < order[j].Province
	})
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, region := range order {
		entry := grouped[region]
		name := latencyProbeTargetLabel(region.Province, region.Carrier)
		var exists int
		if err := tx.QueryRowContext(ctx, `select count(*) from latency_probe_tasks where name=?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		result, err := tx.ExecContext(ctx, `insert into latency_probe_tasks(name,province,carrier,interval_seconds,enabled,created_at,updated_at) values(?,?,?,?,1,?,?)`, name, region.Province, region.Carrier, entry.interval, now, now)
		if err != nil {
			return err
		}
		taskID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		for _, serverID := range entry.serverIDs {
			if _, err := tx.ExecContext(ctx, `insert or ignore into latency_probe_task_servers(task_id,server_id) values(?,?)`, taskID, serverID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `update server_latency_probe_settings set regions_json='[]' where regions_json<>'[]'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?)`, key, "completed", now); err != nil {
		return err
	}
	return tx.Commit()
}

// ValidateNetworkProbeAddress validates operator-supplied network probe destinations.
func ValidateNetworkProbeAddress(address string, method model.LatencyProbeMode) error {
	host := address
	if method == model.LatencyProbeModeHTTP {
		u, err := url.Parse(address)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(address) > 2048 {
			return errors.New("请输入不含凭据或片段的 HTTP / HTTPS URL")
		}
		if u.Port() != "" {
			port, err := strconv.Atoi(u.Port())
			if err != nil || port < 1 || port > 65535 {
				return errors.New("HTTP 端口无效")
			}
		}
		host = u.Hostname()
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !addr.Is4() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
			return errors.New("目标必须是公网 IPv4 地址或域名")
		}
		return nil
	}
	if len(host) > 253 || !strings.Contains(host, ".") {
		return errors.New("请输入公网 IPv4 地址或完整域名")
	}
	labels := strings.Split(strings.TrimSuffix(host, "."), ".")
	if len(labels) < 2 || len(labels[len(labels)-1]) < 2 {
		return errors.New("目标域名无效")
	}
	for _, c := range strings.ToLower(labels[len(labels)-1]) {
		if c < 'a' || c > 'z' {
			return errors.New("目标域名无效")
		}
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return errors.New("目标域名无效")
		}
		for _, c := range strings.ToLower(label) {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return errors.New("目标域名无效")
			}
		}
	}
	return nil
}

func (s *Store) migrateNetworkProbeTaskFields(ctx context.Context) error {
	for _, column := range []struct{ name, ddl string }{
		{"method", "alter table latency_probe_tasks add column method text not null default 'tcp'"},
		{"address", "alter table latency_probe_tasks add column address text not null default ''"},
		{"port", "alter table latency_probe_tasks add column port integer not null default 80"},
	} {
		if err := s.ensureColumn(ctx, "latency_probe_tasks", column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}
