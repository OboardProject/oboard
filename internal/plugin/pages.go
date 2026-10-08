package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginhttp"
)

// InstancePage is one declared page plus the latest resolved snapshot.
// Document is omitted until a run publishes one.
type InstancePage struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Actions     []string        `json:"actions"`
	PublishedAt string          `json:"published_at,omitempty"`
	RunID       string          `json:"run_id,omitempty"`
	Document    json.RawMessage `json:"document,omitempty"`
}

// InstancePages is the operator view of every page on one instance.
type InstancePages struct {
	Pages []InstancePage `json:"pages"`
}

// ListInstancePages returns declared pages and resolves bindings for the caller.
// A missing grant or a pending permission review makes the pages unreadable.
func (s *Service) ListInstancePages(ctx context.Context, actor application.Principal, instanceID int64) (InstancePages, error) {
	if err := s.require(actor, PermRead); err != nil {
		return InstancePages{}, err
	}
	loaded, instance, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return InstancePages{}, err
	}
	if loaded.manifest == nil || len(loaded.manifest.Pages) == 0 {
		return InstancePages{Pages: []InstancePage{}}, nil
	}
	if instance.PermissionReviewRequired {
		return InstancePages{}, Fail(CodePermissionReviewRequired, "新版本申请了新的权限，审核前不展示界面")
	}
	record, err := s.store.GetPluginGrant(ctx, instance.ID)
	if err != nil || loaded.pkg == nil || record.PackageID != loaded.pkg.ID {
		return InstancePages{}, Fail(CodeCapabilityDenied, "界面尚未授权")
	}
	grant, err := ParseGrant(record.GrantJSON)
	if err != nil || !grant.Allows(CapUIPage) {
		return InstancePages{}, Fail(CodeCapabilityDenied, "界面尚未授权")
	}
	snapshots, err := s.store.ListPluginPageSnapshots(ctx, instance.ID)
	if err != nil {
		return InstancePages{}, storeError(err)
	}
	byPage := map[string]model.PluginPageSnapshot{}
	for _, snapshot := range snapshots {
		byPage[snapshot.PageID] = snapshot
	}
	values, custom := decodeEnvironment(instance)
	pages := make([]InstancePage, 0, len(loaded.manifest.Pages))
	for _, page := range loaded.manifest.Pages {
		view := InstancePage{ID: page.ID, Title: page.Title, Actions: append([]string(nil), page.Actions...)}
		if view.Actions == nil {
			view.Actions = []string{}
		}
		snapshot, ok := byPage[page.ID]
		if ok {
			view.PublishedAt = snapshot.PublishedAt.UTC().Format(time.RFC3339)
			view.RunID = snapshot.RunUUID
			if resolved, err := ResolveViewDocument(snapshot.DocumentJSON, s.bindingResolver(ctx, actor, *loaded.manifest, grant, values, custom)); err == nil {
				view.Document = resolved
			}
		}
		pages = append(pages, view)
	}
	return InstancePages{Pages: pages}, nil
}

// RefreshInstancePage queues one ui-triggered run. Opening the page does not.
func (s *Service) RefreshInstancePage(ctx context.Context, actor application.Principal, instanceID int64, pageID, idempotencyKey string) (model.PluginRun, error) {
	return s.queuePageRun(ctx, actor, instanceID, pageID, "", idempotencyKey)
}

// RunInstancePageAction queues one action-triggered run. The button cannot call the SDK itself.
func (s *Service) RunInstancePageAction(ctx context.Context, actor application.Principal, instanceID int64, pageID, action, idempotencyKey string) (model.PluginRun, error) {
	if strings.TrimSpace(action) == "" {
		return model.PluginRun{}, FailField(CodeInvalidArgument, "action", "action is required")
	}
	return s.queuePageRun(ctx, actor, instanceID, pageID, action, idempotencyKey)
}

func (s *Service) queuePageRun(ctx context.Context, actor application.Principal, instanceID int64, pageID, action, idempotencyKey string) (model.PluginRun, error) {
	if err := s.require(actor, PermExecute); err != nil {
		return model.PluginRun{}, err
	}
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" || len(idempotencyKey) > 128 {
		return model.PluginRun{}, FailField(CodeInvalidArgument, "idempotency_key", "idempotency_key is required (at most 128 bytes)")
	}
	loaded, instance, err := s.loadInstance(ctx, instanceID)
	if err != nil {
		return model.PluginRun{}, err
	}
	if loaded.manifest == nil {
		return model.PluginRun{}, Fail(CodePluginDisabled, "插件还没有已发布的版本")
	}
	page, ok := loaded.manifest.Page(pageID)
	if !ok {
		return model.PluginRun{}, FailField(CodeInvalidArgument, "page", "unknown page")
	}
	trigger := model.PluginTriggerUI
	if action != "" {
		if !page.HasAction(action) {
			return model.PluginRun{}, FailField(CodeInvalidArgument, "action", "action is not declared on this page")
		}
		trigger = model.PluginTriggerAction
	}
	if err := s.runnable(ctx, loaded, instance); err != nil {
		return model.PluginRun{}, err
	}
	caller := &CallerRef{PrincipalID: actor.ID, Type: string(actor.Type), GrantID: actor.GrantID}
	if actor.SourceIP.IsValid() {
		caller.SourceIP = actor.SourceIP.String()
	}
	key := trigger + ":" + actor.ID + ":" + pageID + ":" + action + ":" + idempotencyKey
	run, created, err := s.enqueue(ctx, loaded, instance, trigger, TriggerDetail{Caller: caller, Page: pageID, Action: action}, key, actor.ID)
	if err != nil {
		return run, err
	}
	if created {
		s.Wake()
	}
	return run, nil
}

