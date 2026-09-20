package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func webhookFixture(t *testing.T) (*Service, application.Principal, model.PluginWebhook, string) {
	t.Helper()
	db, svc, user := testPluginEnv(t)
	ctx := context.Background()
	if err := db.SetSettings(ctx, map[string]string{model.PluginSettingEnabled: "true", model.PluginSettingSchedulerPaused: "false"}); err != nil {
		t.Fatal(err)
	}
	actor := adminActor(user)
	p, err := svc.CreatePlugin(ctx, actor, "webhook", "test")
	if err != nil {
		t.Fatal(err)
	}
	p, err = svc.UpdatePlugin(ctx, actor, p.ID, p.Name, p.Description, model.PluginStatusEnabled, "")
	if err != nil {
		t.Fatal(err)
	}
	manifest := model.PluginManifest{SchemaVersion: 1, Runtime: model.PluginRuntimeOBoardJSv1, SDKVersion: model.PluginSDKVersionV1, Entry: "main", Capabilities: []string{model.PluginSDKServersGet}, Params: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"message":{"type":"string"}},"required":["message"]}`)}
	rev, err := svc.SaveDraft(ctx, actor, p.ID, "function main(ctx) { return ctx.params; }", MustJSON(manifest))
	if err != nil {
		t.Fatal(err)
	}
	rev, err = svc.Publish(ctx, actor, p.ID, rev.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := model.PluginTriggerBinding{PluginID: p.ID, RevisionID: rev.ID, Name: "webhook", Kind: model.PluginTriggerEvent, SpecJSON: json.RawMessage(`{"timezone":"UTC","event":"plugin.webhook"}`), ParamsJSON: json.RawMessage(`{"message":"fixed"}`), EnvJSON: json.RawMessage(`{}`), Enabled: true, CreatedByUserID: user.ID}
	if err = db.CreatePluginTrigger(ctx, &binding); err != nil {
		t.Fatal(err)
	}
	grant, err := svc.CreateGrant(ctx, actor, model.PluginGrant{PluginID: p.ID, RevisionID: rev.ID, BindingID: &binding.ID, CapabilitiesJSON: MustJSON(manifest.Capabilities), ResourceScopeJSON: json.RawMessage(`{"servers":{"mode":"none"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	item, key, err := svc.CreateWebhook(ctx, actor, binding.ID, grant.ID, "master")
	if err != nil {
		t.Fatal(err)
	}
	if item.Enabled {
		t.Fatal("new endpoints must be disabled")
	}
	item, _, err = svc.ChangeWebhook(ctx, actor, item.ID, item.Generation, true, false, "master")
	if err != nil {
		t.Fatal(err)
	}
	return svc, actor, item, key
}

func receiveWebhook(s *Service, item model.PluginWebhook, key, nonce, body string) (model.PluginRun, error) {
	ts := strconv.FormatInt(s.now().Unix(), 10)
	return s.ReceiveWebhook(context.Background(), item.ID, ts, nonce, WebhookSignature(key, item.ID, ts, nonce, []byte(body)), []byte(body), "master")
}

func TestWebhookSignatureContract(t *testing.T) {
	at := time.Unix(1800000000, 0)
	ts := strconv.FormatInt(at.Unix(), 10)
	nonce := strings.Repeat("a", 32)
	body := []byte(`{"hello":"world"}`)
	sig := WebhookSignature("key", "endpoint", ts, nonce, body)
	// Fixed independent Python hmac contract vector.
	const expected = "ddb2cdcb379de5dc2778b8b0a1e98f2260e44699dea0212064c2a260a893c0bf"
	if sig != expected {
		t.Fatalf("signature changed: %s", sig)
	}
	if _, err := VerifyWebhookSignature("key", "endpoint", ts, nonce, sig, body, at); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, key, id, ts, nonce, sig string
		body                          []byte
		at                            time.Time
	}{
		{"body", "key", "endpoint", ts, nonce, sig, []byte(`{}`), at},
		{"endpoint", "key", "other", ts, nonce, sig, body, at},
		{"key", "wrong", "endpoint", ts, nonce, sig, body, at},
		{"expired", "key", "endpoint", ts, nonce, sig, body, at.Add(WebhookWindow + time.Second)},
		{"future", "key", "endpoint", ts, nonce, sig, body, at.Add(-WebhookWindow - time.Second)},
		{"nonce", "key", "endpoint", ts, "bad", sig, body, at},
		{"timestamp", "key", "endpoint", "0" + ts, nonce, sig, body, at},
		{"oversize", "key", "endpoint", ts, nonce, sig, make([]byte, MaxWebhookBodyBytes+1), at},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyWebhookSignature(tc.key, tc.id, tc.ts, tc.nonce, tc.sig, tc.body, tc.at); !errors.Is(err, ErrWebhookAuthentication) {
				t.Fatalf("accepted bad signature: %v", err)
			}
		})
	}
}

