package plugin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

const MaxWebhookBodyBytes = 64 << 10
const WebhookWindow = 5 * time.Minute

var ErrWebhookAuthentication = errors.New("invalid webhook authentication")

func requireWebhookAdmin(actor application.Principal) error {
	if actor.Role != model.RoleAdmin || actor.UserID == nil || !actor.Interactive || actor.Type != model.APIPrincipalOAuth || actor.ClientName != "oboard-web" {
		return ErrPermissionDenied
	}
	return nil
}

func (s *Service) ListWebhooks(ctx context.Context, actor application.Principal, pluginID int64) ([]model.PluginWebhook, error) {
	if err := requireWebhookAdmin(actor); err != nil {
		return nil, err
	}
	return s.store.ListPluginWebhooks(ctx, pluginID)
}

// CreateWebhook returns the generated key once; ordinary reads never return it.
func (s *Service) CreateWebhook(ctx context.Context, actor application.Principal, bindingID, grantID int64, masterSecret string) (model.PluginWebhook, string, error) {
	if err := requireWebhookAdmin(actor); err != nil {
		return model.PluginWebhook{}, "", err
	}
	binding, err := s.store.GetPluginTrigger(ctx, bindingID)
	if err != nil {
		return model.PluginWebhook{}, "", err
	}
	item := model.PluginWebhook{PluginID: binding.PluginID, BindingID: binding.ID, BindingRevision: binding.BindingRevision, RevisionID: binding.RevisionID, GrantID: grantID, CreatedByUserID: *actor.UserID}
	if _, err = s.webhookBinding(ctx, item); err != nil {
		return model.PluginWebhook{}, "", err
	}
	item.ID, err = webhookRandom()
	if err != nil {
		return model.PluginWebhook{}, "", err
	}
	secret, encrypted, err := newWebhookSecret(masterSecret, item.ID)
	if err != nil {
		return model.PluginWebhook{}, "", err
	}
	item.SecretEncrypted = encrypted
	if err = s.store.CreatePluginWebhook(ctx, &item); err != nil {
		return model.PluginWebhook{}, "", err
	}
	return item, secret, nil
}

func (s *Service) ChangeWebhook(ctx context.Context, actor application.Principal, id string, generation int64, enabled, rotate bool, masterSecret string) (model.PluginWebhook, string, error) {
	if err := requireWebhookAdmin(actor); err != nil {
		return model.PluginWebhook{}, "", err
	}
	item, err := s.store.GetPluginWebhook(ctx, id)
	if err != nil {
		return model.PluginWebhook{}, "", err
	}
	if item.Generation != generation {
		return model.PluginWebhook{}, "", ErrConflict
	}
	if enabled {
		if _, err = s.webhookBinding(ctx, item); err != nil {
			return model.PluginWebhook{}, "", err
		}
	}
	secret, encrypted := "", ""
	if rotate {
		secret, encrypted, err = newWebhookSecret(masterSecret, id)
		if err != nil {
			return model.PluginWebhook{}, "", err
		}
	}
	if err = s.store.ChangePluginWebhook(ctx, item, enabled, encrypted); err != nil {
		return model.PluginWebhook{}, "", err
	}
	item, err = s.store.GetPluginWebhook(ctx, id)
	return item, secret, err
}

func (s *Service) webhookBinding(ctx context.Context, item model.PluginWebhook) (model.PluginTriggerBinding, error) {
	binding, err := s.store.GetPluginTrigger(ctx, item.BindingID)
	if err != nil {
		return binding, store.ErrPluginWebhookChanged
	}
	if binding.PluginID != item.PluginID || binding.RevisionID != item.RevisionID || binding.BindingRevision != item.BindingRevision || binding.Kind != model.PluginTriggerEvent {
		return binding, store.ErrPluginWebhookChanged
	}
	spec, err := ParseTriggerSpec(binding.SpecJSON)
	if err != nil || spec.Event != model.PluginEventWebhook {
		return binding, store.ErrPluginWebhookChanged
	}
	rev, err := s.store.GetPluginRevision(ctx, item.RevisionID)
	if err != nil || rev.PluginID != item.PluginID || rev.Status != model.PluginRevisionPublished {
		return binding, store.ErrPluginWebhookChanged
	}
	grant, err := s.store.GetPluginGrant(ctx, item.GrantID)
	if err != nil || grant.PluginID != item.PluginID || grant.RevisionID != item.RevisionID || grant.BindingID == nil || *grant.BindingID != binding.ID || grant.RevokedAt != nil || (grant.ExpiresAt != nil && !grant.ExpiresAt.After(s.now())) || grant.BindingDigest != BindingDigest(binding) || grant.SourceDigest != rev.SourceDigest {
		return binding, ErrApprovalRequired
	}
	return binding, nil
}

