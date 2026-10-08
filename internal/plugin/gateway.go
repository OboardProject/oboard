package plugin

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" // #nosec G505 -- HMAC-SHA1 is required by third-party API signing schemes (for example Aliyun).
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginhttp"
	"github.com/OboardProject/oboard/internal/pluginrpc"
)

// callContext is everything the gateway re-derived for one SDK call. It is
// rebuilt on every call so revocation, disable or deletion takes effect on
// the very next call of a running plugin.
type callContext struct {
	run      model.PluginRun
	loaded   loadedInstallation
	instance model.PluginInstance
	grant    Grant
	spec     CapabilitySpec
	method   string
	deadline time.Time
}

// Invoke is the Capability Gateway. The effective permission for a call is
//
//	manifest declaration ∩ administrator grant ∩ resource scope ∩ runtime policy
//
// and any failing term returns a structured error.
func (s *Service) Invoke(ctx context.Context, request pluginrpc.CallRequest) pluginrpc.CallResponse {
	call, failure := s.authorizeCall(ctx, request)
	if failure != nil {
		return failCall(failure)
	}
	callCtx, cancel := context.WithDeadline(ctx, earliest(call.deadline, s.now().Add(call.spec.Timeout)))
	defer cancel()
	started := s.now()
	result, resource, detail, err := s.dispatch(callCtx, call, request.Arguments)
	if err != nil && errors.Is(callCtx.Err(), context.DeadlineExceeded) && CodeOf(err) == CodeInternal {
		err = Fail(CodeOperationTimeout, "operation timed out")
	}
	if call.spec.Audited {
		s.audit(ctx, call, resource, detail, err, s.now().Sub(started))
	}
	if err != nil {
		return failCall(AsError(err))
	}
	raw, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return failCall(Fail(CodeInternal, "internal error"))
	}
	return pluginrpc.CallResponse{OK: true, Result: raw}
}

func failCall(err *Error) pluginrpc.CallResponse {
	return pluginrpc.CallResponse{OK: false, Code: err.Code, Message: err.Message}
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Service) authorizeCall(ctx context.Context, request pluginrpc.CallRequest) (callContext, *Error) {
	var call callContext
	run, err := s.store.GetPluginRunByUUID(ctx, request.RunUUID)
	if err != nil {
		return call, Fail(CodeCancelled, "run is not active")
	}
	now := s.now()
	if request.WorkerID == "" || run.LeaseOwner != request.WorkerID || run.LeaseGeneration != request.LeaseGeneration || run.Status != model.PluginRunRunning || run.LeaseUntil == nil || !run.LeaseUntil.After(now) {
		return call, Fail(CodeCancelled, "run lease is no longer valid")
	}
	if run.CancelRequested {
		return call, Fail(CodeCancelled, "run was cancelled")
	}
	spec, ok := CapabilityForMethod(request.Method)
	if !ok {
		return call, Fail(CodeUnsupportedCapability, "unsupported SDK method")
	}
	settings := s.Settings(ctx)
	if !settings.Enabled {
		return call, Fail(CodePluginDisabled, "plugin execution is disabled")
	}
	loaded, instance, loadErr := s.loadInstance(ctx, run.InstanceID)
	if loadErr != nil || loaded.pkg == nil || loaded.manifest == nil {
		return call, Fail(CodePluginDisabled, "plugin is no longer installed")
	}
	if !loaded.installation.Enabled || !instance.Enabled {
		return call, Fail(CodePluginDisabled, "plugin is disabled")
	}
	if loaded.pkg.ID != run.PackageID {
		return call, Fail(CodePluginDisabled, "plugin version changed during the run")
	}
	deadline := s.runDeadline(run, *loaded.manifest)
	if !now.Before(deadline) {
		return call, Fail(CodeRunTimeout, "run deadline reached")
	}
	if !loaded.manifest.HasCapability(spec.Name) {
		return call, Fail(CodeCapabilityDenied, "the manifest does not declare "+spec.Name)
	}
	if instance.PermissionReviewRequired {
		return call, Fail(CodeCapabilityDenied, "permissions are waiting for administrator review")
	}
	record, grantErr := s.store.GetPluginGrant(ctx, instance.ID)
	if grantErr != nil || record.PackageID != run.PackageID {
		return call, Fail(CodeCapabilityDenied, "no administrator grant for this version")
	}
	grant, parseErr := ParseGrant(record.GrantJSON)
	if parseErr != nil || !grant.Allows(spec.Name) {
		return call, Fail(CodeCapabilityDenied, spec.Name+" is not granted")
	}
	if operatorTriggered(run.Trigger) {
		if failure := s.checkCaller(ctx, run); failure != nil {
			return call, failure
		}
	}
	limits, _ := EffectiveLimits(loaded.manifest.Limits, settings.MaxTimeout)
	allowed, budgetErr := s.store.ConsumePluginRunBudget(ctx, run.UUID, run.LeaseGeneration, "capability", limits.SDKCalls)
	if budgetErr != nil {
		return call, Fail(CodeInternal, "internal error")
	}
	if !allowed {
		return call, Fail(CodeLimitExceeded, "SDK call budget for this run is exhausted")
	}
	if !s.limiter.allow(strconv.FormatInt(instance.ID, 10)+"|"+spec.Name, spec.RatePerMinute, now) {
		return call, Fail(CodeRateLimited, spec.Name+" rate limit reached; try again later")
	}
	return callContext{run: run, loaded: loaded, instance: instance, grant: grant, spec: spec, method: request.Method, deadline: deadline}, nil
}

