package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/OboardProject/oboard/internal/store"
)

type RestoreCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type RestoreVerification struct {
	Verified bool           `json:"verified"`
	Checks   []RestoreCheck `json:"checks"`
}

type VerifyRestoreOptions struct {
	ArchivePath   string
	Password      string
	TargetVersion string
	TempParent    string
}

// VerifyRestore only prepares an isolated copy. It never commits a restore or starts Controller workers.
func VerifyRestore(ctx context.Context, options VerifyRestoreOptions) (report RestoreVerification, err error) {
	for _, name := range []string{"archive_format", "decryption_and_files", "version_compatibility", "database_source", "database_migration", "database_integrity", "secret_reencryption", "final_integrity", "cleanup"} {
		report.Checks = append(report.Checks, RestoreCheck{Name: name, Status: "not_run"})
	}
	report.Checks = append(report.Checks, RestoreCheck{Name: "offline_configuration_rebuild", Status: "unsupported"}, RestoreCheck{Name: "external_services_and_node_takeover", Status: "unsupported"})
	record := func(name string, checkErr error) {
		for i := range report.Checks {
			if report.Checks[i].Name == name {
				report.Checks[i].Status = "pass"
				if checkErr != nil {
					report.Checks[i].Status = "fail"
				}
				return
			}
		}
	}
	if !filepath.IsAbs(options.TempParent) || strings.TrimSpace(options.TargetVersion) == "" || strings.TrimSpace(options.Password) == "" {
		return report, errors.New("verification requires an absolute temporary parent, target version and password")
	}
	info, statErr := os.Lstat(options.TempParent)
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return report, errors.New("verification requires an existing private temporary parent directory")
	}
	stage, mkErr := os.MkdirTemp(options.TempParent, "oboard-backup-verify-")
	if mkErr != nil {
		return report, errors.New("cannot create isolated verification directory")
	}
	defer func() {
		cleanupErr := os.RemoveAll(stage)
		record("cleanup", cleanupErr)
		if cleanupErr != nil {
			report.Verified = false
			err = errors.New("verification temporary directory cleanup failed")
		}
	}()
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return report, errors.New("cannot generate isolated verification key")
	}
	_, restored, prepareErr := prepareRestore(ctx, options.ArchivePath, options.Password, options.TargetVersion, hex.EncodeToString(secret), stage, record)
	if prepareErr != nil {
		return report, errors.New("backup verification failed; see check statuses")
	}
	if closeErr := restored.Close(); closeErr != nil {
		return report, errors.New("cannot close verification database")
	}
	report.Verified = true
	return report, nil
}

func validateRestoreDatabase(ctx context.Context, path string) error {
	invalid := errors.New("backup does not contain an existing Controller database")
	file, err := os.Open(path)
	if err != nil {
		return invalid
	}
	var header [16]byte
	_, readErr := io.ReadFull(file, header[:])
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || string(header[:]) != "SQLite format 3\x00" {
		return invalid
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return invalid
	}
	defer db.Close()
	// These settings columns predate the backup format; do not require the current schema.
	var count int
	if err := db.QueryRowContext(ctx, "select count(*) from sqlite_schema where type = 'table' and name = 'app_settings'").Scan(&count); err != nil || count != 1 {
		return invalid
	}
	rows, err := db.QueryContext(ctx, "select key, value from app_settings limit 0")
	if err != nil {
		return invalid
	}
	if err := rows.Close(); err != nil {
		return invalid
	}
	return nil
}

func prepareRestore(ctx context.Context, archivePath, password, targetVersion, targetSecret, stage string, record func(string, error)) (Manifest, *store.Store, error) {
	check := func(name string, err error) error {
		if record != nil {
			record(name, err)
		}
		return err
	}
	manifest, sourceSecret, err := extractArchiveWithReport(archivePath, password, stage, record)
	if err != nil {
		return Manifest{}, nil, err
	}
	if err = check("version_compatibility", CheckCompatibility(manifest.SourceVersion, targetVersion)); err != nil {
		return Manifest{}, nil, err
	}
	database := filepath.Join(stage, "database.sqlite")
	if err = check("database_source", validateRestoreDatabase(ctx, database)); err != nil {
		return Manifest{}, nil, err
	}
	restored, err := store.OpenForRestore(database)
	if check("database_migration", err) != nil {
		return Manifest{}, nil, err
	}
	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"database_integrity", func() error { return restored.CheckIntegrity(ctx) }},
		{"secret_reencryption", func() error { return restored.RewrapEncryptedSecrets(ctx, sourceSecret, targetSecret) }},
		{"final_integrity", func() error { return restored.CheckIntegrity(ctx) }},
	} {
		if err = check(step.name, step.run()); err != nil {
			_ = restored.Close()
			return Manifest{}, nil, err
		}
	}
	return manifest, restored, nil
}
