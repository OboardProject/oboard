package controller

import (
	"github.com/gorilla/websocket"
)

type agentConnSession struct {
	agentID string
	conn    *websocket.Conn
}

func (s *Server) registerAgentConn(serverID int64, agentID string, conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	s.agentConnsMu.Lock()
	defer s.agentConnsMu.Unlock()
	if s.agentConns == nil {
		s.agentConns = map[int64][]agentConnSession{}
	}
	s.agentConns[serverID] = append(s.agentConns[serverID], agentConnSession{agentID: agentID, conn: conn})
}

func (s *Server) unregisterAgentConn(serverID int64, conn *websocket.Conn) {
	if s == nil || conn == nil {
		return
	}
	s.agentConnsMu.Lock()
	defer s.agentConnsMu.Unlock()
	sessions := s.agentConns[serverID]
	for index, item := range sessions {
		if item.conn != conn {
			continue
		}
		sessions = append(sessions[:index], sessions[index+1:]...)
		break
	}
	if len(sessions) == 0 {
		delete(s.agentConns, serverID)
		return
	}
	s.agentConns[serverID] = sessions
}

// evictAgentSessions closes every live Agent websocket for the server. Used
// after enrollment replaces the Agent identity so a still-connected previous
// process cannot receive tasks signed with the revoked token hash.
func (s *Server) evictAgentSessions(serverID int64) {
	if s == nil {
		return
	}
	s.agentConnsMu.Lock()
	sessions := append([]agentConnSession(nil), s.agentConns[serverID]...)
	s.agentConnsMu.Unlock()
	for _, item := range sessions {
		_ = item.conn.Close()
	}
}
