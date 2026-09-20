package capability

import (
	"context"
	"errors"
	"strconv"

	"github.com/OboardProject/oboard/internal/mcpauth"
)

// PluginPackageDescriptors is appended to the unified catalog by NewCatalog.
// Secrets intentionally have no machine descriptor.
func PluginPackageDescriptors() []Descriptor {
	text := map[string]any{"type": "string"}
	boolean := map[string]any{"type": "boolean"}
	number := map[string]any{"type": "integer", "minimum": 0}
	id := map[string]any{"type": "string", "pattern": "^[1-9][0-9]*$", "maxLength": 19}
	jsonValue := map[string]any{"type": []string{"string", "number", "integer", "boolean", "array", "object", "null"}}
	object := map[string]any{"type": "object", "additionalProperties": jsonValue}
	digest := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	zip := map[string]any{"type": "string", "contentEncoding": "base64", "maxLength": 11184812}
	confirmed := map[string]any{"type": "boolean", "const": true}
	installation := closedObject(map[string]any{"plugin_id": number, "package_id": text, "active_revision_id": number, "installed": boolean, "config": object, "updated_at": text}, "plugin_id", "package_id", "active_revision_id", "installed", "config", "updated_at")
	version := closedObject(map[string]any{"plugin_id": number, "revision_id": number, "version": text, "sha256": digest, "manifest": object, "ui": map[string]any{"type": []string{"object", "null"}, "additionalProperties": jsonValue}, "source_kind": text, "source_repository": text, "source_commit": text, "created_at": text}, "plugin_id", "revision_id", "version", "sha256", "manifest", "ui", "source_kind", "source_repository", "source_commit", "created_at")
	diff := closedObject(map[string]any{"added": arrayOf(text), "removed": arrayOf(text), "unchanged": arrayOf(text)}, "added", "removed", "unchanged")
	metadata := closedObject(map[string]any{"plugin_id": text, "name": text, "version": text, "description": text}, "plugin_id", "name", "version", "description")
	source := closedObject(map[string]any{"kind": text, "repository": text, "commit": map[string]any{"type": "string", "pattern": "^[0-9a-f]{40}$"}}, "kind", "repository", "commit")
	previewSchema := schemaObject(map[string]any{"metadata": metadata, "sha256": digest, "existing_plugin_id": number, "active_revision_id": number, "capabilities": diff, "source_compiled": boolean, "has_ui": boolean, "source": source}, "metadata", "sha256", "existing_plugin_id", "active_revision_id", "capabilities", "source_compiled", "has_ui")
	repositoryURL := map[string]any{"type": "string", "minLength": 1, "maxLength": 512}
	commit := map[string]any{"type": "string", "pattern": "^[0-9a-f]{40}$"}
	ref := func(_ context.Context, input any) ([]mcpauth.ResourceRef, error) {
		values, err := canonicalMap(input)
		if err != nil {
			return nil, err
		}
		raw, ok := values["plugin_id"].(string)
		if !ok {
			return nil, errors.New("plugin_id must be a positive integer string")
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return nil, errors.New("invalid plugin_id")
		}
		return []mcpauth.ResourceRef{{Type: "plugin", ID: strconv.FormatInt(n, 10)}}, nil
	}
	descriptors := []Descriptor{
		{Name: "plugins.packages.preview", Description: "校验 ZIP、编译但不执行源码，预览包摘要及权限差异", InputSchema: schemaObject(map[string]any{"package_zip": zip}, "package_zip"), OutputSchema: previewSchema, ReadOnly: true, RBACPermission: "plugins.read", RequiredScopes: []string{"plugins:read"}, ResolveResourceRefs: noRefs},
		{Name: "plugins.packages.install", Description: "按已预览摘要安装不可变版本；首次安装关闭运行，更新不自动激活或授予权限", InputSchema: schemaObject(map[string]any{"package_zip": zip, "expected_sha256": digest, "confirm": confirmed}, "package_zip", "expected_sha256", "confirm"), OutputSchema: schemaObject(map[string]any{"installation": installation, "version": version, "capabilities": diff}, "installation", "version", "capabilities"), RBACPermission: "plugins.publish", RequiredScopes: []string{"plugins:publish"}, ResolveResourceRefs: noRefs},
		{Name: "plugins.github.preview", Description: "将公开 GitHub 仓库分支或标签解析为不可变提交，校验插件包并预览权限", InputSchema: schemaObject(map[string]any{"repository_url": repositoryURL, "ref": map[string]any{"type": "string", "maxLength": 255}}, "repository_url"), OutputSchema: previewSchema, ReadOnly: true, RBACPermission: "plugins.read", RequiredScopes: []string{"plugins:read"}, ResolveResourceRefs: noRefs},
		{Name: "plugins.github.install", Description: "仅安装已审阅提交与摘要；不跟随分支变化，不自动授权或激活更新", InputSchema: schemaObject(map[string]any{"repository_url": repositoryURL, "commit": commit, "expected_sha256": digest, "confirm": confirmed}, "repository_url", "commit", "expected_sha256", "confirm"), OutputSchema: schemaObject(map[string]any{"installation": installation, "version": version, "capabilities": diff}, "installation", "version", "capabilities"), RBACPermission: "plugins.publish", RequiredScopes: []string{"plugins:publish"}, ResolveResourceRefs: noRefs},
		{Name: "plugins.versions.list", Description: "读取已安装包的不可变版本、摘要及来源", InputSchema: schemaObject(map[string]any{"plugin_id": id}, "plugin_id"), OutputSchema: schemaObject(map[string]any{"versions": arrayOf(version)}, "versions"), ReadOnly: true, RBACPermission: "plugins.read", RequiredScopes: []string{"plugins:read"}, ResolveResourceRefs: ref},
		{Name: "plugins.versions.activate", Description: "显式选择活动版本，不启用运行、不复用旧授权、不重绑触发器", InputSchema: schemaObject(map[string]any{"plugin_id": id, "revision_id": id, "confirm": confirmed}, "plugin_id", "revision_id", "confirm"), OutputSchema: schemaObject(map[string]any{"installation": installation}, "installation"), RBACPermission: "plugins.publish", RequiredScopes: []string{"plugins:publish"}, ResolveResourceRefs: ref},
		{Name: "plugins.config.get", Description: "读取插件非敏感配置，不返回密钥", InputSchema: schemaObject(map[string]any{"plugin_id": id}, "plugin_id"), OutputSchema: schemaObject(map[string]any{"installation": installation}, "installation"), ReadOnly: true, RBACPermission: "plugins.read", RequiredScopes: []string{"plugins:read"}, ResolveResourceRefs: ref},
		{Name: "plugins.config.update", Description: "根据活动版本 Schema 校验并保存非敏感配置，最多 64 KiB", InputSchema: schemaObject(map[string]any{"plugin_id": id, "config": object}, "plugin_id", "config"), OutputSchema: schemaObject(map[string]any{"installation": installation}, "installation"), RBACPermission: "plugins.draft", RequiredScopes: []string{"plugins:write"}, ResolveResourceRefs: ref},
		{Name: "plugins.ui.get", Description: "读取活动版本的声明式页面，不包含可执行前端代码", InputSchema: schemaObject(map[string]any{"plugin_id": id}, "plugin_id"), OutputSchema: schemaObject(map[string]any{"ui": map[string]any{"type": []string{"object", "null"}, "additionalProperties": jsonValue}}, "ui"), ReadOnly: true, RBACPermission: "plugins.read", RequiredScopes: []string{"plugins:read"}, ResolveResourceRefs: ref},
		{Name: "plugins.uninstall", Description: "停止触发器、取消运行、撤销授权并卸载；保留历史审计，显式选择是否保留私有状态", InputSchema: schemaObject(map[string]any{"plugin_id": id, "keep_state": boolean, "confirm": confirmed}, "plugin_id", "keep_state", "confirm"), OutputSchema: schemaObject(map[string]any{"uninstalled": boolean}, "uninstalled"), RBACPermission: "plugins.draft", RequiredScopes: []string{"plugins:write", "plugins:triggers", "plugins:cancel"}, ResolveResourceRefs: ref},
	}
	for i := range descriptors {
		d := &descriptors[i]
		d.ResourceTypes = []string{"plugin"}
		d.Idempotent = true
		d.MCPEnabled = true
		d.DataClassification = DataInternal
		if d.ReadOnly {
			d.MinimumAccess = mcpauth.AccessRead
		} else {
			d.MinimumAccess = mcpauth.AccessOperate
			d.Executable = true
			d.RiskClass = 3
			d.ApprovalPolicy = "required"
		}
	}
	return descriptors
}
