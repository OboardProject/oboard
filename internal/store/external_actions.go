package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrExternalActionConsumed = errors.New("external action already consumed")
	ErrExternalActionExpired  = errors.New("external action expired")
)

// ExternalAction is a one-time, grant-scoped external action produced by a
// Workflow (for example a server onboarding enrollment token). The payload is
// encrypted by the Controller and returned at most once through
// oboard_redeem_external_action; it is never exposed through resources and
// never logged.
type ExternalAction struct {
	ID         string
	GrantID    string
	WorkflowID string
	Kind       string
	Payload    string
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	CreatedAt  time.Time
}

func (s *Store) CreateExternalAction(ctx context.Context, action *ExternalAction) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into automation_external_actions(id,grant_id,workflow_id,kind,payload_encrypted,expires_at,created_at) values(?,?,?,?,?,?,?)`, action.ID, action.GrantID, action.WorkflowID, action.Kind, action.Payload, action.ExpiresAt.UTC().Format(time.RFC3339Nano), ts)
	if err == nil {
		action.CreatedAt = parseTime(ts)
	}
	return err
}

func (s *Store) GetExternalAction(ctx context.Context, id string) (*ExternalAction, error) {
	row := s.db.QueryRowContext(ctx, `select id,grant_id,workflow_id,kind,payload_encrypted,expires_at,consumed_at,created_at from automation_external_actions where id=?`, id)
	return scanExternalAction(row)
}

// ConsumeExternalAction returns the payload of an unconsumed, unexpired action
// exactly once and marks it consumed in the same transaction. The encrypted
// payload is deleted after the read so a replayed call cannot recover it.
func (s *Store) ConsumeExternalAction(ctx context.Context, id string, at time.Time) (*ExternalAction, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	action, err := scanExternalAction(tx.QueryRowContext(ctx, `select id,grant_id,workflow_id,kind,payload_encrypted,expires_at,consumed_at,created_at from automation_external_actions where id=?`, id))
	if err != nil {
		return nil, err
	}
	if action.ConsumedAt != nil {
		return action, ErrExternalActionConsumed
	}
	if !action.ExpiresAt.After(at) {
		return action, ErrExternalActionExpired
	}
	ts := at.UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `update automation_external_actions set consumed_at=? where id=? and consumed_at is null`, ts, id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update automation_external_actions set payload_encrypted='' where id=?`, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	consumed := parseTime(ts)
	action.ConsumedAt = &consumed
	return action, nil
}

func scanExternalAction(scanner interface{ Scan(...any) error }) (*ExternalAction, error) {
	var item ExternalAction
	var payload, expires, created string
	var consumed sql.NullString
	if err := scanner.Scan(&item.ID, &item.GrantID, &item.WorkflowID, &item.Kind, &payload, &expires, &consumed, &created); err != nil {
		return nil, err
	}
	item.Payload = payload
	item.ExpiresAt, item.ConsumedAt, item.CreatedAt = parseTime(expires), parseNullTime(consumed), parseTime(created)
	return &item, nil
}
