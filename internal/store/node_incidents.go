package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

const nodeIncidentKindServerOffline = "server_offline"

type incidentRowScanner interface {
	Scan(...any) error
}

func scanNodeIncident(row incidentRowScanner) (model.NodeIncident, error) {
	var item model.NodeIncident
	var firstOffline, detected, created, updated string
	var recoveryCandidate, recoveryDeadline, recovered, resolved sql.NullString
	err := row.Scan(&item.ID, &item.ServerID, &item.ServerName, &item.Kind, &item.Status, &item.Version,
		&firstOffline, &detected, &recoveryCandidate, &recoveryDeadline, &recovered, &resolved,
		&item.OutageDurationSeconds, &item.OfflineThresholdSeconds, &item.RecoveryThresholdSeconds,
		&item.FlapCount, &item.SnapshotJSON, &created, &updated)
	if err != nil {
		return model.NodeIncident{}, err
	}
	item.FirstOfflineAt = parseTime(firstOffline)
	item.DetectedAt = parseTime(detected)
	item.RecoveryCandidateAt = parseNullTime(recoveryCandidate)
	item.RecoveryDeadlineAt = parseNullTime(recoveryDeadline)
	item.RecoveredAt = parseNullTime(recovered)
	item.ResolvedAt = parseNullTime(resolved)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return item, nil
}

const nodeIncidentSelect = `select id,server_id,server_name,kind,status,version,first_offline_at,detected_at,recovery_candidate_at,recovery_deadline_at,recovered_at,resolved_at,outage_duration_seconds,offline_threshold_seconds,recovery_threshold_seconds,flap_count,snapshot_json,created_at,updated_at from node_incidents`

