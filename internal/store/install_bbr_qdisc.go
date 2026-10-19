package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	legacyServerDefaultBBREnabledSetting = "server_default_bbr_enabled"
	ServerDefaultBBRQdiscSetting         = "server_default_bbr_qdisc"
)

// migrateServerDefaultBBRQdisc turns the former boolean BBR + FQ install switch
// into the queue-discipline choice (controller-settings-20261020-bbr-qdisc).
// An explicit "off" is preserved; "on" maps to the fq default, which needs no
// row. A qdisc already chosen by an administrator is never overwritten.
func (s *Store) migrateServerDefaultBBRQdisc(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	err = tx.QueryRowContext(ctx, `select value from app_settings where key=?`, legacyServerDefaultBBREnabledSetting).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if enabled, parseErr := strconv.ParseBool(strings.TrimSpace(raw)); parseErr == nil && !enabled {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do nothing`, ServerDefaultBBRQdiscSetting, "off", now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from app_settings where key=?`, legacyServerDefaultBBREnabledSetting); err != nil {
		return err
	}
	return tx.Commit()
}
