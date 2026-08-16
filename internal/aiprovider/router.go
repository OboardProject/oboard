package aiprovider

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"sync"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

type breakerState struct {
	failures  int
	openUntil time.Time
	halfOpen  bool
}

type Router struct {
	registry Registry
	mu       sync.Mutex
	breakers map[string]*breakerState
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
	random   *rand.Rand
}

func NewRouter(registry Registry) *Router {
	return &Router{
		registry: registry,
		breakers: map[string]*breakerState{},
		now:      time.Now,
		sleep: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		random: rand.New(rand.NewSource(time.Now().UnixNano())), // #nosec G404 -- retry jitter is not used for a security-sensitive decision.
	}
}

func (r *Router) ListModels(ctx context.Context, endpoint RuntimeEndpoint) ([]ModelInfo, error) {
	client, err := r.registry.Client(endpoint.APIStyle)
	if err != nil {
		return nil, err
	}
	return client.ListModels(ctx, endpoint)
}

func (r *Router) Complete(ctx context.Context, provider RuntimeProvider, request Request, formalAudit bool) (*Response, error) {
	endpoints := append([]RuntimeEndpoint(nil), provider.Endpoints...)
	sort.SliceStable(endpoints, func(i, j int) bool { return endpoints[i].Priority < endpoints[j].Priority })
	eligible := 0
	var lastErr error
	totalAttempts := 0
	for _, endpoint := range endpoints {
		if !endpoint.Enabled {
			continue
		}
		modelID := request.Model
		if endpoint.ModelOverride != "" {
			modelID = endpoint.ModelOverride
		}
		if modelID == "" {
			modelID = provider.Model
		}
		if formalAudit && !formalCapabilityEligible(endpoint, modelID) {
			continue
		}
		eligible++
		if !r.breakerAllows(endpoint.ID) {
			lastErr = &ProviderError{Kind: ErrorCircuitOpen, Retryable: true, ProviderID: provider.ID, EndpointID: endpoint.ID, Message: "endpoint circuit is open"}
			continue
		}
		client, err := r.registry.Client(endpoint.APIStyle)
		if err != nil {
			return nil, err
		}
		attempts := endpoint.MaxRetries + 1
		if attempts < 1 {
			attempts = 1
		}
		for attempt := 0; attempt < attempts; attempt++ {
			totalAttempts++
			candidate := request
			candidate.Model = modelID
			if formalAudit && endpoint.Capability != nil {
				candidate.OutputMode = endpoint.Capability.OutputMode
			}
			candidate = PrepareStructuredRequest(candidate)
			started := r.now()
			response, callErr := client.Complete(ctx, endpoint, candidate)
			if callErr == nil {
				r.recordSuccess(endpoint.ID)
				response.ProviderID, response.EndpointID, response.APIStyle = provider.ID, endpoint.ID, endpoint.APIStyle
				response.Latency, response.AttemptCount = r.now().Sub(started), totalAttempts
				return response, nil
			}
			providerErr := AsProviderError(callErr)
			providerErr.ProviderID, providerErr.EndpointID = provider.ID, endpoint.ID
			lastErr = providerErr
			if !providerErr.Retryable {
				r.recordNonRetryable(endpoint.ID)
				return nil, providerErr
			}
			r.recordFailure(endpoint.ID)
			if attempt+1 >= attempts {
				break
			}
			delay := retryDelay(attempt, providerErr.RetryAfter)
			if delay > 0 {
				delay += time.Duration(r.random.Int63n(int64(delay/5 + 1)))
				if err := r.sleep(ctx, delay); err != nil {
					return nil, err
				}
			}
		}
	}
	if eligible == 0 {
		return nil, &ProviderError{Kind: ErrorNoEligible, Retryable: false, ProviderID: provider.ID, Message: "no endpoint with a current audit-ready capability profile is eligible"}
	}
	if lastErr == nil {
		lastErr = errors.New("all enabled endpoints failed")
	}
	return nil, &ProviderError{Kind: ErrorAllEndpoints, Retryable: false, ProviderID: provider.ID, Message: "all eligible endpoints failed", Cause: lastErr}
}

// CompleteEndpoint performs a single bounded call on the selected endpoint.
// It is used for repair so the protocol and route cannot silently change.
func (r *Router) CompleteEndpoint(ctx context.Context, provider RuntimeProvider, endpointID string, request Request) (*Response, error) {
	for _, endpoint := range provider.Endpoints {
		if endpoint.ID != endpointID || !endpoint.Enabled {
			continue
		}
		client, err := r.registry.Client(endpoint.APIStyle)
		if err != nil {
			return nil, err
		}
		if endpoint.ModelOverride != "" {
			request.Model = endpoint.ModelOverride
		} else if request.Model == "" {
			request.Model = provider.Model
		}
		if endpoint.Capability != nil {
			request.OutputMode = endpoint.Capability.OutputMode
		}
		request = PrepareStructuredRequest(request)
		started := r.now()
		response, err := client.Complete(ctx, endpoint, request)
		if err != nil {
			return nil, err
		}
		response.ProviderID, response.EndpointID, response.APIStyle = provider.ID, endpoint.ID, endpoint.APIStyle
		response.Latency, response.AttemptCount = r.now().Sub(started), 1
		return response, nil
	}
	return nil, &ProviderError{Kind: ErrorNotFound, Retryable: false, ProviderID: provider.ID, EndpointID: endpointID, Message: "repair endpoint is unavailable"}
}

func formalCapabilityEligible(endpoint RuntimeEndpoint, modelID string) bool {
	capability := endpoint.Capability
	if capability == nil || capability.ProviderProfileVersion != model.AuditProviderProfileVersion || capability.EndpointID != endpoint.ID || capability.Model != modelID || capability.ConfigDigest != ConfigDigest(endpoint, modelID) {
		return false
	}
	return CapabilityAuditReady(capability)
}

func retryDelay(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	if attempt == 0 {
		return 250 * time.Millisecond
	}
	return 750 * time.Millisecond
}

func (r *Router) breakerAllows(endpointID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.breakers[endpointID]
	if state == nil || state.openUntil.IsZero() {
		return true
	}
	if r.now().Before(state.openUntil) {
		return false
	}
	if state.halfOpen {
		return false
	}
	state.halfOpen = true
	return true
}

func (r *Router) recordSuccess(endpointID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.breakers, endpointID)
}

func (r *Router) recordNonRetryable(endpointID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state := r.breakers[endpointID]; state != nil {
		state.halfOpen = false
	}
}

func (r *Router) recordFailure(endpointID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state := r.breakers[endpointID]
	if state == nil {
		state = &breakerState{}
		r.breakers[endpointID] = state
	}
	state.failures++
	state.halfOpen = false
	if state.failures >= 3 {
		state.openUntil = r.now().Add(30 * time.Second)
	}
}
