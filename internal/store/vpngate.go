package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
)

// ErrVPNGatePoolInUse is returned when a pool is still referenced by a path.
var ErrVPNGatePoolInUse = errors.New("出口池仍被代理拓扑使用，请先移除相关出口节点")

const (
	vpngateMaxPools      = 64
	vpngateNodeRetention = 24 * time.Hour
	vpngateHistoryLimit  = 20
)

type vpngatePolicy struct {
	Countries       []string `json:"countries"`
	OperatorASNs    []string `json:"operator_asns"`
	Network         string   `json:"network"`
	SelectMode      string   `json:"select_mode"`
	MaxRTTMS        int      `json:"max_rtt_ms"`
	MinQualityScore int      `json:"min_quality_score"`
	HotStandby      int      `json:"hot_standby"`
	PoolSize        int      `json:"pool_size"`
	SwitchPolicy    string   `json:"switch_policy"`
	RefreshMinutes  int      `json:"refresh_minutes"`
}

func policyFromPool(pool model.VPNGatePool) vpngatePolicy {
	return vpngatePolicy{
		Countries: pool.Countries, OperatorASNs: pool.OperatorASNs, Network: pool.Network, SelectMode: pool.SelectMode,
		MaxRTTMS: pool.MaxRTTMS, MinQualityScore: pool.MinQualityScore, HotStandby: pool.HotStandby, PoolSize: pool.PoolSize,
		SwitchPolicy: pool.SwitchPolicy, RefreshMinutes: pool.RefreshMinutes,
	}
}

func applyPolicy(pool *model.VPNGatePool, policy vpngatePolicy) {
	pool.Countries, pool.OperatorASNs, pool.Network, pool.SelectMode = policy.Countries, policy.OperatorASNs, policy.Network, policy.SelectMode
	pool.MaxRTTMS, pool.MinQualityScore, pool.HotStandby, pool.PoolSize = policy.MaxRTTMS, policy.MinQualityScore, policy.HotStandby, policy.PoolSize
	pool.SwitchPolicy, pool.RefreshMinutes = policy.SwitchPolicy, policy.RefreshMinutes
	if pool.Countries == nil {
		pool.Countries = []string{}
	}
	if pool.OperatorASNs == nil {
		pool.OperatorASNs = []string{}
	}
}

func scanVPNGatePool(scanner interface{ Scan(...any) error }) (model.VPNGatePool, error) {
	var pool model.VPNGatePool
	var policyJSON, created, updated string
	var enabled int
	if err := scanner.Scan(&pool.ID, &pool.Name, &policyJSON, &enabled, &created, &updated); err != nil {
		return pool, err
	}
	var policy vpngatePolicy
	_ = json.Unmarshal([]byte(policyJSON), &policy)
	applyPolicy(&pool, policy)
	pool.Enabled = enabled == 1
	pool.CreatedAt, pool.UpdatedAt = parseTime(created), parseTime(updated)
	return pool, nil
}

func (s *Store) ListVPNGatePools(ctx context.Context) ([]model.VPNGatePool, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,policy_json,enabled,created_at,updated_at from vpngate_pools order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.VPNGatePool{}
	for rows.Next() {
		pool, err := scanVPNGatePool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pool)
	}
	return out, rows.Err()
}

func (s *Store) GetVPNGatePool(ctx context.Context, id int64) (*model.VPNGatePool, error) {
	pool, err := scanVPNGatePool(s.db.QueryRowContext(ctx, `select id,name,policy_json,enabled,created_at,updated_at from vpngate_pools where id=?`, id))
	if err != nil {
		return nil, err
	}
	return &pool, nil
}

