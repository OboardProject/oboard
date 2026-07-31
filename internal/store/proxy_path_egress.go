package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) ListProxyPathEgressResults(ctx context.Context) ([]model.ProxyPathEgressResult, error) {
	rows, err := s.db.QueryContext(ctx, `select path_id,external_outbound_id,owner_server_id,topology_fingerprint,config_version,task_id,status,last_exit_ip,last_region_code,geo_database_revision,last_error,last_attempt_at,last_success_at,created_at,updated_at from proxy_path_egress_results order by path_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProxyPathEgressResult{}
	for rows.Next() {
		var item model.ProxyPathEgressResult
		var taskID sql.NullInt64
		var attemptAt, successAt sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&item.PathID, &item.ExternalOutboundID, &item.OwnerServerID, &item.TopologyFingerprint, &item.ConfigVersion, &taskID, &item.Status, &item.LastExitIP, &item.LastRegionCode, &item.GeoDatabaseRevision, &item.LastError, &attemptAt, &successAt, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if taskID.Valid {
			item.TaskID = &taskID.Int64
		}
		item.LastAttemptAt = parseNullTime(attemptAt)
		item.LastSuccessAt = parseNullTime(successAt)
		item.CreatedAt = parseTime(createdAt)
		item.UpdatedAt = parseTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetProxyPathEgressResult(ctx context.Context, pathID int64) (*model.ProxyPathEgressResult, error) {
	items, err := s.ListProxyPathEgressResults(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].PathID == pathID {
			return &items[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *Store) MarkProxyPathEgressPending(ctx context.Context, target model.ExternalEgressProbeTarget, configVersion int64, taskID int64) error {
	return s.saveProxyPathEgressAttempt(ctx, target, configVersion, taskID, "pending", "", "", "", "", time.Now().UTC())
}

func (s *Store) SaveProxyPathEgressAttempt(ctx context.Context, target model.ExternalEgressProbeTarget, configVersion int64, taskID int64, status, exitIP, regionCode, geoRevision, lastError string, attemptedAt time.Time) error {
	return s.saveProxyPathEgressAttempt(ctx, target, configVersion, taskID, status, exitIP, regionCode, geoRevision, lastError, attemptedAt)
}

func (s *Store) saveProxyPathEgressAttempt(ctx context.Context, target model.ExternalEgressProbeTarget, configVersion int64, taskID int64, status, exitIP, regionCode, geoRevision, lastError string, attemptedAt time.Time) error {
	if target.PathID <= 0 || target.ExternalOutboundID <= 0 || target.OwnerServerID <= 0 || target.TopologyFingerprint == "" {
		return errors.New("invalid proxy path egress target")
	}
	if attemptedAt.IsZero() {
		attemptedAt = time.Now().UTC()
	}
	current, err := s.GetProxyPathEgressResult(ctx, target.PathID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	createdAt := attemptedAt
	lastExitIP, lastRegionCode, lastGeoRevision := "", "", ""
	var lastSuccessAt *time.Time
	if current != nil {
		createdAt = current.CreatedAt
		if current.TopologyFingerprint == target.TopologyFingerprint {
			lastExitIP = current.LastExitIP
			lastRegionCode = current.LastRegionCode
			lastGeoRevision = current.GeoDatabaseRevision
			lastSuccessAt = current.LastSuccessAt
		}
	}
	if status == "succeeded" {
		lastExitIP = exitIP
		lastRegionCode = regionCode
		lastGeoRevision = geoRevision
		value := attemptedAt
		lastSuccessAt = &value
		lastError = ""
	}
	var nullableTaskID any
	if taskID > 0 {
		nullableTaskID = taskID
	}
	_, err = s.db.ExecContext(ctx, `insert into proxy_path_egress_results(path_id,external_outbound_id,owner_server_id,topology_fingerprint,config_version,task_id,status,last_exit_ip,last_region_code,geo_database_revision,last_error,last_attempt_at,last_success_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(path_id) do update set external_outbound_id=excluded.external_outbound_id,owner_server_id=excluded.owner_server_id,topology_fingerprint=excluded.topology_fingerprint,config_version=excluded.config_version,task_id=excluded.task_id,status=excluded.status,last_exit_ip=excluded.last_exit_ip,last_region_code=excluded.last_region_code,geo_database_revision=excluded.geo_database_revision,last_error=excluded.last_error,last_attempt_at=excluded.last_attempt_at,last_success_at=excluded.last_success_at,updated_at=excluded.updated_at`, target.PathID, target.ExternalOutboundID, target.OwnerServerID, target.TopologyFingerprint, configVersion, nullableTaskID, status, lastExitIP, lastRegionCode, lastGeoRevision, lastError, attemptedAt.Format(time.RFC3339Nano), nilTime(lastSuccessAt), createdAt.Format(time.RFC3339Nano), attemptedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) UpdateProxyPathEgressGeography(ctx context.Context, pathID int64, regionCode, revision string) error {
	_, err := s.db.ExecContext(ctx, `update proxy_path_egress_results set last_region_code=?,geo_database_revision=?,updated_at=? where path_id=? and last_exit_ip<>''`, regionCode, revision, now(), pathID)
	return err
}