func TestWebhookQueuesFixedAuthorityAndRejectsReplay(t *testing.T) {
	svc, _, item, key := webhookFixture(t)
	nonce := strings.Repeat("1", 32)
	run, err := receiveWebhook(svc, item, key, nonce, `{"message":"external"}`)
	if err != nil {
		t.Fatal(err)
	}
	if run.PluginID != item.PluginID || run.RevisionID != item.RevisionID || run.BindingID == nil || *run.BindingID != item.BindingID || run.GrantID == nil || *run.GrantID != item.GrantID {
		t.Fatalf("changed authority: %+v", run)
	}
	var snapshot map[string]json.RawMessage
	if err = json.Unmarshal(run.SnapshotJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	if string(snapshot["params"]) != `{"message":"external"}` || snapshot["subject_server_id"] != nil || snapshot["target_server_id"] != nil {
		t.Fatalf("bad snapshot: %s", run.SnapshotJSON)
	}
	if _, err = receiveWebhook(svc, item, key, nonce, `{"message":"external"}`); !errors.Is(err, store.ErrPluginWebhookReplay) {
		t.Fatalf("replay: %v", err)
	}
	// A new service process uses persisted delivery state, not an in-memory set.
	other := NewService(svc.store, nil)
	if _, err = receiveWebhook(other, item, key, nonce, `{"message":"external"}`); !errors.Is(err, store.ErrPluginWebhookReplay) {
		t.Fatalf("restart replay: %v", err)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), key) || strings.Contains(string(raw), item.SecretEncrypted) {
		t.Fatal("secret leaked")
	}
}