func (s *Store) CreateVPNGatePool(ctx context.Context, pool *model.VPNGatePool) error {
	if err := core.NormalizeVPNGatePool(pool); err != nil {
		return err
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from vpngate_pools`).Scan(&count); err != nil {
		return err
	}
	if count >= vpngateMaxPools {
		return fmt.Errorf("最多创建 %d 个出口池", vpngateMaxPools)
	}
	policy, _ := json.Marshal(policyFromPool(*pool))
	ts := now()
	result, err := s.db.ExecContext(ctx, `insert into vpngate_pools(name,policy_json,enabled,created_at,updated_at) values(?,?,?,?,?)`, pool.Name, string(policy), boolInt(pool.Enabled), ts, ts)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return errors.New("出口池名称已存在")
		}
		return err
	}
	pool.ID, _ = result.LastInsertId()
	pool.CreatedAt, pool.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) UpdateVPNGatePool(ctx context.Context, pool *model.VPNGatePool) error {
	if err := core.NormalizeVPNGatePool(pool); err != nil {
		return err
	}
	policy, _ := json.Marshal(policyFromPool(*pool))
	ts := now()
	result, err := s.db.ExecContext(ctx, `update vpngate_pools set name=?,policy_json=?,enabled=?,updated_at=? where id=?`, pool.Name, string(policy), boolInt(pool.Enabled), ts, pool.ID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return errors.New("出口池名称已存在")
		}
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	pool.UpdatedAt = parseTime(ts)
	return nil
}

// VPNGatePoolReferenced reports whether any path step exits through the pool.
func (s *Store) VPNGatePoolReferenced(ctx context.Context, id int64) (bool, error) {
	var referenced bool
	err := s.db.QueryRowContext(ctx, `select exists(select 1 from proxy_path_steps where node_type=? and json_extract(config_json,'$.vpngate_pool_id')=?)`, string(model.ProxyPathStepVPNGate), id).Scan(&referenced)
	return referenced, err
}

func (s *Store) DeleteVPNGatePool(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var referenced bool
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from proxy_path_steps where node_type=? and json_extract(config_json,'$.vpngate_pool_id')=?)`, string(model.ProxyPathStepVPNGate), id).Scan(&referenced); err != nil {
		return err
	}
	if referenced {
		return ErrVPNGatePoolInUse
	}
	result, err := tx.ExecContext(ctx, `delete from vpngate_pools where id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// UpsertVPNGateCatalog merges a fetched catalog. The VPNGate API returns only
// part of the list per request, so entries accumulate and expire after they
// have not been seen for a day.
func (s *Store) UpsertVPNGateCatalog(ctx context.Context, entries []core.VPNGateCatalogEntry, skipped int, source string, at time.Time) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339Nano)
	for _, entry := range entries {
		spec, err := json.Marshal(entry.Spec)
		if err != nil {
			return 0, err
		}
		key := core.VPNGateNodeKey(entry.IP, entry.Port, entry.Network)
		if _, err := tx.ExecContext(ctx, `insert into vpngate_nodes(node_key,host_name,ip,port,network,country_code,country_name,score,ping_ms,speed_bps,sessions,uptime_ms,operator,spec_json,first_seen_at,last_seen_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			on conflict(node_key) do update set host_name=excluded.host_name,country_code=excluded.country_code,country_name=excluded.country_name,score=excluded.score,ping_ms=excluded.ping_ms,speed_bps=excluded.speed_bps,sessions=excluded.sessions,uptime_ms=excluded.uptime_ms,operator=excluded.operator,spec_json=excluded.spec_json,last_seen_at=excluded.last_seen_at`,
			key, entry.HostName, entry.IP, entry.Port, entry.Network, entry.CountryCode, entry.CountryName, entry.Score, entry.PingMS, entry.SpeedBPS, entry.Sessions, entry.UptimeMS, entry.Operator, string(spec), ts, ts); err != nil {
			return 0, err
		}
	}
	cutoff := at.Add(-vpngateNodeRetention).UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `delete from vpngate_nodes where last_seen_at < ?`, cutoff); err != nil {
		return 0, err
	}
	var total int
	if err := tx.QueryRowContext(ctx, `select count(*) from vpngate_nodes`).Scan(&total); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `insert into vpngate_catalog_state(id,fetched_at,source,entries,skipped,error) values(1,?,?,?,?,'') on conflict(id) do update set fetched_at=excluded.fetched_at,source=excluded.source,entries=excluded.entries,skipped=excluded.skipped,error=''`, ts, source, len(entries), skipped); err != nil {
		return 0, err
	}
	return total, tx.Commit()
}

// RecordVPNGateCatalogError keeps the last fetch failure for the operator.
func (s *Store) RecordVPNGateCatalogError(ctx context.Context, message string) error {
	_, err := s.db.ExecContext(ctx, `insert into vpngate_catalog_state(id,error) values(1,?) on conflict(id) do update set error=excluded.error`, message)
	return err
}

// VPNGateCatalogState summarizes the last catalog fetch.
type VPNGateCatalogState struct {
	FetchedAt time.Time `json:"fetched_at"`
	Source    string    `json:"source"`
	Entries   int       `json:"entries"`
	Skipped   int       `json:"skipped"`
	Error     string    `json:"error,omitempty"`
	Total     int       `json:"total"`
}

func (s *Store) GetVPNGateCatalogState(ctx context.Context) (VPNGateCatalogState, error) {
	var state VPNGateCatalogState
	var fetched string
	err := s.db.QueryRowContext(ctx, `select fetched_at,source,entries,skipped,error from vpngate_catalog_state where id=1`).Scan(&fetched, &state.Source, &state.Entries, &state.Skipped, &state.Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	if fetched != "" {
		state.FetchedAt = parseTime(fetched)
	}
	if err := s.db.QueryRowContext(ctx, `select count(*) from vpngate_nodes`).Scan(&state.Total); err != nil {
		return state, err
	}
	return state, nil
}

const vpngateNodeColumns = `id,node_key,host_name,ip,port,network,country_code,country_name,score,ping_ms,speed_bps,sessions,uptime_ms,operator,spec_json,first_seen_at,last_seen_at`

func scanVPNGateNode(scanner interface{ Scan(...any) error }) (model.VPNGateNode, error) {
	var node model.VPNGateNode
	var spec, first, last string
	if err := scanner.Scan(&node.ID, &node.NodeKey, &node.HostName, &node.IP, &node.Port, &node.Network, &node.CountryCode, &node.CountryName, &node.Score, &node.PingMS, &node.SpeedBPS, &node.Sessions, &node.UptimeMS, &node.Operator, &spec, &first, &last); err != nil {
		return node, err
	}
	if err := json.Unmarshal([]byte(spec), &node.Spec); err != nil {
		return node, err
	}
	node.FirstSeenAt, node.LastSeenAt = parseTime(first), parseTime(last)
	return node, nil
}

func (s *Store) ListVPNGateNodes(ctx context.Context) ([]model.VPNGateNode, error) {
	rows, err := s.db.QueryContext(ctx, `select `+vpngateNodeColumns+` from vpngate_nodes order by score desc, id asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.VPNGateNode{}
	for rows.Next() {
		node, err := scanVPNGateNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, node)
	}
	return out, rows.Err()
}

func (s *Store) GetVPNGateIPQuality(ctx context.Context, ip string) (*model.VPNGateIPQuality, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `select quality_json from vpngate_ip_quality where ip=?`, ip).Scan(&raw); err != nil {
		return nil, err
	}
	var quality model.VPNGateIPQuality
	if err := json.Unmarshal([]byte(raw), &quality); err != nil {
		return nil, err
	}
	return &quality, nil
}

