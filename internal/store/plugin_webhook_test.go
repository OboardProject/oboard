package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestWebhookDeliveryAndRateSurviveDatabaseReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/webhook.sqlite"
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	user := model.User{Username: "webhook-admin", PasswordHash: "hash", Role: model.RoleAdmin, Status: "active", ProxyUUID: "webhook-uuid", ProxyPassword: "test"}
	if err = db.CreateUser(ctx, &user); err != nil {
		t.Fatal(err)
	}
	p := model.Plugin{Name: "hook", OwnerUserID: user.ID, Status: model.PluginStatusEnabled}
	if err = db.CreatePlugin(ctx, &p); err != nil {
		t.Fatal(err)
	}
	rev := model.PluginRevision{PluginID: p.ID, Status: model.PluginRevisionDraft, SchemaVersion: 1, Runtime: model.PluginRuntimeOBoardJSv1, SDKVersion: model.PluginSDKVersionV1, Source: "function main(){}", SourceDigest: "digest", ManifestJSON: json.RawMessage(`{}`), AuthorUserID: user.ID}
	if err = db.SavePluginDraft(ctx, &rev); err != nil {
		t.Fatal(err)
	}
	rev, err = db.PublishPluginRevision(ctx, p.ID, rev.ID, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := model.PluginTriggerBinding{PluginID: p.ID, RevisionID: rev.ID, Name: "hook", Enabled: true, Kind: model.PluginTriggerEvent, SpecJSON: json.RawMessage(`{"timezone":"UTC","event":"plugin.webhook"}`), CreatedByUserID: user.ID}
	if err = db.CreatePluginTrigger(ctx, &binding); err != nil {
		t.Fatal(err)
	}
	grant := model.PluginGrant{PluginID: p.ID, RevisionID: rev.ID, BindingID: &binding.ID, CapabilitiesJSON: json.RawMessage(`[]`), ResourceScopeJSON: json.RawMessage(`{}`), ApprovedByUserID: user.ID}
	if err = db.CreatePluginGrant(ctx, &grant); err != nil {
		t.Fatal(err)
	}
	item := model.PluginWebhook{ID: "endpoint", PluginID: p.ID, BindingID: binding.ID, BindingRevision: binding.BindingRevision, RevisionID: rev.ID, GrantID: grant.ID, SecretEncrypted: "encrypted", CreatedByUserID: user.ID}
	if err = db.CreatePluginWebhook(ctx, &item); err != nil {
		t.Fatal(err)
	}
	if err = db.ChangePluginWebhook(ctx, item, true, ""); err != nil {
		t.Fatal(err)
	}
	item, err = db.GetPluginWebhook(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for i := 0; i < 60; i++ {
		if err = db.AllowPluginWebhookAttempt(ctx, item.ID, at); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.ClaimPluginWebhookDelivery(ctx, item, "nonce", at, at.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ClaimPluginWebhookDelivery(ctx, item, "nonce", at, at.Add(5*time.Minute)); !errors.Is(err, ErrPluginWebhookReplay) {
		t.Fatalf("lost replay state: %v", err)
	}
	if err = db.AllowPluginWebhookAttempt(ctx, item.ID, at); !errors.Is(err, ErrPluginWebhookRate) {
		t.Fatalf("lost rate limit: %v", err)
	}
	if err = db.AllowPluginWebhookAttempt(ctx, item.ID, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = db.ChangePluginWebhook(ctx, item, false, ""); err != nil {
		t.Fatal(err)
	}
	if err = db.ClaimPluginWebhookDelivery(ctx, item, "other", at, at.Add(time.Minute)); !errors.Is(err, ErrPluginWebhookChanged) {
		t.Fatalf("stale generation accepted: %v", err)
	}
}
