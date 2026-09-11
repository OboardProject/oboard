package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/OboardProject/oboard/internal/model"
	"strings"
)

// ConfirmSnellRuntime publishes only endpoints from a verified running config.
// Port retirement and endpoint publication share the same SQLite transaction.
func (s *Store) ConfirmSnellRuntime(ctx context.Context, serverID, version int64, listeners map[string]map[string]any) error {
	if serverID <= 0 || version <= 0 {
		return fmt.Errorf("invalid snell runtime confirmation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, err := json.Marshal(listeners)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	var latest int64
	var activeDigest string
	err = tx.QueryRowContext(ctx, `select config_version,digest from snell_runtime_confirmations where server_id=?`, serverID).Scan(&latest, &activeDigest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if latest > version {
		return nil
	}
	if latest == version {
		if activeDigest != digest {
			return fmt.Errorf("snell runtime confirmation digest conflict")
		}
		return nil
	}
	if _, err = tx.ExecContext(ctx, `insert into snell_runtime_confirmations(server_id,config_version,digest) values(?,?,?) on conflict(server_id) do update set config_version=excluded.config_version,digest=excluded.digest`, serverID, version, digest); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `select id from inbounds where server_id=? and protocol='snell'`, serverID)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	integer := func(v any) int {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
		return 0
	}
	for _, id := range ids {
		mode := "per_identity_port"
		port, pv := 0, 0
		for tag, item := range listeners {
			if tag == fmt.Sprintf("in-%d", id) && item["auth_mode"] == "multi_psk" {
				mode = "shared_port"
				port = integer(item["listen_port"])
				pv = integer(item["version"])
				break
			}
			if strings.HasPrefix(tag, fmt.Sprintf("in-%d-", id)) {
				pv = integer(item["version"])
			}
		}
		if pv == 0 {
			mode = ""
		}
		if pv == 5 {
			pv = 4
		}
		if _, err = tx.ExecContext(ctx, `insert into snell_listener_runtime(inbound_id,server_id,listener_mode,port,protocol_version,config_version,updated_at) values(?,?,?,?,?,?,?) on conflict(inbound_id) do update set listener_mode=excluded.listener_mode,port=excluded.port,protocol_version=excluded.protocol_version,config_version=excluded.config_version,updated_at=excluded.updated_at`, id, serverID, mode, port, pv, version, now()); err != nil {
			return err
		}
	}
	rows, err = tx.QueryContext(ctx, `select id,scope_key,port from proxy_path_port_allocations where server_id=? and kind=?`, serverID, model.ProxyPathPortKindSnellUser)
	if err != nil {
		return err
	}
	var retired []int64
	for rows.Next() {
		var id int64
		var scope string
		var port int
		if err = rows.Scan(&id, &scope, &port); err != nil {
			rows.Close()
			return err
		}
		used := false
		for _, item := range listeners {
			if integer(item["listen_port"]) == port {
				used = true
				break
			}
		}
		if !used {
			retired = append(retired, id)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range retired {
		if _, err = tx.ExecContext(ctx, `delete from proxy_path_port_allocations where id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
