package controller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/scripting"
	"github.com/OboardProject/oboard/internal/scriptrpc"
)

func (s *Server) StartScriptWorkerRPC(ctx context.Context, socketPath string) error {
	socketPath = filepath.Clean(strings.TrimSpace(socketPath))
	if socketPath == "." || !filepath.IsAbs(socketPath) || filepath.Dir(socketPath) == "/" {
		return errors.New("Script Worker socket path must be an absolute path inside a dedicated directory")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return err
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("Script Worker socket path exists and is not a socket")
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
	mux.HandleFunc("/v1/scripts/lease", s.scriptRPCLease)
	mux.HandleFunc("/v1/scripts/complete", s.scriptRPCComplete)
	mux.HandleFunc("/v1/scripts/sdk", s.scriptRPCSDK)
	mux.HandleFunc("/v1/scripts/heartbeat", s.scriptRPCHeartbeat)
	mux.HandleFunc("/v1/scripts/cancel-check", s.scriptRPCCancelCheck)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 35 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (s *Server) StartScriptScheduler(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if s.scripts == nil {
				continue
			}
			s.scripts.Tick(ctx)
			s.scripts.TickSustain(ctx)
		}
	}
}

func (s *Server) scriptRPCHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request scriptrpc.HeartbeatRequest
	if !decodeInternalJSON(w, r, &request) || strings.TrimSpace(request.WorkerID) == "" {
		return
	}
	s.scriptWorkerConnected.Store(true)
	s.scriptIsolation = scripting.IsolationStatus{Available: request.IsolationAvailable, Mode: request.IsolationMode, Reason: request.IsolationReason}
	settings := s.scripts.Settings(r.Context())
	writeJSON(w, http.StatusOK, scriptrpc.HeartbeatResponse{Enabled: settings.Enabled})
}

func (s *Server) scriptRPCLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request scriptrpc.LeaseRequest
	if !decodeInternalJSON(w, r, &request) || strings.TrimSpace(request.WorkerID) == "" {
		return
	}
	settings := s.scripts.Settings(r.Context())
	isolation := s.scriptIsolation
	if !settings.Enabled {
		writeJSON(w, http.StatusOK, scriptrpc.LeaseResponse{IsolationOK: isolation.Available, IsolationMode: isolation.Mode, IsolationReason: isolation.Reason, RetryAfterMS: 2000})
		return
	}
	run, err := s.store.LeaseScriptRun(r.Context(), request.WorkerID, time.Now().UTC().Add(2*time.Minute), settings.RecoveryGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, scriptrpc.LeaseResponse{IsolationOK: isolation.Available, IsolationMode: isolation.Mode, IsolationReason: isolation.Reason, RetryAfterMS: 2000})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	lease, err := s.scriptRunLease(r.Context(), run, settings)
	if err != nil {
		_ = s.store.FinishScriptRun(r.Context(), run.ID, run.LeaseGeneration, model.ScriptRunFailed, scripting.CodeOf(err), scripting.MustJSON(map[string]any{"error": err.Error()}))
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, scriptrpc.LeaseResponse{Run: lease, IsolationOK: isolation.Available, IsolationMode: isolation.Mode, IsolationReason: isolation.Reason})
}

func (s *Server) scriptRPCComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request scriptrpc.CompleteRequest
	if !decodeInternalJSON(w, r, &request) {
		return
	}
	run, err := s.store.GetScriptRunByUUID(r.Context(), request.RunUUID)
	if err != nil {
		http.Error(w, "unknown script run", http.StatusNotFound)
		return
	}
	if run.LeaseGeneration != request.LeaseGeneration || run.LeaseOwner != request.WorkerID {
		http.Error(w, "stale script run lease", http.StatusConflict)
		return
	}
	status := request.Status
	if status != model.ScriptRunSucceeded && status != model.ScriptRunFailed && status != model.ScriptRunTimedOut && status != model.ScriptRunCancelled {
		status = model.ScriptRunFailed
	}
	if err := s.store.FinishScriptRun(r.Context(), run.ID, request.LeaseGeneration, status, request.ErrorCode, request.Result); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	logs := make([]model.ScriptRunLog, 0, len(request.Logs))
	for _, line := range request.Logs {
		logs = append(logs, model.ScriptRunLog{Seq: line.Seq, Level: line.Level, Message: line.Message, FieldsJSON: line.Fields})
	}
	_ = s.store.AppendScriptRunLogs(r.Context(), run.ID, logs)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) scriptRPCSDK(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request scriptrpc.SDKRequest
	if !decodeInternalJSON(w, r, &request) {
		return
	}
	resp, err := s.scriptGateway.Invoke(r.Context(), s.scripts, request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) scriptRPCCancelCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request scriptrpc.CancelCheckRequest
	if !decodeInternalJSON(w, r, &request) {
		return
	}
	run, err := s.store.GetScriptRunByUUID(r.Context(), request.RunUUID)
	if err != nil {
		writeJSON(w, http.StatusOK, scriptrpc.CancelCheckResponse{Cancelled: true})
		return
	}
	cancelled := run.Status == model.ScriptRunCancelled || run.LeaseGeneration != request.LeaseGeneration
	writeJSON(w, http.StatusOK, scriptrpc.CancelCheckResponse{Cancelled: cancelled})
}

