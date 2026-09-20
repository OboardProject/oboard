package model

import (
	"encoding/json"
	"time"
)

// PluginInstallation binds a stable package identity to the existing execution identity.
type PluginInstallation struct {
	PluginID         int64           `json:"plugin_id"`
	PackageID        string          `json:"package_id"`
	ActiveRevisionID int64           `json:"active_revision_id"`
	Installed        bool            `json:"installed"`
	ConfigJSON       json.RawMessage `json:"config"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type PluginPackageVersion struct {
	PluginID         int64           `json:"plugin_id"`
	RevisionID       int64           `json:"revision_id"`
	Version          string          `json:"version"`
	SHA256           string          `json:"sha256"`
	ManifestJSON     json.RawMessage `json:"manifest"`
	UIJSON           json.RawMessage `json:"ui"`
	SourceKind       string          `json:"source_kind"`
	SourceRepository string          `json:"source_repository"`
	SourceCommit     string          `json:"source_commit"`
	CreatedAt        time.Time       `json:"created_at"`
}

type PluginPackageMetadata struct {
	PluginID    string `json:"plugin_id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
}

type PluginCapabilityDiff struct {
	Added     []string `json:"added"`
	Removed   []string `json:"removed"`
	Unchanged []string `json:"unchanged"`
}

type PluginPackageSource struct {
	Kind       string `json:"kind"`
	Repository string `json:"repository,omitempty"`
	Commit     string `json:"commit,omitempty"`
}

type PluginPackagePreview struct {
	Source           *PluginPackageSource  `json:"source,omitempty"`
	Metadata         PluginPackageMetadata `json:"metadata"`
	SHA256           string                `json:"sha256"`
	ExistingPluginID int64                 `json:"existing_plugin_id"`
	ActiveRevisionID int64                 `json:"active_revision_id"`
	Capabilities     PluginCapabilityDiff  `json:"capabilities"`
	SourceCompiled   bool                  `json:"source_compiled"`
	HasUI            bool                  `json:"has_ui"`
}

type PluginPackageInstallResult struct {
	Installation PluginInstallation   `json:"installation"`
	Version      PluginPackageVersion `json:"version"`
	Capabilities PluginCapabilityDiff `json:"capabilities"`
}
