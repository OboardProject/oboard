package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	trafficAcceptAccepted      = "accepted"
	trafficAcceptDuplicate     = "duplicate"
	trafficAcceptCovered       = "covered"
	trafficAcceptGap           = "checkpoint_gap"
	trafficAcceptOverlap       = "checkpoint_overlap"
	trafficAcceptEpochConflict = "epoch_conflict"
	trafficAcceptRejected      = "rejected"

	trafficStreamHealthy         = "healthy"
	trafficStreamStale           = "stale"
	trafficLeaseActive           = "active"
	trafficLeaseReleased         = "released"
	trafficLeaseExpiredUnsettled = "expired_unsettled"
)

type TrafficLedgerCommit struct {
	ServerID        int64
	AgentInstanceID string
	Periods         map[int64]model.TrafficPeriod
	Streams         []model.TrafficStreamObservation
	Reports         []model.TrafficReport
}

type TrafficLedgerResult struct {
	StreamCheckpoints []model.TrafficStreamCheckpoint
	AcceptedReports   []model.TrafficAcceptedReport
}

func (s *Store) migrateTrafficLedgerV2(ctx context.Context) error {
	reportColumns := []struct{ name, sql string }{
		{"protocol_version", `alter table traffic_reports add column protocol_version integer not null default 1`},
		{"counter_source", `alter table traffic_reports add column counter_source text not null default ''`},
		{"stream_id", `alter table traffic_reports add column stream_id text not null default ''`},
		{"counter_epoch", `alter table traffic_reports add column counter_epoch text not null default ''`},
		{"from_upload_bytes", `alter table traffic_reports add column from_upload_bytes integer not null default 0`},
		{"to_upload_bytes", `alter table traffic_reports add column to_upload_bytes integer not null default 0`},
		{"from_download_bytes", `alter table traffic_reports add column from_download_bytes integer not null default 0`},
		{"to_download_bytes", `alter table traffic_reports add column to_download_bytes integer not null default 0`},
		{"accept_status", `alter table traffic_reports add column accept_status text not null default ''`},
	}
	for _, column := range reportColumns {
		if err := s.ensureColumn(ctx, "traffic_reports", column.name, column.sql); err != nil {
			return err
		}
	}
	leaseColumns := []struct{ name, sql string }{
		{"lease_revision", `alter table traffic_leases add column lease_revision integer not null default 1`},
		{"state", `alter table traffic_leases add column state text not null default 'active'`},
		{"issued_at", `alter table traffic_leases add column issued_at text not null default ''`},
		{"last_synced_at", `alter table traffic_leases add column last_synced_at text not null default ''`},
		{"valid_until", `alter table traffic_leases add column valid_until text not null default ''`},
		{"released_at", `alter table traffic_leases add column released_at text`},
	}
	for _, column := range leaseColumns {
		if err := s.ensureColumn(ctx, "traffic_leases", column.name, column.sql); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `create table if not exists traffic_counter_streams (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, user_id integer not null references users(id) on delete cascade, counter_source text not null, stream_id text not null, counter_epoch text not null, period_key text not null, inbound_id integer not null default 0, path_id integer not null default 0, accepted_upload_bytes integer not null default 0, accepted_download_bytes integer not null default 0, status text not null default 'healthy', last_error text not null default '', agent_instance_id text not null default '', first_seen_at text not null, last_seen_at text not null, updated_at text not null, unique(server_id,counter_source,stream_id,counter_epoch,period_key))`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create index if not exists idx_traffic_counter_streams_lookup on traffic_counter_streams(server_id,user_id,period_key,last_seen_at)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create table if not exists traffic_reconciliation_events (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, user_id integer not null references users(id) on delete cascade, source text not null default '', stream_id text not null default '', counter_epoch text not null default '', period_key text not null default '', kind text not null, detail text not null default '', created_at text not null, resolved_at text)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create index if not exists idx_traffic_reconciliation_open on traffic_reconciliation_events(user_id,server_id,created_at desc)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `drop index if exists idx_traffic_reports_v2_range`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create unique index if not exists idx_traffic_reports_range on traffic_reports(server_id, counter_source, stream_id, counter_epoch, period_key, from_upload_bytes, to_upload_bytes, from_download_bytes, to_download_bytes) where stream_id <> ''`); err != nil {
		return err
	}
	return nil
}

func (s *Store) AllocateTrafficLease(ctx context.Context, serverID, userID int64, periodKey string, limitBytes, usedBytes int64) (TrafficLeaseAllocation, error) {
	return s.EnsureTrafficLeaseAllocation(ctx, serverID, userID, periodKey, limitBytes, usedBytes)
}

func (s *Store) GetTrafficLease(ctx context.Context, serverID, userID int64, periodKey string) (model.TrafficLease, error) {
	var item model.TrafficLease
	var issued, synced, validUntil, released, updated sql.NullString
	err := s.db.QueryRowContext(ctx, `select id,server_id,user_id,period_key,lease_bytes,consumed_bytes,coalesce(lease_revision,1),coalesce(nullif(state,''),'active'),issued_at,last_synced_at,valid_until,released_at,updated_at from traffic_leases where server_id=? and user_id=? and period_key=?`, serverID, userID, periodKey).Scan(&item.ID, &item.ServerID, &item.UserID, &item.PeriodKey, &item.LeaseBytes, &item.ConsumedBytes, &item.LeaseRevision, &item.State, &issued, &synced, &validUntil, &released, &updated)
	if err != nil {
		return model.TrafficLease{}, err
	}
	item.IssuedAt = parseTime(issued.String)
	item.LastSyncedAt = parseTime(synced.String)
	if validUntil.Valid && strings.TrimSpace(validUntil.String) != "" {
		t := parseTime(validUntil.String)
		item.ValidUntil = &t
	}
	if released.Valid && strings.TrimSpace(released.String) != "" {
		t := parseTime(released.String)
		item.ReleasedAt = &t
	}
	item.UpdatedAt = parseTime(updated.String)
	return item, nil
}

func (s *Store) ReleaseTrafficLease(ctx context.Context, serverID, userID int64, periodKey, detail string) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update traffic_leases set state=?, released_at=?, updated_at=? where server_id=? and user_id=? and period_key=? and coalesce(nullif(state,''),'active')<>?`, trafficLeaseReleased, ts, ts, serverID, userID, periodKey, trafficLeaseReleased)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil
	}
	return s.InsertTrafficReconciliationEvent(ctx, model.TrafficReconciliationEvent{
		ServerID: serverID, UserID: userID, PeriodKey: periodKey, Kind: "forced_lease_release", Detail: strings.TrimSpace(detail),
	})
}