// checkCaller re-verifies the human or machine that started a manual run.
func (s *Service) checkCaller(ctx context.Context, run model.PluginRun) *Error {
	var detail TriggerDetail
	if json.Unmarshal(run.TriggerJSON, &detail) != nil || detail.Caller == nil {
		return Fail(CodeCapabilityDenied, "the caller of this run can no longer be verified")
	}
	caller, err := s.host.ResolveCaller(ctx, *detail.Caller)
	if err != nil || caller.ID != detail.Caller.PrincipalID || s.require(caller, PermExecute) != nil {
		return Fail(CodeCapabilityDenied, "the caller of this run is no longer allowed to execute plugins")
	}
	return nil
}

func (s *Service) callerAllowsServer(ctx context.Context, run model.PluginRun, serverID int64) bool {
	if !operatorTriggered(run.Trigger) {
		return true
	}
	var detail TriggerDetail
	if json.Unmarshal(run.TriggerJSON, &detail) != nil || detail.Caller == nil {
		return false
	}
	caller, err := s.host.ResolveCaller(ctx, *detail.Caller)
	return err == nil && caller.AllowsInt64("server_ids", serverID)
}

func (s *Service) audit(ctx context.Context, call callContext, resource string, detail map[string]any, err error, duration time.Duration) {
	result := "succeeded"
	code := ""
	if err != nil {
		result, code = "failed", CodeOf(err)
		if code == CodeCapabilityDenied || code == CodeResourceDenied || code == CodeHTTPHostDenied || code == CodeHTTPPrivateAddressDenied || code == CodeTargetNotAllowed {
			result = "denied"
		}
	}
	_ = s.store.InsertPluginAuditEvent(ctx, model.PluginAuditEvent{
		InstallationID: call.loaded.installation.ID, InstanceID: call.instance.ID, PluginKey: call.run.PluginKey, PluginVersion: call.run.PluginVersion,
		RunUUID: call.run.UUID, Capability: call.spec.Name, Resource: resource, Result: result, ErrorCode: code, DurationMS: duration.Milliseconds(), DetailJSON: mustMarshal(detail),
	})
}

func decodeArgs(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return Fail(CodeInvalidArgument, "invalid arguments: "+sanitizeDecodeError(err))
	}
	return nil
}

func (s *Service) dispatch(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	switch call.spec.Name {
	case CapServersRead, CapServersHealthRead, CapServersMetricsRead:
		return s.handleServers(ctx, call, raw)
	case CapNetworkPing, CapNetworkTrace, CapNetworkTCPProbe, CapNetworkDNSLookup, CapNetworkHTTPProbe:
		return s.handleNetwork(ctx, call, raw)
	case CapHTTPRequest:
		return s.handleHTTP(ctx, call, raw)
	case CapStateRead, CapStateWrite:
		return s.handleState(ctx, call, raw)
	case CapSecretsUse:
		return s.handleHMAC(ctx, call, raw)
	case CapNotificationsSend:
		return s.handleNotify(ctx, call, raw)
	case CapUIPage:
		return s.handlePublish(ctx, call, raw)
	default:
		return nil, "", nil, Fail(CodeUnsupportedCapability, "unsupported capability")
	}
}

