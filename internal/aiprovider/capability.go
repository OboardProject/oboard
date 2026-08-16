package aiprovider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func ConfigDigest(endpoint RuntimeEndpoint, modelID string) string {
	keys := make([]string, 0, len(endpoint.Headers))
	for key := range endpoint.Headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	headers := make([][2]string, 0, len(keys))
	for _, key := range keys {
		headers = append(headers, [2]string{key, endpoint.Headers[key]})
	}
	payload, _ := json.Marshal(struct {
		BaseURL, APIStyle, AuthMode, AnthropicVersion, ModelsPath, GeneratePath, Model string
		Headers                                                                        [][2]string
	}{endpoint.BaseURL, string(endpoint.APIStyle), endpoint.AuthMode, endpoint.AnthropicVersion, endpoint.ModelsPath, endpoint.GeneratePath, modelID, headers})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func ValidateJSONSchema(schema map[string]any, raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("JSON output is empty")
	}
	encodedSchema, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encode JSON schema: %w", err)
	}
	var normalizedSchema any
	if err := json.Unmarshal(encodedSchema, &normalizedSchema); err != nil {
		return fmt.Errorf("normalize JSON schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("oboard-schema.json", normalizedSchema); err != nil {
		return fmt.Errorf("load JSON schema: %w", err)
	}
	compiled, err := compiler.Compile("oboard-schema.json")
	if err != nil {
		return fmt.Errorf("compile JSON schema: %w", err)
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("JSON schema validation failed: %w", err)
	}
	return nil
}

// ExtractJSONObject returns the first complete JSON object from a response.
// Providers commonly wrap otherwise valid JSON in Markdown or brief prose.
func ExtractJSONObject(value string) json.RawMessage {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)
	for offset := 0; offset < len(trimmed); offset++ {
		if trimmed[offset] != '{' {
			continue
		}
		decoder := json.NewDecoder(bytes.NewBufferString(trimmed[offset:]))
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			continue
		}
		var object map[string]any
		if json.Unmarshal(raw, &object) == nil && object != nil {
			return raw
		}
	}
	return nil
}

// PrepareStructuredRequest adds the schema to the prompt when an endpoint
// cannot enforce it through a native response-format parameter.
func PrepareStructuredRequest(input Request) Request {
	if input.Schema == nil || input.OutputMode == "" || input.OutputMode == model.AuditOutputModeStrictSchema {
		return input
	}
	encoded, err := json.Marshal(input.Schema)
	if err != nil {
		return input
	}
	input.System = strings.TrimSpace(input.System) + "\n\n仅输出一个符合以下 JSON Schema 的 JSON 对象，不要使用 Markdown，不要添加解释：\n" + string(encoded)
	return input
}

func CapabilityAuditReady(capability *model.AIProviderCapability) bool {
	if capability == nil || capability.ProviderProfileVersion != model.AuditProviderProfileVersion || !capability.AuditReady || !capability.ConnectivityOK || !capability.AuthenticationOK || !capability.TextSupported {
		return false
	}
	switch capability.OutputMode {
	case model.AuditOutputModeStrictSchema:
		return capability.StructuredOutput == model.AuditProviderStructuredJSONSchema
	case model.AuditOutputModeJSONObject:
		return capability.StructuredOutput == model.AuditProviderStructuredJSONObject
	case model.AuditOutputModeText:
		return capability.StructuredOutput == model.AuditProviderStructuredPromptedJSON
	default:
		return false
	}
}
