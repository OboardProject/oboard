package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const proxyCredentialSchema = `create table if not exists proxy_credentials (
 id text primary key,
 user_id integer not null,
 inbound_id integer not null,
 path_id integer not null,
 device_id_hash text not null,
 credential_epoch integer not null,
 protocol text not null,
 status text not null check(status in ('active','revoked')),
 material_encrypted text not null,
 created_at text not null,
 revoked_at text
)`

func (s *Store) ensureProxyCredentials(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, proxyCredentialSchema); err != nil {
		return err
	}
	statements := []string{
		`create unique index if not exists idx_proxy_credentials_active_scope on proxy_credentials(user_id,inbound_id,path_id,device_id_hash,credential_epoch,protocol) where status='active'`,
		`create trigger if not exists proxy_credentials_user_rotation after update of proxy_uuid,proxy_password on users when old.proxy_uuid<>new.proxy_uuid or old.proxy_password<>new.proxy_password begin update proxy_credentials set status='revoked',material_encrypted='',revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') where user_id=new.id and status='active'; end`,
		`create trigger if not exists proxy_credentials_user_delete after delete on users begin update proxy_credentials set status='revoked',material_encrypted='',revoked_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') where user_id=old.id and status='active'; end`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

type proxyCredentialScope struct {
	userID, inboundID, pathID, epoch int64
	device                           string
	protocol                         model.Protocol
}

func credentialScope(c model.ProxyCredential) proxyCredentialScope {
	return proxyCredentialScope{c.UserID, c.InboundID, c.PathID, c.CredentialEpoch, c.DeviceIDHash, c.Protocol}
}

type proxyCredentialMaterial struct {
	Username string `json:"username"`
	Password string `json:"password"`
	UUID     string `json:"uuid"`
}

func newProxyCredentialMaterial() (string, proxyCredentialMaterial, error) {
	var entropy [80]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", proxyCredentialMaterial{}, err
	}
	id := hex.EncodeToString(entropy[:16])
	// Lowercase hexadecimal is valid for SSH and Mieru without reducing the
	// username's 128-bit entropy; the password has independent 256-bit entropy.
	username := "u" + hex.EncodeToString(entropy[16:32])
	password := base64.RawURLEncoding.EncodeToString(entropy[32:64])
	uuid := entropy[64:80]
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	return id, proxyCredentialMaterial{username, password, fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])}, nil
}

// ReconcileProxyCredentials consumes the complete authorized scope set, never a
// per-server subset. Call only from serialized authorization workflows. Reports
// and subscription/configuration reads must not allocate or revive credentials.
func (s *Store) ReconcileProxyCredentials(ctx context.Context, secret string, desired []model.ProxyCredential) error {
	return s.reconcileProxyCredentialsAt(ctx, secret, desired, time.Now())
}

