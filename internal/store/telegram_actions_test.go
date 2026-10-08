package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestTelegramActionTokensMigrateFromPreviousSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "previous.sqlite")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`create table notification_deliveries (id integer primary key autoincrement, channel_id integer not null, event text not null, event_key text not null, title text not null, body text not null, status text not null default 'pending', attempts integer not null default 0, error text not null default '', next_attempt_at text not null, created_at text not null, updated_at text not null, sent_at text, unique(channel_id,event,event_key))`)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var contextColumn int
	if err := db.db.QueryRowContext(ctx, `select count(*) from pragma_table_info('notification_deliveries') where name='context_json'`).Scan(&contextColumn); err != nil || contextColumn != 1 {
		t.Fatalf("context_json column = %d, err=%v", contextColumn, err)
	}
	var tokenTable string
	if err := db.db.QueryRowContext(ctx, `select name from sqlite_master where type='table' and name='telegram_action_tokens'`).Scan(&tokenTable); err != nil || tokenTable != "telegram_action_tokens" {
		t.Fatalf("token table = %q, err=%v", tokenTable, err)
	}
	user := &model.User{Username: "alice", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "uuid", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	channel := &model.NotificationChannel{OwnerUserID: user.ID, Name: "ops", Type: "telegram", Enabled: true, Events: "server_offline", ConfigJSON: `{}`}
	if err := db.CreateNotificationChannel(ctx, channel); err != nil {
		t.Fatal(err)
	}
	delivery := &model.NotificationDelivery{ChannelID: channel.ID, Event: "server_offline", EventKey: "server:4:offline:test", Title: "失联", Body: "服务器失联", ContextJSON: `{"server_id":4}`}
	inserted, err := db.QueueNotificationDelivery(ctx, delivery)
	if err != nil || !inserted || delivery.ContextJSON != `{"server_id":4}` {
		t.Fatalf("queue delivery inserted=%v delivery=%#v err=%v", inserted, delivery, err)
	}
	pending, err := db.ListPendingNotificationDeliveries(ctx, time.Now().UTC().Add(time.Minute), 10)
	if err != nil || len(pending) != 1 || pending[0].ContextJSON != `{"server_id":4}` {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	expires := time.Now().UTC().Add(time.Minute)
	if err := db.CreateTelegramActionToken(ctx, "hash", 10, 0, "view_server", `{"action":"view_server"}`, expires); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTelegramActionTokenMessages(ctx, []string{"hash"}, 22); err != nil {
		t.Fatal(err)
	}
	first, err := db.ConsumeTelegramActionToken(ctx, "hash", 10, time.Now().UTC())
	if err != nil || first.MessageID != 22 {
		t.Fatalf("consume=%#v err=%v", first, err)
	}
	if _, err := db.ConsumeTelegramActionToken(ctx, "hash", 10, time.Now().UTC()); err == nil {
		t.Fatal("consumed token was accepted again")
	}
}
