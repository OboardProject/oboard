package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func normalizeLatencyProbeSettings(server *model.Server) {
	if server.LatencyProbeMode != model.LatencyProbeModeICMP {
		server.LatencyProbeMode = model.LatencyProbeModeTCP
	}
	switch server.LatencyProbePublicTarget {
	case model.ConnectivityProbeTargetCloudflare, model.ConnectivityProbeTarget12306, model.ConnectivityProbeTargetGoogle:
	default:
		server.LatencyProbePublicTarget = model.ConnectivityProbeTargetAuto
	}
	if server.LatencyProbeIntervalSeconds < 30 || server.LatencyProbeIntervalSeconds > 86400 {
		server.LatencyProbeIntervalSeconds = 60
	}
	if server.LatencyProbeSampleCount < 1 || server.LatencyProbeSampleCount > 10 {
		server.LatencyProbeSampleCount = 3
	}
	if server.LatencyProbeMaxTargets < 1 || server.LatencyProbeMaxTargets > 256 {
		server.LatencyProbeMaxTargets = 64
	}
	server.LatencyProbeRegions = cleanLatencyProbeRegions(server.LatencyProbeRegions)
}

func cleanLatencyProbeRegions(values []model.LatencyProbeRegion) []model.LatencyProbeRegion {
	seen := map[string]bool{}
	out := make([]model.LatencyProbeRegion, 0, len(values))
	for _, value := range values {
		value.Province = strings.TrimSpace(value.Province)
		value.Carrier = strings.TrimSpace(value.Carrier)
		key := value.Province + "\x00" + value.Carrier
		if value.Province != "" && value.Carrier != "" && !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Province == out[j].Province {
			return out[i].Carrier < out[j].Carrier
		}
		return out[i].Province < out[j].Province
	})
	return out
}

