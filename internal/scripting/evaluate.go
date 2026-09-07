package scripting

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/scriptrpc"
)

var manageCapabilities = map[string]bool{
	model.ScriptSDKServicesRestart:   true,
	model.ScriptSDKHostPoweroff:      true,
	model.ScriptSDKHostReboot:        true,
	model.ScriptSDKNotificationsSend: true,
}

var powerCapabilities = map[string]bool{
	model.ScriptSDKHostPoweroff: true,
	model.ScriptSDKHostReboot:   true,
}

type SDKHost interface {
	GetServer(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error)
	ListServers(ctx context.Context, principal application.Principal, limit int) ([]map[string]any, error)
	ServerStatus(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error)
	LatestMetrics(ctx context.Context, principal application.Principal, serverID int64) (map[string]any, error)
	GetIncident(ctx context.Context, principal application.Principal, incidentID int64) (map[string]any, error)
	ServiceStatus(ctx context.Context, principal application.Principal, serverID int64, service string) (map[string]any, error)
	RestartService(ctx context.Context, principal application.Principal, run model.ScriptRun, action model.ScriptRunAction, serverID int64, service string) (map[string]any, error)
	HostPower(ctx context.Context, principal application.Principal, run model.ScriptRun, action model.ScriptRunAction, serverID int64, actionName, reason string) (map[string]any, error)
	SendNotification(ctx context.Context, principal application.Principal, run model.ScriptRun, action model.ScriptRunAction, channelID int64, title, body string) (map[string]any, error)
	GetOperation(ctx context.Context, principal application.Principal, operationID string) (map[string]any, error)
	WaitOperation(ctx context.Context, principal application.Principal, operationID string, seconds int) (map[string]any, error)
}

type Gateway struct {
	store StoreView
	host  SDKHost
}

type StoreView interface {
	GetScriptRunByUUID(ctx context.Context, uuid string) (model.ScriptRun, error)
	GetScriptRevision(ctx context.Context, id int64) (model.ScriptRevision, error)
	GetScriptGrant(ctx context.Context, id int64) (model.ScriptGrant, error)
	GetServerScriptPolicy(ctx context.Context, serverID int64) (model.ServerScriptPolicy, error)
	CreateScriptRunAction(ctx context.Context, item *model.ScriptRunAction) error
	GetScriptState(ctx context.Context, scriptID int64, key string) (model.ScriptStateEntry, error)
	CompareAndSetScriptState(ctx context.Context, scriptID int64, key string, expectedVersion int64, value json.RawMessage) (model.ScriptStateEntry, error)
	CountScriptStateKeys(ctx context.Context, scriptID int64) (int, error)
	ListScriptRunActions(ctx context.Context, runID int64) ([]model.ScriptRunAction, error)
}

func NewGateway(store StoreView, host SDKHost) *Gateway {
	return &Gateway{store: store, host: host}
}

