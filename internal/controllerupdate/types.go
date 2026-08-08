package controllerupdate

import "time"

const (
	DefaultSocketPath = "/run/oboard/controller-updater.sock"
	ManifestName      = "controller-release-manifest.json"
	ManifestSchema    = 1
)

type Artifact struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type ChannelRequest struct {
	Channel string `json:"channel"`
}

type Manifest struct {
	Schema    int        `json:"schema"`
	Channel   string     `json:"channel"`
	Version   string     `json:"version"`
	Build     string     `json:"build"`
	Commit    string     `json:"commit"`
	Date      string     `json:"date"`
	Artifacts []Artifact `json:"artifacts"`
}

type BuildInfo struct {
	Version string `json:"version"`
	Build   string `json:"build"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type Status struct {
	Channel                 string    `json:"channel"`
	Current                 BuildInfo `json:"current"`
	Available               BuildInfo `json:"available"`
	UpdateAvailable         bool      `json:"update_available"`
	AutoUpdateEnabled       bool      `json:"auto_update_enabled"`
	AutoUpdateIntervalHours int       `json:"auto_update_interval_hours"`
	CanCancel               bool      `json:"can_cancel"`
	State                   string    `json:"status"`
	LastCheckedAt           string    `json:"last_checked_at,omitempty"`
	LastError               string    `json:"last_error,omitempty"`
	BackupPath              string    `json:"backup_path,omitempty"`
	ManualCommand           string    `json:"manual_command,omitempty"`
}

func (s Status) CheckedAt() time.Time {
	value, _ := time.Parse(time.RFC3339Nano, s.LastCheckedAt)
	return value
}
