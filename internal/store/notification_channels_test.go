package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestNotificationChannelReenableDiscardsBacklog(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "notifications.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "a", ProxyPassword: "a"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "test", Type: "test", Enabled: true, Events: "admin_announcement"}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	queue := func(key string, want bool) model.NotificationDelivery {
		t.Helper()
		d := model.NotificationDelivery{ChannelID: channel.ID, Event: "admin_announcement", EventKey: key, Title: key}
		inserted, err := db.QueueNotificationDelivery(ctx, &d)
		if err != nil || inserted != want {
			t.Fatalf("queue %s: inserted=%v err=%v", key, inserted, err)
		}
		return d
	}
	pending := queue("pending", true)
	failed := queue("failed", true)
	if err := db.CompleteNotificationDelivery(ctx, failed.ID, errors.New("retry"), time.Now()); err != nil {
		t.Fatal(err)
	}
	sent := queue("sent", true)
	if err := db.CompleteNotificationDelivery(ctx, sent.ID, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	channel.Enabled = false
	if err := db.UpdateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	queue("disabled", false)
	// A sender finishing after disable must not resurrect a cancelled retry.
	if err := db.CompleteNotificationDelivery(ctx, pending.ID, errors.New("late failure"), time.Now()); err != nil {
		t.Fatal(err)
	}
	channel.Enabled = true
	if err := db.UpdateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	queue("pending", false)
	fresh := queue("fresh", true)
	channel.Name = "renamed"
	if err := db.UpdateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	deliveries, err := db.ListPendingNotificationDeliveries(ctx, time.Now().Add(time.Hour), 50)
	if err != nil || len(deliveries) != 1 || deliveries[0].ID != fresh.ID {
		t.Fatalf("pending=%+v err=%v", deliveries, err)
	}
	for id, want := range map[int64]string{pending.ID: "cancelled", failed.ID: "cancelled", sent.ID: "sent"} {
		var status string
		if err := db.db.QueryRowContext(ctx, "select status from notification_deliveries where id=?", id).Scan(&status); err != nil || status != want {
			t.Fatalf("delivery %d status=%s want=%s err=%v", id, status, want, err)
		}
	}
}

func TestNotificationBroadcastReenableDiscardsBacklog(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "broadcast.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "a", ProxyPassword: "a"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "bot", Type: "telegram", Enabled: true, Events: "admin_announcement"}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTelegramBindingCode(ctx, "code", user.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	binding, err := db.ConsumeTelegramBindingCode(ctx, "code", channel.ID, 10, 20, "private", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	create := func(key string) int64 {
		t.Helper()
		b := model.NotificationBroadcast{ActorUserID: user.ID, ActorName: "admin", Title: key, Body: "body", FilterJSON: "{}", IdempotencyKey: key}
		ok, err := db.CreateNotificationBroadcast(ctx, &b, []BroadcastRecipient{{UserID: user.ID, Bindings: []model.TelegramBinding{*binding}}})
		if err != nil || !ok {
			t.Fatalf("create %s: %v %v", key, ok, err)
		}
		return b.ID
	}
	old := create("old")
	targets, err := db.ListPendingNotificationBroadcastTargets(ctx, time.Now().Add(time.Hour), 50)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%v err=%v", targets, err)
	}
	channel.Enabled = false
	if err := db.UpdateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	disabled := create("disabled")
	if err := db.CompleteNotificationBroadcastTarget(ctx, targets[0].ID, errors.New("late failure"), time.Now()); err != nil {
		t.Fatal(err)
	}
	channel.Enabled = true
	if err := db.UpdateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	fresh := create("fresh")
	targets, err = db.ListPendingNotificationBroadcastTargets(ctx, time.Now().Add(time.Hour), 50)
	if err != nil || len(targets) != 1 || targets[0].BroadcastID != fresh {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	for _, id := range []int64{old, disabled} {
		b, err := db.GetNotificationBroadcast(ctx, id)
		if err != nil || b.Status != "failed" || b.FailureCount != 1 || b.CompletedAt == nil {
			t.Fatalf("broadcast=%+v err=%v", b, err)
		}
	}
}

func TestNotificationEnabledSinceMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "a", ProxyPassword: "a"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "bot", Type: "test", Enabled: false, Events: "server_offline"}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx, "alter table notification_channels drop column enabled_since"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	channel, err = db.GetNotificationChannel(ctx, channel.ID)
	if err != nil || channel.Enabled || !channel.EnabledSince.IsZero() {
		t.Fatalf("migrated=%+v err=%v", channel, err)
	}
	oldTime := time.Now().Add(-time.Hour)
	channel.Enabled = true
	if err := db.UpdateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	enabled, err := db.GetNotificationChannel(ctx, channel.ID)
	if err != nil || enabled.EnabledSince.IsZero() {
		t.Fatalf("enabled=%+v err=%v", enabled, err)
	}
	channel.Name = "renamed"
	if err := db.UpdateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	current, err := db.GetNotificationChannel(ctx, channel.ID)
	if err != nil || !current.EnabledSince.Equal(enabled.EnabledSince) {
		t.Fatalf("reopened=%+v err=%v", current, err)
	}
	for key, occurred := range map[string]time.Time{"old": oldTime, "new": time.Now()} {
		delivery := model.NotificationDelivery{ChannelID: channel.ID, Event: "server_offline", EventKey: key, OccurredAt: occurred}
		inserted, err := db.QueueNotificationDelivery(ctx, &delivery)
		if err != nil || inserted != (key == "new") {
			t.Fatalf("%s inserted=%v err=%v", key, inserted, err)
		}
	}
}
