package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

func TestTimeCheckSettingsAndServerModeChangeQueueImmediately(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	login := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	token := login["token"].(string)

	settings := request(t, h, http.MethodGet, "/api/v1/ui/settings", token, nil, http.StatusOK)["settings"].(map[string]any)
	if settings[settingServerDefaultTimeCorrection] != string(model.TimeCorrectionAuto) {
		t.Fatalf("default time correction = %#v", settings[settingServerDefaultTimeCorrection])
	}
	servers, ok := settings[settingTimeCheckNTPServers].([]any)
	if !ok || len(servers) != 3 {
		t.Fatalf("default NTP servers = %#v", settings[settingTimeCheckNTPServers])
	}
	request(t, h, http.MethodPost, "/api/v1/ui/settings", token, map[string]any{
		"server_default_time_correction_mode": model.TimeCorrectionAuto,
		"time_check_ntp_servers":              []string{"ntp1.example.com", "ntp2.example.com", "ntp3.example.com"},
	}, http.StatusOK)

	created := request(t, h, http.MethodPost, "/api/v1/ui/servers", token, map[string]any{
		"name": "edge", "listen_ip": "0.0.0.0", "port_range_start": 10000, "port_range_end": 10010,
	}, http.StatusCreated)["server"].(map[string]any)
	serverID := int64(created["id"].(float64))
	if created["time_correction_mode"] != string(model.TimeCorrectionAuto) {
		t.Fatalf("new server time correction = %#v", created["time_correction_mode"])
	}

	server, err := db.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	server.AgentID = "agent-edge"
	server.Status = model.ServerOnline
	if err := db.UpdateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	server.TimeCorrectionMode = model.TimeCorrectionNTP
	response := request(t, h, http.MethodPatch, "/api/v1/ui/servers/"+itoa(serverID), token, server, http.StatusOK)
	if response["time_check_task"] == nil {
		t.Fatalf("mode change did not queue an immediate time check: %#v", response)
	}
	tasks, err := db.ListTasksByServer(context.Background(), serverID, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Type != model.AgentTaskTypeCheckTime {
		t.Fatalf("time check tasks = %#v, err=%v", tasks, err)
	}
	var plan model.TimeCheckPlan
	if err := json.Unmarshal([]byte(tasks[0].PayloadJSON), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.CorrectionMode != model.TimeCorrectionNTP || plan.ThresholdSeconds != timeCheckThresholdSeconds || len(plan.NTPServers) != 3 || plan.NTPServers[0] != "ntp1.example.com" || !plan.Force {
		t.Fatalf("queued time check plan = %#v", plan)
	}
}

func TestNormalizeTimeCheckNTPServersCanonicalizesIPv6AndRejectsPorts(t *testing.T) {
	servers, err := normalizeTimeCheckNTPServers([]string{"[2001:db8::1]", "time.example.com", "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if servers[0] != "2001:db8::1" {
		t.Fatalf("normalized IPv6 NTP server = %q", servers[0])
	}
	if _, err := normalizeTimeCheckNTPServers([]string{"[2001:db8::1]:123", "time.example.com", "192.0.2.1"}); err == nil {
		t.Fatal("NTP server with an explicit port was accepted")
	}
}

func TestDailyTimeCheckStoresResultAndNotifiesAdminsOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	h := srv.Handler()
	request(t, h, http.MethodPost, "/api/v1/ui/auth/bootstrap", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusCreated)
	adminLogin := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "admin", "password": "very-secure-password"}, http.StatusOK)
	adminToken := adminLogin["token"].(string)
	viewer := request(t, h, http.MethodPost, "/api/v1/ui/users", adminToken, map[string]any{"username": "viewer", "password": "long-viewer-password", "role": "viewer", "status": "active"}, http.StatusCreated)["user"].(map[string]any)
	viewerLogin := request(t, h, http.MethodPost, "/api/v1/ui/auth/login", "", map[string]any{"username": "viewer", "password": "long-viewer-password"}, http.StatusOK)
	viewerToken := viewerLogin["token"].(string)
	channel := request(t, h, http.MethodPost, "/api/v1/ui/notification-channels", adminToken, map[string]any{"name": "admin-clock", "type": "telegram", "enabled": true, "events": notificationServerOffline, "config_json": `{}`}, http.StatusCreated)["notification_channel"].(map[string]any)
	bindTestTelegramChannel(t, srv, db, int64(channel["id"].(float64)), 1)
	request(t, h, http.MethodPost, "/api/v1/ui/notification-channels", viewerToken, map[string]any{"name": "viewer-other", "type": "bark", "enabled": true, "events": notificationAdminAnnouncement, "config_json": `{"device_key":"viewer"}`}, http.StatusCreated)

	var sentMu sync.Mutex
	sentOwners := []int64{}
	srv.notificationSender = func(_ context.Context, channel model.NotificationChannel, _, _ string) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sentOwners = append(sentOwners, channel.OwnerUserID)
		return nil
	}
	server := &model.Server{Name: "clock-edge", AgentID: "agent-clock", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline, TimeCorrectionMode: model.TimeCorrectionOff}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	srv.scheduleDailyTimeChecks(ctx)
	srv.scheduleDailyTimeChecks(ctx)
	tasks, err := db.ListTasksByServer(ctx, server.ID, 10)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("daily time checks = %#v, err=%v", tasks, err)
	}
	result := model.TimeCheckResult{Status: "skewed", CorrectionMode: model.TimeCorrectionOff, RawOffsetMS: 45_000, EffectiveOffsetMS: 45_000, Source: "ntp:test", CheckedAt: time.Now().UTC()}
	raw, _ := json.Marshal(result)
	if err := srv.applyTimeCheckTaskResult(ctx, tasks[0], "succeeded", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := srv.applyTimeCheckTaskResult(ctx, tasks[0], "succeeded", string(raw)); err != nil {
		t.Fatal(err)
	}
	srv.notificationWG.Wait()
	sentMu.Lock()
	owners := append([]int64(nil), sentOwners...)
	sentMu.Unlock()
	adminID := int64(adminLogin["user"].(map[string]any)["id"].(float64))
	viewerID := int64(viewer["id"].(float64))
	if len(owners) != 1 || owners[0] != adminID || owners[0] == viewerID {
		t.Fatalf("forced clock notifications reached owners %#v, want admin %d only", owners, adminID)
	}
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TimeCheckStatus != "skewed" || stored.TimeOffsetMS != 45_000 || stored.TimeCheckSource != "ntp:test" || stored.TimeCheckedAt == nil {
		t.Fatalf("stored time check = %#v", stored)
	}
	if err := db.CompleteTask(ctx, tasks[0].ID, "succeeded", string(raw)); err != nil {
		t.Fatal(err)
	}
	srv.scheduleDailyTimeChecks(ctx)
	after, _ := db.ListTasksByServer(ctx, server.ID, 10)
	if len(after) != 1 {
		t.Fatalf("fresh result was scheduled again: %#v", after)
	}

	oldResult := result
	oldResult.CheckedAt = time.Now().UTC().Add(-25 * time.Hour)
	if err := db.UpdateServerTimeCheck(ctx, server.ID, oldResult); err != nil {
		t.Fatal(err)
	}
	srv.scheduleDailyTimeChecks(ctx)
	after, _ = db.ListTasksByServer(ctx, server.ID, 10)
	if len(after) != 2 {
		t.Fatalf("stale result did not schedule a new check: %#v", after)
	}
}

func TestTimedOutTimeCheckRecordsAttemptAndDoesNotImmediatelyRequeue(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "oboard.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	srv := newTestServer(db, "test-secret", "")
	server := &model.Server{Name: "timeout-edge", AgentID: "agent-timeout", ListenIP: "0.0.0.0", PortRangeStart: 10000, PortRangeEnd: 10010, Status: model.ServerOnline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	task, err := srv.queueTimeCheck(ctx, *server, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetTaskStateForTest(ctx, task.ID, "pending", time.Now().Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	srv.expireTimedOutTasks(ctx)
	stored, err := db.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TimeCheckStatus != "unavailable" || stored.TimeCheckedAt == nil || stored.TimeCheckError == "" {
		t.Fatalf("timed out time check state = %#v", stored)
	}
	srv.scheduleDailyTimeChecks(ctx)
	tasks, _ := db.ListTasksByServer(ctx, server.ID, 10)
	if len(tasks) != 1 {
		t.Fatalf("timed out check was immediately requeued: %#v", tasks)
	}
}
