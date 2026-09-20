package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginnetwork"
	"github.com/OboardProject/oboard/internal/pluginrpc"
)

var manageCapabilities = map[string]bool{
	SDKManagementApply:               true,
	SDKNetworkRequest:                true,
	model.PluginSDKServicesRestart:   true,
	model.PluginSDKHostPoweroff:      true,
	model.PluginSDKHostReboot:        true,
	model.PluginSDKNotificationsSend: true,
}

var powerCapabilities = map[string]bool{
	model.PluginSDKHostPoweroff: true,
	model.PluginSDKHostReboot:   true,
}

type SDKHost interface {
	GetServer(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error)
	ListServers(ctx context.Context, principal application.Principal, limit int) ([]map[string]any, error)
	ServerStatus(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error)
	LatestMetrics(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error)
	GetIncident(ctx context.Context, principal application.Principal, incidentID int64) (map[string]any, error)
	ServiceStatus(ctx context.Context, principal application.Principal, serverID int64, service string) (map[string]any, error)
	RestartService(ctx context.Context, principal application.Principal, run model.PluginRun, action model.PluginRunAction, serverID int64, service string) (map[string]any, error)
	HostPower(ctx context.Context, principal application.Principal, run model.PluginRun, action model.PluginRunAction, serverID int64, actionName, reason string) (map[string]any, error)
	SendNotification(ctx context.Context, principal application.Principal, run model.PluginRun, action model.PluginRunAction, channelID int64, title, body string) (map[string]any, error)
	GetOperation(ctx context.Context, principal application.Principal, operationID string) (map[string]any, error)
	WaitOperation(ctx context.Context, principal application.Principal, operationID string, seconds int) (map[string]any, error)
}

type Gateway struct {
	store StoreView
	host  SDKHost
	locks [64]sync.Mutex
}

type StoreView interface {
	GetPluginRunByUUID(ctx context.Context, uuid string) (model.PluginRun, error)
	GetPluginRevision(ctx context.Context, id int64) (model.PluginRevision, error)
	GetPluginGrant(ctx context.Context, id int64) (model.PluginGrant, error)
	GetServerPluginPolicy(ctx context.Context, serverID int64) (model.ServerPluginPolicy, error)
	CreatePluginRunAction(ctx context.Context, item *model.PluginRunAction) error
	GetPluginState(ctx context.Context, pluginID int64, key string) (model.PluginStateEntry, error)
	CompareAndSetPluginState(ctx context.Context, pluginID int64, key string, expectedVersion int64, value json.RawMessage) (model.PluginStateEntry, error)
	CountPluginStateKeys(ctx context.Context, pluginID int64) (int, error)
	ListPluginRunActions(ctx context.Context, runID int64) ([]model.PluginRunAction, error)
}

func NewGateway(store StoreView, host SDKHost) *Gateway {
	return &Gateway{store: store, host: host}
}

type sdkArgs struct {
	ServerID    string                 `json:"server_id"`
	IncidentID  string                 `json:"incident_id"`
	OperationID string                 `json:"operation_id"`
	Service     string                 `json:"service"`
	ChannelID   string                 `json:"channel_id"`
	Title       string                 `json:"title"`
	Body        string                 `json:"body"`
	Reason      string                 `json:"reason"`
	Key         string                 `json:"key"`
	Expected    int64                  `json:"expected_version"`
	Value       json.RawMessage        `json:"value"`
	Limit       int                    `json:"limit"`
	WaitSeconds int                    `json:"wait_seconds"`
	Management  *ManagementRequest     `json:"management,omitempty"`
	Request     *pluginnetwork.Request `json:"request,omitempty"`
	Secret      string                 `json:"secret,omitempty"`
}

