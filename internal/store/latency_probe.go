package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func normalizeLatencyProbeSettings(server *model.Server) {
	if server.LatencyProbeIntervalSeconds < 30 || server.LatencyProbeIntervalSeconds > 86400 {
		server.LatencyProbeIntervalSeconds = 300
	}
	if server.LatencyProbeSampleCount < 1 || server.LatencyProbeSampleCount > 10 {
		server.LatencyProbeSampleCount = 3
	}
	if server.LatencyProbeMaxTargets < 1 || server.LatencyProbeMaxTargets > 256 {
		server.LatencyProbeMaxTargets = 64
	}
	server.LatencyProbeProvinces = cleanStringList(server.LatencyProbeProvinces)
	server.LatencyProbeCarriers = cleanStringList(server.LatencyProbeCarriers)
}

func cleanStringList(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func (s *Store) attachServerLatencySettings(ctx context.Context, servers []model.Server) error {
	if len(servers) == 0 {
		return nil
	}
	byID := make(map[int64]*model.Server, len(servers))
	for i := range servers {
		servers[i].LatencyProbeIntervalSeconds = 300
		servers[i].LatencyProbeSampleCount = 3
		servers[i].LatencyProbeMaxTargets = 64
		byID[servers[i].ID] = &servers[i]
	}
	rows, err := s.db.QueryContext(ctx, `select server_id,enabled,interval_seconds,sample_count,provinces_json,carriers_json,max_targets,resource_version from server_latency_probe_settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var enabled, interval, samples, maxTargets int
		var provincesJSON, carriersJSON, resourceVersion string
		if err := rows.Scan(&id, &enabled, &interval, &samples, &provincesJSON, &carriersJSON, &maxTargets, &resourceVersion); err != nil {
			return err
		}
		server := byID[id]
		if server == nil {
			continue
		}
		server.LatencyProbeEnabled = enabled != 0
		server.LatencyProbeIntervalSeconds = interval
		server.LatencyProbeSampleCount = samples
		server.LatencyProbeMaxTargets = maxTargets
		server.LatencyProbeResourceVersion = resourceVersion
		_ = json.Unmarshal([]byte(provincesJSON), &server.LatencyProbeProvinces)
		_ = json.Unmarshal([]byte(carriersJSON), &server.LatencyProbeCarriers)
		normalizeLatencyProbeSettings(server)
	}
	return rows.Err()
}

func (s *Store) UpdateServerLatencyProbeSettings(ctx context.Context, server *model.Server) error {
	if server == nil || server.ID <= 0 {
		return errors.New("latency probe requires a server")
	}
	normalizeLatencyProbeSettings(server)
	provinces, _ := json.Marshal(server.LatencyProbeProvinces)
	carriers, _ := json.Marshal(server.LatencyProbeCarriers)
	_, err := s.db.ExecContext(ctx, `insert into server_latency_probe_settings(server_id,enabled,interval_seconds,sample_count,provinces_json,carriers_json,max_targets,resource_version,updated_at) values(?,?,?,?,?,?,?,?,?) on conflict(server_id) do update set enabled=excluded.enabled,interval_seconds=excluded.interval_seconds,sample_count=excluded.sample_count,provinces_json=excluded.provinces_json,carriers_json=excluded.carriers_json,max_targets=excluded.max_targets,resource_version=excluded.resource_version,updated_at=excluded.updated_at`, server.ID, boolInt(server.LatencyProbeEnabled), server.LatencyProbeIntervalSeconds, server.LatencyProbeSampleCount, string(provinces), string(carriers), server.LatencyProbeMaxTargets, server.LatencyProbeResourceVersion, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListLatencyProbeResults(ctx context.Context, serverID int64, limit int) ([]model.LatencyProbeResult, error) {
	if limit <= 0 || limit > 4096 {
		limit = 512
	}
	rows, err := s.db.QueryContext(ctx, `select probe_id,province,carrier,ip,available,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,checked_at from server_latency_probe_results where server_id=? order by checked_at desc,id desc limit ?`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.LatencyProbeResult{}
	for rows.Next() {
		var item model.LatencyProbeResult
		var available int
		var checked string
		if err := rows.Scan(&item.ProbeID, &item.Province, &item.Carrier, &item.IP, &available, &item.LatencyMS, &item.MinLatencyMS, &item.P95LatencyMS, &item.JitterMS, &item.SampleCount, &item.SuccessCount, &item.Error, &checked); err != nil {
			return nil, err
		}
		item.Available = available != 0
		item.CheckedAt = parseTime(checked)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SaveLatencyProbeResults(ctx context.Context, serverID int64, report model.LatencyProbeResultReport) error {
	if serverID <= 0 || strings.TrimSpace(report.ResourceVersion) == "" {
		return errors.New("latency probe result is missing server or resource version")
	}
	checkedAt := report.CheckedAt.UTC()
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range report.Items {
		if strings.TrimSpace(item.ProbeID) == "" {
			continue
		}
		if len(item.Error) > 240 {
			item.Error = item.Error[:240]
		}
		if _, err := tx.ExecContext(ctx, `insert or replace into server_latency_probe_results(server_id,resource_version,probe_id,province,carrier,ip,available,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,checked_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, serverID, report.ResourceVersion, item.ProbeID, item.Province, item.Carrier, item.IP, boolInt(item.Available), item.LatencyMS, item.MinLatencyMS, item.P95LatencyMS, item.JitterMS, item.SampleCount, item.SuccessCount, item.Error, checkedAt.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	_, _ = tx.ExecContext(ctx, `delete from server_latency_probe_results where server_id=? and checked_at < ?`, serverID, time.Now().UTC().Add(-35*24*time.Hour).Format(time.RFC3339Nano))
	return tx.Commit()
}

func (s *Store) DeleteLatencyProbeResults(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from server_latency_probe_results where server_id=?`, serverID)
	return err
}
