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
	if secret == "" {
		return errors.New("proxy credential encryption secret is required")
	}
	scopes := make(map[proxyCredentialScope]model.ProxyCredential, len(desired))
	for _, c := range desired {
		if c.UserID <= 0 || c.InboundID <= 0 || c.PathID < 0 || c.CredentialEpoch < 0 || (c.DeviceIDHash != "" && c.CredentialEpoch <= 0) || (c.DeviceIDHash == "" && c.CredentialEpoch != 0) {
			return errors.New("invalid proxy credential scope")
		}
		switch c.Protocol {
		case model.ProtocolVLESS, model.ProtocolHY2, model.ProtocolAnyTLS, model.ProtocolSS, model.ProtocolSocks, model.ProtocolSnell, model.ProtocolMieru, model.ProtocolSSH:
		default:
			return errors.New("invalid proxy credential protocol")
		}
		scopes[credentialScope(c)] = c
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	existing, err := listProxyCredentials(ctx, tx)
	if err != nil {
		return err
	}
	for _, c := range existing {
		if c.Status != "active" {
			continue
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
	if secret == "" {
		return nil, errors.New("proxy credential encryption secret is required")
	}
	out := append([]model.User(nil), users...)
	indexes := make(map[int64][]int, len(users))
	for i := range out {
		out[i].ProxyCredentials = []model.ProxyCredential{}
		indexes[out[i].ID] = append(indexes[out[i].ID], i)
	}
	rows, err := s.db.QueryContext(ctx, `select id,user_id,inbound_id,path_id,device_id_hash,credential_epoch,protocol,status,material_encrypted from proxy_credentials where status='active' order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c model.ProxyCredential
		var encrypted string
		if err := rows.Scan(&c.ID, &c.UserID, &c.InboundID, &c.PathID, &c.DeviceIDHash, &c.CredentialEpoch, &c.Protocol, &c.Status, &encrypted); err != nil {
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
