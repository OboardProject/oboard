package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

var (
	ErrPluginConflict      = errors.New("plugin record changed")
	ErrPluginStateQuota    = errors.New("plugin state quota exceeded")
	ErrPluginStateConflict = errors.New("plugin state version changed")
	ErrPluginVersionExists = errors.New("plugin version already exists")
)

type pluginScanner interface{ Scan(...any) error }

const pluginInstallationColumns = `id,plugin_key,name,description,active_package_id,publisher_identity,enabled,draft_manifest_json,draft_source,draft_updated_at,created_at,updated_at`

func scanPluginInstallation(row pluginScanner) (model.PluginInstallation, error) {
	var item model.PluginInstallation
	var enabled int
	var draftManifest string
	var draftUpdated sql.NullString
	var created, updated string
	if err := row.Scan(&item.ID, &item.PluginKey, &item.Name, &item.Description, &item.ActivePackageID, &item.PublisherIdentity, &enabled, &draftManifest, &item.DraftSource, &draftUpdated, &created, &updated); err != nil {
		return item, err
	}
	item.Enabled = enabled == 1
	if draftManifest != "" {
		item.DraftManifestJSON = json.RawMessage(draftManifest)
	}
	item.DraftUpdatedAt = parseNullTime(draftUpdated)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, nil
}

const pluginPackageColumns = `id,installation_id,plugin_key,version,sha256,manifest_json,source,icon_png,readme,license,publisher_identity,publisher_name,signature_state,source_kind,source_repository,source_commit,created_by_user_id,created_at`

func scanPluginPackage(row pluginScanner) (model.PluginPackage, error) {
	var item model.PluginPackage
	var manifest, created string
	if err := row.Scan(&item.ID, &item.InstallationID, &item.PluginKey, &item.Version, &item.SHA256, &manifest, &item.Source, &item.IconPNG, &item.Readme, &item.License, &item.PublisherIdentity, &item.PublisherName, &item.SignatureState, &item.SourceKind, &item.SourceRepository, &item.SourceCommit, &item.CreatedByUserID, &created); err != nil {
		return item, err
	}
	item.ManifestJSON = json.RawMessage(manifest)
	item.CreatedAt = parseTime(created)
	return item, nil
}

const pluginInstanceColumns = `id,installation_id,name,enabled,auto_paused,permission_review_required,values_json,custom_json,revision,failure_streak,last_run_at,last_success_at,last_error_code,created_at,updated_at`

func scanPluginInstance(row pluginScanner) (model.PluginInstance, error) {
	var item model.PluginInstance
	var enabled, paused, review int
	var values, custom, created, updated string
	var lastRun, lastSuccess sql.NullString
	if err := row.Scan(&item.ID, &item.InstallationID, &item.Name, &enabled, &paused, &review, &values, &custom, &item.Revision, &item.FailureStreak, &lastRun, &lastSuccess, &item.LastErrorCode, &created, &updated); err != nil {
		return item, err
	}
	item.Enabled, item.AutoPaused, item.PermissionReviewRequired = enabled == 1, paused == 1, review == 1
	item.ValuesJSON, item.CustomJSON = json.RawMessage(values), json.RawMessage(custom)
	item.LastRunAt, item.LastSuccessAt = parseNullTime(lastRun), parseNullTime(lastSuccess)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, nil
}

const pluginScheduleColumns = `id,instance_id,kind,interval,cron,timezone,event,enabled,next_due_at,last_fired_at,last_skipped_at,last_skip_reason,created_at,updated_at`

func scanPluginSchedule(row pluginScanner) (model.PluginSchedule, error) {
	var item model.PluginSchedule
	var enabled int
	var next, fired, skipped sql.NullString
	var created, updated string
	if err := row.Scan(&item.ID, &item.InstanceID, &item.Kind, &item.Interval, &item.Cron, &item.Timezone, &item.Event, &enabled, &next, &fired, &skipped, &item.LastSkipReason, &created, &updated); err != nil {
		return item, err
	}
	item.Enabled = enabled == 1
	item.NextDueAt, item.LastFiredAt, item.LastSkippedAt = parseNullTime(next), parseNullTime(fired), parseNullTime(skipped)
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, nil
}

const pluginRunColumns = `id,uuid,installation_id,instance_id,package_id,plugin_key,plugin_version,trigger,trigger_json,caller_principal,idempotency_key,status,error_code,error_message,cancel_requested,queued_at,started_at,finished_at,capability_call_count,http_call_count,agent_operation_count,result_json,lease_owner,lease_generation,lease_until,recovery_generation`

func scanPluginRun(row pluginScanner) (model.PluginRun, error) {
	var item model.PluginRun
	var triggerJSON, result, queued string
	var cancel int
	var started, finished, leaseUntil sql.NullString
	if err := row.Scan(&item.ID, &item.UUID, &item.InstallationID, &item.InstanceID, &item.PackageID, &item.PluginKey, &item.PluginVersion, &item.Trigger, &triggerJSON, &item.CallerPrincipal, &item.IdempotencyKey, &item.Status, &item.ErrorCode, &item.ErrorMessage, &cancel, &queued, &started, &finished, &item.CapabilityCallCount, &item.HTTPCallCount, &item.AgentOperationCount, &result, &item.LeaseOwner, &item.LeaseGeneration, &leaseUntil, &item.RecoveryGeneration); err != nil {
		return item, err
	}
	item.TriggerJSON = json.RawMessage(triggerJSON)
	if result != "" {
		item.ResultJSON = json.RawMessage(result)
	}
	item.CancelRequested = cancel == 1
	item.QueuedAt = parseTime(queued)
	item.StartedAt, item.FinishedAt, item.LeaseUntil = parseNullTime(started), parseNullTime(finished), parseNullTime(leaseUntil)
	return item, nil
}

func pluginChanged(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPluginConflict
	}
	return nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

// --- installations and packages ---

