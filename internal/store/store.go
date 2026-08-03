package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/OboardProject/oboard/internal/model"
)

type Store struct{ db *sql.DB }

const (
	UserGroupSystemAdmins = "administrators"
	UserGroupSystemUsers  = "users"
	bootstrapAdminSetting = "system.bootstrap_admin_user_id"
	configVersionSetting  = "system.config_version_sequence"
)

const serverSelectSQL = `select id,name,coalesce(agent_id,''),coalesce(agent_token_hash,''),chain_secret,coalesce(enrollment_hash,''),enrollment_expires_at,entry_address,coalesce(public_ipv4,''),coalesce(public_ipv6,''),coalesce(interface_ipv6,''),coalesce(region_code,''),coalesce(detected_region_code,''),coalesce(region_mode,'auto'),coalesce(entry_ip_mode,'auto'),listen_ip,coalesce(listen_mode,'auto'),ip_stack,udp_inbound_mode,mtu_mode,mtu_value,mtu_probe_host,mtu_probe_port,mtu_overhead_bytes,bbr_enabled,port_range_start,port_range_end,status,os,coalesce(distro_id,''),coalesce(distro_version,''),coalesce(distro_name,''),coalesce(libc,''),coalesce(service_manager,''),coalesce(package_manager,''),arch,kernel,cpu,memory_bytes,cpu_usage_percent,memory_used_bytes,memory_total_bytes,agent_memory_bytes,disk_bytes,coalesce(agent_version,''),coalesce(agent_build,''),sing_box_version,connection_audit_enabled,last_seen_at,created_at,updated_at from servers`

const serverTelemetrySelectSQL = `select server_id,monitoring_mode,traffic_reset_mode,traffic_reset_day,connectivity_probe_enabled,time_correction_mode,time_check_status,time_offset_ms,time_effective_offset_ms,time_check_source,time_check_error,time_logical_active,time_unsupported_paths_json,time_checked_at,period_start,period_end,traffic_upload_bytes,traffic_download_bytes,network_upload_bps,network_download_bps,last_reported_at,connectivity_available,connectivity_latency_ms,connectivity_checked_at,connectivity_error,offline_notify_enabled,offline_after_seconds from server_telemetry`

const userSelectSQL = `select u.id,u.username,u.nickname,u.password_hash,coalesce(u.session_version,0),u.role,u.status,u.proxy_uuid,u.proxy_password,coalesce(sua.random_id,''),u.speed_limit_mbps,u.traffic_limit_bytes,u.traffic_used_bytes,u.traffic_reset_mode,u.traffic_reset_day,coalesce(u.subscription_token,''),coalesce(p.burn_after_read,0),p.burned_at,coalesce(a.enabled,0),coalesce(a.public_key,''),coalesce(sa.suspended,0),sa.suspended_at,coalesce(sa.suspension_reason,''),u.created_at,u.updated_at from users u left join ssh_user_aliases sua on sua.user_id=u.id left join subscription_token_policies p on p.user_id=u.id left join subscription_age_keys a on a.user_id=u.id left join subscription_access_states sa on sa.user_id=u.id`

func Open(path string) (*Store, error) {
	return open(path, false)
}

func OpenForRestore(path string) (*Store, error) {
	return open(path, true)
}

