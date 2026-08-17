package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestNodeOperationsTablesMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "node-operations-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{
		"notification_broadcast_targets", "notification_broadcasts", "operation_confirmations",
		"telegram_bot_state", "telegram_bindings", "telegram_binding_codes",
		"node_publication_isolations", "node_incident_actions", "node_incident_telegram_messages", "node_incidents",
	} {
		if _, err := raw.Exec(`drop table ` + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("open previous schema: %v", err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	for _, table := range []string{"node_incidents", "node_incident_telegram_messages", "node_publication_isolations", "node_incident_actions", "telegram_binding_codes", "telegram_bindings", "telegram_bot_state", "operation_confirmations", "notification_broadcasts", "notification_broadcast_targets"} {
		var count int
		if err := db.db.QueryRowContext(ctx, `select count(*) from sqlite_master where type='table' and name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s migration count=%d err=%v", table, count, err)
		}
	}
}

func TestNodeIncidentLifecycleReusesFlapAndAutoRestoresIsolation(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "incident.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := &model.Server{Name: "edge", Status: model.ServerOffline}
	if err := db.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	inbound := &model.Inbound{ServerID: server.ID, Name: "published", Protocol: model.ProtocolVLESS, Port: 443, ConfigJSON: "{}", Enabled: true}
	manualInbound := &model.Inbound{ServerID: server.ID, Name: "manual", Protocol: model.ProtocolVLESS, Port: 8443, ConfigJSON: "{}", Enabled: true}
	for _, item := range []*model.Inbound{inbound, manualInbound} {
		if err := db.CreateInbound(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	firstOffline := time.Date(2026, 8, 18, 1, 0, 0, 0, time.UTC)
	detected := firstOffline.Add(2 * time.Minute)
	incident, created, err := db.OpenOrReopenNodeIncident(ctx, *server, firstOffline, detected, 2*time.Minute, 5*time.Minute, `{"inbounds":[]}`)
	if err != nil || !created || incident.Status != model.NodeIncidentActive {
		t.Fatalf("open incident=%#v created=%v err=%v", incident, created, err)
	}
	again, created, err := db.OpenOrReopenNodeIncident(ctx, *server, firstOffline, detected.Add(time.Minute), 2*time.Minute, 5*time.Minute, `{"inbounds":[]}`)
	if err != nil || created || again.ID != incident.ID || again.Version != incident.Version {
		t.Fatalf("duplicate incident=%#v created=%v err=%v", again, created, err)
	}
	isolations, err := db.CreateNodePublicationIsolations(ctx, incident.ID, user.ID, []int64{inbound.ID}, "auto")
	if err != nil || len(isolations) != 1 {
		t.Fatalf("isolate=%#v err=%v", isolations, err)
	}
	if _, err := db.CreateNodePublicationIsolations(ctx, incident.ID, user.ID, []int64{manualInbound.ID}, "manual"); err != nil {
		t.Fatal(err)
	}
	candidate := detected.Add(3 * time.Minute)
	recovering, err := db.MarkNodeIncidentRecovering(ctx, server.ID, candidate, 5*time.Minute)
	if err != nil || recovering == nil || recovering.Status != model.NodeIncidentRecovering {
		t.Fatalf("recovering=%#v err=%v", recovering, err)
	}
	flapped, created, err := db.OpenOrReopenNodeIncident(ctx, *server, firstOffline, candidate.Add(time.Minute), 2*time.Minute, 5*time.Minute, `{"inbounds":[]}`)
	if err != nil || created || flapped.ID != incident.ID || flapped.Status != model.NodeIncidentActive || flapped.FlapCount != 1 {
		t.Fatalf("flapped=%#v created=%v err=%v", flapped, created, err)
	}
	secondCandidate := candidate.Add(2 * time.Minute)
	recovering, err = db.MarkNodeIncidentRecovering(ctx, server.ID, secondCandidate, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := db.ResolveNodeIncident(ctx, incident.ID, recovering.Version, secondCandidate.Add(5*time.Minute))
	if err != nil || resolved.Status != model.NodeIncidentResolved || resolved.OutageDurationSeconds != int64(secondCandidate.Sub(firstOffline).Seconds()) {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	stored, err := db.ListNodePublicationIsolations(ctx, incident.ID)
	if err != nil || len(stored) != 2 || stored[0].Status != "restored" || stored[1].Status != "hidden" {
		t.Fatalf("automatic/manual restore=%#v err=%v", stored, err)
	}
	if _, _, err := db.OpenOrReopenNodeIncident(ctx, *server, secondCandidate.Add(time.Minute), secondCandidate.Add(3*time.Minute), 2*time.Minute, 5*time.Minute, `{}`); err != nil {
		t.Fatalf("new incident after resolve: %v", err)
	}
}

func TestTelegramBindingCodeAndPollLeaseAreOneTime(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "telegram.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "bot", Type: "telegram", Enabled: true, Events: "admin_announcement", ConfigJSON: `{"bot_token":"token","chat_id":"1","interactive":true,"allowed_chat_ids":"1"}`}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	nowTime := time.Now().UTC()
	if err := db.CreateTelegramBindingCode(ctx, "code-hash", user.ID, nowTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	binding, err := db.ConsumeTelegramBindingCode(ctx, "code-hash", channel.ID, 10, 20, "private", nowTime)
	if err != nil || binding.UserID != user.ID || binding.ChatID != 10 {
		t.Fatalf("binding=%#v err=%v", binding, err)
	}
	if _, err := db.ConsumeTelegramBindingCode(ctx, "code-hash", channel.ID, 10, 20, "private", nowTime); err == nil {
		t.Fatal("binding code was reusable")
	}
	offset, claimed, err := db.ClaimTelegramBotPoll(ctx, "bot-hash", "first", nowTime, time.Minute)
	if err != nil || !claimed || offset != 0 {
		t.Fatalf("first lease offset=%d claimed=%v err=%v", offset, claimed, err)
	}
	if _, claimed, err := db.ClaimTelegramBotPoll(ctx, "bot-hash", "second", nowTime.Add(time.Second), time.Minute); err != nil || claimed {
		t.Fatalf("second lease claimed=%v err=%v", claimed, err)
	}
	if err := db.SaveTelegramBotOffset(ctx, "bot-hash", "first", 42); err != nil {
		t.Fatal(err)
	}
	offset, claimed, err = db.ClaimTelegramBotPoll(ctx, "bot-hash", "second", nowTime.Add(2*time.Minute), time.Minute)
	if err != nil || !claimed || offset != 42 {
		t.Fatalf("takeover offset=%d claimed=%v err=%v", offset, claimed, err)
	}
}

func TestNotificationBroadcastRetryNeverRepeatsSentTarget(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "broadcast.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin := &model.User{Username: "admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "a", ProxyPassword: "a"}
	user := &model.User{Username: "user", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "u", ProxyPassword: "u"}
	for _, item := range []*model.User{admin, user} {
		if err := db.CreateUser(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "bot", Type: "telegram", Enabled: true, Events: "admin_announcement", ConfigJSON: `{"bot_token":"token","chat_id":"1"}`}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateTelegramBindingCode(ctx, "broadcast-code", user.ID, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	binding, err := db.ConsumeTelegramBindingCode(ctx, "broadcast-code", channel.ID, 10, 20, "private", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	broadcast := model.NotificationBroadcast{ActorUserID: admin.ID, ActorName: "admin", Title: "notice", Body: "body", FilterJSON: `{}`, IdempotencyKey: "same-key"}
	created, err := db.CreateNotificationBroadcast(ctx, &broadcast, []BroadcastRecipient{{UserID: user.ID, Bindings: []model.TelegramBinding{*binding}}})
	if err != nil || !created {
		t.Fatalf("create=%v broadcast=%#v err=%v", created, broadcast, err)
	}
	targets, err := db.ListPendingNotificationBroadcastTargets(ctx, time.Now().Add(time.Minute), 10)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
	if err := db.CompleteNotificationBroadcastTarget(ctx, targets[0].ID, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	targets, err = db.ListPendingNotificationBroadcastTargets(ctx, time.Now().Add(time.Hour), 10)
	if err != nil || len(targets) != 0 {
		t.Fatalf("sent target retried: %#v err=%v", targets, err)
	}
	duplicate := model.NotificationBroadcast{ActorUserID: admin.ID, ActorName: "admin", Title: "notice", Body: "body", FilterJSON: `{}`, IdempotencyKey: "same-key"}
	created, err = db.CreateNotificationBroadcast(ctx, &duplicate, []BroadcastRecipient{{UserID: user.ID}})
	if err != nil || created || duplicate.ID != broadcast.ID {
		t.Fatalf("idempotent create=%v duplicate=%#v err=%v", created, duplicate, err)
	}
}
