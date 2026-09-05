package controller

import (
	"testing"
	"time"
)

// A healthy Agent answers the Controller ping, so its link must survive well
// past the read timeout. Without the pong path this would drop every Agent.
func TestAgentSocketSurvivesPastReadTimeoutWhilePonging(t *testing.T) {
	_, srv, server, httpServer := newTaskDispatchServer(t)
	srv.agentSocketPingInterval = 40 * time.Millisecond
	srv.agentSocketReadTimeout = 250 * time.Millisecond
	srv.agentSocketWriteTimeout = 2 * time.Second

	agent := connectTestAgent(t, srv, httpServer.URL, server)
	defer agent.close()

	// Gorilla answers pings from inside ReadMessage, so a client only stays
	// alive while it is actually reading, exactly like the real Agent loop.
	readErrors := make(chan error, 1)
	go func() {
		for {
			if _, _, err := agent.conn.ReadMessage(); err != nil {
				readErrors <- err
				return
			}
		}
	}()

	select {
	case err := <-readErrors:
		t.Fatalf("healthy agent link dropped: %v", err)
	case <-time.After(3 * srv.agentSocketReadTimeout):
	}
	if !srv.agentControlOnline(server.ID) {
		t.Fatal("ponging agent must still hold a control channel")
	}
	if !srv.sendAgentControl(server.ID, map[string]any{"type": "interactive_prepare"}) {
		t.Fatal("control payload must still reach a ponging agent")
	}
}

// A host killed without closing TCP leaves a socket that never reads and never
// pongs. It must be reaped instead of holding a control channel forever.
func TestAgentSocketReapsSilentConnection(t *testing.T) {
	_, srv, server, httpServer := newTaskDispatchServer(t)
	srv.agentSocketPingInterval = 40 * time.Millisecond
	srv.agentSocketReadTimeout = 250 * time.Millisecond
	srv.agentSocketWriteTimeout = 2 * time.Second

	agent := connectTestAgent(t, srv, httpServer.URL, server)
	defer agent.close()

	deadline := time.Now().Add(5 * time.Second)
	for !srv.agentControlOnline(server.ID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !srv.agentControlOnline(server.ID) {
		t.Fatal("agent never registered a control channel")
	}
	// This client never reads, so it never pongs.
	for time.Now().Before(deadline) {
		if !srv.agentControlOnline(server.ID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("silent agent connection was never reaped")
}
