package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/metacubex/age"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/store"
)

func TestRestoreDatabaseSourceAcceptsFoundationalSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "historical.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// app_settings existed with these columns when encrypted backups were introduced.
	_, err = db.Exec("create table app_settings (key text primary key, value text not null, updated_at text not null)")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateRestoreDatabase(context.Background(), path); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRestoreRejectsNonControllerDatabase(t *testing.T) {
	for _, kind := range []string{"zero_bytes", "empty_sqlite", "unrelated_schema"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			manager, err := New(Config{Root: filepath.Join(root, "backups"), DatabasePath: filepath.Join(root, "unused.sqlite"), MasterSecret: "synthetic-secret", SourceVersion: "1.2.3", Snapshot: func(ctx context.Context, dest string) error {
				if kind == "zero_bytes" {
					return os.WriteFile(dest, nil, 0600)
				}
				db, err := sql.Open("sqlite", dest)
				if err != nil {
					return err
				}
				defer db.Close()
				statement := "vacuum"
				if kind == "unrelated_schema" {
					statement = "create table unrelated (secret text)"
				}
				_, err = db.ExecContext(ctx, statement)
				return err
			}})
			if err != nil {
				t.Fatal(err)
			}
			archive, err := manager.Create(ctx, "synthetic-password")
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(root, "private")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			report, err := VerifyRestore(ctx, VerifyRestoreOptions{ArchivePath: archive.Path, Password: "synthetic-password", TargetVersion: "1.2.3", TempParent: parent})
			if err == nil || report.Verified {
				t.Fatal("accepted non-Controller database")
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded)+err.Error(), root) || strings.Contains(string(encoded)+err.Error(), "synthetic-secret") {
				t.Fatal("sensitive diagnostic")
			}
			for _, check := range report.Checks {
				if check.Name == "database_source" && check.Status != "fail" {
					t.Fatalf("source check: %+v", check)
				}
				if check.Name == "database_migration" && check.Status != "not_run" {
					t.Fatal("migration ran on invalid source")
				}
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("verification residue")
			}
			if _, err := manager.StageRestore(ctx, archive.Path, "synthetic-password", "1.2.3"); err == nil {
				t.Fatal("staged non-Controller database")
			}
			if _, err := os.Stat(filepath.Join(manager.Root(), pendingFileName)); !os.IsNotExist(err) {
				t.Fatal("production marker created")
			}
			entries, err = os.ReadDir(filepath.Join(manager.Root(), ".restore"))
			if err != nil || len(entries) != 0 {
				t.Fatal("restore residue")
			}
		})
	}
}

func TestVerifyRestoreRejectsUnsafePayloads(t *testing.T) {
	for _, header := range []tar.Header{
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "/escape", Typeflag: tar.TypeReg},
		{Name: "acme/link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"},
		{Name: "acme/link", Typeflag: tar.TypeLink, Linkname: "../../escape"},
	} {
		t.Run(header.Name+string(header.Typeflag), func(t *testing.T) {
			root := t.TempDir()
			payload := filepath.Join(root, payloadFileName)
			f, err := os.Create(payload)
			if err != nil {
				t.Fatal(err)
			}
			recipient, err := age.NewScryptRecipient("synthetic-password")
			if err != nil {
				t.Fatal(err)
			}
			encrypted, err := age.Encrypt(f, recipient)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(encrypted)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&header); err != nil {
				t.Fatal(err)
			}
			for _, close := range []func() error{tw.Close, gz.Close, encrypted.Close, f.Close} {
				if err := close(); err != nil {
					t.Fatal(err)
				}
			}
			hash, size, err := fileSHA256(payload)
			if err != nil {
				t.Fatal(err)
			}
			id, err := NewID()
			if err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(root, "unsafe.obk")
			if err := writeEnvelope(archive, Manifest{ID: id, FormatVersion: FormatVersion, SourceVersion: "1.2.3", PayloadSHA256: hash, PayloadSize: size}, payload); err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(root, "private")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			report, err := VerifyRestore(context.Background(), VerifyRestoreOptions{ArchivePath: archive, Password: "synthetic-password", TargetVersion: "1.2.3", TempParent: parent})
			if err == nil || report.Verified {
				t.Fatal("accepted unsafe payload")
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed cleanup")
			}
			if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
				t.Fatal("escaped extraction")
			}
		})
	}
}

