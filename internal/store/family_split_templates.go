package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) migrateFamilySplitTemplates(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `create table if not exists family_split_templates (id integer primary key autoincrement, name text not null, created_at text not null, updated_at text not null)`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "routing_rules", "family_split_template_id", `alter table routing_rules add column family_split_template_id integer references family_split_templates(id) on delete restrict`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_paths", "template_id", `alter table proxy_paths add column template_id integer references family_split_templates(id) on delete cascade`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_paths", "family", `alter table proxy_paths add column family text not null default ''`); err != nil {
		return err
	}
	if err := s.ensureNullableProxyPathInbound(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create unique index if not exists idx_proxy_paths_template_family on proxy_paths(template_id, family) where template_id is not null`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create unique index if not exists idx_family_split_templates_name on family_split_templates(lower(name))`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create index if not exists idx_routing_rules_family_split_template on routing_rules(family_split_template_id) where family_split_template_id is not null`); err != nil {
		return err
	}
	if err := s.backfillFamilySplitTemplates(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `drop index if exists idx_routing_rules_ipv4_target_path`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `drop index if exists idx_routing_rules_ipv6_target_path`); err != nil {
		return err
	}
	if err := s.dropColumn(ctx, "routing_rules", "ipv4_target_proxy_path_id", `alter table routing_rules drop column ipv4_target_proxy_path_id`); err != nil {
		return err
	}
	return s.dropColumn(ctx, "routing_rules", "ipv6_target_proxy_path_id", `alter table routing_rules drop column ipv6_target_proxy_path_id`)
}

func (s *Store) proxyPathCopyExpr(ctx context.Context, column, present, missing string) (string, error) {
	ok, err := s.tableHasColumn(ctx, "proxy_paths", column)
	if err != nil {
		return "", err
	}
	if ok {
		return present, nil
	}
	return missing, nil
}

func (s *Store) ensureNullableProxyPathInbound(ctx context.Context) error {
	var notNull int
	if err := s.db.QueryRowContext(ctx, `select "notnull" from pragma_table_info('proxy_paths') where name='inbound_id'`).Scan(&notNull); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if notNull == 0 {
		return nil
	}
	kindExpr, err := s.proxyPathCopyExpr(ctx, "kind", "coalesce(kind,'chain')", "'chain'")
	if err != nil {
		return err
	}
	branchExpr, err := s.proxyPathCopyExpr(ctx, "branch_source_step_id", "branch_source_step_id", "null")
	if err != nil {
		return err
	}
	nameModeExpr, err := s.proxyPathCopyExpr(ctx, "name_mode", "coalesce(name_mode,'auto')", "'auto'")
	if err != nil {
		return err
	}
	templateJSONExpr, err := s.proxyPathCopyExpr(ctx, "name_template_json", "coalesce(name_template_json,'[]')", "'[]'")
	if err != nil {
		return err
	}
	exitModeExpr, err := s.proxyPathCopyExpr(ctx, "exit_region_mode", "coalesce(exit_region_mode,'auto')", "'auto'")
	if err != nil {
		return err
	}
	exitCodeExpr, err := s.proxyPathCopyExpr(ctx, "exit_region_code", "coalesce(exit_region_code,'')", "''")
	if err != nil {
		return err
	}
	secretExpr, err := s.proxyPathCopyExpr(ctx, "secret", "coalesce(secret,'')", "''")
	if err != nil {
		return err
	}
	templateIDExpr, err := s.proxyPathCopyExpr(ctx, "template_id", "template_id", "null")
	if err != nil {
		return err
	}
	familyExpr, err := s.proxyPathCopyExpr(ctx, "family", "coalesce(family,'')", "''")
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	createSQL := `create table proxy_paths_nullable_inbound (
			id integer primary key autoincrement,
			inbound_id integer references inbounds(id) on delete cascade,
			kind text not null default 'chain',
			branch_source_step_id integer references proxy_path_steps(id) on delete set null,
			name_mode text not null default 'auto',
			name_template_json text not null default '[]',
			exit_region_mode text not null default 'auto',
			exit_region_code text not null default '',
			secret text not null default '',
			enabled integer not null default 1,
			template_id integer references family_split_templates(id) on delete cascade,
			family text not null default '',
			created_at text not null,
			updated_at text not null
		)`
	copySQL := fmt.Sprintf(`insert into proxy_paths_nullable_inbound(id,inbound_id,kind,branch_source_step_id,name_mode,name_template_json,exit_region_mode,exit_region_code,secret,enabled,template_id,family,created_at,updated_at)
			select id,inbound_id,%s,%s,%s,%s,%s,%s,%s,enabled,%s,%s,created_at,updated_at from proxy_paths`,
		kindExpr, branchExpr, nameModeExpr, templateJSONExpr, exitModeExpr, exitCodeExpr, secretExpr, templateIDExpr, familyExpr)
	for _, statement := range []string{
		createSQL,
		copySQL,
		`drop table proxy_paths`,
		`alter table proxy_paths_nullable_inbound rename to proxy_paths`,
		`create unique index if not exists idx_proxy_paths_template_family on proxy_paths(template_id, family) where template_id is not null`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate nullable proxy path inbound: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) backfillFamilySplitTemplates(ctx context.Context) error {
	var ipv4Count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('routing_rules') where name='ipv4_target_proxy_path_id'`).Scan(&ipv4Count); err != nil {
		return err
	}
	if ipv4Count == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `select id,name,proxy_path_id,stage_step_id,ipv4_target_proxy_path_id,ipv6_target_proxy_path_id from routing_rules where action='family_split' and family_split_template_id is null and ipv4_target_proxy_path_id is not null and ipv6_target_proxy_path_id is not null`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type legacy struct {
		ruleID, ipv4PathID, ipv6PathID int64
		sourcePathID, stageStepID      sql.NullInt64
		name                           string
	}
	var items []legacy
	for rows.Next() {
		var item legacy
		if err := rows.Scan(&item.ruleID, &item.name, &item.sourcePathID, &item.stageStepID, &item.ipv4PathID, &item.ipv6PathID); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	usedNames := map[string]bool{}
	existing, err := s.ListFamilySplitTemplates(ctx)
	if err != nil {
		return err
	}
	for _, item := range existing {
		usedNames[strings.ToLower(strings.TrimSpace(item.Name))] = true
	}
	for _, item := range items {
		name := uniqueFamilySplitTemplateName(strings.TrimSpace(item.name), usedNames)
		template := &model.FamilySplitTemplate{Name: name}
		ipv4Secret, err := randomServerChainSecret()
		if err != nil {
			return err
		}
		ipv6Secret, err := randomServerChainSecret()
		if err != nil {
			return err
		}
		if err := s.CreateFamilySplitTemplate(ctx, template, ipv4Secret, ipv6Secret); err != nil {
			return err
		}
		usedNames[strings.ToLower(name)] = true
		stagePosition := 0
		if item.stageStepID.Valid && item.sourcePathID.Valid {
			var position int
			if err := s.db.QueryRowContext(ctx, `select position from proxy_path_steps where id=? and path_id=?`, item.stageStepID.Int64, item.sourcePathID.Int64).Scan(&position); err == nil {
				stagePosition = position
			}
		}
		if err := s.copyFamilySplitSuffixSteps(ctx, item.ipv4PathID, template.IPv4PathID, stagePosition); err != nil {
			return err
		}
		if err := s.copyFamilySplitSuffixSteps(ctx, item.ipv6PathID, template.IPv6PathID, stagePosition); err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `update routing_rules set family_split_template_id=? where id=?`, template.ID, item.ruleID); err != nil {
			return err
		}
	}
	return nil
}

