package controller

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/backup"
	"github.com/OboardProject/oboard/internal/store"
)

func TestControllerBackupSettingsAndRetention(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OBOARD_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("OBOARD_ACME_HOME", filepath.Join(root, "acme"))
	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-session-secret-with-at-least-thirty-two-characters", "")
	app.ConfigureControllerBackups(filepath.Join(root, "oboard.sqlite"))
	h := app.Handler()
	request(t, h, http.MethodPost, "/api/v2/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v2/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)
	settings := request(t, h, http.MethodPut, "/api/v2/ui/backups/settings", token, map[string]any{
		"enabled":           true,
		"schedule":          "daily",
		"time":              "03:00",
		"weekday":           0,
		"local_retention":   1,
		"remote_retention":  3,
		"destination":       map[string]any{"enabled": false},
		"recovery_password": "backup-recovery-password",
	}, http.StatusOK)
	if settings["settings"].(map[string]any)["password_configured"] != true {
		t.Fatalf("backup settings = %#v", settings)
	}
	publicSettings := settings["settings"].(map[string]any)
	if _, exposed := publicSettings["recovery_password"]; exposed {
		t.Fatalf("backup response exposed recovery password: %#v", publicSettings)
	}
	storedSettings, err := db.ListSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if storedSettings[controllerBackupSecretsSetting] == "" || storedSettings[controllerBackupSecretsSetting] == "backup-recovery-password" {
		t.Fatalf("backup recovery password was not encrypted: %#v", storedSettings[controllerBackupSecretsSetting])
	}
	first := request(t, h, http.MethodPost, "/api/v2/ui/backups", token, map[string]any{"upload_remote": false}, http.StatusCreated)
	if first["backup"].(map[string]any)["local_status"] != "available" {
		t.Fatalf("first backup = %#v", first)
	}
	second := request(t, h, http.MethodPost, "/api/v2/ui/backups", token, map[string]any{"upload_remote": false}, http.StatusCreated)
	secondID := second["backup"].(map[string]any)["id"].(string)
	listed := request(t, h, http.MethodGet, "/api/v2/ui/backups", token, nil, http.StatusOK)
	backups := listed["backups"].([]any)
	if len(backups) != 2 {
		t.Fatalf("backup records = %#v", backups)
	}
	var available int
	for _, raw := range backups {
		item := raw.(map[string]any)
		if item["local_status"] == "available" {
			available++
			if item["id"] != secondID {
				t.Fatalf("retained unexpected backup = %#v", item)
			}
		}
	}
	if available != 1 {
		t.Fatalf("available local backups = %d", available)
	}
	items, err := db.ListControllerBackups(t.Context())
	if err != nil || len(items) != 2 {
		t.Fatalf("stored backups = %#v, err=%v", items, err)
	}
	for _, item := range items {
		if item.LocalStatus == "expired" && item.LocalPath != "" {
			t.Fatalf("expired backup path retained: %#v", item)
		}
		if item.LocalStatus == "available" {
			if _, err := os.Stat(item.LocalPath); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestScheduledBackupPeriodRunsAfterMissedTime(t *testing.T) {
	settings := controllerBackupSettingsState{Schedule: "daily", Time: "03:00", Timezone: "Asia/Shanghai"}
	before := time.Date(2026, time.July, 25, 2, 59, 0, 0, time.FixedZone("CST", 8*60*60))
	if period, due := scheduledBackupPeriod(settings, before); due || period != "" {
		t.Fatalf("daily backup before schedule = %q, %v", period, due)
	}
	after := time.Date(2026, time.July, 25, 8, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	if period, due := scheduledBackupPeriod(settings, after); !due || period != "daily:2026-07-25" {
		t.Fatalf("daily backup after schedule = %q, %v", period, due)
	}
	settings.Schedule, settings.Weekday = "weekly", int(time.Monday)
	tuesday := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	if period, due := scheduledBackupPeriod(settings, tuesday); !due || period != "weekly:2026-31" {
		t.Fatalf("weekly backup after missed schedule = %q, %v", period, due)
	}
}

func TestControllerBackupFetchesExpiredLocalCopyFromWebDAV(t *testing.T) {
	objects := map[string][]byte{}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "user" || password != "password" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case http.MethodPut:
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
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
	defer remote.Close()

	root := t.TempDir()
	t.Setenv("OBOARD_BACKUP_DIR", filepath.Join(root, "backups"))
	t.Setenv("OBOARD_ACME_HOME", filepath.Join(root, "acme"))
	dbPath := filepath.Join(root, "oboard.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-session-secret-with-at-least-thirty-two-characters", "")
	app.ConfigureControllerBackups(dbPath)
	settings := controllerBackupSettingsState{
		LocalRetention:  1,
		RemoteRetention: 3,
		Destination:     backup.Destination{Provider: "webdav", Endpoint: remote.URL + "/vault", Prefix: "oboard", Enabled: true},
		Secrets: controllerBackupSecrets{
			RecoveryPassword: "backup-recovery-password",
			Remote:           backup.RemoteSecrets{Username: "user", Password: "password"},
		},
	}
	item, err := app.createControllerDataBackup(context.Background(), settings, "automatic", true, false)
	if err != nil {
		t.Fatal(err)
	}
	if item.RemoteStatus != "available" || !item.RemoteReady {
		t.Fatalf("remote backup = %#v", item)
	}
	if err := os.Remove(item.LocalPath); err != nil {
		t.Fatal(err)
	}
	if err := db.ExpireControllerBackupLocal(context.Background(), item.ID); err != nil {
		t.Fatal(err)
	}
	item.LocalPath, item.LocalStatus = "", "expired"
	if err := app.ensureControllerBackupLocal(context.Background(), settings, item); err != nil {
		t.Fatal(err)
	}
	if item.LocalStatus != "available" {
		t.Fatalf("fetched backup = %#v", item)
	}
	if _, err := os.Stat(item.LocalPath); err != nil {
		t.Fatal(err)
	}
}