func (s *Service) handlePublish(ctx context.Context, call callContext, raw json.RawMessage) (any, string, map[string]any, error) {
	var args struct {
		Page     string          `json:"page"`
		Document json.RawMessage `json:"document"`
	}
	if err := decodeArgs(raw, &args); err != nil {
		return nil, "", nil, err
	}
	page, ok := call.loaded.manifest.Page(args.Page)
	if !ok {
		return nil, "", nil, Fail(CodeInvalidArgument, "unknown page")
	}
	canonical, err := ValidateViewDocument(args.Document, page)
	if err != nil {
		return nil, "page:" + args.Page, nil, err
	}
	secrets := s.instanceSecretValues(ctx, call.instance.ID)
	redacted := pluginhttp.RedactBytes(canonical, secrets)
	if !json.Valid(redacted) {
		return nil, "page:" + args.Page, nil, Fail(CodeInvalidArgument, "document contains a secret and cannot be stored")
	}
	if !bytes.Equal(redacted, canonical) {
		canonical, err = ValidateViewDocument(redacted, page)
		if err != nil {
			return nil, "page:" + args.Page, nil, err
		}
	}
	published := s.now().UTC()
	if err := s.store.UpsertPluginPageSnapshot(ctx, model.PluginPageSnapshot{
		InstanceID: call.instance.ID, PageID: page.ID, DocumentJSON: canonical, RunUUID: call.run.UUID, PublishedAt: published,
	}); err != nil {
		return nil, "page:" + args.Page, nil, storeError(err)
	}
	return map[string]any{"page": page.ID, "published_at": published.Format(time.RFC3339)}, "page:" + page.ID, nil, nil
}

func (s *Service) bindingResolver(ctx context.Context, actor application.Principal, manifest Manifest, grant Grant, values map[string]json.RawMessage, custom []CustomVar) BindingResolver {
	return func(source, envName string) (any, *ViewBindingError) {
		capability, ok := viewBindingCapability[source]
		if !ok || !manifest.HasCapability(capability) {
			return nil, &ViewBindingError{Code: CodeCapabilityDenied, Message: "插件未声明该读取能力"}
		}
		serverID, failure := serverFromEnv(manifest, values, custom, envName)
		if failure != nil {
			return nil, failure
		}
		if !grant.AllowsServer(capability, serverID) || !actor.AllowsInt64("server_ids", serverID) {
			return nil, &ViewBindingError{Code: CodeResourceDenied, Message: "服务器不在授权范围内"}
		}
		switch source {
		case "servers.get":
			server, found := s.host.Server(ctx, serverID)
			if !found {
				return nil, &ViewBindingError{Code: CodeServerNotFound, Message: "服务器已不存在"}
			}
			return serverView(server), nil
		case "servers.health":
			if _, found := s.host.Server(ctx, serverID); !found {
				return nil, &ViewBindingError{Code: CodeServerNotFound, Message: "服务器已不存在"}
			}
			view, err := s.host.ServerHealth(ctx, serverID)
			if err != nil {
				return nil, &ViewBindingError{Code: CodeOperationFailed, Message: "读取健康状态失败"}
			}
			return view, nil
		default:
			if _, found := s.host.Server(ctx, serverID); !found {
				return nil, &ViewBindingError{Code: CodeServerNotFound, Message: "服务器已不存在"}
			}
			view, err := s.host.ServerMetrics(ctx, serverID)
			if err != nil {
				return nil, &ViewBindingError{Code: CodeOperationFailed, Message: "读取资源指标失败"}
			}
			return view, nil
		}
	}
}

func serverFromEnv(manifest Manifest, values map[string]json.RawMessage, custom []CustomVar, name string) (int64, *ViewBindingError) {
	if field, ok := manifest.EnvField(name); ok {
		if field.Type != EnvServer {
			return 0, &ViewBindingError{Code: CodeInvalidEnvironment, Message: "绑定只能引用单个服务器变量"}
		}
		if !FieldActive(manifest.Environment, values, field) {
			return 0, &ViewBindingError{Code: CodeInvalidEnvironment, Message: "绑定的变量当前未启用"}
		}
		return parseBoundServer(values[name])
	}
	for _, item := range custom {
		if item.Name != name {
			continue
		}
		if item.Type != EnvServer {
			return 0, &ViewBindingError{Code: CodeInvalidEnvironment, Message: "绑定只能引用单个服务器变量"}
		}
		return parseBoundServer(item.Value)
	}
	return 0, &ViewBindingError{Code: CodeInvalidEnvironment, Message: "绑定的变量不存在"}
}

func parseBoundServer(raw json.RawMessage) (int64, *ViewBindingError) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == "" {
		return 0, &ViewBindingError{Code: CodeConfigurationRequired, Message: "还没有选择服务器"}
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
		return 0, &ViewBindingError{Code: CodeInvalidEnvironment, Message: "服务器变量无效"}
	}
	return id, nil
}
