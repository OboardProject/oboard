package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func openHotPathDedupStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "hotpath-dedup.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createHotPathDedupServer(t *testing.T, db *store.Store, name, agentID string) *model.Server {
	t.Helper()
	server := &model.Server{
		Name: name, AgentID: agentID, AgentTokenHash: security.HashSecret(agentID + "-token"),
		Status: model.ServerOnline, LatencyProbeEnabled: true, LatencyProbeMode: model.LatencyProbeModeTCP,
	}
	if err := db.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	fresh, err := db.GetServer(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	return fresh
}

// TestRemoteAccessStatusWritesOnlyOnContentChange proves the steady-state
// heartbeat stops rewriting an identical capability row, and that every input
// that really changes the stored content still reaches the database.
func TestRemoteAccessStatusWritesOnlyOnContentChange(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	server := createHotPathDedupServer(t, db, "dedup-node", "dedup-agent")

	report := model.RemoteAccessReport{
		Capabilities: []string{model.RemoteAccessCapabilityInteractiveMCP, model.RemoteAccessCapabilityExec},
		LocalMode:    model.RemoteAccessModeStandard,
		LocalAllow:   model.RemoteAccessLocalAllow{RemoteTerminal: true},
	}
	for range 1000 {
		if err := srv.persistRemoteAccessStatus(ctx, server.ID, server.AgentID, report); err != nil {
			t.Fatal(err)
		}
	}
	if written := srv.hotPath.remoteAccessWritten.Load(); written != 1 {
		t.Fatalf("expected exactly one capability write for 1000 identical reports, got %d", written)
	}
	if skipped := srv.hotPath.remoteAccessSkipped.Load(); skipped != 999 {
		t.Fatalf("expected 999 skipped writes, got %d", skipped)
	}

	// Capabilities are a set: a re-ordered report is the same content.
	reordered := report
	reordered.Capabilities = []string{model.RemoteAccessCapabilityExec, model.RemoteAccessCapabilityInteractiveMCP}
	if err := srv.persistRemoteAccessStatus(ctx, server.ID, server.AgentID, reordered); err != nil {
		t.Fatal(err)
	}
	if written := srv.hotPath.remoteAccessWritten.Load(); written != 1 {
		t.Fatalf("re-ordered capability set must not be a content change, got %d writes", written)
	}

	// A Hardened local deny is a content change and must be persisted at once.
	hardened := report
	hardened.LocalMode = model.RemoteAccessModeHardened
	hardened.LocalAllow = model.RemoteAccessLocalAllow{}
	if err := srv.persistRemoteAccessStatus(ctx, server.ID, server.AgentID, hardened); err != nil {
		t.Fatal(err)
	}
	if written := srv.hotPath.remoteAccessWritten.Load(); written != 2 {
		t.Fatalf("local mode change was not persisted, writes=%d", written)
	}
	status, err := db.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.LocalMode != model.RemoteAccessModeHardened || status.LocalAllow.RemoteTerminal {
		t.Fatalf("hardened state not stored: %+v", status)
	}

	// Losing a capability is a content change too.
	reduced := hardened
	reduced.Capabilities = []string{model.RemoteAccessCapabilityExec}
	if err := srv.persistRemoteAccessStatus(ctx, server.ID, server.AgentID, reduced); err != nil {
		t.Fatal(err)
	}
	if written := srv.hotPath.remoteAccessWritten.Load(); written != 3 {
		t.Fatalf("capability removal was not persisted, writes=%d", written)
	}

	// A re-enrolled Agent never inherits the previous Agent's committed state.
	if err := srv.persistRemoteAccessStatus(ctx, server.ID, "replacement-agent", reduced); err != nil {
		t.Fatal(err)
	}
	if written := srv.hotPath.remoteAccessWritten.Load(); written != 4 {
		t.Fatalf("agent identity replacement must re-persist, writes=%d", written)
	}

	// A deleted server leaves nothing behind.
	srv.forgetRemoteAccessStatus(server.ID)
	if err := srv.persistRemoteAccessStatus(ctx, server.ID, "replacement-agent", reduced); err != nil {
		t.Fatal(err)
	}
	if written := srv.hotPath.remoteAccessWritten.Load(); written != 5 {
		t.Fatalf("forgotten server must be persisted from scratch, writes=%d", written)
	}
}

