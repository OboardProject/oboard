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
	"slices"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/airpc"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) StartAIWorkerRPC(ctx context.Context, socketPath string) error {
	socketPath = filepath.Clean(strings.TrimSpace(socketPath))
	if socketPath == "." || !filepath.IsAbs(socketPath) || filepath.Dir(socketPath) == "/" {
		return errors.New("AI Worker socket path must be an absolute path inside a dedicated directory")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return err
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("AI Worker socket path exists and is not a socket")
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
	if err := os.Chmod(socketPath, 0o660); err != nil {
		_ = listener.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/lease", s.aiRPCLease)
	mux.HandleFunc("/v1/jobs/", s.aiRPCJob)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	go func() { _ = server.Serve(listener) }()
	return nil
}

func (s *Server) aiRPCLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request airpc.LeaseRequest
	if !decodeInternalJSON(w, r, &request) || strings.TrimSpace(request.WorkerID) == "" || len(request.WorkerID) > 128 {
		return
	}
	job, provider, err := s.store.LeaseAIAnalysisJob(r.Context(), request.WorkerID, time.Now().UTC(), 2*time.Minute)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, airpc.LeaseResponse{})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	credential, err := security.DecryptSecret(s.sessionSecret, "ai-provider-credential:"+provider.ID, provider.CredentialEncrypted)
	if err != nil {
		_ = s.store.FailAIAnalysisJob(r.Context(), request.WorkerID, job.ID, "provider credential cannot be decrypted")
		http.Error(w, "provider credential cannot be decrypted", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, airpc.LeaseResponse{Job: job, Provider: &airpc.Provider{ID: provider.ID, BaseURL: provider.BaseURL, Model: provider.Model, APIKey: credential, AllowRawAudit: provider.AllowRawAudit}})
}

func (s *Server) aiRPCJob(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := pathParts(r.URL.Path, "/v1/jobs/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "complete":
		var request airpc.CompleteRequest
		if !decodeInternalJSON(w, r, &request) {
			return
		}
		if err := validateAIFinding(&request.Finding); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(request.WorkerID) == "" || request.InputTokens < 0 || request.OutputTokens < 0 || request.InputTokens > 100_000_000 || request.OutputTokens > 100_000_000 {
			http.Error(w, "invalid AI usage", http.StatusBadRequest)
			return
		}
		request.Finding.JobID = parts[0]
		job := &model.AIAnalysisJob{ID: parts[0], Output: request.RawOutput, InputTokens: request.InputTokens, OutputTokens: request.OutputTokens}
		if err := s.store.CompleteAIAnalysisJob(r.Context(), request.WorkerID, job, &request.Finding); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "fail":
		var request airpc.FailRequest
		if !decodeInternalJSON(w, r, &request) {
			return
		}
		request.Error = strings.TrimSpace(request.Error)
		if request.WorkerID == "" || request.Error == "" || len(request.Error) > 1000 {
			http.Error(w, "invalid failure", http.StatusBadRequest)
			return
		}
		if err := s.store.FailAIAnalysisJob(r.Context(), request.WorkerID, parts[0], request.Error); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func validateAIFinding(item *model.AIFinding) error {
	if item == nil || !slices.Contains([]string{"possible_account_sharing", "possible_abuse", "likely_legitimate", "insufficient_evidence"}, item.Classification) || item.Confidence < 0 || item.Confidence > 1 || strings.TrimSpace(item.IncidentID) == "" || strings.TrimSpace(item.ProviderID) == "" || strings.TrimSpace(item.Model) == "" || strings.TrimSpace(item.Summary) == "" || len(item.Summary) > 1000 || len(item.EvidenceRefs) > 32 || len(item.CounterEvidence) > 32 || len(item.RecommendedActions) > 16 {
		return errors.New("AI finding does not match the required schema")
	}
	allowedActions := []string{"notify_admin", "request_manual_review", "propose_temporary_subscription_suspension", "continue_observation"}
	for _, values := range [][]string{item.EvidenceRefs, item.CounterEvidence} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 500 {
				return errors.New("AI finding evidence is invalid")
			}
		}
	}
	for _, action := range item.RecommendedActions {
		if !slices.Contains(allowedActions, action) {
			return errors.New("AI finding action is invalid")
		}
	}
	return nil
}

func decodeInternalJSON(w http.ResponseWriter, r *http.Request, output any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	return true
}
