package controller

import (
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

// Agent websocket keepalive. The read timeout is several ping intervals so a
// pong lost to a busy moment never drops a healthy Agent, and it stays well
// above the 10s/20s heartbeat an Agent already answers.
const (
	defaultAgentSocketPingInterval = 30 * time.Second
	defaultAgentSocketReadTimeout  = 90 * time.Second
	defaultAgentSocketWriteTimeout = 20 * time.Second
)

func (s *Server) agentSocketKeepalive() (ping, read, write time.Duration) {
	ping, read, write = s.agentSocketPingInterval, s.agentSocketReadTimeout, s.agentSocketWriteTimeout
	if ping <= 0 {
		ping = defaultAgentSocketPingInterval
	}
	if read <= 0 {
		read = defaultAgentSocketReadTimeout
	}
	if write <= 0 {
		write = defaultAgentSocketWriteTimeout
	}
	return ping, read, write
}

// One server can hold several agent sockets at once: agentConnect counts
// overlapping connections on purpose, because a reconnecting Agent regularly
// dials a new socket before the previous one has finished dying. The control
// channels are therefore kept per connection, in registration order, so a short
// lived duplicate that comes and goes can never unregister the channel of the
// connection that is still serving health reports. Keeping a single slot here
// made that case report the server as offline for every remote request while
// the panel still showed it online.
func (s *Server) registerAgentLive(serverID int64, ch chan any) {
	s.agentLiveMu.Lock()
	defer s.agentLiveMu.Unlock()
	if s.agentLive == nil {
		s.agentLive = map[int64][]chan any{}
	}
	s.agentLive[serverID] = append(s.agentLive[serverID], ch)
}

func (s *Server) unregisterAgentLive(serverID int64, ch chan any) {
	s.agentLiveMu.Lock()
	defer s.agentLiveMu.Unlock()
	channels := s.agentLive[serverID]
	for index, candidate := range channels {
		if candidate != ch {
			continue
		}
		channels = append(channels[:index], channels[index+1:]...)
		break
	}
	if len(channels) == 0 {
		delete(s.agentLive, serverID)
		return
	}
	s.agentLive[serverID] = channels
}

// agentControlOnline reports whether a control payload can be handed to this
// server right now. It is the fact behind every remote request, and unlike the
// persisted status it never lags the socket.
func (s *Server) agentControlOnline(serverID int64) bool {
	s.agentLiveMu.Lock()
	defer s.agentLiveMu.Unlock()
	return len(s.agentLive[serverID]) > 0
}

// sendAgentControl delivers to the newest connection first: after a reconnect
// that is the socket the Agent is actually reading. Older connections are only
// tried when the newest one cannot take the payload immediately, and exactly one
// connection ever receives it, so a duplicated Agent identity cannot start two
// PTYs for one request.
func (s *Server) sendAgentControl(serverID int64, payload any) bool {
	s.agentLiveMu.Lock()
	channels := append([]chan any(nil), s.agentLive[serverID]...)
	s.agentLiveMu.Unlock()
	if len(channels) == 0 {
		return false
	}
	for index := len(channels) - 1; index >= 0; index-- {
		select {
		case channels[index] <- payload:
			return true
		default:
		}
	}
	// Every buffer is full: wait on the newest connection only, so the total
	// budget stays the same regardless of how many sockets are registered.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case channels[len(channels)-1] <- payload:
		return true
	case <-timer.C:
		return false
	}
}

type remoteExecResultHub struct {
	mu      sync.Mutex
	waiters map[string]chan model.RemoteExecTransientResult
	ready   map[string]model.RemoteExecTransientResult
}

func newRemoteExecResultHub() *remoteExecResultHub {
	return &remoteExecResultHub{waiters: map[string]chan model.RemoteExecTransientResult{}, ready: map[string]model.RemoteExecTransientResult{}}
}

func (h *remoteExecResultHub) Put(result model.RemoteExecTransientResult) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch := h.waiters[result.RequestID]; ch != nil {
		select {
		case ch <- result:
		default:
			h.ready[result.RequestID] = result
		}
		delete(h.waiters, result.RequestID)
		return
	}
	h.ready[result.RequestID] = result
	go func() {
		time.Sleep(60 * time.Second)
		h.mu.Lock()
		delete(h.ready, result.RequestID)
		h.mu.Unlock()
	}()
}

func (h *remoteExecResultHub) Wait(requestID string, timeout time.Duration) (model.RemoteExecTransientResult, bool) {
	h.mu.Lock()
	if result, ok := h.ready[requestID]; ok {
		delete(h.ready, requestID)
		h.mu.Unlock()
		return result, true
	}
	ch := make(chan model.RemoteExecTransientResult, 1)
	h.waiters[requestID] = ch
	h.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-ch:
		return result, true
	case <-timer.C:
		h.mu.Lock()
		delete(h.waiters, requestID)
		h.mu.Unlock()
		return model.RemoteExecTransientResult{}, false
	}
}