// TestRemoteAccessStatusUpdatedAtTracksContent proves the stored timestamp means
// "capability last changed" rather than "Agent reported again".
func TestRemoteAccessStatusUpdatedAtTracksContent(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	server := createHotPathDedupServer(t, db, "timestamp-node", "timestamp-agent")
	report := model.RemoteAccessReport{Capabilities: []string{model.RemoteAccessCapabilityExec}, LocalMode: model.RemoteAccessModeStandard}
	if err := db.UpsertServerRemoteAccessStatus(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	first, err := db.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertServerRemoteAccessStatus(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	second, err := db.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !second.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("identical report advanced updated_at: %s -> %s", first.UpdatedAt, second.UpdatedAt)
	}
	report.Capabilities = []string{model.RemoteAccessCapabilityExec, model.RemoteAccessCapabilityInteractiveMCP}
	if err := db.UpsertServerRemoteAccessStatus(ctx, server.ID, report); err != nil {
		t.Fatal(err)
	}
	third, err := db.GetServerRemoteAccessStatus(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !third.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("content change did not advance updated_at: %s", third.UpdatedAt)
	}
}

// TestPresenceClearRunsOnTransitionNotOnEveryHeartbeat proves a server whose
// audit stays off stops issuing a DELETE per heartbeat while still cleaning up
// on every real transition.
func TestPresenceClearRunsOnTransitionNotOnEveryHeartbeat(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	srv := newTestServer(db, "hotpath-secret", "")
	server := createHotPathDedupServer(t, db, "presence-node", "presence-agent")

	// First observation after Controller start is the recovery check.
	srv.syncConnectionAuditPresence(ctx, server, false)
	if performed := srv.hotPath.presenceClearPerformed.Load(); performed != 1 {
		t.Fatalf("first disabled observation must clean up, performed=%d", performed)
	}
	for range 500 {
		srv.syncConnectionAuditPresence(ctx, server, false)
	}
	if performed := srv.hotPath.presenceClearPerformed.Load(); performed != 1 {
		t.Fatalf("audit staying off must not clean up again, performed=%d", performed)
	}
	if skipped := srv.hotPath.presenceClearSkipped.Load(); skipped != 500 {
		t.Fatalf("expected 500 skipped cleanups, got %d", skipped)
	}

	// enabled -> disabled is a transition and cleans up again.
	srv.syncConnectionAuditPresence(ctx, server, true)
	srv.syncConnectionAuditPresence(ctx, server, false)
	if performed := srv.hotPath.presenceClearPerformed.Load(); performed != 2 {
		t.Fatalf("re-disabling must clean up, performed=%d", performed)
	}

	// A replaced Agent identity invalidates the completed cleanup.
	replaced := *server
	replaced.AgentID = "presence-agent-2"
	srv.syncConnectionAuditPresence(ctx, &replaced, false)
	if performed := srv.hotPath.presenceClearPerformed.Load(); performed != 3 {
		t.Fatalf("agent replacement must clean up, performed=%d", performed)
	}

	// A deleted server leaves no per-server state or lock behind.
	srv.forgetPresenceAuditState(server.ID)
	srv.presenceAudit.mu.Lock()
	remaining := len(srv.presenceAudit.servers)
	srv.presenceAudit.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("presence audit state leaked for %d servers", remaining)
	}
}

// TestPresenceReadsExcludeDisabledServerImmediately proves a disabled server
// stops contributing presence to risk evaluation without waiting for the
// physical delete.
func TestPresenceReadsExcludeDisabledServerImmediately(t *testing.T) {
	ctx := context.Background()
	db := openHotPathDedupStore(t)
	server := createHotPathDedupServer(t, db, "read-node", "read-agent")
	server.ConnectionAuditEnabled = true
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "presence-account", PasswordHash: "hash", Role: model.RoleViewer, Status: "active", ProxyUUID: "11111111-1111-4111-8111-111111111199", ProxyPassword: "password"}
	if err := db.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	event := model.ConnectionPresenceEvent{
		Sequence: 1, ServerID: server.ID, UserID: user.ID, InboundID: 7, SourceIP: "198.51.100.9",
		Network: "tcp", Event: "first_meaningful_payload", State: "active", ActiveConnections: 1,
		Meaningful: true, PayloadLastAt: time.Now().UTC(), At: time.Now().UTC(),
	}
	if _, err := db.ApplyConnectionPresenceEvents(ctx, server.AgentID, server.ID, 0, []model.ConnectionPresenceEvent{event}); err != nil {
		t.Fatal(err)
	}
	presence, err := db.ListConnectionPresenceForUser(ctx, user.ID, event.At.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(presence) != 1 {
		t.Fatalf("expected the enabled server's presence to be visible, got %d", len(presence))
	}

	server.ConnectionAuditEnabled = false
	if err := db.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	presence, err = db.ListConnectionPresenceForUser(ctx, user.ID, event.At.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(presence) != 0 {
		t.Fatalf("disabled server still contributed %d presence rows before the physical delete", len(presence))
	}
}