type serverArgs struct {
	ServerID string `json:"server_id"`
}

func parseServerArg(raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != strings.TrimSpace(raw) {
		return 0, Fail(CodeInvalidArgument, "server_id must be a stable server ID string")
	}
	return id, nil
}

// serverInScope enforces grant ∩ caller scope ∩ existence for one server.
// An environment value naming a server never widens this.
func (s *Service) serverInScope(ctx context.Context, call callContext, capability string, id int64) (ServerInfo, error) {
	if !call.grant.AllowsServer(capability, id) || !s.callerAllowsServer(ctx, call.run, id) {
		return ServerInfo{}, Fail(CodeResourceDenied, "server "+strconv.FormatInt(id, 10)+" is outside the granted scope for "+capability)
	}
	server, ok := s.host.Server(ctx, id)
	if !ok {
		return ServerInfo{}, Fail(CodeServerNotFound, "server "+strconv.FormatInt(id, 10)+" no longer exists")
	}
	return server, nil
}

func serverView(server ServerInfo) map[string]any {
	view := map[string]any{
		"server_id": strconv.FormatInt(server.ID, 10), "name": server.Name, "status": server.Status,
		"region_code": server.RegionCode, "public_ipv4": server.PublicIPv4, "public_ipv6": server.PublicIPv6,
		"enrolled": server.Enrolled, "online": server.Online, "agent_version": server.AgentVersion,
	}
	if server.LastSeenAt != nil {
		view["last_seen_at"] = server.LastSeenAt.UTC().Format(time.RFC3339)
	}
	return view
}

func (s *Service) handleServers(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	if call.method == "servers.list" {
		if err := decodeArgs(raw, &struct{}{}); err != nil {
			return nil, "", nil, err
		}
		scope := call.grant.Capabilities[CapServersRead].Servers
		out := []map[string]any{}
		for _, id := range scope {
			if !s.callerAllowsServer(ctx, call.run, id) {
				continue
			}
			if server, ok := s.host.Server(ctx, id); ok {
				out = append(out, serverView(server))
			}
		}
		return map[string]any{"servers": out}, "", nil, nil
	}
	var args serverArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	id, err := parseServerArg(args.ServerID)
	if err != nil {
		return nil, "", nil, err
	}
	server, err := s.serverInScope(ctx, call, call.spec.Name, id)
	if err != nil {
		return nil, "server:" + args.ServerID, nil, err
	}
	switch call.spec.Name {
	case CapServersHealthRead:
		view, err := s.host.ServerHealth(ctx, id)
		return view, "server:" + args.ServerID, nil, err
	case CapServersMetricsRead:
		view, err := s.host.ServerMetrics(ctx, id)
		return view, "server:" + args.ServerID, nil, err
	default:
		return serverView(server), "server:" + args.ServerID, nil, nil
	}
}

