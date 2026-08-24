package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// CreateSnellProfile inserts a reusable Snell parameter set. Builtin status is
// assigned by the caller (seeded profiles are protected from deletion).
func (s *Store) CreateSnellProfile(ctx context.Context, v *model.SnellProfile) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into snell_profiles(name,version,psk,obfs_mode,obfs_host,mode,reuse,tcp_fast_open,remark,builtin,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		strings.TrimSpace(v.Name), v.Version, v.PSK, normalizeSnellObfsMode(v.ObfsMode), v.ObfsHost, normalizeSnellV6Mode(v.Mode), boolInt(v.Reuse), boolInt(v.TCPFastOpen), v.Remark, boolInt(v.Builtin), boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	return nil
}

func normalizeSnellObfsMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "http":
		return "http"
	default:
		return "none"
	}
}

func normalizeSnellV6Mode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "unshaped", "unsafe-raw":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return "default"
	}
}

// UpdateSnellProfile updates a profile. Builtin profiles keep their protected
// flag and cannot be marked disabled. The returned boolean reports whether the
// PSK or any parameter that changes the generated kernel config changed, so
// callers can decide to refresh affected inbounds.
func (s *Store) UpdateSnellProfile(ctx context.Context, v *model.SnellProfile) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var builtin, oldEnabled int
	var oldPSK, oldObfsMode, oldObfsHost, oldMode string
	var oldVersion int
	var oldReuse, oldTCPFastOpen int
	if err := tx.QueryRowContext(ctx, `select builtin,enabled,version,psk,obfs_mode,obfs_host,mode,reuse,tcp_fast_open from snell_profiles where id=?`, v.ID).Scan(&builtin, &oldEnabled, &oldVersion, &oldPSK, &oldObfsMode, &oldObfsHost, &oldMode, &oldReuse, &oldTCPFastOpen); err != nil {
		return false, err
	}
	v.Builtin = builtin == 1
	if v.Builtin {
		v.Enabled = true
	}
	ts := now()
	if _, err := tx.ExecContext(ctx, `update snell_profiles set name=?,version=?,psk=?,obfs_mode=?,obfs_host=?,mode=?,reuse=?,tcp_fast_open=?,remark=?,enabled=?,updated_at=? where id=?`,
		strings.TrimSpace(v.Name), v.Version, v.PSK, normalizeSnellObfsMode(v.ObfsMode), v.ObfsHost, normalizeSnellV6Mode(v.Mode), boolInt(v.Reuse), boolInt(v.TCPFastOpen), v.Remark, boolInt(v.Enabled), ts, v.ID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	v.Builtin = builtin == 1
	v.UpdatedAt = parseTime(ts)
	changed := oldVersion != v.Version || oldPSK != v.PSK || oldObfsMode != normalizeSnellObfsMode(v.ObfsMode) ||
		oldObfsHost != v.ObfsHost || oldMode != normalizeSnellV6Mode(v.Mode) || oldReuse != boolInt(v.Reuse) ||
		oldTCPFastOpen != boolInt(v.TCPFastOpen)
	return changed, nil
}

// ListSnellProfiles returns all reusable Snell parameter sets with the number
// of inbounds referencing each one.
func (s *Store) ListSnellProfiles(ctx context.Context) ([]model.SnellProfile, error) {
	query := `select p.id,p.name,p.version,p.psk,p.obfs_mode,p.obfs_host,p.mode,p.reuse,p.tcp_fast_open,p.remark,p.builtin,p.enabled,p.created_at,p.updated_at,
		(select count(*) from inbounds i where i.protocol='snell' and i.config_json like '%"snell_profile_id":'||p.id||'%' or (i.protocol='snell' and i.config_json like '%"snell_profile_id": '||p.id||'%'))
		from snell_profiles p order by p.builtin desc,p.enabled desc,p.name,p.id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SnellProfile
	for rows.Next() {
		var item model.SnellProfile
		var created, updated string
		var enabled, builtin, reuse, tcpFastOpen int
		if err := rows.Scan(&item.ID, &item.Name, &item.Version, &item.PSK, &item.ObfsMode, &item.ObfsHost, &item.Mode, &reuse, &tcpFastOpen, &item.Remark, &builtin, &enabled, &created, &updated, &item.UsageCount); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		item.Builtin = builtin == 1
		item.Reuse = reuse == 1
		item.TCPFastOpen = tcpFastOpen == 1
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetSnellProfile(ctx context.Context, id int64) (*model.SnellProfile, error) {
	items, err := s.ListSnellProfiles(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].ID == id {
			return &items[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

// DeleteSnellProfile refuses to remove protected (builtin) profiles or
// profiles still referenced by inbounds.
func (s *Store) DeleteSnellProfile(ctx context.Context, id int64) error {
	var builtin, usage int
	if err := s.db.QueryRowContext(ctx, `select builtin,
		(select count(*) from inbounds i where i.protocol='snell' and (i.config_json like '%"snell_profile_id":'||?||'%' or i.config_json like '%"snell_profile_id": '||?||'%'))
		from snell_profiles where id=?`, id, id, id).Scan(&builtin, &usage); err != nil {
		return err
	}
	if builtin == 1 {
		return errors.New("内置 Snell 预设不可删除")
	}
	if usage > 0 {
		return fmt.Errorf("Snell 预设正被 %d 个入站引用，请先解绑", usage)
	}
	_, err := s.db.ExecContext(ctx, `delete from snell_profiles where id=?`, id)
	return err
}