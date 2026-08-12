package controller

import (
	"context"
	"log"
	"time"
)

func (s *Server) StartLatencyProbeScheduler(ctx context.Context) {
	s.runLatencyProbeScheduler(ctx)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runLatencyProbeScheduler(ctx)
		}
	}
}

func (s *Server) runLatencyProbeScheduler(ctx context.Context) {
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		log.Printf("latency probe scheduler: list servers: %v", err)
		return
	}
	for _, server := range servers {
		if ctx.Err() != nil {
			return
		}
		if err := s.enqueueConfiguredLatencyProbe(ctx, server, false); err != nil {
			log.Printf("latency probe scheduler server=%d: %v", server.ID, err)
		}
	}
}
