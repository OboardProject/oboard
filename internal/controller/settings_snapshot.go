package controller

import (
	"context"
)

// settingsSnapshot is the in-memory ListSettings cache. The store bumps a
// process-local revision on every settings write; the snapshot reloads only
// when that revision changes, so per-health-report audit and traffic-window
// evaluation stop issuing a settings query per message.
type settingsSnapshot struct {
	revision int64
	values   map[string]string
}

// settings returns the current settings map, reloading from SQLite only when
// the store revision advanced or on first use.
func (s *Server) runtimeSettings(ctx context.Context) map[string]string {
	revision := s.store.SettingsRevision()
	if current := s.settingsCache.Load(); current != nil && current.revision == revision {
		return current.values
	}
	values, err := s.store.ListSettings(ctx)
	if err != nil {
		// Keep serving the last known snapshot instead of failing the hot
		// path; the next call retries while the revision is unchanged.
		if current := s.settingsCache.Load(); current != nil {
			return current.values
		}
		return map[string]string{}
	}
	s.settingsCache.Store(&settingsSnapshot{revision: revision, values: values})
	return values
}

// invalidateSettingsSnapshot drops the cached settings so the next read goes
// to SQLite even without a store revision bump.
func (s *Server) invalidateSettingsSnapshot() {
	s.settingsCache.Store(nil)
}
