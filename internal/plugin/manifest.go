package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/pluginnetwork"
)

var allowedSDK = map[string]bool{
	SDKConfigGet:                     true,
	SDKManagementQuery:               true,
	SDKManagementPreview:             true,
	SDKManagementApply:               true,
	SDKNetworkRequest:                true,
	model.PluginSDKServersGet:        true,
	model.PluginSDKServersList:       true,
	model.PluginSDKServersStatus:     true,
	model.PluginSDKMetricsLatest:     true,
	model.PluginSDKIncidentsGet:      true,
	model.PluginSDKServicesStatus:    true,
	model.PluginSDKServicesRestart:   true,
	model.PluginSDKHostPoweroff:      true,
	model.PluginSDKHostReboot:        true,
	model.PluginSDKNotificationsSend: true,
	model.PluginSDKOperationsGet:     true,
	model.PluginSDKOperationsWait:    true,
	model.PluginSDKStateGet:          true,
	model.PluginSDKStateCAS:          true,
}

var reservedEnv = map[string]bool{
	"OBOARD_RUN_ID":            true,
	"OBOARD_PLUGIN_ID":         true,
	"OBOARD_REVISION_ID":       true,
	"OBOARD_TRIGGER_ID":        true,
	"OBOARD_SCHEDULED_AT":      true,
	"OBOARD_EVENT_SUBJECT_ID":  true,
	"OBOARD_SUBJECT_SERVER_ID": true,
	"OBOARD_TARGET_SERVER_ID":  true,
	"OBOARD_RUN_MODE":          true,
	"RUN_ID":                   true,
	"PLUGIN_ID":                true,
	"REVISION_ID":              true,
	"TRIGGER_ID":               true,
	"SERVER_ID":                true,
}

func ParseManifest(raw json.RawMessage) (model.PluginManifest, error) {
	if len(raw) == 0 {
		return model.PluginManifest{}, Coded(codeInvalidInput, "manifest is required")
	}
	var manifest model.PluginManifest
	if err := strictJSON(raw, &manifest); err != nil {
		return model.PluginManifest{}, Coded(codeInvalidInput, "manifest is not a closed object: "+err.Error())
	}
	if err := ValidateManifest(manifest); err != nil {
		return model.PluginManifest{}, err
	}
	return manifest, nil
}

func ValidateManifest(manifest model.PluginManifest) error {
	if manifest.SchemaVersion != model.PluginSchemaVersion {
		return Coded(codeInvalidInput, "unsupported schema_version")
	}
	if manifest.Runtime != model.PluginRuntimeOBoardJSv1 {
		return Coded(codeInvalidInput, "runtime must be oboard-js-v1")
	}
	if manifest.SDKVersion != model.PluginSDKVersionV1 {
		return Coded(codeInvalidInput, "sdk_version must be oboard-sdk-v1")
	}
	if strings.TrimSpace(manifest.Entry) != "main" {
		return Coded(codeInvalidInput, "entry must be main")
	}
	if len(manifest.Capabilities) == 0 {
		return Coded(codeInvalidInput, "capabilities are required")
	}
	for _, name := range manifest.Capabilities {
		nested, isNested := strings.CutPrefix(name, "management:")
		if name != strings.TrimSpace(name) || (!allowedSDK[name] && !(isNested && ManagementCapabilityAllowed(nested))) {
			return Coded(codeInvalidInput, "capability is not on the first-version SDK whitelist: "+name)
		}
	}
	if manifest.UIContentSHA256 != "" {
		decoded, err := hex.DecodeString(manifest.UIContentSHA256)
		if err != nil || len(decoded) != sha256.Size || strings.ToLower(manifest.UIContentSHA256) != manifest.UIContentSHA256 {
			return Coded(codeInvalidInput, "invalid UI content digest")
		}
	}
	if manifest.Network != nil {
		var policy pluginnetwork.Policy
		if json.Unmarshal(MustJSON(manifest.Network), &policy) != nil || pluginnetwork.ValidatePolicy(policy) != nil || !containsString(manifest.Capabilities, SDKNetworkRequest) {
			return Coded(codeInvalidInput, "invalid network declaration")
		}
	} else if containsString(manifest.Capabilities, SDKNetworkRequest) {
		return Coded(codeInvalidInput, "network.request requires a network declaration")
	}
	for _, env := range manifest.Env {
		name := strings.TrimSpace(env.Name)
		if name == "" || strings.ContainsAny(name, " \t\n=") || reservedEnv[name] {
			return Coded(codeInvalidInput, "environment variable name is reserved or invalid: "+env.Name)
		}
	}
	if err := validateParamSchema(manifest.Params); err != nil {
		return err
	}
	if err := validateParamSchema(manifest.ConfigSchema); err != nil {
		return err
	}
	if len(manifest.ConfigSchema) > 0 && string(manifest.ConfigSchema) != "null" {
		var schema map[string]any
		if json.Unmarshal(manifest.ConfigSchema, &schema) != nil || schema["type"] != "object" {
			return Coded(codeInvalidInput, "configuration schema must declare an object")
		}
	}
	if manifest.Limits.TimeoutSeconds < 0 || manifest.Limits.TimeoutSeconds > MaxTimeoutSeconds {
		return Coded(codeInvalidInput, "declared timeout exceeds the system ceiling")
	}
	if manifest.Limits.MemoryMiB < 0 || manifest.Limits.MemoryMiB > DefaultRunnerMemoryMiB {
		return Coded(codeInvalidInput, "declared memory exceeds the system ceiling")
	}
	if manifest.Limits.SDKCalls < 0 || manifest.Limits.SDKCalls > DefaultSDKCallLimit {
		return Coded(codeInvalidInput, "declared SDK call limit exceeds the system ceiling")
	}
	if manifest.Limits.ManageActions < 0 || manifest.Limits.ManageActions > DefaultManageActionLimit {
		return Coded(codeInvalidInput, "declared manage-action limit exceeds the system ceiling")
	}
	return nil
}