func (s *Store) reconcileProxyCredentialsAt(ctx context.Context, secret string, desired []model.ProxyCredential, at time.Time) error {
	if secret == "" {
		return errors.New("proxy credential encryption secret is required")
	}
	scopes := make(map[proxyCredentialScope]model.ProxyCredential, len(desired))
	authorized := make(map[proxyCredentialScope]bool, len(desired))
	for _, c := range desired {
		if c.DeviceIDHash != "" {
			return errors.New("device-scoped proxy credential issuance is no longer supported")
		}
		if c.UserID <= 0 || c.InboundID <= 0 || c.PathID < 0 || c.CredentialEpoch < 0 || (c.DeviceIDHash != "" && c.CredentialEpoch <= 0) || (c.DeviceIDHash == "" && c.CredentialEpoch != 0) {
			return errors.New("invalid proxy credential scope")
		}
		switch c.Protocol {
		case model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS, model.ProtocolSocks, model.ProtocolSnell, model.ProtocolMieru, model.ProtocolSSH:
		default:
			return errors.New("invalid proxy credential protocol")
		}
		scopes[credentialScope(c)] = c
		authorized[credentialScope(c)] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Persist ambiguity before allocating any account scope. The hold survives
	// later edits to legacy rows and never changes account access state.
	if _, err := tx.ExecContext(ctx, `insert into device_retirement_reviews(user_id,reason_code,created_at) select distinct user_id,'legacy_device_restriction',? from user_devices where `+restrictedDevicePredicate+` and not exists(select 1 from device_retirement_accounts a join device_retirement_batches b on b.id=a.batch_id where a.user_id=user_devices.user_id and a.decision='account_authorized' and b.state<>'review') on conflict(user_id) do update set reason_code='legacy_device_restriction'`, now()); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `select user_id from device_retirement_reviews where reason_code <> 'transition_pending'`)
	if err != nil {
		return err
	}
	held := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		held[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	existing, err := listProxyCredentials(ctx, tx)
	if err != nil {
		return err
	}
	initialGrace := false
	for _, c := range existing {
		if c.DeviceIDHash != "" && c.Status == "active" {
			// Freeze the upgrade window once; reconciliation and restarts cannot renew it.
			result, err := tx.ExecContext(ctx, `insert into device_retirement_initial_grace(id,deadline) select 1,? where not exists(select 1 from device_retirement_batches) on conflict(id) do nothing`, at.UTC().Add(14*24*time.Hour).Format(time.RFC3339Nano))
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			initialGrace = n == 1
			break
		}
	}
	for _, c := range existing {
		if c.Status != "active" {
			continue
		}
		if c.DeviceIDHash != "" {
			accountScope := credentialScope(c)
			accountScope.device, accountScope.epoch = "", 0
			if authorized[accountScope] {
				if _, err := tx.ExecContext(ctx, `insert into device_retirement_grace_scopes(credential_id) select ? where ? and exists(select 1 from user_devices where user_id=? and device_id_hash=? and credential_epoch=? and status='active' and proxy_access_state='active' and subscription_suspended=0) and not exists(select 1 from device_retirement_batches) on conflict(credential_id) do nothing`, c.ID, initialGrace, c.UserID, c.DeviceIDHash, c.CredentialEpoch); err != nil {
					return err
				}
				// Normal reconciliation may issue an authorized account credential,
				// but retirement of existing device material requires a separate review.
				if _, err := tx.ExecContext(ctx, `insert into device_retirement_reviews(user_id,reason_code,created_at) values(?,'transition_pending',?) on conflict(user_id) do nothing`, c.UserID, now()); err != nil {
					return err
				}
				continue
			}
		}
		scope := credentialScope(c)
		if _, ok := scopes[scope]; ok {
			delete(scopes, scope)
			continue
		}
		if _, err = tx.ExecContext(ctx, `update proxy_credentials set status='revoked',material_encrypted='',revoked_at=? where id=?`, now(), c.ID); err != nil {
			return err
		}
	}
	for _, c := range scopes {
		if c.DeviceIDHash == "" && held[c.UserID] {
			continue
		}
		id, material, err := newProxyCredentialMaterial()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(material)
		if err != nil {
			return err
		}
		encrypted, err := security.EncryptSecret(secret, "proxy-credential:"+id, string(payload))
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `insert into proxy_credentials(id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,protocol,status,material_encrypted,created_at) values(?,?,?,?,?,?,?,'active',?,?)`, id, c.UserID, c.InboundID, c.PathID, c.DeviceIDHash, c.CredentialEpoch, c.Protocol, encrypted, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type proxyCredentialQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listProxyCredentials(ctx context.Context, q proxyCredentialQuerier) ([]model.ProxyCredential, error) {
	rows, err := q.QueryContext(ctx, `select id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,protocol,status from proxy_credentials order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ProxyCredential{}
	for rows.Next() {
		var c model.ProxyCredential
		if err := rows.Scan(&c.ID, &c.UserID, &c.InboundID, &c.PathID, &c.DeviceIDHash, &c.CredentialEpoch, &c.Protocol, &c.Status); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func rewrapProxyCredentials(ctx context.Context, tx *sql.Tx, sourceSecret, targetSecret string) error {
	rows, err := tx.QueryContext(ctx, `select id,material_encrypted from proxy_credentials where status='active'`)
	if err != nil {
		return err
	}
	type item struct{ id, encrypted string }
	var items []item
	for rows.Next() {
		var v item
		if err := rows.Scan(&v.id, &v.encrypted); err != nil {
			rows.Close()
			return err
		}
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		purpose := "proxy-credential:" + v.id
		plain, err := security.DecryptSecret(sourceSecret, purpose, v.encrypted)
		if err != nil {
			return fmt.Errorf("restore proxy credential: %w", err)
		}
		encrypted, err := security.EncryptSecret(targetSecret, purpose, plain)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update proxy_credentials set material_encrypted=? where id=?`, encrypted, v.id); err != nil {
			return err
		}
	}
	return nil
}

// ListProxyCredentials enumerates authorization metadata without decrypting
// secrets, building configurations, assigning ports or advancing any revision.
func (s *Store) ListProxyCredentials(ctx context.Context) ([]model.ProxyCredential, error) {
	return listProxyCredentials(ctx, s.db)
}

// LoadProxyCredentials attaches the persisted material to account projections.
// An empty set remains empty: reads never derive or create authentication data.
func (s *Store) LoadProxyCredentials(ctx context.Context, secret string, users []model.User) ([]model.User, error) {
	return s.loadProxyCredentialsAt(ctx, secret, users, time.Now())
}

func (s *Store) loadProxyCredentialsAt(ctx context.Context, secret string, users []model.User, at time.Time) ([]model.User, error) {
	if secret == "" {
		return nil, errors.New("proxy credential encryption secret is required")
	}
	out := append([]model.User(nil), users...)
	indexes := make(map[int64][]int, len(users))
	for i := range out {
		out[i].ProxyCredentials = []model.ProxyCredential{}
		out[i].DeviceTransitionUntil = time.Time{}
		indexes[out[i].ID] = append(indexes[out[i].ID], i)
	}
	if len(indexes) == 0 {
		return out, nil
	}
	transitions, err := s.db.QueryContext(ctx, `select user_id,min(deadline) from (
 select a.user_id,b.deadline from device_retirement_accounts a join device_retirement_batches b on b.id=a.batch_id where b.state='transition' and a.decision='account_authorized'
 union all select c.user_id,g.deadline from device_retirement_grace_scopes r join proxy_credentials c on c.id=r.credential_id cross join device_retirement_initial_grace g
 union all select c.user_id,b.deadline from device_retirement_grace_scopes r join proxy_credentials c on c.id=r.credential_id cross join device_retirement_batches b
 ) group by user_id`)
	if err != nil {
		return nil, err
	}
	for transitions.Next() {
		var id int64
		var deadline string
		if err := transitions.Scan(&id, &deadline); err != nil {
			transitions.Close()
			return nil, err
		}
		until, err := time.Parse(time.RFC3339Nano, deadline)
		if err != nil {
			transitions.Close()
			return nil, err
		}
		if until.After(at) {
			for _, i := range indexes[id] {
				out[i].DeviceTransitionUntil = until
			}
		}
	}
	if err := transitions.Err(); err != nil {
		transitions.Close()
		return nil, err
	}
	transitions.Close()
	query := `select id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,protocol,status,material_encrypted,exists(select 1 from user_devices d where d.user_id=proxy_credentials.user_id and d.device_id_hash=proxy_credentials.device_id_hash and d.credential_epoch=proxy_credentials.credential_epoch and d.status='active' and d.proxy_access_state='active' and d.subscription_suspended=0) and (exists(select 1 from device_retirement_scopes r join device_retirement_batches b on b.id=r.batch_id join device_retirement_accounts a on a.batch_id=b.id and a.user_id=r.user_id where r.credential_id=proxy_credentials.id and r.user_id=proxy_credentials.user_id and b.state='transition' and a.decision='account_authorized' and (not exists(select 1 from device_retirement_initial_grace) or exists(select 1 from device_retirement_grace_scopes g where g.credential_id=proxy_credentials.id))) or (exists(select 1 from device_retirement_grace_scopes g where g.credential_id=proxy_credentials.id) and not exists(select 1 from device_retirement_batches b where b.state not in ('review','transition')) and not exists(select 1 from device_retirement_accounts a where a.user_id=proxy_credentials.user_id and a.decision='retain_restriction'))) from proxy_credentials where status='active'`
	var args []any
	if len(indexes) <= 128 {
		for id := range indexes {
			args = append(args, id)
		}
		query += ` and user_id in (` + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + `)`
	}
	rows, err := s.db.QueryContext(ctx, query+` order by id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c model.ProxyCredential
		var encrypted string
		if err := rows.Scan(&c.ID, &c.UserID, &c.InboundID, &c.PathID, &c.DeviceIDHash, &c.CredentialEpoch, &c.Protocol, &c.Status, &encrypted, &c.DeviceTransitionAllowed); err != nil {
			return nil, err
		}
		if len(indexes[c.UserID]) == 0 {
			continue
		}
		plain, err := security.DecryptSecret(secret, "proxy-credential:"+c.ID, encrypted)
		if err != nil {
			return nil, fmt.Errorf("decrypt proxy credential %s: %w", c.ID, err)
		}
		var material proxyCredentialMaterial
		if err := json.Unmarshal([]byte(plain), &material); err != nil {
			return nil, err
		}
		if material.Username == "" || material.Password == "" || material.UUID == "" {
			return nil, errors.New("incomplete proxy credential material")
		}
		c.Username, c.Password, c.UUID = material.Username, material.Password, material.UUID
		for _, i := range indexes[c.UserID] {
			out[i].ProxyCredentials = append(out[i].ProxyCredentials, c)
		}
	}
	return out, rows.Err()
}
