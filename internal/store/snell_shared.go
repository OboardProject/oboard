package store

import (
	"context"
	"encoding/json"
	"fmt"
)

func (s *Store) migrateSnellSharedListeners(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id,config_json from inbounds where protocol='snell'`)
	if err != nil {
		return err
	}
	type update struct {
		id     int64
		config string
	}
	var updates []update
	for rows.Next() {
		var id int64
		var raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		var cfg map[string]any
		if err = json.Unmarshal([]byte(raw), &cfg); err != nil || cfg == nil {
			rows.Close()
			return fmt.Errorf("invalid Snell inbound %d configuration", id)
		}
		mode, present := cfg["listener_mode"]
		if present && mode != "per_identity_port" {
			continue
		}
		cfg["listener_mode"] = "shared_port"
		encoded, err := json.Marshal(cfg)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update{id, string(encoded)})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range updates {
		if _, err = tx.ExecContext(ctx, `update inbounds set config_json=?,updated_at=? where id=?`, item.config, now(), item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
