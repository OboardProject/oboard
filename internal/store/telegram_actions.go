package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// TelegramActionToken is a short-lived callback stored server-side. The Telegram
// callback_data carries only the unhashed token.
type TelegramActionToken struct {
	TokenHash   string
	ChatID      int64
	MessageID   int64
	Action      string
	PayloadJSON string
	ExpiresAt   time.Time
	ConsumedAt  *time.Time
}

func (s *Store) CreateTelegramActionToken(ctx context.Context, tokenHash string, chatID, messageID int64, action, payloadJSON string, expiresAt time.Time) error {
	if strings.TrimSpace(tokenHash) == "" || chatID == 0 || strings.TrimSpace(action) == "" || strings.TrimSpace(payloadJSON) == "" {
		return errors.New("telegram action token is incomplete")
	}
	_, err := s.db.ExecContext(ctx, `insert into telegram_action_tokens(token_hash,chat_id,message_id,action,payload_json,expires_at,created_at) values(?,?,?,?,?,?,?)`, tokenHash, chatID, messageID, action, payloadJSON, expiresAt.UTC().Format(time.RFC3339Nano), now())
	return err
}

func (s *Store) SetTelegramActionTokenMessages(ctx context.Context, tokenHashes []string, messageID int64) error {
	if messageID <= 0 || len(tokenHashes) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, hash := range tokenHashes {
		if strings.TrimSpace(hash) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `update telegram_action_tokens set message_id=? where token_hash=? and message_id=0`, messageID, hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteTelegramActionTokens(ctx context.Context, tokenHashes []string) error {
	if len(tokenHashes) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, hash := range tokenHashes {
		if strings.TrimSpace(hash) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `delete from telegram_action_tokens where token_hash=?`, hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteTelegramActionTokensForMessage(ctx context.Context, chatID, messageID int64) error {
	if chatID == 0 || messageID <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `delete from telegram_action_tokens where chat_id=? and message_id=? and consumed_at is null`, chatID, messageID)
	return err
}

func (s *Store) DeleteExpiredTelegramActionTokens(ctx context.Context, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `delete from telegram_action_tokens where expires_at < ? or consumed_at is not null`, at.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) TelegramActionToken(ctx context.Context, tokenHash string) (*TelegramActionToken, error) {
	var item TelegramActionToken
	var expires, consumed sql.NullString
	err := s.db.QueryRowContext(ctx, `select token_hash,chat_id,message_id,action,payload_json,expires_at,consumed_at from telegram_action_tokens where token_hash=?`, tokenHash).Scan(&item.TokenHash, &item.ChatID, &item.MessageID, &item.Action, &item.PayloadJSON, &expires, &consumed)
	if err != nil {
		return nil, err
	}
	item.ExpiresAt = parseTime(expires.String)
	if consumed.Valid && strings.TrimSpace(consumed.String) != "" {
		parsed := parseTime(consumed.String)
		item.ConsumedAt = &parsed
	}
	return &item, nil
}

func (s *Store) ConsumeTelegramActionToken(ctx context.Context, tokenHash string, chatID int64, at time.Time) (*TelegramActionToken, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var item TelegramActionToken
	var expires string
	err = tx.QueryRowContext(ctx, `select token_hash,chat_id,message_id,action,payload_json,expires_at from telegram_action_tokens where token_hash=? and consumed_at is null and expires_at>=?`, tokenHash, at.UTC().Format(time.RFC3339Nano)).Scan(&item.TokenHash, &item.ChatID, &item.MessageID, &item.Action, &item.PayloadJSON, &expires)
	if err != nil {
		return nil, err
	}
	if item.ChatID != chatID {
		return nil, sql.ErrNoRows
	}
	item.ExpiresAt = parseTime(expires)
	res, err := tx.ExecContext(ctx, `update telegram_action_tokens set consumed_at=? where token_hash=? and consumed_at is null`, at.UTC().Format(time.RFC3339Nano), tokenHash)
	if err != nil {
		return nil, err
	}
	if count, _ := res.RowsAffected(); count != 1 {
		return nil, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *Store) AccountAuditSnapshotJSON(ctx context.Context, userID int64) (json.RawMessage, error) {
	var raw sql.NullString
	err := s.db.QueryRowContext(ctx, `select snapshot from account_audit_snapshots where user_id=?`, userID).Scan(&raw)
	if err != nil {
		return nil, err
	}
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, sql.ErrNoRows
	}
	return json.RawMessage(raw.String), nil
}
