package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

var ErrPluginPackageConflict = errors.New("plugin package revision conflict")

const pluginInstallationSelect = `select plugin_id,package_id,coalesce(active_revision_id,0),installed,config_json,updated_at from plugin_installations`

func scanPluginInstallation(row interface{ Scan(...any) error }) (model.PluginInstallation, error) {
	var item model.PluginInstallation
	var config, updated string
	err := row.Scan(&item.PluginID, &item.PackageID, &item.ActiveRevisionID, &item.Installed, &config, &updated)
	item.ConfigJSON = json.RawMessage(config)
	item.UpdatedAt = parseTime(updated)
	return item, err
}
func (s *Store) GetPluginInstallation(ctx context.Context, pluginID int64) (model.PluginInstallation, error) {
	return scanPluginInstallation(s.db.QueryRowContext(ctx, pluginInstallationSelect+` where plugin_id=?`, pluginID))
}
func (s *Store) FindPluginInstallation(ctx context.Context, packageID string) (model.PluginInstallation, error) {
	return scanPluginInstallation(s.db.QueryRowContext(ctx, pluginInstallationSelect+` where package_id=?`, packageID))
}

const pluginPackageVersionSelect = `select plugin_id,revision_id,version,sha256,manifest_json,ui_json,source_kind,source_repository,source_commit,created_at from plugin_package_versions`

