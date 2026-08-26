package controller

import (
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

func (s *Server) registerAgentLive(serverID int64, ch chan any) {
	s.agentLiveMu.Lock()
	defer s.agentLiveMu.Unlock()
	if s.agentLive == nil {
		s.agentLive = map[int64]chan any{}
	}
	s.agentLive[serverID] = ch
}

func (s *Server) unregisterAgentLive(serverID int64, ch chan any) {
	s.agentLiveMu.Lock()
	defer s.agentLiveMu.Unlock()
	if s.agentLive[serverID] == ch {
		delete(s.agentLive, serverID)
	}
}

func (s *Server) sendAgentControl(serverID int64, payload any) bool {
	s.agentLiveMu.Lock()
	ch := s.agentLive[serverID]
	s.agentLiveMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- payload:
		return true
	case <-time.After(2 * time.Second):
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
