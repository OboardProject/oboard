package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

const (
	RuntimeUsersPendingAgentOffline       = "agent_offline"
	RuntimeUsersPendingDelivering         = "delivering"
	RuntimeUsersPendingAgentUpgrade       = "agent_upgrade_required"
	RuntimeUsersPendingDeliveryFailed     = "delivery_failed"
	RuntimeUsersPendingUnenrolled         = "unenrolled"
	RuntimeUsersPendingAwaitingConfirm    = "awaiting_confirmation"
	RuntimeUsersPendingRuntimeUnavailable = "runtime_unavailable"
	RuntimeUsersPendingCoreConfigFallback = "core_config_fallback"
)

type RuntimeUserState struct {
	ServerID                 int64
	DesiredRevision          int64
	DesiredDigest            string
	EvaluatedRoutingRevision uint64
	DeliveredRevision        int64
	DeliveredMessageID       string
	DeliveredAt              *time.Time
	ConfirmedRevision        int64
	ConfirmedDigest          string
	ConfirmedBootID          string
	ConfirmedAt              *time.Time
	PendingReason            string
	LastError                string
	Retryable                bool
	UpdatedAt                time.Time
}

func (s RuntimeUserState) Confirmed() bool {
	return s.DesiredRevision > 0 && s.ConfirmedRevision >= s.DesiredRevision
}

type RuntimeUserDesiredEvaluation struct {
	State   RuntimeUserState
	Changed bool
}

const runtimeUserStateSelectSQL = `select server_id,desired_revision,desired_digest,evaluated_routing_revision,delivered_revision,delivered_message_id,delivered_at,confirmed_revision,confirmed_digest,confirmed_boot_id,confirmed_at,pending_reason,last_error,retryable,updated_at from runtime_user_states`

func (s *Store) EvaluateRuntimeUsersDesired(ctx context.Context, serverID int64, routingRevision uint64, digest string, now time.Time) (RuntimeUserDesiredEvaluation, error) {
	return s.recordRuntimeUserDesiredAt(ctx, serverID, digest, routingRevision, now)
}

func (s *Store) RecordRuntimeUserDesired(ctx context.Context, serverID int64, digest string, routingRevision uint64) (RuntimeUserDesiredEvaluation, error) {
	return s.recordRuntimeUserDesiredAt(ctx, serverID, digest, routingRevision, time.Now().UTC())
}

func (s *Store) recordRuntimeUserDesiredAt(ctx context.Context, serverID int64, digest string, routingRevision uint64, now time.Time) (RuntimeUserDesiredEvaluation, error) {
	if serverID <= 0 {
		return RuntimeUserDesiredEvaluation{}, sql.ErrNoRows
	}
	digest = strings.TrimSpace(digest)
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	ts := now.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeUserDesiredEvaluation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `insert or ignore into runtime_user_states(server_id,updated_at) values(?,?)`, serverID, ts); err != nil {
		return RuntimeUserDesiredEvaluation{}, err
	}
	current, err := scanRuntimeUserState(tx.QueryRowContext(ctx, runtimeUserStateSelectSQL+` where server_id=?`, serverID))
	if err != nil {
		return RuntimeUserDesiredEvaluation{}, err
	}
	out := RuntimeUserDesiredEvaluation{State: current}
	if current.DesiredDigest == digest && current.DesiredRevision > 0 {
		if current.EvaluatedRoutingRevision == routingRevision {
			return out, nil
		}
		if _, err := tx.ExecContext(ctx, `update runtime_user_states set evaluated_routing_revision=?,updated_at=? where server_id=?`, routingRevision, ts, serverID); err != nil {
			return RuntimeUserDesiredEvaluation{}, err
		}
		if err := tx.Commit(); err != nil {
			return RuntimeUserDesiredEvaluation{}, err
		}
		out.State.EvaluatedRoutingRevision = routingRevision
		return out, nil
	}
	nextRevision := current.DesiredRevision + 1
	if _, err := tx.ExecContext(ctx, `update runtime_user_states set desired_revision=?,desired_digest=?,evaluated_routing_revision=?,pending_reason=case when pending_reason='' then ? else pending_reason end,updated_at=? where server_id=?`, nextRevision, digest, routingRevision, RuntimeUsersPendingDelivering, ts, serverID); err != nil {
		return RuntimeUserDesiredEvaluation{}, err
	}
	if err := tx.Commit(); err != nil {
		return RuntimeUserDesiredEvaluation{}, err
	}
	out.Changed = true
	out.State.DesiredRevision = nextRevision
	out.State.DesiredDigest = digest
	out.State.EvaluatedRoutingRevision = routingRevision
	if out.State.PendingReason == "" {
		out.State.PendingReason = RuntimeUsersPendingDelivering
	}
	return out, nil
}

