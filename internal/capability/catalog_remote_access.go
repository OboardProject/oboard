package capability

import (
	"github.com/OboardProject/oboard/internal/mcpauth"
	"github.com/OboardProject/oboard/internal/model"
)

const (
	PrivilegeRemoteOperations     = model.PrivilegeRemoteOperations
	PrivilegeRemoteExec           = model.PrivilegeRemoteExec
	PrivilegeRemoteShell          = model.PrivilegeRemoteShell
	PrivilegeRemoteInteractive    = model.PrivilegeRemoteInteractive
	ApprovalPrivilegedGrant       = model.ApprovalPolicyPrivilegedGrant
	PermissionServersRemoteAccess = "servers.remote_access"
)

func remoteAccessDescriptors(positiveID map[string]any, stringValue, boolValue map[string]any) []Descriptor {
	serverIDInput := schemaObject(map[string]any{"server_id": positiveID}, "server_id")
	execInput := schemaObject(map[string]any{
		"server_id":       positiveID,
		"argv":            map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": map[string]any{"type": "string", "maxLength": 4096}},
		"cwd":             map[string]any{"type": "string", "maxLength": 4096},
		"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300},
	}, "server_id", "argv")
	shellInput := schemaObject(map[string]any{
		"server_id":       positiveID,
		"command":         map[string]any{"type": "string", "minLength": 1, "maxLength": 32768},
		"cwd":             map[string]any{"type": "string", "maxLength": 4096},
		"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 300},
	}, "server_id", "command")
	execOutput := schemaObject(map[string]any{
		"exit_code": map[string]any{"type": "integer"}, "duration_ms": map[string]any{"type": "integer"},
		"stdout": stringValue, "stderr": stringValue, "stdout_truncated": boolValue, "stderr_truncated": boolValue,
		"stdout_bytes": map[string]any{"type": "integer"}, "stderr_bytes": map[string]any{"type": "integer"},
	}, "exit_code")
	privileged := func(name, description, privilege string, input, output []byte, risk int, executable bool) Descriptor {
		return Descriptor{
			Name: name, Description: description, InputSchema: input, OutputSchema: output,
			RequiredScopes: []string{"servers:write"}, ResourceTypes: []string{"server"},
			ResourceEvaluator: "server_ids", RiskClass: risk, ApprovalPolicy: ApprovalPrivilegedGrant,
			Idempotent: false, DataClassification: DataSensitive, MCPEnabled: true, Executable: executable,
			ReadOnly: !executable, MinimumAccess: mcpauth.AccessOperate, PrivilegeClass: privilege,
			RBACPermission: PermissionServersRemoteAccess, ResolveResourceRefs: serverRefFromServerID,
		}
	}
	return []Descriptor{
		privileged("node.system_info", "读取授权服务器的系统信息（内核、发行版、负载、内存）", PrivilegeRemoteOperations, serverIDInput, rawSchema(map[string]any{"type": "object"}), 2, false),
		privileged("node.network_info", "读取授权服务器的网卡与监听概况", PrivilegeRemoteOperations, serverIDInput, rawSchema(map[string]any{"type": "object"}), 2, false),
		privileged("node.disk_usage", "读取授权服务器的磁盘用量", PrivilegeRemoteOperations, serverIDInput, rawSchema(map[string]any{"type": "object"}), 2, false),
		privileged("node.listeners", "读取授权服务器的监听端口", PrivilegeRemoteOperations, serverIDInput, rawSchema(map[string]any{"type": "object"}), 2, false),
		privileged("node.service_status", "读取 OBoard Agent 与内核服务状态", PrivilegeRemoteOperations, schemaObject(map[string]any{"server_id": positiveID, "service": map[string]any{"type": "string", "enum": []string{"", "oboard-agent", "oboard-sb", "all"}}}, "server_id"), rawSchema(map[string]any{"type": "object"}), 2, false),
		privileged("node.restart_service", "重启授权服务器上的 OBoard Agent 或内核服务", PrivilegeRemoteOperations, schemaObject(map[string]any{"server_id": positiveID, "service": map[string]any{"type": "string", "enum": []string{"oboard-agent", "oboard-sb"}}}, "server_id", "service"), rawSchema(map[string]any{"type": "object"}), 3, true),
		privileged("node.get_logs", "读取授权服务器上的 OBoard 服务日志", PrivilegeRemoteOperations, schemaObject(map[string]any{"server_id": positiveID, "services": map[string]any{"type": "string", "enum": []string{"all", "agent", "core"}}, "lines": map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}, "server_id"), rawSchema(map[string]any{"type": "object"}), 2, false),
		privileged("node.run_diagnostics", "对授权服务器运行网络诊断", PrivilegeRemoteOperations, serverIDInput, rawSchema(map[string]any{"type": "object"}), 2, false),
		privileged("node.exec", "以 argv 在授权服务器上执行程序，不经过 shell", PrivilegeRemoteExec, execInput, execOutput, 3, true),
		privileged("node.exec_shell", "以 /bin/sh -c 在授权服务器上执行 shell 表达式", PrivilegeRemoteShell, shellInput, execOutput, 3, true),
	}
}

func (d Descriptor) DefaultGrantable() bool {
	return d.PrivilegeClass == ""
}

func (d Descriptor) Privileged() bool {
	return d.PrivilegeClass != ""
}
