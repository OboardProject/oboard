package model

import "time"

const PluginEventWebhook = "plugin.webhook"

// PluginWebhook binds an opaque public endpoint to one administrator-approved trigger.
type PluginWebhook struct {
	ID              string    `json:"id"`
	PluginID        int64     `json:"plugin_id"`
	BindingID       int64     `json:"binding_id"`
	BindingRevision int64     `json:"binding_revision"`
	RevisionID      int64     `json:"revision_id"`
	GrantID         int64     `json:"grant_id"`
	Enabled         bool      `json:"enabled"`
	Generation      int64     `json:"generation"`
	SecretEncrypted string    `json:"-"`
	CreatedByUserID int64     `json:"created_by_user_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func PluginWebhookSecretPurpose(id string) string { return "plugin-webhook:" + id }
