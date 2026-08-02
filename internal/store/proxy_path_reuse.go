package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

var ErrRoutingTopologyChanged = errors.New("routing topology changed")

type ProxyPathReuseWrite struct {
	Path                 model.ProxyPath
	Steps                []model.ProxyPathStep
	ExistingPathID       int64
	BranchSourcePosition int
}

type routingQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) RoutingTopologyRevision(ctx context.Context) (string, error) {
	return routingTopologyRevision(ctx, s.db)
}

func routingTopologyRevision(ctx context.Context, queryer routingQueryer) (string, error) {
	hash := sha256.New()
	for _, table := range []string{"servers", "inbounds", "external_outbounds", "proxy_paths", "proxy_path_steps", "proxy_path_port_allocations", "warp_profiles"} {
		rows, err := queryer.QueryContext(ctx, `select id,updated_at from `+table+` order by id asc`)
		if err != nil {
			return "", err
		}
		for rows.Next() {
			var id int64
			var updated string
			if err := rows.Scan(&id, &updated); err != nil {
				return "", errors.Join(err, rows.Close())
			}
			_, _ = fmt.Fprintf(hash, "%s:%d:%s\n", table, id, updated)
		}
		if err := rows.Err(); err != nil {
			return "", errors.Join(err, rows.Close())
		}
		if err := rows.Close(); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ApplyProxyPathReuse writes an already validated expansion as one topology
// transaction. The revision check closes the gap between the pure projection
// and acquiring SQLite's write lock.
func (s *Store) ApplyProxyPathReuse(ctx context.Context, expectedRevision string, writes []ProxyPathReuseWrite) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `begin immediate`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
	}()
	revision, err := routingTopologyRevision(ctx, conn)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(expectedRevision), revision) {
		return ErrRoutingTopologyChanged
	}
	ts := now()
	for writeIndex := range writes {
		write := &writes[writeIndex]
		path := &write.Path
		if err := encodeProxyPathNameTemplate(path); err != nil {
			return err
		}
		if write.ExistingPathID != 0 {
			path.ID = write.ExistingPathID
			result, err := conn.ExecContext(ctx, `update proxy_paths set kind=?,branch_source_step_id=null,name_mode=?,name_template_json=?,exit_region_mode=?,exit_region_code=?,enabled=?,updated_at=? where id=?`, path.Kind, path.NameMode, path.NameTemplateJSON, path.ExitRegionMode, path.ExitRegionCode, boolInt(path.Enabled), ts, path.ID)
			if err != nil {
				return err
			}
			if count, err := result.RowsAffected(); err != nil || count != 1 {
				if err != nil {
					return err
				}
				return sql.ErrNoRows
			}
		} else {
			result, err := conn.ExecContext(ctx, `insert into proxy_paths(inbound_id,kind,branch_source_step_id,name_mode,name_template_json,exit_region_mode,exit_region_code,secret,enabled,created_at,updated_at) values(?,?,?,?,nullif(?,''),?,?,?,?,?,?)`, path.InboundID, path.Kind, nil, path.NameMode, path.NameTemplateJSON, path.ExitRegionMode, path.ExitRegionCode, path.Secret, boolInt(path.Enabled), ts, ts)
			if err != nil {
				return err
			}
			path.ID, err = result.LastInsertId()
			if err != nil {
				return err
			}
			path.CreatedAt = parseTime(ts)
		}
		path.UpdatedAt = parseTime(ts)
		stepIDByPosition := map[int]int64{}
		for stepIndex := range write.Steps {
			step := &write.Steps[stepIndex]
			step.ID = 0
			step.PathID = path.ID
			result, err := conn.ExecContext(ctx, `insert into proxy_path_steps(path_id,position,node_type,transport_mode,processing_role,server_id,inbound_id,external_outbound_id,config_json,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?)`, step.PathID, step.Position, step.NodeType, step.TransportMode, boolInt(step.ProcessingRole), step.ServerID, step.InboundID, step.ExternalOutboundID, step.ConfigJSON, ts, ts)
			if err != nil {
				return err
			}
			step.ID, err = result.LastInsertId()
			if err != nil {
				return err
			}
			step.CreatedAt = parseTime(ts)
			step.UpdatedAt = step.CreatedAt
			stepIDByPosition[step.Position] = step.ID
		}
		if write.BranchSourcePosition > 0 {
			branchSourceID := stepIDByPosition[write.BranchSourcePosition]
			if branchSourceID == 0 {
				if err := conn.QueryRowContext(ctx, `select id from proxy_path_steps where path_id=? and position=?`, path.ID, write.BranchSourcePosition).Scan(&branchSourceID); err != nil {
					return err
				}
			}
			if _, err := conn.ExecContext(ctx, `update proxy_paths set branch_source_step_id=? where id=?`, branchSourceID, path.ID); err != nil {
				return err
			}
			path.BranchSourceStepID = &branchSourceID
		}
	}
	if _, err := conn.ExecContext(ctx, `commit`); err != nil {
		return err
	}
	committed = true
	return nil
}

var _ routingQueryer = (*sql.DB)(nil)
var _ routingQueryer = (*sql.Conn)(nil)