func (s *Store) RecordRuntimeUsersDelivery(ctx context.Context, serverID, revision int64, messageID string) error {
	return s.RecordRuntimeUserDelivery(ctx, serverID, revision, messageID)
}

func (s *Store) RecordRuntimeUserDelivery(ctx context.Context, serverID, revision int64, messageID string) error {
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `update runtime_user_states set delivered_revision=?,delivered_message_id=?,delivered_at=?,pending_reason=case when confirmed_revision>=desired_revision then '' else ? end,last_error='',retryable=1,updated_at=? where server_id=?`, revision, strings.TrimSpace(messageID), ts, RuntimeUsersPendingAwaitingConfirm, ts, serverID)
	return err
}

func (s *Store) RecordRuntimeUsersConfirmation(ctx context.Context, serverID, revision int64, digest, bootID string) (bool, error) {
	return s.RecordRuntimeUserConfirmation(ctx, serverID, revision, digest, bootID)
}

func (s *Store) RecordRuntimeUserConfirmation(ctx context.Context, serverID, revision int64, digest, bootID string) (bool, error) {
	if serverID <= 0 || revision <= 0 {
		return false, nil
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `insert or ignore into runtime_user_states(server_id,updated_at) values(?,?)`, serverID, ts); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `update runtime_user_states set confirmed_revision=?,confirmed_digest=?,confirmed_boot_id=?,confirmed_at=?,pending_reason=case when desired_revision<=? then '' else pending_reason end,last_error=case when desired_revision<=? then '' else last_error end,updated_at=? where server_id=? and confirmed_revision<?`, revision, strings.TrimSpace(digest), strings.TrimSpace(bootID), ts, revision, revision, ts, serverID, revision)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	affected, _ := result.RowsAffected()
	return affected > 0, nil
}

func (s *Store) MarkRuntimeUsersPending(ctx context.Context, serverID int64, reason, lastError string, retryable bool) error {
	return s.MarkRuntimeUserPending(ctx, serverID, reason, lastError, retryable)
}

func (s *Store) MarkRuntimeUserPending(ctx context.Context, serverID int64, reason, lastError string, retryable bool) error {
	if serverID <= 0 {
		return nil
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `insert or ignore into runtime_user_states(server_id,updated_at) values(?,?)`, serverID, ts); err != nil {
		return err
	}
	retryFlag := 0
	if retryable {
		retryFlag = 1
	}
	_, err := s.db.ExecContext(ctx, `update runtime_user_states set pending_reason=?,last_error=?,retryable=?,updated_at=? where server_id=?`, strings.TrimSpace(reason), strings.TrimSpace(lastError), retryFlag, ts, serverID)
	return err
}

func (s *Store) RuntimeUserState(ctx context.Context, serverID int64) (RuntimeUserState, error) {
	state, err := scanRuntimeUserState(s.db.QueryRowContext(ctx, runtimeUserStateSelectSQL+` where server_id=?`, serverID))
	if err == sql.ErrNoRows {
		return RuntimeUserState{ServerID: serverID, Retryable: true}, nil
	}
	return state, err
}

func (s *Store) ListRuntimeUserStates(ctx context.Context) ([]RuntimeUserState, error) {
	rows, err := s.db.QueryContext(ctx, runtimeUserStateSelectSQL+` order by server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RuntimeUserState{}
	for rows.Next() {
		state, err := scanRuntimeUserState(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

func (s *Store) StaleRuntimeUserServerIDs(ctx context.Context, routingRevision uint64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select srv.id from servers srv left join runtime_user_states st on st.server_id=srv.id where coalesce(srv.agent_id,'')<>'' and (st.server_id is null or st.evaluated_routing_revision<? or st.confirmed_revision<st.desired_revision) order by srv.id`, routingRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type runtimeUserStateScanner interface {
	Scan(dest ...any) error
}

func scanRuntimeUserState(scanner runtimeUserStateScanner) (RuntimeUserState, error) {
	var item RuntimeUserState
	var deliveredAt, confirmedAt, updatedAt sql.NullString
	var retryable int
	if err := scanner.Scan(&item.ServerID, &item.DesiredRevision, &item.DesiredDigest, &item.EvaluatedRoutingRevision, &item.DeliveredRevision, &item.DeliveredMessageID, &deliveredAt, &item.ConfirmedRevision, &item.ConfirmedDigest, &item.ConfirmedBootID, &confirmedAt, &item.PendingReason, &item.LastError, &retryable, &updatedAt); err != nil {
		return RuntimeUserState{}, err
	}
	item.Retryable = retryable != 0
	item.DeliveredAt = parseNullTime(deliveredAt)
	item.ConfirmedAt = parseNullTime(confirmedAt)
	if updatedAt.Valid {
		if ts, err := time.Parse(time.RFC3339Nano, updatedAt.String); err == nil {
			item.UpdatedAt = ts
		}
	}
	return item, nil
}