func (s *Store) ExpireStaleTrafficLeases(ctx context.Context) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `update traffic_leases set state=?, updated_at=? where coalesce(nullif(state,''),'active')=? and valid_until<>'' and valid_until<?`, trafficLeaseExpiredUnsettled, ts, trafficLeaseActive, ts)
	return err
}

type trafficTx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) CommitTrafficLedger(ctx context.Context, commit TrafficLedgerCommit) (TrafficLedgerResult, error) {
	var result TrafficLedgerResult
	if commit.ServerID <= 0 {
		return result, errors.New("server id is required")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return result, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `begin immediate`); err != nil {
		return result, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
	}()
	tx := trafficTx(conn)
	ts := now()
	seen := map[string]model.TrafficStreamCheckpoint{}
	for _, observation := range commit.Streams {
		checkpoint, err := upsertTrafficStreamTx(tx, ctx, commit.ServerID, commit.AgentInstanceID, observation, ts)
		if err != nil {
			return result, err
		}
		key := trafficStreamKey(observation.Source, observation.StreamID, observation.CounterEpoch, observation.PeriodKey)
		seen[key] = checkpoint
		if observation.Status == "state_corrupt" || observation.Status == "counter_regression" || observation.Status == trafficAcceptGap || observation.Status == trafficAcceptOverlap || observation.Status == trafficAcceptEpochConflict {
			event := model.TrafficReconciliationEvent{
				ServerID: commit.ServerID, UserID: observation.UserID, Source: observation.Source, StreamID: observation.StreamID,
				CounterEpoch: observation.CounterEpoch, PeriodKey: observation.PeriodKey, Kind: observation.Status, Detail: observation.Status,
			}
			if err := insertTrafficReconciliationEventTx(tx, ctx, event, ts); err != nil {
				return result, err
			}
			checkpoint.Status = observation.Status
			seen[key] = checkpoint
		}
	}
	touchedUsers := map[int64]string{}
	for _, report := range commit.Reports {
		accepted, checkpoint, err := commitTrafficReportTx(tx, ctx, commit.ServerID, commit.AgentInstanceID, report, commit.Periods[report.UserID], ts)
		if err != nil {
			return result, err
		}
		result.AcceptedReports = append(result.AcceptedReports, accepted)
		key := trafficStreamKey(report.CounterSource, report.StreamID, report.CounterEpoch, report.PeriodKey)
		seen[key] = checkpoint
		if accepted.Status == trafficAcceptAccepted {
			touchedUsers[report.UserID] = report.PeriodKey
		}
		if accepted.Status == trafficAcceptGap || accepted.Status == trafficAcceptOverlap || accepted.Status == trafficAcceptEpochConflict {
			if err := insertTrafficReconciliationEventTx(tx, ctx, model.TrafficReconciliationEvent{
				ServerID: commit.ServerID, UserID: report.UserID, Source: report.CounterSource, StreamID: report.StreamID,
				CounterEpoch: report.CounterEpoch, PeriodKey: report.PeriodKey, Kind: accepted.Status, Detail: accepted.Status,
			}, ts); err != nil {
				return result, err
			}
		}
	}
	for userID, periodKey := range touchedUsers {
		if _, err := tx.ExecContext(ctx, `update users set traffic_used_bytes=(select coalesce(upload_bytes+download_bytes,0) from traffic_periods where user_id=? and period_key=?), updated_at=? where id=?`, userID, periodKey, ts, userID); err != nil {
			return result, err
		}
	}
	if _, err := conn.ExecContext(ctx, `commit`); err != nil {
		return result, err
	}
	committed = true
	for _, checkpoint := range seen {
		result.StreamCheckpoints = append(result.StreamCheckpoints, checkpoint)
	}
	return result, nil
}