func ValidateSource(source string) error {
	if strings.TrimSpace(source) == "" {
		return Coded(codeInvalidInput, "source is required")
	}
	if len(source) > MaxSourceBytes {
		return Coded(codeLimitExceeded, "source exceeds 256 KiB")
	}
	if strings.Contains(source, "require(") || strings.Contains(source, "import ") {
		return Coded(codeInvalidInput, "dynamic modules are not supported")
	}
	return nil
}

func RevisionDigest(source string, manifest model.PluginManifest) string {
	var canonical any
	if json.Unmarshal(MustJSON(manifest), &canonical) != nil {
		return ""
	}
	digest, _ := DigestJSON(map[string]any{"source": source, "manifest": canonical})
	return digest
}

func SourceDigest(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

func DigestJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func EffectiveLimits(declared model.PluginDeclaredLimits, systemTimeout int) model.PluginDeclaredLimits {
	if systemTimeout <= 0 || systemTimeout > MaxTimeoutSeconds {
		systemTimeout = MaxTimeoutSeconds
	}
	out := model.PluginDeclaredLimits{
		TimeoutSeconds: DefaultTimeoutSeconds,
		MemoryMiB:      DefaultRunnerMemoryMiB,
		SDKCalls:       DefaultSDKCallLimit,
		ManageActions:  DefaultManageActionLimit,
		LogBytes:       MaxLogBytes,
		ResultBytes:    MaxResultBytes,
	}
	if declared.TimeoutSeconds > 0 && declared.TimeoutSeconds < out.TimeoutSeconds {
		out.TimeoutSeconds = declared.TimeoutSeconds
	}
	if out.TimeoutSeconds > systemTimeout {
		out.TimeoutSeconds = systemTimeout
	}
	if declared.MemoryMiB > 0 && declared.MemoryMiB < out.MemoryMiB {
		out.MemoryMiB = declared.MemoryMiB
	}
	if declared.SDKCalls > 0 && declared.SDKCalls < out.SDKCalls {
		out.SDKCalls = declared.SDKCalls
	}
	if declared.ManageActions > 0 && declared.ManageActions < out.ManageActions {
		out.ManageActions = declared.ManageActions
	}
	if declared.LogBytes > 0 && declared.LogBytes < out.LogBytes {
		out.LogBytes = declared.LogBytes
	}
	if declared.ResultBytes > 0 && declared.ResultBytes < out.ResultBytes {
		out.ResultBytes = declared.ResultBytes
	}
	return out
}

func ValidateParams(schema json.RawMessage, params json.RawMessage) error {
	if len(schema) == 0 || string(schema) == "null" {
		if len(params) == 0 || string(params) == "null" || string(params) == "{}" {
			return nil
		}
		return Coded(codeInvalidInput, "parameters are not declared")
	}
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	if len(params) > MaxParamsEnvBytes {
		return Coded(codeLimitExceeded, "parameters exceed 64 KiB")
	}
	compiler := jsonschema.NewCompiler()
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(schema)))
	if err != nil {
		return Coded(codeInvalidInput, "parameter schema is invalid")
	}
	if err := compiler.AddResource("mem://params.json", doc); err != nil {
		return Coded(codeInvalidInput, err.Error())
	}
	sch, err := compiler.Compile("mem://params.json")
	if err != nil {
		return Coded(codeInvalidInput, err.Error())
	}
	inst, err := jsonschema.UnmarshalJSON(strings.NewReader(string(params)))
	if err != nil {
		return Coded(codeInvalidInput, "parameters are not JSON")
	}
	if err := sch.Validate(inst); err != nil {
		return Coded(codeInvalidInput, err.Error())
	}
	return nil
}

