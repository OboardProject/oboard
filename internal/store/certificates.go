package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Store) CreateDNSCredential(ctx context.Context, v *model.DNSCredential) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `insert into dns_credentials(name,provider,zone_name,zone_id,config_encrypted,enabled,verified_at,last_error,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`, v.Name, v.Provider, v.ZoneName, v.ZoneID, v.ConfigEncrypted, boolInt(v.Enabled), timePtrString(v.VerifiedAt), v.LastError, ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	if err := replaceDNSCredentialZones(ctx, tx, v.ID, v.Zones, ts); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	v.Configured = v.ConfigEncrypted != ""
	return nil
}

func (s *Store) UpdateDNSCredential(ctx context.Context, v *model.DNSCredential) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `update dns_credentials set name=?,provider=?,zone_name=?,zone_id=?,config_encrypted=?,enabled=?,verified_at=?,last_error=?,updated_at=? where id=?`, v.Name, v.Provider, v.ZoneName, v.ZoneID, v.ConfigEncrypted, boolInt(v.Enabled), timePtrString(v.VerifiedAt), v.LastError, ts, v.ID); err != nil {
		return err
	}
	if err := replaceDNSCredentialZones(ctx, tx, v.ID, v.Zones, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceDNSCredentialZones(ctx context.Context, tx *sql.Tx, credentialID int64, zones []model.DNSCredentialZone, ts string) error {
	rows, err := tx.QueryContext(ctx, `select id from dns_credential_zones where credential_id=?`, credentialID)
	if err != nil {
		return err
	}
	existing := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		existing[id] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i := range zones {
		zone := &zones[i]
		if existing[zone.ID] {
			if _, err := tx.ExecContext(ctx, `update dns_credential_zones set zone_name=?,provider_zone_id=?,server_id=?,updated_at=? where id=? and credential_id=?`, zone.ZoneName, zone.ProviderZoneID, zone.ServerID, ts, zone.ID, credentialID); err != nil {
				return err
			}
			delete(existing, zone.ID)
		} else {
			res, err := tx.ExecContext(ctx, `insert into dns_credential_zones(credential_id,zone_name,provider_zone_id,server_id,created_at,updated_at) values(?,?,?,?,?,?)`, credentialID, zone.ZoneName, zone.ProviderZoneID, zone.ServerID, ts, ts)
			if err != nil {
				return err
			}
			zone.ID, _ = res.LastInsertId()
		}
		zone.CredentialID = credentialID
		zone.CreatedAt = parseTime(ts)
		zone.UpdatedAt = zone.CreatedAt
	}
	for id := range existing {
		if _, err := tx.ExecContext(ctx, `delete from dns_credential_zones where id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetDNSCredentialVerification(ctx context.Context, id int64, verifiedAt *time.Time, lastError string) error {
	_, err := s.db.ExecContext(ctx, `update dns_credentials set verified_at=?,last_error=?,updated_at=? where id=?`, timePtrString(verifiedAt), lastError, now(), id)
	return err
}

func (s *Store) ListDNSCredentials(ctx context.Context) ([]model.DNSCredential, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,provider,zone_name,zone_id,config_encrypted,enabled,verified_at,last_error,created_at,updated_at from dns_credentials order by name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanDNSCredentials(rows)
	if err != nil {
		return nil, err
	}
	if err := s.attachDNSCredentialZones(ctx, items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) GetDNSCredential(ctx context.Context, id int64) (*model.DNSCredential, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,provider,zone_name,zone_id,config_encrypted,enabled,verified_at,last_error,created_at,updated_at from dns_credentials where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanDNSCredentials(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	if err := s.attachDNSCredentialZones(ctx, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

func (s *Store) attachDNSCredentialZones(ctx context.Context, credentials []model.DNSCredential) error {
	if len(credentials) == 0 {
		return nil
	}
	byID := make(map[int64]*model.DNSCredential, len(credentials))
	for i := range credentials {
		credentials[i].Zones = []model.DNSCredentialZone{}
		byID[credentials[i].ID] = &credentials[i]
	}
	rows, err := s.db.QueryContext(ctx, `select id,credential_id,zone_name,provider_zone_id,server_id,created_at,updated_at from dns_credential_zones order by zone_name,id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var zone model.DNSCredentialZone
		var serverID sql.NullInt64
		var createdAt, updatedAt string
		if err := rows.Scan(&zone.ID, &zone.CredentialID, &zone.ZoneName, &zone.ProviderZoneID, &serverID, &createdAt, &updatedAt); err != nil {
			return err
		}
		if serverID.Valid {
			zone.ServerID = &serverID.Int64
		}
		zone.CreatedAt, zone.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
		if credential := byID[zone.CredentialID]; credential != nil {
			credential.Zones = append(credential.Zones, zone)
		}
	}
	return rows.Err()
}

func (s *Store) GetDNSCredentialZone(ctx context.Context, id int64) (*model.DNSCredentialZone, error) {
	var zone model.DNSCredentialZone
	var serverID sql.NullInt64
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `select id,credential_id,zone_name,provider_zone_id,server_id,created_at,updated_at from dns_credential_zones where id=?`, id).Scan(&zone.ID, &zone.CredentialID, &zone.ZoneName, &zone.ProviderZoneID, &serverID, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	if serverID.Valid {
		zone.ServerID = &serverID.Int64
	}
	zone.CreatedAt, zone.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	return &zone, nil
}

func (s *Store) ListDNSRecordMetadata(ctx context.Context, zoneID int64) (map[string]model.DNSRecord, error) {
	rows, err := s.db.QueryContext(ctx, `select provider_record_id,comment,server_id,inbound_id from dns_record_metadata where dns_zone_id=?`, zoneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]model.DNSRecord{}
	for rows.Next() {
		var record model.DNSRecord
		var serverID, inboundID sql.NullInt64
		if err := rows.Scan(&record.ID, &record.Comment, &serverID, &inboundID); err != nil {
			return nil, err
		}
		if serverID.Valid {
			record.ServerID = &serverID.Int64
		}
		if inboundID.Valid {
			record.InboundID = &inboundID.Int64
		}
		out[record.ID] = record
	}
	return out, rows.Err()
}

func (s *Store) UpsertDNSRecordMetadata(ctx context.Context, zoneID int64, record model.DNSRecord) error {
	_, err := s.db.ExecContext(ctx, `insert into dns_record_metadata(dns_zone_id,provider_record_id,comment,server_id,inbound_id,updated_at) values(?,?,?,?,?,?) on conflict(dns_zone_id,provider_record_id) do update set comment=excluded.comment,server_id=excluded.server_id,inbound_id=excluded.inbound_id,updated_at=excluded.updated_at`, zoneID, record.ID, record.Comment, record.ServerID, record.InboundID, now())
	return err
}

func (s *Store) DeleteDNSRecordMetadata(ctx context.Context, zoneID int64, recordID string) error {
	_, err := s.db.ExecContext(ctx, `delete from dns_record_metadata where dns_zone_id=? and provider_record_id=?`, zoneID, recordID)
	return err
}

func scanDNSCredentials(rows *sql.Rows) ([]model.DNSCredential, error) {
	var out []model.DNSCredential
	for rows.Next() {
		var v model.DNSCredential
		var enabled int
		var verified sql.NullString
		var createdAt, updatedAt string
		if err := rows.Scan(&v.ID, &v.Name, &v.Provider, &v.ZoneName, &v.ZoneID, &v.ConfigEncrypted, &enabled, &verified, &v.LastError, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		v.Configured = v.ConfigEncrypted != ""
		v.Enabled = enabled == 1
		if verified.Valid && verified.String != "" {
			t := parseTime(verified.String)
			v.VerifiedAt = &t
		}
		v.CreatedAt = parseTime(createdAt)
		v.UpdatedAt = parseTime(updatedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDNSCredential(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `delete from dns_credentials where id=?`, id)
	return err
}

func (s *Store) CreateCertificate(ctx context.Context, v *model.Certificate) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	domains, _ := json.Marshal(v.Domains)
	validation, _ := json.Marshal(v.ValidationRecords)
	res, err := s.db.ExecContext(ctx, `insert into certificates(name,primary_domain,domains_json,wildcard,challenge_type,dns_credential_id,issuance_server_id,acme_ca,account_email,google_eab_credential_id,eab_key_id,eab_hmac_key_encrypted,status,certificate_pem,fullchain_pem,private_key_encrypted,revision,not_before,not_after,auto_renew,validation_records_json,last_error,last_issued_at,last_renewal_attempt_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.Name, v.PrimaryDomain, string(domains), boolInt(v.Wildcard), v.ChallengeType, v.DNSCredentialID, v.IssuanceServerID, v.ACMECA, v.AccountEmail, v.GoogleEABCredentialID, v.EABKeyID, v.EABHMACKeyEncrypted, v.Status, v.CertificatePEM, v.FullchainPEM, v.PrivateKeyEncrypted, v.Revision, timePtrString(v.NotBefore), timePtrString(v.NotAfter), boolInt(v.AutoRenew), string(validation), v.LastError, timePtrString(v.LastIssuedAt), timePtrString(v.LastRenewalAttemptAt), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateCertificate(ctx context.Context, v *model.Certificate) error {
	domains, _ := json.Marshal(v.Domains)
	validation, _ := json.Marshal(v.ValidationRecords)
	_, err := s.db.ExecContext(ctx, `update certificates set name=?,primary_domain=?,domains_json=?,wildcard=?,challenge_type=?,dns_credential_id=?,issuance_server_id=?,acme_ca=?,account_email=?,google_eab_credential_id=?,eab_key_id=?,eab_hmac_key_encrypted=?,status=?,certificate_pem=?,fullchain_pem=?,private_key_encrypted=?,revision=?,not_before=?,not_after=?,auto_renew=?,validation_records_json=?,last_error=?,last_issued_at=?,last_renewal_attempt_at=?,updated_at=? where id=?`, v.Name, v.PrimaryDomain, string(domains), boolInt(v.Wildcard), v.ChallengeType, v.DNSCredentialID, v.IssuanceServerID, v.ACMECA, v.AccountEmail, v.GoogleEABCredentialID, v.EABKeyID, v.EABHMACKeyEncrypted, v.Status, v.CertificatePEM, v.FullchainPEM, v.PrivateKeyEncrypted, v.Revision, timePtrString(v.NotBefore), timePtrString(v.NotAfter), boolInt(v.AutoRenew), string(validation), v.LastError, timePtrString(v.LastIssuedAt), timePtrString(v.LastRenewalAttemptAt), now(), v.ID)
	return err
}

func (s *Store) ListCertificates(ctx context.Context) ([]model.Certificate, error) {
	rows, err := s.db.QueryContext(ctx, certificateSelectSQL+` order by primary_domain,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCertificates(rows)
}

func (s *Store) GetCertificate(ctx context.Context, id int64) (*model.Certificate, error) {
	rows, err := s.db.QueryContext(ctx, certificateSelectSQL+` where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanCertificates(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

const certificateSelectSQL = `select id,name,primary_domain,domains_json,wildcard,challenge_type,dns_credential_id,issuance_server_id,acme_ca,account_email,google_eab_credential_id,eab_key_id,eab_hmac_key_encrypted,status,certificate_pem,fullchain_pem,private_key_encrypted,revision,not_before,not_after,auto_renew,validation_records_json,last_error,last_issued_at,last_renewal_attempt_at,created_at,updated_at from certificates`

func scanCertificates(rows *sql.Rows) ([]model.Certificate, error) {
	var out []model.Certificate
	for rows.Next() {
		var v model.Certificate
		var wildcard, autoRenew int
		var dnsCredentialID, issuanceServerID, googleEABCredentialID sql.NullInt64
		var notBefore, notAfter, lastIssuedAt, lastRenewalAttemptAt sql.NullString
		var domainsJSON, validationJSON, createdAt, updatedAt string
		if err := rows.Scan(&v.ID, &v.Name, &v.PrimaryDomain, &domainsJSON, &wildcard, &v.ChallengeType, &dnsCredentialID, &issuanceServerID, &v.ACMECA, &v.AccountEmail, &googleEABCredentialID, &v.EABKeyID, &v.EABHMACKeyEncrypted, &v.Status, &v.CertificatePEM, &v.FullchainPEM, &v.PrivateKeyEncrypted, &v.Revision, &notBefore, &notAfter, &autoRenew, &validationJSON, &v.LastError, &lastIssuedAt, &lastRenewalAttemptAt, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(domainsJSON), &v.Domains)
		_ = json.Unmarshal([]byte(validationJSON), &v.ValidationRecords)
		v.Wildcard = wildcard == 1
		v.AutoRenew = autoRenew == 1
		v.EABConfigured = googleEABCredentialID.Valid || (v.EABKeyID != "" && v.EABHMACKeyEncrypted != "")
		if dnsCredentialID.Valid {
			v.DNSCredentialID = &dnsCredentialID.Int64
		}
		if issuanceServerID.Valid {
			v.IssuanceServerID = &issuanceServerID.Int64
		}
		if googleEABCredentialID.Valid {
			v.GoogleEABCredentialID = &googleEABCredentialID.Int64
		}
		setCertificateTime(&v.NotBefore, notBefore)
		setCertificateTime(&v.NotAfter, notAfter)
		setCertificateTime(&v.LastIssuedAt, lastIssuedAt)
		setCertificateTime(&v.LastRenewalAttemptAt, lastRenewalAttemptAt)
		v.CreatedAt = parseTime(createdAt)
		v.UpdatedAt = parseTime(updatedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}

func setCertificateTime(target **time.Time, value sql.NullString) {
	if value.Valid && value.String != "" {
		t := parseTime(value.String)
		*target = &t
	}
}

func (s *Store) DeleteCertificate(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `delete from certificates where id=?`, id)
	return err
}

func (s *Store) UpsertInboundCertificateBinding(ctx context.Context, v *model.InboundCertificateBinding) error {
	ts := now()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = parseTime(ts)
	}
	v.UpdatedAt = parseTime(ts)
	_, err := s.db.ExecContext(ctx, `insert into inbound_certificate_bindings(inbound_id,certificate_id,mode,server_name,created_at,updated_at) values(?,?,?,?,?,?) on conflict(inbound_id) do update set certificate_id=excluded.certificate_id,mode=excluded.mode,server_name=excluded.server_name,updated_at=excluded.updated_at`, v.InboundID, v.CertificateID, v.Mode, v.ServerName, ts, ts)
	return err
}

func (s *Store) ListInboundCertificateBindings(ctx context.Context) ([]model.InboundCertificateBinding, error) {
	rows, err := s.db.QueryContext(ctx, `select inbound_id,certificate_id,mode,server_name,created_at,updated_at from inbound_certificate_bindings order by inbound_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInboundCertificateBindings(rows)
}

func scanInboundCertificateBindings(rows *sql.Rows) ([]model.InboundCertificateBinding, error) {
	var out []model.InboundCertificateBinding
	for rows.Next() {
		var v model.InboundCertificateBinding
		var certificateID sql.NullInt64
		var createdAt, updatedAt string
		if err := rows.Scan(&v.InboundID, &certificateID, &v.Mode, &v.ServerName, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if certificateID.Valid {
			v.CertificateID = &certificateID.Int64
		}
		v.CreatedAt = parseTime(createdAt)
		v.UpdatedAt = parseTime(updatedAt)
		out = append(out, v)
	}
	return out, rows.Err()
}