func scanPluginPackageVersion(row interface{ Scan(...any) error }) (model.PluginPackageVersion, error) {
	var v model.PluginPackageVersion
	var manifest, ui, created string
	err := row.Scan(&v.PluginID, &v.RevisionID, &v.Version, &v.SHA256, &manifest, &ui, &v.SourceKind, &v.SourceRepository, &v.SourceCommit, &created)
	v.ManifestJSON, v.UIJSON, v.CreatedAt = json.RawMessage(manifest), json.RawMessage(ui), parseTime(created)
	return v, err
}
func (s *Store) GetPluginPackageVersion(ctx context.Context, pluginID, revisionID int64) (model.PluginPackageVersion, error) {
	return scanPluginPackageVersion(s.db.QueryRowContext(ctx, pluginPackageVersionSelect+` where plugin_id=? and revision_id=?`, pluginID, revisionID))
}
func (s *Store) ListPluginPackageVersions(ctx context.Context, pluginID int64) ([]model.PluginPackageVersion, error) {
	rows, err := s.db.QueryContext(ctx, pluginPackageVersionSelect+` where plugin_id=? order by revision_id desc`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PluginPackageVersion{}
	for rows.Next() {
		item, err := scanPluginPackageVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// InstallPluginPackage never calls PublishPluginRevision: package publication must
// neither enable the plugin nor supersede the currently active revision.
func (s *Store) InstallPluginPackage(ctx context.Context, metadata model.PluginPackageMetadata, rev model.PluginRevision, version model.PluginPackageVersion, expectedPluginID, expectedActive int64) (model.PluginInstallation, model.PluginPackageVersion, error) {
	var empty model.PluginInstallation
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, version, err
	}
	defer tx.Rollback()
	ts := now()
	item, err := scanPluginInstallation(tx.QueryRowContext(ctx, pluginInstallationSelect+` where package_id=?`, metadata.PluginID))
	if errors.Is(err, sql.ErrNoRows) {
		if expectedPluginID != 0 {
			return empty, version, ErrPluginPackageConflict
		}
		res, err := tx.ExecContext(ctx, `insert into plugins(name,description,owner_user_id,status,created_at,updated_at) values(?,?,?,'disabled',?,?)`, metadata.Name, metadata.Description, rev.AuthorUserID, ts, ts)
		if err != nil {
			return empty, version, err
		}
		item.PluginID, err = res.LastInsertId()
		if err != nil {
			return empty, version, err
		}
		item.PackageID = metadata.PluginID
		item.ConfigJSON = json.RawMessage(`{}`)
		if _, err = tx.ExecContext(ctx, `insert into plugin_installations(plugin_id,package_id,updated_at) values(?,?,?)`, item.PluginID, item.PackageID, ts); err != nil {
			return empty, version, err
		}
	} else if err != nil {
		return empty, version, err
	} else if item.PluginID != expectedPluginID || item.ActiveRevisionID != expectedActive {
		return empty, version, ErrPluginPackageConflict
	}
	existing, err := scanPluginPackageVersion(tx.QueryRowContext(ctx, pluginPackageVersionSelect+` where plugin_id=? and version=?`, item.PluginID, version.Version))
	if err == nil {
		if existing.SHA256 != version.SHA256 {
			return empty, version, ErrPluginPackageConflict
		}
		if !item.Installed {
			item.Installed = true
			item.ActiveRevisionID = existing.RevisionID
			item.UpdatedAt = parseTime(ts)
			if _, err = tx.ExecContext(ctx, `update plugin_installations set installed=1,active_revision_id=?,updated_at=? where plugin_id=?`, existing.RevisionID, ts, item.PluginID); err != nil {
				return empty, version, err
			}
			if _, err = tx.ExecContext(ctx, `update plugins set status='disabled',updated_at=? where id=?`, ts, item.PluginID); err != nil {
				return empty, version, err
			}
		}
		return item, existing, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, version, err
	}
	var number int64
	if err = tx.QueryRowContext(ctx, `select coalesce(max(revision_number),0)+1 from plugin_revisions where plugin_id=?`, item.PluginID).Scan(&number); err != nil {
		return empty, version, err
	}
	res, err := tx.ExecContext(ctx, `insert into plugin_revisions(plugin_id,revision_number,status,schema_version,runtime,sdk_version,source,source_digest,manifest_json,author_user_id,published_at,published_by_user_id,created_at) values(?,?,'published',?,?,?,?,?,?,?,?,?,?)`, item.PluginID, number, rev.SchemaVersion, rev.Runtime, rev.SDKVersion, rev.Source, rev.SourceDigest, string(rev.ManifestJSON), rev.AuthorUserID, ts, rev.AuthorUserID, ts)
	if err != nil {
		return empty, version, err
	}
	version.RevisionID, err = res.LastInsertId()
	if err != nil {
		return empty, version, err
	}
	version.PluginID = item.PluginID
	version.CreatedAt = parseTime(ts)
	if len(version.UIJSON) == 0 {
		version.UIJSON = json.RawMessage(`null`)
	}
	_, err = tx.ExecContext(ctx, `insert into plugin_package_versions(revision_id,plugin_id,version,sha256,manifest_json,ui_json,source_kind,source_repository,source_commit,created_at) values(?,?,?,?,?,?,?,?,?,?)`, version.RevisionID, version.PluginID, version.Version, version.SHA256, string(version.ManifestJSON), string(version.UIJSON), version.SourceKind, version.SourceRepository, version.SourceCommit, ts)
	if err != nil {
		return empty, version, err
	}
	if item.ActiveRevisionID == 0 {
		item.ActiveRevisionID = version.RevisionID
	}
	if !item.Installed {
		if _, err = tx.ExecContext(ctx, `update plugins set status='disabled',updated_at=? where id=?`, ts, item.PluginID); err != nil {
			return empty, version, err
		}
	}
	item.Installed = true
	item.UpdatedAt = parseTime(ts)
	if _, err = tx.ExecContext(ctx, `update plugin_installations set active_revision_id=?,installed=1,updated_at=? where plugin_id=?`, item.ActiveRevisionID, ts, item.PluginID); err != nil {
		return empty, version, err
	}
	return item, version, tx.Commit()
}

func (s *Store) ActivatePluginPackageVersion(ctx context.Context, pluginID, revisionID int64, expectedUpdatedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `update plugin_installations set active_revision_id=?,updated_at=? where plugin_id=? and installed=1 and updated_at=? and exists(select 1 from plugin_package_versions v join plugin_revisions r on r.id=v.revision_id where v.plugin_id=? and v.revision_id=? and r.status='published')`, revisionID, now(), pluginID, expectedUpdatedAt.UTC().Format(time.RFC3339Nano), pluginID, revisionID)
	return pluginExtensionChanged(res, err)
}
func (s *Store) UpdatePluginInstallationConfig(ctx context.Context, pluginID int64, config json.RawMessage, expectedUpdatedAt time.Time) error {
	res, err := s.db.ExecContext(ctx, `update plugin_installations set config_json=?,updated_at=? where plugin_id=? and installed=1 and updated_at=?`, string(config), now(), pluginID, expectedUpdatedAt.UTC().Format(time.RFC3339Nano))
	return pluginExtensionChanged(res, err)
}
func pluginExtensionChanged(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPluginPackageConflict
	}
	return nil
}

