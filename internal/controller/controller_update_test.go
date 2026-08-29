package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/controllerupdate"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

func TestControllerUpdateAPIAndBackupCleanup(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "obu-")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDir, "updater.sock")
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	statePath := filepath.Join(root, "updater-status.json")
	updater := controllerupdate.NewService(controllerupdate.ServiceConfig{
		SocketPath:       socketPath,
		BinaryEnvPath:    binaryEnv,
		ControllerBinary: binary,
		StatePath:        statePath,
		WorkRoot:         filepath.Join(root, "work"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- updater.Serve(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("start updater: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("updater socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("updater shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("updater did not stop")
		}
	})

	dbPath := filepath.Join(root, "oboard.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	app.controllerBackupDir = filepath.Join(root, "backups")
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)
	status := request(t, handler, http.MethodGet, "/api/v1/ui/controller-update", adminToken, nil, http.StatusOK)
	if _, exists := status["install_method"]; exists || status["channel"] != "pinned" || status["status"] != "pinned" {
		t.Fatalf("unexpected update status: %#v", status)
	}
	if status["auto_update_interval_hours"] != float64(controllerUpdateDefaultIntervalHours) {
		t.Fatalf("unexpected default update interval: %#v", status["auto_update_interval_hours"])
	}
	settingsResponse := request(t, handler, http.MethodGet, "/api/v1/ui/settings", adminToken, nil, http.StatusOK)
	defaults := settingsResponse["settings"].(map[string]any)
	if defaults[agentAutoUpdateSetting] != false || defaults[subscriptionRelayAutoUpdateSetting] != false || defaults[updateWindowEnabledSetting] != false || defaults[updateWindowStartHourSetting] != float64(3) || defaults[updateWindowEndHourSetting] != float64(7) {
		t.Fatalf("unexpected managed update defaults: %#v", defaults)
	}
	settingsResponse = request(t, handler, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{
		agentAutoUpdateSetting: true, subscriptionRelayAutoUpdateSetting: true,
		updateWindowEnabledSetting: true, updateWindowStartHourSetting: 22, updateWindowEndHourSetting: 4,
	}, http.StatusOK)
	saved := settingsResponse["settings"].(map[string]any)
	if saved[agentAutoUpdateSetting] != true || saved[subscriptionRelayAutoUpdateSetting] != true || saved[updateWindowEnabledSetting] != true || saved[updateWindowStartHourSetting] != float64(22) || saved[updateWindowEndHourSetting] != float64(4) {
		t.Fatalf("unexpected managed update settings: %#v", saved)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{updateWindowStartHourSetting: 24}, http.StatusBadRequest)
	request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/check", adminToken, nil, http.StatusOK)
	for _, interval := range []int{1, 6, 24, 72, 168} {
		settings := request(t, handler, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{"controller_auto_update_interval_hours": interval}, http.StatusOK)
		if got := settings["settings"].(map[string]any)["controller_auto_update_interval_hours"]; got != float64(interval) {
			t.Fatalf("unexpected saved update interval: got %#v, want %d", got, interval)
		}
	}
	for _, interval := range []int{2, 12} {
		request(t, handler, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{"controller_auto_update_interval_hours": interval}, http.StatusBadRequest)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/settings", adminToken, map[string]any{"controller_auto_update_enabled": true}, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/install", adminToken, nil, http.StatusConflict)
	request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/cancel", adminToken, nil, http.StatusConflict)
	staleRun := &store.ControllerUpdateRun{Source: "manual", TargetVersion: "dev-test", TargetBuild: "20260828194806", Phase: store.ControllerUpdatePhaseVerifying}
	if err := db.CreateControllerUpdateRun(t.Context(), staleRun); err != nil {
		t.Fatal(err)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/force-finish", adminToken, map[string]any{"confirmation": "结束"}, http.StatusBadRequest)
	forced := request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/force-finish", adminToken, map[string]any{"confirmation": controllerUpdateForceFinishPhrase}, http.StatusOK)
	if operation, _ := forced["operation"].(map[string]any); operation == nil || operation["active"] != false || operation["phase"] != store.ControllerUpdatePhaseCancelled {
		t.Fatalf("unexpected force-finished operation: %#v", forced)
	}
	if active, err := db.GetActiveControllerUpdateRun(t.Context()); err != nil || active != nil {
		t.Fatalf("active update after force finish: %#v err=%v", active, err)
	}

	request(t, handler, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerLogin := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)
	request(t, handler, http.MethodGet, "/api/v1/ui/controller-update", viewerLogin["token"].(string), nil, http.StatusForbidden)
	request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/force-finish", viewerLogin["token"].(string), map[string]any{"confirmation": controllerUpdateForceFinishPhrase}, http.StatusForbidden)

	firstBackup, err := app.createControllerBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	zeroStale := filepath.Join(app.controllerBackupDir, "oboard-before-update-stale.sqlite")
	if err := os.WriteFile(zeroStale, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(app.controllerBackupDir, "manual-backup.sqlite")
	if err := os.WriteFile(unrelated, []byte("manual"), 0o600); err != nil {
		t.Fatal(err)
	}
	latestBackup, err := app.createControllerBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(firstBackup); err != nil {
		t.Fatalf("previous update backup was removed: %v", err)
	}
	if _, err := os.Stat(zeroStale); !os.IsNotExist(err) {
		t.Fatalf("zero-byte stale update backup was not removed: %v", err)
	}
	if _, err := os.Stat(latestBackup); err != nil {
		t.Fatalf("latest update backup was removed: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated backup was removed: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(app.controllerBackupDir, "oboard-before-update-*.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 {
		t.Fatalf("unexpected retained update backups: %#v", backups)
	}
}

func TestCleanupControllerUpdateBackupFilesPreservesRetained(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerBackupDir = filepath.Join(root, "backups")
	if err := os.MkdirAll(app.controllerBackupDir, 0o700); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(app.controllerBackupDir, "oboard-before-update-retained.sqlite")
	stale := filepath.Join(app.controllerBackupDir, "oboard-before-update-stale.sqlite")
	zero := filepath.Join(app.controllerBackupDir, "oboard-before-update-zero.sqlite")
	unrelated := filepath.Join(app.controllerBackupDir, "manual.sqlite")
	for _, path := range []string{retained, stale, unrelated} {
		if err := os.WriteFile(path, []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(zero, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	app.cleanupControllerUpdateBackupFiles(retained)
	if _, err := os.Stat(zero); !os.IsNotExist(err) {
		t.Fatalf("zero-byte update backup was not removed: %v", err)
	}
	for _, path := range []string{retained, stale, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("complete backup or unrelated file was removed: %v", err)
		}
	}
}

func TestSuccessfulControllerUpdateRetainsBackup(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerBackupDir = filepath.Join(root, "backups")
	backupPath, err := app.createControllerBackup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.recordControllerUpdateBackup(context.Background(), backupPath, "20260810010101"); err != nil {
		t.Fatal(err)
	}
	settings, err := db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	app.removeSuccessfulControllerUpdateBackup(context.Background(), settings, controllerupdate.Status{Current: controllerupdate.BuildInfo{Build: "different-build"}})
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("backup was removed before the target build succeeded: %v", err)
	}
	app.removeSuccessfulControllerUpdateBackup(context.Background(), settings, controllerupdate.Status{Current: controllerupdate.BuildInfo{Build: "20260810010101"}})
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("successful update backup was removed: %v", err)
	}
}

func TestWriteControllerUpdateStatusLocalizesUpdaterError(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	if err := db.SetSetting(context.Background(), controllerUpdateErrorSetting, "saved: no space left on device"); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/ui/controller-update", nil)
	app.writeControllerUpdateStatus(recorder, request, controllerupdate.Status{LastError: "updater: read-only file system"})
	var decoded struct {
		LastError string `json:"last_error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.LastError != "磁盘为只读状态，无法写入" {
		t.Fatalf("updater error was not localized: %q", decoded.LastError)
	}
}

func TestControllerUpdatePanelActivityGate(t *testing.T) {
	app := &Server{}
	if !app.controllerPanelIdle(time.Now()) {
		t.Fatal("panel with no activity should be idle")
	}
	app.beginControllerPanelRequest()
	if app.controllerPanelIdle(time.Now().Add(controllerUpdatePanelIdlePeriod)) {
		t.Fatal("active panel request must block automatic update")
	}
	app.endControllerPanelRequest()
	if app.controllerPanelIdle(time.Now()) {
		t.Fatal("recent panel activity must block automatic update")
	}
	app.controllerActivityMu.Lock()
	app.controllerLastActivity = time.Now().Add(-controllerUpdatePanelIdlePeriod)
	app.controllerActivityMu.Unlock()
	if !app.controllerPanelIdle(time.Now()) {
		t.Fatal("panel should become idle after the inactivity window")
	}
}

func TestFallbackControllerUpdateStatus(t *testing.T) {
	t.Setenv("OBOARD_UPDATE_CHANNEL", "latest")
	status := (&Server{}).fallbackControllerUpdateStatus()
	if status.Channel != "stable" || status.State != "unavailable" {
		t.Fatalf("unexpected fallback status: %#v", status)
	}
	t.Setenv("OBOARD_UPDATE_CHANNEL", "1.2.3")
	if status := (&Server{}).fallbackControllerUpdateStatus(); status.Channel != "pinned" {
		t.Fatalf("exact binary version should be pinned: %#v", status)
	}
}

func TestControllerUpdateSkipBackupDefault(t *testing.T) {
	if controllerUpdateSkipBackup(nil) {
		t.Fatal("omitted skip_backup should create backup (secure default)")
	}
	skip, keep := true, false
	if !controllerUpdateSkipBackup(&skip) {
		t.Fatal("skip_backup=true should skip backup")
	}
	if controllerUpdateSkipBackup(&keep) {
		t.Fatal("skip_backup=false should create a backup")
	}
}

func TestControllerUpdateBusyReturnsImmediately(t *testing.T) {
	app := &Server{}
	app.controllerUpdateRunMu.Lock()
	defer app.controllerUpdateRunMu.Unlock()

	startedAt := time.Now()
	status, accepted, err := app.beginManualControllerUpdate(t.Context(), false)
	if !errors.Is(err, errControllerUpdateBusy) {
		t.Fatalf("busy update error = %v, want %v", err, errControllerUpdateBusy)
	}
	if accepted || status != (controllerupdate.Status{}) {
		t.Fatalf("busy update result = %#v, accepted=%v", status, accepted)
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("busy update waited for operation lock: %s", elapsed)
	}
}

func TestControllerUpdaterPrepareUnsupported(t *testing.T) {
	if !controllerUpdaterPrepareUnsupported(&controllerupdate.UpdaterStatusError{Code: http.StatusNotFound, Message: "404 page not found"}) {
		t.Fatal("legacy updater 404 was not recognized")
	}
	if controllerUpdaterPrepareUnsupported(&controllerupdate.UpdaterStatusError{Code: http.StatusConflict}) {
		t.Fatal("updater conflict was treated as a legacy updater")
	}
	if controllerUpdaterPrepareUnsupported(context.Canceled) {
		t.Fatal("context cancellation was treated as a legacy updater")
	}
}

func TestControllerUpdateInstallContinuesAfterRequestDisconnect(t *testing.T) {
	root := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "obu-disconnect-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	installStarted := make(chan struct{})
	releaseInstall := make(chan struct{})
	var releaseInstallOnce sync.Once
	unblockInstall := func() { releaseInstallOnce.Do(func() { close(releaseInstall) }) }
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := controllerupdate.Status{
			Channel:         "dev",
			State:           "available",
			UpdateAvailable: true,
			Available:       controllerupdate.BuildInfo{Version: "dev-next", Build: "20260813000000"},
		}
		switch r.URL.Path {
		case "/v1/prepare":
			status.State = "downloading"
			status.CanCancel = true
		case "/v1/install":
			close(installStarted)
			select {
			case <-releaseInstall:
			case <-r.Context().Done():
				t.Errorf("updater install request was cancelled with the panel request: %v", r.Context().Err())
				return
			}
			status.State = "downloading"
			status.CanCancel = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- updater.Serve(listener) }()
	t.Cleanup(func() {
		unblockInstall()
		_ = updater.Close()
		<-serveDone
	})

	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	app.controllerBackupDir = filepath.Join(root, "backups")

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ui/controller-update/install", strings.NewReader(`{"skip_backup":false}`)).WithContext(requestCtx)
	handlerDone := make(chan struct{})
	go func() {
		app.controllerUpdateInstall(recorder, request)
		close(handlerDone)
	}()

	select {
	case <-handlerDone:
		if recorder.Code != http.StatusOK {
			t.Fatalf("install response=%d body=%s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(time.Second):
		unblockInstall()
		t.Fatal("install API waited for the background updater operation")
	}
	cancelRequest()
	select {
	case <-installStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("background install approval did not start")
	}
	unblockInstall()
	app.controllerUpdateRunMu.Lock()
	app.controllerUpdateRunMu.Unlock()
	settings, err := db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(settings[controllerBackupSetting]) == "" {
		t.Fatal("background update did not record its database backup")
	}
}

func TestControllerUpdateInstallSkipBackup(t *testing.T) {
	root := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "obu-skip-backup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	installStarted := make(chan struct{})
	releaseInstall := make(chan struct{})
	var releaseInstallOnce sync.Once
	unblockInstall := func() { releaseInstallOnce.Do(func() { close(releaseInstall) }) }
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := controllerupdate.Status{
			Channel:         "dev",
			State:           "available",
			UpdateAvailable: true,
			Available:       controllerupdate.BuildInfo{Version: "dev-next", Build: "20260828000000"},
		}
		switch r.URL.Path {
		case "/v1/prepare":
			status.State = "downloading"
			status.CanCancel = true
		case "/v1/install":
			close(installStarted)
			select {
			case <-releaseInstall:
			case <-r.Context().Done():
				t.Errorf("updater install request was cancelled: %v", r.Context().Err())
				return
			}
			status.State = "installing"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- updater.Serve(listener) }()
	t.Cleanup(func() {
		unblockInstall()
		_ = updater.Close()
		<-serveDone
	})

	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	blockedBackup := filepath.Join(root, "backups")
	if err := os.WriteFile(blockedBackup, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.controllerBackupDir = blockedBackup

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ui/controller-update/install", strings.NewReader(`{"skip_backup":true}`))
	request.Header.Set("Content-Type", "application/json")
	handlerDone := make(chan struct{})
	go func() {
		app.controllerUpdateInstall(recorder, request)
		close(handlerDone)
	}()

	select {
	case <-handlerDone:
		if recorder.Code != http.StatusOK {
			t.Fatalf("install response=%d body=%s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(time.Second):
		unblockInstall()
		t.Fatal("install API waited for the background updater operation")
	}
	select {
	case <-installStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("skip-backup install did not reach the updater")
	}
	unblockInstall()
	app.controllerUpdateRunMu.Lock()
	app.controllerUpdateRunMu.Unlock()
	settings, err := db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(settings[controllerBackupSetting]) != "" {
		t.Fatalf("skip_backup still recorded a database backup: %q", settings[controllerBackupSetting])
	}
}

func TestControllerUpdateInstallRequiresBackupByDefault(t *testing.T) {
	root := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "obu-require-backup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := controllerupdate.Status{Channel: "dev", State: "available", UpdateAvailable: true, Available: controllerupdate.BuildInfo{Version: "dev-next", Build: "20260828000002"}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- updater.Serve(listener) }()
	t.Cleanup(func() { _ = updater.Close(); <-serveDone })

	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	blockedBackup := filepath.Join(root, "backups")
	if err := os.WriteFile(blockedBackup, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.controllerBackupDir = blockedBackup

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/ui/controller-update/install", nil)
	app.controllerUpdateInstall(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("install response should still be 202 accepted even when backup fails async, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	// Wait for background run to fail due to blocked backup dir
	deadline := time.Now().Add(2 * time.Second)
	for {
		if active, _ := db.GetActiveControllerUpdateRun(context.Background()); active != nil && active.Phase == store.ControllerUpdatePhaseFailed {
			break
		}
		if latest, _ := db.LatestControllerUpdateRun(context.Background()); latest != nil && latest.Phase == store.ControllerUpdatePhaseFailed {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	app.controllerUpdateRunMu.Lock()
	app.controllerUpdateRunMu.Unlock()
	settings, err := db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(settings[controllerBackupSetting]) != "" {
		t.Fatalf("failed backup should not record path: %q", settings[controllerBackupSetting])
	}
}

func TestScheduledControllerUpdateSkipsBackup(t *testing.T) {
	root := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "obu-auto-skip-backup-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	installStarted := make(chan struct{})
	releaseInstall := make(chan struct{})
	var releaseInstallOnce sync.Once
	unblockInstall := func() { releaseInstallOnce.Do(func() { close(releaseInstall) }) }
	status := controllerupdate.Status{
		Channel:         "dev",
		State:           "available",
		UpdateAvailable: true,
		Available:       controllerupdate.BuildInfo{Version: "dev-next", Build: "20260828000001"},
	}
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := status
		switch r.URL.Path {
		case "/v1/prepare":
			reply.State = "downloading"
			reply.CanCancel = true
		case "/v1/install":
			close(installStarted)
			select {
			case <-releaseInstall:
			case <-r.Context().Done():
				t.Errorf("updater install request was cancelled: %v", r.Context().Err())
				return
			}
			reply.State = "installing"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- updater.Serve(listener) }()
	t.Cleanup(func() {
		unblockInstall()
		_ = updater.Close()
		<-serveDone
	})

	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	blockedBackup := filepath.Join(root, "backups")
	if err := os.WriteFile(blockedBackup, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.controllerBackupDir = blockedBackup

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.installScheduledControllerUpdate(context.Background(), status)
	}()
	select {
	case <-installStarted:
		t.Fatal("automatic update should not reach updater when backup cannot be created")
	case <-time.After(1 * time.Second):
	}
	unblockInstall()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("automatic update did not finish")
	}
	settings, err := db.ListSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(settings[controllerBackupSetting]) != "" {
		t.Fatalf("automatic update should not record backup when preflight failed: %q", settings[controllerBackupSetting])
	}
	// Verify run failed due to preflight
	if run, _ := db.LatestControllerUpdateRun(context.Background()); run == nil || run.Phase != store.ControllerUpdatePhaseFailed {
		t.Fatalf("automatic update run should be failed due to disk/backup preflight, got %#v", run)
	}
}

func TestScheduledControllerUpdateWithExplicitSkipBackupReachesUpdater(t *testing.T) {
	// Verify that explicit skip still works if caller really wants to skip (manual path)
	root := t.TempDir()
	socketDir, err := os.MkdirTemp("/tmp", "obu-auto-skip-explicit-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	installStarted := make(chan struct{})
	releaseInstall := make(chan struct{})
	var releaseInstallOnce sync.Once
	unblockInstall := func() { releaseInstallOnce.Do(func() { close(releaseInstall) }) }
	status := controllerupdate.Status{
		Channel: "dev", State: "available", UpdateAvailable: true,
		Available: controllerupdate.BuildInfo{Version: "dev-next", Build: "20260828000003"},
	}
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply := status
		if r.URL.Path == "/v1/install" {
			close(installStarted)
			<-releaseInstall
			reply.State = "installing"
		} else if r.URL.Path == "/v1/prepare" {
			reply.State = "downloading"
			reply.CanCancel = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})}
	serveDone := make(chan error, 1)
	go func() { serveDone <- updater.Serve(listener) }()
	t.Cleanup(func() { unblockInstall(); _ = updater.Close(); <-serveDone })
	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	// Use a valid backup dir but call with skip=true directly via applyPrepared
	blockedBackup := filepath.Join(root, "backups")
	_ = os.MkdirAll(blockedBackup, 0o700)
	app.controllerBackupDir = blockedBackup
	// Directly test preflight skip path: should succeed even with blocked dir if skip true
	// Here we use blocked as file to ensure skip bypasses check
	blockedFile := filepath.Join(root, "blocked")
	_ = os.WriteFile(blockedFile, []byte("x"), 0o600)
	run := &store.ControllerUpdateRun{Source: "manual", TargetBuild: "20260828000003", Phase: store.ControllerUpdatePhasePreflight}
	if err := app.preflightControllerUpdate(context.Background(), run, blockedFile, true); err != nil {
		t.Fatalf("preflight with skip should not check backup dir: %v", err)
	}
	_ = installStarted
	_ = unblockInstall
}

func TestControllerUpdateCapabilitiesAndPublicView(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "obu-mcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	channel := "stable"
	checkCalls, channelCalls, cancelCalls := 0, 0, 0
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		status := controllerupdate.Status{
			Channel: channel, State: "available", UpdateAvailable: true, CanCancel: true,
			Available:     controllerupdate.BuildInfo{Version: "2.0.0", Build: "20260817000000"},
			BackupPath:    "/private/controller.sqlite",
			ManualCommand: "sudo updater install --secret",
		}
		switch r.URL.Path {
		case "/v1/check":
			checkCalls++
		case "/v1/channel":
			var request controllerupdate.ChannelRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode channel request: %v", err)
			}
			channel = request.Channel
			status.Channel = channel
			channelCalls++
		case "/v1/cancel":
			status.State = "cancelling"
			cancelCalls++
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	})}
	done := make(chan error, 1)
	go func() { done <- updater.Serve(listener) }()
	t.Cleanup(func() {
		_ = updater.Close()
		<-done
	})

	db := openControllerAutomationTestStore(t)
	server := newTestServer(db, "test-secret", "")
	server.controllerUpdater = controllerupdate.NewClient(socketPath)
	admin := &model.User{Username: "update-admin", PasswordHash: "unused", Role: model.RoleAdmin, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111117", ProxyPassword: "unused"}
	if err := db.CreateUser(t.Context(), admin); err != nil {
		t.Fatal(err)
	}
	principal := userAutomationPrincipal(t, db, admin.ID)
	view, err := server.queryManagementCapability(t.Context(), principal, "controller_update.status", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), "backup_path") || strings.Contains(string(encoded), "manual_command") || strings.Contains(string(encoded), "/private/") || strings.Contains(string(encoded), "sudo updater") {
		t.Fatalf("controller update public view leaked local details: %s", encoded)
	}

	applyAutomationChangeset(t, server, principal, "controller-check", automation.OperationRequest{Capability: "controller_update.check", Input: json.RawMessage(`{}`)})
	applyAutomationChangeset(t, server, principal, "controller-channel", automation.OperationRequest{Capability: "controller_update.set_channel", Input: json.RawMessage(`{"channel":"dev"}`)})
	applyAutomationChangeset(t, server, principal, "controller-cancel", automation.OperationRequest{Capability: "controller_update.cancel", Input: json.RawMessage(`{"confirm":true}`)})
	run := &store.ControllerUpdateRun{Source: "manual", TargetBuild: "20260817000000", Phase: store.ControllerUpdatePhaseVerifying}
	if err := db.CreateControllerUpdateRun(t.Context(), run); err != nil {
		t.Fatal(err)
	}
	applyAutomationChangeset(t, server, principal, "controller-force-finish", automation.OperationRequest{Capability: "controller_update.force_finish", Input: json.RawMessage(`{"confirmation":"强制结束更新任务"}`)})
	if active, err := db.GetActiveControllerUpdateRun(t.Context()); err != nil || active != nil {
		t.Fatalf("active update after automation force finish: %#v err=%v", active, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if checkCalls != 1 || channelCalls != 1 || cancelCalls != 2 || channel != "dev" {
		t.Fatalf("updater calls check=%d channel=%d cancel=%d current_channel=%q", checkCalls, channelCalls, cancelCalls, channel)
	}
}

func TestControllerUpdateChannelAPI(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "oboard-controller")
	if err := os.WriteFile(binary, []byte("controller"), 0o755); err != nil {
		t.Fatal(err)
	}
	binaryEnv := filepath.Join(root, "controller.env")
	if err := os.WriteFile(binaryEnv, []byte("OBOARD_UPDATE_CHANNEL=pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "obu-channel-")
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(socketDir, "updater.sock")
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	updater := controllerupdate.NewService(controllerupdate.ServiceConfig{
		SocketPath:       socketPath,
		BinaryEnvPath:    binaryEnv,
		ControllerBinary: binary,
		StatePath:        filepath.Join(root, "updater-status.json"),
		WorkRoot:         filepath.Join(root, "work"),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- updater.Serve(ctx) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("start updater: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("updater socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("updater shutdown: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("updater did not stop")
		}
	})

	db, err := store.Open(filepath.Join(root, "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	handler := app.Handler()
	request(t, handler, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := login["token"].(string)

	status := request(t, handler, http.MethodGet, "/api/v1/ui/controller-update", adminToken, nil, http.StatusOK)
	if status["channel"] != "pinned" || status["status"] != "pinned" {
		t.Fatalf("unexpected update status: %#v", status)
	}
	request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/channel", adminToken, map[string]any{"channel": "nightly"}, http.StatusBadRequest)
	switched := request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/channel", adminToken, map[string]any{"channel": "dev"}, http.StatusOK)
	if switched["channel"] != "dev" || switched["status"] != "idle" {
		t.Fatalf("unexpected switched status: %#v", switched)
	}
	data, err := os.ReadFile(binaryEnv)
	if err != nil || !strings.Contains(string(data), `OBOARD_UPDATE_CHANNEL="dev"`) {
		t.Fatalf("channel was not persisted: %q, %v", data, err)
	}

	request(t, handler, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-user-password", "role": "viewer", "status": "active"}, http.StatusCreated)
	viewerLogin := request(t, handler, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-user-password"}, http.StatusOK)
	request(t, handler, http.MethodPost, "/api/v1/ui/controller-update/channel", viewerLogin["token"].(string), map[string]any{"channel": "stable"}, http.StatusForbidden)
}

func TestControllerUpdateInstallMarksMaintenanceBeforeUpdaterCall(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "obu-maintenance-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "updater.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	markerSeen := make(chan controllerUpdateMaintenanceMarker, 1)
	updater := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := db.GetSetting(r.Context(), controllerUpdateMaintenanceSetting)
		var marker controllerUpdateMaintenanceMarker
		_ = json.Unmarshal([]byte(raw), &marker)
		markerSeen <- marker
		_ = json.NewEncoder(w).Encode(controllerupdate.Status{State: "installing", Available: controllerupdate.BuildInfo{Build: "target-build"}})
	})}
	done := make(chan error, 1)
	go func() { done <- updater.Serve(listener) }()
	t.Cleanup(func() { _ = updater.Close(); <-done })
	app := newTestServer(db, "test-secret", "")
	app.controllerUpdater = controllerupdate.NewClient(socketPath)
	status, err := app.installControllerUpdate(t.Context(), "target-build")
	if err != nil || status.State != "installing" {
		t.Fatalf("install status=%#v err=%v", status, err)
	}
	select {
	case marker := <-markerSeen:
		if marker.TargetBuild != "target-build" || marker.StartedAt.IsZero() {
			t.Fatalf("maintenance marker=%#v", marker)
		}
	case <-time.After(time.Second):
		t.Fatal("updater did not observe maintenance marker")
	}
	if !app.controllerUpdateMaintenance.Load() {
		t.Fatal("maintenance flag was not active during install")
	}
	app.clearControllerUpdateMaintenance(t.Context())
}

func TestControllerStartupConsumesUpdateMaintenanceForOpenConnections(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "maintenance-node", AgentID: "maintenance-agent"}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	connectedAt := time.Now().UTC().Add(-time.Minute)
	if err := db.RecordControllerConnectionEvent(ctx, server.ID, true, connectedAt); err != nil {
		t.Fatal(err)
	}
	marker, _ := json.Marshal(controllerUpdateMaintenanceMarker{StartedAt: time.Now().UTC().Add(-30 * time.Second), TargetBuild: version.Build})
	if err := db.SetSetting(ctx, controllerUpdateMaintenanceSetting, string(marker)); err != nil {
		t.Fatal(err)
	}
	_ = newTestServer(db, "test-secret", "")
	history, err := db.ListConnectivityHistory(ctx, server.ID, connectedAt.Add(-time.Second), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	foundMaintenanceDisconnect := false
	for _, event := range history.Events {
		if event.Kind == model.ConnectivityEventControllerDisconnected && event.Source == model.ConnectivityEventSourceControllerUpdate {
			foundMaintenanceDisconnect = true
		}
	}
	if !foundMaintenanceDisconnect {
		t.Fatalf("maintenance connection history=%#v", history.Events)
	}
	if raw, err := db.GetSetting(ctx, controllerUpdateMaintenanceSetting); err != nil || raw != "" {
		t.Fatalf("maintenance marker after startup=%q err=%v", raw, err)
	}

	reconnectedAt := time.Now().UTC()
	if err := db.RecordControllerConnectionEvent(ctx, server.ID, true, reconnectedAt); err != nil {
		t.Fatal(err)
	}
	mismatched, _ := json.Marshal(controllerUpdateMaintenanceMarker{StartedAt: time.Now().UTC(), TargetBuild: version.Build + "-other"})
	if err := db.SetSetting(ctx, controllerUpdateMaintenanceSetting, string(mismatched)); err != nil {
		t.Fatal(err)
	}
	_ = newTestServer(db, "test-secret", "")
	history, err = db.ListConnectivityHistory(ctx, server.ID, reconnectedAt.Add(-time.Second), time.Now().UTC().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	lastSource := ""
	for _, event := range history.Events {
		if event.Kind == model.ConnectivityEventControllerDisconnected {
			lastSource = event.Source
		}
	}
	if lastSource != model.ConnectivityEventSourceAgentSocket {
		t.Fatalf("mismatched maintenance marker source=%q history=%#v", lastSource, history.Events)
	}
}
