package controller

import (
	"context"
	"net/http"

	"github.com/OboardProject/oboard/internal/store"
)

// auditWindowHours resolves the window an audit request may ask for.
//
// The console used to offer 7 and 30 day ranges against a fixed 30-day raw
// retention. Now that retention is an operator setting, a request for more than
// the database keeps would quietly answer from whatever survived the last
// purge, which reads as "there was less activity" rather than "that history is
// gone". Clamping to retention keeps the answer honest, and the response
// carries the window that was actually used.
func (s *Server) auditWindowHours(ctx context.Context, r *http.Request, fallback int) int {
	hours := intQuery(r, "window_hours", fallback)
	if hours < 1 {
		hours = fallback
	}
	maxHours := store.ConnectionAuditRetentionDays(s.runtimeSettings(ctx)) * 24
	if maxHours < 1 {
		maxHours = store.DefaultConnectionAuditRetentionDays * 24
	}
	if hours > maxHours {
		return maxHours
	}
	return hours
}