// InstallPluginPackage creates a new installation with its first package and
// first (disabled, ungranted) instance in one transaction.
func (s *Store) InstallPluginPackage(ctx context.Context, installation *model.PluginInstallation, pkg *model.PluginPackage, instanceName string) (model.PluginInstance, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.PluginInstance{}, err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.ExecContext(ctx, `insert into plugin_installations(plugin_key,name,description,active_package_id,publisher_identity,enabled,created_at,updated_at) values(?,?,?,0,?,0,?,?)`,
		installation.PluginKey, installation.Name, installation.Description, installation.PublisherIdentity, ts, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return model.PluginInstance{}, ErrPluginConflict
		}
		return model.PluginInstance{}, err
	}
	installation.ID, _ = res.LastInsertId()
	pkg.InstallationID = installation.ID
	if err := insertPluginPackageTx(ctx, tx, pkg, ts); err != nil {
		return model.PluginInstance{}, err
	}
	if _, err := tx.ExecContext(ctx, `update plugin_installations set active_package_id=? where id=?`, pkg.ID, installation.ID); err != nil {
		return model.PluginInstance{}, err
	}
	installation.ActivePackageID = pkg.ID
	instance := model.PluginInstance{InstallationID: installation.ID, Name: instanceName, ValuesJSON: json.RawMessage(`{}`), CustomJSON: json.RawMessage(`[]`), Revision: 1}
	if err := insertPluginInstanceTx(ctx, tx, &instance, ts); err != nil {
		return model.PluginInstance{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.PluginInstance{}, err
	}
	installation.CreatedAt, installation.UpdatedAt = parseTime(ts), parseTime(ts)
	return instance, nil
}

// CreatePluginDraftInstallation creates an editor-only installation with a
// draft and no executable package.
func (s *Store) CreatePluginDraftInstallation(ctx context.Context, installation *model.PluginInstallation) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into plugin_installations(plugin_key,name,description,active_package_id,publisher_identity,enabled,draft_manifest_json,draft_source,draft_updated_at,created_at,updated_at) values(?,?,?,0,?,0,?,?,?,?,?)`,
		installation.PluginKey, installation.Name, installation.Description, installation.PublisherIdentity, string(installation.DraftManifestJSON), installation.DraftSource, ts, ts, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPluginConflict
		}
		return err
	}
	installation.ID, _ = res.LastInsertId()
	return nil
}

func insertPluginPackageTx(ctx context.Context, tx *sql.Tx, pkg *model.PluginPackage, ts string) error {
	res, err := tx.ExecContext(ctx, `insert into plugin_packages(installation_id,plugin_key,version,sha256,manifest_json,source,icon_png,readme,license,publisher_identity,publisher_name,signature_state,source_kind,source_repository,source_commit,created_by_user_id,created_at) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		pkg.InstallationID, pkg.PluginKey, pkg.Version, pkg.SHA256, string(pkg.ManifestJSON), pkg.Source, pkg.IconPNG, pkg.Readme, pkg.License, pkg.PublisherIdentity, pkg.PublisherName, pkg.SignatureState, pkg.SourceKind, pkg.SourceRepository, pkg.SourceCommit, pkg.CreatedByUserID, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPluginVersionExists
		}
		return err
	}
	pkg.ID, _ = res.LastInsertId()
	pkg.CreatedAt = parseTime(ts)
	return nil
}

func (s *Store) GetPluginInstallation(ctx context.Context, id int64) (model.PluginInstallation, error) {
	return scanPluginInstallation(s.db.QueryRowContext(ctx, `select `+pluginInstallationColumns+` from plugin_installations where id=?`, id))
}

func (s *Store) GetPluginInstallationByKey(ctx context.Context, key string) (model.PluginInstallation, error) {
	return scanPluginInstallation(s.db.QueryRowContext(ctx, `select `+pluginInstallationColumns+` from plugin_installations where plugin_key=?`, key))
}

