package backup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func TestEncryptedBackupRestoresDataAndRewrapsSecrets(t *testing.T) {
	root := t.TempDir()
	sourceDBPath := filepath.Join(root, "source.sqlite")
	source, err := store.Open(sourceDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx := context.Background()
	if err := source.SetSetting(ctx, "backup-value", "present"); err != nil {
		t.Fatal(err)
	}
	sourceSecret := "source-secret-with-at-least-thirty-two-characters"
	targetSecret := "target-secret-with-at-least-thirty-two-characters"
	wrapped, err := security.EncryptSecret(sourceSecret, "dns-credential", `{"token":"secret"}`)
	if err != nil {
		t.Fatal(err)
	}
	credential := &model.DNSCredential{Name: "backup", Provider: "cloudflare", ConfigEncrypted: wrapped, Enabled: true}
	if err := source.CreateDNSCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	backupSettingsPlain := `{"recovery_password":"backup-password","remote":{"access_key":"access","secret_key":"secret"}}`
	backupSettingsWrapped, err := security.EncryptSecret(sourceSecret, "controller_backup_secret_config", backupSettingsPlain)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SetSetting(ctx, "controller_backup_secret_config", backupSettingsWrapped); err != nil {
		t.Fatal(err)
	}
	acmeHome := filepath.Join(root, "acme")
	if err := os.MkdirAll(acmeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acmeHome, "account.conf"), []byte("account"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupRoot := filepath.Join(root, "backups")
	manager, err := New(Config{Root: backupRoot, DatabasePath: sourceDBPath, ACMEHome: acmeHome, MasterSecret: sourceSecret, SourceVersion: "1.2.3", Snapshot: source.Backup})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(ctx, "recovery-password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Validate(created.Path, "wrong-password"); err == nil {
		t.Fatal("backup accepted an incorrect password")
	}
	inspection, err := manager.Inspect(created.Path)
	if err != nil || inspection.Manifest.ID != created.Manifest.ID {
		t.Fatalf("inspection = %#v, err=%v", inspection, err)
	}
	manager.config.MasterSecret = targetSecret
	staged, err := manager.StageRestore(ctx, created.Path, "recovery-password", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.Open(filepath.Join(staged.StageRoot, "database.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	settings, err := restored.ListSettings(ctx)
	if err != nil || settings["backup-value"] != "present" {
		t.Fatalf("restored settings = %#v, err=%v", settings, err)
	}
	restoredCredential, err := restored.GetDNSCredential(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := security.DecryptSecret(targetSecret, "dns-credential", restoredCredential.ConfigEncrypted)
	if err != nil || plain != `{"token":"secret"}` {
		t.Fatalf("rewrapped credential = %q, err=%v", plain, err)
	}
	restoredSettings, err := restored.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restoredBackupSettings, err := security.DecryptSecret(targetSecret, "controller_backup_secret_config", restoredSettings["controller_backup_secret_config"])
	if err != nil || restoredBackupSettings != backupSettingsPlain {
		t.Fatalf("rewrapped backup settings = %q, err=%v", restoredBackupSettings, err)
	}
	if _, err := os.Stat(filepath.Join(staged.StageRoot, "acme", "account.conf")); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingRestore(Config{Root: backupRoot, DatabasePath: filepath.Join(root, "target.sqlite"), ACMEHome: filepath.Join(root, "target-acme")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "target-acme", "account.conf")); err != nil {
		t.Fatal(err)
	}
}

func TestWebDAVUploadAndDelete(t *testing.T) {
	var put, downloaded, deleted bool
	var stored []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "user" || password != "password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			put = true
			stored, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		case http.MethodGet:
			downloaded = true
			_, _ = w.Write(stored)
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "backup.obk")
	if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := Destination{Provider: "webdav", Endpoint: server.URL + "/backup", Prefix: "oboard", Enabled: true}
	secrets := RemoteSecrets{Username: "user", Password: "password"}
	if err := TestDestination(context.Background(), nil, destination, secrets); err != nil {
		t.Fatal(err)
	}
	if err := Upload(context.Background(), nil, destination, secrets, "oboard/backup.obk", path); err != nil {
		t.Fatal(err)
	}
	downloadedPath := filepath.Join(t.TempDir(), "downloaded.obk")
	if _, err := Download(context.Background(), nil, destination, secrets, "oboard/backup.obk", downloadedPath); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(downloadedPath)
	if err != nil || string(contents) != "backup" {
		t.Fatalf("downloaded backup = %q, err=%v", contents, err)
	}
	if err := Delete(context.Background(), nil, destination, secrets, "oboard/backup.obk"); err != nil {
		t.Fatal(err)
	}
	if !put || !downloaded || !deleted {
		t.Fatalf("put=%v downloaded=%v deleted=%v", put, downloaded, deleted)
	}
}

func TestBackupVersionCompatibility(t *testing.T) {
	tests := []struct {
		source string
		target string
		ok     bool
	}{
		{source: "1.2.3", target: "1.2.3", ok: true},
		{source: "1.2.3", target: "1.3.0", ok: true},
		{source: "1.2.3", target: "2.0.0", ok: true},
		{source: "2.0.0", target: "1.9.9", ok: false},
		{source: "1.2.3-rc.1", target: "1.2.3", ok: true},
		{source: "1.2.3", target: "1.2.3-rc.1", ok: false},
		{source: "1.2.3-rc.2", target: "1.2.3-rc.10", ok: true},
		{source: "dev", target: "dev", ok: true},
		{source: "dev", target: "1.2.3", ok: false},
		{source: "9.9.9", target: "dev", ok: true},
	}
	for _, test := range tests {
		if got := CheckCompatibility(test.source, test.target) == nil; got != test.ok {
			t.Errorf("CheckCompatibility(%q, %q) = %v, want %v", test.source, test.target, got, test.ok)
		}
	}
}

func TestS3CompatibleUploadDownloadAndDelete(t *testing.T) {
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=access/") || r.Header.Get("x-amz-content-sha256") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPut:
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			value, exists := objects[r.URL.Path]
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(value)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	destination := Destination{Provider: "s3", Endpoint: server.URL, Bucket: "backups", Prefix: "oboard", Region: "us-east-1", ForcePathStyle: true, Enabled: true}
	secrets := RemoteSecrets{AccessKey: "access", SecretKey: "secret"}
	if err := TestDestination(context.Background(), nil, destination, secrets); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.obk")
	if err := os.WriteFile(source, []byte("s3-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := ObjectKey(destination, "source.obk")
	if err := Upload(context.Background(), nil, destination, secrets, key, source); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.obk")
	if _, err := Download(context.Background(), nil, destination, secrets, key, target); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(target)
	if err != nil || string(value) != "s3-backup" {
		t.Fatalf("downloaded S3 backup = %q, err=%v", value, err)
	}
	if err := Delete(context.Background(), nil, destination, secrets, key); err != nil {
		t.Fatal(err)
	}
}

func TestPendingRestoreRollsBackDatabaseWhenACMESwitchFails(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	sourcePath := filepath.Join(root, "source.sqlite")
	source, err := store.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.SetSetting(ctx, "restore-value", "source"); err != nil {
		t.Fatal(err)
	}
	backupRoot := filepath.Join(root, "backups")
	sourceACME := filepath.Join(root, "source-acme")
	if err := os.MkdirAll(sourceACME, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceACME, "account.conf"), []byte("account"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(Config{Root: backupRoot, DatabasePath: sourcePath, ACMEHome: sourceACME, MasterSecret: "source-secret-with-at-least-thirty-two-characters", SourceVersion: "1.2.3", Snapshot: source.Backup})
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(ctx, "recovery-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(root, "target.sqlite")
	target, err := store.Open(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.SetSetting(ctx, "restore-value", "target"); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	manager.config.DatabasePath = targetPath
	manager.config.MasterSecret = "target-secret-with-at-least-thirty-two-characters"
	if _, err := manager.StageRestore(ctx, created.Path, "recovery-password", "1.2.3"); err != nil {
		t.Fatal(err)
	}
	blockedParent := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedParent, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPendingRestore(Config{Root: backupRoot, DatabasePath: targetPath, ACMEHome: filepath.Join(blockedParent, "acme")}); err == nil {
		t.Fatal("restore unexpectedly succeeded")
	}
	reopened, err := store.Open(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	settings, err := reopened.ListSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["restore-value"] != "target" {
		t.Fatalf("database was not rolled back: %#v", settings)
	}
}
