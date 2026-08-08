package aiprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

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
