package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) migrateProbeDDNS(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `select i.id,i.server_id,i.name,i.enabled,i.dns_credential_id,i.dns_domain,i.dns_proxy_enabled,i.dns_record_types,i.ddns_interval_seconds,coalesce(i.entry_ip_mode,'auto'),coalesce(s.public_ipv4,''),coalesce(s.public_ipv6,'') from inbounds i join servers s on s.id=i.server_id where i.ddns_enabled=1 and coalesce(i.ddns_record_id,0)=0 order by i.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type previous struct {
		id, serverID, credentialID int64
		name, domain, types, mode  string
		enabled, proxy             int
		interval                   int
		ipv4, ipv6                 string
	}
	var items []previous
	for rows.Next() {
		var item previous
		var credential sql.NullInt64
		if err = rows.Scan(&item.id, &item.serverID, &item.name, &item.enabled, &credential, &item.domain, &item.proxy, &item.types, &item.interval, &item.mode, &item.ipv4, &item.ipv6); err != nil {
			return err
		}
		if credential.Valid {
			item.credentialID = credential.Int64
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
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
	type grouped struct {
		record model.ProbeDDNSRecord
		ids    []int64
	}
	groups := map[string]*grouped{}
	var order []string
	var skipped []int64
	for _, item := range items {
		domain := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(item.domain)), ".")
		if item.credentialID <= 0 || !probeDDNSDomain(domain) {
			skipped = append(skipped, item.id)
			continue
		}
		key := strconv.FormatInt(item.serverID, 10) + "\x00" + domain
		group := groups[key]
		if group == nil {
			name := strings.TrimSpace(item.name)
			if name == "" {
				name = domain
			}
			if utf8.RuneCountInString(name) > 60 {
				name = string([]rune(name)[:60])
			}
			interval := item.interval
			if interval < 300 {
				interval = 300
			}
			if interval > 86400 {
				interval = 86400
			}
			credential := item.credentialID
			group = &grouped{record: model.ProbeDDNSRecord{
				ServerID:        item.serverID,
				Name:            name,
				DNSCredentialID: &credential,
				Domain:          domain,
				RecordTypes:     migratedProbeDDNSTypes(item.types, item.mode, item.ipv4, item.ipv6),
				ProxyEnabled:    item.proxy == 1,
				IntervalSeconds: interval,
				Enabled:         item.enabled == 1,
			}}
			groups[key] = group
			order = append(order, key)
		} else {
			if item.interval >= 300 && item.interval < group.record.IntervalSeconds {
				group.record.IntervalSeconds = item.interval
			}
			group.record.RecordTypes = mergeProbeDDNSTypes(group.record.RecordTypes, migratedProbeDDNSTypes(item.types, item.mode, item.ipv4, item.ipv6))
			group.record.ProxyEnabled = group.record.ProxyEnabled || item.proxy == 1
			group.record.Enabled = group.record.Enabled || item.enabled == 1
		}
		group.ids = append(group.ids, item.id)
	}
	ts := now()
	for _, key := range order {
		group := groups[key]
		var id int64
		err = tx.QueryRowContext(ctx, `select id from probe_ddns_records where server_id=? and domain=?`, group.record.ServerID, group.record.Domain).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			var result sql.Result
			result, err = tx.ExecContext(ctx, `insert into probe_ddns_records(server_id,name,dns_credential_id,domain,record_types,proxy_enabled,interval_seconds,enabled,interface_name,interface_address,address_family,exclude_private,revision,status,error,current_address,created_at,updated_at) values(?,?,?,?,?,?,?,?, '','','',0,1,'','','',?,?)`, group.record.ServerID, group.record.Name, group.record.DNSCredentialID, group.record.Domain, group.record.RecordTypes, boolInt(group.record.ProxyEnabled), group.record.IntervalSeconds, boolInt(group.record.Enabled), ts, ts)
			if err != nil {
				return err
			}
			id, err = result.LastInsertId()
		}
		if err != nil {
			return err
		}
		for _, inboundID := range group.ids {
			if _, err = tx.ExecContext(ctx, `update inbounds set ddns_record_id=?, ddns_enabled=0 where id=? and ddns_enabled=1 and coalesce(ddns_record_id,0)=0`, id, inboundID); err != nil {
				return err
			}
		}
	}
	for _, inboundID := range skipped {
		if _, err = tx.ExecContext(ctx, `update inbounds set ddns_enabled=0 where id=? and ddns_enabled=1`, inboundID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func migratedProbeDDNSTypes(types, mode, ipv4, ipv6 string) string {
	switch strings.ToLower(strings.TrimSpace(types)) {
	case "a", "aaaa", "both":
		return strings.ToLower(strings.TrimSpace(types))
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "ipv6":
		return "aaaa"
	case "ipv4":
		return "a"
	}
	if strings.TrimSpace(ipv4) != "" {
		return "a"
	}
	if strings.TrimSpace(ipv6) != "" {
		return "aaaa"
	}
	return "a"
}

func mergeProbeDDNSTypes(left, right string) string {
	if left == "both" || right == "both" || (left != right && left != "" && right != "") {
		return "both"
	}
	if left != "" {
		return left
	}
	return right
}

func probeDDNSDomain(domain string) bool {
	if len(domain) < 3 || len(domain) > 253 || strings.Contains(domain, "..") || strings.ContainsAny(domain, " \t") {
		return false
	}
	return strings.Count(domain, ".") >= 1
}

const probeDDNSColumns = `id,server_id,name,dns_credential_id,domain,record_types,proxy_enabled,interval_seconds,enabled,interface_name,interface_address,address_family,exclude_private,revision,status,error,current_address,last_synced_at,created_at,updated_at`

func (s *Store) ListProbeDDNS(ctx context.Context) ([]model.ProbeDDNSRecord, error) {
	rows, err := s.db.QueryContext(ctx, `select `+probeDDNSColumns+` from probe_ddns_records order by id desc limit 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProbeDDNSRecord{}
	for rows.Next() {
		item, err := scanProbeDDNS(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetProbeDDNS(ctx context.Context, id int64) (model.ProbeDDNSRecord, error) {
	row := s.db.QueryRowContext(ctx, `select `+probeDDNSColumns+` from probe_ddns_records where id=?`, id)
	return scanProbeDDNS(row)
}

type probeDDNSScanner interface {
	Scan(dest ...any) error
}

func scanProbeDDNS(row probeDDNSScanner) (model.ProbeDDNSRecord, error) {
	var v model.ProbeDDNSRecord
	var credential sql.NullInt64
	var proxy, enabled, exclude int
	var synced, created, updated sql.NullString
	err := row.Scan(&v.ID, &v.ServerID, &v.Name, &credential, &v.Domain, &v.RecordTypes, &proxy, &v.IntervalSeconds, &enabled, &v.InterfaceName, &v.InterfaceAddress, &v.AddressFamily, &exclude, &v.Revision, &v.Status, &v.Error, &v.CurrentAddress, &synced, &created, &updated)
	if err != nil {
		return v, err
	}
	if credential.Valid {
		v.DNSCredentialID = &credential.Int64
	}
	v.ProxyEnabled = proxy == 1
	v.Enabled = enabled == 1
	v.ExcludePrivate = exclude == 1
	if synced.Valid && synced.String != "" {
		t := parseTime(synced.String)
		v.LastSyncedAt = &t
	}
	if created.Valid {
		v.CreatedAt = parseTime(created.String)
	}
	if updated.Valid {
		v.UpdatedAt = parseTime(updated.String)
	}
	return v, nil
}

func (s *Store) SaveProbeDDNS(ctx context.Context, server model.Server, v *model.ProbeDDNSRecord) error {
	if err := ValidateProbeDDNS(v); err != nil {
		return err
	}
	if v.ServerID != server.ID {
		return errors.New("服务器身份不匹配")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = runtimeSecurityServerTx(ctx, tx, server); err != nil {
		return err
	}
	ts := now()
	if v.ID == 0 {
		var count int
		if err = tx.QueryRowContext(ctx, `select count(*) from probe_ddns_records`).Scan(&count); err != nil {
			return err
		}
		if count >= 1000 {
			return errors.New("最多支持 1000 条 DDNS 记录")
		}
		var taken int
		if err = tx.QueryRowContext(ctx, `select count(*) from probe_ddns_records where server_id=? and domain=?`, v.ServerID, v.Domain).Scan(&taken); err != nil {
			return err
		}
		if taken != 0 {
			return errors.New("该服务器已有相同域名的 DDNS 记录")
		}
		result, e := tx.ExecContext(ctx, `insert into probe_ddns_records(server_id,name,dns_credential_id,domain,record_types,proxy_enabled,interval_seconds,enabled,interface_name,interface_address,address_family,exclude_private,revision,status,error,current_address,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,1,'','','',?,?)`, v.ServerID, v.Name, v.DNSCredentialID, v.Domain, v.RecordTypes, boolInt(v.ProxyEnabled), v.IntervalSeconds, boolInt(v.Enabled), v.InterfaceName, v.InterfaceAddress, v.AddressFamily, boolInt(v.ExcludePrivate), ts, ts)
		if e != nil {
			return e
		}
		v.ID, err = result.LastInsertId()
		v.Revision = 1
		v.CreatedAt = parseTime(ts)
		v.UpdatedAt = v.CreatedAt
	} else {
		result, e := tx.ExecContext(ctx, `update probe_ddns_records set name=?,dns_credential_id=?,domain=?,record_types=?,proxy_enabled=?,interval_seconds=?,enabled=?,interface_name=?,interface_address=?,address_family=?,exclude_private=?,revision=revision+1,next_at=0,updated_at=? where id=? and server_id=? and revision=?`, v.Name, v.DNSCredentialID, v.Domain, v.RecordTypes, boolInt(v.ProxyEnabled), v.IntervalSeconds, boolInt(v.Enabled), v.InterfaceName, v.InterfaceAddress, v.AddressFamily, boolInt(v.ExcludePrivate), ts, v.ID, v.ServerID, v.Revision)
		if e != nil {
			return e
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			return errors.New("DDNS 记录已改变，请刷新")
		}
		var others int
		if err = tx.QueryRowContext(ctx, `select count(*) from probe_ddns_records where server_id=? and domain=? and id<>?`, v.ServerID, v.Domain, v.ID).Scan(&others); err != nil {
			return err
		}
		if others != 0 {
			return errors.New("该服务器已有相同域名的 DDNS 记录")
		}
		v.Revision++
		v.UpdatedAt = parseTime(ts)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteProbeDDNS(ctx context.Context, id, revision int64) error {
	result, err := s.db.ExecContext(ctx, `delete from probe_ddns_records where id=? and revision=?`, id, revision)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("DDNS 记录已改变，请刷新")
	}
	return nil
}

func (s *Store) TouchProbeDDNS(ctx context.Context, id, revision int64) error {
	result, err := s.db.ExecContext(ctx, `update probe_ddns_records set next_at=0 where id=? and revision=? and enabled=1`, id, revision)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return errors.New("DDNS 记录已改变或已停用，请刷新")
	}
	return nil
}

func (s *Store) ClaimProbeDDNS(ctx context.Context, v model.ProbeDDNSRecord, at time.Time) (bool, error) {
	interval := v.IntervalSeconds
	if interval < 300 {
		interval = 300
	}
	result, err := s.db.ExecContext(ctx, `update probe_ddns_records set next_at=?,status='running',error='' where id=? and revision=? and enabled=1 and next_at<=?`, at.Add(time.Duration(interval)*time.Second).Unix(), v.ID, v.Revision, at.Unix())
	if err != nil {
		return false, err
	}
	n, _ := result.RowsAffected()
	return n == 1, nil
}

func (s *Store) FinishProbeDDNS(ctx context.Context, v model.ProbeDDNSRecord, status, message, address string, synced *time.Time) error {
	result, err := s.db.ExecContext(ctx, `update probe_ddns_records set status=?,error=?,current_address=?,last_synced_at=? where id=? and revision=?`, status, message, address, timePtrString(synced), v.ID, v.Revision)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return nil
	}
	return nil
}

func (s *Store) ProbeDDNSByDomain(ctx context.Context, serverID int64, domain string) (model.ProbeDDNSRecord, error) {
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	row := s.db.QueryRowContext(ctx, `select `+probeDDNSColumns+` from probe_ddns_records where server_id=? and domain=? and enabled=1`, serverID, domain)
	return scanProbeDDNS(row)
}

func ValidateProbeDDNS(v *model.ProbeDDNSRecord) error {
	v.Name = strings.TrimSpace(v.Name)
	v.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(v.Domain)), ".")
	v.RecordTypes = strings.ToLower(strings.TrimSpace(v.RecordTypes))
	v.AddressFamily = strings.ToLower(strings.TrimSpace(v.AddressFamily))
	v.InterfaceName = strings.TrimSpace(v.InterfaceName)
	v.InterfaceAddress = strings.TrimSpace(v.InterfaceAddress)
	if v.ServerID <= 0 || v.Name == "" || utf8.RuneCountInString(v.Name) > 60 {
		return errors.New("请选择服务器并填写不超过 60 字的名称")
	}
	if v.DNSCredentialID == nil || *v.DNSCredentialID <= 0 {
		return errors.New("请选择域名服务账号")
	}
	if !probeDDNSDomain(v.Domain) {
		return errors.New("请填写有效的解析域名")
	}
	switch v.RecordTypes {
	case "a", "aaaa", "both":
	default:
		return errors.New("记录类型必须是 A、AAAA 或同时发布")
	}
	switch v.AddressFamily {
	case "", "ipv4", "ipv6":
	default:
		return errors.New("地址族无效")
	}
	if v.IntervalSeconds < 300 || v.IntervalSeconds > 86400 {
		return errors.New("DDNS 检查间隔为 5 分钟至 24 小时")
	}
	bound := v.InterfaceName != "" || v.InterfaceAddress != ""
	if bound && (v.InterfaceName == "" || v.InterfaceAddress == "") {
		return errors.New("绑定网卡时必须同时指定网卡名称和 Agent 报告的地址")
	}
	if bound && v.RecordTypes == "both" {
		return errors.New("绑定单个网卡地址时请只选择 A 或 AAAA")
	}
	if v.AddressFamily == "ipv4" && v.RecordTypes == "aaaa" {
		return errors.New("IPv4 过滤不能发布 AAAA 记录")
	}
	if v.AddressFamily == "ipv6" && v.RecordTypes == "a" {
		return errors.New("IPv6 过滤不能发布 A 记录")
	}
	return nil
}