func uniqueFamilySplitTemplateName(base string, used map[string]bool) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "双栈模板"
	}
	candidate := base
	for index := 2; used[strings.ToLower(candidate)]; index++ {
		candidate = fmt.Sprintf("%s · %d", base, index)
	}
	return candidate
}

func (s *Store) copyFamilySplitSuffixSteps(ctx context.Context, sourcePathID, targetPathID int64, stagePosition int) error {
	rows, err := s.db.QueryContext(ctx, `select node_type,transport_mode,processing_role,server_id,inbound_id,external_outbound_id,config_json from proxy_path_steps where path_id=? and position>? order by position,id`, sourcePathID, stagePosition)
	if err != nil {
		return err
	}
	defer rows.Close()
	type hop struct {
		nodeType, transportMode, configJSON string
		processingRole                      int
		serverID, inboundID, externalID     sql.NullInt64
	}
	var hops []hop
	for rows.Next() {
		var item hop
		if err := rows.Scan(&item.nodeType, &item.transportMode, &item.processingRole, &item.serverID, &item.inboundID, &item.externalID, &item.configJSON); err != nil {
			return err
		}
		hops = append(hops, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	ts := now()
	for index, item := range hops {
		if _, err := s.db.ExecContext(ctx, `insert into proxy_path_steps(path_id,position,node_type,transport_mode,processing_role,server_id,inbound_id,external_outbound_id,config_json,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?)`, targetPathID, index+1, item.nodeType, item.transportMode, item.processingRole, nullInt64Value(item.serverID), nullInt64Value(item.inboundID), nullInt64Value(item.externalID), item.configJSON, ts, ts); err != nil {
			return err
		}
	}
	return nil
}

func nullInt64Value(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func zeroToNull(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func (s *Store) familySplitTemplateNameTaken(ctx context.Context, name string, excludeID int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from family_split_templates where lower(trim(name))=? and id!=?`, strings.ToLower(strings.TrimSpace(name)), excludeID).Scan(&count)
	return count > 0, err
}

func (s *Store) CreateFamilySplitTemplate(ctx context.Context, v *model.FamilySplitTemplate, ipv4Secret, ipv6Secret string) error {
	if v == nil {
		return errors.New("family split template is required")
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return errors.New("name required")
	}
	taken, err := s.familySplitTemplateNameTaken(ctx, v.Name, 0)
	if err != nil {
		return err
	}
	if taken {
		return errors.New("双栈模板名称已存在")
	}
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `insert into family_split_templates(name,created_at,updated_at) values(?,?,?)`, v.Name, ts, ts)
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	v.ID = id
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	ipv4ID, err := insertFamilyBranchPathTx(tx, ctx, id, model.FamilySplitFamilyIPv4, ipv4Secret, ts)
	if err != nil {
		return err
	}
	ipv6ID, err := insertFamilyBranchPathTx(tx, ctx, id, model.FamilySplitFamilyIPv6, ipv6Secret, ts)
	if err != nil {
		return err
	}
	v.IPv4PathID = ipv4ID
	v.IPv6PathID = ipv6ID
	return tx.Commit()
}

func insertFamilyBranchPathTx(tx *sql.Tx, ctx context.Context, templateID int64, family, secret, ts string) (int64, error) {
	result, err := tx.ExecContext(ctx, `insert into proxy_paths(inbound_id,kind,name_mode,name_template_json,exit_region_mode,exit_region_code,secret,enabled,template_id,family,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, nil, model.ProxyPathKindFamilyBranch, model.ProxyPathNameAuto, `[]`, "auto", "", secret, 0, templateID, family, ts, ts)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) UpdateFamilySplitTemplate(ctx context.Context, v *model.FamilySplitTemplate) error {
	if v == nil || v.ID <= 0 {
		return errors.New("family split template id is required")
	}
	v.Name = strings.TrimSpace(v.Name)
	if v.Name == "" {
		return errors.New("name required")
	}
	taken, err := s.familySplitTemplateNameTaken(ctx, v.Name, v.ID)
	if err != nil {
		return err
	}
	if taken {
		return errors.New("双栈模板名称已存在")
	}
	_, err = s.db.ExecContext(ctx, `update family_split_templates set name=?,updated_at=? where id=?`, v.Name, now(), v.ID)
	return err
}

func (s *Store) DeleteFamilySplitTemplate(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("family split template id is required")
	}
	count, err := s.CountFamilySplitTemplateReferences(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return errors.New("双栈模板仍被分流规则引用")
	}
	result, err := s.db.ExecContext(ctx, `delete from family_split_templates where id=?`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetFamilySplitTemplate(ctx context.Context, id int64) (*model.FamilySplitTemplate, error) {
	items, err := s.ListFamilySplitTemplates(ctx)
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

func (s *Store) ListFamilySplitTemplates(ctx context.Context) ([]model.FamilySplitTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `select t.id,t.name,t.created_at,t.updated_at,
		coalesce((select id from proxy_paths where template_id=t.id and family=?),0),
		coalesce((select id from proxy_paths where template_id=t.id and family=?),0)
		from family_split_templates t order by t.id desc`, model.FamilySplitFamilyIPv4, model.FamilySplitFamilyIPv6)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.FamilySplitTemplate
	for rows.Next() {
		var item model.FamilySplitTemplate
		var ca, ua string
		if err := rows.Scan(&item.ID, &item.Name, &ca, &ua, &item.IPv4PathID, &item.IPv6PathID); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(ca)
		item.UpdatedAt = parseTime(ua)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CountFamilySplitTemplateReferences(ctx context.Context, templateID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from routing_rules where family_split_template_id=?`, templateID).Scan(&count)
	return count, err
}

func (s *Store) CountEnabledFamilySplitTemplateReferences(ctx context.Context, templateID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `select count(*) from routing_rules where family_split_template_id=? and enabled=1 and action='family_split'`, templateID).Scan(&count)
	return count, err
}

func (s *Store) SetFamilyBranchPathsEnabled(ctx context.Context, templateID int64, enabled bool) error {
	_, err := s.db.ExecContext(ctx, `update proxy_paths set enabled=?,updated_at=? where template_id=? and kind=?`, boolInt(enabled), now(), templateID, model.ProxyPathKindFamilyBranch)
	return err
}

func (s *Store) ListFamilySplitTemplateGraftServerIDs(ctx context.Context, templateID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `select distinct server_id from routing_rules where family_split_template_id=? and action='family_split'`, templateID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