func commitTrafficReportTx(tx trafficTx, ctx context.Context, serverID int64, agentInstanceID string, report model.TrafficReport, period model.TrafficPeriod, ts string) (model.TrafficAcceptedReport, model.TrafficStreamCheckpoint, error) {
	out := model.TrafficAcceptedReport{
		ReportID: report.ReportID, StreamID: report.StreamID, CounterEpoch: report.CounterEpoch, PeriodKey: report.PeriodKey,
	}
	if strings.TrimSpace(report.ReportID) == "" || strings.TrimSpace(report.StreamID) == "" || strings.TrimSpace(report.CounterEpoch) == "" || strings.TrimSpace(report.PeriodKey) == "" || strings.TrimSpace(report.CounterSource) == "" || report.UserID <= 0 {
		out.Status = trafficAcceptRejected
		return out, model.TrafficStreamCheckpoint{}, nil
	}
	if report.ToUploadBytes < report.FromUploadBytes || report.ToDownloadBytes < report.FromDownloadBytes {
		out.Status = trafficAcceptRejected
		return out, model.TrafficStreamCheckpoint{}, nil
	}
	var existingID string
	err := tx.QueryRowContext(ctx, `select report_id from traffic_reports where report_id=?`, report.ReportID).Scan(&existingID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, model.TrafficStreamCheckpoint{}, err
	}
	stream, err := loadOrCreateTrafficStreamTx(tx, ctx, serverID, agentInstanceID, report, ts)
	if err != nil {
		return out, model.TrafficStreamCheckpoint{}, err
	}
	out.AcceptedUpload = stream.AcceptedUploadBytes
	out.AcceptedDownload = stream.AcceptedDownloadBytes
	checkpoint := streamCheckpoint(stream)
	if existingID != "" {
		out.Status = trafficAcceptDuplicate
		return out, checkpoint, nil
	}
	var coveredID string
	coverErr := tx.QueryRowContext(ctx, `select report_id from traffic_reports where server_id=? and counter_source=? and stream_id=? and counter_epoch=? and period_key=? and from_upload_bytes=? and to_upload_bytes=? and from_download_bytes=? and to_download_bytes=? and stream_id<>''`, serverID, report.CounterSource, report.StreamID, report.CounterEpoch, report.PeriodKey, report.FromUploadBytes, report.ToUploadBytes, report.FromDownloadBytes, report.ToDownloadBytes).Scan(&coveredID)
	if coverErr != nil && !errors.Is(coverErr, sql.ErrNoRows) {
		return out, checkpoint, coverErr
	}
	if coveredID != "" {
		out.Status = trafficAcceptCovered
		return out, checkpoint, nil
	}
	status := validateTrafficCheckpoint(report.FromUploadBytes, report.ToUploadBytes, report.FromDownloadBytes, report.ToDownloadBytes, stream.AcceptedUploadBytes, stream.AcceptedDownloadBytes)
	out.Status = status
	if status != trafficAcceptAccepted {
		if status == trafficAcceptCovered {
			out.AcceptedUpload = stream.AcceptedUploadBytes
			out.AcceptedDownload = stream.AcceptedDownloadBytes
		}
		checkpoint.Status = status
		if _, err := tx.ExecContext(ctx, `update traffic_counter_streams set status=?, last_error=?, last_seen_at=?, updated_at=? where id=?`, status, status, ts, ts, stream.ID); err != nil {
			return out, checkpoint, err
		}
		checkpoint.LastError = status
		return out, checkpoint, nil
	}
	deltaUpload := report.ToUploadBytes - report.FromUploadBytes
	deltaDownload := report.ToDownloadBytes - report.FromDownloadBytes
	report.Upload = deltaUpload
	report.Download = deltaDownload
	if period.PeriodKey == "" {
		period.PeriodKey = report.PeriodKey
		period.UserID = report.UserID
	}
	if period.StartedAt.IsZero() {
		period.StartedAt = report.StartedAt
	}
	if period.EndsAt.IsZero() {
		period.EndsAt = report.EndedAt
	}
	if _, err := tx.ExecContext(ctx, `insert into traffic_periods(user_id,period_key,started_at,ends_at,upload_bytes,download_bytes,traffic_limit_bytes,state,updated_at) values(?,?,?,?,0,0,?,?,?) on conflict(user_id,period_key) do update set started_at=excluded.started_at,ends_at=excluded.ends_at,traffic_limit_bytes=excluded.traffic_limit_bytes,updated_at=excluded.updated_at`, period.UserID, period.PeriodKey, period.StartedAt.Format(time.RFC3339Nano), period.EndsAt.Format(time.RFC3339Nano), period.Limit, periodState(period.Upload+period.Download, period.Limit), ts); err != nil {
		return out, checkpoint, err
	}
	if _, err := tx.ExecContext(ctx, `insert into traffic_reports(report_id,server_id,user_id,inbound_id,path_id,period_key,upload_bytes,download_bytes,started_at,ended_at,created_at,protocol_version,counter_source,stream_id,counter_epoch,from_upload_bytes,to_upload_bytes,from_download_bytes,to_download_bytes,accept_status) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, report.ReportID, serverID, report.UserID, report.InboundID, report.PathID, report.PeriodKey, deltaUpload, deltaDownload, report.StartedAt.Format(time.RFC3339Nano), report.EndedAt.Format(time.RFC3339Nano), ts, 1, report.CounterSource, report.StreamID, report.CounterEpoch, report.FromUploadBytes, report.ToUploadBytes, report.FromDownloadBytes, report.ToDownloadBytes, trafficAcceptAccepted); err != nil {
		if isUniqueConstraint(err) {
			out.Status = trafficAcceptCovered
			return out, checkpoint, nil
		}
		return out, checkpoint, err
	}
	if _, err := tx.ExecContext(ctx, `insert into traffic_stats(server_id,user_id,inbound_id,upload_bytes,download_bytes,created_at) values(?,?,?,?,?,?)`, serverID, report.UserID, report.InboundID, deltaUpload, deltaDownload, ts); err != nil {
		return out, checkpoint, err
	}
	if _, err := tx.ExecContext(ctx, `update traffic_periods set upload_bytes=upload_bytes+?, download_bytes=download_bytes+?, state=case when traffic_limit_bytes>0 and upload_bytes+download_bytes+?+?>=traffic_limit_bytes then 'quota_exceeded' else 'active' end, updated_at=? where user_id=? and period_key=?`, deltaUpload, deltaDownload, deltaUpload, deltaDownload, ts, report.UserID, report.PeriodKey); err != nil {
		return out, checkpoint, err
	}
	if _, err := tx.ExecContext(ctx, `insert into traffic_leases(server_id,user_id,period_key,lease_bytes,consumed_bytes,updated_at) values(?,?,?,0,?,?) on conflict(server_id,user_id,period_key) do update set consumed_bytes=consumed_bytes+excluded.consumed_bytes, updated_at=excluded.updated_at`, serverID, report.UserID, report.PeriodKey, deltaUpload+deltaDownload, ts); err != nil {
		return out, checkpoint, err
	}
	if _, err := tx.ExecContext(ctx, `update traffic_counter_streams set accepted_upload_bytes=?, accepted_download_bytes=?, status=?, last_error='', last_seen_at=?, updated_at=? where id=?`, report.ToUploadBytes, report.ToDownloadBytes, trafficStreamHealthy, ts, ts, stream.ID); err != nil {
		return out, checkpoint, err
	}
	out.AcceptedUpload = report.ToUploadBytes
	out.AcceptedDownload = report.ToDownloadBytes
	out.Status = trafficAcceptAccepted
	checkpoint.AcceptedUpload = report.ToUploadBytes
	checkpoint.AcceptedDownload = report.ToDownloadBytes
	checkpoint.Status = trafficStreamHealthy
	checkpoint.LastError = ""
	return out, checkpoint, nil
}

func validateTrafficCheckpoint(fromUp, toUp, fromDown, toDown, acceptedUp, acceptedDown int64) string {
	if toUp <= acceptedUp && toDown <= acceptedDown {
		return trafficAcceptCovered
	}
	if fromUp == acceptedUp && fromDown == acceptedDown && toUp >= fromUp && toDown >= fromDown {
		return trafficAcceptAccepted
	}
	if (fromUp < acceptedUp && acceptedUp < toUp) || (fromDown < acceptedDown && acceptedDown < toDown) {
		return trafficAcceptOverlap
	}
	if fromUp > acceptedUp || fromDown > acceptedDown {
		return trafficAcceptGap
	}
	if fromUp < acceptedUp || fromDown < acceptedDown {
		return trafficAcceptOverlap
	}
	return trafficAcceptRejected
}

func loadOrCreateTrafficStreamTx(tx trafficTx, ctx context.Context, serverID int64, agentInstanceID string, report model.TrafficReport, ts string) (model.TrafficCounterStream, error) {
	inboundID, pathID := optionalInt64(report.InboundID), optionalInt64(report.PathID)
	obs := model.TrafficStreamObservation{
		Source: report.CounterSource, StreamID: report.StreamID, CounterEpoch: report.CounterEpoch, PeriodKey: report.PeriodKey,
		UserID: report.UserID, InboundID: inboundID, PathID: pathID,
	}
	return upsertTrafficStreamTxReturn(tx, ctx, serverID, agentInstanceID, obs, ts)
}

func upsertTrafficStreamTx(tx trafficTx, ctx context.Context, serverID int64, agentInstanceID string, observation model.TrafficStreamObservation, ts string) (model.TrafficStreamCheckpoint, error) {
	stream, err := upsertTrafficStreamTxReturn(tx, ctx, serverID, agentInstanceID, observation, ts)
	if err != nil {
		return model.TrafficStreamCheckpoint{}, err
	}
	return streamCheckpoint(stream), nil
}

func upsertTrafficStreamTxReturn(tx trafficTx, ctx context.Context, serverID int64, agentInstanceID string, observation model.TrafficStreamObservation, ts string) (model.TrafficCounterStream, error) {
	var stream model.TrafficCounterStream
	if strings.TrimSpace(observation.StreamID) == "" || strings.TrimSpace(observation.CounterEpoch) == "" || strings.TrimSpace(observation.PeriodKey) == "" || strings.TrimSpace(observation.Source) == "" || observation.UserID <= 0 {
		return stream, errors.New("traffic stream identity is invalid")
	}
	status := strings.TrimSpace(observation.Status)
	if status == "" {
		status = trafficStreamHealthy
	}
	if _, err := tx.ExecContext(ctx, `insert into traffic_counter_streams(server_id,user_id,counter_source,stream_id,counter_epoch,period_key,inbound_id,path_id,accepted_upload_bytes,accepted_download_bytes,status,last_error,agent_instance_id,first_seen_at,last_seen_at,updated_at) values(?,?,?,?,?,?,?,?,0,0,?,?,?,?,?,?) on conflict(server_id,counter_source,stream_id,counter_epoch,period_key) do update set user_id=excluded.user_id, inbound_id=case when excluded.inbound_id>0 then excluded.inbound_id else traffic_counter_streams.inbound_id end, path_id=case when excluded.path_id>0 then excluded.path_id else traffic_counter_streams.path_id end, agent_instance_id=case when excluded.agent_instance_id='' then traffic_counter_streams.agent_instance_id else excluded.agent_instance_id end, last_seen_at=excluded.last_seen_at, updated_at=excluded.updated_at`, serverID, observation.UserID, observation.Source, observation.StreamID, observation.CounterEpoch, observation.PeriodKey, observation.InboundID, observation.PathID, status, "", agentInstanceID, ts, ts, ts); err != nil {
		return stream, err
	}
	var first, last, updated string
	if err := tx.QueryRowContext(ctx, `select id,server_id,user_id,counter_source,stream_id,counter_epoch,period_key,inbound_id,path_id,accepted_upload_bytes,accepted_download_bytes,status,last_error,agent_instance_id,first_seen_at,last_seen_at,updated_at from traffic_counter_streams where server_id=? and counter_source=? and stream_id=? and counter_epoch=? and period_key=?`, serverID, observation.Source, observation.StreamID, observation.CounterEpoch, observation.PeriodKey).Scan(&stream.ID, &stream.ServerID, &stream.UserID, &stream.CounterSource, &stream.StreamID, &stream.CounterEpoch, &stream.PeriodKey, &stream.InboundID, &stream.PathID, &stream.AcceptedUploadBytes, &stream.AcceptedDownloadBytes, &stream.Status, &stream.LastError, &stream.AgentInstanceID, &first, &last, &updated); err != nil {
		return stream, err
	}
	stream.FirstSeenAt = parseTime(first)
	stream.LastSeenAt = parseTime(last)
	stream.UpdatedAt = parseTime(updated)
	return inheritTrafficStreamCheckpointTx(tx, ctx, stream, ts)
}

func inheritTrafficStreamCheckpointTx(tx trafficTx, ctx context.Context, stream model.TrafficCounterStream, ts string) (model.TrafficCounterStream, error) {
	if stream.AcceptedUploadBytes != 0 || stream.AcceptedDownloadBytes != 0 {
		return stream, nil
	}
	var sourcePeriod string
	err := tx.QueryRowContext(ctx, `select source_period_key from traffic_period_transitions where user_id=? and target_period_key=? order by created_at desc limit 1`, stream.UserID, stream.PeriodKey).Scan(&sourcePeriod)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return stream, nil
		}
		return stream, err
	}
	if strings.TrimSpace(sourcePeriod) == "" || sourcePeriod == stream.PeriodKey {
		return stream, nil
	}
	var upload, download int64
	err = tx.QueryRowContext(ctx, `select accepted_upload_bytes, accepted_download_bytes from traffic_counter_streams where server_id=? and counter_source=? and stream_id=? and counter_epoch=? and period_key=?`, stream.ServerID, stream.CounterSource, stream.StreamID, stream.CounterEpoch, sourcePeriod).Scan(&upload, &download)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return stream, nil
		}
		return stream, err
	}
	if upload == 0 && download == 0 {
		return stream, nil
	}
	if _, err := tx.ExecContext(ctx, `update traffic_counter_streams set accepted_upload_bytes=?, accepted_download_bytes=?, updated_at=? where id=?`, upload, download, ts, stream.ID); err != nil {
		return stream, err
	}
	stream.AcceptedUploadBytes = upload
	stream.AcceptedDownloadBytes = download
	return stream, nil
}

func (s *Store) InsertTrafficReconciliationEvent(ctx context.Context, event model.TrafficReconciliationEvent) error {
	return insertTrafficReconciliationEventTx(s.db, ctx, event, now())
}

func insertTrafficReconciliationEventTx(exec interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, ctx context.Context, event model.TrafficReconciliationEvent, ts string) error {
	if strings.TrimSpace(event.Kind) == "" {
		return nil
	}
	_, err := exec.ExecContext(ctx, `insert into traffic_reconciliation_events(server_id,user_id,source,stream_id,counter_epoch,period_key,kind,detail,created_at) values(?,?,?,?,?,?,?,?,?)`, event.ServerID, event.UserID, event.Source, event.StreamID, event.CounterEpoch, event.PeriodKey, event.Kind, event.Detail, ts)
	return err
}

func (s *Store) ListTrafficReconciliationEvents(ctx context.Context, userID, serverID int64, periodKey, kind string, limit int) ([]model.TrafficReconciliationEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `select id,server_id,user_id,source,stream_id,counter_epoch,period_key,kind,detail,created_at,resolved_at from traffic_reconciliation_events where resolved_at is null`
	args := []any{}
	if userID > 0 {
		query += ` and user_id=?`
		args = append(args, userID)
	}
	if serverID > 0 {
		query += ` and server_id=?`
		args = append(args, serverID)
	}
	if strings.TrimSpace(periodKey) != "" {
		query += ` and period_key=?`
		args = append(args, periodKey)
	}
	if strings.TrimSpace(kind) != "" {
		query += ` and kind=?`
		args = append(args, kind)
	}
	query += ` order by id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TrafficReconciliationEvent{}
	for rows.Next() {
		var item model.TrafficReconciliationEvent
		var created string
		var resolved sql.NullString
		if err := rows.Scan(&item.ID, &item.ServerID, &item.UserID, &item.Source, &item.StreamID, &item.CounterEpoch, &item.PeriodKey, &item.Kind, &item.Detail, &created, &resolved); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		if resolved.Valid && strings.TrimSpace(resolved.String) != "" {
			t := parseTime(resolved.String)
			item.ResolvedAt = &t
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) MarkTrafficStreamsRecovering(ctx context.Context, userID, serverID int64, periodKey string) error {
	query := `update traffic_counter_streams set status='recovering', last_error='administrator requested reconciliation', updated_at=? where 1=1`
	args := []any{now()}
	if userID > 0 {
		query += ` and user_id=?`
		args = append(args, userID)
	}
	if serverID > 0 {
		query += ` and server_id=?`
		args = append(args, serverID)
	}
	if strings.TrimSpace(periodKey) != "" {
		query += ` and period_key=?`
		args = append(args, periodKey)
	}
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) GetTrafficLedger(ctx context.Context, userID, serverID int64, periodKey string) (model.TrafficLedgerView, error) {
	view := model.TrafficLedgerView{UserID: userID, Servers: []model.TrafficLedgerServer{}}
	if userID <= 0 {
		return view, errors.New("user id is required")
	}
	if strings.TrimSpace(periodKey) == "" {
		_ = s.db.QueryRowContext(ctx, `select period_key from traffic_periods where user_id=? order by updated_at desc, id desc limit 1`, userID).Scan(&periodKey)
	}
	view.Period.Key = periodKey
	if period, err := s.GetTrafficPeriod(ctx, userID, periodKey); err == nil {
		view.Period = model.TrafficLedgerPeriod{
			Key: period.PeriodKey, UploadBytes: period.Upload, DownloadBytes: period.Download,
			UsedBytes: period.Upload + period.Download, LimitBytes: period.Limit, State: period.State,
		}
	}
	query := `select id,server_id,user_id,counter_source,stream_id,counter_epoch,period_key,inbound_id,path_id,accepted_upload_bytes,accepted_download_bytes,status,last_error,agent_instance_id,first_seen_at,last_seen_at,updated_at from traffic_counter_streams where user_id=?`
	args := []any{userID}
	if serverID > 0 {
		query += ` and server_id=?`
		args = append(args, serverID)
	}
	if strings.TrimSpace(periodKey) != "" {
		query += ` and period_key=?`
		args = append(args, periodKey)
	}
	query += ` order by server_id, counter_source, stream_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return view, err
	}
	defer rows.Close()
	streamsByServer := map[int64][]model.TrafficCounterStream{}
	serverIDs := []int64{}
	seenServer := map[int64]bool{}
	for rows.Next() {
		var item model.TrafficCounterStream
		var first, last, updated string
		if err := rows.Scan(&item.ID, &item.ServerID, &item.UserID, &item.CounterSource, &item.StreamID, &item.CounterEpoch, &item.PeriodKey, &item.InboundID, &item.PathID, &item.AcceptedUploadBytes, &item.AcceptedDownloadBytes, &item.Status, &item.LastError, &item.AgentInstanceID, &first, &last, &updated); err != nil {
			return view, err
		}
		item.FirstSeenAt = parseTime(first)
		item.LastSeenAt = parseTime(last)
		item.UpdatedAt = parseTime(updated)
		if time.Since(item.LastSeenAt) > 10*time.Minute && item.Status == trafficStreamHealthy {
			item.Status = trafficStreamStale
		}
		streamsByServer[item.ServerID] = append(streamsByServer[item.ServerID], item)
		if !seenServer[item.ServerID] {
			seenServer[item.ServerID] = true
			serverIDs = append(serverIDs, item.ServerID)
		}
	}
	if err := rows.Err(); err != nil {
		return view, err
	}
	leaseQuery := `select id,server_id,user_id,period_key,lease_bytes,consumed_bytes,coalesce(lease_revision,1),coalesce(nullif(state,''),'active') from traffic_leases where user_id=?`
	leaseArgs := []any{userID}
	if serverID > 0 {
		leaseQuery += ` and server_id=?`
		leaseArgs = append(leaseArgs, serverID)
	}
	if strings.TrimSpace(periodKey) != "" {
		leaseQuery += ` and period_key=?`
		leaseArgs = append(leaseArgs, periodKey)
	}
	leaseRows, err := s.db.QueryContext(ctx, leaseQuery, leaseArgs...)
	if err != nil {
		return view, err
	}
	defer leaseRows.Close()
	leases := map[int64]model.TrafficLedgerLease{}
	for leaseRows.Next() {
		var lease model.TrafficLedgerLease
		var sid, uid int64
		var key, state string
		if err := leaseRows.Scan(&lease.LeaseID, &sid, &uid, &key, &lease.GrantedBytes, &lease.ConsumedBytes, &lease.Revision, &state); err != nil {
			return view, err
		}
		lease.State = state
		remaining := lease.GrantedBytes - lease.ConsumedBytes
		if remaining < 0 {
			remaining = 0
		}
		lease.RemainingBytes = remaining
		leases[sid] = lease
		if !seenServer[sid] {
			seenServer[sid] = true
			serverIDs = append(serverIDs, sid)
		}
	}
	names := map[int64]string{}
	if len(serverIDs) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(serverIDs)), ",")
		nameArgs := make([]any, 0, len(serverIDs))
		for _, id := range serverIDs {
			nameArgs = append(nameArgs, id)
		}
		nameRows, err := s.db.QueryContext(ctx, `select id,name from servers where id in (`+placeholders+`)`, nameArgs...)
		if err != nil {
			return view, err
		}
		for nameRows.Next() {
			var id int64
			var name string
			if err := nameRows.Scan(&id, &name); err != nil {
				nameRows.Close()
				return view, err
			}
			names[id] = name
		}
		if err := nameRows.Close(); err != nil {
			return view, err
		}
	}
	for _, id := range serverIDs {
		server := model.TrafficLedgerServer{ServerID: id, ServerName: names[id], Lease: leases[id], Streams: streamsByServer[id]}
		if server.Streams == nil {
			server.Streams = []model.TrafficCounterStream{}
		}
		server.Sync.Status = trafficStreamHealthy
		for _, stream := range server.Streams {
			if stream.LastSeenAt.After(server.Sync.LastSeenAt) {
				server.Sync.LastSeenAt = stream.LastSeenAt
			}
			switch stream.Status {
			case "counter_regression", trafficAcceptGap, trafficAcceptOverlap, trafficAcceptEpochConflict, "state_corrupt":
				server.Sync.Status = stream.Status
				server.Sync.LastError = stream.LastError
			case "recovering", trafficStreamStale:
				if server.Sync.Status == trafficStreamHealthy {
					server.Sync.Status = stream.Status
				}
			}
		}
		if server.Lease.State == "" {
			server.Lease.State = trafficLeaseActive
		}
		view.Servers = append(view.Servers, server)
	}
	issues, err := s.ListTrafficReconciliationEvents(ctx, userID, serverID, periodKey, "", 50)
	if err != nil {
		return view, err
	}
	view.Issues = issues
	return view, nil
}

func streamCheckpoint(stream model.TrafficCounterStream) model.TrafficStreamCheckpoint {
	return model.TrafficStreamCheckpoint{
		Source: stream.CounterSource, StreamID: stream.StreamID, CounterEpoch: stream.CounterEpoch, PeriodKey: stream.PeriodKey,
		AcceptedUpload: stream.AcceptedUploadBytes, AcceptedDownload: stream.AcceptedDownloadBytes, Status: stream.Status, LastError: stream.LastError,
	}
}

func trafficStreamKey(source, streamID, epoch, period string) string {
	return source + "\x00" + streamID + "\x00" + epoch + "\x00" + period
}

func optionalInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique") || strings.Contains(msg, "constraint failed")
}
