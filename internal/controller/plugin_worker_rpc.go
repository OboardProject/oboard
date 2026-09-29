package controller

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/pluginrpc"
	"github.com/OboardProject/oboard/internal/pluginsandbox"
)

// StartPluginWorkerRPC serves the plugin worker on a Controller-owned Unix
// socket. It is the only channel through which plugin code reaches OBoard.
func (s *Server) StartPluginWorkerRPC(ctx context.Context, socketPath string) error {
	socketPath = filepath.Clean(strings.TrimSpace(socketPath))
	if socketPath == "." || !filepath.IsAbs(socketPath) || filepath.Dir(socketPath) == "/" {
		return errors.New("Plugin Worker socket path must be an absolute path inside a dedicated directory")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return err
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("Plugin Worker socket path exists and is not a socket")
		}
		if err := os.Remove(socketPath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(socketPath, 0o660); err != nil { // #nosec G302 -- the dedicated worker group requires read/write access to this Unix socket.
		_ = listener.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc/plugins/heartbeat", s.pluginRPCHeartbeat)
	mux.HandleFunc("/rpc/plugins/lease", s.pluginRPCLease)
	mux.HandleFunc("/rpc/plugins/call", s.pluginRPCCall)
	mux.HandleFunc("/rpc/plugins/complete", s.pluginRPCComplete)
	mux.HandleFunc("/rpc/plugins/cancel-check", s.pluginRPCCancelCheck)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 75 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("plugin worker rpc: %v", err)
		}
	}()
	return nil
}

// StartPluginScheduler fires schedules and events and sweeps expired leases
// and retention.
func (s *Server) StartPluginScheduler(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastSweep := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.plugins.Tick(ctx)
		if time.Since(lastSweep) >= 30*time.Second {
			s.plugins.Sweep(ctx)
			lastSweep = time.Now()
		}
	}
}

func (s *Server) pluginRPCHeartbeat(w http.ResponseWriter, r *http.Request) {
	var request pluginrpc.HeartbeatRequest
	if !pluginRPCDecode(w, r, &request) {
		return
	}
	if request.ProtocolVersion != pluginrpc.ProtocolVersion {
		http.Error(w, "unsupported plugin worker protocol", http.StatusConflict)
		return
	}
	s.pluginWorker.record(pluginsandbox.IsolationStatus{Available: request.IsolationAvailable, Mode: request.IsolationMode, Reason: request.IsolationReason})
	writeJSON(w, http.StatusOK, pluginrpc.HeartbeatResponse{Enabled: s.plugins.Settings(r.Context()).Enabled})
}

func (s *Server) pluginRPCLease(w http.ResponseWriter, r *http.Request) {
	var request pluginrpc.LeaseRequest
	if !pluginRPCDecode(w, r, &request) {
		return
	}
	if connected, isolation := s.pluginWorker.snapshot(); !connected || !isolation.Available {
		writeJSON(w, http.StatusOK, pluginrpc.LeaseResponse{RetryAfterMS: 2000})
		return
	}
	run, err := s.plugins.Lease(r.Context(), request.WorkerID)
	if err != nil {
		http.Error(w, "lease failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, pluginrpc.LeaseResponse{Run: run, RetryAfterMS: 2000})
}

func (s *Server) pluginRPCCall(w http.ResponseWriter, r *http.Request) {
	var request pluginrpc.CallRequest
	if !pluginRPCDecode(w, r, &request) {
		return
	}
	writeJSON(w, http.StatusOK, s.plugins.Invoke(r.Context(), request))
}

func (s *Server) pluginRPCComplete(w http.ResponseWriter, r *http.Request) {
	var request pluginrpc.CompleteRequest
	if !pluginRPCDecode(w, r, &request) {
		return
	}
	if err := s.plugins.Complete(r.Context(), request); err != nil {
		http.Error(w, "stale lease", http.StatusConflict)
		return
	}
	s.publishRealtime("plugins")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pluginRPCCancelCheck(w http.ResponseWriter, r *http.Request) {
	var request pluginrpc.CancelCheckRequest
	if !pluginRPCDecode(w, r, &request) {
		return
	}
	writeJSON(w, http.StatusOK, pluginrpc.CancelCheckResponse{Cancelled: s.plugins.CancelRequested(r.Context(), request.RunUUID, request.LeaseGeneration)})
}

func pluginRPCDecode(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !decodeInternalJSON(w, r, target) {
		return false
	}
	return true
}
