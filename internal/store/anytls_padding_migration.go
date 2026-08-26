package store

import (
	"context"
	"fmt"

	"github.com/OboardProject/oboard/internal/core"
)

// migrateExistingAnyTLSPaddingMetadata classifies pre-feature schemes as
// custom snapshots. It never adds a scheme and never changes an existing
// scheme's bytes or effective data-plane projection.
func (s *Store) migrateExistingAnyTLSPaddingMetadata(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `select id,config_json,created_at from inbounds where protocol='anytls' and config_json like '%"padding_scheme"%' and config_json not like '%"_oboard_padding"%'`)
	if err != nil {
		return err
	}
	type candidate struct {
		id        int64
		config    string
		createdAt string
	}
	items := []candidate{}
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.config, &item.createdAt); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		updated, changed, err := core.MarkExistingAnyTLSPaddingCustom(item.config, parseTime(item.createdAt))
		if err != nil {
			return fmt.Errorf("annotate AnyTLS inbound %d padding: %w", item.id, err)
		}
		if !changed {
			continue
		}
		if _, err := tx.ExecContext(ctx, `update inbounds set config_json=? where id=? and config_json=?`, updated, item.id, item.config); err != nil {
			return err
		}
	}
	return tx.Commit()
}
