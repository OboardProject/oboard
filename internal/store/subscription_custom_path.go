package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) SetSubscriptionCustomPath(ctx context.Context, userID int64, alias string) (*model.SubscriptionCustomPath, error) {
	ts := now()
	result, err := s.db.ExecContext(ctx, `insert into subscription_custom_paths(user_id,alias,created_at,updated_at)
		select id,?,?,? from users where id=?
		on conflict(user_id) do update set alias=excluded.alias,updated_at=excluded.updated_at`, alias, ts, ts, userID)
	if err != nil {
		return nil, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return s.GetSubscriptionCustomPathForUser(ctx, userID)
}

func (s *Store) GetSubscriptionCustomPathForUser(ctx context.Context, userID int64) (*model.SubscriptionCustomPath, error) {
	var item model.SubscriptionCustomPath
	var created, updated string
	err := s.db.QueryRowContext(ctx, `select user_id,alias,created_at,updated_at from subscription_custom_paths where user_id=?`, userID).Scan(&item.UserID, &item.Alias, &created, &updated)
	if err != nil {
		return nil, err
	}
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return &item, nil
}

func (s *Store) ListSubscriptionCustomPaths(ctx context.Context) ([]model.SubscriptionCustomPath, error) {
	rows, err := s.db.QueryContext(ctx, `select user_id,alias,created_at,updated_at from subscription_custom_paths order by user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []model.SubscriptionCustomPath{}
	for rows.Next() {
		var item model.SubscriptionCustomPath
		var created, updated string
		if err := rows.Scan(&item.UserID, &item.Alias, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetUserBySubscriptionCustomPath(ctx context.Context, alias string) (*model.User, error) {
	rows, err := s.db.QueryContext(ctx, userSelectSQL+` join subscription_custom_paths scp on scp.user_id=u.id where scp.alias=? and u.status='active'`, alias)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUsers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) DeleteSubscriptionCustomPath(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from subscription_custom_paths where user_id=?`, userID)
	return err
}

func (s *Store) SetSubscriptionCustomPathUserPolicy(ctx context.Context, userID int64, mode model.SubscriptionCustomPathPolicy) error {
	if mode == model.SubscriptionCustomPathInherit {
		_, err := s.db.ExecContext(ctx, `delete from subscription_custom_path_user_policies where user_id=?`, userID)
		return err
	}
	result, err := s.db.ExecContext(ctx, `insert into subscription_custom_path_user_policies(user_id,mode,updated_at)
		select id,?,? from users where id=? on conflict(user_id) do update set mode=excluded.mode,updated_at=excluded.updated_at`, mode, now(), userID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) SetSubscriptionCustomPathGroupPolicy(ctx context.Context, groupID int64, mode model.SubscriptionCustomPathPolicy) error {
	if mode == model.SubscriptionCustomPathInherit {
		_, err := s.db.ExecContext(ctx, `delete from subscription_custom_path_group_policies where group_id=?`, groupID)
		return err
	}
	result, err := s.db.ExecContext(ctx, `insert into subscription_custom_path_group_policies(group_id,mode,updated_at)
		select id,?,? from user_groups where id=? on conflict(group_id) do update set mode=excluded.mode,updated_at=excluded.updated_at`, mode, now(), groupID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		return sql.ErrNoRows
	}
	return err
}

func (s *Store) SubscriptionCustomPathPolicies(ctx context.Context) (map[int64]model.SubscriptionCustomPathPolicy, map[int64]model.SubscriptionCustomPathPolicy, error) {
	users := map[int64]model.SubscriptionCustomPathPolicy{}
	groups := map[int64]model.SubscriptionCustomPathPolicy{}
	rows, err := s.db.QueryContext(ctx, `select user_id,mode from subscription_custom_path_user_policies`)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id int64
		var mode model.SubscriptionCustomPathPolicy
		if err := rows.Scan(&id, &mode); err != nil {
			return nil, nil, errors.Join(err, rows.Close())
		}
		users[id] = mode
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, nil, err
	}
	rows, err = s.db.QueryContext(ctx, `select group_id,mode from subscription_custom_path_group_policies`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var mode model.SubscriptionCustomPathPolicy
		if err := rows.Scan(&id, &mode); err != nil {
			return nil, nil, err
		}
		groups[id] = mode
	}
	return users, groups, rows.Err()
}

func (s *Store) RevokeSubscriptionCredentials(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	result, err := tx.ExecContext(ctx, `update users set subscription_token=null,updated_at=? where id=?`, ts, userID)
	if err != nil {
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		if err != nil {
			return err
		}
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `delete from subscription_one_time_tokens where user_id=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from subscription_custom_paths where user_id=?`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_token_policies(user_id,burn_after_read,burned_at,updated_at) values(?,0,null,?)
		on conflict(user_id) do update set burned_at=null,updated_at=excluded.updated_at`, userID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func IsSubscriptionCustomPathConflict(err error) bool {
	return err != nil && !errors.Is(err, sql.ErrNoRows) && (containsSQLiteConstraint(err, "subscription_custom_paths.alias") || containsSQLiteConstraint(err, "UNIQUE constraint failed"))
}

func containsSQLiteConstraint(err error, text string) bool {
	return err != nil && strings.Contains(err.Error(), text)
}