func open(path string, restore bool) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), `pragma busy_timeout=5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(context.Background(), `pragma foreign_keys=on`); err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background(), restore); err != nil {
		_ = db.Close()
		return nil, err
	}
	// SQLite may create the file with the process umask. Tighten owner-only
	// mode after open so session secrets, encrypted DNS tokens, and certificate
	// private keys are not world-readable on multi-user hosts.
	if err := secureSQLiteFile(path); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func secureSQLiteFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return nil
	}
	// DSN forms such as "file:path?..." are not regular files.
	if strings.Contains(path, "?") || strings.HasPrefix(path, "file:") {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	return os.Chmod(path, 0o600)
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) CheckHealth(ctx context.Context) error {
	for _, query := range []string{
		serverSelectSQL + ` limit 0`,
		serverTelemetrySelectSQL + ` limit 0`,
	} {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	return s.migrate(ctx, false)
}

func (s *Store) migrate(ctx context.Context, restore bool) error {
	legacy, err := s.legacyDNSSchemaPresent(ctx)
	if err != nil {
		return err
	}
	if legacy && restore {
		return errors.New("backup uses a retired DNS schema and must be restored with a compatible Controller version first")
	}
	if legacy {
		if err := s.resetLegacyDNSSchema(ctx); err != nil {
			return err
		}
	}
	schema := []string{
		`create table if not exists app_settings (key text primary key, value text not null, updated_at text not null)`,
		`create table if not exists controller_backups (id text primary key, name text not null, origin text not null, local_path text not null default '', local_status text not null default 'available', remote_key text not null default '', remote_target text not null default '', remote_status text not null default 'disabled', remote_error text not null default '', size_bytes integer not null default 0, source_version text not null default '', format_version integer not null default 1, protected integer not null default 0, created_at text not null, updated_at text not null)`,
		`create table if not exists rate_limits (key_hash text primary key, window_start text not null, count integer not null, updated_at text not null)`,
		`create table if not exists api_principals (id text primary key, owner_user_id integer references users(id) on delete set null, name text not null, type text not null, enabled integer not null default 1, scopes_json text not null default '[]', resource_filter_json text not null default '{}', allowed_cidrs_json text not null default '[]', rate_limit_per_minute integer not null default 60, max_concurrency integer not null default 4, expires_at text, last_used_at text, created_at text not null, updated_at text not null)`,
		`create table if not exists api_tokens (id text primary key, principal_id text not null references api_principals(id) on delete cascade, token_hash text not null unique, prefix text not null, expires_at text not null, revoked_at text, last_used_at text, created_at text not null)`,
		`create table if not exists oauth_clients (id text primary key, name text not null, redirect_uris_json text not null default '[]', allowed_scopes_json text not null default '[]', client_metadata_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists oauth_authorization_codes (code_hash text primary key, client_id text not null references oauth_clients(id) on delete cascade, user_id integer not null references users(id) on delete cascade, principal_id text not null references api_principals(id) on delete cascade, redirect_uri text not null, scopes_json text not null, resource text not null, code_challenge text not null, expires_at text not null, created_at text not null)`,
		`create table if not exists oauth_access_tokens (token_hash text primary key, principal_id text not null references api_principals(id) on delete cascade, client_id text not null references oauth_clients(id) on delete cascade, user_id integer not null references users(id) on delete cascade, scopes_json text not null, resource text not null, expires_at text not null, revoked_at text, created_at text not null)`,
		`create table if not exists oauth_refresh_tokens (token_hash text primary key, family_id text not null, principal_id text not null references api_principals(id) on delete cascade, client_id text not null references oauth_clients(id) on delete cascade, user_id integer not null references users(id) on delete cascade, scopes_json text not null, resource text not null, expires_at text not null, consumed_at text, revoked_at text, created_at text not null)`,
		`create table if not exists approval_policies (id text primary key, principal_id text not null references api_principals(id) on delete cascade, capability text not null, resource_filter_json text not null default '{}', mode text not null, allow_risk4 integer not null default 0, expires_at text, created_at text not null, updated_at text not null, unique(principal_id,capability))`,
		`create table if not exists automation_changesets (id text primary key, principal_id text not null, actor_user_id integer references users(id) on delete set null, status text not null, reason text not null default '', idempotency_key text not null, base_revisions_json text not null default '{}', plan_hash text not null default '', risk_class integer not null default 0, auto_apply integer not null default 0, validation_json text not null default '{}', blast_radius_json text not null default '{}', result_json text not null default '{}', expires_at text not null, created_at text not null, updated_at text not null, validated_at text, approved_at text, applied_at text, completed_at text, unique(principal_id,idempotency_key))`,
		`create table if not exists automation_operations (id text primary key, changeset_id text not null references automation_changesets(id) on delete cascade, position integer not null, capability text not null, input_json text not null, secret_refs_json text not null default '[]', resource_refs_json text not null default '{}', risk_class integer not null default 0, status text not null default 'pending', result_json text not null default '{}', error_code text not null default '', error_message text not null default '', created_at text not null, completed_at text, unique(changeset_id,position))`,
		`create table if not exists automation_approvals (id text primary key, changeset_id text not null references automation_changesets(id) on delete cascade, approver_id integer not null references users(id) on delete restrict, decision text not null, plan_hash text not null, comment text not null default '', approved_risk integer not null default 0, created_at text not null)`,
		`create table if not exists automation_secret_inputs (id text primary key, changeset_id text not null references automation_changesets(id) on delete cascade, purpose text not null, value_encrypted text not null, expires_at text not null, consumed_at text, created_at text not null)`,
		`create table if not exists tool_call_audits (id text primary key, principal_id text not null, client_name text not null default '', model_provider text not null default '', capability text not null, scope text not null default '', data_classification text not null, affected_resources_json text not null default '{}', approval_id text not null default '', request_id text not null, arguments_hash text not null, result text not null, source_ip text not null, created_at text not null)`,
		`create table if not exists ai_providers (id text primary key, name text not null, base_url text not null, model text not null, api_format text not null default 'chat_completions', credential_encrypted text not null default '', enabled integer not null default 0, allow_raw_audit integer not null default 0, daily_token_limit integer not null default 0, last_used_at text, created_at text not null, updated_at text not null)`,
		`create table if not exists ai_audit_reviews (id text primary key, request_id text not null unique, provider_id text not null references ai_providers(id) on delete restrict, requested_by integer not null references users(id) on delete restrict, status text not null, scope_json text not null, evidence_types_json text not null, window_started_at text not null, window_ended_at text not null, snapshot_at text not null, privacy_mode text not null, resolved_user_ids_json text not null default '[]', resolved_server_ids_json text not null default '[]', final_output_json text not null default '{}', error text not null default '', input_tokens integer not null default 0, output_tokens integer not null default 0, created_at text not null, updated_at text not null, completed_at text)`,
		`create table if not exists ai_audit_review_evidence (ref text not null, review_id text not null references ai_audit_reviews(id) on delete cascade, kind text not null, user_id integer references users(id) on delete set null, server_id integer references servers(id) on delete set null, payload_json text not null, created_at text not null, primary key(review_id,ref))`,
		`create table if not exists ai_audit_review_jobs (id text primary key, review_id text not null references ai_audit_reviews(id) on delete cascade, provider_id text not null references ai_providers(id) on delete restrict, stage integer not null, position integer not null, kind text not null, status text not null, input_json text not null, output_json text not null default '{}', error text not null default '', error_detail text not null default '', attempts integer not null default 0, input_tokens integer not null default 0, output_tokens integer not null default 0, lease_owner text not null default '', lease_until text, created_at text not null, updated_at text not null, completed_at text, unique(review_id,stage,position))`,
		`create table if not exists audit_feature_snapshots (id text primary key, user_id integer not null references users(id) on delete cascade, window text not null, window_started_at text not null, window_ended_at text not null, feature_version integer not null, rule_score integer not null, anomaly_score integer, features_json text not null, fingerprint text not null, created_at text not null)`,
		`create table if not exists audit_incidents (id text primary key, user_id integer not null references users(id) on delete cascade, status text not null, classification text not null default '', severity text not null, rule_score integer not null, anomaly_score integer, fingerprint text not null, latest_snapshot_id text references audit_feature_snapshots(id) on delete set null, opened_at text not null, updated_at text not null, resolved_at text)`,
		`create table if not exists operator_feedback (id text primary key, incident_id text not null references audit_incidents(id) on delete cascade, actor_user_id integer not null references users(id) on delete restrict, label text not null, comment text not null default '', created_at text not null)`,
		`create table if not exists event_outbox (id text primary key, topic text not null, aggregate_id text not null, payload_json text not null, status text not null default 'pending', attempts integer not null default 0, available_at text not null, lease_owner text not null default '', lease_until text, created_at text not null, completed_at text)`,
		`create unique index if not exists idx_audit_incident_fingerprint on audit_incidents(fingerprint)`,
		`create index if not exists idx_audit_feature_user_window on audit_feature_snapshots(user_id,window,created_at desc)`,
		`create index if not exists idx_ai_audit_reviews_created on ai_audit_reviews(created_at desc)`,
		`create index if not exists idx_ai_audit_review_jobs_queue on ai_audit_review_jobs(status,created_at)`,
		`create index if not exists idx_ai_audit_review_evidence_review on ai_audit_review_evidence(review_id,kind,ref)`,
		`create table if not exists users (id integer primary key autoincrement, username text not null unique, nickname text not null default '', password_hash text not null, session_version integer not null default 0, role text not null, status text not null, proxy_uuid text not null, proxy_password text not null, speed_limit_mbps integer not null default 0, traffic_limit_bytes integer not null default 0, traffic_used_bytes integer not null default 0, traffic_reset_mode text not null default 'monthly', traffic_reset_day integer not null default 1, subscription_token text unique, created_at text not null, updated_at text not null)`,
		`create table if not exists ssh_user_aliases (user_id integer primary key references users(id) on delete cascade, random_id text not null unique, created_at text not null)`,
		`create table if not exists subscription_token_policies (user_id integer primary key references users(id) on delete cascade, burn_after_read integer not null default 0, burned_at text, updated_at text not null)`,
		`create table if not exists subscription_one_time_tokens (token text primary key, user_id integer not null references users(id) on delete cascade, created_at text not null)`,
		`create table if not exists subscription_age_keys (user_id integer primary key references users(id) on delete cascade, enabled integer not null default 0, public_key text not null default '', updated_at text not null)`,
		`create table if not exists subscription_pull_audits (id integer primary key autoincrement, user_id integer not null references users(id) on delete cascade, source_ip text not null, source_country_code text not null default '', source_country text not null default '', source_province text not null default '', source_city text not null default '', source_isp text not null default '', geo_database_revision text not null default '', user_agent text not null default '', client_name text not null default '', format text not null default '', profile_id integer, age_encrypted integer not null default 0, token_kind text not null default '', outcome text not null, reason text not null default '', risk_eligible integer not null default 0, requested_at text not null, created_at text not null)`,
		`create table if not exists subscription_access_states (user_id integer primary key references users(id) on delete cascade, suspended integer not null default 0, suspended_at text, suspension_reason text not null default '', trigger_audit_id integer references subscription_pull_audits(id) on delete set null, trigger_snapshot_json text not null default '', evaluation_started_at text not null, resumed_at text, resumed_by integer references users(id) on delete set null, updated_at text not null)`,
		`create table if not exists user_authentication (user_id integer primary key references users(id) on delete cascade, totp_enabled integer not null default 0, totp_secret_encrypted text not null default '', recovery_code_hashes_json text not null default '[]', totp_last_used_step integer not null default -1, webauthn_user_handle text unique, updated_at text not null)`,
		`create table if not exists passkey_credentials (id integer primary key autoincrement, user_id integer not null references users(id) on delete cascade, name text not null, credential_id text not null unique, credential_json text not null, created_at text not null, last_used_at text)`,
		`create table if not exists auth_challenges (token_hash text primary key, kind text not null, user_id integer references users(id) on delete cascade, data_encrypted text not null, expires_at text not null, created_at text not null)`,
		`create table if not exists revoked_user_sessions (token_hash text primary key, user_id integer not null references users(id) on delete cascade, expires_at text not null, created_at text not null)`,
		`create table if not exists ssh_server_host_keys (server_id integer primary key references servers(id) on delete cascade, public_key text not null, fingerprint text not null, config_version integer not null, updated_at text not null)`,
		`create table if not exists ssh_deployment_plans (server_id integer primary key references servers(id) on delete cascade, plan_digest text not null, config_version integer not null, updated_at text not null)`,
		`create table if not exists ssh_password_deployments (server_id integer not null references servers(id) on delete cascade, user_id integer not null references users(id) on delete cascade, password_digest text not null, config_version integer not null, updated_at text not null, primary key(server_id,user_id))`,
		`create table if not exists servers (id integer primary key autoincrement, name text not null, agent_id text unique, agent_token_hash text, chain_secret text not null, enrollment_hash text, enrollment_expires_at text, entry_address text, public_ipv4 text not null default '', public_ipv6 text not null default '', region_code text not null default '', detected_region_code text not null default '', region_mode text not null default 'auto', entry_ip_mode text not null default 'auto', listen_ip text, ip_stack text not null default 'auto', udp_inbound_mode text not null default 'allow', mtu_mode text not null default 'detect', mtu_value integer not null default 0, mtu_probe_host text not null default '1.1.1.1', mtu_probe_port integer not null default 443, mtu_overhead_bytes integer not null default 0, bbr_enabled integer not null default 0, port_range_start integer not null default 10000, port_range_end integer not null default 20000, status text not null, os text, distro_id text not null default '', distro_version text not null default '', distro_name text not null default '', libc text not null default '', service_manager text not null default '', package_manager text not null default '', arch text, kernel text, cpu text, memory_bytes integer not null default 0, cpu_usage_percent real not null default 0, memory_used_bytes integer not null default 0, memory_total_bytes integer not null default 0, agent_memory_bytes integer not null default 0, disk_bytes integer not null default 0, agent_version text not null default '', agent_build text not null default '', sing_box_version text, connection_audit_enabled integer not null default 0, last_seen_at text, created_at text not null, updated_at text not null)`,
		`create table if not exists dns_credentials (id integer primary key autoincrement, name text not null unique, provider text not null, zone_name text not null, zone_id text not null default '', config_encrypted text not null, enabled integer not null default 1, verified_at text, last_error text not null default '', created_at text not null, updated_at text not null)`,
		`create table if not exists dns_credential_zones (id integer primary key autoincrement, credential_id integer not null references dns_credentials(id) on delete cascade, zone_name text not null, provider_zone_id text not null default '', server_id integer references servers(id) on delete set null, created_at text not null, updated_at text not null)`,
		`create table if not exists dns_record_metadata (dns_zone_id integer not null references dns_credential_zones(id) on delete cascade, provider_record_id text not null, comment text not null default '', server_id integer references servers(id) on delete set null, inbound_id integer references inbounds(id) on delete set null, updated_at text not null, primary key(dns_zone_id,provider_record_id))`,
		`create table if not exists inbounds (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, name text not null, protocol text not null, listen_ip text not null, port integer not null, entry_ip_mode text not null default 'auto', external_ip text not null default '', dns_sync_enabled integer not null default 0, dns_credential_id integer references dns_credentials(id) on delete set null, dns_domain text not null default '', dns_proxy_enabled integer not null default 0, dns_record_types text not null default 'auto', ddns_enabled integer not null default 0, ddns_interval_seconds integer not null default 300, dns_sync_status text not null default '', dns_sync_error text not null default '', dns_last_synced_at text, tls integer not null default 0, config_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists google_eab_credentials (id integer primary key autoincrement, key_id text not null unique, remark text not null default '', hmac_key_encrypted text not null, created_at text not null, updated_at text not null)`,
		`create table if not exists certificates (id integer primary key autoincrement, name text not null, primary_domain text not null, domains_json text not null default '[]', wildcard integer not null default 0, challenge_type text not null, dns_credential_id integer references dns_credentials(id) on delete set null, issuance_server_id integer references servers(id) on delete set null, acme_ca text not null default 'letsencrypt', account_email text not null default '', google_eab_credential_id integer references google_eab_credentials(id) on delete restrict, eab_key_id text not null default '', eab_hmac_key_encrypted text not null default '', status text not null default 'pending', certificate_pem text not null default '', fullchain_pem text not null default '', private_key_encrypted text not null default '', revision text not null default '', not_before text, not_after text, auto_renew integer not null default 1, validation_records_json text not null default '[]', last_error text not null default '', last_issued_at text, last_renewal_attempt_at text, created_at text not null, updated_at text not null)`,
		`create table if not exists inbound_certificate_bindings (inbound_id integer primary key references inbounds(id) on delete cascade, certificate_id integer references certificates(id) on delete set null, mode text not null default 'auto', server_name text not null default '', created_at text not null, updated_at text not null)`,
		`create table if not exists inbound_users (id integer primary key autoincrement, inbound_id integer not null references inbounds(id) on delete cascade, user_id integer not null references users(id) on delete cascade, enabled integer not null default 1, created_at text not null, updated_at text not null, unique(inbound_id,user_id))`,
		`create table if not exists user_groups (id integer primary key autoincrement, name text not null unique, description text not null default '', role text not null default 'viewer', system_key text not null default '', enabled integer not null default 1, speed_limit_mbps integer not null default 0, traffic_limit_bytes integer not null default 0, traffic_reset_mode text not null default 'monthly', traffic_reset_day integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists user_group_members (id integer primary key autoincrement, group_id integer not null references user_groups(id) on delete cascade, user_id integer not null references users(id) on delete cascade, enabled integer not null default 1, created_at text not null, updated_at text not null, unique(group_id,user_id))`,
		`create table if not exists inbound_access_grants (id integer primary key autoincrement, subject_type text not null, subject_id integer not null, scope_type text not null, server_id integer references servers(id) on delete cascade, inbound_id integer references inbounds(id) on delete cascade, enabled integer not null default 1, created_at text not null, updated_at text not null, unique(subject_type,subject_id,scope_type,server_id,inbound_id))`,
		`create table if not exists outbounds (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, next_server_id integer references servers(id) on delete set null, name text not null, protocol text not null, target_address text not null, target_port integer not null, config_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists routing_rules (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, name text not null, priority integer not null default 100, match_json text not null default '{}', action text not null, outbound_id integer references outbounds(id) on delete set null, external_outbound_id integer references external_outbounds(id) on delete set null, target_server_id integer references servers(id) on delete set null, outbound_tag text not null default '', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists external_outbounds (id integer primary key autoincrement, server_id integer references servers(id) on delete set null, name text not null, protocol text not null, scope text not null default 'global', target_address text not null default '', target_port integer not null default 0, config_json text not null default '{}', region_mode text not null default 'auto', region_code text not null default '', expose_to_users integer not null default 0, enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists external_outbound_access_grants (id integer primary key autoincrement, external_outbound_id integer not null references external_outbounds(id) on delete cascade, subject_type text not null, subject_id integer not null, enabled integer not null default 1, created_at text not null, updated_at text not null, unique(external_outbound_id,subject_type,subject_id))`,
		`create table if not exists proxy_paths (id integer primary key autoincrement, inbound_id integer not null references inbounds(id) on delete cascade, kind text not null default 'chain', branch_source_step_id integer references proxy_path_steps(id) on delete set null, name_mode text not null default 'auto', name_template_json text not null default '[]', exit_region_mode text not null default 'auto', exit_region_code text not null default '', secret text not null default '', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists proxy_path_steps (id integer primary key autoincrement, path_id integer not null references proxy_paths(id) on delete cascade, position integer not null, node_type text not null, transport_mode text not null default 'singbox', processing_role integer not null default 0, server_id integer references servers(id) on delete set null, inbound_id integer references inbounds(id) on delete set null, external_outbound_id integer references external_outbounds(id) on delete set null, config_json text not null default '{}', created_at text not null, updated_at text not null)`,
		`create table if not exists proxy_path_port_allocations (id integer primary key autoincrement, kind text not null, scope_key text not null, server_id integer not null references servers(id) on delete cascade, port integer not null, created_at text not null, updated_at text not null, unique(kind,scope_key,server_id))`,
		`create table if not exists warp_profiles (id integer primary key autoincrement, server_id integer not null unique references servers(id) on delete cascade, name text not null, status text not null default 'needed', config_json text not null default '{}', mtu integer not null default 0, dns_strategy text not null default '', last_requested_at text, error text not null default '', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists dns_lists (id integer primary key autoincrement, name text not null unique, kind text not null, revision integer not null default 1, candidates_json text not null, enabled integer not null default 1, protected integer not null default 0, created_at text not null, updated_at text not null)`,
		`create table if not exists server_dns_policies (server_id integer primary key references servers(id) on delete cascade, encrypted_list_id integer not null references dns_lists(id) on delete restrict, bootstrap_list_id integer not null references dns_lists(id) on delete restrict, revision integer not null default 1, strategy text not null default 'auto', auto_test text not null default 'first_apply', test_interval_seconds integer not null default 3600, encrypted_selected_json text not null default '[]', bootstrap_selected_json text not null default '[]', encrypted_selection_revision integer not null default 0, bootstrap_selection_revision integer not null default 0, last_attempt_at text, last_success_at text, last_error text not null default '', needs_benchmark integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists port_forwards (id integer primary key autoincrement, name text not null, source_server_id integer not null references servers(id) on delete cascade, target_server_id integer not null references servers(id) on delete cascade, listen_ip text not null default '', listen_port integer not null, target_address text not null default '', target_port integer not null, protocol text not null default 'tcp', backend text not null default 'auto', probe_mode text not null default 'apply', probe_interval_seconds integer not null default 300, sample_rate real not null default 0, priority integer not null default 100, config_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists tunnels (id integer primary key autoincrement, name text not null, source_server_id integer not null references servers(id) on delete cascade, target_server_id integer not null references servers(id) on delete cascade, type text not null, local_address text not null default '', peer_address text not null default '', listen_port integer not null default 0, target_endpoint text not null default '', target_port integer not null default 0, priority integer not null default 100, config_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists agent_tasks (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, type text not null, payload_json text not null, status text not null, result_json text not null default '{}', config_version integer not null default 0, nonce text not null, created_at text not null, updated_at text not null, completed_at text)`,
		`create table if not exists proxy_path_egress_results (path_id integer primary key references proxy_paths(id) on delete cascade, external_outbound_id integer not null references external_outbounds(id) on delete cascade, owner_server_id integer not null references servers(id) on delete cascade, topology_fingerprint text not null, config_version integer not null default 0, task_id integer references agent_tasks(id) on delete set null, status text not null default 'pending', last_exit_ip text not null default '', last_region_code text not null default '', geo_database_revision text not null default '', last_error text not null default '', last_attempt_at text, last_success_at text, created_at text not null, updated_at text not null)`,
		`create table if not exists deployment_failure_dismissals (config_version integer primary key, actor_id integer not null, dismissed_at text not null)`,
		`create table if not exists audit_logs (id integer primary key autoincrement, actor_id integer references users(id) on delete set null, action text not null, target text not null, detail text not null, ip text not null, created_at text not null)`,
		`create table if not exists traffic_stats (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, user_id integer references users(id) on delete cascade, inbound_id integer references inbounds(id) on delete set null, upload_bytes integer not null, download_bytes integer not null, created_at text not null)`,
		`create table if not exists traffic_periods (id integer primary key autoincrement, user_id integer not null references users(id) on delete cascade, period_key text not null, started_at text not null, ends_at text not null, upload_bytes integer not null default 0, download_bytes integer not null default 0, traffic_limit_bytes integer not null default 0, state text not null default 'active', updated_at text not null, unique(user_id,period_key))`,
		`create table if not exists traffic_reports (report_id text primary key, server_id integer not null references servers(id) on delete cascade, user_id integer not null references users(id) on delete cascade, inbound_id integer references inbounds(id) on delete set null, path_id integer references proxy_paths(id) on delete set null, period_key text not null, upload_bytes integer not null, download_bytes integer not null, started_at text not null, ended_at text not null, created_at text not null)`,
		`create table if not exists connection_audit_reports (report_id text primary key, server_id integer not null references servers(id) on delete cascade, user_id integer not null references users(id) on delete cascade, inbound_id integer references inbounds(id) on delete set null, path_id integer references proxy_paths(id) on delete set null, source_ip text not null, source_geo_code text not null default '', source_country_code text not null default '', source_country text not null default '', source_province text not null default '', source_city text not null default '', source_isp text not null default '', geo_database_revision text not null default '', network text not null, destination text not null default '', destination_port integer not null default 0, outbound_tag text not null default '', outbound_type text not null default '', connection_count integer not null, closed_count integer not null default 0, duration_total_ms integer not null default 0, duration_max_ms integer not null default 0, active_peak integer not null default 0, active_at_end integer not null default 0, collection_generation integer not null default 0, bucket_capacity integer not null default 1, dropped_bucket_count integer not null default 0, collection_started_at text not null, collection_ended_at text not null, started_at text not null, ended_at text not null, created_at text not null)`,
		`create table if not exists traffic_leases (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, user_id integer not null references users(id) on delete cascade, period_key text not null, lease_bytes integer not null default 0, consumed_bytes integer not null default 0, updated_at text not null, unique(server_id,user_id,period_key))`,
		`create table if not exists server_telemetry (server_id integer primary key references servers(id) on delete cascade, monitoring_mode text not null default 'lightweight', traffic_reset_mode text not null default 'monthly', traffic_reset_day integer not null default 1, connectivity_probe_enabled integer not null default 0, time_correction_mode text not null default 'off', time_check_status text not null default 'unknown', time_offset_ms integer not null default 0, time_effective_offset_ms integer not null default 0, time_check_source text not null default '', time_check_error text not null default '', time_logical_active integer not null default 0, time_unsupported_paths_json text not null default '[]', time_checked_at text, period_key text not null default '', period_start text not null default '', period_end text not null default '', traffic_upload_bytes integer not null default 0, traffic_download_bytes integer not null default 0, raw_upload_bytes integer not null default 0, raw_download_bytes integer not null default 0, network_upload_bps integer not null default 0, network_download_bps integer not null default 0, last_reported_at text, connectivity_available integer not null default -1, connectivity_latency_ms integer not null default 0, connectivity_checked_at text, connectivity_error text not null default '', updated_at text not null)`,
		`create table if not exists server_metric_samples (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, cpu_usage_percent real not null default 0, memory_used_bytes integer not null default 0, memory_total_bytes integer not null default 0, network_upload_bps integer not null default 0, network_download_bps integer not null default 0, traffic_upload_bytes integer not null default 0, traffic_download_bytes integer not null default 0, connectivity_available integer not null default -1, connectivity_latency_ms integer not null default 0, sampled_at text not null)`,
		`create table if not exists dns_benchmark_runs (id integer primary key autoincrement, request_id text not null unique, server_id integer not null references servers(id) on delete cascade, policy_revision integer not null, encrypted_list_id integer not null, encrypted_list_revision integer not null, bootstrap_list_id integer not null, bootstrap_list_revision integer not null, trigger text not null, apply_on_success integer not null default 0, requested_by integer references users(id) on delete set null, task_id integer references agent_tasks(id) on delete set null, apply_task_id integer references agent_tasks(id) on delete set null, status text not null, error text not null default '', started_at text, completed_at text, created_at text not null, updated_at text not null)`,
		`create table if not exists dns_benchmark_results (id integer primary key autoincrement, report_id text not null unique, request_id text not null default '', server_id integer not null references servers(id) on delete cascade, policy_revision integer not null, encrypted_list_id integer not null, encrypted_list_revision integer not null, bootstrap_list_id integer not null, bootstrap_list_revision integer not null, encrypted_json text not null, bootstrap_json text not null, status text not null, error text not null default '', created_at text not null)`,
		`create table if not exists mtu_detection_results (id integer primary key autoincrement, server_id integer not null references servers(id) on delete cascade, mode text not null default 'detect', target_host text not null default '', target_port integer not null default 0, interface_name text not null default '', current_mtu integer not null default 0, path_mtu integer not null default 0, recommended_mtu integer not null default 0, applied_mtu integer not null default 0, confidence text not null default '', error text not null default '', result_json text not null default '{}', created_at text not null)`,
		`create table if not exists port_forward_probe_results (id integer primary key autoincrement, port_forward_id integer not null, server_id integer not null references servers(id) on delete cascade, mode text not null, available integer not null default 0, latency_ms integer not null default 0, sample_count integer not null default 0, error text not null default '', result_json text not null default '{}', created_at text not null)`,
		`create table if not exists inbound_probe_results (id integer primary key autoincrement, inbound_id integer not null references inbounds(id) on delete cascade, server_id integer not null references servers(id) on delete cascade, config_version integer not null default 0, mode text not null, transport text not null, endpoint text not null default '', available integer not null default 0, confirmed integer not null default 0, latency_ms integer not null default 0, min_latency_ms integer not null default 0, p95_latency_ms integer not null default 0, jitter_ms integer not null default 0, sample_count integer not null default 0, success_count integer not null default 0, error text not null default '', result_json text not null default '{}', created_at text not null)`,
		`create table if not exists notification_channels (id integer primary key autoincrement, owner_user_id integer not null references users(id) on delete cascade, name text not null, type text not null, enabled integer not null default 1, events text not null default 'server_offline,server_online', config_json text not null default '{}', templates_json text not null default '{}', created_at text not null, updated_at text not null)`,
		`create table if not exists notification_channel_user_targets (channel_id integer not null references notification_channels(id) on delete cascade, user_id integer not null references users(id) on delete cascade, created_at text not null, primary key(channel_id,user_id))`,
		`create table if not exists notification_announcements (id integer primary key autoincrement, actor_user_id integer not null references users(id) on delete cascade, actor_name text not null, title text not null, body text not null, user_ids_json text not null default '[]', queued_count integer not null default 0, created_at text not null)`,
		`create table if not exists notification_deliveries (id integer primary key autoincrement, channel_id integer not null references notification_channels(id) on delete cascade, event text not null, event_key text not null, title text not null, body text not null, status text not null default 'pending', attempts integer not null default 0, error text not null default '', next_attempt_at text not null, created_at text not null, updated_at text not null, sent_at text, unique(channel_id,event,event_key))`,
		`create table if not exists server_offline_notices (server_id integer primary key references servers(id) on delete cascade, status text not null, since_at text not null, notify_at text not null, group_key text not null default '', notified integer not null default 0, updated_at text not null)`,
		`create table if not exists subscription_profiles (id integer primary key autoincrement, name text not null unique, group_name text not null default 'default', description text not null default '', config_json text not null default '{}', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create table if not exists subscription_assignments (id integer primary key autoincrement, profile_id integer not null references subscription_profiles(id) on delete cascade, user_id integer not null references users(id) on delete cascade, server_id integer references servers(id) on delete set null, inbound_id integer references inbounds(id) on delete set null, group_name text not null default '', enabled integer not null default 1, created_at text not null, updated_at text not null)`,
		`create index if not exists idx_tasks_server_status on agent_tasks(server_id, status)`,
		`create index if not exists idx_controller_backups_created on controller_backups(created_at desc)`,
		`create index if not exists idx_traffic_server on traffic_stats(server_id, created_at)`,
		`create index if not exists idx_traffic_reports_user_period on traffic_reports(user_id, period_key)`,
		`create index if not exists idx_connection_audit_user_time on connection_audit_reports(user_id, ended_at desc)`,
		`create index if not exists idx_connection_audit_server_time on connection_audit_reports(server_id, ended_at desc)`,
		`create index if not exists idx_connection_audit_source_time on connection_audit_reports(source_ip, ended_at desc)`,
		`create index if not exists idx_connection_audit_time on connection_audit_reports(ended_at desc)`,
		`create index if not exists idx_traffic_periods_user on traffic_periods(user_id, period_key)`,
		`create index if not exists idx_traffic_leases_server on traffic_leases(server_id, period_key)`,
		`create index if not exists idx_server_metric_samples_server_time on server_metric_samples(server_id, sampled_at desc)`,
		`create index if not exists idx_inbound_users_inbound on inbound_users(inbound_id, enabled)`,
		`create index if not exists idx_inbound_users_user on inbound_users(user_id, enabled)`,
		`create index if not exists idx_user_group_members_group on user_group_members(group_id, enabled)`,
		`create index if not exists idx_user_group_members_user on user_group_members(user_id, enabled)`,
		`create index if not exists idx_inbound_access_grants_subject on inbound_access_grants(subject_type, subject_id, enabled)`,
		`create index if not exists idx_inbound_access_grants_scope on inbound_access_grants(scope_type, server_id, inbound_id, enabled)`,
		`create index if not exists idx_external_outbound_access_grants_subject on external_outbound_access_grants(subject_type, subject_id, enabled)`,
		`create index if not exists idx_external_outbound_access_grants_external on external_outbound_access_grants(external_outbound_id, enabled)`,
		`create index if not exists idx_port_forwards_source on port_forwards(source_server_id, enabled, priority)`,
		`create index if not exists idx_tunnels_source on tunnels(source_server_id, enabled, priority)`,
		`create index if not exists idx_dns_bench_server on dns_benchmark_results(server_id, created_at)`,
		`create index if not exists idx_dns_lists_kind_enabled on dns_lists(kind, enabled)`,
		`create index if not exists idx_dns_runs_server_created on dns_benchmark_runs(server_id, created_at)`,
		`create index if not exists idx_mtu_detection_server on mtu_detection_results(server_id, created_at)`,
		`create index if not exists idx_forward_probe_server on port_forward_probe_results(server_id, created_at)`,
		`create index if not exists idx_inbound_probe_inbound on inbound_probe_results(inbound_id, created_at)`,
		`create index if not exists idx_notification_targets_user on notification_channel_user_targets(user_id, channel_id)`,
		`create index if not exists idx_notification_delivery_pending on notification_deliveries(status, next_attempt_at, attempts)`,
		`create index if not exists idx_subscription_assignments_user on subscription_assignments(user_id, enabled)`,
		`create index if not exists idx_subscription_assignments_profile on subscription_assignments(profile_id, enabled)`,
		`create index if not exists idx_subscription_one_time_tokens_user on subscription_one_time_tokens(user_id)`,
		`create index if not exists idx_subscription_pull_audits_user_time on subscription_pull_audits(user_id,requested_at desc)`,
		`create index if not exists idx_subscription_pull_audits_source_time on subscription_pull_audits(source_ip,requested_at desc)`,
		`create index if not exists idx_subscription_pull_audits_time on subscription_pull_audits(requested_at desc)`,
		`create index if not exists idx_passkey_credentials_user on passkey_credentials(user_id)`,
		`create index if not exists idx_auth_challenges_expiry on auth_challenges(expires_at)`,
		`create index if not exists idx_revoked_user_sessions_expiry on revoked_user_sessions(expires_at)`,
		`create index if not exists idx_dns_credential_zones_credential on dns_credential_zones(credential_id, zone_name)`,
		`create index if not exists idx_dns_credential_zones_server on dns_credential_zones(server_id)`,
		`create index if not exists idx_proxy_path_port_allocations_server on proxy_path_port_allocations(server_id)`,
		`create index if not exists idx_ssh_password_deployments_user on ssh_password_deployments(user_id, server_id)`,
	}
	for _, stmt := range schema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `drop table if exists ssh_user_keys`); err != nil {
		return err
	}
	if err := s.ensureNullableAuthChallengeUser(ctx); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "servers", "connection_audit_enabled", `alter table servers add column connection_audit_enabled integer not null default 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "servers", "bbr_enabled", `alter table servers add column bbr_enabled integer not null default 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "servers", "listen_mode", `alter table servers add column listen_mode text not null default 'auto'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "servers", "interface_ipv6", `alter table servers add column interface_ipv6 text not null default ''`); err != nil {
		return err
	}
	serverTelemetryColumns := []struct {
		name string
		sql  string
	}{
		{"offline_notify_enabled", `alter table server_telemetry add column offline_notify_enabled integer not null default 1`},
		{"offline_after_seconds", `alter table server_telemetry add column offline_after_seconds integer not null default 0`},
		{"time_correction_mode", `alter table server_telemetry add column time_correction_mode text not null default 'off'`},
		{"time_check_status", `alter table server_telemetry add column time_check_status text not null default 'unknown'`},
		{"time_offset_ms", `alter table server_telemetry add column time_offset_ms integer not null default 0`},
		{"time_effective_offset_ms", `alter table server_telemetry add column time_effective_offset_ms integer not null default 0`},
		{"time_check_source", `alter table server_telemetry add column time_check_source text not null default ''`},
		{"time_check_error", `alter table server_telemetry add column time_check_error text not null default ''`},
		{"time_logical_active", `alter table server_telemetry add column time_logical_active integer not null default 0`},
		{"time_unsupported_paths_json", `alter table server_telemetry add column time_unsupported_paths_json text not null default '[]'`},
		{"time_checked_at", `alter table server_telemetry add column time_checked_at text`},
	}
	for _, column := range serverTelemetryColumns {
		if err := s.ensureColumn(ctx, "server_telemetry", column.name, column.sql); err != nil {
			return err
		}
	}
	connectionAuditGeoColumns := []struct {
		name string
		sql  string
	}{
		{"source_country_code", `alter table connection_audit_reports add column source_country_code text not null default ''`},
		{"source_country", `alter table connection_audit_reports add column source_country text not null default ''`},
		{"source_province", `alter table connection_audit_reports add column source_province text not null default ''`},
		{"source_city", `alter table connection_audit_reports add column source_city text not null default ''`},
		{"source_isp", `alter table connection_audit_reports add column source_isp text not null default ''`},
		{"geo_database_revision", `alter table connection_audit_reports add column geo_database_revision text not null default ''`},
		{"closed_count", `alter table connection_audit_reports add column closed_count integer not null default 0`},
		{"duration_total_ms", `alter table connection_audit_reports add column duration_total_ms integer not null default 0`},
		{"duration_max_ms", `alter table connection_audit_reports add column duration_max_ms integer not null default 0`},
		{"collection_generation", `alter table connection_audit_reports add column collection_generation integer not null default 0`},
		{"bucket_capacity", `alter table connection_audit_reports add column bucket_capacity integer not null default 1`},
		{"dropped_bucket_count", `alter table connection_audit_reports add column dropped_bucket_count integer not null default 0`},
		{"collection_started_at", `alter table connection_audit_reports add column collection_started_at text not null default '1970-01-01T00:00:00Z'`},
		{"collection_ended_at", `alter table connection_audit_reports add column collection_ended_at text not null default '1970-01-01T00:00:00Z'`},
	}
	for _, column := range connectionAuditGeoColumns {
		if err := s.ensureColumn(ctx, "connection_audit_reports", column.name, column.sql); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "certificates", "eab_key_id", `alter table certificates add column eab_key_id text not null default ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "certificates", "eab_hmac_key_encrypted", `alter table certificates add column eab_hmac_key_encrypted text not null default ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "certificates", "google_eab_credential_id", `alter table certificates add column google_eab_credential_id integer references google_eab_credentials(id) on delete restrict`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `create index if not exists idx_certificates_google_eab_credential on certificates(google_eab_credential_id)`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "controller_backups", "remote_target", `alter table controller_backups add column remote_target text not null default ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "ai_providers", "api_format", `alter table ai_providers add column api_format text not null default 'chat_completions'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "ai_audit_review_jobs", "error_detail", `alter table ai_audit_review_jobs add column error_detail text not null default ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_paths", "name_mode", `alter table proxy_paths add column name_mode text not null default 'auto'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_paths", "kind", `alter table proxy_paths add column kind text not null default 'chain'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_paths", "branch_source_step_id", `alter table proxy_paths add column branch_source_step_id integer references proxy_path_steps(id) on delete set null`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_paths", "name_template_json", `alter table proxy_paths add column name_template_json text not null default '[]'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_paths", "exit_region_mode", `alter table proxy_paths add column exit_region_mode text not null default 'auto'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_paths", "exit_region_code", `alter table proxy_paths add column exit_region_code text not null default ''`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "external_outbounds", "region_mode", `alter table external_outbounds add column region_mode text not null default 'auto'`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "external_outbounds", "region_code", `alter table external_outbounds add column region_code text not null default ''`); err != nil {
		return err
	}
	if err := s.dropColumn(ctx, "proxy_paths", "name", `alter table proxy_paths drop column name`); err != nil {
		return err
	}
	if err := s.ensureProxyPathStepPositions(ctx); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `insert into dns_credential_zones(credential_id,zone_name,provider_zone_id,created_at,updated_at) select id,zone_name,zone_id,created_at,updated_at from dns_credentials where zone_name<>'' and not exists(select 1 from dns_credential_zones where credential_id=dns_credentials.id)`); err != nil {
		return err
	}
	if err := s.ensureBuiltinUserGroups(ctx); err != nil {
		return err
	}
	if err := s.ensureSSHUserAliases(ctx); err != nil {
		return err
	}
	return s.ensureDefaultDNSLists(ctx)
}

func (s *Store) ensureNullableAuthChallengeUser(ctx context.Context) error {
	var notNull int
	if err := s.db.QueryRowContext(ctx, `select "notnull" from pragma_table_info('auth_challenges') where name='user_id'`).Scan(&notNull); err != nil {
		return err
	}
	if notNull == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`drop index if exists idx_auth_challenges_expiry`,
		`alter table auth_challenges rename to auth_challenges_required_user`,
		`create table auth_challenges (token_hash text primary key, kind text not null, user_id integer references users(id) on delete cascade, data_encrypted text not null, expires_at text not null, created_at text not null)`,
		`insert into auth_challenges(token_hash,kind,user_id,data_encrypted,expires_at,created_at) select token_hash,kind,user_id,data_encrypted,expires_at,created_at from auth_challenges_required_user`,
		`drop table auth_challenges_required_user`,
		`create index idx_auth_challenges_expiry on auth_challenges(expires_at)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ensureProxyPathStepPositions compacts each path's positions to a dense 1..N
// sequence and then enforces uniqueness. Position is the only ordering source
// for an ordered chain, so a duplicate would make every consumer fall back to
// its (position, id) tie-break and would collide the synthetic resource IDs
// derived from position.
func (s *Store) ensureProxyPathStepPositions(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id,path_id from proxy_path_steps order by path_id asc,position asc,id asc`)
	if err != nil {
		return err
	}
	type ordered struct {
		id     int64
		pathID int64
	}
	items := []ordered{}
	for rows.Next() {
		var item ordered
		if err := rows.Scan(&item.id, &item.pathID); err != nil {
			return errors.Join(err, rows.Close())
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return err
	}
	// Shift every row out of the target range first so the rewrite cannot trip
	// the unique index halfway through.
	if _, err := tx.ExecContext(ctx, `update proxy_path_steps set position=-position where position>0`); err != nil {
		return err
	}
	next := map[int64]int{}
	for _, item := range items {
		next[item.pathID]++
		if _, err := tx.ExecContext(ctx, `update proxy_path_steps set position=? where id=?`, next[item.pathID], item.id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `create unique index if not exists idx_proxy_path_steps_position on proxy_path_steps(path_id,position)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureColumn(ctx context.Context, table, column, alterSQL string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info(?) where name=?`, table, column).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, alterSQL)
	return err
}

func (s *Store) dropColumn(ctx context.Context, table, column, alterSQL string) error {
	var count int
	if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info(?) where name=?`, table, column).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, alterSQL)
	return err
}

func (s *Store) resetLegacyDNSSchema(ctx context.Context) error {
	legacy, err := s.legacyDNSSchemaPresent(ctx)
	if err != nil {
		return err
	}
	if !legacy {
		return nil
	}
	// DNS storage is intentionally destructive. The project has no supported
	// migration from dns_profiles; a development database is rebuilt instead.
	for _, stmt := range []string{"drop table if exists dns_benchmark_results", "drop table if exists dns_benchmark_runs", "drop table if exists server_dns_policies", "drop table if exists dns_profiles", "drop table if exists dns_lists"} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) legacyDNSSchemaPresent(ctx context.Context) (bool, error) {
	var profilesExists int
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='dns_profiles'`).Scan(&profilesExists); err != nil {
		return false, err
	}
	legacyResults := false
	var resultsExists int
	if err := s.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name='dns_benchmark_results'`).Scan(&resultsExists); err != nil {
		return false, err
	}
	if resultsExists > 0 {
		var resultColumns int
		if err := s.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('dns_benchmark_results') where name='report_id'`).Scan(&resultColumns); err == nil {
			legacyResults = resultColumns == 0
		}
	}
	return profilesExists != 0 || legacyResults, nil
}

func (s *Store) ensureDefaultDNSLists(ctx context.Context) error {
	ts := now()
	defaults := []struct {
		name       string
		kind       model.DNSListKind
		candidates []model.DNSCandidate
	}{
		{
			name: "默认加密 DNS",
			kind: model.DNSListEncrypted,
			candidates: []model.DNSCandidate{
				{Tag: "cloudflare-doh", Transport: model.DNSTransportDoH, Server: "cloudflare-dns.com", Port: 443, Path: "/dns-query", TLSName: "cloudflare-dns.com"},
				{Tag: "google-dot", Transport: model.DNSTransportDoT, Server: "dns.google", Port: 853, TLSName: "dns.google"},
				{Tag: "quad9-doq", Transport: model.DNSTransportDoQ, Server: "dns.quad9.net", Port: 853, TLSName: "dns.quad9.net"},
			},
		},
		{
			name: "默认底层 DNS",
			kind: model.DNSListBootstrap,
			candidates: []model.DNSCandidate{
				{Tag: "cloudflare-udp", Transport: model.DNSTransportUDP, Server: "1.1.1.1", Port: 53},
				{Tag: "google-tcp", Transport: model.DNSTransportTCP, Server: "8.8.8.8", Port: 53},
				{Tag: "quad9-udp", Transport: model.DNSTransportUDP, Server: "9.9.9.9", Port: 53},
			},
		},
	}
	for _, item := range defaults {
		encoded, err := json.Marshal(item.candidates)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `insert into dns_lists(name,kind,revision,candidates_json,enabled,protected,created_at,updated_at)
			select ?,?,1,?,1,1,?,? where not exists(select 1 from dns_lists where kind=? and protected=1)`, item.name, item.kind, string(encoded), ts, ts, item.kind); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureBuiltinUserGroups(ctx context.Context) error {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `insert into user_groups(name,description,role,system_key,enabled,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_at,updated_at)
		select '管理员组','系统管理员账号；该分组不可删除。','admin',?,1,0,0,'monthly',1,?,?
		where not exists(select 1 from user_groups where system_key=?)`, UserGroupSystemAdmins, ts, ts, UserGroupSystemAdmins); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `insert into user_groups(name,description,role,system_key,enabled,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_at,updated_at)
		select '普通用户组','普通用户默认分组，不包含后台管理权限。','viewer',?,1,0,0,'monthly',1,?,?
		where not exists(select 1 from user_groups where system_key=?)`, UserGroupSystemUsers, ts, ts, UserGroupSystemUsers); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `update user_groups set role='admin',enabled=1,updated_at=? where system_key=?`, ts, UserGroupSystemAdmins); err != nil {
		return err
	}
	var adminID int64
	if err := s.db.QueryRowContext(ctx, `select id from users where role='admin' order by id limit 1`).Scan(&adminID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if adminID != 0 {
		if err := s.SetBootstrapAdmin(ctx, adminID); err != nil {
			return err
		}
		if err := s.AssignUserToBuiltinGroup(ctx, adminID, UserGroupSystemAdmins); err != nil {
			return err
		}
	}
	_, err := s.db.ExecContext(ctx, `insert into user_group_members(group_id,user_id,enabled,created_at,updated_at)
		select g.id,u.id,1,?,? from users u join user_groups g on g.system_key=?
		where u.role<>'admin' and not exists(select 1 from user_group_members m where m.user_id=u.id)`, ts, ts, UserGroupSystemUsers)
	return err
}

func (s *Store) SetBootstrapAdmin(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do nothing`, bootstrapAdminSetting, fmt.Sprint(userID), now())
	return err
}

func (s *Store) IsBootstrapAdmin(ctx context.Context, userID int64) (bool, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `select value from app_settings where key=?`, bootstrapAdminSetting).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && value == fmt.Sprint(userID), err
}

func (s *Store) AssignUserToBuiltinGroup(ctx context.Context, userID int64, systemKey string) error {
	var groupID int64
	if err := s.db.QueryRowContext(ctx, `select id from user_groups where system_key=?`, systemKey).Scan(&groupID); err != nil {
		return err
	}
	v := &model.UserGroupMember{GroupID: groupID, UserID: userID, Enabled: true}
	return s.CreateUserGroupMember(ctx, v)
}

func (s *Store) EffectiveUserRole(ctx context.Context, user model.User) (model.Role, error) {
	role := user.Role
	rows, err := s.db.QueryContext(ctx, `select g.role from user_groups g join user_group_members m on m.group_id=g.id where m.user_id=? and m.enabled=1 and g.enabled=1`, user.ID)
	if err != nil {
		return role, err
	}
	defer rows.Close()
	rank := map[model.Role]int{model.RoleViewer: 1, model.RoleOperator: 2, model.RoleAdmin: 3}
	for rows.Next() {
		var candidate model.Role
		if err := rows.Scan(&candidate); err != nil {
			return role, err
		}
		if rank[candidate] > rank[role] {
			role = candidate
		}
	}
	return role, rows.Err()
}

func (s *Store) ListSettings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `select key,value from app_settings order by key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, key, value, now)
	return err
}

// NextConfigVersion allocates one process- and database-wide monotonic version.
// A single UPSERT keeps concurrent deployment and core-refresh requests from
// reusing a version, while the task-table floor handles databases created by
// older builds that did not persist the sequence setting.
func (s *Store) NextConfigVersion(ctx context.Context) (int64, error) {
	candidate := time.Now().UnixMilli()
	var raw string
	err := s.db.QueryRowContext(ctx, `insert into app_settings(key,value,updated_at)
		values(?,cast(max(?,coalesce((select max(config_version)+1 from agent_tasks where config_version>0),?)) as text),?)
		on conflict(key) do update set
			value=cast(max(cast(app_settings.value as integer)+1,cast(excluded.value as integer)) as text),
			updated_at=excluded.updated_at
		returning value`, configVersionSetting, candidate, candidate, now()).Scan(&raw)
	if err != nil {
		return 0, err
	}
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || version <= 0 {
		return 0, fmt.Errorf("invalid allocated config version %q", raw)
	}
	return version, nil
}

// SetSettings applies related runtime settings as one durable state change.
func (s *Store) SetSettings(ctx context.Context, values map[string]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for key, value := range values {
		if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value, updated_at=excluded.updated_at`, key, value, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Backup creates a transactionally consistent SQLite snapshot. VACUUM INTO
// runs through the Store's single database connection, so the result includes
// committed WAL data without copying live database sidecar files.
func (s *Store) Backup(ctx context.Context, destination string) error {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return errors.New("backup destination is required")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `vacuum into ?`, destination); err != nil {
		return fmt.Errorf("create SQLite backup: %w", err)
	}
	return os.Chmod(destination, 0o600)
}

func (s *Store) CheckIntegrity(ctx context.Context) error {
	var integrity string
	if err := s.db.QueryRowContext(ctx, `pragma integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", integrity)
	}
	rows, err := s.db.QueryContext(ctx, `pragma foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("SQLite foreign key check failed")
	}
	return rows.Err()
}

func normalizeTrafficResetMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "month_day", "day", "custom_day":
		return "month_day"
	default:
		return "monthly"
	}
}

func normalizeTrafficResetDay(day int) int {
	if day < 1 {
		return 1
	}
	if day > 31 {
		return 31
	}
	return day
}

func (s *Store) AllowRate(ctx context.Context, keyHash string, limit int, window time.Duration, maxEntries int) (allowed bool, err error) {
	if strings.TrimSpace(keyHash) == "" || limit <= 0 || window <= 0 {
		return true, nil
	}
	if maxEntries < 1 {
		maxEntries = 1
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `begin immediate`); err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
	}()
	nowTime := time.Now().UTC()
	nowValue := nowTime.Format(time.RFC3339Nano)
	var startValue string
	var count int
	err = conn.QueryRowContext(ctx, `select window_start,count from rate_limits where key_hash=?`, keyHash).Scan(&startValue, &count)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := conn.ExecContext(ctx, `delete from rate_limits where updated_at < ?`, nowTime.Add(-4*window).Format(time.RFC3339Nano)); err != nil {
			return false, err
		}
		var entries int
		if err := conn.QueryRowContext(ctx, `select count(*) from rate_limits`).Scan(&entries); err != nil {
			return false, err
		}
		if entries >= maxEntries {
			if _, err := conn.ExecContext(ctx, `commit`); err != nil {
				return false, err
			}
			committed = true
			return false, nil
		}
		if _, err := conn.ExecContext(ctx, `insert into rate_limits(key_hash,window_start,count,updated_at) values(?,?,1,?)`, keyHash, nowValue, nowValue); err != nil {
			return false, err
		}
		allowed = true
	case err != nil:
		return false, err
	default:
		start, parseErr := time.Parse(time.RFC3339Nano, startValue)
		if parseErr != nil || nowTime.Sub(start) >= window {
			if _, err := conn.ExecContext(ctx, `update rate_limits set window_start=?,count=1,updated_at=? where key_hash=?`, nowValue, nowValue, keyHash); err != nil {
				return false, err
			}
			allowed = true
		} else if count >= limit {
			allowed = false
		} else {
			if _, err := conn.ExecContext(ctx, `update rate_limits set count=count+1,updated_at=? where key_hash=?`, nowValue, keyHash); err != nil {
				return false, err
			}
			allowed = true
		}
	}
	if _, err := conn.ExecContext(ctx, `commit`); err != nil {
		return false, err
	}
	committed = true
	return allowed, nil
}

// BootstrapAdmin atomically creates the first administrator and records its
// immutable bootstrap identity. The immediate transaction serializes this
// decision across controller processes sharing the same SQLite database.
func (s *Store) BootstrapAdmin(ctx context.Context, u *model.User) (created bool, err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `begin immediate`); err != nil {
		return false, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
	}()

	var count int
	if err := conn.QueryRowContext(ctx, `select count(*) from users where role='admin'`).Scan(&count); err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}

	ts := now()
	res, err := conn.ExecContext(ctx, `insert into users(username,nickname,password_hash,session_version,role,status,proxy_uuid,proxy_password,speed_limit_mbps,traffic_limit_bytes,traffic_used_bytes,traffic_reset_mode,traffic_reset_day,subscription_token,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.Username, u.Nickname, u.PasswordHash, u.SessionVersion, model.RoleAdmin, u.Status, u.ProxyUUID, u.ProxyPassword, u.SpeedLimitMbps, u.TrafficLimitBytes, u.TrafficUsedBytes, normalizeTrafficResetMode(u.TrafficResetMode), normalizeTrafficResetDay(u.TrafficResetDay), nullEmpty(u.SubscriptionToken), ts, ts)
	if err != nil {
		return false, err
	}
	u.ID, err = res.LastInsertId()
	if err != nil {
		return false, err
	}
	u.Role = model.RoleAdmin
	u.CreatedAt = parseTime(ts)
	u.UpdatedAt = u.CreatedAt
	if _, err := conn.ExecContext(ctx, `insert into subscription_token_policies(user_id,burn_after_read,burned_at,updated_at) values(?,?,?,?)`, u.ID, boolInt(u.SubscriptionBurnAfterRead), timePtrString(u.SubscriptionBurnedAt), ts); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `insert into subscription_age_keys(user_id,enabled,public_key,updated_at) values(?,?,?,?)`, u.ID, boolInt(u.SubscriptionAgeEnabled), strings.TrimSpace(u.SubscriptionAgePublicKey), ts); err != nil {
		return false, err
	}
	if u.SSHRandomID, err = assignSSHUserAlias(ctx, conn, u.ID, ts); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, bootstrapAdminSetting, fmt.Sprint(u.ID), ts); err != nil {
		return false, err
	}
	var groupID int64
	if err := conn.QueryRowContext(ctx, `select id from user_groups where system_key=?`, UserGroupSystemAdmins).Scan(&groupID); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `insert into user_group_members(group_id,user_id,enabled,created_at,updated_at) values(?,?,?,?,?)`, groupID, u.ID, 1, ts, ts); err != nil {
		return false, err
	}
	if _, err := conn.ExecContext(ctx, `commit`); err != nil {
		return false, err
	}
	committed = true
	return true, nil
}

func (s *Store) CreateUser(ctx context.Context, u *model.User) error {
	ts := now()
	u.CreatedAt = parseTime(ts)
	u.UpdatedAt = u.CreatedAt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `insert into users(username,nickname,password_hash,session_version,role,status,proxy_uuid,proxy_password,speed_limit_mbps,traffic_limit_bytes,traffic_used_bytes,traffic_reset_mode,traffic_reset_day,subscription_token,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, u.Username, u.Nickname, u.PasswordHash, u.SessionVersion, u.Role, u.Status, u.ProxyUUID, u.ProxyPassword, u.SpeedLimitMbps, u.TrafficLimitBytes, u.TrafficUsedBytes, normalizeTrafficResetMode(u.TrafficResetMode), normalizeTrafficResetDay(u.TrafficResetDay), nullEmpty(u.SubscriptionToken), ts, ts)
	if err != nil {
		return err
	}
	u.ID, _ = res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `insert into subscription_token_policies(user_id,burn_after_read,burned_at,updated_at) values(?,?,?,?)`, u.ID, boolInt(u.SubscriptionBurnAfterRead), timePtrString(u.SubscriptionBurnedAt), ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_age_keys(user_id,enabled,public_key,updated_at) values(?,?,?,?)`, u.ID, boolInt(u.SubscriptionAgeEnabled), strings.TrimSpace(u.SubscriptionAgePublicKey), ts); err != nil {
		return err
	}
	if u.SSHRandomID, err = assignSSHUserAlias(ctx, tx, u.ID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

type sshAliasExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func assignSSHUserAlias(ctx context.Context, exec sshAliasExecutor, userID int64, createdAt string) (string, error) {
	const firstID int64 = 100_000_000_000
	span := big.NewInt(900_000_000_000)
	for attempt := 0; attempt < 32; attempt++ {
		value, err := rand.Int(rand.Reader, span)
		if err != nil {
			return "", err
		}
		alias := strconv.FormatInt(firstID+value.Int64(), 10)
		if _, err := exec.ExecContext(ctx, `insert into ssh_user_aliases(user_id,random_id,created_at) values(?,?,?)`, userID, alias, createdAt); err == nil {
			return alias, nil
		} else if !strings.Contains(strings.ToLower(err.Error()), "unique") {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique SSH user alias")
}

func (s *Store) ensureSSHUserAliases(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `select id from users where not exists(select 1 from ssh_user_aliases where user_id=users.id) order by id`)
	if err != nil {
		return err
	}
	var userIDs []int64
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return errors.Join(err, rows.Close())
		}
		userIDs = append(userIDs, userID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, userID := range userIDs {
		if _, err := assignSSHUserAlias(ctx, tx, userID, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SSHUserAlias(ctx context.Context, userID int64) (string, error) {
	var alias string
	if err := s.db.QueryRowContext(ctx, `select random_id from ssh_user_aliases where user_id=?`, userID).Scan(&alias); err != nil {
		return "", err
	}
	return alias, nil
}

func (s *Store) UpdateUser(ctx context.Context, u *model.User) error {
	u.UpdatedAt = time.Now().UTC()
	ts := u.UpdatedAt.Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `update users set username=?, nickname=?, password_hash=coalesce(nullif(?,''),password_hash), role=?, status=?, proxy_uuid=?, proxy_password=?, speed_limit_mbps=?, traffic_limit_bytes=?, traffic_used_bytes=?, traffic_reset_mode=?, traffic_reset_day=?, subscription_token=?, updated_at=? where id=?`, u.Username, u.Nickname, u.PasswordHash, u.Role, u.Status, u.ProxyUUID, u.ProxyPassword, u.SpeedLimitMbps, u.TrafficLimitBytes, u.TrafficUsedBytes, normalizeTrafficResetMode(u.TrafficResetMode), normalizeTrafficResetDay(u.TrafficResetDay), nullEmpty(u.SubscriptionToken), ts, u.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_token_policies(user_id,burn_after_read,burned_at,updated_at) values(?,?,?,?) on conflict(user_id) do update set burn_after_read=excluded.burn_after_read,burned_at=excluded.burned_at,updated_at=excluded.updated_at`, u.ID, boolInt(u.SubscriptionBurnAfterRead), timePtrString(u.SubscriptionBurnedAt), ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_age_keys(user_id,enabled,public_key,updated_at) values(?,?,?,?) on conflict(user_id) do update set enabled=excluded.enabled,public_key=excluded.public_key,updated_at=excluded.updated_at`, u.ID, boolInt(u.SubscriptionAgeEnabled), strings.TrimSpace(u.SubscriptionAgePublicKey), ts); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) BumpSessionVersion(ctx context.Context, userID int64) (int64, error) {
	ts := now()
	res, err := s.db.ExecContext(ctx, `update users set session_version=coalesce(session_version,0)+1, updated_at=? where id=?`, ts, userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, sql.ErrNoRows
	}
	var version int64
	if err := s.db.QueryRowContext(ctx, `select coalesce(session_version,0) from users where id=?`, userID).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func (s *Store) UpdateUserSubscriptionToken(ctx context.Context, userID int64, token string) error {
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `update users set subscription_token=?, updated_at=? where id=?`, nullEmpty(token), ts, userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `insert into subscription_token_policies(user_id,burn_after_read,burned_at,updated_at) values(?,0,null,?) on conflict(user_id) do update set burned_at=null,updated_at=excluded.updated_at`, userID, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetUserSubscriptionBurnAfterRead(ctx context.Context, userID int64, enabled bool) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into subscription_token_policies(user_id,burn_after_read,burned_at,updated_at)
		select id,?,null,? from users where id=?
		on conflict(user_id) do update set burn_after_read=excluded.burn_after_read,updated_at=excluded.updated_at`, boolInt(enabled), ts, userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) CreateOneTimeSubscriptionToken(ctx context.Context, userID int64, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("subscription token required")
	}
	res, err := s.db.ExecContext(ctx, `insert into subscription_one_time_tokens(token,user_id,created_at)
		select ?,id,? from users
		where id=? and status='active' and not exists(select 1 from users where subscription_token=?)`, token, now(), userID, token)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteOneTimeSubscriptionTokensForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from subscription_one_time_tokens where user_id=?`, userID)
	return err
}

// ConsumeSubscriptionToken atomically invalidates a one-time token. Persistent
// tokens are only revalidated and remain unchanged.
func (s *Store) ConsumeSubscriptionToken(ctx context.Context, userID int64, token string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `delete from subscription_one_time_tokens
		where token=? and user_id=? and exists(select 1 from users where id=? and status='active')`, token, userID, userID)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else if n == 1 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
	var burnAfterRead int
	var burnedAt sql.NullString
	err = tx.QueryRowContext(ctx, `select coalesce(p.burn_after_read,0),p.burned_at
		from users u left join subscription_token_policies p on p.user_id=u.id
		where u.id=? and u.subscription_token=? and u.status='active'`, userID, token).Scan(&burnAfterRead, &burnedAt)
	if err != nil {
		return false, err
	}
	if burnAfterRead == 0 {
		return false, tx.Commit()
	}
	if burnedAt.Valid {
		return false, sql.ErrNoRows
	}
	ts := now()
	res, err = tx.ExecContext(ctx, `update subscription_token_policies set burned_at=?,updated_at=? where user_id=? and burn_after_read=1 and burned_at is null`, ts, ts, userID)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else if n != 1 {
		return false, sql.ErrNoRows
	}
	res, err = tx.ExecContext(ctx, `update users set subscription_token=null,updated_at=? where id=? and subscription_token=? and status='active'`, ts, userID, token)
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return false, err
	} else if n != 1 {
		return false, sql.ErrNoRows
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) DeleteSubscriptionTokenPolicyForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from subscription_token_policies where user_id=?`, userID)
	return err
}

func (s *Store) SetUserSubscriptionAge(ctx context.Context, userID int64, enabled bool, publicKey string) error {
	publicKey = strings.TrimSpace(publicKey)
	res, err := s.db.ExecContext(ctx, `insert into subscription_age_keys(user_id,enabled,public_key,updated_at)
		select id,?,?,? from users where id=?
		on conflict(user_id) do update set enabled=excluded.enabled,public_key=excluded.public_key,updated_at=excluded.updated_at`, boolInt(enabled), publicKey, now(), userID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteSubscriptionAgeForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from subscription_age_keys where user_id=?`, userID)
	return err
}

func (s *Store) Delete(ctx context.Context, table string, id int64) error {
	query, ok := deleteSQLForTable(table)
	if !ok {
		return errors.New("invalid table")
	}
	_, err := s.db.ExecContext(ctx, query, id)
	return err
}

func (s *Store) GetUserByUsername(ctx context.Context, username string) (*model.User, error) {
	rows, err := s.db.QueryContext(ctx, userSelectSQL+` where u.username=?`, username)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUsers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) GetUserByPasskey(ctx context.Context, credentialID, userHandle string) (*model.User, error) {
	rows, err := s.db.QueryContext(ctx, userSelectSQL+` where u.id=(
		select p.user_id from passkey_credentials p
		join user_authentication a on a.user_id=p.user_id
		where p.credential_id=? and a.webauthn_user_handle=?)`, credentialID, userHandle)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUsers(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) GetUserBySubscriptionToken(ctx context.Context, token string) (*model.User, error) {
	rows, err := s.db.QueryContext(ctx, userSelectSQL+` where u.subscription_token=? and u.status='active' and (coalesce(p.burn_after_read,0)=0 or p.burned_at is null)`, token)
	if err != nil {
		return nil, err
	}
	items, err := scanUsers(rows)
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(items) != 0 {
		return &items[0], nil
	}
	rows, err = s.db.QueryContext(ctx, userSelectSQL+` where u.status='active' and exists(
		select 1 from subscription_one_time_tokens t where t.user_id=u.id and t.token=?)`, token)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err = scanUsers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx, userSelectSQL+` order by u.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUsers(rows)
}

func (s *Store) GetUser(ctx context.Context, id int64) (*model.User, error) {
	rows, err := s.db.QueryContext(ctx, userSelectSQL+` where u.id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUsers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func scanUsers(rows *sql.Rows) ([]model.User, error) {
	var out []model.User
	for rows.Next() {
		var u model.User
		var ca, ua string
		var burnAfterRead int
		var burnedAt sql.NullString
		var ageEnabled int
		var suspended int
		var suspendedAt sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &u.Nickname, &u.PasswordHash, &u.SessionVersion, &u.Role, &u.Status, &u.ProxyUUID, &u.ProxyPassword, &u.SSHRandomID, &u.SpeedLimitMbps, &u.TrafficLimitBytes, &u.TrafficUsedBytes, &u.TrafficResetMode, &u.TrafficResetDay, &u.SubscriptionToken, &burnAfterRead, &burnedAt, &ageEnabled, &u.SubscriptionAgePublicKey, &suspended, &suspendedAt, &u.SubscriptionSuspendReason, &ca, &ua); err != nil {
			return nil, err
		}
		u.SubscriptionBurnAfterRead = burnAfterRead != 0
		u.SubscriptionBurnedAt = parseNullTime(burnedAt)
		u.SubscriptionAgeEnabled = ageEnabled != 0
		u.SubscriptionSuspended = suspended != 0
		u.SubscriptionSuspendedAt = parseNullTime(suspendedAt)
		u.TrafficResetMode = normalizeTrafficResetMode(u.TrafficResetMode)
		u.TrafficResetDay = normalizeTrafficResetDay(u.TrafficResetDay)
		u.CreatedAt = parseTime(ca)
		u.UpdatedAt = parseTime(ua)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) CreateSubscriptionProfile(ctx context.Context, v *model.SubscriptionProfile) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into subscription_profiles(name,group_name,description,config_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?)`, v.Name, v.GroupName, v.Description, v.ConfigJSON, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateSubscriptionProfile(ctx context.Context, v *model.SubscriptionProfile) error {
	_, err := s.db.ExecContext(ctx, `update subscription_profiles set name=?,group_name=?,description=?,config_json=?,enabled=?,updated_at=? where id=?`, v.Name, v.GroupName, v.Description, v.ConfigJSON, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListSubscriptionProfiles(ctx context.Context) ([]model.SubscriptionProfile, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,group_name,description,config_json,enabled,created_at,updated_at from subscription_profiles order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionProfiles(rows)
}

func (s *Store) GetSubscriptionProfile(ctx context.Context, id int64) (*model.SubscriptionProfile, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,group_name,description,config_json,enabled,created_at,updated_at from subscription_profiles where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanSubscriptionProfiles(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func scanSubscriptionProfiles(rows *sql.Rows) ([]model.SubscriptionProfile, error) {
	var out []model.SubscriptionProfile
	for rows.Next() {
		var v model.SubscriptionProfile
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.Name, &v.GroupName, &v.Description, &v.ConfigJSON, &enabled, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateSubscriptionAssignment(ctx context.Context, v *model.SubscriptionAssignment) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into subscription_assignments(profile_id,user_id,server_id,inbound_id,group_name,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, v.ProfileID, v.UserID, v.ServerID, v.InboundID, v.GroupName, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateSubscriptionAssignment(ctx context.Context, v *model.SubscriptionAssignment) error {
	_, err := s.db.ExecContext(ctx, `update subscription_assignments set profile_id=?,user_id=?,server_id=?,inbound_id=?,group_name=?,enabled=?,updated_at=? where id=?`, v.ProfileID, v.UserID, v.ServerID, v.InboundID, v.GroupName, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListSubscriptionAssignments(ctx context.Context) ([]model.SubscriptionAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `select id,profile_id,user_id,server_id,inbound_id,group_name,enabled,created_at,updated_at from subscription_assignments order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionAssignments(rows)
}

func (s *Store) ListSubscriptionAssignmentsForUser(ctx context.Context, userID int64) ([]model.SubscriptionAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `select id,profile_id,user_id,server_id,inbound_id,group_name,enabled,created_at,updated_at from subscription_assignments where user_id=? and enabled=1 order by id desc`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSubscriptionAssignments(rows)
}

func (s *Store) GetSubscriptionAssignment(ctx context.Context, id int64) (*model.SubscriptionAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `select id,profile_id,user_id,server_id,inbound_id,group_name,enabled,created_at,updated_at from subscription_assignments where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanSubscriptionAssignments(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) DeleteSubscriptionAssignmentsForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from subscription_assignments where user_id=?`, userID)
	return err
}

func (s *Store) DeleteSubscriptionAssignmentsForProfile(ctx context.Context, profileID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from subscription_assignments where profile_id=?`, profileID)
	return err
}

func scanSubscriptionAssignments(rows *sql.Rows) ([]model.SubscriptionAssignment, error) {
	var out []model.SubscriptionAssignment
	for rows.Next() {
		var v model.SubscriptionAssignment
		var serverID, inboundID sql.NullInt64
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.ProfileID, &v.UserID, &serverID, &inboundID, &v.GroupName, &enabled, &ca, &ua); err != nil {
			return nil, err
		}
		if serverID.Valid {
			v.ServerID = &serverID.Int64
		}
		if inboundID.Valid {
			v.InboundID = &inboundID.Int64
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateServer(ctx context.Context, v *model.Server) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	if strings.TrimSpace(v.ChainSecret) == "" {
		secret, err := randomServerChainSecret()
		if err != nil {
			return err
		}
		v.ChainSecret = secret
	}
	normalizeServerEntryIP(v)
	normalizeServerRegion(v)
	res, err := s.db.ExecContext(ctx, `insert into servers(name,agent_id,agent_token_hash,chain_secret,enrollment_hash,entry_address,public_ipv4,public_ipv6,interface_ipv6,region_code,detected_region_code,region_mode,entry_ip_mode,listen_ip,listen_mode,ip_stack,udp_inbound_mode,mtu_mode,mtu_value,mtu_probe_host,mtu_probe_port,mtu_overhead_bytes,bbr_enabled,port_range_start,port_range_end,status,os,distro_id,distro_version,distro_name,libc,service_manager,package_manager,arch,kernel,cpu,memory_bytes,cpu_usage_percent,memory_used_bytes,memory_total_bytes,agent_memory_bytes,disk_bytes,agent_version,agent_build,sing_box_version,connection_audit_enabled,last_seen_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.Name, nullEmpty(v.AgentID), nullEmpty(v.AgentTokenHash), v.ChainSecret, nullEmpty(v.EnrollmentHash), v.EntryAddress, v.PublicIPv4, v.PublicIPv6, v.InterfaceIPv6, v.RegionCode, v.DetectedRegionCode, v.RegionMode, v.EntryIPMode, v.ListenIP, v.ListenMode, v.IPStack, v.UDPInboundMode, v.MTUMode, v.MTUValue, v.MTUProbeHost, v.MTUProbePort, v.MTUOverheadBytes, boolInt(v.BBREnabled), v.PortRangeStart, v.PortRangeEnd, v.Status, v.OS, v.DistroID, v.DistroVersion, v.DistroName, v.Libc, v.ServiceManager, v.PackageManager, v.Arch, v.Kernel, v.CPU, v.MemoryBytes, v.CPUUsagePercent, v.MemoryUsedBytes, v.MemoryTotalBytes, v.AgentMemoryBytes, v.DiskBytes, v.AgentVersion, v.AgentBuild, v.SingBoxVersion, boolInt(v.ConnectionAuditEnabled), nilTime(v.LastSeenAt), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	if err := s.UpdateServerTelemetrySettings(ctx, v); err != nil {
		return err
	}
	_, err = s.EnsureServerDNSPolicy(ctx, v.ID)
	return err
}

func (s *Store) UpdateServer(ctx context.Context, v *model.Server) error {
	v.UpdatedAt = time.Now().UTC()
	normalizeServerEntryIP(v)
	normalizeServerRegion(v)
	// Note: enrollment_hash is intentionally not cleared via empty string here —
	// coalesce(nullif('',''), enrollment_hash) would preserve the old hash.
	// Use SetServerEnrollmentHash / ClaimServerEnrollment for one-time token lifecycle.
	_, err := s.db.ExecContext(ctx, `update servers set name=?, agent_id=coalesce(nullif(?,''),agent_id), agent_token_hash=coalesce(nullif(?,''),agent_token_hash), chain_secret=coalesce(nullif(?,''),chain_secret), enrollment_hash=coalesce(nullif(?,''),enrollment_hash), entry_address=?, public_ipv4=?, public_ipv6=?, interface_ipv6=?, region_code=?, detected_region_code=?, region_mode=?, entry_ip_mode=?, listen_ip=?, listen_mode=?, ip_stack=?, udp_inbound_mode=?, mtu_mode=?, mtu_value=?, mtu_probe_host=?, mtu_probe_port=?, mtu_overhead_bytes=?, bbr_enabled=?, port_range_start=?, port_range_end=?, status=?, os=?, distro_id=?, distro_version=?, distro_name=?, libc=?, service_manager=?, package_manager=?, arch=?, kernel=?, cpu=?, memory_bytes=?, cpu_usage_percent=?, memory_used_bytes=?, memory_total_bytes=?, agent_memory_bytes=?, disk_bytes=?, agent_version=?, agent_build=?, sing_box_version=?, connection_audit_enabled=?, last_seen_at=?, updated_at=? where id=?`, v.Name, v.AgentID, v.AgentTokenHash, v.ChainSecret, v.EnrollmentHash, v.EntryAddress, v.PublicIPv4, v.PublicIPv6, v.InterfaceIPv6, v.RegionCode, v.DetectedRegionCode, v.RegionMode, v.EntryIPMode, v.ListenIP, v.ListenMode, v.IPStack, v.UDPInboundMode, v.MTUMode, v.MTUValue, v.MTUProbeHost, v.MTUProbePort, v.MTUOverheadBytes, boolInt(v.BBREnabled), v.PortRangeStart, v.PortRangeEnd, v.Status, v.OS, v.DistroID, v.DistroVersion, v.DistroName, v.Libc, v.ServiceManager, v.PackageManager, v.Arch, v.Kernel, v.CPU, v.MemoryBytes, v.CPUUsagePercent, v.MemoryUsedBytes, v.MemoryTotalBytes, v.AgentMemoryBytes, v.DiskBytes, v.AgentVersion, v.AgentBuild, v.SingBoxVersion, boolInt(v.ConnectionAuditEnabled), nilTime(v.LastSeenAt), v.UpdatedAt.Format(time.RFC3339Nano), v.ID)
	if err != nil {
		return err
	}
	return s.UpdateServerTelemetrySettings(ctx, v)
}

// SetServerEnrollmentHash stores or clears a one-time enrollment hash.
// Pass an empty hash to clear (unlike UpdateServer, which cannot clear via "").
// expiresAt is required when setting a hash; cleared tokens also clear expiry.
func (s *Store) SetServerEnrollmentHash(ctx context.Context, serverID int64, hash string, expiresAt time.Time) error {
	ts := now()
	if strings.TrimSpace(hash) == "" {
		_, err := s.db.ExecContext(ctx, `update servers set enrollment_hash=NULL, enrollment_expires_at=NULL, updated_at=? where id=?`, ts, serverID)
		return err
	}
	if expiresAt.IsZero() {
		return errors.New("enrollment token expiry is required")
	}
	_, err := s.db.ExecContext(ctx, `update servers set enrollment_hash=?, enrollment_expires_at=?, updated_at=? where id=?`, hash, expiresAt.UTC().Format(time.RFC3339Nano), ts, serverID)
	return err
}

// ClaimServerEnrollment atomically consumes a one-time enrollment hash and binds
// long-term Agent credentials. Returns sql.ErrNoRows when the hash is missing,
// expired, or already consumed (including concurrent claims).
func (s *Store) ClaimServerEnrollment(ctx context.Context, enrollmentHash, agentID, agentTokenHash string) (*model.Server, error) {
	if strings.TrimSpace(enrollmentHash) == "" || strings.TrimSpace(agentID) == "" || strings.TrimSpace(agentTokenHash) == "" {
		return nil, sql.ErrNoRows
	}
	ts := now()
	// Clear expired tokens first so they cannot race with a claim.
	_, _ = s.db.ExecContext(ctx, `update servers set enrollment_hash=NULL, enrollment_expires_at=NULL, updated_at=? where enrollment_hash=? and enrollment_expires_at is not null and enrollment_expires_at < ?`, ts, enrollmentHash, ts)
	res, err := s.db.ExecContext(ctx, `update servers set agent_id=?, agent_token_hash=?, enrollment_hash=NULL, enrollment_expires_at=NULL, status=?, last_seen_at=?, updated_at=? where enrollment_hash=? and (enrollment_expires_at is null or enrollment_expires_at >= ?)`, agentID, agentTokenHash, model.ServerOnline, ts, ts, enrollmentHash, ts)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, sql.ErrNoRows
	}
	return s.GetServerByAgent(ctx, agentID)
}

func (s *Store) ListServers(ctx context.Context) ([]model.Server, error) {
	rows, err := s.db.QueryContext(ctx, serverSelectSQL+` order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanServers(rows)
	if err != nil {
		return nil, err
	}
	return items, s.attachServerTelemetry(ctx, items)
}

func (s *Store) GetServer(ctx context.Context, id int64) (*model.Server, error) {
	rows, err := s.db.QueryContext(ctx, serverSelectSQL+` where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanServers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	if err := s.attachServerTelemetry(ctx, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

func (s *Store) GetServerByAgent(ctx context.Context, agentID string) (*model.Server, error) {
	rows, err := s.db.QueryContext(ctx, serverSelectSQL+` where agent_id=?`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanServers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	if err := s.attachServerTelemetry(ctx, items); err != nil {
		return nil, err
	}
	return &items[0], nil
}

func scanServers(rows *sql.Rows) ([]model.Server, error) {
	var out []model.Server
	for rows.Next() {
		var v model.Server
		var last, enrollExp sql.NullString
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.Name, &v.AgentID, &v.AgentTokenHash, &v.ChainSecret, &v.EnrollmentHash, &enrollExp, &v.EntryAddress, &v.PublicIPv4, &v.PublicIPv6, &v.InterfaceIPv6, &v.RegionCode, &v.DetectedRegionCode, &v.RegionMode, &v.EntryIPMode, &v.ListenIP, &v.ListenMode, &v.IPStack, &v.UDPInboundMode, &v.MTUMode, &v.MTUValue, &v.MTUProbeHost, &v.MTUProbePort, &v.MTUOverheadBytes, &v.BBREnabled, &v.PortRangeStart, &v.PortRangeEnd, &v.Status, &v.OS, &v.DistroID, &v.DistroVersion, &v.DistroName, &v.Libc, &v.ServiceManager, &v.PackageManager, &v.Arch, &v.Kernel, &v.CPU, &v.MemoryBytes, &v.CPUUsagePercent, &v.MemoryUsedBytes, &v.MemoryTotalBytes, &v.AgentMemoryBytes, &v.DiskBytes, &v.AgentVersion, &v.AgentBuild, &v.SingBoxVersion, &v.ConnectionAuditEnabled, &last, &ca, &ua); err != nil {
			return nil, err
		}
		if enrollExp.Valid && enrollExp.String != "" {
			t := parseTime(enrollExp.String)
			v.EnrollmentExpiresAt = &t
		}
		normalizeServerEntryIP(&v)
		normalizeServerRegion(&v)
		if v.IPStack == "" {
			v.IPStack = model.IPStackAuto
		}
		if v.ListenMode == "" {
			v.ListenMode = model.ListenModeAuto
		}
		if v.UDPInboundMode == "" {
			v.UDPInboundMode = model.UDPInboundAllow
		}
		if v.MTUMode == "" {
			v.MTUMode = model.MTUModeDetect
		}
		if v.MTUProbeHost == "" {
			v.MTUProbeHost = "1.1.1.1"
		}
		if v.MTUProbePort == 0 {
			v.MTUProbePort = 443
		}
		v.LastSeenAt = parseNullTime(last)
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func randomServerChainSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func normalizeServerMonitoringMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "standard":
		return "standard"
	default:
		return "lightweight"
	}
}

func normalizeTimeCorrectionMode(mode model.TimeCorrectionMode) model.TimeCorrectionMode {
	switch mode {
	case model.TimeCorrectionAuto, model.TimeCorrectionNTP:
		return mode
	default:
		return model.TimeCorrectionOff
	}
}

func (s *Store) UpdateServerTelemetrySettings(ctx context.Context, server *model.Server) error {
	if server == nil || server.ID <= 0 {
		return errors.New("server telemetry requires a server")
	}
	server.MonitoringMode = normalizeServerMonitoringMode(server.MonitoringMode)
	server.TrafficResetMode = normalizeTrafficResetMode(server.TrafficResetMode)
	server.TrafficResetDay = normalizeTrafficResetDay(server.TrafficResetDay)
	server.TimeCorrectionMode = normalizeTimeCorrectionMode(server.TimeCorrectionMode)
	_, err := s.db.ExecContext(ctx, `insert into server_telemetry(server_id,monitoring_mode,traffic_reset_mode,traffic_reset_day,connectivity_probe_enabled,time_correction_mode,offline_notify_enabled,offline_after_seconds,updated_at) values(?,?,?,?,?,?,?,?,?)
		on conflict(server_id) do update set monitoring_mode=excluded.monitoring_mode,traffic_reset_mode=excluded.traffic_reset_mode,traffic_reset_day=excluded.traffic_reset_day,connectivity_probe_enabled=excluded.connectivity_probe_enabled,time_correction_mode=excluded.time_correction_mode,offline_notify_enabled=excluded.offline_notify_enabled,offline_after_seconds=excluded.offline_after_seconds,updated_at=excluded.updated_at`, server.ID, server.MonitoringMode, server.TrafficResetMode, server.TrafficResetDay, boolInt(server.ConnectivityProbeEnabled), server.TimeCorrectionMode, boolInt(server.OfflineNotifyEnabled), server.OfflineAfterSeconds, now())
	return err
}

func (s *Store) attachServerTelemetry(ctx context.Context, servers []model.Server) error {
	if len(servers) == 0 {
		return nil
	}
	byID := make(map[int64]*model.Server, len(servers))
	for i := range servers {
		servers[i].MonitoringMode = "lightweight"
		servers[i].TrafficResetMode = "monthly"
		servers[i].TrafficResetDay = 1
		servers[i].OfflineNotifyEnabled = true
		servers[i].OfflineAfterSeconds = 0
		servers[i].TimeCorrectionMode = model.TimeCorrectionOff
		servers[i].TimeCheckStatus = "unknown"
		servers[i].ConnectivityStatus = "disabled"
		byID[servers[i].ID] = &servers[i]
	}
	rows, err := s.db.QueryContext(ctx, serverTelemetrySelectSQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var mode, resetMode, correctionMode, timeStatus, timeSource, timeError, timeUnsupportedPathsJSON, periodStart, periodEnd, reportedAt, checkedAt, connectivityError string
		var resetDay, probeEnabled, logicalActive, available, offlineNotifyEnabled, offlineAfterSeconds int
		var timeOffset, timeEffectiveOffset int64
		var up, down, upBPS, downBPS uint64
		var latency int64
		var timeChecked, reported, checked sql.NullString
		if err := rows.Scan(&id, &mode, &resetMode, &resetDay, &probeEnabled, &correctionMode, &timeStatus, &timeOffset, &timeEffectiveOffset, &timeSource, &timeError, &logicalActive, &timeUnsupportedPathsJSON, &timeChecked, &periodStart, &periodEnd, &up, &down, &upBPS, &downBPS, &reported, &available, &latency, &checked, &connectivityError, &offlineNotifyEnabled, &offlineAfterSeconds); err != nil {
			return err
		}
		server := byID[id]
		if server == nil {
			continue
		}
		server.MonitoringMode = normalizeServerMonitoringMode(mode)
		server.TrafficResetMode = normalizeTrafficResetMode(resetMode)
		server.TrafficResetDay = normalizeTrafficResetDay(resetDay)
		server.ConnectivityProbeEnabled = probeEnabled == 1
		server.OfflineNotifyEnabled = offlineNotifyEnabled != 0
		server.OfflineAfterSeconds = offlineAfterSeconds
		server.TimeCorrectionMode = normalizeTimeCorrectionMode(model.TimeCorrectionMode(correctionMode))
		server.TimeCheckStatus = timeStatus
		server.TimeOffsetMS = timeOffset
		server.TimeEffectiveOffsetMS = timeEffectiveOffset
		server.TimeCheckSource = timeSource
		server.TimeCheckError = timeError
		server.TimeLogicalActive = logicalActive == 1
		_ = json.Unmarshal([]byte(timeUnsupportedPathsJSON), &server.TimeUnsupportedPaths)
		server.TrafficPeriodStart = periodStart
		server.TrafficPeriodEnd = periodEnd
		server.TrafficUploadBytes = up
		server.TrafficDownloadBytes = down
		server.NetworkUploadBPS = upBPS
		server.NetworkDownloadBPS = downBPS
		server.ConnectivityLatencyMS = latency
		server.ConnectivityError = connectivityError
		if !server.ConnectivityProbeEnabled {
			server.ConnectivityStatus = "disabled"
		} else if available == 1 {
			server.ConnectivityStatus = "available"
		} else if available == 0 {
			server.ConnectivityStatus = "unavailable"
		} else {
			server.ConnectivityStatus = "pending"
		}
		if reported.Valid {
			reportedAt = reported.String
			t := parseTime(reportedAt)
			server.TelemetryUpdatedAt = &t
		}
		if checked.Valid {
			checkedAt = checked.String
			t := parseTime(checkedAt)
			server.ConnectivityCheckedAt = &t
		}
		if timeChecked.Valid {
			t := parseTime(timeChecked.String)
			server.TimeCheckedAt = &t
		}
	}
	return rows.Err()
}

func (s *Store) UpdateServerTimeCheck(ctx context.Context, serverID int64, result model.TimeCheckResult) error {
	if serverID <= 0 {
		return errors.New("time check requires a server")
	}
	checkedAt := result.CheckedAt.UTC()
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `insert into server_telemetry(server_id,updated_at) values(?,?) on conflict(server_id) do nothing`, serverID, now())
	if err != nil {
		return err
	}
	unsupportedPaths, _ := json.Marshal(result.UnsupportedTimePaths)
	_, err = s.db.ExecContext(ctx, `update server_telemetry set time_check_status=?,time_offset_ms=?,time_effective_offset_ms=?,time_check_source=?,time_check_error=?,time_logical_active=?,time_unsupported_paths_json=?,time_checked_at=?,updated_at=? where server_id=?`, result.Status, result.RawOffsetMS, result.EffectiveOffsetMS, result.Source, result.Error, boolInt(result.LogicalTimeActive), string(unsupportedPaths), checkedAt.Format(time.RFC3339Nano), now(), serverID)
	return err
}

func (s *Store) ResetServerTimeCheck(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `update server_telemetry set time_check_status='pending',time_check_error='',time_unsupported_paths_json='[]',time_checked_at=NULL,updated_at=? where server_id=?`, now(), serverID)
	return err
}

func rateWithinProbeTolerance(reported, calculated uint64) bool {
	var diff uint64
	if reported > calculated {
		diff = reported - calculated
	} else {
		diff = calculated - reported
	}
	return diff <= calculated*2+(1<<20)
}

func (s *Store) UpdateServerTelemetryReport(ctx context.Context, serverID int64, report model.HealthReport, window model.ServerTrafficWindow) error {
	if serverID <= 0 || window.Key == "" || window.Start.IsZero() || window.End.IsZero() {
		return errors.New("invalid server telemetry report window")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := report.Timestamp.UTC()
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	nowText := ts.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `insert into server_telemetry(server_id,updated_at) values(?,?) on conflict(server_id) do nothing`, serverID, nowText); err != nil {
		return err
	}
	var periodKey string
	var periodUp, periodDown, previousUp, previousDown uint64
	var lastRawAt, previousChecked sql.NullString
	var previousAvailable int
	var previousLatency int64
	var previousError string
	if err := tx.QueryRowContext(ctx, `select period_key,traffic_upload_bytes,traffic_download_bytes,raw_upload_bytes,raw_download_bytes,last_reported_at,connectivity_available,connectivity_latency_ms,connectivity_checked_at,connectivity_error from server_telemetry where server_id=?`, serverID).Scan(&periodKey, &periodUp, &periodDown, &previousUp, &previousDown, &lastRawAt, &previousAvailable, &previousLatency, &previousChecked, &previousError); err != nil {
		return err
	}
	periodChanged := periodKey != window.Key
	hasBaseline := lastRawAt.Valid && !periodChanged
	if periodChanged {
		periodUp, periodDown = 0, 0
	}
	var uploadDelta, downloadDelta uint64
	var elapsed float64
	if hasBaseline {
		last := parseTime(lastRawAt.String)
		elapsed = ts.Sub(last).Seconds()
		if elapsed > 0 && elapsed <= 10*60 {
			if report.NetworkTotalUploadBytes >= previousUp {
				uploadDelta = report.NetworkTotalUploadBytes - previousUp
			}
			if report.NetworkTotalDownloadBytes >= previousDown {
				downloadDelta = report.NetworkTotalDownloadBytes - previousDown
			}
			maxDelta := uint64(elapsed * float64(uint64(100<<30)))
			if uploadDelta > maxDelta {
				uploadDelta = 0
			}
			if downloadDelta > maxDelta {
				downloadDelta = 0
			}
		}
	}
	periodUp += uploadDelta
	periodDown += downloadDelta
	uploadBPS, downloadBPS := report.NetworkUploadBPS, report.NetworkDownloadBPS
	if elapsed > 0 {
		calculatedUp := uint64(float64(uploadDelta) / elapsed)
		calculatedDown := uint64(float64(downloadDelta) / elapsed)
		if !rateWithinProbeTolerance(uploadBPS, calculatedUp) {
			uploadBPS = calculatedUp
		}
		if !rateWithinProbeTolerance(downloadBPS, calculatedDown) {
			downloadBPS = calculatedDown
		}
	}
	available, latency, checkedAt, connectivityError := previousAvailable, previousLatency, previousChecked, previousError
	if !report.ConnectivityProbeEnabled {
		available, latency, checkedAt, connectivityError = -1, 0, sql.NullString{}, ""
	} else if !report.ConnectivityCheckedAt.IsZero() {
		nextChecked := report.ConnectivityCheckedAt.UTC().Format(time.RFC3339Nano)
		if !previousChecked.Valid || nextChecked >= previousChecked.String {
			if report.ConnectivityAvailable {
				available = 1
			} else {
				available = 0
			}
			latency = report.ConnectivityLatencyMS
			checkedAt = sql.NullString{String: nextChecked, Valid: true}
			connectivityError = report.ConnectivityError
		}
	}
	_, err = tx.ExecContext(ctx, `update server_telemetry set period_key=?,period_start=?,period_end=?,traffic_upload_bytes=?,traffic_download_bytes=?,raw_upload_bytes=?,raw_download_bytes=?,network_upload_bps=?,network_download_bps=?,last_reported_at=?,connectivity_available=?,connectivity_latency_ms=?,connectivity_checked_at=?,connectivity_error=?,updated_at=? where server_id=?`, window.Key, window.Start.UTC().Format(time.RFC3339Nano), window.End.UTC().Format(time.RFC3339Nano), periodUp, periodDown, report.NetworkTotalUploadBytes, report.NetworkTotalDownloadBytes, uploadBPS, downloadBPS, nowText, available, latency, nullableString(checkedAt), connectivityError, nowText, serverID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into server_metric_samples(server_id,cpu_usage_percent,memory_used_bytes,memory_total_bytes,network_upload_bps,network_download_bps,traffic_upload_bytes,traffic_download_bytes,connectivity_available,connectivity_latency_ms,sampled_at) values(?,?,?,?,?,?,?,?,?,?,?)`, serverID, report.CPUUsagePercent, report.MemoryUsedBytes, report.MemoryTotalBytes, uploadBPS, downloadBPS, periodUp, periodDown, available, latency, nowText); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from server_metric_samples where server_id=? and sampled_at<?`, serverID, ts.Add(-48*time.Hour).Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func nullableString(value sql.NullString) any {
	if value.Valid {
		return value.String
	}
	return nil
}

func (s *Store) ListServerMetricSamples(ctx context.Context, serverID int64, limit int) ([]model.ServerMetricSample, error) {
	if limit <= 0 || limit > 720 {
		limit = 120
	}
	columns := `id,server_id,cpu_usage_percent,memory_used_bytes,memory_total_bytes,network_upload_bps,network_download_bps,traffic_upload_bytes,traffic_download_bytes,connectivity_available,connectivity_latency_ms,sampled_at`
	query := `select ` + columns + ` from server_metric_samples`
	args := []any{}
	if serverID > 0 {
		query += ` where server_id=?`
		args = append(args, serverID)
		query += ` order by sampled_at desc limit ?`
		args = append(args, limit)
	} else {
		query = `with ranked as (select ` + columns + `,row_number() over(partition by server_id order by sampled_at desc) as rn from server_metric_samples) select ` + columns + ` from ranked where rn<=? order by server_id,sampled_at`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.ServerMetricSample{}
	for rows.Next() {
		var item model.ServerMetricSample
		var available int
		var sampledAt string
		if err := rows.Scan(&item.ID, &item.ServerID, &item.CPUUsagePercent, &item.MemoryUsedBytes, &item.MemoryTotalBytes, &item.NetworkUploadBPS, &item.NetworkDownloadBPS, &item.TrafficUploadBytes, &item.TrafficDownloadBytes, &available, &item.ConnectivityLatencyMS, &sampledAt); err != nil {
			return nil, err
		}
		if available >= 0 {
			value := available == 1
			item.ConnectivityAvailable = &value
		}
		item.SampledAt = parseTime(sampledAt)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if serverID > 0 {
		for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
			out[left], out[right] = out[right], out[left]
		}
	}
	return out, nil
}

func (s *Store) DeleteServerTelemetry(ctx context.Context, serverID int64) error {
	if _, err := s.db.ExecContext(ctx, `delete from server_metric_samples where server_id=?`, serverID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `delete from server_telemetry where server_id=?`, serverID)
	return err
}

func (s *Store) UpsertHealthTransition(ctx context.Context, report model.HealthReport, window model.ServerTrafficWindow) (model.ServerStatus, model.ServerStatus, error) {
	server, err := s.GetServerByAgent(ctx, report.AgentID)
	if err != nil {
		return "", "", err
	}
	old := server.Status
	n := time.Now().UTC()
	server.Status = report.Status
	applyDetectedPublicIPs(server, report)
	if code := normalizeRegionCode(report.RegionCode); code != "" {
		server.DetectedRegionCode = code
	}
	server.OS = report.OS
	server.DistroID = report.DistroID
	server.DistroVersion = report.DistroVersion
	server.DistroName = report.DistroName
	server.Libc = report.Libc
	server.ServiceManager = report.ServiceManager
	server.PackageManager = report.PackageManager
	server.Arch = report.Arch
	server.Kernel = report.Kernel
	server.CPU = report.CPU
	server.MemoryBytes = report.MemoryBytes
	server.CPUUsagePercent = report.CPUUsagePercent
	server.MemoryUsedBytes = report.MemoryUsedBytes
	server.MemoryTotalBytes = report.MemoryTotalBytes
	server.AgentMemoryBytes = report.AgentMemoryBytes
	server.DiskBytes = report.DiskBytes
	server.AgentVersion = report.AgentVersion
	server.AgentBuild = report.AgentBuild
	server.SingBoxVersion = report.SingBoxVersion
	server.LastSeenAt = &n
	if err := s.UpdateServer(ctx, server); err != nil {
		return old, server.Status, err
	}
	return old, server.Status, s.UpdateServerTelemetryReport(ctx, server.ID, report, window)
}

func applyDetectedPublicIPs(server *model.Server, report model.HealthReport) {
	if ip, family := cleanPublicIP(report.PublicIPv4); family == "ipv4" {
		server.PublicIPv4 = ip
	}
	if ip, family := cleanPublicIP(report.PublicIPv6); family == "ipv6" {
		server.PublicIPv6 = ip
	}
	if ip, family := cleanPublicIP(report.InterfaceIPv6); family == "ipv6" {
		server.InterfaceIPv6 = ip
	} else {
		server.InterfaceIPv6 = ""
	}
	normalizeServerEntryIP(server)
}

func normalizeServerEntryIP(server *model.Server) {
	if server.EntryIPMode == "" {
		server.EntryIPMode = model.EntryIPModeAuto
	}
	server.EntryAddress = strings.TrimSpace(server.EntryAddress)
	switch server.EntryIPMode {
	case model.EntryIPModeCustom:
		return
	case model.EntryIPModeIPv4, model.EntryIPModeIPv6:
		return
	default:
		server.EntryIPMode = model.EntryIPModeAuto
	}
}

func normalizeServerRegion(server *model.Server) {
	server.RegionCode = normalizeRegionCode(server.RegionCode)
	server.DetectedRegionCode = normalizeRegionCode(server.DetectedRegionCode)
	if strings.EqualFold(strings.TrimSpace(server.RegionMode), "manual") && server.RegionCode != "" {
		server.RegionMode = "manual"
		return
	}
	server.RegionMode = "auto"
	server.RegionCode = ""
}

func normalizeRegionCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
		return ""
	}
	return code
}

func cleanPublicIP(raw string) (string, string) {
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if raw == "" {
		return "", ""
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() {
		return "", ""
	}
	if addr.Is4() {
		return addr.String(), "ipv4"
	}
	return addr.String(), "ipv6"
}

func (s *Store) MarkStaleServersOfflineEffective(ctx context.Context, now time.Time, defaultAfter time.Duration) ([]model.Server, error) {
	items, err := s.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	type staleItem struct {
		server model.Server
	}
	stale := []staleItem{}
	for _, item := range items {
		if item.Status != model.ServerOnline && item.Status != model.ServerDegraded && item.Status != model.ServerUnknown {
			continue
		}
		if strings.TrimSpace(item.AgentID) == "" || item.LastSeenAt == nil {
			continue
		}
		threshold := defaultAfter
		if item.OfflineAfterSeconds > 0 {
			threshold = time.Duration(item.OfflineAfterSeconds) * time.Second
		}
		if now.Sub(item.LastSeenAt.UTC()) >= threshold {
			stale = append(stale, staleItem{server: item})
		}
	}
	if len(stale) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	marked := []model.Server{}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	for _, item := range stale {
		res, err := tx.ExecContext(ctx, `update servers set status='offline', updated_at=? where id=? and status!='offline'`, ts, item.server.ID)
		if err != nil {
			return nil, err
		}
		if n, err := res.RowsAffected(); err == nil && n == 1 {
			marked = append(marked, item.server)
		}
	}
	if len(marked) == 0 {
		return nil, tx.Commit()
	}
	return marked, tx.Commit()
}

func (s *Store) CreateInbound(ctx context.Context, v *model.Inbound) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	if v.EntryIPMode == "" {
		v.EntryIPMode = model.EntryIPModeAuto
	}
	if v.DNSRecordTypes == "" {
		v.DNSRecordTypes = "auto"
	}
	if v.DDNSInterval == 0 {
		v.DDNSInterval = 300
	}
	res, err := s.db.ExecContext(ctx, `insert into inbounds(server_id,name,protocol,listen_ip,port,entry_ip_mode,external_ip,dns_sync_enabled,dns_credential_id,dns_domain,dns_proxy_enabled,dns_record_types,ddns_enabled,ddns_interval_seconds,dns_sync_status,dns_sync_error,dns_last_synced_at,tls,config_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ServerID, v.Name, v.Protocol, v.ListenIP, v.Port, v.EntryIPMode, v.ExternalIP, boolInt(v.DNSSyncEnabled), v.DNSCredentialID, v.DNSDomain, boolInt(v.DNSProxyEnabled), v.DNSRecordTypes, boolInt(v.DDNSEnabled), v.DDNSInterval, v.DNSSyncStatus, v.DNSSyncError, timePtrString(v.DNSLastSyncedAt), boolInt(v.TLS), v.ConfigJSON, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}
func (s *Store) UpdateInbound(ctx context.Context, v *model.Inbound) error {
	if v.EntryIPMode == "" {
		v.EntryIPMode = model.EntryIPModeAuto
	}
	if v.DNSRecordTypes == "" {
		v.DNSRecordTypes = "auto"
	}
	if v.DDNSInterval == 0 {
		v.DDNSInterval = 300
	}
	_, err := s.db.ExecContext(ctx, `update inbounds set server_id=?,name=?,protocol=?,listen_ip=?,port=?,entry_ip_mode=?,external_ip=?,dns_sync_enabled=?,dns_credential_id=?,dns_domain=?,dns_proxy_enabled=?,dns_record_types=?,ddns_enabled=?,ddns_interval_seconds=?,dns_sync_status=?,dns_sync_error=?,dns_last_synced_at=?,tls=?,config_json=?,enabled=?,updated_at=? where id=?`, v.ServerID, v.Name, v.Protocol, v.ListenIP, v.Port, v.EntryIPMode, v.ExternalIP, boolInt(v.DNSSyncEnabled), v.DNSCredentialID, v.DNSDomain, boolInt(v.DNSProxyEnabled), v.DNSRecordTypes, boolInt(v.DDNSEnabled), v.DDNSInterval, v.DNSSyncStatus, v.DNSSyncError, timePtrString(v.DNSLastSyncedAt), boolInt(v.TLS), v.ConfigJSON, boolInt(v.Enabled), now(), v.ID)
	return err
}
func (s *Store) ListInbounds(ctx context.Context) ([]model.Inbound, error) {
	rows, err := s.db.QueryContext(ctx, `select i.id,i.server_id,i.name,i.protocol,i.listen_ip,i.port,coalesce(i.entry_ip_mode,'auto'),coalesce(i.external_ip,''),coalesce(i.dns_sync_enabled,0),i.dns_credential_id,coalesce(i.dns_domain,''),coalesce(i.dns_proxy_enabled,0),coalesce(i.dns_record_types,'auto'),coalesce(i.ddns_enabled,0),coalesce(i.ddns_interval_seconds,300),coalesce(i.dns_sync_status,''),coalesce(i.dns_sync_error,''),i.dns_last_synced_at,i.tls,i.config_json,i.enabled,i.created_at,i.updated_at,coalesce(b.mode,''),b.certificate_id,coalesce(b.server_name,'') from inbounds i left join inbound_certificate_bindings b on b.inbound_id=i.id order by i.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Inbound
	for rows.Next() {
		var v model.Inbound
		var tls, en, dnsSync, dnsProxy, ddns int
		var ca, ua string
		var dnsCredentialID, certificateID sql.NullInt64
		var dnsSynced sql.NullString
		if err := rows.Scan(&v.ID, &v.ServerID, &v.Name, &v.Protocol, &v.ListenIP, &v.Port, &v.EntryIPMode, &v.ExternalIP, &dnsSync, &dnsCredentialID, &v.DNSDomain, &dnsProxy, &v.DNSRecordTypes, &ddns, &v.DDNSInterval, &v.DNSSyncStatus, &v.DNSSyncError, &dnsSynced, &tls, &v.ConfigJSON, &en, &ca, &ua, &v.CertificateMode, &certificateID, &v.CertificateDomain); err != nil {
			return nil, err
		}
		if v.EntryIPMode == "" {
			v.EntryIPMode = model.EntryIPModeAuto
		}
		if v.DNSRecordTypes == "" {
			v.DNSRecordTypes = "auto"
		}
		v.DNSSyncEnabled = dnsSync == 1
		if dnsCredentialID.Valid {
			v.DNSCredentialID = &dnsCredentialID.Int64
		}
		if certificateID.Valid {
			v.CertificateID = &certificateID.Int64
		}
		v.DNSProxyEnabled = dnsProxy == 1
		v.DDNSEnabled = ddns == 1
		if dnsSynced.Valid && dnsSynced.String != "" {
			t := parseTime(dnsSynced.String)
			v.DNSLastSyncedAt = &t
		}
		v.TLS = tls == 1
		v.Enabled = en == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) UpdateInboundDNSSyncResult(ctx context.Context, id int64, status, syncError string, syncedAt *time.Time) error {
	_, err := s.db.ExecContext(ctx, `update inbounds set dns_sync_status=?, dns_sync_error=?, dns_last_synced_at=?, updated_at=? where id=?`, status, syncError, timePtrString(syncedAt), now(), id)
	return err
}

func (s *Store) GetInbound(ctx context.Context, id int64) (*model.Inbound, error) {
	items, err := s.ListInbounds(ctx)
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

func (s *Store) CreateInboundUser(ctx context.Context, v *model.InboundUser) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	_, err := s.db.ExecContext(ctx, `insert into inbound_users(inbound_id,user_id,enabled,created_at,updated_at) values(?,?,?,?,?) on conflict(inbound_id,user_id) do update set enabled=excluded.enabled, updated_at=excluded.updated_at`, v.InboundID, v.UserID, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	current, err := s.GetInboundUserByPair(ctx, v.InboundID, v.UserID)
	if err != nil {
		return err
	}
	*v = *current
	return nil
}

func (s *Store) UpdateInboundUser(ctx context.Context, v *model.InboundUser) error {
	_, err := s.db.ExecContext(ctx, `update inbound_users set inbound_id=?,user_id=?,enabled=?,updated_at=? where id=?`, v.InboundID, v.UserID, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListInboundUsers(ctx context.Context) ([]model.InboundUser, error) {
	rows, err := s.db.QueryContext(ctx, `select id,inbound_id,user_id,enabled,created_at,updated_at from inbound_users order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInboundUsers(rows)
}

func (s *Store) ListInboundUsersForInbound(ctx context.Context, inboundID int64) ([]model.InboundUser, error) {
	rows, err := s.db.QueryContext(ctx, `select id,inbound_id,user_id,enabled,created_at,updated_at from inbound_users where inbound_id=? order by id desc`, inboundID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInboundUsers(rows)
}

func (s *Store) GetInboundUser(ctx context.Context, id int64) (*model.InboundUser, error) {
	rows, err := s.db.QueryContext(ctx, `select id,inbound_id,user_id,enabled,created_at,updated_at from inbound_users where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanInboundUsers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) GetInboundUserByPair(ctx context.Context, inboundID, userID int64) (*model.InboundUser, error) {
	rows, err := s.db.QueryContext(ctx, `select id,inbound_id,user_id,enabled,created_at,updated_at from inbound_users where inbound_id=? and user_id=?`, inboundID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanInboundUsers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) DeleteInboundUser(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `delete from inbound_users where id=?`, id)
	return err
}

func (s *Store) DeleteInboundUsersForInbound(ctx context.Context, inboundID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from inbound_users where inbound_id=?`, inboundID)
	return err
}

func (s *Store) DeleteInboundUsersForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from inbound_users where user_id=?`, userID)
	return err
}

func (s *Store) ApplySSHDeploymentState(ctx context.Context, hostKey model.SSHServerHostKey, passwordDigests map[int64]string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `insert into ssh_server_host_keys(server_id,public_key,fingerprint,config_version,updated_at) values(?,?,?,?,?) on conflict(server_id) do update set public_key=excluded.public_key,fingerprint=excluded.fingerprint,config_version=excluded.config_version,updated_at=excluded.updated_at`, hostKey.ServerID, hostKey.PublicKey, hostKey.Fingerprint, hostKey.ConfigVersion, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `insert into ssh_deployment_plans(server_id,plan_digest,config_version,updated_at) values(?,?,?,?) on conflict(server_id) do update set plan_digest=excluded.plan_digest,config_version=excluded.config_version,updated_at=excluded.updated_at`, hostKey.ServerID, hostKey.PlanDigest, hostKey.ConfigVersion, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from ssh_password_deployments where server_id=?`, hostKey.ServerID); err != nil {
		return err
	}
	for userID, digest := range passwordDigests {
		if _, err := tx.ExecContext(ctx, `insert into ssh_password_deployments(server_id,user_id,password_digest,config_version,updated_at) values(?,?,?,?,?)`, hostKey.ServerID, userID, digest, hostKey.ConfigVersion, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ClearSSHDeploymentState(ctx context.Context, serverID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `delete from ssh_password_deployments where server_id=?`, serverID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from ssh_server_host_keys where server_id=?`, serverID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from ssh_deployment_plans where server_id=?`, serverID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetSSHServerHostKey(ctx context.Context, serverID int64) (*model.SSHServerHostKey, error) {
	var v model.SSHServerHostKey
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `select h.server_id,h.public_key,h.fingerprint,coalesce(p.plan_digest,''),h.config_version,h.updated_at from ssh_server_host_keys h left join ssh_deployment_plans p on p.server_id=h.server_id where h.server_id=?`, serverID).Scan(&v.ServerID, &v.PublicKey, &v.Fingerprint, &v.PlanDigest, &v.ConfigVersion, &updatedAt)
	if err != nil {
		return nil, err
	}
	v.UpdatedAt = parseTime(updatedAt)
	return &v, nil
}

func (s *Store) ListSSHPasswordDeploymentsForUser(ctx context.Context, userID int64) ([]model.SSHPasswordDeployment, error) {
	rows, err := s.db.QueryContext(ctx, `select server_id,user_id,password_digest,config_version,updated_at from ssh_password_deployments where user_id=? order by server_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.SSHPasswordDeployment{}
	for rows.Next() {
		var item model.SSHPasswordDeployment
		var updatedAt string
		if err := rows.Scan(&item.ServerID, &item.UserID, &item.PasswordDigest, &item.ConfigVersion, &updatedAt); err != nil {
			return nil, err
		}
		item.UpdatedAt = parseTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanInboundUsers(rows *sql.Rows) ([]model.InboundUser, error) {
	var out []model.InboundUser
	for rows.Next() {
		var v model.InboundUser
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.InboundID, &v.UserID, &enabled, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateUserGroup(ctx context.Context, v *model.UserGroup) error {
	if v.Role == "" {
		v.Role = model.RoleViewer
	}
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into user_groups(name,description,role,system_key,enabled,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?)`, v.Name, v.Description, v.Role, v.SystemKey, boolInt(v.Enabled), v.SpeedLimitMbps, v.TrafficLimitBytes, normalizeTrafficResetMode(v.TrafficResetMode), normalizeTrafficResetDay(v.TrafficResetDay), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateUserGroup(ctx context.Context, v *model.UserGroup) error {
	_, err := s.db.ExecContext(ctx, `update user_groups set name=?,description=?,role=?,system_key=?,enabled=?,speed_limit_mbps=?,traffic_limit_bytes=?,traffic_reset_mode=?,traffic_reset_day=?,updated_at=? where id=?`, v.Name, v.Description, v.Role, v.SystemKey, boolInt(v.Enabled), v.SpeedLimitMbps, v.TrafficLimitBytes, normalizeTrafficResetMode(v.TrafficResetMode), normalizeTrafficResetDay(v.TrafficResetDay), now(), v.ID)
	return err
}

func (s *Store) ListUserGroups(ctx context.Context) ([]model.UserGroup, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,description,role,system_key,enabled,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_at,updated_at from user_groups order by case system_key when 'administrators' then 0 when 'users' then 1 else 2 end,id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserGroups(rows)
}

func (s *Store) GetUserGroup(ctx context.Context, id int64) (*model.UserGroup, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,description,role,system_key,enabled,speed_limit_mbps,traffic_limit_bytes,traffic_reset_mode,traffic_reset_day,created_at,updated_at from user_groups where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUserGroups(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func scanUserGroups(rows *sql.Rows) ([]model.UserGroup, error) {
	var out []model.UserGroup
	for rows.Next() {
		var v model.UserGroup
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.Name, &v.Description, &v.Role, &v.SystemKey, &enabled, &v.SpeedLimitMbps, &v.TrafficLimitBytes, &v.TrafficResetMode, &v.TrafficResetDay, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		v.TrafficResetMode = normalizeTrafficResetMode(v.TrafficResetMode)
		v.TrafficResetDay = normalizeTrafficResetDay(v.TrafficResetDay)
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateUserGroupMember(ctx context.Context, v *model.UserGroupMember) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	_, err := s.db.ExecContext(ctx, `insert into user_group_members(group_id,user_id,enabled,created_at,updated_at) values(?,?,?,?,?) on conflict(group_id,user_id) do update set enabled=excluded.enabled, updated_at=excluded.updated_at`, v.GroupID, v.UserID, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	current, err := s.GetUserGroupMemberByPair(ctx, v.GroupID, v.UserID)
	if err != nil {
		return err
	}
	*v = *current
	return nil
}

func (s *Store) UpdateUserGroupMember(ctx context.Context, v *model.UserGroupMember) error {
	_, err := s.db.ExecContext(ctx, `update user_group_members set group_id=?,user_id=?,enabled=?,updated_at=? where id=?`, v.GroupID, v.UserID, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListUserGroupMembers(ctx context.Context) ([]model.UserGroupMember, error) {
	rows, err := s.db.QueryContext(ctx, `select id,group_id,user_id,enabled,created_at,updated_at from user_group_members order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanUserGroupMembers(rows)
}

func (s *Store) GetUserGroupMember(ctx context.Context, id int64) (*model.UserGroupMember, error) {
	rows, err := s.db.QueryContext(ctx, `select id,group_id,user_id,enabled,created_at,updated_at from user_group_members where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUserGroupMembers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) GetUserGroupMemberByPair(ctx context.Context, groupID, userID int64) (*model.UserGroupMember, error) {
	rows, err := s.db.QueryContext(ctx, `select id,group_id,user_id,enabled,created_at,updated_at from user_group_members where group_id=? and user_id=?`, groupID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanUserGroupMembers(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func scanUserGroupMembers(rows *sql.Rows) ([]model.UserGroupMember, error) {
	var out []model.UserGroupMember
	for rows.Next() {
		var v model.UserGroupMember
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.GroupID, &v.UserID, &enabled, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateInboundAccessGrant(ctx context.Context, v *model.InboundAccessGrant) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into inbound_access_grants(subject_type,subject_id,scope_type,server_id,inbound_id,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, v.SubjectType, v.SubjectID, v.ScopeType, v.ServerID, v.InboundID, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateInboundAccessGrant(ctx context.Context, v *model.InboundAccessGrant) error {
	_, err := s.db.ExecContext(ctx, `update inbound_access_grants set subject_type=?,subject_id=?,scope_type=?,server_id=?,inbound_id=?,enabled=?,updated_at=? where id=?`, v.SubjectType, v.SubjectID, v.ScopeType, v.ServerID, v.InboundID, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListInboundAccessGrants(ctx context.Context) ([]model.InboundAccessGrant, error) {
	rows, err := s.db.QueryContext(ctx, `select id,subject_type,subject_id,scope_type,server_id,inbound_id,enabled,created_at,updated_at from inbound_access_grants order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanInboundAccessGrants(rows)
}

func (s *Store) GetInboundAccessGrant(ctx context.Context, id int64) (*model.InboundAccessGrant, error) {
	rows, err := s.db.QueryContext(ctx, `select id,subject_type,subject_id,scope_type,server_id,inbound_id,enabled,created_at,updated_at from inbound_access_grants where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanInboundAccessGrants(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func scanInboundAccessGrants(rows *sql.Rows) ([]model.InboundAccessGrant, error) {
	var out []model.InboundAccessGrant
	for rows.Next() {
		var v model.InboundAccessGrant
		var serverID, inboundID sql.NullInt64
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.SubjectType, &v.SubjectID, &v.ScopeType, &serverID, &inboundID, &enabled, &ca, &ua); err != nil {
			return nil, err
		}
		if serverID.Valid {
			v.ServerID = &serverID.Int64
		}
		if inboundID.Valid {
			v.InboundID = &inboundID.Int64
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) DeleteUserGroupMembersForGroup(ctx context.Context, groupID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from user_group_members where group_id=?`, groupID)
	return err
}

func (s *Store) DeleteUserGroupMembersForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from user_group_members where user_id=?`, userID)
	return err
}

func (s *Store) DeleteInboundAccessGrantsForGroup(ctx context.Context, groupID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from inbound_access_grants where subject_type=? and subject_id=?`, model.AccessSubjectGroup, groupID)
	return err
}

func (s *Store) DeleteInboundAccessGrantsForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from inbound_access_grants where subject_type=? and subject_id=?`, model.AccessSubjectUser, userID)
	return err
}

func (s *Store) DeleteInboundAccessGrantsForInbound(ctx context.Context, inboundID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from inbound_access_grants where scope_type=? and inbound_id=?`, model.AccessScopeInbound, inboundID)
	return err
}

func (s *Store) DeleteInboundAccessGrantsForServer(ctx context.Context, serverID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from inbound_access_grants where scope_type=? and server_id=?`, model.AccessScopeServer, serverID)
	return err
}

func (s *Store) CreateOutbound(ctx context.Context, v *model.Outbound) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into outbounds(server_id,next_server_id,name,protocol,target_address,target_port,config_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`, v.ServerID, v.NextServerID, v.Name, v.Protocol, v.TargetAddress, v.TargetPort, v.ConfigJSON, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}
func (s *Store) UpdateOutbound(ctx context.Context, v *model.Outbound) error {
	_, err := s.db.ExecContext(ctx, `update outbounds set server_id=?,next_server_id=?,name=?,protocol=?,target_address=?,target_port=?,config_json=?,enabled=?,updated_at=? where id=?`, v.ServerID, v.NextServerID, v.Name, v.Protocol, v.TargetAddress, v.TargetPort, v.ConfigJSON, boolInt(v.Enabled), now(), v.ID)
	return err
}
func (s *Store) ListOutbounds(ctx context.Context) ([]model.Outbound, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,next_server_id,name,protocol,target_address,target_port,config_json,enabled,created_at,updated_at from outbounds order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Outbound
	for rows.Next() {
		var v model.Outbound
		var next sql.NullInt64
		var en int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.ServerID, &next, &v.Name, &v.Protocol, &v.TargetAddress, &v.TargetPort, &v.ConfigJSON, &en, &ca, &ua); err != nil {
			return nil, err
		}
		if next.Valid {
			v.NextServerID = &next.Int64
		}
		v.Enabled = en == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetOutbound(ctx context.Context, id int64) (*model.Outbound, error) {
	items, err := s.ListOutbounds(ctx)
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

func (s *Store) CreateRoutingRule(ctx context.Context, v *model.RoutingRule) error {
	if v.Action == model.RouteActionInterface && v.InterfaceName != "" {
		v.OutboundTag = v.InterfaceName
	}
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into routing_rules(server_id,name,priority,match_json,action,outbound_id,external_outbound_id,target_server_id,outbound_tag,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ServerID, v.Name, v.Priority, v.MatchJSON, v.Action, v.OutboundID, v.ExternalOutboundID, v.TargetServerID, v.OutboundTag, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateRoutingRule(ctx context.Context, v *model.RoutingRule) error {
	if v.Action == model.RouteActionInterface && v.InterfaceName != "" {
		v.OutboundTag = v.InterfaceName
	}
	_, err := s.db.ExecContext(ctx, `update routing_rules set server_id=?,name=?,priority=?,match_json=?,action=?,outbound_id=?,external_outbound_id=?,target_server_id=?,outbound_tag=?,enabled=?,updated_at=? where id=?`, v.ServerID, v.Name, v.Priority, v.MatchJSON, v.Action, v.OutboundID, v.ExternalOutboundID, v.TargetServerID, v.OutboundTag, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListRoutingRules(ctx context.Context) ([]model.RoutingRule, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,name,priority,match_json,action,outbound_id,external_outbound_id,target_server_id,outbound_tag,enabled,created_at,updated_at from routing_rules order by priority asc,id asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.RoutingRule
	for rows.Next() {
		var v model.RoutingRule
		var outboundID, externalID, targetID sql.NullInt64
		var en int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.ServerID, &v.Name, &v.Priority, &v.MatchJSON, &v.Action, &outboundID, &externalID, &targetID, &v.OutboundTag, &en, &ca, &ua); err != nil {
			return nil, err
		}
		if outboundID.Valid {
			v.OutboundID = &outboundID.Int64
		}
		if externalID.Valid {
			v.ExternalOutboundID = &externalID.Int64
		}
		if targetID.Valid {
			v.TargetServerID = &targetID.Int64
		}
		if v.Action == model.RouteActionInterface {
			v.InterfaceName = v.OutboundTag
		}
		v.Enabled = en == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetRoutingRule(ctx context.Context, id int64) (*model.RoutingRule, error) {
	items, err := s.ListRoutingRules(ctx)
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

func (s *Store) CreateExternalOutbound(ctx context.Context, v *model.ExternalOutbound) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into external_outbounds(server_id,name,protocol,scope,target_address,target_port,config_json,region_mode,region_code,expose_to_users,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ServerID, v.Name, v.Protocol, v.Scope, v.TargetAddress, v.TargetPort, v.ConfigJSON, v.RegionMode, v.RegionCode, boolInt(v.ExposeToUsers), boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateExternalOutbound(ctx context.Context, v *model.ExternalOutbound) error {
	_, err := s.db.ExecContext(ctx, `update external_outbounds set server_id=?,name=?,protocol=?,scope=?,target_address=?,target_port=?,config_json=?,region_mode=?,region_code=?,expose_to_users=?,enabled=?,updated_at=? where id=?`, v.ServerID, v.Name, v.Protocol, v.Scope, v.TargetAddress, v.TargetPort, v.ConfigJSON, v.RegionMode, v.RegionCode, boolInt(v.ExposeToUsers), boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListExternalOutbounds(ctx context.Context) ([]model.ExternalOutbound, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,name,protocol,scope,target_address,target_port,config_json,coalesce(region_mode,'auto'),coalesce(region_code,''),expose_to_users,enabled,created_at,updated_at from external_outbounds order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ExternalOutbound
	for rows.Next() {
		var v model.ExternalOutbound
		var serverID sql.NullInt64
		var expose, en int
		var ca, ua string
		if err := rows.Scan(&v.ID, &serverID, &v.Name, &v.Protocol, &v.Scope, &v.TargetAddress, &v.TargetPort, &v.ConfigJSON, &v.RegionMode, &v.RegionCode, &expose, &en, &ca, &ua); err != nil {
			return nil, err
		}
		if serverID.Valid {
			v.ServerID = &serverID.Int64
		}
		v.ExposeToUsers = expose == 1
		v.Enabled = en == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetExternalOutbound(ctx context.Context, id int64) (*model.ExternalOutbound, error) {
	items, err := s.ListExternalOutbounds(ctx)
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

func (s *Store) CreateExternalOutboundAccessGrant(ctx context.Context, v *model.ExternalOutboundAccessGrant) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into external_outbound_access_grants(external_outbound_id,subject_type,subject_id,enabled,created_at,updated_at) values(?,?,?,?,?,?)`, v.ExternalOutboundID, v.SubjectType, v.SubjectID, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateExternalOutboundAccessGrant(ctx context.Context, v *model.ExternalOutboundAccessGrant) error {
	_, err := s.db.ExecContext(ctx, `update external_outbound_access_grants set external_outbound_id=?,subject_type=?,subject_id=?,enabled=?,updated_at=? where id=?`, v.ExternalOutboundID, v.SubjectType, v.SubjectID, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListExternalOutboundAccessGrants(ctx context.Context) ([]model.ExternalOutboundAccessGrant, error) {
	rows, err := s.db.QueryContext(ctx, `select id,external_outbound_id,subject_type,subject_id,enabled,created_at,updated_at from external_outbound_access_grants order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ExternalOutboundAccessGrant
	for rows.Next() {
		var v model.ExternalOutboundAccessGrant
		var enabled int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.ExternalOutboundID, &v.SubjectType, &v.SubjectID, &enabled, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetExternalOutboundAccessGrant(ctx context.Context, id int64) (*model.ExternalOutboundAccessGrant, error) {
	items, err := s.ListExternalOutboundAccessGrants(ctx)
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

func (s *Store) DeleteExternalOutboundAccessGrantsForExternal(ctx context.Context, externalID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from external_outbound_access_grants where external_outbound_id=?`, externalID)
	return err
}

func (s *Store) DeleteExternalOutboundAccessGrantsForUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from external_outbound_access_grants where subject_type='user' and subject_id=?`, userID)
	return err
}

func (s *Store) DeleteExternalOutboundAccessGrantsForGroup(ctx context.Context, groupID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from external_outbound_access_grants where subject_type='group' and subject_id=?`, groupID)
	return err
}

func (s *Store) CreateProxyPath(ctx context.Context, v *model.ProxyPath) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	if err := encodeProxyPathNameTemplate(v); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `insert into proxy_paths(inbound_id,kind,branch_source_step_id,name_mode,name_template_json,exit_region_mode,exit_region_code,secret,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?)`, v.InboundID, v.Kind, v.BranchSourceStepID, v.NameMode, v.NameTemplateJSON, v.ExitRegionMode, v.ExitRegionCode, v.Secret, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateProxyPath(ctx context.Context, v *model.ProxyPath) error {
	if err := encodeProxyPathNameTemplate(v); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `update proxy_paths set inbound_id=?,kind=?,branch_source_step_id=?,name_mode=?,name_template_json=?,exit_region_mode=?,exit_region_code=?,secret=?,enabled=?,updated_at=? where id=?`, v.InboundID, v.Kind, v.BranchSourceStepID, v.NameMode, v.NameTemplateJSON, v.ExitRegionMode, v.ExitRegionCode, v.Secret, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListProxyPaths(ctx context.Context) ([]model.ProxyPath, error) {
	rows, err := s.db.QueryContext(ctx, `select id,inbound_id,coalesce(kind,'chain'),branch_source_step_id,coalesce(name_mode,'auto'),coalesce(name_template_json,'[]'),coalesce(exit_region_mode,'auto'),coalesce(exit_region_code,''),coalesce(secret,''),enabled,created_at,updated_at from proxy_paths order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProxyPath
	for rows.Next() {
		var v model.ProxyPath
		var en int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.InboundID, &v.Kind, &v.BranchSourceStepID, &v.NameMode, &v.NameTemplateJSON, &v.ExitRegionMode, &v.ExitRegionCode, &v.Secret, &en, &ca, &ua); err != nil {
			return nil, err
		}
		if err := decodeProxyPathNameTemplate(&v); err != nil {
			return nil, err
		}
		v.Enabled = en == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ClearProxyPathBranchSourcesFromPosition(ctx context.Context, pathID int64, position int) error {
	_, err := s.db.ExecContext(ctx, `update proxy_paths set branch_source_step_id=null,updated_at=? where branch_source_step_id in (select id from proxy_path_steps where path_id=? and position>=?)`, now(), pathID, position)
	return err
}

func (s *Store) ClearProxyPathBranchSource(ctx context.Context, pathID int64) error {
	_, err := s.db.ExecContext(ctx, `update proxy_paths set branch_source_step_id=null,updated_at=? where id=? and branch_source_step_id is not null`, now(), pathID)
	return err
}

func encodeProxyPathNameTemplate(v *model.ProxyPath) error {
	if v.Kind == "" {
		v.Kind = model.ProxyPathKindChain
	}
	if v.NameMode == "" {
		v.NameMode = model.ProxyPathNameAuto
	}
	if v.NameTemplate == nil {
		v.NameTemplate = []model.ProxyPathNamePart{}
	}
	b, err := json.Marshal(v.NameTemplate)
	if err != nil {
		return err
	}
	v.NameTemplateJSON = string(b)
	return nil
}

func decodeProxyPathNameTemplate(v *model.ProxyPath) error {
	if v.NameMode == "" {
		v.NameMode = model.ProxyPathNameAuto
	}
	v.NameTemplate = []model.ProxyPathNamePart{}
	if strings.TrimSpace(v.NameTemplateJSON) == "" {
		return nil
	}
	return json.Unmarshal([]byte(v.NameTemplateJSON), &v.NameTemplate)
}

func (s *Store) GetProxyPath(ctx context.Context, id int64) (*model.ProxyPath, error) {
	items, err := s.ListProxyPaths(ctx)
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

func (s *Store) DeleteProxyPath(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `delete from proxy_path_steps where path_id=?`, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `delete from proxy_paths where id=?`, id)
	return err
}

// ListProxyPathPortAllocations returns every persisted generated-listener port.
// Ports are stored rather than re-derived so that the value an operator can see
// is the value that gets deployed, and so an unrelated topology change cannot
// shift a listener that is already live.
func (s *Store) ListProxyPathPortAllocations(ctx context.Context) ([]model.ProxyPathPortAllocation, error) {
	rows, err := s.db.QueryContext(ctx, `select id,kind,scope_key,server_id,port,created_at,updated_at from proxy_path_port_allocations order by kind asc,scope_key asc,server_id asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProxyPathPortAllocation
	for rows.Next() {
		var v model.ProxyPathPortAllocation
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.Kind, &v.ScopeKey, &v.ServerID, &v.Port, &ca, &ua); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaveProxyPathPortAllocations persists newly allocated ports and removes the
// records that are no longer claimed by any generated listener. Both run in one
// transaction so a concurrent reader never sees a partially rewritten set.
func (s *Store) SaveProxyPathPortAllocations(ctx context.Context, added []model.ProxyPathPortAllocation, removedIDs []int64) error {
	if len(added) == 0 && len(removedIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for _, item := range added {
		if _, err := tx.ExecContext(ctx, `insert into proxy_path_port_allocations(kind,scope_key,server_id,port,created_at,updated_at) values(?,?,?,?,?,?) on conflict(kind,scope_key,server_id) do update set port=excluded.port,updated_at=excluded.updated_at`, item.Kind, item.ScopeKey, item.ServerID, item.Port, ts, ts); err != nil {
			return err
		}
	}
	for _, id := range removedIDs {
		if _, err := tx.ExecContext(ctx, `delete from proxy_path_port_allocations where id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// truncateProxyPathStepsTx cuts every path at its first step returned by the
// match query, which must select (path_id, min(position)). Proxy path steps are
// an ordered chain, so deleting only the matched row would leave later steps in
// place and silently reconnect a different topology than the operator selected.
// Paths left without any step are removed as well.
func truncateProxyPathStepsTx(ctx context.Context, tx *sql.Tx, matchQuery string, args ...any) error {
	rows, err := tx.QueryContext(ctx, matchQuery, args...)
	if err != nil {
		return err
	}
	type cut struct {
		pathID   int64
		position int
	}
	cuts := []cut{}
	for rows.Next() {
		var item cut
		if err := rows.Scan(&item.pathID, &item.position); err != nil {
			return errors.Join(err, rows.Close())
		}
		cuts = append(cuts, item)
	}
	if err := rows.Err(); err != nil {
		return errors.Join(err, rows.Close())
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range cuts {
		if _, err := tx.ExecContext(ctx, `delete from proxy_path_steps where path_id=? and position>=?`, item.pathID, item.position); err != nil {
			return err
		}
	}
	for _, item := range cuts {
		if _, err := tx.ExecContext(ctx, `delete from proxy_paths where id=? and not exists (select 1 from proxy_path_steps s where s.path_id=proxy_paths.id)`, item.pathID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) DeleteProxyPathsForInbound(ctx context.Context, inboundID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A path rooted at this inbound loses its entry point entirely.
	if _, err := tx.ExecContext(ctx, `delete from proxy_path_steps where path_id in (select id from proxy_paths where inbound_id=?)`, inboundID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from proxy_paths where inbound_id=?`, inboundID); err != nil {
		return err
	}
	// A path that only traverses this inbound as a hop is cut at that hop.
	if err := truncateProxyPathStepsTx(ctx, tx, `select path_id,min(position) from proxy_path_steps where inbound_id=? group by path_id`, inboundID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteProxyPathStepsForExternal(ctx context.Context, externalID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := truncateProxyPathStepsTx(ctx, tx, `select path_id,min(position) from proxy_path_steps where external_outbound_id=? group by path_id`, externalID); err != nil {
		return err
	}
	return tx.Commit()
}

// CleanupRoutingForServer removes topology edges owned by or targeting a
// server. For ordered proxy paths it cuts the path at the first affected step
// rather than retaining later nodes as a silently rewired chain.
func (s *Store) CleanupRoutingForServer(ctx context.Context, serverID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := truncateProxyPathStepsTx(ctx, tx, `select s.path_id,min(s.position) from proxy_path_steps s left join inbounds i on i.id=s.inbound_id where s.server_id=? or i.server_id=? group by s.path_id`, serverID, serverID); err != nil {
		return err
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`delete from port_forward_probe_results where port_forward_id in (select id from port_forwards where source_server_id=? or target_server_id=?)`, []any{serverID, serverID}},
		{`delete from port_forwards where source_server_id=? or target_server_id=?`, []any{serverID, serverID}},
		{`delete from tunnels where source_server_id=? or target_server_id=?`, []any{serverID, serverID}},
		{`delete from routing_rules where server_id=? or target_server_id=?`, []any{serverID, serverID}},
		{`delete from outbounds where server_id=? or next_server_id=?`, []any{serverID, serverID}},
		{`delete from warp_profiles where server_id=?`, []any{serverID}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PruneOrphanedProxyPathSteps repairs topology left by older Controller builds
// that allowed a server or target inbound to be deleted without cutting paths.
func (s *Store) PruneOrphanedProxyPathSteps(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := truncateProxyPathStepsTx(ctx, tx, `select s.path_id,min(s.position) from proxy_path_steps s left join servers sv on sv.id=s.server_id left join inbounds i on i.id=s.inbound_id where (s.server_id is not null and sv.id is null) or (s.inbound_id is not null and i.id is null) group by s.path_id`); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteProxyPathStepsFromPosition removes one path edge and everything that
// depends on it. Proxy path steps are an ordered chain, so retaining later
// steps after removing an earlier edge would silently reconnect a different
// topology than the operator selected.
func (s *Store) DeleteProxyPathStepsFromPosition(ctx context.Context, pathID int64, position int) error {
	_, err := s.db.ExecContext(ctx, `delete from proxy_path_steps where path_id=? and position>=?`, pathID, position)
	return err
}

func (s *Store) CreateProxyPathStep(ctx context.Context, v *model.ProxyPathStep) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into proxy_path_steps(path_id,position,node_type,transport_mode,processing_role,server_id,inbound_id,external_outbound_id,config_json,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?)`, v.PathID, v.Position, v.NodeType, v.TransportMode, boolInt(v.ProcessingRole), v.ServerID, v.InboundID, v.ExternalOutboundID, v.ConfigJSON, ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateProxyPathStep(ctx context.Context, v *model.ProxyPathStep) error {
	_, err := s.db.ExecContext(ctx, `update proxy_path_steps set path_id=?,position=?,node_type=?,transport_mode=?,processing_role=?,server_id=?,inbound_id=?,external_outbound_id=?,config_json=?,updated_at=? where id=?`, v.PathID, v.Position, v.NodeType, v.TransportMode, boolInt(v.ProcessingRole), v.ServerID, v.InboundID, v.ExternalOutboundID, v.ConfigJSON, now(), v.ID)
	return err
}

func (s *Store) ListProxyPathSteps(ctx context.Context) ([]model.ProxyPathStep, error) {
	rows, err := s.db.QueryContext(ctx, `select id,path_id,position,node_type,coalesce(transport_mode,'singbox'),coalesce(processing_role,0),server_id,inbound_id,external_outbound_id,coalesce(config_json,'{}'),created_at,updated_at from proxy_path_steps order by path_id asc,position asc,id asc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ProxyPathStep
	for rows.Next() {
		var v model.ProxyPathStep
		var inboundID, externalID, serverID sql.NullInt64
		var processing int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.PathID, &v.Position, &v.NodeType, &v.TransportMode, &processing, &serverID, &inboundID, &externalID, &v.ConfigJSON, &ca, &ua); err != nil {
			return nil, err
		}
		v.ProcessingRole = processing == 1
		if serverID.Valid {
			v.ServerID = &serverID.Int64
		}
		if inboundID.Valid {
			v.InboundID = &inboundID.Int64
		}
		if externalID.Valid {
			v.ExternalOutboundID = &externalID.Int64
		}
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) ListProxyPathStepsForPath(ctx context.Context, pathID int64) ([]model.ProxyPathStep, error) {
	items, err := s.ListProxyPathSteps(ctx)
	if err != nil {
		return nil, err
	}
	out := []model.ProxyPathStep{}
	for _, item := range items {
		if item.PathID == pathID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Store) GetProxyPathStep(ctx context.Context, id int64) (*model.ProxyPathStep, error) {
	items, err := s.ListProxyPathSteps(ctx)
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

func (s *Store) CreateWARPProfile(ctx context.Context, v *model.WARPProfile) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into warp_profiles(server_id,name,status,config_json,mtu,dns_strategy,last_requested_at,error,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?)`, v.ServerID, v.Name, v.Status, v.ConfigJSON, v.MTU, v.DNSStrategy, nilTime(v.LastRequestedAt), v.Error, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateWARPProfile(ctx context.Context, v *model.WARPProfile) error {
	_, err := s.db.ExecContext(ctx, `update warp_profiles set server_id=?,name=?,status=?,config_json=?,mtu=?,dns_strategy=?,last_requested_at=?,error=?,enabled=?,updated_at=? where id=?`, v.ServerID, v.Name, v.Status, v.ConfigJSON, v.MTU, v.DNSStrategy, nilTime(v.LastRequestedAt), v.Error, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListWARPProfiles(ctx context.Context) ([]model.WARPProfile, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,name,status,config_json,mtu,dns_strategy,last_requested_at,error,enabled,created_at,updated_at from warp_profiles order by id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.WARPProfile
	for rows.Next() {
		var v model.WARPProfile
		var last sql.NullString
		var en int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.ServerID, &v.Name, &v.Status, &v.ConfigJSON, &v.MTU, &v.DNSStrategy, &last, &v.Error, &en, &ca, &ua); err != nil {
			return nil, err
		}
		if last.Valid {
			t := parseTime(last.String)
			v.LastRequestedAt = &t
		}
		v.Enabled = en == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetWARPProfile(ctx context.Context, id int64) (*model.WARPProfile, error) {
	items, err := s.ListWARPProfiles(ctx)
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

func (s *Store) GetWARPProfileForServer(ctx context.Context, serverID int64) (*model.WARPProfile, error) {
	items, err := s.ListWARPProfiles(ctx)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].ServerID == serverID {
			return &items[i], nil
		}
	}
	return nil, sql.ErrNoRows
}

func (s *Store) EnsureWARPProfileForServer(ctx context.Context, serverID int64) (*model.WARPProfile, error) {
	profile, err := s.GetWARPProfileForServer(ctx, serverID)
	if err == nil {
		return profile, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	profile = &model.WARPProfile{
		ServerID:   serverID,
		Name:       fmt.Sprintf("WARP / server-%d", serverID),
		Status:     model.WARPStatusNeeded,
		ConfigJSON: "{}",
		Enabled:    true,
	}
	if err := s.CreateWARPProfile(ctx, profile); err != nil {
		// A concurrent rule/path write may have created the server singleton.
		if existing, getErr := s.GetWARPProfileForServer(ctx, serverID); getErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return profile, nil
}

func (s *Store) ApplyWARPReport(ctx context.Context, report model.WARPConfigReport) error {
	profile, err := s.GetWARPProfile(ctx, report.ProfileID)
	if err != nil {
		return err
	}
	if profile.ServerID != report.ServerID {
		return fmt.Errorf("warp profile %d belongs to server %d, not %d", profile.ID, profile.ServerID, report.ServerID)
	}
	profile.Status = report.Status
	if profile.Status == "" {
		profile.Status = model.WARPStatusFailed
	}
	if strings.TrimSpace(report.ConfigJSON) != "" {
		profile.ConfigJSON = report.ConfigJSON
	}
	if report.MTU > 0 {
		profile.MTU = report.MTU
	}
	profile.Error = report.Error
	return s.UpdateWARPProfile(ctx, profile)
}

func (s *Store) CreateDNSList(ctx context.Context, v *model.DNSList) error {
	encoded, err := json.Marshal(v.Candidates)
	if err != nil {
		return err
	}
	ts := now()
	if v.Revision <= 0 {
		v.Revision = 1
	}
	res, err := s.db.ExecContext(ctx, `insert into dns_lists(name,kind,revision,candidates_json,enabled,protected,created_at,updated_at) values(?,?,?,?,?,?,?,?)`, strings.TrimSpace(v.Name), v.Kind, v.Revision, string(encoded), boolInt(v.Enabled), boolInt(v.Protected), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	return nil
}

func (s *Store) UpdateDNSList(ctx context.Context, v *model.DNSList) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var protected int
	var oldCandidates string
	var oldRevision int64
	var oldKind model.DNSListKind
	if err := tx.QueryRowContext(ctx, `select protected,candidates_json,revision,kind from dns_lists where id=?`, v.ID).Scan(&protected, &oldCandidates, &oldRevision, &oldKind); err != nil {
		return false, err
	}
	if v.Kind != oldKind {
		return false, errors.New("dns list kind cannot be changed")
	}
	v.Protected = protected == 1
	if v.Protected {
		v.Enabled = true
	}
	encoded, err := json.Marshal(v.Candidates)
	if err != nil {
		return false, err
	}
	candidatesChanged := oldCandidates != string(encoded)
	revision := oldRevision
	if candidatesChanged {
		revision++
	}
	ts := now()
	if _, err := tx.ExecContext(ctx, `update dns_lists set name=?,kind=?,revision=?,candidates_json=?,enabled=?,updated_at=? where id=?`, strings.TrimSpace(v.Name), v.Kind, revision, string(encoded), boolInt(v.Enabled), ts, v.ID); err != nil {
		return false, err
	}
	if candidatesChanged {
		var query string
		if v.Kind == model.DNSListEncrypted {
			query = `update server_dns_policies set revision=revision+1,encrypted_selected_json='[]',encrypted_selection_revision=0,needs_benchmark=1,updated_at=? where encrypted_list_id=?`
		} else {
			query = `update server_dns_policies set revision=revision+1,bootstrap_selected_json='[]',bootstrap_selection_revision=0,needs_benchmark=1,updated_at=? where bootstrap_list_id=?`
		}
		if _, err := tx.ExecContext(ctx, query, ts, v.ID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	v.Revision = revision
	v.UpdatedAt = parseTime(ts)
	return candidatesChanged, nil
}

func (s *Store) ListDNSLists(ctx context.Context, enabledOnly bool) ([]model.DNSList, error) {
	query := `select l.id,l.name,l.kind,l.revision,l.candidates_json,l.enabled,l.protected,l.created_at,l.updated_at,
		(select count(*) from server_dns_policies p where p.encrypted_list_id=l.id or p.bootstrap_list_id=l.id)
		from dns_lists l`
	if enabledOnly {
		query += ` where l.enabled=1`
	}
	query += ` order by l.protected desc,l.kind,l.name,l.id`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DNSList
	for rows.Next() {
		var item model.DNSList
		var candidates, created, updated string
		var enabled, protected int
		if err := rows.Scan(&item.ID, &item.Name, &item.Kind, &item.Revision, &candidates, &enabled, &protected, &created, &updated, &item.UsageCount); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(candidates), &item.Candidates); err != nil {
			return nil, fmt.Errorf("decode dns list %d: %w", item.ID, err)
		}
		item.Enabled = enabled == 1
		item.Protected = protected == 1
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) GetDNSList(ctx context.Context, id int64) (*model.DNSList, error) {
	items, err := s.ListDNSLists(ctx, false)
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

func (s *Store) DeleteDNSList(ctx context.Context, id int64) error {
	var protected, usage int
	if err := s.db.QueryRowContext(ctx, `select protected,(select count(*) from server_dns_policies where encrypted_list_id=? or bootstrap_list_id=?) from dns_lists where id=?`, id, id, id).Scan(&protected, &usage); err != nil {
		return err
	}
	if protected == 1 {
		return errors.New("protected dns list cannot be deleted")
	}
	if usage > 0 {
		return fmt.Errorf("dns list is used by %d server policies", usage)
	}
	_, err := s.db.ExecContext(ctx, `delete from dns_lists where id=?`, id)
	return err
}

// SetDefaultDNSList makes the given list the sole default of its kind. Newly
// created server DNS policies use the default list of each kind. The previous
// default of the same kind is demoted to an ordinary list.
func (s *Store) SetDefaultDNSList(ctx context.Context, id int64) (*model.DNSList, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var kind model.DNSListKind
	var enabled int
	if err := tx.QueryRowContext(ctx, `select kind,enabled from dns_lists where id=?`, id).Scan(&kind, &enabled); err != nil {
		return nil, err
	}
	if enabled == 0 {
		return nil, errors.New("disabled dns list cannot be set as default")
	}
	ts := now()
	if _, err := tx.ExecContext(ctx, `update dns_lists set protected=0,updated_at=? where kind=? and id<>?`, ts, kind, id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `update dns_lists set protected=1,enabled=1,updated_at=? where id=?`, ts, id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetDNSList(ctx, id)
}

func (s *Store) EnsureServerDNSPolicy(ctx context.Context, serverID int64) (*model.ServerDNSPolicy, error) {
	if item, err := s.GetServerDNSPolicy(ctx, serverID); err == nil {
		return item, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var encryptedID, bootstrapID int64
	if err := s.db.QueryRowContext(ctx, `select id from dns_lists where kind=? and protected=1 and enabled=1 order by id limit 1`, model.DNSListEncrypted).Scan(&encryptedID); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `select id from dns_lists where kind=? and protected=1 and enabled=1 order by id limit 1`, model.DNSListBootstrap).Scan(&bootstrapID); err != nil {
		return nil, err
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `insert into server_dns_policies(server_id,encrypted_list_id,bootstrap_list_id,revision,strategy,auto_test,test_interval_seconds,created_at,updated_at) values(?,?,?,1,'auto','first_apply',3600,?,?) on conflict(server_id) do nothing`, serverID, encryptedID, bootstrapID, ts, ts)
	if err != nil {
		return nil, err
	}
	return s.GetServerDNSPolicy(ctx, serverID)
}

const dnsPolicySelectSQL = `select server_id,encrypted_list_id,bootstrap_list_id,revision,strategy,auto_test,test_interval_seconds,encrypted_selected_json,bootstrap_selected_json,encrypted_selection_revision,bootstrap_selection_revision,last_attempt_at,last_success_at,last_error,needs_benchmark,created_at,updated_at from server_dns_policies`

func scanDNSPolicy(scanner interface{ Scan(...any) error }) (*model.ServerDNSPolicy, error) {
	var item model.ServerDNSPolicy
	var encrypted, bootstrap, created, updated string
	var attempted, succeeded sql.NullString
	var needsBenchmark int
	if err := scanner.Scan(&item.ServerID, &item.EncryptedListID, &item.BootstrapListID, &item.Revision, &item.Strategy, &item.AutoTest, &item.TestIntervalSeconds, &encrypted, &bootstrap, &item.EncryptedSelectionRevision, &item.BootstrapSelectionRevision, &attempted, &succeeded, &item.LastError, &needsBenchmark, &created, &updated); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(encrypted), &item.EncryptedSelected); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(bootstrap), &item.BootstrapSelected); err != nil {
		return nil, err
	}
	if item.EncryptedSelected == nil {
		item.EncryptedSelected = []model.DNSCandidate{}
	}
	if item.BootstrapSelected == nil {
		item.BootstrapSelected = []model.DNSCandidate{}
	}
	item.LastAttemptAt = parseNullTime(attempted)
	item.LastSuccessAt = parseNullTime(succeeded)
	item.NeedsBenchmark = needsBenchmark == 1
	item.CreatedAt = parseTime(created)
	item.UpdatedAt = parseTime(updated)
	return &item, nil
}

func (s *Store) GetServerDNSPolicy(ctx context.Context, serverID int64) (*model.ServerDNSPolicy, error) {
	return scanDNSPolicy(s.db.QueryRowContext(ctx, dnsPolicySelectSQL+` where server_id=?`, serverID))
}

func (s *Store) ListServerDNSPolicies(ctx context.Context) ([]model.ServerDNSPolicy, error) {
	rows, err := s.db.QueryContext(ctx, dnsPolicySelectSQL+` order by server_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ServerDNSPolicy
	for rows.Next() {
		item, err := scanDNSPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *item)
	}
	return out, rows.Err()
}

func (s *Store) UpdateServerDNSPolicy(ctx context.Context, v *model.ServerDNSPolicy) error {
	current, err := s.EnsureServerDNSPolicy(ctx, v.ServerID)
	if err != nil {
		return err
	}
	encrypted, err := s.GetDNSList(ctx, v.EncryptedListID)
	if err != nil {
		return err
	}
	bootstrap, err := s.GetDNSList(ctx, v.BootstrapListID)
	if err != nil {
		return err
	}
	if encrypted.Kind != model.DNSListEncrypted || bootstrap.Kind != model.DNSListBootstrap {
		return errors.New("dns policy list kinds do not match")
	}
	if !encrypted.Enabled || !bootstrap.Enabled {
		return errors.New("dns policy cannot select a disabled list")
	}
	if strings.TrimSpace(v.Strategy) == "" {
		v.Strategy = "auto"
	}
	if v.AutoTest == "" {
		v.AutoTest = model.DNSAutoTestFirstApply
	}
	if v.TestIntervalSeconds == 0 {
		v.TestIntervalSeconds = 3600
	}
	if v.AutoTest == model.DNSAutoTestPeriodic && v.TestIntervalSeconds < 300 {
		return errors.New("periodic dns test interval must be at least 300 seconds")
	}
	listChanged := current.EncryptedListID != v.EncryptedListID || current.BootstrapListID != v.BootstrapListID
	changed := listChanged || current.Strategy != v.Strategy || current.AutoTest != v.AutoTest || current.TestIntervalSeconds != v.TestIntervalSeconds
	if !changed {
		*v = *current
		return nil
	}
	v.Revision = current.Revision + 1
	v.EncryptedSelected = current.EncryptedSelected
	v.BootstrapSelected = current.BootstrapSelected
	v.EncryptedSelectionRevision = current.EncryptedSelectionRevision
	v.BootstrapSelectionRevision = current.BootstrapSelectionRevision
	if current.EncryptedListID != v.EncryptedListID {
		v.EncryptedSelected = []model.DNSCandidate{}
		v.EncryptedSelectionRevision = 0
	}
	if current.BootstrapListID != v.BootstrapListID {
		v.BootstrapSelected = []model.DNSCandidate{}
		v.BootstrapSelectionRevision = 0
	}
	v.LastAttemptAt = current.LastAttemptAt
	v.LastSuccessAt = current.LastSuccessAt
	v.LastError = current.LastError
	v.NeedsBenchmark = current.NeedsBenchmark || listChanged
	encJSON, _ := json.Marshal(v.EncryptedSelected)
	bootstrapJSON, _ := json.Marshal(v.BootstrapSelected)
	ts := now()
	_, err = s.db.ExecContext(ctx, `update server_dns_policies set encrypted_list_id=?,bootstrap_list_id=?,revision=?,strategy=?,auto_test=?,test_interval_seconds=?,encrypted_selected_json=?,bootstrap_selected_json=?,encrypted_selection_revision=?,bootstrap_selection_revision=?,needs_benchmark=?,updated_at=? where server_id=?`, v.EncryptedListID, v.BootstrapListID, v.Revision, v.Strategy, v.AutoTest, v.TestIntervalSeconds, string(encJSON), string(bootstrapJSON), v.EncryptedSelectionRevision, v.BootstrapSelectionRevision, boolInt(v.NeedsBenchmark), ts, v.ServerID)
	v.CreatedAt = current.CreatedAt
	v.UpdatedAt = parseTime(ts)
	return err
}

func (s *Store) CreateDNSBenchmarkRun(ctx context.Context, v *model.DNSBenchmarkRun) error {
	ts := now()
	if v.Status == "" {
		v.Status = "pending"
	}
	res, err := s.db.ExecContext(ctx, `insert into dns_benchmark_runs(request_id,server_id,policy_revision,encrypted_list_id,encrypted_list_revision,bootstrap_list_id,bootstrap_list_revision,trigger,apply_on_success,requested_by,task_id,apply_task_id,status,error,started_at,completed_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.RequestID, v.ServerID, v.PolicyRevision, v.EncryptedListID, v.EncryptedListRevision, v.BootstrapListID, v.BootstrapListRevision, v.Trigger, boolInt(v.ApplyOnSuccess), v.RequestedBy, v.TaskID, v.ApplyTaskID, v.Status, v.Error, nilTime(v.StartedAt), nilTime(v.CompletedAt), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	return nil
}

func (s *Store) AttachDNSBenchmarkTask(ctx context.Context, requestID string, taskID int64) error {
	_, err := s.db.ExecContext(ctx, `update dns_benchmark_runs set task_id=?,status='running',started_at=coalesce(started_at,?),updated_at=? where request_id=?`, taskID, now(), now(), requestID)
	return err
}

func (s *Store) UpdateDNSBenchmarkRunApply(ctx context.Context, requestID string, taskID *int64, status, message string) error {
	ts := now()
	completed := any(nil)
	if status == "applied" || status == "apply_failed" {
		completed = ts
	}
	_, err := s.db.ExecContext(ctx, `update dns_benchmark_runs set apply_task_id=coalesce(?,apply_task_id),status=?,error=?,completed_at=coalesce(?,completed_at),updated_at=? where request_id=?`, taskID, status, message, completed, ts, requestID)
	return err
}

func (s *Store) CompleteDNSBenchmarkApplyTask(ctx context.Context, taskID int64, succeeded bool, message string) error {
	status := "apply_failed"
	if succeeded {
		status = "applied"
		message = ""
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `update dns_benchmark_runs set status=?,error=?,completed_at=?,updated_at=? where apply_task_id=?`, status, message, ts, ts, taskID)
	return err
}

func (s *Store) FailDNSBenchmarkRunForTask(ctx context.Context, taskID int64, message string) error {
	ts := now()
	_, err := s.db.ExecContext(ctx, `update dns_benchmark_runs set status='failed',error=?,completed_at=?,updated_at=? where task_id=? and status in ('pending','running')`, message, ts, ts, taskID)
	return err
}

type DNSBenchmarkStoreOutcome struct {
	Duplicate      bool
	Stale          bool
	Success        bool
	ApplyOnSuccess bool
}

func (s *Store) RecordDNSBenchmarkResult(ctx context.Context, v *model.DNSBenchmarkResult) (DNSBenchmarkStoreOutcome, error) {
	var outcome DNSBenchmarkStoreOutcome
	if strings.TrimSpace(v.ReportID) == "" {
		return outcome, errors.New("dns benchmark report_id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return outcome, err
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRowContext(ctx, `select count(*) from dns_benchmark_results where report_id=?`, v.ReportID).Scan(&existing); err != nil {
		return outcome, err
	}
	if existing > 0 {
		outcome.Duplicate = true
		return outcome, tx.Commit()
	}
	policy, err := scanDNSPolicy(tx.QueryRowContext(ctx, dnsPolicySelectSQL+` where server_id=?`, v.ServerID))
	if err != nil {
		return outcome, err
	}
	var encryptedRevision, bootstrapRevision int64
	var encryptedJSON, bootstrapJSON string
	if err := tx.QueryRowContext(ctx, `select revision,candidates_json from dns_lists where id=?`, policy.EncryptedListID).Scan(&encryptedRevision, &encryptedJSON); err != nil {
		return outcome, err
	}
	if err := tx.QueryRowContext(ctx, `select revision,candidates_json from dns_lists where id=?`, policy.BootstrapListID).Scan(&bootstrapRevision, &bootstrapJSON); err != nil {
		return outcome, err
	}
	outcome.Stale = policy.Revision != v.PolicyRevision || policy.EncryptedListID != v.EncryptedListID || encryptedRevision != v.EncryptedListRevision || policy.BootstrapListID != v.BootstrapListID || bootstrapRevision != v.BootstrapListRevision
	encryptedSelected, encryptedErr := canonicalDNSSelection(encryptedJSON, v.Encrypted)
	bootstrapSelected, bootstrapErr := canonicalDNSSelection(bootstrapJSON, v.Bootstrap)
	resultError := strings.TrimSpace(v.Error)
	if encryptedErr != nil && resultError == "" {
		resultError = "encrypted: " + encryptedErr.Error()
	}
	if bootstrapErr != nil && resultError == "" {
		resultError = "bootstrap: " + bootstrapErr.Error()
	}
	outcome.Success = !outcome.Stale && resultError == "" && len(encryptedSelected) > 0 && len(bootstrapSelected) > 0
	status := "failed"
	if outcome.Stale {
		status = "stale"
	} else if outcome.Success {
		status = "succeeded"
	}
	v.Status = status
	v.Error = resultError
	encResult, _ := json.Marshal(v.Encrypted)
	bootstrapResult, _ := json.Marshal(v.Bootstrap)
	ts := now()
	if _, err := tx.ExecContext(ctx, `insert into dns_benchmark_results(report_id,request_id,server_id,policy_revision,encrypted_list_id,encrypted_list_revision,bootstrap_list_id,bootstrap_list_revision,encrypted_json,bootstrap_json,status,error,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ReportID, v.RequestID, v.ServerID, v.PolicyRevision, v.EncryptedListID, v.EncryptedListRevision, v.BootstrapListID, v.BootstrapListRevision, string(encResult), string(bootstrapResult), status, resultError, ts); err != nil {
		return outcome, err
	}
	if !outcome.Stale {
		if outcome.Success {
			encSelected, _ := json.Marshal(encryptedSelected)
			bootSelected, _ := json.Marshal(bootstrapSelected)
			if _, err := tx.ExecContext(ctx, `update server_dns_policies set encrypted_selected_json=?,bootstrap_selected_json=?,encrypted_selection_revision=?,bootstrap_selection_revision=?,last_attempt_at=?,last_success_at=?,last_error='',needs_benchmark=0,updated_at=? where server_id=?`, string(encSelected), string(bootSelected), encryptedRevision, bootstrapRevision, ts, ts, ts, v.ServerID); err != nil {
				return outcome, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `update server_dns_policies set last_attempt_at=?,last_error=?,needs_benchmark=1,updated_at=? where server_id=?`, ts, resultError, ts, v.ServerID); err != nil {
				return outcome, err
			}
		}
	}
	if v.RequestID != "" {
		var apply int
		var runPolicyRevision, runEncryptedID, runEncryptedRevision, runBootstrapID, runBootstrapRevision int64
		if err := tx.QueryRowContext(ctx, `select apply_on_success,policy_revision,encrypted_list_id,encrypted_list_revision,bootstrap_list_id,bootstrap_list_revision from dns_benchmark_runs where request_id=? and server_id=?`, v.RequestID, v.ServerID).Scan(&apply, &runPolicyRevision, &runEncryptedID, &runEncryptedRevision, &runBootstrapID, &runBootstrapRevision); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				return outcome, err
			}
		} else {
			runMatches := runPolicyRevision == v.PolicyRevision && runEncryptedID == v.EncryptedListID && runEncryptedRevision == v.EncryptedListRevision && runBootstrapID == v.BootstrapListID && runBootstrapRevision == v.BootstrapListRevision
			outcome.ApplyOnSuccess = apply == 1 && outcome.Success && runMatches
			if !runMatches {
				status = "stale"
			}
			if _, err := tx.ExecContext(ctx, `update dns_benchmark_runs set status=?,error=?,completed_at=?,updated_at=? where request_id=?`, status, resultError, ts, ts, v.RequestID); err != nil {
				return outcome, err
			}
		}
	}
	v.CreatedAt = parseTime(ts)
	return outcome, tx.Commit()
}

func canonicalDNSSelection(candidatesJSON string, group model.DNSBenchmarkGroup) ([]model.DNSCandidate, error) {
	var candidates []model.DNSCandidate
	if err := json.Unmarshal([]byte(candidatesJSON), &candidates); err != nil {
		return nil, err
	}
	usable := map[string]bool{}
	for _, item := range group.Items {
		if item.Error == "" {
			usable[item.Tag] = true
		}
	}
	byTag := map[string]model.DNSCandidate{}
	for _, candidate := range candidates {
		byTag[candidate.Tag] = candidate
	}
	selected := make([]model.DNSCandidate, 0, 2)
	seen := map[string]bool{}
	for _, tag := range group.BestTags {
		candidate, ok := byTag[tag]
		if !ok || !usable[tag] || seen[tag] {
			continue
		}
		seen[tag] = true
		selected = append(selected, candidate)
		if len(selected) == 2 {
			break
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("no usable candidates")
	}
	return selected, nil
}

func (s *Store) ListDNSBenchmarkResults(ctx context.Context, serverID int64, limit int) ([]model.DNSBenchmarkResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `select id,report_id,request_id,server_id,policy_revision,encrypted_list_id,encrypted_list_revision,bootstrap_list_id,bootstrap_list_revision,encrypted_json,bootstrap_json,status,error,created_at from dns_benchmark_results`
	args := []any{}
	if serverID > 0 {
		query += ` where server_id=?`
		args = append(args, serverID)
	}
	query += ` order by id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.DNSBenchmarkResult
	for rows.Next() {
		var item model.DNSBenchmarkResult
		var encrypted, bootstrap, created string
		if err := rows.Scan(&item.ID, &item.ReportID, &item.RequestID, &item.ServerID, &item.PolicyRevision, &item.EncryptedListID, &item.EncryptedListRevision, &item.BootstrapListID, &item.BootstrapListRevision, &encrypted, &bootstrap, &item.Status, &item.Error, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(encrypted), &item.Encrypted); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(bootstrap), &item.Bootstrap); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) AddMTUDetectionResult(ctx context.Context, v model.MTUDetectionResult) error {
	ts := now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `insert into mtu_detection_results(server_id,mode,target_host,target_port,interface_name,current_mtu,path_mtu,recommended_mtu,applied_mtu,confidence,error,result_json,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.ServerID, v.Mode, v.TargetHost, v.TargetPort, v.InterfaceName, v.CurrentMTU, v.PathMTU, v.RecommendedMTU, v.AppliedMTU, v.Confidence, v.Error, v.ResultJSON, ts); err != nil {
		return err
	}
	if v.RecommendedMTU > 0 {
		if _, err := tx.ExecContext(ctx, `update servers set mtu_value=?, updated_at=? where id=? and mtu_mode!='apply'`, v.RecommendedMTU, ts, v.ServerID); err != nil {
			return err
		}
	}
	if v.AppliedMTU > 0 {
		if _, err := tx.ExecContext(ctx, `update servers set mtu_value=?, updated_at=? where id=?`, v.AppliedMTU, ts, v.ServerID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListMTUDetectionResults(ctx context.Context, serverID int64, limit int) ([]model.MTUDetectionResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `select id,server_id,mode,target_host,target_port,interface_name,current_mtu,path_mtu,recommended_mtu,applied_mtu,confidence,error,result_json,created_at from mtu_detection_results`
	args := []any{}
	if serverID > 0 {
		query += ` where server_id=?`
		args = append(args, serverID)
	}
	query += ` order by id desc limit ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.MTUDetectionResult
	for rows.Next() {
		var v model.MTUDetectionResult
		var ca string
		if err := rows.Scan(&v.ID, &v.ServerID, &v.Mode, &v.TargetHost, &v.TargetPort, &v.InterfaceName, &v.CurrentMTU, &v.PathMTU, &v.RecommendedMTU, &v.AppliedMTU, &v.Confidence, &v.Error, &v.ResultJSON, &ca); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTime(ca)
		var details struct {
			Methods []model.MTUDetectionMethod `json:"methods"`
		}
		if json.Unmarshal([]byte(v.ResultJSON), &details) == nil {
			v.Methods = details.Methods
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreatePortForward(ctx context.Context, v *model.PortForward) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into port_forwards(name,source_server_id,target_server_id,listen_ip,listen_port,target_address,target_port,protocol,backend,probe_mode,probe_interval_seconds,sample_rate,priority,config_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.Name, v.SourceServerID, v.TargetServerID, v.ListenIP, v.ListenPort, v.TargetAddress, v.TargetPort, v.Protocol, v.Backend, v.ProbeMode, v.ProbeIntervalSeconds, v.SampleRate, v.Priority, v.ConfigJSON, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdatePortForward(ctx context.Context, v *model.PortForward) error {
	_, err := s.db.ExecContext(ctx, `update port_forwards set name=?,source_server_id=?,target_server_id=?,listen_ip=?,listen_port=?,target_address=?,target_port=?,protocol=?,backend=?,probe_mode=?,probe_interval_seconds=?,sample_rate=?,priority=?,config_json=?,enabled=?,updated_at=? where id=?`, v.Name, v.SourceServerID, v.TargetServerID, v.ListenIP, v.ListenPort, v.TargetAddress, v.TargetPort, v.Protocol, v.Backend, v.ProbeMode, v.ProbeIntervalSeconds, v.SampleRate, v.Priority, v.ConfigJSON, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListPortForwards(ctx context.Context) ([]model.PortForward, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,source_server_id,target_server_id,listen_ip,listen_port,target_address,target_port,protocol,backend,probe_mode,probe_interval_seconds,sample_rate,priority,config_json,enabled,created_at,updated_at from port_forwards order by priority asc,id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PortForward
	for rows.Next() {
		var v model.PortForward
		var en int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.Name, &v.SourceServerID, &v.TargetServerID, &v.ListenIP, &v.ListenPort, &v.TargetAddress, &v.TargetPort, &v.Protocol, &v.Backend, &v.ProbeMode, &v.ProbeIntervalSeconds, &v.SampleRate, &v.Priority, &v.ConfigJSON, &en, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = en == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetPortForward(ctx context.Context, id int64) (*model.PortForward, error) {
	items, err := s.ListPortForwards(ctx)
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

func (s *Store) CreateTunnel(ctx context.Context, v *model.Tunnel) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	res, err := s.db.ExecContext(ctx, `insert into tunnels(name,source_server_id,target_server_id,type,local_address,peer_address,listen_port,target_endpoint,target_port,priority,config_json,enabled,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, v.Name, v.SourceServerID, v.TargetServerID, v.Type, v.LocalAddress, v.PeerAddress, v.ListenPort, v.TargetEndpoint, v.TargetPort, v.Priority, v.ConfigJSON, boolInt(v.Enabled), ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateTunnel(ctx context.Context, v *model.Tunnel) error {
	_, err := s.db.ExecContext(ctx, `update tunnels set name=?,source_server_id=?,target_server_id=?,type=?,local_address=?,peer_address=?,listen_port=?,target_endpoint=?,target_port=?,priority=?,config_json=?,enabled=?,updated_at=? where id=?`, v.Name, v.SourceServerID, v.TargetServerID, v.Type, v.LocalAddress, v.PeerAddress, v.ListenPort, v.TargetEndpoint, v.TargetPort, v.Priority, v.ConfigJSON, boolInt(v.Enabled), now(), v.ID)
	return err
}

func (s *Store) ListTunnels(ctx context.Context) ([]model.Tunnel, error) {
	rows, err := s.db.QueryContext(ctx, `select id,name,source_server_id,target_server_id,type,local_address,peer_address,listen_port,target_endpoint,target_port,priority,config_json,enabled,created_at,updated_at from tunnels order by priority asc,id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTunnels(rows)
}

func (s *Store) GetTunnel(ctx context.Context, id int64) (*model.Tunnel, error) {
	items, err := s.ListTunnels(ctx)
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

func scanTunnels(rows *sql.Rows) ([]model.Tunnel, error) {
	var out []model.Tunnel
	for rows.Next() {
		var v model.Tunnel
		var en int
		var ca, ua string
		if err := rows.Scan(&v.ID, &v.Name, &v.SourceServerID, &v.TargetServerID, &v.Type, &v.LocalAddress, &v.PeerAddress, &v.ListenPort, &v.TargetEndpoint, &v.TargetPort, &v.Priority, &v.ConfigJSON, &en, &ca, &ua); err != nil {
			return nil, err
		}
		v.Enabled = en == 1
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) AddPortForwardProbeResult(ctx context.Context, v model.PortForwardProbeResult) error {
	_, err := s.db.ExecContext(ctx, `insert into port_forward_probe_results(port_forward_id,server_id,mode,available,latency_ms,sample_count,error,result_json,created_at) values(?,?,?,?,?,?,?,?,?)`, v.PortForwardID, v.ServerID, v.Mode, boolInt(v.Available), v.LatencyMS, v.SampleCount, v.Error, v.ResultJSON, now())
	return err
}

func (s *Store) ListPortForwardProbeResults(ctx context.Context, serverID int64, portForwardID int64, limit int) ([]model.PortForwardProbeResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const baseQuery = `select id,port_forward_id,server_id,mode,available,latency_ms,sample_count,error,result_json,created_at from port_forward_probe_results`
	query := baseQuery
	args := []any{}
	switch {
	case serverID > 0 && portForwardID > 0:
		query = baseQuery + ` where server_id=? and port_forward_id=?`
		args = append(args, serverID, portForwardID)
	case serverID > 0:
		query = baseQuery + ` where server_id=?`
		args = append(args, serverID)
	case portForwardID > 0:
		query = baseQuery + ` where port_forward_id=?`
		args = append(args, portForwardID)
	}
	query += " order by id desc limit ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.PortForwardProbeResult
	for rows.Next() {
		var v model.PortForwardProbeResult
		var available int
		var created string
		if err := rows.Scan(&v.ID, &v.PortForwardID, &v.ServerID, &v.Mode, &available, &v.LatencyMS, &v.SampleCount, &v.Error, &v.ResultJSON, &created); err != nil {
			return nil, err
		}
		v.Available = available == 1
		v.CreatedAt = parseTime(created)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) AddInboundProbeResult(ctx context.Context, v model.InboundProbeResult) error {
	_, err := s.db.ExecContext(ctx, `insert into inbound_probe_results(inbound_id,server_id,config_version,mode,transport,endpoint,available,confirmed,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,result_json,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.InboundID, v.ServerID, v.ConfigVersion, v.Mode, v.Transport, v.Endpoint, boolInt(v.Available), boolInt(v.Confirmed), v.LatencyMS, v.MinLatencyMS, v.P95LatencyMS, v.JitterMS, v.SampleCount, v.SuccessCount, v.Error, v.ResultJSON, now())
	return err
}

func (s *Store) DeleteInboundProbeResults(ctx context.Context, inboundID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from inbound_probe_results where inbound_id=?`, inboundID)
	return err
}

func (s *Store) DeletePortForwardProbeResults(ctx context.Context, portForwardID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from port_forward_probe_results where port_forward_id=?`, portForwardID)
	return err
}

func (s *Store) ListInboundProbeResults(ctx context.Context, serverID, inboundID int64, limit int) ([]model.InboundProbeResult, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	const baseQuery = `select id,inbound_id,server_id,config_version,mode,transport,endpoint,available,confirmed,latency_ms,min_latency_ms,p95_latency_ms,jitter_ms,sample_count,success_count,error,result_json,created_at from inbound_probe_results`
	query := baseQuery
	args := []any{}
	switch {
	case serverID > 0 && inboundID > 0:
		query = baseQuery + ` where server_id=? and inbound_id=?`
		args = append(args, serverID, inboundID)
	case serverID > 0:
		query = baseQuery + ` where server_id=?`
		args = append(args, serverID)
	case inboundID > 0:
		query = baseQuery + ` where inbound_id=?`
		args = append(args, inboundID)
	}
	query += " order by id desc limit ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.InboundProbeResult
	for rows.Next() {
		var v model.InboundProbeResult
		var available, confirmed int
		var created string
		if err := rows.Scan(&v.ID, &v.InboundID, &v.ServerID, &v.ConfigVersion, &v.Mode, &v.Transport, &v.Endpoint, &available, &confirmed, &v.LatencyMS, &v.MinLatencyMS, &v.P95LatencyMS, &v.JitterMS, &v.SampleCount, &v.SuccessCount, &v.Error, &v.ResultJSON, &created); err != nil {
			return nil, err
		}
		v.Available = available == 1
		v.Confirmed = confirmed == 1
		v.CreatedAt = parseTime(created)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) CreateTask(ctx context.Context, v *model.AgentTask) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	var completed any
	if isTerminalTaskStatus(v.Status) {
		completed = ts
		t := parseTime(ts)
		v.CompletedAt = &t
	}
	res, err := s.db.ExecContext(ctx, `insert into agent_tasks(server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at) values(?,?,?,?,?,?,?,?,?,?)`, v.ServerID, v.Type, v.PayloadJSON, v.Status, v.ResultJSON, v.ConfigVersion, v.Nonce, ts, ts, completed)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func isTerminalTaskStatus(status string) bool {
	switch status {
	case "succeeded", "failed", "rollback_failed":
		return true
	default:
		return false
	}
}

// IsTerminalTaskStatus reports whether a task has settled and must not accept
// further Agent-supplied results.
func IsTerminalTaskStatus(status string) bool { return isTerminalTaskStatus(status) }

func (s *Store) ActiveTaskByServerType(ctx context.Context, serverID int64, taskType string) (*model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where server_id=? and type=? and status in ('pending','running') order by id desc limit 1`, serverID, taskType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanTasks(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) FailStaleActiveTasksByServerType(ctx context.Context, serverID int64, taskType string, olderThan time.Time, result string) error {
	if result == "" {
		result = "{}"
	}
	ts := now()
	_, err := s.db.ExecContext(ctx, `update agent_tasks set status='failed', result_json=?, updated_at=?, completed_at=? where server_id=? and type=? and status in ('pending','running') and updated_at < ?`, result, ts, ts, serverID, taskType, olderThan.UTC().Format(time.RFC3339Nano))
	return err
}

// SetTaskStateForTest rewrites task status and updated_at for cross-package timeout tests.
func (s *Store) SetTaskStateForTest(ctx context.Context, id int64, status string, updatedAt time.Time) error {
	if status == "" {
		_, err := s.db.ExecContext(ctx, `update agent_tasks set updated_at=? where id=?`, updatedAt.UTC().Format(time.RFC3339Nano), id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `update agent_tasks set status=?, updated_at=? where id=?`, status, updatedAt.UTC().Format(time.RFC3339Nano), id)
	return err
}

func (s *Store) FailTimedOutTasks(ctx context.Context, pendingOlderThan, runningOlderThan time.Time, pendingResult, runningResult string) ([]model.AgentTask, error) {
	ts := now()
	failed := []model.AgentTask{}
	if pendingResult == "" {
		pendingResult = "{}"
	}
	if runningResult == "" {
		runningResult = "{}"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	failStatus := func(status string, olderThan time.Time, result string) error {
		if olderThan.IsZero() {
			return nil
		}
		rows, err := tx.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where status=? and updated_at < ? order by id`, status, olderThan.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		items, err := scanTasks(rows)
		closeErr := rows.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if _, err := tx.ExecContext(ctx, `update agent_tasks set status='failed', result_json=?, updated_at=?, completed_at=? where status=? and updated_at < ?`, result, ts, ts, status, olderThan.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		for i := range items {
			items[i].Status = "failed"
			items[i].ResultJSON = result
			items[i].UpdatedAt = parseTime(ts)
			completed := parseTime(ts)
			items[i].CompletedAt = &completed
		}
		failed = append(failed, items...)
		return nil
	}
	if err := failStatus("pending", pendingOlderThan, pendingResult); err != nil {
		return nil, err
	}
	if err := failStatus("running", runningOlderThan, runningResult); err != nil {
		return nil, err
	}
	return failed, tx.Commit()
}

func (s *Store) CompleteTask(ctx context.Context, id int64, status, result string) error {
	if !isTerminalTaskStatus(status) {
		return fmt.Errorf("invalid terminal task status %q", status)
	}
	if result == "" {
		result = "{}"
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `update agent_tasks set status=?, result_json=?, updated_at=?, completed_at=? where id=? and status in ('pending','running')`, status, result, ts, ts, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	// Idempotent success when the task is already in the requested terminal state.
	var current string
	err = s.db.QueryRowContext(ctx, `select status from agent_tasks where id=?`, id).Scan(&current)
	if err != nil {
		return err
	}
	if current == status {
		return nil
	}
	return fmt.Errorf("task %d is not completable (status=%s)", id, current)
}

// RequeueTaskIfRunning returns an in-flight task to the pending queue after an
// Agent connection drops before the task result is reported. The conditional
// update avoids racing with a late /agent/task-results report that may have
// completed the task already.
func (s *Store) RequeueTaskIfRunning(ctx context.Context, id int64, result string) error {
	if result == "" {
		result = "{}"
	}
	_, err := s.db.ExecContext(ctx, `update agent_tasks set status='pending', result_json=?, updated_at=? where id=? and status='running'`, result, now(), id)
	return err
}

func (s *Store) NextTask(ctx context.Context, serverID int64) (*model.AgentTask, error) {
	var task model.AgentTask
	var createdAt, updatedAt string
	var completedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where server_id=? and status='pending' order by id limit 1`, serverID).Scan(&task.ID, &task.ServerID, &task.Type, &task.PayloadJSON, &task.Status, &task.ResultJSON, &task.ConfigVersion, &task.Nonce, &createdAt, &updatedAt, &completedAt)
	if err != nil {
		return nil, err
	}
	ts := now()
	res, err := s.db.ExecContext(ctx, `update agent_tasks set status='running', updated_at=? where id=? and status='pending'`, ts, task.ID)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, sql.ErrNoRows
	}
	task.Status = "running"
	task.CreatedAt = parseTime(createdAt)
	task.UpdatedAt = parseTime(ts)
	task.CompletedAt = parseNullTime(completedAt)
	return &task, nil
}
func (s *Store) GetTask(ctx context.Context, id int64) (*model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanTasks(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) ListTasks(ctx context.Context, limit int) ([]model.AgentTask, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks order by id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) LatestDeploymentTasks(ctx context.Context) ([]model.AgentTask, error) {
	var version sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `select max(config_version) from agent_tasks where config_version > 0 and type = ?`, model.AgentTaskTypeApplyDeployment).Scan(&version); err != nil {
		return nil, err
	}
	if !version.Valid || version.Int64 <= 0 {
		return []model.AgentTask{}, nil
	}
	return s.ListTasksByConfigVersion(ctx, version.Int64)
}

func (s *Store) DismissDeploymentFailure(ctx context.Context, configVersion, actorID int64) error {
	if configVersion <= 0 || actorID <= 0 {
		return errors.New("config version and actor are required")
	}
	_, err := s.db.ExecContext(ctx, `insert into deployment_failure_dismissals(config_version,actor_id,dismissed_at) values(?,?,?) on conflict(config_version) do update set actor_id=excluded.actor_id,dismissed_at=excluded.dismissed_at`, configVersion, actorID, now())
	return err
}

func (s *Store) GetDeploymentFailureDismissal(ctx context.Context, configVersion int64) (*model.DeploymentFailureDismissal, error) {
	var item model.DeploymentFailureDismissal
	var dismissedAt string
	err := s.db.QueryRowContext(ctx, `select config_version,actor_id,dismissed_at from deployment_failure_dismissals where config_version=?`, configVersion).Scan(&item.ConfigVersion, &item.ActorID, &dismissedAt)
	if err != nil {
		return nil, err
	}
	item.DismissedAt = parseTime(dismissedAt)
	return &item, nil
}

func (s *Store) LastSuccessfulTaskByServerType(ctx context.Context, serverID int64, taskType string) (*model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where server_id=? and type=? and status = 'succeeded' order by id desc limit 1`, serverID, taskType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanTasks(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

// LastSuccessfulConfigTaskByServer returns the actual config baseline seen by
// an Agent. A focused apply_core_config can be newer than the last full
// deployment and must participate in deployment diffing.
func (s *Store) LastSuccessfulConfigTaskByServer(ctx context.Context, serverID int64) (*model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where server_id=? and type in (?,?) and status='succeeded' order by config_version desc,id desc limit 1`, serverID, model.AgentTaskTypeApplyDeployment, model.AgentTaskTypeApplyCoreConfig)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanTasks(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) ListTasksByServer(ctx context.Context, serverID int64, limit int) ([]model.AgentTask, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where server_id=? order by id desc limit ?`, serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) ListTasksByConfigVersion(ctx context.Context, version int64) ([]model.AgentTask, error) {
	rows, err := s.db.QueryContext(ctx, `select id,server_id,type,payload_json,status,result_json,config_version,nonce,created_at,updated_at,completed_at from agent_tasks where config_version=? order by id`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}
func scanTasks(rows *sql.Rows) ([]model.AgentTask, error) {
	var out []model.AgentTask
	for rows.Next() {
		var v model.AgentTask
		var ca, ua string
		var co sql.NullString
		if err := rows.Scan(&v.ID, &v.ServerID, &v.Type, &v.PayloadJSON, &v.Status, &v.ResultJSON, &v.ConfigVersion, &v.Nonce, &ca, &ua, &co); err != nil {
			return nil, err
		}
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		v.CompletedAt = parseNullTime(co)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) AddAudit(ctx context.Context, a model.AuditLog) error {
	_, err := s.db.ExecContext(ctx, `insert into audit_logs(actor_id,action,target,detail,ip,created_at) values(?,?,?,?,?,?)`, a.ActorID, a.Action, a.Target, a.Detail, a.IP, now())
	return err
}

func (s *Store) CreateNotificationChannel(ctx context.Context, v *model.NotificationChannel) error {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	if v.Events == "" {
		v.Events = "traffic_quota_exceeded,admin_announcement"
	}
	if v.ConfigJSON == "" {
		v.ConfigJSON = "{}"
	}
	if v.TemplatesJSON == "" {
		v.TemplatesJSON = "{}"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `insert into notification_channels(owner_user_id,name,type,enabled,events,config_json,templates_json,created_at,updated_at) values(?,?,?,?,?,?,?,?,?)`, v.OwnerUserID, v.Name, v.Type, boolInt(v.Enabled), v.Events, v.ConfigJSON, v.TemplatesJSON, ts, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	if err := replaceNotificationChannelTargets(ctx, tx, v.ID, v.UserIDs, ts); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateNotificationChannel(ctx context.Context, v *model.NotificationChannel) error {
	v.UpdatedAt = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `update notification_channels set name=?,type=?,enabled=?,events=?,config_json=?,templates_json=?,updated_at=? where id=? and owner_user_id=?`, v.Name, v.Type, boolInt(v.Enabled), v.Events, v.ConfigJSON, v.TemplatesJSON, v.UpdatedAt.Format(time.RFC3339Nano), v.ID, v.OwnerUserID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	if err := replaceNotificationChannelTargets(ctx, tx, v.ID, v.UserIDs, v.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListNotificationChannelsByOwner(ctx context.Context, ownerUserID int64) ([]model.NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, notificationChannelSelect+` where c.owner_user_id=? group by c.id order by c.id desc`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNotificationChannels(rows)
}

func (s *Store) ListEnabledNotificationChannels(ctx context.Context, event string) ([]model.NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, notificationChannelSelect+` where c.enabled=1 group by c.id order by c.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanNotificationChannels(rows)
	if err != nil {
		return nil, err
	}
	out := items[:0]
	for _, item := range items {
		if eventEnabled(item.Events, event) {
			out = append(out, item)
		}
	}
	return out, nil
}

func (s *Store) ListEnabledNotificationChannelsUnfiltered(ctx context.Context) ([]model.NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, notificationChannelSelect+` where c.enabled=1 group by c.id order by c.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNotificationChannels(rows)
}

func (s *Store) GetNotificationChannel(ctx context.Context, id int64) (*model.NotificationChannel, error) {
	rows, err := s.db.QueryContext(ctx, notificationChannelSelect+` where c.id=? group by c.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanNotificationChannels(rows)
	if err != nil || len(items) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}

func (s *Store) DeleteNotificationChannel(ctx context.Context, id, ownerUserID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `delete from notification_channel_user_targets where channel_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from notification_deliveries where channel_id=?`, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `delete from notification_channels where id=? and owner_user_id=?`, id, ownerUserID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func (s *Store) DeleteNotificationDataForUser(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `delete from notification_deliveries where channel_id in (select id from notification_channels where owner_user_id=?)`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from notification_channel_user_targets where channel_id in (select id from notification_channels where owner_user_id=?) or user_id=?`, userID, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from notification_channels where owner_user_id=?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

const notificationChannelSelect = `select c.id,c.owner_user_id,coalesce(u.username,''),c.name,c.type,c.enabled,c.events,c.config_json,c.templates_json,c.created_at,c.updated_at,coalesce(group_concat(t.user_id),'') from notification_channels c left join users u on u.id=c.owner_user_id left join notification_channel_user_targets t on t.channel_id=c.id`

func scanNotificationChannels(rows *sql.Rows) ([]model.NotificationChannel, error) {
	var out []model.NotificationChannel
	for rows.Next() {
		var v model.NotificationChannel
		var enabled int
		var ca, ua, targets string
		if err := rows.Scan(&v.ID, &v.OwnerUserID, &v.OwnerUsername, &v.Name, &v.Type, &enabled, &v.Events, &v.ConfigJSON, &v.TemplatesJSON, &ca, &ua, &targets); err != nil {
			return nil, err
		}
		v.Enabled = enabled == 1
		for _, value := range strings.Split(targets, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && id > 0 {
				v.UserIDs = append(v.UserIDs, id)
			}
		}
		v.CreatedAt = parseTime(ca)
		v.UpdatedAt = parseTime(ua)
		out = append(out, v)
	}
	return out, rows.Err()
}

func replaceNotificationChannelTargets(ctx context.Context, tx *sql.Tx, channelID int64, userIDs []int64, ts string) error {
	if _, err := tx.ExecContext(ctx, `delete from notification_channel_user_targets where channel_id=?`, channelID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, userID := range userIDs {
		if userID <= 0 || seen[userID] {
			continue
		}
		seen[userID] = true
		if _, err := tx.ExecContext(ctx, `insert into notification_channel_user_targets(channel_id,user_id,created_at) values(?,?,?)`, channelID, userID, ts); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateNotificationAnnouncement(ctx context.Context, v *model.NotificationAnnouncement) error {
	userIDs, err := json.Marshal(v.UserIDs)
	if err != nil {
		return err
	}
	ts := now()
	v.CreatedAt = parseTime(ts)
	res, err := s.db.ExecContext(ctx, `insert into notification_announcements(actor_user_id,actor_name,title,body,user_ids_json,queued_count,created_at) values(?,?,?,?,?,?,?)`, v.ActorUserID, v.ActorName, v.Title, v.Body, string(userIDs), v.QueuedCount, ts)
	if err != nil {
		return err
	}
	v.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateNotificationAnnouncementQueuedCount(ctx context.Context, id int64, count int) error {
	_, err := s.db.ExecContext(ctx, `update notification_announcements set queued_count=? where id=?`, count, id)
	return err
}

func (s *Store) ListNotificationAnnouncements(ctx context.Context, limit int) ([]model.NotificationAnnouncement, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `select id,actor_user_id,actor_name,title,body,user_ids_json,queued_count,created_at from notification_announcements order by id desc limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.NotificationAnnouncement{}
	for rows.Next() {
		var item model.NotificationAnnouncement
		var userIDs, created string
		if err := rows.Scan(&item.ID, &item.ActorUserID, &item.ActorName, &item.Title, &item.Body, &userIDs, &item.QueuedCount, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(userIDs), &item.UserIDs)
		item.CreatedAt = parseTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) QueueNotificationDelivery(ctx context.Context, v *model.NotificationDelivery) (bool, error) {
	ts := now()
	v.CreatedAt = parseTime(ts)
	v.UpdatedAt = v.CreatedAt
	if v.NextAttemptAt.IsZero() {
		v.NextAttemptAt = v.CreatedAt
	}
	res, err := s.db.ExecContext(ctx, `insert or ignore into notification_deliveries(channel_id,event,event_key,title,body,status,attempts,error,next_attempt_at,created_at,updated_at) values(?,?,?,?,?,'pending',0,'',?,?,?)`, v.ChannelID, v.Event, v.EventKey, v.Title, v.Body, v.NextAttemptAt.UTC().Format(time.RFC3339Nano), ts, ts)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	v.ID, _ = res.LastInsertId()
	return true, nil
}

func (s *Store) ListPendingNotificationDeliveries(ctx context.Context, at time.Time, limit int) ([]model.NotificationDelivery, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `select d.id,d.channel_id,d.event,d.event_key,d.title,d.body,d.status,d.attempts,d.error,d.next_attempt_at,d.created_at,d.updated_at,d.sent_at,c.owner_user_id,coalesce(u.username,''),c.name,c.type,c.enabled,c.events,c.config_json,c.templates_json,c.created_at,c.updated_at from notification_deliveries d join notification_channels c on c.id=d.channel_id left join users u on u.id=c.owner_user_id where d.status in ('pending','failed') and d.attempts<3 and d.next_attempt_at<=? and c.enabled=1 order by d.id limit ?`, at.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.NotificationDelivery{}
	for rows.Next() {
		var item model.NotificationDelivery
		var next, created, updated, channelCreated, channelUpdated string
		var sent sql.NullString
		var enabled int
		if err := rows.Scan(&item.ID, &item.ChannelID, &item.Event, &item.EventKey, &item.Title, &item.Body, &item.Status, &item.Attempts, &item.Error, &next, &created, &updated, &sent, &item.Channel.OwnerUserID, &item.Channel.OwnerUsername, &item.Channel.Name, &item.Channel.Type, &enabled, &item.Channel.Events, &item.Channel.ConfigJSON, &item.Channel.TemplatesJSON, &channelCreated, &channelUpdated); err != nil {
			return nil, err
		}
		item.NextAttemptAt = parseTime(next)
		item.CreatedAt = parseTime(created)
		item.UpdatedAt = parseTime(updated)
		item.SentAt = parseNullTime(sent)
		item.Channel.ID = item.ChannelID
		item.Channel.Enabled = enabled == 1
		item.Channel.CreatedAt = parseTime(channelCreated)
		item.Channel.UpdatedAt = parseTime(channelUpdated)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CompleteNotificationDelivery(ctx context.Context, id int64, sendErr error, retryAt time.Time) error {
	ts := now()
	if sendErr == nil {
		_, err := s.db.ExecContext(ctx, `update notification_deliveries set status='sent',attempts=attempts+1,error='',updated_at=?,sent_at=? where id=?`, ts, ts, id)
		return err
	}
	errorText := strings.TrimSpace(sendErr.Error())
	if len(errorText) > 500 {
		errorText = errorText[:500]
	}
	_, err := s.db.ExecContext(ctx, `update notification_deliveries set status='failed',attempts=attempts+1,error=?,next_attempt_at=?,updated_at=? where id=?`, errorText, retryAt.UTC().Format(time.RFC3339Nano), ts, id)
	return err
}

func eventEnabled(events, event string) bool {
	for _, item := range strings.Split(events, ",") {
		if strings.TrimSpace(item) == event {
			return true
		}
	}
	return false
}

func (s *Store) ListAuditPage(ctx context.Context, limit, offset int, action string) ([]model.AuditLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	query := `select id,actor_id,action,target,detail,ip,created_at from audit_logs`
	args := []any{}
	if strings.TrimSpace(action) != "" {
		query += ` where action=?`
		args = append(args, action)
	}
	query += ` order by id desc limit ? offset ?`
	args = append(args, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AuditLog
	for rows.Next() {
		var v model.AuditLog
		var actor sql.NullInt64
		var ca string
		if err := rows.Scan(&v.ID, &actor, &v.Action, &v.Target, &v.Detail, &v.IP, &ca); err != nil {
			return nil, err
		}
		if actor.Valid {
			v.ActorID = &actor.Int64
		}
		v.CreatedAt = parseTime(ca)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) AddTrafficReports(ctx context.Context, reports []model.TrafficReport, period model.TrafficPeriod) ([]string, error) {
	accepted := []string{}
	if len(reports) == 0 {
		return accepted, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `insert into traffic_periods(user_id,period_key,started_at,ends_at,upload_bytes,download_bytes,traffic_limit_bytes,state,updated_at) values(?,?,?,?,0,0,?,?,?) on conflict(user_id,period_key) do update set started_at=excluded.started_at,ends_at=excluded.ends_at,traffic_limit_bytes=excluded.traffic_limit_bytes,updated_at=excluded.updated_at`, period.UserID, period.PeriodKey, period.StartedAt.Format(time.RFC3339Nano), period.EndsAt.Format(time.RFC3339Nano), period.Limit, periodState(period.Upload+period.Download, period.Limit), ts); err != nil {
		return nil, err
	}
	for _, report := range reports {
		if strings.TrimSpace(report.ReportID) == "" || report.UserID <= 0 || report.Upload < 0 || report.Download < 0 {
			continue
		}
		res, err := tx.ExecContext(ctx, `insert or ignore into traffic_reports(report_id,server_id,user_id,inbound_id,path_id,period_key,upload_bytes,download_bytes,started_at,ended_at,created_at) values(?,?,?,?,?,?,?,?,?,?,?)`, report.ReportID, report.ServerID, report.UserID, report.InboundID, report.PathID, report.PeriodKey, report.Upload, report.Download, report.StartedAt.Format(time.RFC3339Nano), report.EndedAt.Format(time.RFC3339Nano), ts)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// The report was already committed during an earlier attempt. Returning
			// its ID lets an Agent safely clear a retry after a lost response.
			accepted = append(accepted, report.ReportID)
			continue
		}
		accepted = append(accepted, report.ReportID)
		if _, err := tx.ExecContext(ctx, `insert into traffic_stats(server_id,user_id,inbound_id,upload_bytes,download_bytes,created_at) values(?,?,?,?,?,?)`, report.ServerID, report.UserID, report.InboundID, report.Upload, report.Download, ts); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `update traffic_periods set upload_bytes=upload_bytes+?, download_bytes=download_bytes+?, state=case when traffic_limit_bytes>0 and upload_bytes+download_bytes+?+?>=traffic_limit_bytes then 'quota_exceeded' else 'active' end, updated_at=? where user_id=? and period_key=?`, report.Upload, report.Download, report.Upload, report.Download, ts, report.UserID, report.PeriodKey); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `insert into traffic_leases(server_id,user_id,period_key,lease_bytes,consumed_bytes,updated_at) values(?,?,?,0,?,?) on conflict(server_id,user_id,period_key) do update set consumed_bytes=consumed_bytes+excluded.consumed_bytes, updated_at=excluded.updated_at`, report.ServerID, report.UserID, report.PeriodKey, report.Upload+report.Download, ts); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update users set traffic_used_bytes=(select coalesce(upload_bytes+download_bytes,0) from traffic_periods where user_id=? and period_key=?), updated_at=? where id=?`, period.UserID, period.PeriodKey, ts, period.UserID); err != nil {
		return nil, err
	}
	return accepted, tx.Commit()
}

type TrafficLeaseAllocation struct {
	RemainingBytes int64
	ResetBytes     int64
}

func (s *Store) EnsureTrafficLeaseAllocation(ctx context.Context, serverID, userID int64, periodKey string, limitBytes, usedBytes int64) (TrafficLeaseAllocation, error) {
	if limitBytes <= 0 || serverID <= 0 || userID <= 0 || periodKey == "" {
		return TrafficLeaseAllocation{}, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return TrafficLeaseAllocation{}, err
	}
	defer conn.Close()
	// Reserve SQLite's writer lock before reading existing allocations. A
	// deferred transaction would allow two policy syncs to calculate from the
	// same remaining quota and over-allocate it.
	if _, err := conn.ExecContext(ctx, `begin immediate`); err != nil {
		return TrafficLeaseAllocation{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
	}()
	ts := now()
	if _, err := conn.ExecContext(ctx, `insert into traffic_leases(server_id,user_id,period_key,lease_bytes,consumed_bytes,updated_at) values(?,?,?,0,0,?) on conflict(server_id,user_id,period_key) do nothing`, serverID, userID, periodKey, ts); err != nil {
		return TrafficLeaseAllocation{}, err
	}
	var currentLease, currentConsumed, otherUnconsumed int64
	if err := conn.QueryRowContext(ctx, `select lease_bytes, consumed_bytes from traffic_leases where server_id=? and user_id=? and period_key=?`, serverID, userID, periodKey).Scan(&currentLease, &currentConsumed); err != nil {
		return TrafficLeaseAllocation{}, err
	}
	var durableUsed int64
	if err := conn.QueryRowContext(ctx, `select upload_bytes+download_bytes from traffic_periods where user_id=? and period_key=?`, userID, periodKey).Scan(&durableUsed); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TrafficLeaseAllocation{}, err
	} else if err == nil && durableUsed > usedBytes {
		usedBytes = durableUsed
	}
	if err := conn.QueryRowContext(ctx, `select coalesce(sum(case when lease_bytes>consumed_bytes then lease_bytes-consumed_bytes else 0 end),0) from traffic_leases where server_id<>? and user_id=? and period_key=?`, serverID, userID, periodKey).Scan(&otherUnconsumed); err != nil {
		return TrafficLeaseAllocation{}, err
	}
	currentRemaining := currentLease - currentConsumed
	if currentRemaining < 0 {
		currentRemaining = 0
	}
	globalRemaining := limitBytes - usedBytes
	if globalRemaining < 0 {
		globalRemaining = 0
	}
	available := globalRemaining - otherUnconsumed - currentRemaining
	if available < 0 {
		available = 0
	}
	chunk := limitBytes / 10
	if chunk < 64<<20 {
		chunk = 64 << 20
	}
	if chunk > limitBytes {
		chunk = limitBytes
	}
	if currentRemaining < chunk/2 && available > 0 {
		grant := chunk - currentRemaining
		if grant > available {
			grant = available
		}
		if currentLease < currentConsumed {
			currentLease = currentConsumed
		}
		currentLease += grant
		currentRemaining += grant
		if _, err := conn.ExecContext(ctx, `update traffic_leases set lease_bytes=?, updated_at=? where server_id=? and user_id=? and period_key=?`, currentLease, ts, serverID, userID, periodKey); err != nil {
			return TrafficLeaseAllocation{}, err
		}
	}
	if _, err := conn.ExecContext(ctx, `commit`); err != nil {
		return TrafficLeaseAllocation{}, err
	}
	committed = true
	return TrafficLeaseAllocation{RemainingBytes: currentRemaining, ResetBytes: currentLease}, nil
}

func (s *Store) GetTrafficPeriod(ctx context.Context, userID int64, periodKey string) (model.TrafficPeriod, error) {
	var p model.TrafficPeriod
	var start, end, updated string
	err := s.db.QueryRowContext(ctx, `select id,user_id,period_key,started_at,ends_at,upload_bytes,download_bytes,traffic_limit_bytes,state,updated_at from traffic_periods where user_id=? and period_key=?`, userID, periodKey).Scan(&p.ID, &p.UserID, &p.PeriodKey, &start, &end, &p.Upload, &p.Download, &p.Limit, &p.State, &updated)
	if err != nil {
		return model.TrafficPeriod{}, err
	}
	p.StartedAt = parseTime(start)
	p.EndsAt = parseTime(end)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

func (s *Store) EnsureTrafficPeriod(ctx context.Context, userID int64, periodKey string, start, end time.Time, limit int64) (model.TrafficPeriod, error) {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `insert into traffic_periods(user_id,period_key,started_at,ends_at,upload_bytes,download_bytes,traffic_limit_bytes,state,updated_at) values(?,?,?,?,0,0,?,'active',?) on conflict(user_id,period_key) do update set started_at=excluded.started_at,ends_at=excluded.ends_at,traffic_limit_bytes=excluded.traffic_limit_bytes,state=case when traffic_limit_bytes>0 and upload_bytes+download_bytes>=excluded.traffic_limit_bytes then 'quota_exceeded' else 'active' end,updated_at=excluded.updated_at`, userID, periodKey, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), limit, ts); err != nil {
		return model.TrafficPeriod{}, err
	}
	return s.GetTrafficPeriod(ctx, userID, periodKey)
}

func periodState(used, limit int64) string {
	if limit > 0 && used >= limit {
		return "quota_exceeded"
	}
	return "active"
}

func (s *Store) Dashboard(ctx context.Context) (model.DashboardSummary, error) {
	var d model.DashboardSummary
	queries := []struct {
		query string
		dest  []any
	}{
		{`select count(*) from servers`, []any{&d.ServersTotal}},
		{`select count(*) from servers where status='online'`, []any{&d.ServersOnline}},
		{`select count(*) from servers where status='offline'`, []any{&d.ServersOffline}},
		{`select count(*) from servers where status='degraded'`, []any{&d.ServersDegraded}},
		{`select count(*) from users`, []any{&d.UsersTotal}},
		{`select count(*) from users where status='active'`, []any{&d.UsersActive}},
		{`select coalesce(sum(p.upload_bytes),0),coalesce(sum(p.download_bytes),0)
			from traffic_periods p
			join (select user_id,max(started_at) as started_at from traffic_periods group by user_id) latest
			  on latest.user_id=p.user_id and latest.started_at=p.started_at`, []any{&d.TrafficUpload, &d.TrafficDownload}},
		{`select count(*) from agent_tasks where status='pending'`, []any{&d.PendingTasks}},
		{`select count(*) from agent_tasks where status='running'`, []any{&d.RunningTasks}},
		{`select count(*) from agent_tasks where status in ('failed','rollback_failed')`, []any{&d.FailedTasks}},
		{`select coalesce(max(config_version),0) from agent_tasks`, []any{&d.LastConfigVersion}},
	}
	for _, item := range queries {
		if err := s.db.QueryRowContext(ctx, item.query).Scan(item.dest...); err != nil {
			return model.DashboardSummary{}, err
		}
	}
	return d, nil
}

type FullRoutingConfig struct {
	Servers                      []model.Server                      `json:"servers"`
	Inbounds                     []model.Inbound                     `json:"inbounds"`
	InboundUsers                 []model.InboundUser                 `json:"inbound_users"`
	UserGroups                   []model.UserGroup                   `json:"user_groups"`
	UserGroupMembers             []model.UserGroupMember             `json:"user_group_members"`
	InboundAccessGrants          []model.InboundAccessGrant          `json:"inbound_access_grants"`
	Outbounds                    []model.Outbound                    `json:"outbounds"`
	RoutingRules                 []model.RoutingRule                 `json:"routing_rules"`
	ExternalOutbounds            []model.ExternalOutbound            `json:"external_outbounds"`
	ExternalOutboundAccessGrants []model.ExternalOutboundAccessGrant `json:"external_outbound_access_grants"`
	ProxyPaths                   []model.ProxyPath                   `json:"proxy_paths"`
	ProxyPathSteps               []model.ProxyPathStep               `json:"proxy_path_steps"`
	ProxyPathEgressResults       []model.ProxyPathEgressResult       `json:"proxy_path_egress_results"`
	WARPProfiles                 []model.WARPProfile                 `json:"warp_profiles"`
	DNSLists                     []model.DNSList                     `json:"dns_lists"`
	ServerDNSPolicies            []model.ServerDNSPolicy             `json:"server_dns_policies"`
	Users                        []model.User                        `json:"users"`
	ProxyPathPortAllocations     []model.ProxyPathPortAllocation     `json:"proxy_path_port_allocations"`
}

func (s *Store) FullRoutingConfigData(ctx context.Context) (FullRoutingConfig, error) {
	servers, err := s.ListServers(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	in, err := s.ListInbounds(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	out, err := s.ListOutbounds(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	dnsLists, err := s.ListDNSLists(ctx, false)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	dnsPolicies, err := s.ListServerDNSPolicies(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	portAllocations, err := s.ListProxyPathPortAllocations(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	rules, err := s.ListRoutingRules(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	external, err := s.ListExternalOutbounds(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	externalGrants, err := s.ListExternalOutboundAccessGrants(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	proxyPaths, err := s.ListProxyPaths(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	proxyPathSteps, err := s.ListProxyPathSteps(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	proxyPathEgressResults, err := s.ListProxyPathEgressResults(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	warp, err := s.ListWARPProfiles(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	inboundUsers, err := s.ListInboundUsers(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	groups, err := s.ListUserGroups(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	members, err := s.ListUserGroupMembers(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	grants, err := s.ListInboundAccessGrants(ctx)
	if err != nil {
		return FullRoutingConfig{}, err
	}
	return FullRoutingConfig{Servers: servers, Inbounds: in, InboundUsers: inboundUsers, UserGroups: groups, UserGroupMembers: members, InboundAccessGrants: grants, Outbounds: out, RoutingRules: rules, ExternalOutbounds: external, ExternalOutboundAccessGrants: externalGrants, ProxyPaths: proxyPaths, ProxyPathSteps: proxyPathSteps, ProxyPathEgressResults: proxyPathEgressResults, WARPProfiles: warp, DNSLists: dnsLists, ServerDNSPolicies: dnsPolicies, Users: users, ProxyPathPortAllocations: portAllocations}, nil
}

func nullEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func parseTime(v string) time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t
	}
	return time.Time{}
}

func parseNullTime(v sql.NullString) *time.Time {
	if !v.Valid || strings.TrimSpace(v.String) == "" {
		return nil
	}
	t := parseTime(v.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func timePtrString(v *time.Time) any {
	if v == nil || v.IsZero() {
		return nil
	}
	return v.UTC().Format(time.RFC3339Nano)
}

func nilTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.UTC().Format(time.RFC3339Nano)
}
func deleteSQLForTable(t string) (string, bool) {
	switch t {
	case "users":
		return `delete from users where id=?`, true
	case "servers":
		return `delete from servers where id=?`, true
	case "inbounds":
		return `delete from inbounds where id=?`, true
	case "inbound_users":
		return `delete from inbound_users where id=?`, true
	case "user_groups":
		return `delete from user_groups where id=?`, true
	case "user_group_members":
		return `delete from user_group_members where id=?`, true
	case "inbound_access_grants":
		return `delete from inbound_access_grants where id=?`, true
	case "outbounds":
		return `delete from outbounds where id=?`, true
	case "routing_rules":
		return `delete from routing_rules where id=?`, true
	case "external_outbounds":
		return `delete from external_outbounds where id=?`, true
	case "external_outbound_access_grants":
		return `delete from external_outbound_access_grants where id=?`, true
	case "proxy_paths":
		return `delete from proxy_paths where id=?`, true
	case "proxy_path_steps":
		return `delete from proxy_path_steps where id=?`, true
	case "warp_profiles":
		return `delete from warp_profiles where id=?`, true
	case "port_forwards":
		return `delete from port_forwards where id=?`, true
	case "tunnels":
		return `delete from tunnels where id=?`, true
	case "notification_channels":
		return `delete from notification_channels where id=?`, true
	case "subscription_profiles":
		return `delete from subscription_profiles where id=?`, true
	case "subscription_assignments":
		return `delete from subscription_assignments where id=?`, true
	default:
		return "", false
	}
}

func (s *Store) ValidateServerExists(ctx context.Context, ids ...int64) error {
	for _, id := range ids {
		if _, err := s.GetServer(ctx, id); err != nil {
			return fmt.Errorf("server %d: %w", id, err)
		}
	}
	return nil
}