func (s *Store) ListPluginInstallations(ctx context.Context) ([]model.PluginInstallation, error) {
	rows, err := s.db.QueryContext(ctx, `select `+pluginInstallationColumns+` from plugin_installations order by lower(name), id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginInstallation{}
	for rows.Next() {
		item, err := scanPluginInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SetPluginInstallationEnabled(ctx context.Context, id int64, enabled bool) error {
	return pluginChanged(s.db.ExecContext(ctx, `update plugin_installations set enabled=?,updated_at=? where id=?`, boolInt(enabled), now(), id))
}

func (s *Store) SavePluginDraft(ctx context.Context, id int64, manifestJSON []byte, source string) error {
	ts := now()
	return pluginChanged(s.db.ExecContext(ctx, `update plugin_installations set draft_manifest_json=?,draft_source=?,draft_updated_at=?,updated_at=? where id=?`, string(manifestJSON), source, ts, ts, id))
}

// PluginActivation carries a version switch: every instance gets its grant
// restricted to the new manifest and its removed variables pruned.
type PluginActivation struct {
	InstallationID  int64
	Name            string
	Description     string
	ClearDraft      bool
	Instances       []PluginInstanceActivation
	ExpectedPackage int64
}

type PluginInstanceActivation struct {
	InstanceID    int64
	Values        json.RawMessage
	Custom        json.RawMessage
	Grant         json.RawMessage
	HadGrant      bool
	RequireReview bool
}

// AddAndActivatePluginPackage stores a new immutable package (or reuses
// packageID when it already exists) and switches every instance to it.
func (s *Store) AddAndActivatePluginPackage(ctx context.Context, pkg *model.PluginPackage, activation PluginActivation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if pkg.ID == 0 {
		pkg.InstallationID = activation.InstallationID
		if err := insertPluginPackageTx(ctx, tx, pkg, ts); err != nil {
			return err
		}
	}
	draftClause := ""
	if activation.ClearDraft {
		draftClause = `,draft_manifest_json='',draft_source='',draft_updated_at=null`
	}
	if err := pluginChanged(tx.ExecContext(ctx, `update plugin_installations set active_package_id=?,name=?,description=?,updated_at=?`+draftClause+` where id=? and active_package_id=?`,
		pkg.ID, activation.Name, activation.Description, ts, activation.InstallationID, activation.ExpectedPackage)); err != nil {
		return err
	}
	for _, item := range activation.Instances {
		if _, err := tx.ExecContext(ctx, `update plugin_instances set values_json=?,custom_json=?,permission_review_required=?,revision=revision+1,updated_at=? where id=? and installation_id=?`,
			string(item.Values), string(item.Custom), boolInt(item.RequireReview), ts, item.InstanceID, activation.InstallationID); err != nil {
			return err
		}
		if item.HadGrant {
			if _, err := tx.ExecContext(ctx, `update plugin_grants set package_id=?,grant_json=?,revision=revision+1 where instance_id=?`, pkg.ID, string(item.Grant), item.InstanceID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// DeletePluginInstallation removes the package, instances, grants, secrets,
// state and schedules. Run history and audit events remain.
func (s *Store) DeletePluginInstallation(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `update plugin_runs set status='cancelled',error_code='CANCELLED',error_message='插件已卸载',finished_at=?,lease_generation=lease_generation+1,lease_owner='',lease_until=null where installation_id=? and status='queued'`, ts, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update plugin_runs set cancel_requested=1 where installation_id=? and status='running'`, id); err != nil {
		return err
	}
	if err := pluginChanged(tx.ExecContext(ctx, `delete from plugin_installations where id=?`, id)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetPluginPackage(ctx context.Context, id int64) (model.PluginPackage, error) {
	return scanPluginPackage(s.db.QueryRowContext(ctx, `select `+pluginPackageColumns+` from plugin_packages where id=?`, id))
}

func (s *Store) GetPluginPackageBySHA(ctx context.Context, installationID int64, sha string) (model.PluginPackage, error) {
	return scanPluginPackage(s.db.QueryRowContext(ctx, `select `+pluginPackageColumns+` from plugin_packages where installation_id=? and sha256=?`, installationID, sha))
}

func (s *Store) ListPluginPackages(ctx context.Context, installationID int64) ([]model.PluginPackage, error) {
	rows, err := s.db.QueryContext(ctx, `select `+pluginPackageColumns+` from plugin_packages where installation_id=? order by id desc`, installationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginPackage{}
	for rows.Next() {
		item, err := scanPluginPackage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// --- instances ---

func insertPluginInstanceTx(ctx context.Context, tx *sql.Tx, item *model.PluginInstance, ts string) error {
	res, err := tx.ExecContext(ctx, `insert into plugin_instances(installation_id,name,enabled,values_json,custom_json,revision,created_at,updated_at) values(?,?,?,?,?,1,?,?)`,
		item.InstallationID, item.Name, boolInt(item.Enabled), string(orJSONObject(item.ValuesJSON)), string(orJSONArray(item.CustomJSON)), ts, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPluginConflict
		}
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.Revision = 1
	item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) CreatePluginInstance(ctx context.Context, item *model.PluginInstance) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertPluginInstanceTx(ctx, tx, item, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetPluginInstance(ctx context.Context, id int64) (model.PluginInstance, error) {
	return scanPluginInstance(s.db.QueryRowContext(ctx, `select `+pluginInstanceColumns+` from plugin_instances where id=?`, id))
}

func (s *Store) ListPluginInstances(ctx context.Context, installationID int64) ([]model.PluginInstance, error) {
	query := `select ` + pluginInstanceColumns + ` from plugin_instances`
	args := []any{}
	if installationID > 0 {
		query += ` where installation_id=?`
		args = append(args, installationID)
	}
	rows, err := s.db.QueryContext(ctx, query+` order by id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginInstance{}
	for rows.Next() {
		item, err := scanPluginInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// UpdatePluginInstanceEnvironment saves normalized values with optimistic
// concurrency on the instance revision.
func (s *Store) UpdatePluginInstanceEnvironment(ctx context.Context, id, expectedRevision int64, values, custom json.RawMessage) (int64, error) {
	if err := pluginChanged(s.db.ExecContext(ctx, `update plugin_instances set values_json=?,custom_json=?,revision=revision+1,updated_at=? where id=? and revision=?`,
		string(orJSONObject(values)), string(orJSONArray(custom)), now(), id, expectedRevision)); err != nil {
		return 0, err
	}
	return expectedRevision + 1, nil
}

func (s *Store) UpdatePluginInstanceSettings(ctx context.Context, id int64, name string, enabled bool, resume bool) error {
	clause := ""
	if resume {
		clause = `,auto_paused=0,failure_streak=0`
	}
	err := pluginChanged(s.db.ExecContext(ctx, `update plugin_instances set name=?,enabled=?,revision=revision+1,updated_at=?`+clause+` where id=?`, name, boolInt(enabled), now(), id))
	if isUniqueViolation(err) {
		return ErrPluginConflict
	}
	return err
}

func (s *Store) DeletePluginInstance(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	if _, err := tx.ExecContext(ctx, `update plugin_runs set status='cancelled',error_code='CANCELLED',error_message='插件实例已删除',finished_at=?,lease_generation=lease_generation+1,lease_owner='',lease_until=null where instance_id=? and status='queued'`, ts, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update plugin_runs set cancel_requested=1 where instance_id=? and status='running'`, id); err != nil {
		return err
	}
	if err := pluginChanged(tx.ExecContext(ctx, `delete from plugin_instances where id=?`, id)); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordPluginRunOutcome maintains the failure streak. A streak reaching
// pauseAt pauses automatic triggers without touching configuration.
func (s *Store) RecordPluginRunOutcome(ctx context.Context, instanceID int64, succeeded bool, errorCode string, finishedAt time.Time, pauseAt int) error {
	ts := finishedAt.UTC().Format(time.RFC3339Nano)
	if succeeded {
		_, err := s.db.ExecContext(ctx, `update plugin_instances set failure_streak=0,last_run_at=?,last_success_at=?,last_error_code='',updated_at=? where id=?`, ts, ts, now(), instanceID)
		return err
	}
	_, err := s.db.ExecContext(ctx, `update plugin_instances set failure_streak=failure_streak+1,last_run_at=?,last_error_code=?,auto_paused=case when failure_streak+1>=? then 1 else auto_paused end,updated_at=? where id=?`, ts, errorCode, pauseAt, now(), instanceID)
	return err
}

// --- grants ---

func (s *Store) GetPluginGrant(ctx context.Context, instanceID int64) (model.PluginGrant, error) {
	var item model.PluginGrant
	var grant, approved string
	err := s.db.QueryRowContext(ctx, `select instance_id,package_id,grant_json,revision,approved_by_user_id,approved_at from plugin_grants where instance_id=?`, instanceID).
		Scan(&item.InstanceID, &item.PackageID, &grant, &item.Revision, &item.ApprovedByUserID, &approved)
	item.GrantJSON = json.RawMessage(grant)
	item.ApprovedAt = parseTime(approved)
	return item, err
}

// SetPluginGrant replaces the grant for an instance and clears the review
// flag when the grant binds the active package. expectedRevision 0 means
// "no grant expected yet".
func (s *Store) SetPluginGrant(ctx context.Context, grant model.PluginGrant, expectedRevision int64) (model.PluginGrant, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return grant, err
	}
	defer tx.Rollback()
	ts := now()
	var current int64
	err = tx.QueryRowContext(ctx, `select revision from plugin_grants where instance_id=?`, grant.InstanceID).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if expectedRevision != 0 {
			return grant, ErrPluginConflict
		}
		_, err = tx.ExecContext(ctx, `insert into plugin_grants(instance_id,package_id,grant_json,revision,approved_by_user_id,approved_at) values(?,?,?,1,?,?)`, grant.InstanceID, grant.PackageID, string(grant.GrantJSON), grant.ApprovedByUserID, ts)
		grant.Revision = 1
	case err != nil:
		return grant, err
	default:
		if expectedRevision != current {
			return grant, ErrPluginConflict
		}
		_, err = tx.ExecContext(ctx, `update plugin_grants set package_id=?,grant_json=?,revision=revision+1,approved_by_user_id=?,approved_at=? where instance_id=?`, grant.PackageID, string(grant.GrantJSON), grant.ApprovedByUserID, ts, grant.InstanceID)
		grant.Revision = current + 1
	}
	if err != nil {
		return grant, err
	}
	if _, err := tx.ExecContext(ctx, `update plugin_instances set permission_review_required=0,revision=revision+1,updated_at=? where id=? and (select active_package_id from plugin_installations i where i.id=plugin_instances.installation_id)=?`, ts, grant.InstanceID, grant.PackageID); err != nil {
		return grant, err
	}
	if err := tx.Commit(); err != nil {
		return grant, err
	}
	grant.ApprovedAt = parseTime(ts)
	return grant, nil
}

func (s *Store) DeletePluginGrant(ctx context.Context, instanceID int64) error {
	_, err := s.db.ExecContext(ctx, `delete from plugin_grants where instance_id=?`, instanceID)
	return err
}

// --- secrets ---

func (s *Store) SetPluginSecret(ctx context.Context, instanceID int64, name, encrypted string) error {
	_, err := s.db.ExecContext(ctx, `insert into plugin_secrets(instance_id,name,value_encrypted,updated_at) values(?,?,?,?) on conflict(instance_id,name) do update set value_encrypted=excluded.value_encrypted,updated_at=excluded.updated_at`, instanceID, name, encrypted, now())
	return err
}

func (s *Store) DeletePluginSecret(ctx context.Context, instanceID int64, name string) error {
	_, err := s.db.ExecContext(ctx, `delete from plugin_secrets where instance_id=? and name=?`, instanceID, name)
	return err
}

// ListPluginSecretNames returns configured secret names only.
func (s *Store) ListPluginSecretNames(ctx context.Context, instanceID int64) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `select name,updated_at from plugin_secrets where instance_id=?`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var name, updated string
		if err := rows.Scan(&name, &updated); err != nil {
			return nil, err
		}
		out[name] = parseTime(updated)
	}
	return out, rows.Err()
}

func (s *Store) GetPluginSecretEncrypted(ctx context.Context, instanceID int64, name string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, `select value_encrypted from plugin_secrets where instance_id=? and name=?`, instanceID, name).Scan(&value)
	return value, err
}

func (s *Store) ListPluginSecretsEncrypted(ctx context.Context, instanceID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `select name,value_encrypted from plugin_secrets where instance_id=?`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

func rewrapPluginSecrets(ctx context.Context, tx *sql.Tx, sourceSecret, targetSecret string) error {
	type secretRow struct {
		instanceID      int64
		name, encrypted string
	}
	rows, err := tx.QueryContext(ctx, `select instance_id,name,value_encrypted from plugin_secrets where value_encrypted<>''`)
	if err != nil {
		return err
	}
	var items []secretRow
	for rows.Next() {
		var item secretRow
		if err := rows.Scan(&item.instanceID, &item.name, &item.encrypted); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		plain, err := security.DecryptSecret(sourceSecret, "plugin-secret", item.encrypted)
		if err != nil {
			return fmt.Errorf("restore plugin secret: %w", err)
		}
		encrypted, err := security.EncryptSecret(targetSecret, "plugin-secret", plain)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update plugin_secrets set value_encrypted=?,updated_at=? where instance_id=? and name=?`, encrypted, now(), item.instanceID, item.name); err != nil {
			return err
		}
	}
	return nil
}

// --- state ---

func (s *Store) GetPluginState(ctx context.Context, instanceID int64, key string) (model.PluginStateEntry, error) {
	var item model.PluginStateEntry
	var value, updated string
	err := s.db.QueryRowContext(ctx, `select instance_id,key,value_json,version,updated_at from plugin_state where instance_id=? and key=?`, instanceID, key).Scan(&item.InstanceID, &item.Key, &value, &item.Version, &updated)
	item.ValueJSON = json.RawMessage(value)
	item.UpdatedAt = parseTime(updated)
	return item, err
}

func (s *Store) ListPluginState(ctx context.Context, instanceID int64, prefix string, limit int) ([]model.PluginStateEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `select instance_id,key,value_json,version,updated_at from plugin_state where instance_id=? and substr(key,1,?)=? order by key limit ?`, instanceID, len(prefix), prefix, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginStateEntry{}
	for rows.Next() {
		var item model.PluginStateEntry
		var value, updated string
		if err := rows.Scan(&item.InstanceID, &item.Key, &value, &item.Version, &updated); err != nil {
			return nil, err
		}
		item.ValueJSON = json.RawMessage(value)
		item.UpdatedAt = parseTime(updated)
		out = append(out, item)
	}
	return out, rows.Err()
}

// PutPluginState writes one key under quota. expectedVersion nil writes
// unconditionally; 0 means "create only"; a positive value is a CAS.
func (s *Store) PutPluginState(ctx context.Context, instanceID int64, key string, value json.RawMessage, expectedVersion *int64, maxKeys, maxTotalBytes int) (model.PluginStateEntry, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.PluginStateEntry{}, err
	}
	defer tx.Rollback()
	var current int64
	var currentSize int
	err = tx.QueryRowContext(ctx, `select version,length(value_json)+length(key) from plugin_state where instance_id=? and key=?`, instanceID, key).Scan(&current, &currentSize)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return model.PluginStateEntry{}, err
	}
	if expectedVersion != nil && *expectedVersion != current {
		return model.PluginStateEntry{}, ErrPluginStateConflict
	}
	var keys, total int
	if err := tx.QueryRowContext(ctx, `select count(*),coalesce(sum(length(value_json)+length(key)),0) from plugin_state where instance_id=?`, instanceID).Scan(&keys, &total); err != nil {
		return model.PluginStateEntry{}, err
	}
	if !exists && keys+1 > maxKeys || total-currentSize+len(value)+len(key) > maxTotalBytes {
		return model.PluginStateEntry{}, ErrPluginStateQuota
	}
	ts := now()
	version := current + 1
	if exists {
		_, err = tx.ExecContext(ctx, `update plugin_state set value_json=?,version=?,updated_at=? where instance_id=? and key=? and version=?`, string(value), version, ts, instanceID, key, current)
	} else {
		_, err = tx.ExecContext(ctx, `insert into plugin_state(instance_id,key,value_json,version,updated_at) values(?,?,?,?,?)`, instanceID, key, string(value), version, ts)
	}
	if err != nil {
		if isUniqueViolation(err) {
			return model.PluginStateEntry{}, ErrPluginStateConflict
		}
		return model.PluginStateEntry{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.PluginStateEntry{}, err
	}
	return model.PluginStateEntry{InstanceID: instanceID, Key: key, ValueJSON: value, Version: version, UpdatedAt: parseTime(ts)}, nil
}

// DeletePluginState removes a key. expectedVersion nil deletes
// unconditionally; it reports whether a key was removed.
func (s *Store) DeletePluginState(ctx context.Context, instanceID int64, key string, expectedVersion *int64) (bool, error) {
	query := `delete from plugin_state where instance_id=? and key=?`
	args := []any{instanceID, key}
	if expectedVersion != nil {
		query += ` and version=?`
		args = append(args, *expectedVersion)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 && expectedVersion != nil {
		return false, ErrPluginStateConflict
	}
	return n > 0, nil
}

func (s *Store) PluginStateUsage(ctx context.Context, instanceID int64) (int, int, error) {
	var keys, total int
	err := s.db.QueryRowContext(ctx, `select count(*),coalesce(sum(length(value_json)+length(key)),0) from plugin_state where instance_id=?`, instanceID).Scan(&keys, &total)
	return keys, total, err
}

// --- schedules ---

func (s *Store) CreatePluginSchedule(ctx context.Context, item *model.PluginSchedule) error {
	ts := now()
	res, err := s.db.ExecContext(ctx, `insert into plugin_schedules(instance_id,kind,interval,cron,timezone,event,enabled,next_due_at,created_at,updated_at) values(?,?,?,?,?,?,?,?,?,?)`,
		item.InstanceID, item.Kind, item.Interval, item.Cron, item.Timezone, item.Event, boolInt(item.Enabled), timePtrString(item.NextDueAt), ts, ts)
	if err != nil {
		return err
	}
	item.ID, _ = res.LastInsertId()
	item.CreatedAt, item.UpdatedAt = parseTime(ts), parseTime(ts)
	return nil
}

func (s *Store) UpdatePluginSchedule(ctx context.Context, item model.PluginSchedule) error {
	return pluginChanged(s.db.ExecContext(ctx, `update plugin_schedules set kind=?,interval=?,cron=?,timezone=?,event=?,enabled=?,next_due_at=?,updated_at=? where id=? and instance_id=?`,
		item.Kind, item.Interval, item.Cron, item.Timezone, item.Event, boolInt(item.Enabled), timePtrString(item.NextDueAt), now(), item.ID, item.InstanceID))
}

func (s *Store) DeletePluginSchedule(ctx context.Context, id int64) error {
	return pluginChanged(s.db.ExecContext(ctx, `delete from plugin_schedules where id=?`, id))
}

func (s *Store) GetPluginSchedule(ctx context.Context, id int64) (model.PluginSchedule, error) {
	return scanPluginSchedule(s.db.QueryRowContext(ctx, `select `+pluginScheduleColumns+` from plugin_schedules where id=?`, id))
}

func (s *Store) ListPluginSchedules(ctx context.Context, instanceID int64) ([]model.PluginSchedule, error) {
	return s.queryPluginSchedules(ctx, `select `+pluginScheduleColumns+` from plugin_schedules where instance_id=? order by id`, instanceID)
}

func (s *Store) ListDuePluginSchedules(ctx context.Context, until time.Time, limit int) ([]model.PluginSchedule, error) {
	return s.queryPluginSchedules(ctx, `select `+pluginScheduleColumns+` from plugin_schedules where enabled=1 and kind in ('interval','cron') and next_due_at is not null and next_due_at<=? order by next_due_at limit ?`, until.UTC().Format(time.RFC3339Nano), limit)
}

func (s *Store) ListEnabledEventPluginSchedules(ctx context.Context, event string) ([]model.PluginSchedule, error) {
	return s.queryPluginSchedules(ctx, `select `+pluginScheduleColumns+` from plugin_schedules where enabled=1 and kind='event' and event=? order by id`, event)
}

func (s *Store) queryPluginSchedules(ctx context.Context, query string, args ...any) ([]model.PluginSchedule, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginSchedule{}
	for rows.Next() {
		item, err := scanPluginSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkPluginSchedule records a firing or a skip and moves the next slot.
func (s *Store) MarkPluginSchedule(ctx context.Context, id int64, fired bool, skipReason string, next *time.Time) error {
	ts := now()
	if fired {
		_, err := s.db.ExecContext(ctx, `update plugin_schedules set last_fired_at=?,last_skip_reason='',next_due_at=?,updated_at=? where id=?`, ts, timePtrString(next), ts, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `update plugin_schedules set last_skipped_at=?,last_skip_reason=?,next_due_at=?,updated_at=? where id=?`, ts, skipReason, timePtrString(next), ts, id)
	return err
}

// --- runs ---

// CreatePluginRun inserts a queued run unless the instance already has an
// active run (per-instance concurrency is one). The existing run with the same
// idempotency key is returned unchanged.
func (s *Store) CreatePluginRun(ctx context.Context, run *model.PluginRun) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	existing, err := scanPluginRun(tx.QueryRowContext(ctx, `select `+pluginRunColumns+` from plugin_runs where idempotency_key=?`, run.IdempotencyKey))
	if err == nil {
		*run = existing
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `select count(*) from plugin_runs where instance_id=? and status in ('queued','running')`, run.InstanceID).Scan(&active); err != nil {
		return false, err
	}
	if active > 0 {
		return false, ErrPluginConflict
	}
	ts := now()
	res, err := tx.ExecContext(ctx, `insert into plugin_runs(uuid,installation_id,instance_id,package_id,plugin_key,plugin_version,trigger,trigger_json,caller_principal,idempotency_key,status,queued_at,recovery_generation) values(?,?,?,?,?,?,?,?,?,?,'queued',?,?)`,
		run.UUID, run.InstallationID, run.InstanceID, run.PackageID, run.PluginKey, run.PluginVersion, run.Trigger, string(orJSONObject(run.TriggerJSON)), run.CallerPrincipal, run.IdempotencyKey, ts, run.RecoveryGeneration)
	if err != nil {
		return false, err
	}
	run.ID, _ = res.LastInsertId()
	run.Status = model.PluginRunQueued
	run.QueuedAt = parseTime(ts)
	return true, tx.Commit()
}

func (s *Store) GetPluginRun(ctx context.Context, id int64) (model.PluginRun, error) {
	return scanPluginRun(s.db.QueryRowContext(ctx, `select `+pluginRunColumns+` from plugin_runs where id=?`, id))
}

func (s *Store) GetPluginRunByUUID(ctx context.Context, uuid string) (model.PluginRun, error) {
	return scanPluginRun(s.db.QueryRowContext(ctx, `select `+pluginRunColumns+` from plugin_runs where uuid=?`, uuid))
}

type PluginRunFilter struct {
	InstallationID int64
	InstanceID     int64
	BeforeID       int64
	Limit          int
}

func (s *Store) ListPluginRuns(ctx context.Context, filter PluginRunFilter) ([]model.PluginRun, error) {
	if filter.Limit <= 0 || filter.Limit > 200 {
		filter.Limit = 50
	}
	clauses, args := []string{}, []any{}
	if filter.InstallationID > 0 {
		clauses, args = append(clauses, "installation_id=?"), append(args, filter.InstallationID)
	}
	if filter.InstanceID > 0 {
		clauses, args = append(clauses, "instance_id=?"), append(args, filter.InstanceID)
	}
	if filter.BeforeID > 0 {
		clauses, args = append(clauses, "id<?"), append(args, filter.BeforeID)
	}
	query := `select ` + pluginRunColumns + ` from plugin_runs`
	if len(clauses) > 0 {
		query += ` where ` + strings.Join(clauses, " and ")
	}
	rows, err := s.db.QueryContext(ctx, query+` order by id desc limit ?`, append(args, filter.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginRun{}
	for rows.Next() {
		item, err := scanPluginRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CountPluginRunsByStatus(ctx context.Context, statuses ...string) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	args := make([]any, len(statuses))
	for i, status := range statuses {
		args[i] = status
	}
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from plugin_runs where status in (`+strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",")+`)`, args...).Scan(&n)
	return n, err
}

// LeasePluginRun claims the oldest queued run for a worker when fewer than
// maxRunning runs are active. Runs from another recovery generation are never
// leased.
func (s *Store) LeasePluginRun(ctx context.Context, workerID string, until time.Time, recoveryGen int64, maxRunning int) (model.PluginRun, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.PluginRun{}, err
	}
	defer tx.Rollback()
	var running int
	if err := tx.QueryRowContext(ctx, `select count(*) from plugin_runs where status='running'`).Scan(&running); err != nil {
		return model.PluginRun{}, err
	}
	if running >= maxRunning {
		return model.PluginRun{}, sql.ErrNoRows
	}
	run, err := scanPluginRun(tx.QueryRowContext(ctx, `select `+pluginRunColumns+` from plugin_runs where status='queued' and cancel_requested=0 and recovery_generation=? order by queued_at, id limit 1`, recoveryGen))
	if err != nil {
		return model.PluginRun{}, err
	}
	ts := now()
	until = until.UTC()
	if err := pluginChanged(tx.ExecContext(ctx, `update plugin_runs set status='running',started_at=?,lease_owner=?,lease_generation=lease_generation+1,lease_until=? where id=? and status='queued'`, ts, workerID, until.Format(time.RFC3339Nano), run.ID)); err != nil {
		return model.PluginRun{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.PluginRun{}, err
	}
	run.Status = model.PluginRunRunning
	started := parseTime(ts)
	run.StartedAt = &started
	run.LeaseOwner = workerID
	run.LeaseGeneration++
	run.LeaseUntil = &until
	return run, nil
}

// FinishPluginRun records the terminal state for the current lease only.
func (s *Store) FinishPluginRun(ctx context.Context, uuid string, generation int64, status, code, message string, result json.RawMessage) (model.PluginRun, error) {
	ts := now()
	if err := pluginChanged(s.db.ExecContext(ctx, `update plugin_runs set status=?,error_code=?,error_message=?,result_json=?,finished_at=?,lease_owner='',lease_until=null where uuid=? and lease_generation=? and status='running'`,
		status, code, message, string(result), ts, uuid, generation)); err != nil {
		return model.PluginRun{}, err
	}
	return s.GetPluginRunByUUID(ctx, uuid)
}

// FailQueuedPluginRun terminates a run that could not start (for example its
// configuration became invalid before a lease).
func (s *Store) FailQueuedPluginRun(ctx context.Context, id int64, status, code, message string) error {
	ts := now()
	return pluginChanged(s.db.ExecContext(ctx, `update plugin_runs set status=?,error_code=?,error_message=?,finished_at=?,lease_owner='',lease_until=null where id=? and status in ('queued','running')`, status, code, message, ts, id))
}

// RequestPluginRunCancel cancels a queued run immediately and flags a running
// one so the worker kills its runner and the gateway refuses further calls.
func (s *Store) RequestPluginRunCancel(ctx context.Context, id int64) error {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `update plugin_runs set status='cancelled',error_code='CANCELLED',error_message='已取消',finished_at=?,cancel_requested=1 where id=? and status='queued'`, ts, id); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `update plugin_runs set cancel_requested=1 where id=? and status='running'`, id)
	return err
}