func (s *Service) handleState(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	var args struct {
		Key             string          `json:"key"`
		Prefix          string          `json:"prefix"`
		Value           json.RawMessage `json:"value"`
		ExpectedVersion *int64          `json:"expected_version"`
		Limit           int             `json:"limit"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	validKey := func(key string) error {
		if key == "" || len(key) > MaxStateKeyBytes || !utf8.ValidString(key) || hasControl(key, false) {
			return Fail(CodeInvalidArgument, "state key must be 1-128 bytes of printable text")
		}
		return nil
	}
	instanceID := call.instance.ID
	op := map[string]string{"state.get": "get", "state.list": "list", "state.set": "set", "state.delete": "delete", "state.compareAndSwap": "cas"}[call.method]
	switch op {
	case "get":
		if err := validKey(args.Key); err != nil {
			return nil, "", nil, err
		}
		entry, err := s.store.GetPluginState(ctx, instanceID, args.Key)
		if notFound(err) {
			return map[string]any{"key": args.Key, "exists": false, "version": 0}, "", nil, nil
		}
		if err != nil {
			return nil, "", nil, storeError(err)
		}
		return map[string]any{"key": entry.Key, "exists": true, "version": entry.Version, "value": entry.ValueJSON, "updated_at": entry.UpdatedAt.UTC().Format(time.RFC3339)}, "", nil, nil
	case "list":
		if len(args.Prefix) > MaxStateKeyBytes {
			return nil, "", nil, Fail(CodeInvalidArgument, "prefix is too long")
		}
		entries, err := s.store.ListPluginState(ctx, instanceID, args.Prefix, args.Limit)
		if err != nil {
			return nil, "", nil, storeError(err)
		}
		items := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			items = append(items, map[string]any{"key": entry.Key, "version": entry.Version, "value": entry.ValueJSON})
		}
		return map[string]any{"entries": items}, "", nil, nil
	}
	if call.spec.Name != CapStateWrite {
		return nil, "", nil, Fail(CodeCapabilityDenied, "state.write is required")
	}
	if err := validKey(args.Key); err != nil {
		return nil, "", nil, err
	}
	switch op {
	case "set", "cas":
		value := bytes.TrimSpace(args.Value)
		if len(value) == 0 || !json.Valid(value) {
			return nil, "", nil, Fail(CodeInvalidArgument, "state value must be JSON")
		}
		if len(value) > MaxStateValueBytes {
			return nil, "", nil, Fail(CodeStateQuotaExceeded, "state value exceeds 16 KiB")
		}
		expected := args.ExpectedVersion
		if op == "cas" && expected == nil {
			return nil, "", nil, Fail(CodeInvalidArgument, "compareAndSwap requires expected_version (0 creates)")
		}
		if op == "set" {
			expected = nil
		}
		var compact bytes.Buffer
		_ = json.Compact(&compact, value)
		entry, err := s.store.PutPluginState(ctx, instanceID, args.Key, compact.Bytes(), expected, MaxStateKeys, MaxStateTotalBytes)
		if err != nil {
			return nil, "", nil, storeError(err)
		}
		return map[string]any{"key": entry.Key, "version": entry.Version}, "", nil, nil
	case "delete":
		deleted, err := s.store.DeletePluginState(ctx, instanceID, args.Key, args.ExpectedVersion)
		if err != nil {
			return nil, "", nil, storeError(err)
		}
		return map[string]any{"key": args.Key, "deleted": deleted}, "", nil, nil
	default:
		return nil, "", nil, Fail(CodeInvalidArgument, "unknown state operation")
	}
}

// secretRefArg is the wire form of a SecretRef produced by the runtime.
type secretRefArg struct {
	Secret string `json:"$secret"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

// resolveSecret turns an instance-bound reference into plaintext for use
// inside the gateway only. A reference to another instance never resolves.
func (s *Service) resolveSecret(ctx context.Context, call callContext, ref secretRefArg) (string, error) {
	if !call.grant.Allows(CapSecretsUse) || !call.loaded.manifest.HasCapability(CapSecretsUse) {
		return "", Fail(CodeCapabilityDenied, "secrets.use is not granted")
	}
	instanceID, name, ok := ParseSecretRef(ref.Secret)
	if !ok || instanceID != call.instance.ID {
		return "", Fail(CodeSecretNotConfigured, "secret reference does not belong to this plugin instance")
	}
	if !secretNameExpected(call.loaded.manifest, call.instance, name) {
		return "", Fail(CodeSecretNotConfigured, "secret "+name+" is not declared")
	}
	if len(ref.Prefix) > 64 || len(ref.Suffix) > 64 {
		return "", Fail(CodeInvalidArgument, "secret prefix/suffix is too long")
	}
	encrypted, err := s.store.GetPluginSecretEncrypted(ctx, call.instance.ID, name)
	if err != nil {
		return "", Fail(CodeSecretNotConfigured, "secret "+name+" is not configured")
	}
	plain, err := s.host.DecryptSecret(encrypted)
	if err != nil || plain == "" {
		return "", Fail(CodeSecretNotConfigured, "secret "+name+" is not available")
	}
	return ref.Prefix + plain + ref.Suffix, nil
}

// handleHMAC computes an HMAC whose key is a SecretRef. The digest leaves
// the gateway; the secret never does.
func (s *Service) handleHMAC(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	var args struct {
		Algorithm    string       `json:"algorithm"`
		Key          secretRefArg `json:"key"`
		Data         string       `json:"data"`
		DataEncoding string       `json:"data_encoding"`
		Output       string       `json:"output"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	var constructor func() hash.Hash
	switch args.Algorithm {
	case "sha256":
		constructor = sha256.New
	case "sha1":
		constructor = sha1.New
	default:
		return nil, "", nil, Fail(CodeInvalidArgument, "algorithm must be sha256 or sha1")
	}
	data, err := decodeData(args.Data, args.DataEncoding)
	if err != nil {
		return nil, "", nil, err
	}
	key, err := s.resolveSecret(ctx, call, args.Key)
	if err != nil {
		return nil, "", nil, err
	}
	mac := hmac.New(constructor, []byte(key))
	mac.Write(data)
	digest, err := encodeOutput(mac.Sum(nil), args.Output)
	if err != nil {
		return nil, "", nil, err
	}
	_, name, _ := ParseSecretRef(args.Key.Secret)
	return map[string]any{"digest": digest}, "secret:" + name, map[string]any{"algorithm": args.Algorithm, "secret": name}, nil
}

func decodeData(data, encoding string) ([]byte, error) {
	if len(data) > 1<<20 {
		return nil, Fail(CodeInvalidArgument, "data exceeds 1 MiB")
	}
	switch encoding {
	case "", "utf8":
		return []byte(data), nil
	case "hex":
		out, err := hex.DecodeString(data)
		if err != nil {
			return nil, Fail(CodeInvalidArgument, "data is not hex")
		}
		return out, nil
	case "base64":
		out, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, Fail(CodeInvalidArgument, "data is not base64")
		}
		return out, nil
	default:
		return nil, Fail(CodeInvalidArgument, "data_encoding must be utf8, hex or base64")
	}
}

func encodeOutput(sum []byte, output string) (string, error) {
	switch output {
	case "", "hex":
		return hex.EncodeToString(sum), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(sum), nil
	case "base64url":
		return base64.RawURLEncoding.EncodeToString(sum), nil
	default:
		return "", Fail(CodeInvalidArgument, "output must be hex, base64 or base64url")
	}
}

type httpArgs struct {
	URL             string                     `json:"url"`
	Method          string                     `json:"method"`
	Headers         map[string]json.RawMessage `json:"headers"`
	Query           map[string]json.RawMessage `json:"query"`
	Body            *string                    `json:"body"`
	BodyEncoding    string                     `json:"body_encoding"`
	Auth            *httpAuth                  `json:"auth"`
	TimeoutMS       int                        `json:"timeout_ms"`
	FollowRedirects bool                       `json:"follow_redirects"`
	MaxResponse     int64                      `json:"max_response_bytes"`
}

type httpAuth struct {
	Type     string       `json:"type"`
	Username string       `json:"username,omitempty"`
	Secret   secretRefArg `json:"secret"`
}

// headerValue accepts a plain string or a SecretRef object.
func (s *Service) headerValue(ctx context.Context, call callContext, raw json.RawMessage) (string, bool, error) {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain, false, nil
	}
	var ref secretRefArg
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&ref) != nil || ref.Secret == "" {
		return "", false, Fail(CodeInvalidArgument, "header and query values must be strings or secret references")
	}
	value, err := s.resolveSecret(ctx, call, ref)
	return value, true, err
}

func (s *Service) handleHTTP(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	var args httpArgs
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	limits, _ := EffectiveLimits(call.loaded.manifest.Limits, s.Settings(ctx).MaxTimeout)
	allowed, err := s.store.ConsumePluginRunBudget(ctx, call.run.UUID, call.run.LeaseGeneration, "http", limits.HTTPRequests)
	if err != nil {
		return nil, "", nil, Fail(CodeInternal, "internal error")
	}
	if !allowed {
		return nil, "", nil, Fail(CodeLimitExceeded, "HTTP request budget for this run is exhausted")
	}
	parsed, err := pluginhttp.ParseURL(args.URL)
	host := ""
	if parsed != nil {
		host = parsed.Hostname()
	}
	method := strings.ToUpper(strings.TrimSpace(args.Method))
	if method == "" {
		method = "GET"
	}
	detail := map[string]any{"method": method, "host": host}
	if err != nil {
		if errors.Is(err, pluginhttp.ErrPrivateAddress) {
			return nil, "http:" + host, detail, Fail(CodeHTTPPrivateAddressDenied, "private, loopback and metadata addresses are never reachable")
		}
		return nil, "http:" + host, detail, Fail(CodeInvalidArgument, "url must be an absolute https URL without credentials or fragment")
	}
	secrets := []string{}
	plainHeaders, secretHeaders := map[string]string{}, map[string]string{}
	if len(args.Headers) > pluginhttp.MaxRequestHeaders {
		return nil, "http:" + host, detail, Fail(CodeInvalidArgument, "too many headers")
	}
	for name, value := range args.Headers {
		text, isSecret, err := s.headerValue(ctx, call, value)
		if err != nil {
			return nil, "http:" + host, detail, err
		}
		if isSecret {
			secretHeaders[name] = text
			secrets = append(secrets, text)
		} else {
			plainHeaders[name] = text
		}
	}
	if args.Auth != nil {
		secret, err := s.resolveSecret(ctx, call, args.Auth.Secret)
		if err != nil {
			return nil, "http:" + host, detail, err
		}
		switch args.Auth.Type {
		case "bearer":
			secretHeaders["Authorization"] = "Bearer " + secret
		case "basic":
			if strings.Contains(args.Auth.Username, ":") || len(args.Auth.Username) > 256 {
				return nil, "http:" + host, detail, Fail(CodeInvalidArgument, "invalid basic auth username")
			}
			secretHeaders["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(args.Auth.Username+":"+secret))
		default:
			return nil, "http:" + host, detail, Fail(CodeInvalidArgument, "auth.type must be bearer or basic")
		}
		secrets = append(secrets, secret)
		detail["auth"] = args.Auth.Type
	}
	if len(args.Query) > 0 {
		values := parsed.Query()
		if len(args.Query) > 64 {
			return nil, "http:" + host, detail, Fail(CodeInvalidArgument, "too many query parameters")
		}
		secretQuery := false
		for name, value := range args.Query {
			text, isSecret, err := s.headerValue(ctx, call, value)
			if err != nil {
				return nil, "http:" + host, detail, err
			}
			if isSecret {
				secrets = append(secrets, text)
				secretQuery = true
			}
			values.Set(name, text)
		}
		parsed.RawQuery = values.Encode()
		if secretQuery {
			args.FollowRedirects = false
		}
	}
	var body []byte
	if args.Body != nil {
		switch args.BodyEncoding {
		case "", "utf8":
			body = []byte(*args.Body)
		case "base64":
			body, err = base64.StdEncoding.DecodeString(*args.Body)
			if err != nil {
				return nil, "http:" + host, detail, Fail(CodeInvalidArgument, "body is not base64")
			}
		default:
			return nil, "http:" + host, detail, Fail(CodeInvalidArgument, "body_encoding must be utf8 or base64")
		}
	}
	timeout := time.Duration(args.TimeoutMS) * time.Millisecond
	if args.TimeoutMS < 0 || timeout > pluginhttp.MaxTimeout {
		return nil, "http:" + host, detail, Fail(CodeInvalidArgument, "timeout_ms must be at most 30000")
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); timeout == 0 || timeout > remaining {
			timeout = remaining
		}
	}
	policy := pluginhttp.Policy{
		Hosts: call.grant.HTTPHosts(), Methods: call.loaded.manifest.HTTP.Methods, MaxResponseBytes: args.MaxResponse,
		Deny: func(ip netip.Addr) bool { return s.host.HTTPDenied(ctx, ip) },
	}
	request := pluginhttp.Request{URL: parsed.String(), Method: method, Headers: plainHeaders, SecretHeaders: secretHeaders, Body: body, Timeout: timeout, FollowRedirects: args.FollowRedirects && len(secrets) == 0}
	response, err := pluginhttp.Do(ctx, request, policy)
	if err != nil {
		return nil, "http:" + host, detail, mapHTTPError(err)
	}
	detail["status"] = response.Status
	detail["bytes"] = len(response.Body)
	payload := pluginhttp.RedactBytes(response.Body, secrets)
	headers := map[string]string{}
	for name, value := range response.Headers {
		headers[name] = pluginhttp.Redact(value, secrets)
	}
	out := map[string]any{"status": response.Status, "headers": headers, "redirects": response.Redirects, "final_url": redactURL(response.FinalURL, secrets)}
	if utf8.Valid(payload) {
		out["body"] = string(payload)
		if strings.Contains(headers["content-type"], "json") {
			var decoded any
			if json.Unmarshal(payload, &decoded) == nil {
				out["json"] = decoded
			}
		}
	} else {
		out["body_base64"] = base64.StdEncoding.EncodeToString(payload)
	}
	return out, "http:" + host, detail, nil
}

func redactURL(raw string, secrets []string) string {
	if u, err := url.Parse(raw); err == nil && u.RawQuery != "" && len(secrets) > 0 {
		return pluginhttp.Redact(raw, secrets)
	}
	return raw
}

func mapHTTPError(err error) error {
	switch {
	case errors.Is(err, pluginhttp.ErrHostDenied):
		return Fail(CodeHTTPHostDenied, "the host is not in the granted HTTP allowlist")
	case errors.Is(err, pluginhttp.ErrMethodDenied):
		return Fail(CodeHTTPHostDenied, "the HTTP method is not declared in the manifest")
	case errors.Is(err, pluginhttp.ErrPrivateAddress):
		return Fail(CodeHTTPPrivateAddressDenied, "the host resolves to a private or reserved address")
	case errors.Is(err, pluginhttp.ErrTimeout), errors.Is(err, pluginhttp.ErrCanceled):
		return Fail(CodeHTTPTimeout, "the HTTP request timed out")
	case errors.Is(err, pluginhttp.ErrResponseTooLarge):
		return Fail(CodeHTTPResponseTooLarge, "the HTTP response exceeds the size limit")
	case errors.Is(err, pluginhttp.ErrRequestTooLarge), errors.Is(err, pluginhttp.ErrInvalidRequest):
		return Fail(CodeInvalidArgument, "the HTTP request is invalid or too large")
	case errors.Is(err, pluginhttp.ErrDNS):
		return Fail(CodeHTTPFailed, "the host could not be resolved")
	default:
		return Fail(CodeHTTPFailed, "the HTTP request failed")
	}
}

func (s *Service) handleNotify(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	var args struct {
		Title      string   `json:"title"`
		Body       string   `json:"body"`
		ChannelIDs []string `json:"channel_ids"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	title, body := strings.TrimSpace(args.Title), strings.TrimSpace(args.Body)
	if title == "" || len(title) > 200 || len(body) > 4000 || hasControl(title, false) || hasControl(body, true) {
		return nil, "", nil, Fail(CodeInvalidArgument, "title is required (≤200 bytes), body at most 4000 bytes")
	}
	channels := call.grant.Capabilities[CapNotificationsSend].Channels
	if len(args.ChannelIDs) > 0 {
		channels = nil
		for _, raw := range args.ChannelIDs {
			id, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || id <= 0 {
				return nil, "", nil, Fail(CodeInvalidArgument, "channel_ids must be ID strings")
			}
			if !call.grant.AllowsChannel(id) {
				return nil, "channel:" + raw, nil, Fail(CodeResourceDenied, "notification channel "+raw+" is not granted")
			}
			channels = append(channels, id)
		}
	}
	secrets := s.instanceSecretValues(ctx, call.instance.ID)
	title, body = pluginhttp.Redact(title, secrets), pluginhttp.Redact(body, secrets)
	sent := []string{}
	for _, id := range channels {
		if err := s.host.SendNotification(ctx, id, title, body); err != nil {
			return map[string]any{"sent": sent}, "channel:" + strconv.FormatInt(id, 10), map[string]any{"channels": len(channels)}, Fail(CodeOperationFailed, "notification delivery failed")
		}
		sent = append(sent, strconv.FormatInt(id, 10))
	}
	return map[string]any{"sent": sent}, "channels", map[string]any{"channels": len(sent)}, nil
}