func (s *Server) scriptRunLease(ctx context.Context, run model.ScriptRun, settings scripting.Settings) (*scriptrpc.RunLease, error) {
	rev, err := s.store.GetScriptRevision(ctx, run.RevisionID)
	if err != nil {
		return nil, err
	}
	manifest, err := scripting.ParseManifest(rev.ManifestJSON)
	if err != nil {
		return nil, err
	}
	limits := scripting.EffectiveLimits(manifest.Limits, settings.MaxTimeoutSeconds)
	var snapshot struct {
		Params json.RawMessage   `json:"params"`
		Env    map[string]string `json:"env"`
	}
	_ = json.Unmarshal(run.SnapshotJSON, &snapshot)
	env := map[string]string{}
	for _, decl := range manifest.Env {
		if decl.Default != "" {
			env[decl.Name] = decl.Default
		}
	}
	for key, value := range snapshot.Env {
		if isReservedScriptEnv(key) {
			continue
		}
		env[key] = value
	}
	env["OBOARD_RUN_ID"] = run.UUID
	env["OBOARD_SCRIPT_ID"] = formatScriptID(run.ScriptID)
	env["OBOARD_REVISION_ID"] = formatScriptID(run.RevisionID)
	env["OBOARD_TRIGGER_ID"] = formatOptionalID(run.BindingID)
	env["OBOARD_RUN_MODE"] = run.Mode
	if subject, ok := snapshotEnv(run.SnapshotJSON, "subject_server_id"); ok {
		env["OBOARD_SUBJECT_SERVER_ID"] = subject
		env["OBOARD_EVENT_SUBJECT_ID"] = subject
	}
	if target, ok := snapshotEnv(run.SnapshotJSON, "target_server_id"); ok {
		env["OBOARD_TARGET_SERVER_ID"] = target
	}
	if scheduled, ok := snapshotEnv(run.SnapshotJSON, "scheduled_at"); ok {
		env["OBOARD_SCHEDULED_AT"] = scheduled
	}
	return &scriptrpc.RunLease{
		RunID: run.ID, UUID: run.UUID, ScriptID: run.ScriptID, RevisionID: run.RevisionID,
		LeaseGeneration: run.LeaseGeneration, Mode: run.Mode, Source: rev.Source,
		Params: snapshot.Params, Env: env,
		Limits: scriptrpc.RunLimits{
			TimeoutSeconds: limits.TimeoutSeconds, MemoryMiB: limits.MemoryMiB,
			SDKCalls: limits.SDKCalls, ManageActions: limits.ManageActions,
			LogBytes: limits.LogBytes, ResultBytes: limits.ResultBytes,
		},
		TimeoutSeconds: limits.TimeoutSeconds,
	}, nil
}

func isReservedScriptEnv(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "OBOARD_RUN_ID", "OBOARD_SCRIPT_ID", "OBOARD_REVISION_ID", "OBOARD_TRIGGER_ID", "OBOARD_SCHEDULED_AT", "OBOARD_SUBJECT_SERVER_ID", "OBOARD_TARGET_SERVER_ID", "OBOARD_RUN_MODE", "RUN_ID", "SCRIPT_ID", "REVISION_ID", "TRIGGER_ID", "SERVER_ID":
		return true
	default:
		return false
	}
}

func formatOptionalID(id *int64) string {
	if id == nil {
		return ""
	}
	return formatScriptID(*id)
}

func snapshotEnv(raw json.RawMessage, key string) (string, bool) {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return "", false
	}
	value, ok := payload[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return text, ok
}