func (g *Gateway) Invoke(ctx context.Context, svc *Service, req pluginrpc.SDKRequest) (pluginrpc.SDKResponse, error) {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(req.RunUUID))
	lock := &g.locks[hash.Sum32()%uint32(len(g.locks))]
	lock.Lock()
	defer lock.Unlock()
	if ctx.Err() != nil {
		return failSDK(codeCancelled, "request cancelled"), nil
	}
	run, err := g.store.GetPluginRunByUUID(ctx, req.RunUUID)
	if err != nil {
		return failSDK(codeInvalidInput, "run not found"), nil
	}
	if req.WorkerID == "" || run.LeaseOwner != req.WorkerID || run.LeaseUntil == nil || !run.LeaseUntil.After(svc.now()) || run.LeaseGeneration != req.LeaseGeneration || run.Status != model.PluginRunRunning {
		return failSDK(codeCancelled, "run lease is stale or cancelled"), nil
	}
	if !allowedSDK[req.Capability] {
		return failSDK(codeInvalidInput, "capability is not on the SDK whitelist"), nil
	}
	if err := strictJSON(req.Arguments, &sdkArgs{}); err != nil && strings.TrimSpace(string(req.Arguments)) != "" && string(req.Arguments) != "{}" {
		var probe map[string]json.RawMessage
		if json.Unmarshal(req.Arguments, &probe) != nil {
			return failSDK(codeInvalidInput, "arguments must be a closed object"), nil
		}
		allowed := map[string]bool{"server_id": true, "incident_id": true, "operation_id": true, "service": true, "channel_id": true, "title": true, "body": true, "reason": true, "key": true, "expected_version": true, "value": true, "limit": true, "wait_seconds": true, "management": true, "request": true, "secret": true}
		for key := range probe {
			if !allowed[key] {
				return failSDK(codeInvalidInput, "unknown argument: "+key), nil
			}
		}
	}
	var args sdkArgs
	if len(req.Arguments) > 0 {
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return failSDK(codeInvalidInput, "arguments are not JSON"), nil
		}
	}
	principal, err := svc.EffectivePrincipal(ctx, run)
	if err != nil {
		return failSDK(CodeOf(err), err.Error()), nil
	}
	rev, err := g.store.GetPluginRevision(ctx, run.RevisionID)
	if err != nil {
		return failSDK(codeInvalidInput, "revision not found"), nil
	}
	manifest, err := ParseManifest(rev.ManifestJSON)
	if err != nil {
		return failSDK(CodeOf(err), err.Error()), nil
	}
	if !containsString(manifest.Capabilities, req.Capability) {
		return failSDK(codePermissionDenied, "revision does not declare this capability"), nil
	}
	if !containsString(principal.Scopes, req.Capability) && !containsString(principal.Scopes, "*") {
		return failSDK(codePermissionDenied, "effective identity does not include this capability"), nil
	}
	if strings.HasPrefix(req.Capability, "management.") {
		if args.Management == nil || !ManagementCapabilityAllowed(args.Management.Capability) || !containsString(principal.Scopes, "management:"+args.Management.Capability) {
			return failSDK(codePermissionDenied, "nested management capability is not authorized"), nil
		}
	}
	actions, err := g.store.ListPluginRunActions(ctx, run.ID)
	if err != nil {
		return failSDK(codeRuntimeUnavailable, "cannot verify SDK budget"), nil
	}
	if req.Capability == model.PluginSDKOperationsGet || req.Capability == model.PluginSDKOperationsWait {
		owned := false
		for _, action := range actions {
			if action.OperationID != "" && action.OperationID == args.OperationID {
				owned = true
				break
			}
		}
		if !owned {
			return failSDK(codePermissionDenied, "operation does not belong to this run"), nil
		}
	}
	limits := EffectiveLimits(manifest.Limits, svc.Settings(ctx).MaxTimeoutSeconds)
	if len(actions)+1 > limits.SDKCalls {
		return failSDK(codeLimitExceeded, "SDK call limit exceeded"), nil
	}
	manageCount := 0
	powerTargets := map[string]bool{}
	for _, item := range actions {
		if manageCapabilities[item.Capability] {
			manageCount++
		}
		if powerCapabilities[item.Capability] {
			var target struct {
				ServerID string `json:"server_id"`
			}
			if json.Unmarshal(item.TargetJSON, &target) != nil {
				return failSDK(codeRuntimeUnavailable, "invalid action target"), nil
			}
			powerTargets[target.ServerID] = true
		}
	}
	if manageCapabilities[req.Capability] && manageCount+1 > limits.ManageActions {
		return failSDK(codeLimitExceeded, "manage action limit exceeded"), nil
	}
	if powerCapabilities[req.Capability] {
		if !powerTargets[args.ServerID] && len(powerTargets)+1 > DefaultHostPowerTargets {
			return failSDK(codeLimitExceeded, "host power is limited to one target per run"), nil
		}
	}
	if run.Mode == model.PluginRunModeSimulate && (manageCapabilities[req.Capability] || req.Capability == model.PluginSDKStateCAS || req.Capability == model.PluginSDKServicesStatus || req.Capability == SDKManagementPreview) {
		operationID := "sim_" + req.ActionKey
		return pluginrpc.SDKResponse{OK: true, OperationID: operationID, Result: MustJSON(map[string]any{"simulated": true, "capability": req.Capability, "operation_id": operationID})}, nil
	}
	digest, err := DigestJSON(map[string]any{"capability": req.Capability, "arguments": json.RawMessage(orEmptyJSON(req.Arguments))})
	if err != nil {
		return failSDK(codeInvalidInput, err.Error()), nil
	}
	action := model.PluginRunAction{
		RunID: run.ID, ActionKey: strings.TrimSpace(req.ActionKey), Capability: req.Capability,
		TargetJSON:    MustJSON(map[string]any{"server_id": args.ServerID, "incident_id": args.IncidentID, "operation_id": args.OperationID}),
		PayloadDigest: digest, Status: model.PluginActionPending, LeaseGeneration: run.LeaseGeneration,
	}
	if action.ActionKey == "" {
		action.ActionKey = fmt.Sprintf("%s:%d", req.Capability, len(actions)+1)
	}
	if len(action.ActionKey) > 128 {
		return failSDK(codeInvalidInput, "action key too long"), nil
	}
	for _, existing := range actions {
		if existing.ActionKey != action.ActionKey {
			continue
		}
		if existing.PayloadDigest != digest {
			return failSDK(codeIdempotencyConflict, "same action key with a different payload"), nil
		}
		if existing.Status == model.PluginActionPending {
			return failSDK(codeResultUnknown, "previous action outcome is unknown"), nil
		}
		return g.existingAction(existing), nil
	}
	if err := g.store.CreatePluginRunAction(ctx, &action); err != nil {
		if strings.Contains(err.Error(), "idempotency_conflict") {
			return failSDK(codeIdempotencyConflict, "same action key with a different payload"), nil
		}
		if action.ID > 0 {
			return g.existingAction(action), nil
		}
		return failSDK(codeInvalidInput, err.Error()), nil
	}
	if action.Status != model.PluginActionPending || action.OperationID != "" {
		return g.existingAction(action), nil
	}
	principal, err = svc.EffectivePrincipal(ctx, run)
	if err != nil || !containsString(principal.Scopes, req.Capability) || strings.HasPrefix(req.Capability, "management.") && !containsString(principal.Scopes, "management:"+args.Management.Capability) {
		action.Status, action.ErrorCode = model.PluginActionFailed, codePermissionDenied
		_ = svc.store.UpdatePluginRunAction(ctx, action)
		return failSDK(codePermissionDenied, "run authorization changed"), nil
	}
	result, err := g.dispatch(ctx, principal, run, action, args)
	if err != nil {
		action.Status = model.PluginActionFailed
		action.ErrorCode = CodeOf(err)
		_ = svc.store.UpdatePluginRunAction(ctx, action)
		return failSDK(CodeOf(err), err.Error()), nil
	}
	action.Status = model.PluginActionAccepted
	if id, _ := result["operation_id"].(string); id != "" {
		action.OperationID = id
		action.Status = model.PluginActionDispatching
	}
	if id, _ := result["changeset_id"].(string); id != "" {
		action.ChangesetID = id
	}
	if id, _ := result["task_id"].(string); id != "" {
		if parsed, err := parseID(id); err == nil {
			action.TaskID = &parsed
		}
	}
	action.ResultJSON = MustJSON(result)
	_ = svc.store.UpdatePluginRunAction(ctx, action)
	resp := pluginrpc.SDKResponse{OK: true, Result: action.ResultJSON, OperationID: action.OperationID, ChangesetID: action.ChangesetID}
	if action.TaskID != nil {
		resp.TaskID = fmt.Sprintf("%d", *action.TaskID)
	}
	return resp, nil
}