func TestVerifyRestoreDatabaseFailuresAreRedacted(t *testing.T) {
	for _, brokenDB := range []bool{true, false} {
		t.Run(map[bool]string{true: "database", false: "secrets"}[brokenDB], func(t *testing.T) {
			root := t.TempDir()
			ctx := context.Background()
			dbPath := filepath.Join(root, "source.sqlite")
			db, err := store.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.CreateDNSCredential(ctx, &model.DNSCredential{Name: "broken", Provider: "cloudflare", ConfigEncrypted: "SECRET-corrupt-ciphertext", Enabled: true}); err != nil {
				t.Fatal(err)
			}
			manager, err := New(Config{Root: filepath.Join(root, "backups"), DatabasePath: dbPath, MasterSecret: "synthetic-secret", SourceVersion: "1.2.3", Snapshot: func(ctx context.Context, dest string) error {
				if brokenDB {
					return os.WriteFile(dest, []byte("SECRET-invalid-database"), 0600)
				}
				return db.Backup(ctx, dest, store.BackupOptions{})
			}})
			if err != nil {
				t.Fatal(err)
			}
			archive, err := manager.Create(ctx, "synthetic-password")
			if err != nil {
				t.Fatal(err)
			}
			parent := filepath.Join(root, "private")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			report, err := VerifyRestore(ctx, VerifyRestoreOptions{ArchivePath: archive.Path, Password: "synthetic-password", TargetVersion: "1.2.3", TempParent: parent})
			if err == nil || report.Verified {
				t.Fatal("accepted broken database or key")
			}
			encoded, _ := json.Marshal(report)
			if strings.Contains(string(encoded)+err.Error(), "SECRET") || strings.Contains(err.Error(), root) {
				t.Fatal("sensitive diagnostic")
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed cleanup")
			}
		})
	}
}

func TestVerifyRestoreIsolated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "source.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, "sentinel", "unchanged"); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Config{Root: filepath.Join(root, "backups"), DatabasePath: dbPath, MasterSecret: "synthetic-source-secret", SourceVersion: "1.2.3", Snapshot: func(ctx context.Context, dest string) error { return db.Backup(ctx, dest, store.BackupOptions{}) }})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := manager.Create(ctx, "synthetic-password")
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "verification")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(root, "corrupt.obk")
	if err := os.WriteFile(corrupt, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, archive, password, version string
		ok                               bool
	}{
		{"valid", archive.Path, "synthetic-password", "1.2.3", true},
		{"password", archive.Path, "wrong-password", "1.2.3", false},
		{"version", archive.Path, "synthetic-password", "0.1.0", false},
		{"corrupt", corrupt, "synthetic-password", "1.2.3", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := VerifyRestore(ctx, VerifyRestoreOptions{ArchivePath: tc.archive, Password: tc.password, TargetVersion: tc.version, TempParent: work})
			if (err == nil) != tc.ok || report.Verified != tc.ok {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			statuses := make(map[string]string)
			for _, check := range report.Checks {
				statuses[check.Name] = check.Status
			}
			if tc.name == "corrupt" && (statuses["archive_format"] != "fail" || statuses["decryption_and_files"] != "not_run") {
				t.Fatalf("invalid envelope check statuses: %v", statuses)
			}
			if tc.name == "password" && (statuses["archive_format"] != "pass" || statuses["decryption_and_files"] != "fail") {
				t.Fatalf("invalid decryption check statuses: %v", statuses)
			}
			entries, err := os.ReadDir(work)
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary files remain: %v %v", entries, err)
			}
			if _, err := os.Stat(filepath.Join(manager.Root(), pendingFileName)); !os.IsNotExist(err) {
				t.Fatalf("production marker: %v", err)
			}
		})
	}
	t.Run("single_envelope_read", func(t *testing.T) {
		stage, err := os.MkdirTemp(work, "single-read-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(stage)
		var checks []string
		_, restored, err := prepareRestore(ctx, archive.Path, "synthetic-password", "1.2.3", "synthetic-target-secret", stage, func(name string, checkErr error) {
			checks = append(checks, name)
			if checkErr != nil {
				t.Errorf("check %s: %v", name, checkErr)
			}
			if name == "archive_format" {
				// Once the envelope is validated and staged, preparation must not reopen it.
				if err := os.Remove(archive.Path); err != nil {
					t.Fatal(err)
				}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := restored.Close(); err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(checks, ","); got != "archive_format,decryption_and_files,version_compatibility,database_source,database_migration,database_integrity,secret_reencryption,plugin_webhook_reencryption,final_integrity" {
			t.Fatalf("unexpected preparation checks: %s", got)
		}
	})
	value, err := db.GetSetting(ctx, "sentinel")
	if err != nil || value != "unchanged" {
		t.Fatalf("source changed: %q %v", value, err)
	}
	value, err = db.GetSetting(ctx, "controller_backup_restore_reconcile")
	if err != nil || value != "" {
		t.Fatalf("source reconciliation changed: %q %v", value, err)
	}
}