type sdkArgs struct {
	ServerID    string `json:"server_id"`
	IncidentID  string `json:"incident_id"`
	OperationID string `json:"operation_id"`
	Service     string `json:"service"`
	ChannelID   string `json:"channel_id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	Reason      string `json:"reason"`
	Key         string `json:"key"`
	Expected    int64  `json:"expected_version"`
	Value       json.RawMessage `json:"value"`
	Limit       int    `json:"limit"`
	WaitSeconds int    `json:"wait_seconds"`
}

func (g *Gateway) Invoke(ctx context.Context, svc *Service, req scriptrpc.SDKRequest) (scriptrpc.SDKResponse, error) {
	run, err := g.store.GetScriptRunByUUID(ctx, req.RunUUID)
	if err != nil {
		return failSDK(codeInvalidInput, "run not found"), nil
	}
	if run.LeaseGeneration != req.LeaseGeneration || run.Status != model.ScriptRunRunning {
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
		allowed := map[string]bool{"server_id": true, "incident_id": true, "operation_id": true, "service": true, "channel_id": true, "title": true, "body": true, "reason": true, "key": true, "expected_version": true, "value": true, "limit": true, "wait_seconds": true}
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
	rev, err := g.store.GetScriptRevision(ctx, run.RevisionID)
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
	actions, _ := g.store.ListScriptRunActions(ctx, run.ID)
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
			powerTargets[string(item.TargetJSON)] = true
		}
	}
	if manageCapabilities[req.Capability] && manageCount+1 > limits.ManageActions {
		return failSDK(codeLimitExceeded, "manage action limit exceeded"), nil
	}
	if powerCapabilities[req.Capability] {
		target := fmt.Sprintf(`{"server_id":%q}`, args.ServerID)
		if !powerTargets[target] && len(powerTargets)+1 > DefaultHostPowerTargets {
			return failSDK(codeLimitExceeded, "host power is limited to one target per run"), nil
		}
	}
	if run.Mode == model.ScriptRunModeSimulate && manageCapabilities[req.Capability] {
		operationID := "sim_" + req.ActionKey
		return scriptrpc.SDKResponse{OK: true, OperationID: operationID, Result: MustJSON(map[string]any{"simulated": true, "capability": req.Capability, "operation_id": operationID})}, nil
	}
	digest, err := DigestJSON(map[string]any{"capability": req.Capability, "arguments": json.RawMessage(orEmptyJSON(req.Arguments))})
	if err != nil {
		return failSDK(codeInvalidInput, err.Error()), nil
	}
	action := model.ScriptRunAction{
		RunID: run.ID, ActionKey: strings.TrimSpace(req.ActionKey), Capability: req.Capability,
		TargetJSON: MustJSON(map[string]any{"server_id": args.ServerID, "incident_id": args.IncidentID, "operation_id": args.OperationID}),
		PayloadDigest: digest, Status: model.ScriptActionPending, LeaseGeneration: run.LeaseGeneration,
	}
	if action.ActionKey == "" {
		action.ActionKey = fmt.Sprintf("%s:%d", req.Capability, len(actions)+1)
	}
	if err := g.store.CreateScriptRunAction(ctx, &action); err != nil {
		if strings.Contains(err.Error(), "idempotency_conflict") {
			return failSDK(codeIdempotencyConflict, "same action key with a different payload"), nil
		}
		if action.ID > 0 {
			return g.existingAction(action), nil
		}
		return failSDK(codeInvalidInput, err.Error()), nil
	}
	if action.Status != model.ScriptActionPending || action.OperationID != "" {
		return g.existingAction(action), nil
	}
	result, err := g.dispatch(ctx, principal, run, action, args)
	if err != nil {
		action.Status = model.ScriptActionFailed
		action.ErrorCode = CodeOf(err)
		_ = svc.store.UpdateScriptRunAction(ctx, action)
		return failSDK(CodeOf(err), err.Error()), nil
	}
	action.Status = model.ScriptActionAccepted
	if id, _ := result["operation_id"].(string); id != "" {
		action.OperationID = id
		action.Status = model.ScriptActionDispatching
	}
	if id, _ := result["changeset_id"].(string); id != "" {
		action.ChangesetID = id
	}
	action.ResultJSON = MustJSON(result)
	_ = svc.store.UpdateScriptRunAction(ctx, action)
	resp := scriptrpc.SDKResponse{OK: true, Result: action.ResultJSON, OperationID: action.OperationID, ChangesetID: action.ChangesetID}
	if action.TaskID != nil {
		resp.TaskID = fmt.Sprintf("%d", *action.TaskID)
	}
	return resp, nil
}

func (g *Gateway) existingAction(action model.ScriptRunAction) scriptrpc.SDKResponse {
	return scriptrpc.SDKResponse{OK: action.ErrorCode == "", ErrorCode: action.ErrorCode, Result: action.ResultJSON, OperationID: action.OperationID, ChangesetID: action.ChangesetID}
}

func (g *Gateway) dispatch(ctx context.Context, principal application.Principal, run model.ScriptRun, action model.ScriptRunAction, args sdkArgs) (map[string]any, error) {
	serverID, _ := parseID(args.ServerID)
	if serverID > 0 && !principal.AllowsInt64("server_ids", serverID) {
		return nil, ErrResourceOutOfScope
	}
	switch action.Capability {
	case model.ScriptSDKServersGet:
		return g.host.GetServer(ctx, principal, serverID)
	case model.ScriptSDKServersList:
		items, err := g.host.ListServers(ctx, principal, args.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"servers": items}, nil
	case model.ScriptSDKServersStatus:
		return g.host.ServerStatus(ctx, principal, serverID)
	case model.ScriptSDKMetricsLatest:
		return g.host.LatestMetrics(ctx, principal, serverID)
	case model.ScriptSDKIncidentsGet:
		incidentID, _ := parseID(args.IncidentID)
		return g.host.GetIncident(ctx, principal, incidentID)
	case model.ScriptSDKServicesStatus:
		return g.host.ServiceStatus(ctx, principal, serverID, args.Service)
	case model.ScriptSDKServicesRestart:
		return g.host.RestartService(ctx, principal, run, action, serverID, args.Service)
	case model.ScriptSDKHostPoweroff:
		return g.host.HostPower(ctx, principal, run, action, serverID, model.HostPowerActionPoweroff, args.Reason)
	case model.ScriptSDKHostReboot:
		return g.host.HostPower(ctx, principal, run, action, serverID, model.HostPowerActionReboot, args.Reason)
	case model.ScriptSDKNotificationsSend:
		channelID, _ := parseID(args.ChannelID)
		return g.host.SendNotification(ctx, principal, run, action, channelID, args.Title, args.Body)
	case model.ScriptSDKOperationsGet:
		return g.host.GetOperation(ctx, principal, args.OperationID)
	case model.ScriptSDKOperationsWait:
		wait := args.WaitSeconds
		if wait <= 0 || wait > OperationsWaitMaxSeconds {
			wait = OperationsWaitMaxSeconds
		}
		return g.host.WaitOperation(ctx, principal, args.OperationID, wait)
	case model.ScriptSDKStateGet:
		item, err := g.store.GetScriptState(ctx, run.ScriptID, args.Key)
		if err != nil {
			return map[string]any{"key": args.Key, "exists": false, "version": "0"}, nil
		}
		return map[string]any{"key": item.Key, "value": json.RawMessage(item.ValueJSON), "version": fmt.Sprintf("%d", item.Version), "exists": true}, nil
	case model.ScriptSDKStateCAS:
		n, _ := g.store.CountScriptStateKeys(ctx, run.ScriptID)
		if args.Expected == 0 && n >= 32 {
			return nil, Coded(codeLimitExceeded, "script private state quota exceeded")
		}
		item, err := g.store.CompareAndSetScriptState(ctx, run.ScriptID, args.Key, args.Expected, args.Value)
		if err != nil {
			return nil, ErrConditionChanged
		}
		return map[string]any{"key": item.Key, "version": fmt.Sprintf("%d", item.Version), "value": json.RawMessage(item.ValueJSON)}, nil
	default:
		return nil, ErrCapabilityUnsupported
	}
}

func failSDK(code, message string) scriptrpc.SDKResponse {
	return scriptrpc.SDKResponse{OK: false, ErrorCode: code, Message: message}
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
	var id int64
	_, err := fmt.Sscan(raw, &id)
	return id, err
}