// CancelPluginRunsForInstance cancels queued runs and flags running runs.
func (s *Store) CancelPluginRunsForInstance(ctx context.Context, instanceID int64, message string) error {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `update plugin_runs set status='cancelled',error_code='CANCELLED',error_message=?,finished_at=?,cancel_requested=1 where instance_id=? and status='queued'`, message, ts, instanceID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `update plugin_runs set cancel_requested=1 where instance_id=? and status='running'`, instanceID)
	return err
}

func (s *Store) CancelPluginRunsForInstallation(ctx context.Context, installationID int64, message string) error {
	ts := now()
	if _, err := s.db.ExecContext(ctx, `update plugin_runs set status='cancelled',error_code='CANCELLED',error_message=?,finished_at=?,cancel_requested=1 where installation_id=? and status='queued'`, message, ts, installationID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `update plugin_runs set cancel_requested=1 where installation_id=? and status='running'`, installationID)
	return err
}

// ConsumePluginRunBudget atomically increments one counter for the current
// lease and reports false when the budget is exhausted.
func (s *Store) ConsumePluginRunBudget(ctx context.Context, uuid string, generation int64, counter string, limit int) (bool, error) {
	column := ""
	switch counter {
	case "capability":
		column = "capability_call_count"
	case "http":
		column = "http_call_count"
	case "agent":
		column = "agent_operation_count"
	default:
		return false, fmt.Errorf("unknown plugin budget %q", counter)
	}
	res, err := s.db.ExecContext(ctx, `update plugin_runs set `+column+`=`+column+`+1 where uuid=? and lease_generation=? and status='running' and cancel_requested=0 and `+column+`<?`, uuid, generation, limit)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ExpirePluginRunLeases fails running runs whose worker lease lapsed and
// returns them so instance outcomes can be recorded.
func (s *Store) ExpirePluginRunLeases(ctx context.Context, at time.Time) ([]model.PluginRun, error) {
	rows, err := s.db.QueryContext(ctx, `select `+pluginRunColumns+` from plugin_runs where status='running' and lease_until is not null and lease_until<?`, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	expired := []model.PluginRun{}
	for rows.Next() {
		item, err := scanPluginRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		expired = append(expired, item)
	}
	rows.Close()
	out := []model.PluginRun{}
	for _, run := range expired {
		if _, err := s.FinishPluginRun(ctx, run.UUID, run.LeaseGeneration, model.PluginRunFailed, "RUNTIME_UNAVAILABLE", "执行服务未按时回报结果", nil); err == nil {
			run.Status = model.PluginRunFailed
			run.ErrorCode = "RUNTIME_UNAVAILABLE"
			out = append(out, run)
		}
	}
	return out, nil
}

func (s *Store) AppendPluginRunLogs(ctx context.Context, runID int64, logs []model.PluginRunLog) error {
	if len(logs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for _, line := range logs {
		if _, err := tx.ExecContext(ctx, `insert into plugin_run_logs(run_id,seq,level,message,created_at) values(?,?,?,?,?) on conflict(run_id,seq) do nothing`, runID, line.Seq, line.Level, line.Message, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListPluginRunLogs(ctx context.Context, runID, afterSeq int64, limit int) ([]model.PluginRunLog, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `select run_id,seq,level,message,created_at from plugin_run_logs where run_id=? and seq>? order by seq limit ?`, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginRunLog{}
	for rows.Next() {
		var item model.PluginRunLog
		var created string
		if err := rows.Scan(&item.RunID, &item.Seq, &item.Level, &item.Message, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		out = append(out, item)
	}
	return out, rows.Err()
}

// --- audit ---

func (s *Store) UpsertPluginPageSnapshot(ctx context.Context, item model.PluginPageSnapshot) error {
	published := item.PublishedAt.UTC().Format(time.RFC3339Nano)
	if item.PublishedAt.IsZero() {
		published = now()
	}
	_, err := s.db.ExecContext(ctx, `insert into plugin_page_snapshots(instance_id,page_id,document_json,run_uuid,published_at) values(?,?,?,?,?)
		on conflict(instance_id,page_id) do update set document_json=excluded.document_json,run_uuid=excluded.run_uuid,published_at=excluded.published_at`,
		item.InstanceID, item.PageID, string(item.DocumentJSON), item.RunUUID, published)
	return err
}

func (s *Store) ListPluginPageSnapshots(ctx context.Context, instanceID int64) ([]model.PluginPageSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `select instance_id,page_id,document_json,run_uuid,published_at from plugin_page_snapshots where instance_id=? order by page_id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginPageSnapshot{}
	for rows.Next() {
		var item model.PluginPageSnapshot
		var document, published string
		if err := rows.Scan(&item.InstanceID, &item.PageID, &document, &item.RunUUID, &published); err != nil {
			return nil, err
		}
		item.DocumentJSON = json.RawMessage(document)
		item.PublishedAt = parseTime(published)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) InsertPluginAuditEvent(ctx context.Context, item model.PluginAuditEvent) error {
	_, err := s.db.ExecContext(ctx, `insert into plugin_audit_events(created_at,installation_id,instance_id,plugin_key,plugin_version,run_uuid,capability,resource,result,error_code,duration_ms,detail_json) values(?,?,?,?,?,?,?,?,?,?,?,?)`,
		now(), item.InstallationID, item.InstanceID, item.PluginKey, item.PluginVersion, item.RunUUID, item.Capability, item.Resource, item.Result, item.ErrorCode, item.DurationMS, string(orJSONObject(item.DetailJSON)))
	return err
}

func (s *Store) ListPluginAuditEvents(ctx context.Context, instanceID, beforeID int64, limit int) ([]model.PluginAuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `select id,created_at,installation_id,instance_id,plugin_key,plugin_version,run_uuid,capability,resource,result,error_code,duration_ms,detail_json from plugin_audit_events where instance_id=?`
	args := []any{instanceID}
	if beforeID > 0 {
		query += ` and id<?`
		args = append(args, beforeID)
	}
	rows, err := s.db.QueryContext(ctx, query+` order by id desc limit ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginAuditEvent{}
	for rows.Next() {
		var item model.PluginAuditEvent
		var created, detail string
		if err := rows.Scan(&item.ID, &created, &item.InstallationID, &item.InstanceID, &item.PluginKey, &item.PluginVersion, &item.RunUUID, &item.Capability, &item.Resource, &item.Result, &item.ErrorCode, &item.DurationMS, &detail); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.DetailJSON = json.RawMessage(detail)
		out = append(out, item)
	}
	return out, rows.Err()
}

// --- retention, restore and events ---

// PrunePluginHistory removes run logs and finished runs older than the
// retention windows, and audit events older than the run window.
func (s *Store) PrunePluginHistory(ctx context.Context, logsBefore, runsBefore time.Time) error {
	if _, err := s.db.ExecContext(ctx, `delete from plugin_run_logs where run_id in (select id from plugin_runs where finished_at is not null and finished_at<?)`, logsBefore.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `delete from plugin_runs where finished_at is not null and finished_at<?`, runsBefore.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `delete from plugin_audit_events where created_at<?`, runsBefore.UTC().Format(time.RFC3339Nano))
	return err
}

// RevokePluginAuthorityAfterRestore keeps restored plugins inert: the runtime
// is disabled, scheduling paused, every grant revoked (instances need a fresh
// review) and unfinished runs cancelled.
func (s *Store) RevokePluginAuthorityAfterRestore(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	for key, value := range map[string]string{model.PluginSettingEnabled: "false", model.PluginSettingSchedulerPaused: "true"} {
		if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,?,?) on conflict(key) do update set value=excluded.value,updated_at=excluded.updated_at`, key, value, ts); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into app_settings(key,value,updated_at) values(?,'2',?) on conflict(key) do update set value=cast(cast(value as integer)+1 as text),updated_at=excluded.updated_at`, model.PluginSettingRecoveryGeneration, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `delete from plugin_grants`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update plugin_instances set permission_review_required=1,updated_at=?`, ts); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update plugin_runs set status='cancelled',error_code='CANCELLED',error_message='备份恢复后取消',finished_at=?,lease_generation=lease_generation+1,lease_owner='',lease_until=null where status in ('queued','running')`, ts); err != nil {
		return err
	}
	return tx.Commit()
}

type EventOutboxItem struct {
	ID          string
	Topic       string
	AggregateID string
	Payload     json.RawMessage
	Attempts    int
	CreatedAt   time.Time
}

func (s *Store) EnqueuePluginEventTx(ctx context.Context, tx *sql.Tx, topic, aggregateID string, payload json.RawMessage) error {
	ts := now()
	_, err := tx.ExecContext(ctx, `insert into event_outbox(id,topic,aggregate_id,payload_json,status,available_at,created_at) values(?,?,?,?,'pending',?,?)`,
		fmt.Sprintf("evt_%d_%s", time.Now().UTC().UnixNano(), aggregateID), topic, aggregateID, string(payload), ts, ts)
	return err
}

func (s *Store) ClaimPluginEvents(ctx context.Context, owner string, until time.Time, limit int) ([]EventOutboxItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now()
	// A worker can disappear after leasing an event. Requeue leases that have
	// expired before selecting new work; otherwise one abandoned event remains
	// stuck in leased forever and is never retried.
	if _, err := tx.ExecContext(ctx, `update event_outbox set status='pending',lease_owner='',lease_until=null where topic like 'plugin.%' and status='leased' and lease_until is not null and lease_until<=?`, ts); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `select id,topic,aggregate_id,payload_json,attempts,created_at from event_outbox where status='pending' and available_at<=? and topic like 'plugin.%' order by created_at limit ?`, ts, limit)
	if err != nil {
		return nil, err
	}
	var items []EventOutboxItem
	for rows.Next() {
		var item EventOutboxItem
		var payload, created string
		if err := rows.Scan(&item.ID, &item.Topic, &item.AggregateID, &payload, &item.Attempts, &created); err != nil {
			rows.Close()
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	claimed := items[:0]
	for _, item := range items {
		res, err := tx.ExecContext(ctx, `update event_outbox set status='leased',lease_owner=?,lease_until=?,attempts=attempts+1 where id=? and status='pending'`, owner, until.UTC().Format(time.RFC3339Nano), item.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			claimed = append(claimed, item)
		}
	}
	return claimed, tx.Commit()
}

func (s *Store) CompletePluginEvent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `update event_outbox set status='completed',completed_at=? where id=?`, now(), id)
	return err
}

func orJSONObject(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func orJSONArray(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage(`[]`)
	}
	return raw
}

// CountActiveAgentTasks counts one server's pending or running tasks of the
// given types.
func (s *Store) CountActiveAgentTasks(ctx context.Context, serverID int64, types []string) (int, error) {
	if len(types) == 0 {
		return 0, nil
	}
	args := []any{serverID}
	for _, item := range types {
		args = append(args, item)
	}
	var n int
	err := s.db.QueryRowContext(ctx, `select count(*) from agent_tasks where server_id=? and status in ('pending','running') and type in (`+strings.TrimSuffix(strings.Repeat("?,", len(types)), ",")+`)`, args...).Scan(&n)
	return n, err
}
