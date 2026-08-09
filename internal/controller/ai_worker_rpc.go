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

	"github.com/OboardProject/oboard/internal/aiprovider"
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
	if err := os.Chmod(socketPath, 0o660); err != nil { // #nosec G302 -- the dedicated worker group requires read/write access to this Unix socket.
		_ = listener.Close()
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/jobs/lease", s.aiRPCLease)
	mux.HandleFunc("/v1/jobs/", s.aiRPCJob)
	mux.HandleFunc("/v1/model-discovery/lease", s.aiRPCModelDiscoveryLease)
	mux.HandleFunc("/v1/model-discovery/", s.aiRPCModelDiscoveryResult)
	mux.HandleFunc("/v1/ai-test/lease", s.aiRPCAITestLease)
	mux.HandleFunc("/v1/ai-test/", s.aiRPCAITestResult)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
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

func (s *Server) aiRPCModelDiscoveryLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request airpc.ModelDiscoveryLeaseRequest
	if !decodeInternalJSON(w, r, &request) || strings.TrimSpace(request.WorkerID) == "" || len(request.WorkerID) > 128 {
		return
	}
	discovery, err := s.aiModelDiscoveries.lease(r.Context(), request.WorkerID)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			http.Error(w, "model discovery lease failed", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, airpc.ModelDiscoveryLeaseResponse{Request: discovery})
}

func (s *Server) aiRPCModelDiscoveryResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := pathParts(r.URL.Path, "/v1/model-discovery/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "complete":
		var request airpc.ModelDiscoveryCompleteRequest
		if !decodeInternalJSON(w, r, &request) {
			return
		}
		models, err := normalizeAIModelIDs(request.Models)
		if strings.TrimSpace(request.WorkerID) == "" || err != nil {
			http.Error(w, "invalid model discovery result", http.StatusBadRequest)
			return
		}
		if _, err := s.aiModelDiscoveries.finish(parts[0], request.WorkerID, aiModelDiscoveryResult{models: models}); err != nil {
			http.Error(w, "model discovery request is no longer active", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case "fail":
		var request airpc.ModelDiscoveryFailRequest
		if !decodeInternalJSON(w, r, &request) {
			return
		}
		request.Error = strings.TrimSpace(request.Error)
		if request.WorkerID == "" || request.Error == "" || len(request.Error) > 1000 {
			http.Error(w, "invalid model discovery failure", http.StatusBadRequest)
			return
		}
		if _, err := s.aiModelDiscoveries.finish(parts[0], request.WorkerID, aiModelDiscoveryResult{err: errors.New("AI Provider model discovery failed"), detail: request.Error}); err != nil {
			http.Error(w, "model discovery request is no longer active", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
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
	job, provider, err := s.store.LeaseAuditReviewJob(r.Context(), request.WorkerID, time.Now().UTC(), 2*time.Minute)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusOK, airpc.LeaseResponse{})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if provider == nil || !providerHasAuditEndpoint(provider) {
		_ = s.store.FailAuditReviewJob(r.Context(), request.WorkerID, job.ID, "provider 未通过审计就绪测试（需要 A/B 级能力）", nil)
		http.Error(w, "provider capability is not audit-ready", http.StatusConflict)
		return
	}
	review, err := s.store.GetAuditReview(r.Context(), job.ReviewID)
	if err != nil {
		_ = s.store.FailAuditReviewJob(r.Context(), request.WorkerID, job.ID, "review cannot be loaded", nil)
		http.Error(w, "review cannot be loaded", http.StatusInternalServerError)
		return
	}
	if review.PrivacyMode == "raw" && !provider.AllowRawAudit {
		_ = s.store.FailAuditReviewJob(r.Context(), request.WorkerID, job.ID, "provider raw audit authorization was revoked", nil)
		http.Error(w, "provider raw audit authorization was revoked", http.StatusConflict)
		return
	}
	runtimeEndpoints, err := s.runtimeAIEndpoints(provider)
	if err != nil {
		_ = s.store.FailAuditReviewJob(r.Context(), request.WorkerID, job.ID, "provider credential cannot be decrypted", nil)
		http.Error(w, "provider credential cannot be decrypted", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, airpc.LeaseResponse{Job: job, Provider: &airpc.Provider{ID: provider.ID, Name: provider.Name, ProviderKind: provider.ProviderKind, Model: provider.DefaultModel, RoutingStrategy: provider.RoutingStrategy, AllowRawAudit: provider.AllowRawAudit, Endpoints: runtimeEndpoints}})
}

func providerHasAuditEndpoint(provider *model.AIProvider) bool {
	for _, endpoint := range provider.Endpoints {
		capability := endpoint.Capability
		if endpoint.Enabled && capability != nil && (capability.AuditGrade == model.AuditProviderGradeA || capability.AuditGrade == model.AuditProviderGradeB) {
			return true
		}
	}
	return false
}

func (s *Server) runtimeAIEndpoints(provider *model.AIProvider) ([]airpc.RuntimeEndpoint, error) {
	endpoints := make([]airpc.RuntimeEndpoint, 0, len(provider.Endpoints))
	for _, endpoint := range provider.Endpoints {
		credential := ""
		var err error
		if endpoint.CredentialEncrypted != "" {
			credential, err = security.DecryptSecret(s.sessionSecret, "ai-provider-endpoint-credential:"+endpoint.ID, endpoint.CredentialEncrypted)
			if err != nil {
				return nil, err
			}
		}
		headers := map[string]string{}
		if err := json.Unmarshal([]byte(endpoint.HeadersJSON), &headers); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, airpc.RuntimeEndpoint{ID: endpoint.ID, Name: endpoint.Name, BaseURL: endpoint.BaseURL, APIStyle: endpoint.APIStyle, AuthMode: endpoint.AuthMode, Credential: credential, AnthropicVersion: endpoint.AnthropicVersion, Headers: headers, ModelsPath: endpoint.ModelsPath, GeneratePath: endpoint.GeneratePath, ModelOverride: endpoint.ModelOverride, Priority: endpoint.Priority, Enabled: endpoint.Enabled, TimeoutMS: endpoint.TimeoutMS, MaxRetries: endpoint.MaxRetries, AllowPrivateNetwork: endpoint.AllowPrivateNetwork, Capability: endpoint.Capability})
	}
	return endpoints, nil
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
		job, err := s.store.GetAuditReviewJobByID(r.Context(), parts[0])
		if err != nil {
			http.Error(w, "unknown review job", http.StatusNotFound)
			return
		}
		if len(request.Output) == 0 || len(request.Output) > 1<<20 {
			http.Error(w, "invalid AI output", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(request.WorkerID) == "" || request.InputTokens < 0 || request.OutputTokens < 0 || request.InputTokens > 100_000_000 || request.OutputTokens > 100_000_000 {
			http.Error(w, "invalid AI usage", http.StatusBadRequest)
			return
		}
		if request.Route == nil || request.Route.ProviderID != job.ProviderID || request.Route.EndpointID == "" || request.Route.APIStyle == "" || request.Route.Model == "" || request.Route.CapabilityProfileVersion != model.AuditProviderProfileVersion || request.Route.CapabilityConfigDigest == "" || request.Route.AttemptCount < 1 || request.Route.AttemptCount > 100 || request.Route.InputTokens != request.InputTokens || request.Route.OutputTokens != request.OutputTokens || request.Route.LatencyMS < 0 || request.Route.LatencyMS > int64((10*time.Minute)/time.Millisecond) || len(request.Route.ProviderRequestID) > 512 {
			http.Error(w, "invalid AI route evidence", http.StatusBadRequest)
			return
		}
		provider, err := s.store.GetAIProvider(r.Context(), job.ProviderID)
		if err != nil {
			http.Error(w, "AI route provider cannot be loaded", http.StatusBadRequest)
			return
		}
		capability, err := validateAuditRouteEvidence(provider, request.Route)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := s.auditReviews.ValidateReportWithCapability(r.Context(), job.ReviewID, job, request.Output, capability); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		routeJSON, _ := json.Marshal(request.Route)
		if _, err := s.store.CompleteAuditReviewJob(r.Context(), request.WorkerID, parts[0], request.Output, request.InputTokens, request.OutputTokens, routeJSON); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if err := s.auditReviews.Advance(r.Context(), job.ReviewID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.publishRealtime("audit", "ai-reviews")
		w.WriteHeader(http.StatusNoContent)
	case "fail":
		var request airpc.FailRequest
		if !decodeInternalJSON(w, r, &request) {
			return
		}
		request.Error = strings.TrimSpace(request.Error)
		request.ErrorDetail = normalizeErrorDetail(request.ErrorDetail)
		if request.WorkerID == "" || request.Error == "" || len(request.Error) > 1000 {
			http.Error(w, "invalid failure", http.StatusBadRequest)
			return
		}
		if err := s.store.FailAuditReviewJob(r.Context(), request.WorkerID, parts[0], request.Error, request.ErrorDetail); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func validateAuditRouteEvidence(provider *model.AIProvider, route *airpc.RouteEvidence) (*model.AIProviderCapability, error) {
	if provider == nil || route == nil || provider.ID != route.ProviderID {
		return nil, errors.New("invalid AI route evidence")
	}
	for _, endpoint := range provider.Endpoints {
		if endpoint.ID != route.EndpointID {
			continue
		}
		capability := endpoint.Capability
		expectedModel := provider.DefaultModel
		if endpoint.ModelOverride != "" {
			expectedModel = endpoint.ModelOverride
		}
		if !endpoint.Enabled || endpoint.APIStyle != route.APIStyle || route.Model != expectedModel || capability == nil || capability.ProviderProfileVersion != route.CapabilityProfileVersion || capability.ProviderID != provider.ID || capability.EndpointID != endpoint.ID || capability.APIStyle != endpoint.APIStyle || capability.Model != route.Model || capability.ConfigDigest != route.CapabilityConfigDigest || (capability.AuditGrade != model.AuditProviderGradeA && capability.AuditGrade != model.AuditProviderGradeB) {
			return nil, errors.New("stale or ineligible AI route evidence")
		}
		return capability, nil
	}
	return nil, errors.New("AI route endpoint does not belong to provider")
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

func (s *Server) aiRPCAITestLease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request airpc.AITestLeaseRequest
	if !decodeInternalJSON(w, r, &request) || strings.TrimSpace(request.WorkerID) == "" || len(request.WorkerID) > 128 {
		return
	}
	test, err := s.aiTests.lease(r.Context(), request.WorkerID)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			http.Error(w, "AI provider test lease failed", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, airpc.AITestLeaseResponse{Request: test})
}

func (s *Server) aiRPCAITestResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := pathParts(r.URL.Path, "/v1/ai-test/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	switch parts[1] {
	case "complete":
		var request airpc.AITestCompleteRequest
		if !decodeInternalJSON(w, r, &request) {
			return
		}
		request.RequestJSON = strings.TrimSpace(request.RequestJSON)
		request.ResponseJSON = strings.TrimSpace(request.ResponseJSON)
		request.Error = strings.TrimSpace(request.Error)
		if strings.TrimSpace(request.WorkerID) == "" || (request.StatusCode != 0 && (request.StatusCode < 100 || request.StatusCode > 599)) || request.DurationMS < 0 || request.DurationMS > 3_600_000 || len(request.Error) > 1000 || len(request.RequestJSON) > aiTestRawJSONLimit || len(request.ResponseJSON) > aiTestRawJSONLimit || len(request.Content) > 500 {
			http.Error(w, "invalid AI provider test result", http.StatusBadRequest)
			return
		}
		if err := validateAITestCapability(request.Capability); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result := aiTestResult{ok: request.OK, requestJSON: request.RequestJSON, responseJSON: request.ResponseJSON, statusCode: request.StatusCode, durationMS: request.DurationMS, content: request.Content, detail: request.Error, capability: request.Capability}
		requested, err := s.aiTests.activeRequest(parts[0], request.WorkerID)
		if err != nil {
			http.Error(w, "AI provider test request is no longer active", http.StatusConflict)
			return
		}
		if request.Capability != nil {
			if err := validateAITestTarget(requested, request.Capability); err != nil {
				http.Error(w, "AI provider capability target mismatch", http.StatusBadRequest)
				return
			}
			if requested.ProviderID != "" {
				if err := s.store.UpsertAIProviderEndpointCapability(r.Context(), request.Capability); err != nil && !errors.Is(err, sql.ErrNoRows) {
					http.Error(w, "AI provider capability cannot be stored", http.StatusInternalServerError)
					return
				}
			}
		}
		if _, err := s.aiTests.finish(parts[0], request.WorkerID, result); err != nil {
			http.Error(w, "AI provider test request is no longer active", http.StatusConflict)
			return
		}
		recordAITestLog(requested, result)
		w.WriteHeader(http.StatusNoContent)
	case "fail":
		var request airpc.AITestFailRequest
		if !decodeInternalJSON(w, r, &request) {
			return
		}
		request.Error = strings.TrimSpace(request.Error)
		request.RequestJSON = strings.TrimSpace(request.RequestJSON)
		request.ResponseJSON = strings.TrimSpace(request.ResponseJSON)
		if strings.TrimSpace(request.WorkerID) == "" || request.Error == "" || len(request.Error) > 1000 || request.StatusCode < 0 || request.StatusCode > 599 || request.DurationMS < 0 || request.DurationMS > 3_600_000 || len(request.RequestJSON) > aiTestRawJSONLimit || len(request.ResponseJSON) > aiTestRawJSONLimit {
			http.Error(w, "invalid AI provider test failure", http.StatusBadRequest)
			return
		}
		requested, err := s.aiTests.finish(parts[0], request.WorkerID, aiTestResult{ok: false, requestJSON: request.RequestJSON, responseJSON: request.ResponseJSON, statusCode: request.StatusCode, durationMS: request.DurationMS, detail: request.Error})
		if err != nil {
			http.Error(w, "AI provider test request is no longer active", http.StatusConflict)
			return
		}
		recordAITestLog(requested, aiTestResult{ok: false, requestJSON: request.RequestJSON, responseJSON: request.ResponseJSON, statusCode: request.StatusCode, durationMS: request.DurationMS, detail: request.Error})
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func validateAITestTarget(request airpc.AITestRequest, capability *model.AIProviderCapability) error {
	if capability == nil {
		return nil
	}
	providerID := request.ProviderID
	if providerID == "" {
		providerID = "draft:" + request.ID
	}
	endpoint := aiprovider.RuntimeEndpoint{
		ID:                  request.Endpoint.ID,
		ProviderID:          providerID,
		BaseURL:             request.Endpoint.BaseURL,
		APIStyle:            aiprovider.APIStyle(request.Endpoint.APIStyle),
		AuthMode:            request.Endpoint.AuthMode,
		AnthropicVersion:    request.Endpoint.AnthropicVersion,
		Headers:             request.Endpoint.Headers,
		ModelsPath:          request.Endpoint.ModelsPath,
		GeneratePath:        request.Endpoint.GeneratePath,
		ModelOverride:       request.Endpoint.ModelOverride,
		AllowPrivateNetwork: request.Endpoint.AllowPrivateNetwork,
	}
	if capability.ProviderID != providerID || capability.EndpointID != request.Endpoint.ID || capability.APIStyle != request.Endpoint.APIStyle || capability.Model != request.Model || capability.ConfigDigest != aiprovider.ConfigDigest(endpoint, request.Model) {
		return errors.New("AI provider capability target mismatch")
	}
	return nil
}