// ReceiveWebhook uses body only as schema-validated plugin parameters. Identity,
// environment, target scope, grant and revision always come from persisted state.
func (s *Service) ReceiveWebhook(ctx context.Context, id, timestamp, nonce, signature string, body []byte, masterSecret string) (model.PluginRun, error) {
	if len(body) > MaxWebhookBodyBytes {
		return model.PluginRun{}, Coded(codeLimitExceeded, "webhook body exceeds 64 KiB")
	}
	item, err := s.store.GetPluginWebhook(ctx, id)
	if err != nil || !item.Enabled {
		return model.PluginRun{}, ErrWebhookAuthentication
	}
	at := s.now()
	if err = s.store.AllowPluginWebhookAttempt(ctx, id, at); err != nil {
		return model.PluginRun{}, err
	}
	secret, err := security.DecryptSecret(masterSecret, model.PluginWebhookSecretPurpose(id), item.SecretEncrypted)
	if err != nil {
		return model.PluginRun{}, ErrWebhookAuthentication
	}
	signedAt, err := VerifyWebhookSignature(secret, id, timestamp, nonce, signature, body, at)
	if err != nil {
		return model.PluginRun{}, err
	}
	binding, err := s.webhookBinding(ctx, item)
	if err != nil {
		return model.PluginRun{}, err
	}
	rev, err := s.store.GetPluginRevision(ctx, item.RevisionID)
	if err != nil {
		return model.PluginRun{}, err
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil {
		return model.PluginRun{}, err
	}
	if !json.Valid(body) {
		return model.PluginRun{}, Coded(codeInvalidInput, "webhook body must be JSON")
	}
	if err = ValidateParams(manifest.Params, body); err != nil {
		return model.PluginRun{}, err
	}
	// The insertion guard rejects a concurrent grant/endpoint/binding change.
	grants, err := s.store.ListActivePluginGrants(ctx, item.PluginID, item.RevisionID, &binding.ID)
	if err != nil {
		return model.PluginRun{}, err
	}
	if len(grants) == 0 || grants[0].ID != item.GrantID {
		return model.PluginRun{}, ErrApprovalRequired
	}
	if err = s.store.ClaimPluginWebhookDelivery(ctx, item, nonce, at, signedAt.Add(WebhookWindow+time.Second)); err != nil {
		return model.PluginRun{}, err
	}
	binding.ParamsJSON = append(json.RawMessage(nil), body...)
	run, created, err := s.EnqueueTriggerRun(ctx, binding, "webhook:"+id+":"+nonce, model.PluginEventWebhook, map[string]any{
		"webhook_id": id, "webhook_generation": item.Generation, "webhook_nonce": nonce,
		"event": model.PluginEventWebhook,
	})
	if err != nil || !created {
		_ = s.store.FinishPluginWebhookDelivery(ctx, id, nonce, 0)
		if err != nil {
			return model.PluginRun{}, err
		}
		return model.PluginRun{}, store.ErrPluginWebhookChanged
	}
	if err = s.store.FinishPluginWebhookDelivery(ctx, id, nonce, run.ID); err != nil {
		return model.PluginRun{}, err
	}
	return run, nil
}

// WebhookSignature is hex HMAC-SHA256 over the domain, endpoint, Unix seconds,
// nonce and exact request body, separated by newlines. The key is the literal
// one-time returned secret string (not its hex-decoded representation).
func WebhookSignature(secret, id, timestamp, nonce string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "oboard-plugin-webhook-v1\n%s\n%s\n%s\n", id, timestamp, nonce)
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func VerifyWebhookSignature(secret, id, timestamp, nonce, signature string, body []byte, at time.Time) (time.Time, error) {
	invalid := func() (time.Time, error) { return time.Time{}, ErrWebhookAuthentication }
	if len(body) > MaxWebhookBodyBytes || len(timestamp) > 12 || len(nonce) != 32 || len(signature) != 64 {
		return invalid()
	}
	if _, err := hex.DecodeString(nonce); err != nil {
		return invalid()
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || strconv.FormatInt(seconds, 10) != timestamp {
		return invalid()
	}
	signedAt := time.Unix(seconds, 0).UTC()
	if signedAt.Before(at.Add(-WebhookWindow)) || signedAt.After(at.Add(WebhookWindow)) {
		return invalid()
	}
	supplied, err := hex.DecodeString(signature)
	if err != nil {
		return invalid()
	}
	expected, _ := hex.DecodeString(WebhookSignature(secret, id, timestamp, nonce, body))
	if !hmac.Equal(supplied, expected) {
		return invalid()
	}
	return signedAt, nil
}

func webhookRandom() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
func newWebhookSecret(masterSecret, id string) (string, string, error) {
	if masterSecret == "" {
		return "", "", errors.New("webhook encryption is unavailable")
	}
	secret, err := webhookRandom()
	if err != nil {
		return "", "", err
	}
	encrypted, err := security.EncryptSecret(masterSecret, model.PluginWebhookSecretPurpose(id), secret)
	return secret, encrypted, err
}