func (s *Store) attachServerLatencySettings(ctx context.Context, servers []model.Server) error {
	if len(servers) == 0 {
		return nil
	}
	byID := make(map[int64]*model.Server, len(servers))
	for i := range servers {
		servers[i].LatencyProbeEnabled = true
		servers[i].LatencyProbeMode = model.LatencyProbeModeTCP
		servers[i].LatencyProbePublicTarget = model.ConnectivityProbeTargetAuto
		servers[i].LatencyProbeIntervalSeconds = 60
		servers[i].LatencyProbeSampleCount = 3
		servers[i].LatencyProbeMaxTargets = 64
		byID[servers[i].ID] = &servers[i]
	}
	serverIDs, placeholders := serverIDQueryArgs(servers)
	rows, err := s.db.QueryContext(ctx, `select server_id,enabled,mode,public_target,interval_seconds,sample_count,regions_json,max_targets,resource_version from server_latency_probe_settings where server_id in (`+placeholders+`)`, serverIDs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var enabled, interval, samples, maxTargets int
		var mode, publicTarget, regionsJSON, resourceVersion string
		if err := rows.Scan(&id, &enabled, &mode, &publicTarget, &interval, &samples, &regionsJSON, &maxTargets, &resourceVersion); err != nil {
			return err
		}
		server := byID[id]
		if server == nil {
			continue
		}
		server.LatencyProbeEnabled = enabled != 0
		server.LatencyProbeMode = model.LatencyProbeMode(mode)
		server.LatencyProbePublicTarget = model.ConnectivityTarget(publicTarget)
		server.LatencyProbeIntervalSeconds = interval
		server.LatencyProbeSampleCount = samples
		server.LatencyProbeMaxTargets = maxTargets
		server.LatencyProbeResourceVersion = resourceVersion
		_ = json.Unmarshal([]byte(regionsJSON), &server.LatencyProbeRegions)
		normalizeLatencyProbeSettings(server)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for index := range servers {
		if !servers[index].LatencyProbeEnabled {
			servers[index].ConnectivityStatus = "disabled"
		}
	}
	return nil
}

func (s *Store) UpdateServerLatencyProbeSettings(ctx context.Context, server *model.Server) error {
	if server == nil || server.ID <= 0 {
		return errors.New("latency probe requires a server")
	}
	normalizeLatencyProbeSettings(server)
	regions, _ := json.Marshal(server.LatencyProbeRegions)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldEnabled int
	var oldMode, oldPublicTarget string
	oldErr := tx.QueryRowContext(ctx, `select enabled,mode,public_target from server_latency_probe_settings where server_id=?`, server.ID).Scan(&oldEnabled, &oldMode, &oldPublicTarget)
	if oldErr != nil && oldErr != sql.ErrNoRows {
		return oldErr
	}
	updatedAt := server.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	if _, err := tx.ExecContext(ctx, `insert into server_latency_probe_settings(server_id,enabled,mode,public_target,interval_seconds,sample_count,regions_json,max_targets,resource_version,updated_at) values(?,?,?,?,?,?,?,?,?,?) on conflict(server_id) do update set enabled=excluded.enabled,mode=excluded.mode,public_target=excluded.public_target,interval_seconds=excluded.interval_seconds,sample_count=excluded.sample_count,regions_json=excluded.regions_json,max_targets=excluded.max_targets,resource_version=excluded.resource_version,updated_at=excluded.updated_at`, server.ID, boolInt(server.LatencyProbeEnabled), server.LatencyProbeMode, server.LatencyProbePublicTarget, server.LatencyProbeIntervalSeconds, server.LatencyProbeSampleCount, string(regions), server.LatencyProbeMaxTargets, server.LatencyProbeResourceVersion, updatedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	newEnabled := boolInt(server.LatencyProbeEnabled)
	if oldErr == sql.ErrNoRows || oldEnabled != newEnabled {
		state := "disabled"
		if server.LatencyProbeEnabled {
			state = "enabled"
		}
		if _, err := recordConnectivitySettingEvent(ctx, tx, server.ID, server.LatencyProbeEnabled, updatedAt, "latency-setting:"+updatedAt.Format(time.RFC3339Nano)+":"+state, "latency_setting"); err != nil {
			return err
		}
	}
	publicChanged := oldErr == nil && oldEnabled == 1 && newEnabled == 1 && (oldMode != string(server.LatencyProbeMode) || oldPublicTarget != string(server.LatencyProbePublicTarget))
	if publicChanged {
		if _, err := tx.ExecContext(ctx, `update server_telemetry set connectivity_available=-1,connectivity_latency_ms=0,connectivity_checked_at=null,connectivity_error='' where server_id=?`, server.ID); err != nil {
			return err
		}
		if _, err := insertConnectivityEvent(ctx, tx, model.ServerConnectivityEvent{ServerID: server.ID, Kind: model.ConnectivityEventProbeTargetChanged, Source: "latency_setting", EffectiveAt: updatedAt, EventKey: "latency-target:" + updatedAt.Format(time.RFC3339Nano) + ":" + string(server.LatencyProbeMode) + ":" + string(server.LatencyProbePublicTarget)}); err != nil {
			return err
		}
	}
	if newEnabled == 0 {
		if _, err := tx.ExecContext(ctx, `update server_telemetry set connectivity_available=-1,connectivity_latency_ms=0,connectivity_checked_at=null,connectivity_error='' where server_id=?`, server.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) migrateUnifiedLatencyProbeSettings(ctx context.Context) error {
	const key = "migration.controller-db-20260813-unified-latency-probes"
	var marker string
	if err := s.db.QueryRowContext(ctx, `select value from app_settings where key=?`, key).Scan(&marker); err == nil {
		return nil
	} else if err != sql.ErrNoRows {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `update server_latency_probe_settings set enabled=coalesce((select connectivity_probe_enabled from server_telemetry where server_id=server_latency_probe_settings.server_id),1),mode='tcp',public_target=coalesce((select connectivity_probe_target from server_telemetry where server_id=server_latency_probe_settings.server_id),'auto'),interval_seconds=60,regions_json='[]',provinces_json='[]',carriers_json='[]'`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into server_latency_probe_settings(server_id,enabled,mode,public_target,interval_seconds,sample_count,regions_json,max_targets,resource_version,updated_at) select s.id,coalesce(t.connectivity_probe_enabled,1),'tcp',coalesce(t.connectivity_probe_target,'auto'),60,3,'[]',64,'',? from servers s left join server_telemetry t on t.server_id=s.id where not exists(select 1 from server_latency_probe_settings l where l.server_id=s.id)`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?)`, key, "completed", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListLatencyProbeResults(ctx context.Context, serverID int64, limit int) ([]model.LatencyProbeResult, error) {
	if limit <= 0 || limit > 4096 {
		limit = 512
	}
	rows, err := s.db.QueryContext(ctx, `select probe_id,kind,mode,province,carrier,host,ip,port,available,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,checked_at from server_latency_probe_results where server_id=? order by checked_at desc,id desc limit ?`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.LatencyProbeResult{}
	for rows.Next() {
		var item model.LatencyProbeResult
		var available int
		var checked string
		if err := rows.Scan(&item.ProbeID, &item.Kind, &item.Mode, &item.Province, &item.Carrier, &item.Host, &item.IP, &item.Port, &available, &item.LatencyMS, &item.MinLatencyMS, &item.P95LatencyMS, &item.JitterMS, &item.SampleCount, &item.SuccessCount, &item.Error, &checked); err != nil {
			return nil, err
		}
		item.Available = available != 0
		item.CheckedAt = parseTime(checked)
		items = append(items, item)
	}
	return items, rows.Err()
}

const maxRegionalLatencyPointBuckets = 360

func (s *Store) ListRegionalLatencyPoints(ctx context.Context, serverID int64, from, to time.Time, bucket time.Duration) ([]model.ServerRegionalLatencyPoint, *time.Time, error) {
	from = from.UTC()
	to = to.UTC()
	if serverID <= 0 || from.IsZero() || to.IsZero() || !to.After(from) || bucket < time.Second {
		return nil, nil, errors.New("invalid regional latency point query")
	}
	bucketSeconds := int64(bucket / time.Second)
	bucketCount := int64((to.Sub(from) + bucket - 1) / bucket)
	if bucketSeconds <= 0 || bucketCount > maxRegionalLatencyPointBuckets {
		return nil, nil, errors.New("regional latency point query exceeds 360 buckets")
	}

	var dataStartText sql.NullString
	if err := s.db.QueryRowContext(ctx, `select min(checked_at) from server_latency_probe_results where server_id=? and kind='regional'`, serverID).Scan(&dataStartText); err != nil {
		return nil, nil, err
	}
	var dataStart *time.Time
	if dataStartText.Valid && dataStartText.String != "" {
		parsed := parseTime(dataStartText.String)
		dataStart = &parsed
	}

	fromText := from.Format(time.RFC3339Nano)
	rows, err := s.db.QueryContext(ctx, `
		with filtered as (
			select province,carrier,cast((unixepoch(checked_at)-unixepoch(?))/? as integer) as bucket_index,latency_ms
			from server_latency_probe_results
			where server_id=? and kind='regional' and available=1 and success_count>0 and latency_ms>0 and checked_at>=? and checked_at<?
		)
		select province,carrier,bucket_index,avg(latency_ms),min(latency_ms),max(latency_ms),count(*)
		from filtered
		where bucket_index>=0 and bucket_index<?
		group by province,carrier,bucket_index
		order by bucket_index,province,carrier`, fromText, bucketSeconds, serverID, fromText, to.Format(time.RFC3339Nano), bucketCount)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	points := make([]model.ServerRegionalLatencyPoint, 0)
	for rows.Next() {
		var point model.ServerRegionalLatencyPoint
		var bucketIndex int64
		if err := rows.Scan(&point.Province, &point.Carrier, &bucketIndex, &point.LatencyMS, &point.MinLatencyMS, &point.MaxLatencyMS, &point.Count); err != nil {
			return nil, nil, err
		}
		point.Kind = "regional"
		point.Available = true
		point.CheckedAt = from.Add(time.Duration(bucketIndex) * bucket)
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return points, dataStart, nil
}

func (s *Store) SaveLatencyProbeResults(ctx context.Context, serverID int64, report model.LatencyProbeResultReport) error {
	if serverID <= 0 || strings.TrimSpace(report.ReportID) == "" || strings.TrimSpace(report.ResourceVersion) == "" {
		return errors.New("latency probe result is missing server, report, or resource version")
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
		result, err := tx.ExecContext(ctx, `insert or ignore into server_latency_probe_results(server_id,report_id,resource_version,probe_id,kind,mode,province,carrier,host,ip,port,available,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,checked_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, serverID, report.ReportID, report.ResourceVersion, item.ProbeID, item.Kind, item.Mode, item.Province, item.Carrier, item.Host, item.IP, item.Port, boolInt(item.Available), item.LatencyMS, item.MinLatencyMS, item.P95LatencyMS, item.JitterMS, item.SampleCount, item.SuccessCount, item.Error, checkedAt.Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		inserted, _ := result.RowsAffected()
		if inserted == 1 && item.Kind == "public" {
			available := item.Available
			if _, err := insertConnectivityEvent(ctx, tx, model.ServerConnectivityEvent{
				ServerID:    serverID,
				Kind:        model.ConnectivityEventProbeResult,
				Available:   &available,
				LatencyMS:   int(item.LatencyMS),
				Error:       item.Error,
				Source:      "latency_probe",
				EffectiveAt: checkedAt,
				EventKey:    "latency:" + report.ReportID + ":" + item.ProbeID,
			}); err != nil {
				return err
			}
			var previousChecked sql.NullString
			if err := tx.QueryRowContext(ctx, `select connectivity_checked_at from server_telemetry where server_id=?`, serverID).Scan(&previousChecked); err != nil && err != sql.ErrNoRows {
				return err
			}
			if !previousChecked.Valid || checkedAt.After(parseTime(previousChecked.String)) {
				currentAvailable := boolInt(item.Available)
				updateCurrent := true
				var connectionKind, connectionAt string
				connectionErr := tx.QueryRowContext(ctx, `select kind,effective_at from server_connectivity_events where server_id=? and kind in (?,?) order by effective_at desc,id desc limit 1`, serverID, model.ConnectivityEventControllerConnected, model.ConnectivityEventControllerDisconnected).Scan(&connectionKind, &connectionAt)
				if connectionErr != nil && connectionErr != sql.ErrNoRows {
					return connectionErr
				}
				if connectionErr == nil {
					if model.ConnectivityEventKind(connectionKind) == model.ConnectivityEventControllerConnected {
						currentAvailable = 1
					} else if !checkedAt.After(parseTime(connectionAt)) {
						updateCurrent = false
					}
				}
				if updateCurrent {
					_, err = tx.ExecContext(ctx, `update server_telemetry set connectivity_available=?,connectivity_latency_ms=?,connectivity_checked_at=?,connectivity_error=?,updated_at=? where server_id=?`, currentAvailable, item.LatencyMS, checkedAt.Format(time.RFC3339Nano), item.Error, time.Now().UTC().Format(time.RFC3339Nano), serverID)
				}
				if err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteLatencyProbeResults(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from server_latency_probe_results where server_id=?`, serverID)
	return err
}
