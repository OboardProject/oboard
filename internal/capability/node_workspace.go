package capability

import "github.com/OboardProject/oboard/internal/mcpauth"

func nodeWorkspaceDescriptors(positiveID, stringValue, boolValue map[string]any) []Descriptor {
	userInput := schemaObject(map[string]any{"user_id": positiveID}, "user_id")
	groupIDs := map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": positiveID}
	read := func(name, description string) Descriptor {
		return Descriptor{Name: name, Description: description, InputSchema: userInput, OutputSchema: rawSchema(map[string]any{"type": "object"}), RequiredScopes: []string{"nodes:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: userRefFromID}
	}
	descriptors := []Descriptor{
		read("node_library.list", "列出指定用户可见的脱敏节点库"),
		read("node_groups.list", "列出指定用户的节点组"),
		read("node_sources.list", "列出指定用户的脱敏第三方来源和刷新状态"),
		read("subscription_outputs.list", "列出指定用户的组合订阅"),
		{Name: "subscription_outputs.preview", Description: "预览组合订阅的兼容节点、统计和生成内容", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "output_id": positiveID, "format": stringValue}, "user_id", "output_id", "format"), OutputSchema: rawSchema(map[string]any{"type": "object"}), RequiredScopes: []string{"nodes:read"}, ResourceTypes: []string{"user"}, ResourceEvaluator: "user_ids", ReadOnly: true, Idempotent: true, DataClassification: DataSensitive, MCPEnabled: true, MinimumAccess: mcpauth.AccessRead, ResolveResourceRefs: userRefFromID},
	}
	writes := []Descriptor{
		{Name: "node_groups.create", Description: "创建手动或远程节点组", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "name": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "kind": map[string]any{"type": "string", "enum": []string{"manual", "remote"}}, "url": stringValue, "content": stringValue}, "user_id", "name", "kind"), SensitiveFields: []string{"url", "content"}, SensitiveInput: []string{"url", "content"}},
		{Name: "node_groups.update", Description: "重命名节点组", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "group_id": positiveID, "name": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}}, "user_id", "group_id", "name")},
		{Name: "node_groups.delete", Description: "删除非系统节点组及其私有节点", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "group_id": positiveID}, "user_id", "group_id"), Destructive: true},
		{Name: "node_sources.refresh", Description: "立即拉取并刷新第三方订阅来源", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "source_id": positiveID}, "user_id", "source_id"), OpenWorld: true},
		{Name: "node_library.update", Description: "更新手动导入节点的名称、启用状态或节点配置", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "node_id": stringValue, "name": stringValue, "content": stringValue, "enabled": boolValue}, "user_id", "node_id"), SensitiveFields: []string{"content"}, SensitiveInput: []string{"content"}},
		{Name: "subscription_outputs.save", Description: "创建或更新有序组合订阅（含 Sub-Store 式过滤规则）", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "output_id": map[string]any{"type": "integer", "minimum": 0}, "name": map[string]any{"type": "string", "minLength": 1, "maxLength": 80}, "group_ids": groupIDs, "filters": map[string]any{"type": "array", "maxItems": 32, "items": schemaObject(map[string]any{"type": map[string]any{"type": "string", "enum": []string{"keep_name", "drop_name", "keep_protocol", "drop_protocol", "keep_region", "drop_region", "keep_group", "drop_group"}}, "value": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}}, "type", "value")}, "enabled": boolValue}, "user_id", "name", "group_ids")},
		{Name: "subscription_outputs.delete", Description: "删除非默认组合订阅", InputSchema: schemaObject(map[string]any{"user_id": positiveID, "output_id": positiveID}, "user_id", "output_id"), Destructive: true},
	}
	for _, descriptor := range writes {
		descriptor.OutputSchema = rawSchema(map[string]any{"type": "object"})
		descriptor.RequiredScopes = []string{"nodes:write"}
		descriptor.ResourceTypes = []string{"user"}
		descriptor.ResourceEvaluator = "user_ids"
		descriptor.RiskClass = 2
		descriptor.ApprovalPolicy = "required"
		descriptor.Idempotent = true
		descriptor.DataClassification = DataSensitive
		descriptor.MCPEnabled = true
		descriptor.Executable = true
		descriptor.MinimumAccess = mcpauth.AccessOperate
		descriptor.ResolveResourceRefs = userRefFromID
		descriptors = append(descriptors, descriptor)
	}
	return descriptors
}
