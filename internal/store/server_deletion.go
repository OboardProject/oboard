package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Server deletion stages. A deletion is durable from the moment it is claimed,
// so a Controller that dies mid-delete resumes at the stage it recorded instead
// of leaving a half-removed server behind.
const (
	// ServerDeletionPurging means the bulk telemetry history is still being
	// removed and the server row still exists.
	ServerDeletionPurging = "purging"
	// ServerDeletionExternal means the server is gone from the database and
	// only owned external state (DNS records) is still being released.
	ServerDeletionExternal = "external"
)

// ErrServerDeleting is returned when work is requested for a server whose
// deletion has already been claimed. The row may still exist while its history
// drains, or while a restart-interrupted deletion is being finished; either way
// accepting new work would resurrect a server the operator removed.
var ErrServerDeleting = errors.New("server is being deleted")

// ServerDeletion is the durable record of a server removal in progress. It
// deliberately has no foreign key to servers: it has to outlive the row so the
// external cleanup that follows the row delete can still be resumed and
// retried.
type ServerDeletion struct {
	ServerID    int64     `json:"server_id"`
	Name        string    `json:"name"`
	Stage       string    `json:"stage"`
	Payload     string    `json:"payload"`
	Attempts    int       `json:"attempts"`
	LastError   string    `json:"last_error"`
	RequestedAt time.Time `json:"requested_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const serverDeletionSelectSQL = `select server_id,name,stage,payload_json,attempts,last_error,requested_at,updated_at from server_deletions`

// BeginServerDeletion claims a server for deletion. The claim is idempotent:
// a second request for the same server joins the existing deletion instead of
// starting a competing one, and reports claimed=false so the caller knows the
// payload it passed was not the one recorded.
func (s *Store) BeginServerDeletion(ctx context.Context, serverID int64, name, payload string) (ServerDeletion, bool, error) {
	ts := now()
	result, err := s.db.ExecContext(ctx, `insert or ignore into server_deletions(server_id,name,stage,payload_json,attempts,last_error,requested_at,updated_at) values(?,?,?,?,0,'',?,?)`,
		serverID, strings.TrimSpace(name), ServerDeletionPurging, payload, ts, ts)
	if err != nil {
		return ServerDeletion{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return ServerDeletion{}, false, err
	}
	deletion, err := s.GetServerDeletion(ctx, serverID)
	if err != nil {
		return ServerDeletion{}, false, err
	}
	return deletion, inserted > 0, nil
}

// HasServerDeletion reports whether this server's deletion was already
// claimed. It is a primary-key lookup, so the write paths that guard on it pay
// one indexed read.
func (s *Store) HasServerDeletion(ctx context.Context, serverID int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from server_deletions where server_id=?`, serverID).Scan(&count)
	return count > 0, err
}

// serverDeletionClaimedTx is the same check inside a transaction, so a guard
// and the write it protects cannot straddle a concurrent delete claim.
func serverDeletionClaimedTx(ctx context.Context, tx *sql.Tx, serverID int64) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `select count(*) from server_deletions where server_id=?`, serverID).Scan(&count)
	return count > 0, err
}

func (s *Store) GetServerDeletion(ctx context.Context, serverID int64) (ServerDeletion, error) {
	rows, err := s.db.QueryContext(ctx, serverDeletionSelectSQL+` where server_id=?`, serverID)
	if err != nil {
		return ServerDeletion{}, err
	}
	defer rows.Close()
	items, err := scanServerDeletions(rows)
	if err != nil {
		return ServerDeletion{}, err
	}
	if len(items) == 0 {
		return ServerDeletion{}, sql.ErrNoRows
	}
	return items[0], nil
}

// ListServerDeletions returns every unfinished deletion, oldest first, so a
// restart resumes them in the order they were requested.
func (s *Store) ListServerDeletions(ctx context.Context) ([]ServerDeletion, error) {
	rows, err := s.db.QueryContext(ctx, serverDeletionSelectSQL+` order by requested_at, server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanServerDeletions(rows)
}

func scanServerDeletions(rows *sql.Rows) ([]ServerDeletion, error) {
	items := []ServerDeletion{}
	for rows.Next() {
		var item ServerDeletion
		var requestedAt, updatedAt string
		if err := rows.Scan(&item.ServerID, &item.Name, &item.Stage, &item.Payload, &item.Attempts, &item.LastError, &requestedAt, &updatedAt); err != nil {
			return nil, err
		}
		item.RequestedAt, _ = time.Parse(time.RFC3339Nano, requestedAt)
		item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

// SetServerDeletionStage advances a deletion and clears the previous failure.
func (s *Store) SetServerDeletionStage(ctx context.Context, serverID int64, stage string) error {
	_, err := s.db.ExecContext(ctx, `update server_deletions set stage=?,last_error='',updated_at=? where server_id=?`, stage, now(), serverID)
	return err
}

// RecordServerDeletionFailure keeps a failed external cleanup visible and
// retryable instead of dropping it. The attempt counter is what lets an
// operator see a deletion that keeps failing against an unreachable provider.
func (s *Store) RecordServerDeletionFailure(ctx context.Context, serverID int64, reason string) error {
	if len(reason) > 1024 {
		reason = reason[:1024]
	}
	_, err := s.db.ExecContext(ctx, `update server_deletions set attempts=attempts+1,last_error=?,updated_at=? where server_id=?`, reason, now(), serverID)
	return err
}

// CompleteServerDeletion removes the tombstone once nothing is left to release.
func (s *Store) CompleteServerDeletion(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from server_deletions where server_id=?`, serverID)
	return err
}

// serverHistoryPurgeTables are the per-server tables that grow without bound.
// They cascade from servers(id), so deleting the row removes them anyway - as
// one transaction that holds SQLite's single writer for as long as the history
// is large. Purging them in bounded batches first keeps the final row delete
// small, so an unrelated write is never queued behind one server's months of
// samples.
var serverHistoryPurgeTables = []string{"server_metric_samples", "server_connectivity_events"}

// PurgeServerHistoryBatch removes at most limit rows of per-server history in
// total across those tables and reports how many it removed. Zero means the
// history is drained.
func (s *Store) PurgeServerHistoryBatch(ctx context.Context, serverID int64, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("purge batch limit must be positive")
	}
	removed := 0
	for _, table := range serverHistoryPurgeTables {
		remaining := limit - removed
		if remaining <= 0 {
			break
		}
		result, err := s.db.ExecContext(ctx, `delete from `+table+` where rowid in (select rowid from `+table+` where server_id=? limit ?)`, serverID, remaining)
		if err != nil {
			return removed, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return removed, err
		}
		removed += int(count)
	}
	return removed, nil
}
