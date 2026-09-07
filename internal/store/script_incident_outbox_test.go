package store

import (
	"context"
	"testing"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func TestNodeIncidentOutboxIsIndependentOfNotifyAndDoesNotRefireSameIncident(t *testing.T) {
	ctx := context.Background()
	s, err := Open(t.TempDir() + "/script-incidents.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	server := &model.Server{Name: "offline-node", AgentID: "agent-script-1", Status: model.ServerOffline}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)
	first, created, err := s.OpenOrReopenNodeIncident(ctx, *server, now.Add(-2*time.Minute), now, time.Minute, 30*time.Second, `{"notify":false}`)
	if err != nil || !created || first.ID == 0 {
		t.Fatalf("create incident: %#v created=%v err=%v", first, created, err)
	}
	events, err := s.ClaimScriptEvents(ctx, "test", now.Add(time.Minute), 16)
	if err != nil || len(events) != 1 || events[0].Topic != "script.server.offline" {
		t.Fatalf("first offline outbox: %#v err=%v", events, err)
	}
	if err := s.CompleteScriptEvent(ctx, events[0].ID); err != nil {
		t.Fatal(err)
	}
	second, created, err := s.OpenOrReopenNodeIncident(ctx, *server, now.Add(-time.Minute), now.Add(10*time.Second), time.Minute, 30*time.Second, `{"notify":false}`)
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("same incident must not recreate: %#v created=%v err=%v", second, created, err)
	}
	again, err := s.ClaimScriptEvents(ctx, "test", now.Add(time.Minute), 16)
	if err != nil || len(again) != 0 {
		t.Fatalf("version/flap must not enqueue another offline event: %#v err=%v", again, err)
	}
	if _, err := s.MarkNodeIncidentRecovering(ctx, server.ID, now.Add(20*time.Second), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	reopened, created, err := s.OpenOrReopenNodeIncident(ctx, *server, now.Add(-time.Minute), now.Add(25*time.Second), time.Minute, 30*time.Second, `{"notify":false}`)
	if err != nil || created || reopened.ID != first.ID {
		t.Fatalf("recovering flap must keep incident id: %#v created=%v err=%v", reopened, created, err)
	}
	flapEvents, err := s.ClaimScriptEvents(ctx, "test", now.Add(time.Minute), 16)
	if err != nil || len(flapEvents) != 0 {
		t.Fatalf("flap reopen must not enqueue another offline event: %#v err=%v", flapEvents, err)
	}
}