func MergeEnv(manifest model.PluginManifest, sets ...map[string]string) (map[string]string, error) {
	declared := map[string]model.PluginEnvDeclaration{}
	out := map[string]string{}
	for _, item := range manifest.Env {
		declared[item.Name] = item
		if item.Default != "" {
			out[item.Name] = item.Default
		}
	}
	for i, set := range sets {
		for key, value := range set {
			if reservedEnv[key] {
				return nil, Coded(codeInvalidInput, "reserved environment variable cannot be set: "+key)
			}
			decl, ok := declared[key]
			if !ok {
				return nil, Coded(codeInvalidInput, "undeclared environment variable: "+key)
			}
			if i > 0 && !decl.Overridable {
				return nil, Coded(codeInvalidInput, "environment variable is not overridable: "+key)
			}
			out[key] = value
		}
	}
	return out, nil
}

func SystemEnv(run model.PluginRun, triggerID int64, scheduledAt, subjectID string) map[string]string {
	binding := ""
	if run.BindingID != nil {
		binding = fmt.Sprintf("%d", *run.BindingID)
	}
	if triggerID > 0 {
		binding = fmt.Sprintf("%d", triggerID)
	}
	return map[string]string{
		"OBOARD_RUN_ID":           run.UUID,
		"OBOARD_PLUGIN_ID":        fmt.Sprintf("%d", run.PluginID),
		"OBOARD_REVISION_ID":      fmt.Sprintf("%d", run.RevisionID),
		"OBOARD_TRIGGER_ID":       binding,
		"OBOARD_SCHEDULED_AT":     scheduledAt,
		"OBOARD_EVENT_SUBJECT_ID": subjectID,
		"OBOARD_RUN_MODE":         run.Mode,
	}
}

func validateParamSchema(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if len(raw) > MaxParamsEnvBytes {
		return Coded(codeLimitExceeded, "parameter schema exceeds 64 KiB")
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return Coded(codeInvalidInput, "parameter schema is not JSON")
	}
	if containsRef(node) {
		return Coded(codeInvalidInput, "external $ref is not allowed")
	}
	nodes, depth := schemaComplexity(node, 1)
	if depth > MaxParamSchemaDepth || nodes > MaxParamSchemaNodes {
		return Coded(codeInvalidInput, "parameter schema is too complex")
	}
	return nil
}

func containsRef(node any) bool {
	switch value := node.(type) {
	case map[string]any:
		if _, ok := value["$ref"]; ok {
			return true
		}
		for _, child := range value {
			if containsRef(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if containsRef(child) {
				return true
			}
		}
	}
	return false
}

func schemaComplexity(node any, depth int) (int, int) {
	switch value := node.(type) {
	case map[string]any:
		nodes, maxDepth := 1, depth
		for _, child := range value {
			childNodes, childDepth := schemaComplexity(child, depth+1)
			nodes += childNodes
			if childDepth > maxDepth {
				maxDepth = childDepth
			}
		}
		return nodes, maxDepth
	case []any:
		nodes, maxDepth := 1, depth
		for _, child := range value {
			childNodes, childDepth := schemaComplexity(child, depth+1)
			nodes += childNodes
			if childDepth > maxDepth {
				maxDepth = childDepth
			}
		}
		return nodes, maxDepth
	default:
		return 1, depth
	}
}