// SetPluginInstallationSecret accepts ciphertext only. It deliberately has no
// read counterpart on the management surface.
func (s *Store) GetPluginInstallationSecretEncrypted(ctx context.Context, pluginID int64, name string) (string, error) {
	var encrypted string
	err := s.db.QueryRowContext(ctx, `select s.value_encrypted from plugin_installation_secrets s join plugin_installations i on i.plugin_id=s.plugin_id where s.plugin_id=? and s.name=? and i.installed=1`, pluginID, name).Scan(&encrypted)
	return encrypted, err
}

func (s *Store) SetPluginInstallationSecret(ctx context.Context, pluginID int64, name, encrypted string, expectedUpdatedAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `update plugin_installations set updated_at=? where plugin_id=? and installed=1 and updated_at=?`, now(), pluginID, expectedUpdatedAt.UTC().Format(time.RFC3339Nano))
	if err = pluginExtensionChanged(res, err); err != nil {
		return err
	}
	if encrypted == "" {
		_, err = tx.ExecContext(ctx, `delete from plugin_installation_secrets where plugin_id=? and name=?`, pluginID, name)
	} else {
		_, err = tx.ExecContext(ctx, `insert into plugin_installation_secrets(plugin_id,name,value_encrypted,updated_at) values(?,?,?,?) on conflict(plugin_id,name) do update set value_encrypted=excluded.value_encrypted,updated_at=excluded.updated_at`, pluginID, name, encrypted, now())
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// UninstallPluginPackage retains identity, revisions, grants and run audit rows
// because historical runs reference them. Revoked grants cannot authorize reuse.
func (s *Store) UninstallPluginPackage(ctx context.Context, pluginID int64, keepState bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	res, err := tx.ExecContext(ctx, `update plugin_installations set installed=0,active_revision_id=null,updated_at=? where plugin_id=?`, ts, pluginID)
	if err = pluginExtensionChanged(res, err); err != nil {
		return err
	}
	for _, stmt := range []string{
		`update plugin_trigger_bindings set enabled=0,binding_revision=binding_revision+1,updated_at=? where plugin_id=?`,
		`update plugin_trigger_states set armed=0,next_due_at=null,updated_at=? where binding_id in (select id from plugin_trigger_bindings where plugin_id=?)`,
		`update plugins set status='archived',updated_at=? where id=?`,
		`update plugin_runs set status='cancelled',error_code='cancelled',lease_owner='',lease_until=null,lease_generation=lease_generation+1,finished_at=? where plugin_id=? and status in ('queued','running')`,
		`update plugin_grants set revoked_at=?,grant_revision=grant_revision+1 where plugin_id=? and revoked_at is null`,
	} {
		if _, err = tx.ExecContext(ctx, stmt, ts, pluginID); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `delete from plugin_installation_secrets where plugin_id=?`, pluginID); err != nil {
		return err
	}
	if !keepState {
		if _, err = tx.ExecContext(ctx, `delete from plugin_state where plugin_id=?`, pluginID); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `update plugin_installations set config_json='{}' where plugin_id=?`, pluginID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