func (g *Gateway) existingAction(action model.PluginRunAction) pluginrpc.SDKResponse {
	return pluginrpc.SDKResponse{OK: action.ErrorCode == "", ErrorCode: action.ErrorCode, Result: action.ResultJSON, OperationID: action.OperationID, ChangesetID: action.ChangesetID}
}

func (g *Gateway) dispatch(ctx context.Context, principal application.Principal, run model.PluginRun, action model.PluginRunAction, args sdkArgs) (map[string]any, error) {
	serverID, idErr := parseID(args.ServerID)
	if idErr != nil {
		return nil, Coded(codeInvalidInput, "invalid server_id")
	}
	if serverID > 0 && !principal.AllowsInt64("server_ids", serverID) {
		return nil, ErrResourceOutOfScope
	}
	switch action.Capability {
	case SDKManagementQuery, SDKManagementPreview, SDKManagementApply:
		host, ok := g.host.(ManagementHost)
		if !ok || args.Management == nil {
			return nil, ErrCapabilityUnsupported
		}
		return host.PluginManagement(ctx, principal, run, action, action.Capability, *args.Management)
	case SDKNetworkRequest:
		if args.Request == nil || run.GrantID == nil {
			return nil, ErrPermissionDenied
		}
		rev, err := g.store.GetPluginRevision(ctx, run.RevisionID)
		if err != nil {
			return nil, err
		}
		manifest, err := ParseManifest(rev.ManifestJSON)
		if err != nil {
			return nil, err
		}
		grant, err := g.store.GetPluginGrant(ctx, *run.GrantID)
		if err != nil {
			return nil, err
		}
		policy, err := effectiveNetworkPolicy(manifest, grant)
		if err != nil {
			return nil, err
		}
		var response pluginnetwork.Response
		if args.Secret != "" {
			declared := false
			for _, ref := range manifest.Secrets {
				if ref.Name == args.Secret {
					declared = true
				}
			}
			var constraints struct {
				Secrets []string `json:"secrets"`
			}
			if json.Unmarshal(grant.ConstraintsJSON, &constraints) != nil || !declared || !containsString(constraints.Secrets, args.Secret) {
				return nil, ErrPermissionDenied
			}
			host, ok := g.host.(NetworkSecretHost)
			if !ok {
				return nil, ErrCapabilityUnsupported
			}
			response, err = host.PluginNetworkRequest(ctx, principal, run, *args.Request, policy, args.Secret)
			response = pluginnetwork.Response{Status: response.Status}
		} else {
			response, err = pluginnetwork.Do(ctx, *args.Request, policy)
		}
		if err != nil {
			return nil, Coded(codeInvalidInput, "network request failed")
		}
		return map[string]any{"status": response.Status, "headers": response.Headers, "body": response.Body}, nil
	case model.PluginSDKServersGet:
		return g.host.GetServer(ctx, principal, serverID)
	case model.PluginSDKServersList:
		items, err := g.host.ListServers(ctx, principal, args.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"servers": items}, nil
	case model.PluginSDKServersStatus:
		return g.host.ServerStatus(ctx, principal, serverID)
	case model.PluginSDKMetricsLatest:
		return g.host.LatestMetrics(ctx, principal, serverID)
	case model.PluginSDKIncidentsGet:
		incidentID, _ := parseID(args.IncidentID)
		return g.host.GetIncident(ctx, principal, incidentID)
	case model.PluginSDKServicesStatus:
		return g.host.ServiceStatus(ctx, principal, serverID, args.Service)
	case model.PluginSDKServicesRestart:
		return g.host.RestartService(ctx, principal, run, action, serverID, args.Service)
	case model.PluginSDKHostPoweroff:
		return g.host.HostPower(ctx, principal, run, action, serverID, model.HostPowerActionPoweroff, args.Reason)
	case model.PluginSDKHostReboot:
		return g.host.HostPower(ctx, principal, run, action, serverID, model.HostPowerActionReboot, args.Reason)
	case model.PluginSDKNotificationsSend:
		channelID, _ := parseID(args.ChannelID)
		return g.host.SendNotification(ctx, principal, run, action, channelID, args.Title, args.Body)
	case model.PluginSDKOperationsGet:
		return g.host.GetOperation(ctx, principal, args.OperationID)
	case model.PluginSDKOperationsWait:
		wait := args.WaitSeconds
		if wait <= 0 || wait > OperationsWaitMaxSeconds {
			wait = OperationsWaitMaxSeconds
		}
		return g.host.WaitOperation(ctx, principal, args.OperationID, wait)
	case SDKConfigGet:
		reader, ok := g.store.(interface {
			GetPluginInstallation(context.Context, int64) (model.PluginInstallation, error)
		})
		if !ok {
			return nil, ErrPermissionDenied
		}
		installation, err := reader.GetPluginInstallation(ctx, run.PluginID)
		if err != nil {
			return nil, err
		}
		if !installation.Installed {
			return nil, ErrPermissionDenied
		}
		return map[string]any{"config": json.RawMessage(installation.ConfigJSON)}, nil
	case model.PluginSDKStateGet:
		item, err := g.store.GetPluginState(ctx, run.PluginID, args.Key)
		if err != nil {
			return map[string]any{"key": args.Key, "exists": false, "version": "0"}, nil
		}
		return map[string]any{"key": item.Key, "value": json.RawMessage(item.ValueJSON), "version": fmt.Sprintf("%d", item.Version), "exists": true}, nil
	case model.PluginSDKStateCAS:
		n, _ := g.store.CountPluginStateKeys(ctx, run.PluginID)
		if args.Expected == 0 && n >= 32 {
			return nil, Coded(codeLimitExceeded, "plugin private state quota exceeded")
		}
		item, err := g.store.CompareAndSetPluginState(ctx, run.PluginID, args.Key, args.Expected, args.Value)
		if err != nil {
			return nil, ErrConditionChanged
		}
		return map[string]any{"key": item.Key, "version": fmt.Sprintf("%d", item.Version), "value": json.RawMessage(item.ValueJSON)}, nil
	default:
		return nil, ErrCapabilityUnsupported
	}
}

func failSDK(code, message string) pluginrpc.SDKResponse {
	return pluginrpc.SDKResponse{OK: false, ErrorCode: code, Message: message}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func parseID(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return id, nil
}