// OpenOrReopenNodeIncident creates one outage event per server, or moves a
// recovering event back to active when the server flaps before its deadline.
func (s *Store) OpenOrReopenNodeIncident(ctx context.Context, server model.Server, firstOfflineAt, detectedAt time.Time, offlineThreshold, recoveryThreshold time.Duration, snapshotJSON string) (model.NodeIncident, bool, error) {
	if server.ID <= 0 {
		return model.NodeIncident{}, false, errors.New("node incident requires a server")
	}
	if strings.TrimSpace(snapshotJSON) == "" {
		snapshotJSON = "{}"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.NodeIncident{}, false, err
	}
	defer tx.Rollback()
	item, err := scanNodeIncident(tx.QueryRowContext(ctx, nodeIncidentSelect+` where server_id=? and kind=? and status in ('active','recovering') order by id desc limit 1`, server.ID, nodeIncidentKindServerOffline))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.NodeIncident{}, false, err
	}
	ts := detectedAt.UTC().Format(time.RFC3339Nano)
	if errors.Is(err, sql.ErrNoRows) {
		res, insertErr := tx.ExecContext(ctx, `insert into node_incidents(server_id,server_name,kind,status,version,first_offline_at,detected_at,offline_threshold_seconds,recovery_threshold_seconds,snapshot_json,created_at,updated_at) values(?,?,?,'active',1,?,?,?,?,?,?,?)`,
			server.ID, server.Name, nodeIncidentKindServerOffline, firstOfflineAt.UTC().Format(time.RFC3339Nano), ts, int(offlineThreshold.Seconds()), int(recoveryThreshold.Seconds()), snapshotJSON, ts, ts)
		if insertErr != nil {
			return model.NodeIncident{}, false, insertErr
		}
		id, _ := res.LastInsertId()
		item, err = scanNodeIncident(tx.QueryRowContext(ctx, nodeIncidentSelect+` where id=?`, id))
		if err != nil {
			return model.NodeIncident{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return model.NodeIncident{}, false, err
		}
		return item, true, nil
	}
	if item.Status == model.NodeIncidentRecovering {
		_, err = tx.ExecContext(ctx, `update node_incidents set status='active',version=version+1,recovery_candidate_at=null,recovery_deadline_at=null,recovered_at=null,resolved_at=null,outage_duration_seconds=0,flap_count=flap_count+1,snapshot_json=?,updated_at=? where id=? and version=?`, snapshotJSON, ts, item.ID, item.Version)
		if err != nil {
			return model.NodeIncident{}, false, err
		}
		item, err = scanNodeIncident(tx.QueryRowContext(ctx, nodeIncidentSelect+` where id=?`, item.ID))
		if err != nil {
			return model.NodeIncident{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.NodeIncident{}, false, err
	}
	return item, false, nil
}

func (s *Store) MarkNodeIncidentRecovering(ctx context.Context, serverID int64, candidateAt time.Time, recoveryThreshold time.Duration) (*model.NodeIncident, error) {
	deadline := candidateAt.UTC().Add(recoveryThreshold)
	res, err := s.db.ExecContext(ctx, `update node_incidents set status='recovering',version=version+1,recovery_candidate_at=?,recovery_deadline_at=?,recovered_at=?,recovery_threshold_seconds=?,updated_at=? where server_id=? and kind=? and status='active'`,
		candidateAt.UTC().Format(time.RFC3339Nano), deadline.Format(time.RFC3339Nano), candidateAt.UTC().Format(time.RFC3339Nano), int(recoveryThreshold.Seconds()), now(), serverID, nodeIncidentKindServerOffline)
	if err != nil {
		return nil, err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return nil, nil
	}
	item, err := scanNodeIncident(s.db.QueryRowContext(ctx, nodeIncidentSelect+` where server_id=? and kind=? and status='recovering'`, serverID, nodeIncidentKindServerOffline))
	return &item, err
}

func (s *Store) ListDueRecoveringNodeIncidents(ctx context.Context, at time.Time) ([]model.NodeIncident, error) {
	rows, err := s.db.QueryContext(ctx, nodeIncidentSelect+` where status='recovering' and recovery_deadline_at is not null and recovery_deadline_at<=? order by recovery_deadline_at`, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NodeIncident{}
	for rows.Next() {
		item, err := scanNodeIncident(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ResolveNodeIncident(ctx context.Context, incidentID, expectedVersion int64, resolvedAt time.Time) (*model.NodeIncident, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	item, err := scanNodeIncident(tx.QueryRowContext(ctx, nodeIncidentSelect+` where id=?`, incidentID))
	if err != nil {
		return nil, err
	}
	if item.Status != model.NodeIncidentRecovering || item.Version != expectedVersion || item.RecoveryCandidateAt == nil {
		return nil, errors.New("node incident recovery state changed")
	}
	duration := int64(item.RecoveryCandidateAt.Sub(item.FirstOfflineAt).Seconds())
	if duration < 0 {
		duration = 0
	}
	ts := resolvedAt.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `update node_incidents set status='resolved',version=version+1,resolved_at=?,outage_duration_seconds=?,updated_at=? where id=? and version=? and status='recovering'`, ts, duration, ts, incidentID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return nil, errors.New("node incident recovery state changed")
	}
	if _, err := tx.ExecContext(ctx, `update node_publication_isolations set status='restored',restored_at=?,updated_at=? where incident_id=? and status='hidden' and recovery_policy='auto'`, ts, ts, incidentID); err != nil {
		return nil, err
	}
	item, err = scanNodeIncident(tx.QueryRowContext(ctx, nodeIncidentSelect+` where id=?`, incidentID))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) GetNodeIncident(ctx context.Context, id int64) (*model.NodeIncident, error) {
	item, err := scanNodeIncident(s.db.QueryRowContext(ctx, nodeIncidentSelect+` where id=?`, id))
	return &item, err
}

func (s *Store) ListNodeIncidents(ctx context.Context, status string, limit, offset int) ([]model.NodeIncident, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	query := nodeIncidentSelect
	args := []any{}
	if status != "" {
		query += ` where status=?`
		args = append(args, status)
	}
	query += ` order by id desc limit ? offset ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NodeIncident{}
	for rows.Next() {
		item, err := scanNodeIncident(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) EnsureNodeIncidentTelegramMessage(ctx context.Context, incidentID, channelID, chatID int64) (*model.NodeIncidentTelegramMessage, error) {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into node_incident_telegram_messages(incident_id,channel_id,chat_id,created_at,updated_at) values(?,?,?,?,?) on conflict(incident_id,chat_id) do update set channel_id=coalesce(node_incident_telegram_messages.channel_id,excluded.channel_id),updated_at=excluded.updated_at`, incidentID, channelID, chatID, ts, ts)
	if err != nil {
		return nil, err
	}
	return s.GetNodeIncidentTelegramMessage(ctx, incidentID, chatID)
}

func (s *Store) GetNodeIncidentTelegramMessage(ctx context.Context, incidentID, chatID int64) (*model.NodeIncidentTelegramMessage, error) {
	var item model.NodeIncidentTelegramMessage
	var channel sql.NullInt64
	var edited sql.NullString
	var created, updated string
	err := s.db.QueryRowContext(ctx, `select id,incident_id,channel_id,chat_id,message_id,fallback_message_id,last_event_version,last_edited_at,last_error,created_at,updated_at from node_incident_telegram_messages where incident_id=? and chat_id=?`, incidentID, chatID).Scan(&item.ID, &item.IncidentID, &channel, &item.ChatID, &item.MessageID, &item.FallbackMessageID, &item.LastEventVersion, &edited, &item.LastError, &created, &updated)
	if err != nil {
		return nil, err
	}
	if channel.Valid {
		item.ChannelID = &channel.Int64
	}
	item.LastEditedAt = parseNullTime(edited)
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return &item, nil
}

func (s *Store) UpdateNodeIncidentTelegramMessage(ctx context.Context, id, messageID, fallbackMessageID, eventVersion int64, editErr error) error {
	ts := now()
	errorText := ""
	if editErr != nil {
		errorText = editErr.Error()
		if len(errorText) > 1000 {
			errorText = errorText[:1000]
		}
	}
	_, err := s.db.ExecContext(ctx, `update node_incident_telegram_messages set message_id=case when ?>0 then ? else message_id end,fallback_message_id=case when ?>0 then ? else fallback_message_id end,last_event_version=?,last_edited_at=?,last_error=?,updated_at=? where id=?`, messageID, messageID, fallbackMessageID, fallbackMessageID, eventVersion, ts, errorText, ts, id)
	return err
}

func (s *Store) CreateNodeIncidentAction(ctx context.Context, item *model.NodeIncidentAction) error {
	if item == nil || item.IncidentID <= 0 || item.ActorUserID <= 0 || strings.TrimSpace(item.Kind) == "" {
		return errors.New("invalid node incident action")
	}
	if item.Status == "" {
		item.Status = "deployment_pending"
	}
	ts := now()
	item.CreatedAt = parseTime(ts)
	item.UpdatedAt = item.CreatedAt
	var completedAt any
	if item.Status != "deployment_pending" {
		completedAt = ts
		completed := item.CreatedAt
		item.CompletedAt = &completed
	}
	res, err := s.db.ExecContext(ctx, `insert into node_incident_actions(incident_id,actor_user_id,kind,status,inbound_ids_json,changeset_id,config_version,task_count,error,created_at,completed_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, item.IncidentID, item.ActorUserID, item.Kind, item.Status, item.InboundIDsJSON, item.ChangesetID, item.ConfigVersion, item.TaskCount, item.Error, ts, completedAt, ts)
	if err != nil {
		return err
	}
	item.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) ListNodeIncidentActions(ctx context.Context, incidentID int64) ([]model.NodeIncidentAction, error) {
	query := `select id,incident_id,actor_user_id,kind,status,inbound_ids_json,changeset_id,config_version,task_count,error,created_at,completed_at,updated_at from node_incident_actions`
	args := []any{}
	if incidentID > 0 {
		query += ` where incident_id=?`
		args = append(args, incidentID)
	}
	query += ` order by id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NodeIncidentAction{}
	for rows.Next() {
		var item model.NodeIncidentAction
		var created, updated string
		var completed sql.NullString
		if err := rows.Scan(&item.ID, &item.IncidentID, &item.ActorUserID, &item.Kind, &item.Status, &item.InboundIDsJSON, &item.ChangesetID, &item.ConfigVersion, &item.TaskCount, &item.Error, &created, &completed, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.CompletedAt = parseNullTime(completed)
		item.UpdatedAt = parseTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ReconcileNodeIncidentActions(ctx context.Context) ([]model.NodeIncidentAction, error) {
	items, err := s.ListNodeIncidentActions(ctx, 0)
	if err != nil {
		return nil, err
	}
	completed := []model.NodeIncidentAction{}
	for _, item := range items {
		if item.Status != "deployment_pending" {
			continue
		}
		rows, err := s.db.QueryContext(ctx, `select status from agent_tasks where config_version=? and type=? order by id`, item.ConfigVersion, model.AgentTaskTypeApplyDeployment)
		if err != nil {
			return nil, err
		}
		total, succeeded := 0, 0
		failure := ""
		for rows.Next() {
			var status string
			if err := rows.Scan(&status); err != nil {
				rows.Close()
				return nil, err
			}
			total++
			switch status {
			case "succeeded":
				succeeded++
			case "failed", "rollback_failed":
				failure = "one or more deployment tasks failed"
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		status := ""
		if failure != "" {
			status = "failed"
		} else if total == item.TaskCount && succeeded == item.TaskCount {
			status = "succeeded"
		}
		if status == "" {
			continue
		}
		if len(failure) > 1000 {
			failure = failure[:1000]
		}
		ts := now()
		if _, err := s.db.ExecContext(ctx, `update node_incident_actions set status=?,error=?,completed_at=?,updated_at=? where id=? and status='deployment_pending'`, status, failure, ts, ts, item.ID); err != nil {
			return nil, err
		}
		item.Status = status
		item.Error = failure
		completedAt := parseTime(ts)
		item.CompletedAt = &completedAt
		item.UpdatedAt = completedAt
		completed = append(completed, item)
	}
	return completed, nil
}

func (s *Store) CreateNodePublicationIsolations(ctx context.Context, incidentID, actorUserID int64, inboundIDs []int64, recoveryPolicy string) ([]model.NodePublicationIsolation, error) {
	if recoveryPolicy != "manual" && recoveryPolicy != "auto" {
		return nil, errors.New("recovery policy must be manual or auto")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	incident, err := scanNodeIncident(tx.QueryRowContext(ctx, nodeIncidentSelect+` where id=?`, incidentID))
	if err != nil {
		return nil, err
	}
	if incident.Status == model.NodeIncidentResolved {
		return nil, errors.New("resolved node incident is read-only")
	}
	ts := now()
	seen := map[int64]bool{}
	for _, inboundID := range inboundIDs {
		if inboundID <= 0 || seen[inboundID] {
			continue
		}
		seen[inboundID] = true
		var name string
		var serverID int64
		if err := tx.QueryRowContext(ctx, `select name,server_id from inbounds where id=?`, inboundID).Scan(&name, &serverID); err != nil {
			return nil, err
		}
		if serverID != incident.ServerID {
			return nil, fmt.Errorf("inbound %d is not on incident server", inboundID)
		}
		if _, err := tx.ExecContext(ctx, `insert into node_publication_isolations(incident_id,inbound_id,inbound_name,server_id,recovery_policy,status,actor_user_id,created_at,updated_at) values(?,?,?,?,?,'hidden',?,?,?) on conflict(inbound_id) where status='hidden' and inbound_id is not null do update set recovery_policy=excluded.recovery_policy,actor_user_id=excluded.actor_user_id,updated_at=excluded.updated_at`, incidentID, inboundID, name, serverID, recoveryPolicy, actorUserID, ts, ts); err != nil {
			return nil, err
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("at least one inbound is required")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListNodePublicationIsolations(ctx, incidentID)
}

func (s *Store) ListNodePublicationIsolations(ctx context.Context, incidentID int64) ([]model.NodePublicationIsolation, error) {
	query := `select id,incident_id,inbound_id,inbound_name,server_id,recovery_policy,status,actor_user_id,restored_by,restored_at,created_at,updated_at from node_publication_isolations`
	args := []any{}
	if incidentID > 0 {
		query += ` where incident_id=?`
		args = append(args, incidentID)
	}
	query += ` order by id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.NodePublicationIsolation{}
	for rows.Next() {
		var item model.NodePublicationIsolation
		var inboundID, restoredBy sql.NullInt64
		var restoredAt sql.NullString
		var created, updated string
		if err := rows.Scan(&item.ID, &item.IncidentID, &inboundID, &item.InboundName, &item.ServerID, &item.RecoveryPolicy, &item.Status, &item.ActorUserID, &restoredBy, &restoredAt, &created, &updated); err != nil {
			return nil, err
		}
		if inboundID.Valid {
			item.InboundID = &inboundID.Int64
		}
		if restoredBy.Valid {
			item.RestoredBy = &restoredBy.Int64
		}
		item.RestoredAt = parseNullTime(restoredAt)
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) RestoreNodePublicationIsolation(ctx context.Context, isolationID, actorUserID int64) error {
	res, err := s.db.ExecContext(ctx, `update node_publication_isolations set status='restored',restored_by=?,restored_at=?,updated_at=? where id=? and status='hidden'`, actorUserID, now(), now(), isolationID)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) MarkNodePublicationIsolationsRemoved(ctx context.Context, inboundIDs []int64, actorUserID int64) error {
	if len(inboundIDs) == 0 {
		return nil
	}
	ts := now()
	for _, inboundID := range inboundIDs {
		if _, err := s.db.ExecContext(ctx, `update node_publication_isolations set status='removed',restored_by=?,restored_at=?,updated_at=? where inbound_id=? and status='hidden'`, actorUserID, ts, ts, inboundID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListHiddenInboundIDs(ctx context.Context) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `select inbound_id from node_publication_isolations where status='hidden' and inbound_id is not null`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *Store) CreateTelegramBindingCode(ctx context.Context, codeHash string, userID int64, expiresAt time.Time) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into telegram_binding_codes(code_hash,user_id,expires_at,created_at) values(?,?,?,?)`, codeHash, userID, expiresAt.UTC().Format(time.RFC3339Nano), ts)
	return err
}

func (s *Store) ConsumeTelegramBindingCode(ctx context.Context, codeHash string, channelID, chatID, telegramUserID int64, chatType string, at time.Time) (*model.TelegramBinding, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var userID int64
	err = tx.QueryRowContext(ctx, `select user_id from telegram_binding_codes where code_hash=? and consumed_at is null and expires_at>=?`, codeHash, at.UTC().Format(time.RFC3339Nano)).Scan(&userID)
	if err != nil {
		return nil, err
	}
	var status string
	if err := tx.QueryRowContext(ctx, `select status from users where id=?`, userID).Scan(&status); err != nil || status != "active" {
		return nil, errors.New("binding user is not active")
	}
	var channelOwnerID int64
	var channelType string
	if err := tx.QueryRowContext(ctx, `select owner_user_id,type from notification_channels where id=?`, channelID).Scan(&channelOwnerID, &channelType); err != nil || channelOwnerID != userID || channelType != "telegram" {
		return nil, errors.New("binding channel is not owned by the user")
	}
	ts := at.UTC().Format(time.RFC3339Nano)
	res, err := tx.ExecContext(ctx, `update telegram_binding_codes set consumed_at=? where code_hash=? and consumed_at is null`, ts, codeHash)
	if err != nil {
		return nil, err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return nil, errors.New("binding code was already used")
	}
	res, err = tx.ExecContext(ctx, `insert into telegram_bindings(channel_id,user_id,chat_id,telegram_user_id,chat_type,created_at,updated_at) values(?,?,?,?,?,?,?) on conflict(channel_id,chat_id,telegram_user_id) do update set user_id=excluded.user_id,chat_type=excluded.chat_type,updated_at=excluded.updated_at`, channelID, userID, chatID, telegramUserID, chatType, ts, ts)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if id == 0 {
		return s.GetTelegramBinding(ctx, channelID, chatID, telegramUserID)
	}
	return s.GetTelegramBinding(ctx, channelID, chatID, telegramUserID)
}

func (s *Store) GetTelegramBinding(ctx context.Context, channelID, chatID, telegramUserID int64) (*model.TelegramBinding, error) {
	var item model.TelegramBinding
	var created, updated string
	err := s.db.QueryRowContext(ctx, `select id,channel_id,user_id,chat_id,telegram_user_id,chat_type,created_at,updated_at from telegram_bindings where channel_id=? and chat_id=? and telegram_user_id=?`, channelID, chatID, telegramUserID).Scan(&item.ID, &item.ChannelID, &item.UserID, &item.ChatID, &item.TelegramUserID, &item.ChatType, &created, &updated)
	if err != nil {
		return nil, err
	}
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return &item, nil
}

func (s *Store) GetTelegramBindingForChat(ctx context.Context, chatID, telegramUserID int64) (*model.TelegramBinding, error) {
	var item model.TelegramBinding
	var created, updated string
	err := s.db.QueryRowContext(ctx, `select id,channel_id,user_id,chat_id,telegram_user_id,chat_type,created_at,updated_at from telegram_bindings where chat_id=? and telegram_user_id=? order by updated_at desc,id desc limit 1`, chatID, telegramUserID).Scan(&item.ID, &item.ChannelID, &item.UserID, &item.ChatID, &item.TelegramUserID, &item.ChatType, &created, &updated)
	if err != nil {
		return nil, err
	}
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return &item, nil
}

func (s *Store) DeleteTelegramBinding(ctx context.Context, channelID, chatID, telegramUserID int64) error {
	res, err := s.db.ExecContext(ctx, `delete from telegram_bindings where channel_id=? and chat_id=? and telegram_user_id=?`, channelID, chatID, telegramUserID)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteTelegramBindingsForChat(ctx context.Context, chatID, telegramUserID int64) error {
	res, err := s.db.ExecContext(ctx, `delete from telegram_bindings where chat_id=? and telegram_user_id=?`, chatID, telegramUserID)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) TelegramBindingActive(ctx context.Context, id, userID int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from telegram_bindings where id=? and user_id=?`, id, userID).Scan(&count)
	return count == 1, err
}

func (s *Store) DeleteTelegramBindingByID(ctx context.Context, id, userID int64, allowAnyUser bool) error {
	query := `delete from telegram_bindings where id=?`
	args := []any{id}
	if !allowAnyUser {
		query += ` and user_id=?`
		args = append(args, userID)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) ListTelegramBindingsByChannel(ctx context.Context, channelID int64) ([]model.TelegramBinding, error) {
	rows, err := s.db.QueryContext(ctx, `select id,channel_id,user_id,chat_id,telegram_user_id,chat_type,created_at,updated_at from telegram_bindings where channel_id=? order by id`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.TelegramBinding{}
	for rows.Next() {
		var item model.TelegramBinding
		var created, updated string
		if err := rows.Scan(&item.ID, &item.ChannelID, &item.UserID, &item.ChatID, &item.TelegramUserID, &item.ChatType, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListTelegramBindingsForUser(ctx context.Context, userID int64) ([]model.TelegramBinding, error) {
	rows, err := s.db.QueryContext(ctx, `select id,channel_id,user_id,chat_id,telegram_user_id,chat_type,created_at,updated_at from telegram_bindings where user_id=? order by id desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.TelegramBinding{}
	for rows.Next() {
		var item model.TelegramBinding
		var created, updated string
		if err := rows.Scan(&item.ID, &item.ChannelID, &item.UserID, &item.ChatID, &item.TelegramUserID, &item.ChatType, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CreateOperationConfirmation(ctx context.Context, tokenHash, capability string, eventID, eventVersion, actorUserID int64, payloadJSON string, expiresAt time.Time) error {
	var storedEventID any
	if eventID > 0 {
		storedEventID = eventID
	}
	_, err := s.db.ExecContext(ctx, `insert into operation_confirmations(token_hash,capability,event_id,event_version,actor_user_id,payload_json,expires_at,created_at) values(?,?,?,?,?,?,?,?)`, tokenHash, capability, storedEventID, eventVersion, actorUserID, payloadJSON, expiresAt.UTC().Format(time.RFC3339Nano), now())
	return err
}

func (s *Store) ConsumeOperationConfirmation(ctx context.Context, tokenHash, capability string, eventID, eventVersion, actorUserID int64, at time.Time) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var payload string
	err = tx.QueryRowContext(ctx, `select payload_json from operation_confirmations where token_hash=? and capability=? and coalesce(event_id,0)=? and event_version=? and actor_user_id=? and consumed_at is null and expires_at>=?`, tokenHash, capability, eventID, eventVersion, actorUserID, at.UTC().Format(time.RFC3339Nano)).Scan(&payload)
	if err != nil {
		return "", err
	}
	res, err := tx.ExecContext(ctx, `update operation_confirmations set consumed_at=? where token_hash=? and consumed_at is null`, at.UTC().Format(time.RFC3339Nano), tokenHash)
	if err != nil {
		return "", err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return "", errors.New("confirmation token was already consumed")
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return payload, nil
}

type OperationConfirmation struct {
	Capability   string
	EventID      int64
	EventVersion int64
	PayloadJSON  string
}

func (s *Store) ConsumeOperationConfirmationToken(ctx context.Context, tokenHash string, actorUserID int64, at time.Time) (OperationConfirmation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OperationConfirmation{}, err
	}
	defer tx.Rollback()
	var item OperationConfirmation
	err = tx.QueryRowContext(ctx, `select capability,coalesce(event_id,0),event_version,payload_json from operation_confirmations where token_hash=? and actor_user_id=? and consumed_at is null and expires_at>=?`, tokenHash, actorUserID, at.UTC().Format(time.RFC3339Nano)).Scan(&item.Capability, &item.EventID, &item.EventVersion, &item.PayloadJSON)
	if err != nil {
		return OperationConfirmation{}, err
	}
	res, err := tx.ExecContext(ctx, `update operation_confirmations set consumed_at=? where token_hash=? and consumed_at is null`, at.UTC().Format(time.RFC3339Nano), tokenHash)
	if err != nil {
		return OperationConfirmation{}, err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return OperationConfirmation{}, errors.New("confirmation token was already consumed")
	}
	if err := tx.Commit(); err != nil {
		return OperationConfirmation{}, err
	}
	return item, nil
}

// ClaimTelegramBotPoll acquires a short SQLite lease and returns the durable
// update offset. This prevents two Controller instances from consuming the
// same Bot API stream concurrently.
func (s *Store) ClaimTelegramBotPoll(ctx context.Context, tokenHash, owner string, at time.Time, lease time.Duration) (int64, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	ts := at.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert or ignore into telegram_bot_state(token_hash,updated_at) values(?,?)`, tokenHash, ts); err != nil {
		return 0, false, err
	}
	res, err := tx.ExecContext(ctx, `update telegram_bot_state set lease_owner=?,lease_until=?,updated_at=? where token_hash=? and (lease_until is null or lease_until<=? or lease_owner=?)`, owner, at.UTC().Add(lease).Format(time.RFC3339Nano), ts, tokenHash, ts, owner)
	if err != nil {
		return 0, false, err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return 0, false, nil
	}
	var offset int64
	if err := tx.QueryRowContext(ctx, `select update_offset from telegram_bot_state where token_hash=?`, tokenHash).Scan(&offset); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return offset, true, nil
}

func (s *Store) SaveTelegramBotOffset(ctx context.Context, tokenHash, owner string, offset int64) error {
	res, err := s.db.ExecContext(ctx, `update telegram_bot_state set update_offset=case when update_offset<? then ? else update_offset end,updated_at=? where token_hash=? and lease_owner=?`, offset, offset, now(), tokenHash, owner)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return errors.New("telegram bot poll lease was lost")
	}
	return nil
}
