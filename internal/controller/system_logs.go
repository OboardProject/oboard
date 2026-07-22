package controller

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) systemLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		fail(w, errors.New("controller log storage is not configured"), http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		lines := 500
		if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 || value > 5000 {
				fail(w, errors.New("lines must be between 1 and 5000"), 400)
				return
			}
			lines = value
		}
		snapshot, err := s.logs.Snapshot(lines, r.URL.Query().Get("q"))
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"logs": snapshot})
	case http.MethodPost:
		var req struct {
			Action string `json:"action"`
		}
		if !decode(w, r, &req) {
			return
		}
		if strings.ToLower(strings.TrimSpace(req.Action)) != "rotate" {
			fail(w, errors.New("action must be rotate"), 400)
			return
		}
		if err := s.logs.Rotate(); err != nil {
			fail(w, err, 500)
			return
		}
		log.Printf("controller log rotated by administrator")
		auditReq(s, r, "rotate", "system-logs", "controller")
		write(w, 200, map[string]any{"message": "controller logs rotated"})
	case http.MethodDelete:
		if err := s.logs.Clear(); err != nil {
			fail(w, err, 500)
			return
		}
		auditReq(s, r, "clear", "system-logs", "controller")
		write(w, 200, map[string]any{"message": "controller logs cleared"})
	default:
		method(w)
	}
}

func (s *Server) systemLogsDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if s.logs == nil {
		fail(w, errors.New("controller log storage is not configured"), http.StatusServiceUnavailable)
		return
	}
	filename := fmt.Sprintf("oboard-controller-logs-%s.zip", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if err := s.logs.WriteZIP(w); err != nil {
		log.Printf("download controller logs: %v", err)
	}
}
