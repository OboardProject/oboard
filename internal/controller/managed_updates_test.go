package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
	"github.com/OboardProject/oboard/internal/version"
)

func TestAutomaticUpdateAllowedAtUsesControllerTimezone(t *testing.T) {
	settings := map[string]string{
		updateWindowEnabledSetting:   "true",
		updateWindowStartHourSetting: "3",
		updateWindowEndHourSetting:   "7",
		"traffic_timezone":           "Asia/Shanghai",
	}
	if !automaticUpdateAllowedAt(settings, time.Date(2026, 8, 10, 19, 0, 0, 0, time.UTC)) {
		t.Fatal("03:00 in the Controller timezone should be inside the update window")
	}
	if automaticUpdateAllowedAt(settings, time.Date(2026, 8, 10, 23, 0, 0, 0, time.UTC)) {
		t.Fatal("07:00 in the Controller timezone should be outside the update window")
	}
	settings[updateWindowStartHourSetting], settings[updateWindowEndHourSetting] = "22", "4"
	if !automaticUpdateAllowedAt(settings, time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)) {
		t.Fatal("a cross-midnight update window should include 23:00")
	}
}

func TestScheduledManagedUpdatesQueueOutdatedTargetsOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "controller.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "node", Status: model.ServerOnline, AgentID: "agent-1", AgentBuild: "20260101000000"}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(time.Hour)
	relay := &model.SubscriptionRelay{Name: "relay", PublicURL: "https://relay.example", Status: "pending", EnrollmentHash: "enroll", EnrollmentExpiresAt: &expiresAt}
	if err := db.CreateSubscriptionRelay(context.Background(), relay); err != nil {
		t.Fatal(err)
	}
	relay, err = db.ClaimSubscriptionRelayEnrollment(context.Background(), "enroll", "token", "secret")
	if err != nil {
		t.Fatal(err)
	}
	relay.Status, relay.Build = "online", "20260101000000"
	if err := db.UpdateSubscriptionRelayHeartbeat(context.Background(), relay); err != nil {
		t.Fatal(err)
	}
	oldAgentBuild, oldControllerBuild := version.AgentBuild, version.Build
	version.AgentBuild, version.Build = "20260811030000", "20260811030000"
	t.Cleanup(func() { version.AgentBuild, version.Build = oldAgentBuild, oldControllerBuild })
	if err := db.SetSettings(context.Background(), map[string]string{agentAutoUpdateSetting: "true", subscriptionRelayAutoUpdateSetting: "true"}); err != nil {
		t.Fatal(err)
	}
	s := newTestServer(db, "test-secret", "")
	if err := db.SetSetting(context.Background(), "controller_url", "https://controller.example"); err != nil {
		t.Fatal(err)
	}
	s.runScheduledManagedUpdates(context.Background())
	s.runScheduledManagedUpdates(context.Background())
	tasks, err := db.ListTasksByServer(context.Background(), server.ID, 10)
	if err != nil || len(tasks) != 1 || tasks[0].Type != model.AgentTaskTypeUpdateAgent {
		t.Fatalf("Agent update tasks = %#v, err = %v", tasks, err)
	}
	updatedRelay, err := db.GetSubscriptionRelay(context.Background(), relay.ID)
	if err != nil || updatedRelay.UpdateRequestedAt == nil || updatedRelay.UpdateTargetBuild != version.Build {
		t.Fatalf("relay update = %#v, err = %v", updatedRelay, err)
	}
}
