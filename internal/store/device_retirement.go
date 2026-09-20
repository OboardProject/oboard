package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"time"
)

const deviceRetirementSchema = `create table if not exists device_retirement_initial_grace (
 id integer primary key check(id=1), deadline text not null
);
create table if not exists device_retirement_grace_scopes (
 credential_id text primary key
);
create table if not exists device_retirement_reviews (
 user_id integer primary key,
 reason_code text not null,
 created_at text not null
);
create table if not exists device_retirement_batches (
 id integer primary key, state text not null, deadline text not null, actor text not null,
 reason text not null, created_at text not null, revoked_at text,
 deployment_version integer not null default 0
);
create table if not exists device_retirement_accounts (
 batch_id integer not null, user_id integer not null, decision text not null default 'pending',
 reason text not null default '', primary key(batch_id,user_id)
);
create table if not exists device_retirement_scopes (
 batch_id integer not null, user_id integer not null, credential_id text not null,
 primary key(batch_id,credential_id)
);
create table if not exists device_retirement_nodes (
 batch_id integer not null, server_id integer not null,
 primary key(batch_id,server_id)
);
create table if not exists device_retirement_targets (
 batch_id integer not null, server_id integer not null,
 authorization_revision integer not null default 0, users_revision integer not null default 0,
 primary key(batch_id,server_id)
)`

const restrictedDevicePredicate = `(status <> 'active' or subscription_suspended <> 0 or proxy_access_state <> 'active')`

// DeviceRetirementPreflight contains counts only, never token or credential material.
// It is a dry run: it does not revoke credentials, change users or queue deployments.
type DeviceRetirementPreflight struct {
	GraceDeadline               string `json:"grace_deadline,omitempty"`
	GraceState                  string `json:"grace_state"`
	LegacySubscriptions         int64  `json:"legacy_subscriptions"`
	LegacyCredentials           int64  `json:"legacy_credentials"`
	RestrictedObjects           int64  `json:"restricted_objects"`
	AffectedAccounts            int64  `json:"affected_accounts"`
	AffectedNodes               int64  `json:"affected_nodes"`
	ReviewAccounts              int64  `json:"review_accounts"`
	PendingNodeSync             int64  `json:"pending_node_sync"`
	NodeRevocationConfirmed     bool   `json:"node_revocation_confirmed"`
	MissingAccountSubscriptions int64  `json:"missing_account_subscriptions"`
	NodeScopeComplete           bool   `json:"node_scope_complete"`
	NodeScopeReason             string `json:"node_scope_reason,omitempty"`
}

func (s *Store) PreviewDeviceRetirement(ctx context.Context) (DeviceRetirementPreflight, error) {
	return previewDeviceRetirement(ctx, s.db)
}

// PreviewDeviceRetirementDatabase opens an existing database without migrations or writes.
func PreviewDeviceRetirementDatabase(ctx context.Context, path string) (DeviceRetirementPreflight, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return DeviceRetirementPreflight{}, err
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)"}}
	uri.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return DeviceRetirementPreflight{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return previewDeviceRetirement(ctx, db)
}

func previewDeviceRetirement(ctx context.Context, db interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}) (DeviceRetirementPreflight, error) {
	var out DeviceRetirementPreflight
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var reviewTable int
	if err := tx.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='device_retirement_reviews'`).Scan(&reviewTable); err != nil {
		return out, err
	}
	out.GraceState = "not_initialized"
	var graceTable int
	if err := tx.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='device_retirement_initial_grace'`).Scan(&graceTable); err != nil {
		return out, err
	}
	if graceTable != 0 {
		err := tx.QueryRowContext(ctx, `select min(deadline) from (select deadline from device_retirement_initial_grace where id=1 union all select deadline from device_retirement_batches) having count(*)>0`).Scan(&out.GraceDeadline)
		if err != nil && err != sql.ErrNoRows {
			return out, err
		}
		if out.GraceDeadline != "" {
			out.GraceState = "review_required"
			deadline, err := time.Parse(time.RFC3339Nano, out.GraceDeadline)
			if err != nil {
				return out, err
			}
			if !deadline.After(time.Now()) {
				out.GraceState = "expired_review_required"
			}
		}
	}
	reviewCount := "0"
	if reviewTable != 0 {
		reviewCount = "(select count(*) from device_retirement_reviews)"
	}
	err = tx.QueryRowContext(ctx, `with affected_nodes as (
 select i.server_id from proxy_credentials c join inbounds i on i.id=c.inbound_id where c.device_id_hash<>'' and c.status='active'
 union
 select coalesce(p.server_id,i.server_id) from proxy_credentials c join proxy_path_steps p on p.path_id=c.path_id left join inbounds i on i.id=p.inbound_id where c.device_id_hash<>'' and c.status='active' and coalesce(p.server_id,i.server_id) is not null
 ) select
 (select count(*) from user_devices),
 (select count(*) from proxy_credentials where device_id_hash<>'' and status='active'),
 (select count(*) from user_devices where `+restrictedDevicePredicate+`),
 (select count(*) from (select user_id from user_devices union select user_id from proxy_credentials where device_id_hash<>'')),
 (select count(*) from affected_nodes),
 `+reviewCount+`,
 (select count(*) from affected_nodes n left join configuration_sync_states s on s.server_id=n.server_id where coalesce(s.state,'unknown')<>'synced'),
 (select count(*) from users u where coalesce(u.subscription_token,'')='' and u.id in (select user_id from user_devices union select user_id from proxy_credentials where device_id_hash<>''))
 `).Scan(&out.LegacySubscriptions, &out.LegacyCredentials, &out.RestrictedObjects, &out.AffectedAccounts, &out.AffectedNodes, &out.ReviewAccounts, &out.PendingNodeSync, &out.MissingAccountSubscriptions)
	if out.AffectedAccounts > 0 {
		out.NodeScopeReason = "current_topology_only_no_historical_node_confirmation"
	}
	return out, err
}