func TestWebhookRejectsInputAndAuthorityChanges(t *testing.T) {
	for _, kind := range []string{"schema", "binding", "binding_disabled", "grant", "new_grant", "disabled", "plugin_disabled", "runtime_disabled", "paused"} {
		t.Run(kind, func(t *testing.T) {
			svc, actor, item, key := webhookFixture(t)
			ctx := context.Background()
			body := `{"message":"ok"}`
			switch kind {
			case "schema":
				body = `{"message":"ok","grant_id":999,"target_server_id":999}`
			case "binding":
				b, _ := svc.store.GetPluginTrigger(ctx, item.BindingID)
				b.ParamsJSON = json.RawMessage(`{"message":"changed"}`)
				if err := svc.store.UpdatePluginTrigger(ctx, &b, b.BindingRevision); err != nil {
					t.Fatal(err)
				}
			case "binding_disabled":
				b, err := svc.store.GetPluginTrigger(ctx, item.BindingID)
				if err != nil {
					t.Fatal(err)
				}
				b.Enabled = false
				if err := svc.store.UpdatePluginTrigger(ctx, &b, b.BindingRevision); err != nil {
					t.Fatal(err)
				}
			case "grant":
				if err := svc.store.RevokePluginGrant(ctx, item.GrantID); err != nil {
					t.Fatal(err)
				}
			case "new_grant":
				g, _ := svc.store.GetPluginGrant(ctx, item.GrantID)
				g.ID = 0
				if _, err := svc.CreateGrant(ctx, actor, g); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if _, _, err := svc.ChangeWebhook(ctx, actor, item.ID, item.Generation, false, false, "master"); err != nil {
					t.Fatal(err)
				}
			case "plugin_disabled":
				p, err := svc.store.GetPlugin(ctx, item.PluginID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := svc.UpdatePlugin(ctx, actor, p.ID, p.Name, p.Description, model.PluginStatusDisabled, ""); err != nil {
					t.Fatal(err)
				}
			case "runtime_disabled":
				if err := svc.store.SetSettings(ctx, map[string]string{model.PluginSettingEnabled: "false"}); err != nil {
					t.Fatal(err)
				}
			case "paused":
				if err := svc.store.SetSettings(ctx, map[string]string{model.PluginSettingSchedulerPaused: "true"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := receiveWebhook(svc, item, key, strings.Repeat("2", 32), body); err == nil {
				t.Fatal("unsafe delivery accepted")
			}
			if count, err := svc.store.CountActivePluginRuns(ctx, item.PluginID); err != nil || count != 0 {
				t.Fatalf("queued invalid delivery: %d %v", count, err)
			}
		})
	}
}

func TestWebhookRotationCancelsRunsAndInvalidatesOldKey(t *testing.T) {
	svc, actor, item, key := webhookFixture(t)
	run, err := receiveWebhook(svc, item, key, strings.Repeat("3", 32), `{"message":"ok"}`)
	if err != nil {
		t.Fatal(err)
	}
	next, nextKey, err := svc.ChangeWebhook(context.Background(), actor, item.ID, item.Generation, true, true, "master")
	if err != nil {
		t.Fatal(err)
	}
	if nextKey == key || next.Generation <= item.Generation {
		t.Fatal("key was not rotated")
	}
	old, err := svc.store.GetPluginRun(context.Background(), run.ID)
	if err != nil || old.Status != model.PluginRunCancelled {
		t.Fatalf("run not cancelled: %+v %v", old, err)
	}
	if _, err = receiveWebhook(svc, next, key, strings.Repeat("4", 32), `{"message":"ok"}`); !errors.Is(err, ErrWebhookAuthentication) {
		t.Fatalf("old key accepted: %v", err)
	}
	if _, err = receiveWebhook(svc, next, nextKey, strings.Repeat("4", 32), `{"message":"ok"}`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = svc.ChangeWebhook(context.Background(), actor, item.ID, item.Generation, false, false, "master"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale management write: %v", err)
	}
}

func TestWebhookPersistentRateLimitAndRestore(t *testing.T) {
	svc, _, item, key := webhookFixture(t)
	at := time.Now().UTC().Truncate(time.Minute).Add(time.Second)
	svc.now = func() time.Time { return at }
	for i := 0; i < 60; i++ {
		if _, err := receiveWebhook(svc, item, "wrong", fmt.Sprintf("%032x", i), `{"message":"ok"}`); !errors.Is(err, ErrWebhookAuthentication) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	next := NewService(svc.store, nil)
	next.now = svc.now
	if _, err := receiveWebhook(next, item, key, strings.Repeat("5", 32), `{"message":"ok"}`); !errors.Is(err, store.ErrPluginWebhookRate) {
		t.Fatalf("lost durable limit: %v", err)
	}
	at = at.Add(time.Minute)
	if _, err := receiveWebhook(next, item, key, strings.Repeat("5", 32), `{"message":"ok"}`); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.RestorePluginWebhooks(context.Background(), "wrong", "new"); err == nil {
		t.Fatal("wrong recovery password accepted")
	}
	before, _ := svc.store.GetPluginWebhook(context.Background(), item.ID)
	if !before.Enabled || before.Generation != item.Generation {
		t.Fatal("failed rewrap changed state")
	}
	if err := svc.store.RestorePluginWebhooks(context.Background(), "master", "new"); err != nil {
		t.Fatal(err)
	}
	restored, err := svc.store.GetPluginWebhook(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := security.DecryptSecret("new", model.PluginWebhookSecretPurpose(item.ID), restored.SecretEncrypted)
	if err != nil || plain != key || restored.Enabled || restored.Generation <= item.Generation {
		t.Fatalf("unsafe restore: %+v %v", restored, err)
	}
	if _, err = security.DecryptSecret("master", model.PluginWebhookSecretPurpose(item.ID), restored.SecretEncrypted); err == nil {
		t.Fatal("old encryption key retained")
	}
}

func TestWebhookAdminOnlyAndInsertionGuard(t *testing.T) {
	svc, actor, item, _ := webhookFixture(t)
	ctx := context.Background()
	for _, kind := range []string{"operator", "machine", "mcp", "plugin"} {
		other := actor
		switch kind {
		case "operator":
			other.Role = model.RoleOperator
		case "machine":
			other.Interactive = false
		case "mcp":
			other.ClientName = "external-mcp"
		case "plugin":
			other.Type = model.APIPrincipalPlugin
		}
		if _, err := svc.ListWebhooks(ctx, other, item.PluginID); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("%s can list: %v", kind, err)
		}
		if _, _, err := svc.CreateWebhook(ctx, other, item.BindingID, item.GrantID, "master"); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("%s can create: %v", kind, err)
		}
		if _, _, err := svc.ChangeWebhook(ctx, other, item.ID, item.Generation, true, true, "master"); !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("%s can rotate: %v", kind, err)
		}
	}
	// Reproduce a grant change between preflight and the atomic run insert.
	nonce := strings.Repeat("6", 32)
	if err := svc.store.ClaimPluginWebhookDelivery(ctx, item, nonce, time.Now(), time.Now().Add(WebhookWindow)); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.RevokePluginGrant(ctx, item.GrantID); err != nil {
		t.Fatal(err)
	}
	run := model.PluginRun{UUID: "guard", PluginID: item.PluginID, RevisionID: item.RevisionID, BindingID: &item.BindingID, GrantID: &item.GrantID, TriggerKind: model.PluginEventWebhook, Status: model.PluginRunQueued, Mode: model.PluginRunModeLive, IdempotencyKey: "guard", SnapshotJSON: MustJSON(map[string]any{"webhook_id": item.ID, "webhook_generation": item.Generation, "webhook_nonce": nonce})}
	if err := svc.store.CreatePluginRun(ctx, &run); err == nil {
		t.Fatal("revocation race queued a run")
	}
}