func (s *Store) SaveVPNGateIPQuality(ctx context.Context, quality model.VPNGateIPQuality) error {
	raw, err := json.Marshal(quality)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into vpngate_ip_quality(ip,quality_json,checked_at) values(?,?,?) on conflict(ip) do update set quality_json=excluded.quality_json,checked_at=excluded.checked_at`, quality.IP, string(raw), quality.CheckedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `delete from vpngate_ip_quality where checked_at < ?`, time.Now().Add(-7*24*time.Hour).UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) GetVPNGatePoolServer(ctx context.Context, poolID, serverID int64) (*model.VPNGatePoolServer, error) {
	var raw string
	if err := s.db.QueryRowContext(ctx, `select state_json from vpngate_pool_servers where pool_id=? and server_id=?`, poolID, serverID).Scan(&raw); err != nil {
		return nil, err
	}
	var state model.VPNGatePoolServer
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return nil, err
	}
	state.PoolID, state.ServerID = poolID, serverID
	return &state, nil
}

func (s *Store) ListVPNGatePoolServers(ctx context.Context) ([]model.VPNGatePoolServer, error) {
	rows, err := s.db.QueryContext(ctx, `select pool_id,server_id,state_json from vpngate_pool_servers order by pool_id,server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.VPNGatePoolServer{}
	for rows.Next() {
		var poolID, serverID int64
		var raw string
		if err := rows.Scan(&poolID, &serverID, &raw); err != nil {
			return nil, err
		}
		var state model.VPNGatePoolServer
		if json.Unmarshal([]byte(raw), &state) != nil {
			continue
		}
		state.PoolID, state.ServerID = poolID, serverID
		out = append(out, state)
	}
	return out, rows.Err()
}

func (s *Store) SaveVPNGatePoolServer(ctx context.Context, state *model.VPNGatePoolServer) error {
	if len(state.History) > vpngateHistoryLimit {
		state.History = state.History[len(state.History)-vpngateHistoryLimit:]
	}
	state.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `insert into vpngate_pool_servers(pool_id,server_id,state_json,updated_at) values(?,?,?,?) on conflict(pool_id,server_id) do update set state_json=excluded.state_json,updated_at=excluded.updated_at`, state.PoolID, state.ServerID, string(raw), state.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// DeleteVPNGatePoolServer drops selection state for a pair no longer in use.
func (s *Store) DeleteVPNGatePoolServer(ctx context.Context, poolID, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from vpngate_pool_servers where pool_id=? and server_id=?`, poolID, serverID)
	return err
}
