package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Environment value types. The product calls these "环境变量", but they are a
// typed virtual namespace delivered to the runtime; they never become process
// environment variables and never carry permission.
const (
	EnvString      = "string"
	EnvText        = "text"
	EnvInteger     = "integer"
	EnvNumber      = "number"
	EnvBoolean     = "boolean"
	EnvSelect      = "select"
	EnvMultiSelect = "multi_select"
	EnvServer      = "server"
	EnvServers     = "servers"
	EnvSecret      = "secret"
	EnvURL         = "url"
	EnvDuration    = "duration"
	EnvJSON        = "json"
)

var declaredEnvTypes = map[string]bool{
	EnvString: true, EnvText: true, EnvInteger: true, EnvNumber: true, EnvBoolean: true,
	EnvSelect: true, EnvMultiSelect: true, EnvServer: true, EnvServers: true, EnvSecret: true,
	EnvURL: true, EnvDuration: true, EnvJSON: true,
}

// Custom variables have no schema, so types that need declared options are
// not available to them.
var customEnvTypes = map[string]bool{
	EnvString: true, EnvText: true, EnvInteger: true, EnvNumber: true, EnvBoolean: true,
	EnvServer: true, EnvServers: true, EnvSecret: true, EnvURL: true, EnvDuration: true, EnvJSON: true,
}

// Server selector UI filters. They only narrow the picker; authorization is
// always the grant, checked again at run time.
var serverFilters = map[string]bool{"enrolled": true, "online": true, "ipv4": true, "ipv6": true}

const (
	maxStringBytes   = 1024
	maxTextBytes     = 16 << 10
	maxJSONBytes     = 16 << 10
	maxJSONDepth     = 16
	maxURLBytes      = 2048
	maxServersItems  = 64
	maxSelectOptions = 64
	maxSafeInteger   = 1<<53 - 1
	minDurationValue = time.Second
	maxDurationValue = 30 * 24 * time.Hour
)

type EnvField struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Label       string          `json:"label"`
	Description string          `json:"description,omitempty"`
	Required    bool            `json:"required,omitempty"`
	Default     json.RawMessage `json:"default,omitempty"`
	Placeholder string          `json:"placeholder,omitempty"`
	MinLength   *int            `json:"min_length,omitempty"`
	MaxLength   *int            `json:"max_length,omitempty"`
	Pattern     string          `json:"pattern,omitempty"`
	Min         *float64        `json:"min,omitempty"`
	Max         *float64        `json:"max,omitempty"`
	MinDuration string          `json:"min_duration,omitempty"`
	MaxDuration string          `json:"max_duration,omitempty"`
	Options     []EnvOption     `json:"options,omitempty"`
	MinItems    *int            `json:"min_items,omitempty"`
	MaxItems    *int            `json:"max_items,omitempty"`
	Filter      []string        `json:"filter,omitempty"`
	Schemes     []string        `json:"schemes,omitempty"`
	DependsOn   *EnvCondition   `json:"depends_on,omitempty"`
}

type EnvOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// EnvCondition is the only conditional form: show the field when another,
// earlier field equals a literal value. No expressions are evaluated.
type EnvCondition struct {
	Field  string          `json:"field"`
	Equals json.RawMessage `json:"equals"`
}

// CustomVar is an administrator-added variable without manifest schema.
type CustomVar struct {
	Name  string          `json:"name"`
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value,omitempty"`
}

func ValidEnvName(name string) bool {
	return envNamePattern.MatchString(name) && !strings.HasPrefix(name, "OBOARD_")
}

// ValidateEnvironmentSchema validates the manifest environment declaration.
func ValidateEnvironmentSchema(fields []EnvField, capabilities []string) error {
	if len(fields) > maxEnvFields {
		return FailField(CodeInvalidManifest, "environment", "at most 64 environment fields")
	}
	hasSecretsUse := false
	for _, name := range capabilities {
		hasSecretsUse = hasSecretsUse || name == CapSecretsUse
	}
	seen := map[string]EnvField{}
	for i := range fields {
		field := &fields[i]
		path := "environment." + field.Name
		if !envNamePattern.MatchString(field.Name) {
			return FailField(CodeInvalidManifest, "environment", fmt.Sprintf("field %q must match [A-Z][A-Z0-9_]*", field.Name))
		}
		if strings.HasPrefix(field.Name, "OBOARD_") {
			return FailField(CodeInvalidManifest, path, "the OBOARD_ prefix is reserved")
		}
		if _, dup := seen[field.Name]; dup {
			return FailField(CodeInvalidManifest, path, "duplicate environment field")
		}
		if !declaredEnvTypes[field.Type] {
			return FailField(CodeInvalidManifest, path+".type", "unsupported type "+field.Type)
		}
		if strings.TrimSpace(field.Label) == "" || len(field.Label) > 80 || hasControl(field.Label, false) {
			return FailField(CodeInvalidManifest, path+".label", "label is required and at most 80 bytes")
		}
		if len(field.Description) > 512 || hasControl(field.Description, true) || len(field.Placeholder) > 128 || hasControl(field.Placeholder, false) {
			return FailField(CodeInvalidManifest, path, "description or placeholder is too long")
		}
		if field.Type == EnvSecret && !hasSecretsUse {
			return FailField(CodeInvalidManifest, path, "secret fields require the secrets.use capability")
		}
		if err := validateFieldConstraints(field, path); err != nil {
			return err
		}
		if len(field.Default) > 0 {
			switch field.Type {
			case EnvSecret, EnvServer, EnvServers:
				return FailField(CodeInvalidManifest, path+".default", "secret and server fields cannot have defaults")
			}
			normalized, err := NormalizeEnvValue(*field, field.Default)
			if err != nil {
				return FailField(CodeInvalidManifest, path+".default", "default: "+err.Error())
			}
			field.Default = normalized
		}
		if field.DependsOn != nil {
			target, ok := seen[field.DependsOn.Field]
			if !ok {
				return FailField(CodeInvalidManifest, path+".depends_on", "depends_on must reference an earlier field")
			}
			switch target.Type {
			case EnvBoolean, EnvSelect, EnvString, EnvInteger:
			default:
				return FailField(CodeInvalidManifest, path+".depends_on", "depends_on may only reference boolean, select, string or integer fields")
			}
			if len(field.DependsOn.Equals) == 0 {
				return FailField(CodeInvalidManifest, path+".depends_on", "depends_on.equals is required")
			}
			normalized, err := NormalizeEnvValue(target, field.DependsOn.Equals)
			if err != nil {
				return FailField(CodeInvalidManifest, path+".depends_on", "equals: "+err.Error())
			}
			field.DependsOn.Equals = normalized
		}
		seen[field.Name] = *field
	}
	return nil
}

func validateFieldConstraints(field *EnvField, path string) error {
	allowed := map[string]bool{}
	mark := func(names ...string) {
		for _, name := range names {
			allowed[name] = true
		}
	}
	switch field.Type {
	case EnvString, EnvText:
		mark("length", "pattern")
	case EnvInteger, EnvNumber:
		mark("range")
	case EnvSelect:
		mark("options")
	case EnvMultiSelect:
		mark("options", "items")
	case EnvServer:
		mark("filter")
	case EnvServers:
		mark("filter", "items")
	case EnvURL:
		mark("schemes")
	case EnvDuration:
		mark("duration")
	}
	present := map[string]bool{
		"length":   field.MinLength != nil || field.MaxLength != nil,
		"pattern":  field.Pattern != "",
		"range":    field.Min != nil || field.Max != nil,
		"options":  len(field.Options) > 0,
		"items":    field.MinItems != nil || field.MaxItems != nil,
		"filter":   len(field.Filter) > 0,
		"schemes":  len(field.Schemes) > 0,
		"duration": field.MinDuration != "" || field.MaxDuration != "",
	}
	for name, has := range present {
		if has && !allowed[name] {
			return FailField(CodeInvalidManifest, path, fmt.Sprintf("%s constraints are not valid for type %s", name, field.Type))
		}
	}
	if field.MinLength != nil && (*field.MinLength < 0 || *field.MinLength > maxTextBytes) || field.MaxLength != nil && (*field.MaxLength < 1 || *field.MaxLength > maxTextBytes) ||
		field.MinLength != nil && field.MaxLength != nil && *field.MinLength > *field.MaxLength {
		return FailField(CodeInvalidManifest, path, "invalid length bounds")
	}
	if field.Pattern != "" {
		if len(field.Pattern) > 256 {
			return FailField(CodeInvalidManifest, path+".pattern", "pattern is at most 256 bytes")
		}
		if _, err := regexp.Compile("^(?:" + field.Pattern + ")$"); err != nil {
			return FailField(CodeInvalidManifest, path+".pattern", "pattern is not a valid RE2 expression")
		}
	}
	if field.Min != nil && (math.IsNaN(*field.Min) || math.IsInf(*field.Min, 0)) || field.Max != nil && (math.IsNaN(*field.Max) || math.IsInf(*field.Max, 0)) ||
		field.Min != nil && field.Max != nil && *field.Min > *field.Max {
		return FailField(CodeInvalidManifest, path, "invalid numeric bounds")
	}
	if field.Type == EnvInteger && (field.Min != nil && *field.Min != math.Trunc(*field.Min) || field.Max != nil && *field.Max != math.Trunc(*field.Max)) {
		return FailField(CodeInvalidManifest, path, "integer bounds must be integers")
	}
	if field.Type == EnvSelect || field.Type == EnvMultiSelect {
		if len(field.Options) == 0 || len(field.Options) > maxSelectOptions {
			return FailField(CodeInvalidManifest, path+".options", "declare between 1 and 64 options")
		}
		values := map[string]bool{}
		for _, option := range field.Options {
			if option.Value == "" || len(option.Value) > 128 || hasControl(option.Value, false) || strings.TrimSpace(option.Label) == "" || len(option.Label) > 80 || hasControl(option.Label, false) || values[option.Value] {
				return FailField(CodeInvalidManifest, path+".options", "options need unique values and non-empty labels")
			}
			values[option.Value] = true
		}
	}
	if field.MinItems != nil && (*field.MinItems < 0 || *field.MinItems > maxServersItems) || field.MaxItems != nil && (*field.MaxItems < 1 || *field.MaxItems > maxServersItems) ||
		field.MinItems != nil && field.MaxItems != nil && *field.MinItems > *field.MaxItems {
		return FailField(CodeInvalidManifest, path, "invalid item bounds")
	}
	filters := map[string]bool{}
	for i, filter := range field.Filter {
		filter = strings.TrimSpace(filter)
		if !serverFilters[filter] || filters[filter] {
			return FailField(CodeInvalidManifest, path+".filter", "unsupported or duplicate server filter "+filter)
		}
		filters[filter] = true
		field.Filter[i] = filter
	}
	sort.Strings(field.Filter)
	for _, scheme := range field.Schemes {
		if scheme != "https" && scheme != "http" {
			return FailField(CodeInvalidManifest, path+".schemes", "url schemes may only be https or http")
		}
	}
	if field.MinDuration != "" || field.MaxDuration != "" {
		minD, maxD, err := durationBounds(*field)
		if err != nil || minD > maxD {
			return FailField(CodeInvalidManifest, path, "invalid duration bounds")
		}
	}
	return nil
}

func durationBounds(field EnvField) (time.Duration, time.Duration, error) {
	minD, maxD := minDurationValue, maxDurationValue
	if field.MinDuration != "" {
		d, _, err := ParseDuration(field.MinDuration)
		if err != nil {
			return 0, 0, err
		}
		minD = d
	}
	if field.MaxDuration != "" {
		d, _, err := ParseDuration(field.MaxDuration)
		if err != nil {
			return 0, 0, err
		}
		maxD = d
	}
	if minD < minDurationValue || maxD > maxDurationValue {
		return 0, 0, fmt.Errorf("duration bounds exceed 1s..30d")
	}
	return minD, maxD, nil
}

// NormalizeEnvValue validates a stored value against its field and returns the
// canonical stored JSON. Secret values are never passed here.
func NormalizeEnvValue(field EnvField, raw json.RawMessage) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, fmt.Errorf("a value is required")
	}
	switch field.Type {
	case EnvString, EnvText:
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return nil, fmt.Errorf("must be text")
		}
		limit := maxStringBytes
		if field.Type == EnvText {
			limit = maxTextBytes
		}
		if field.MaxLength != nil && *field.MaxLength < limit {
			limit = *field.MaxLength
		}
		if !utf8.ValidString(value) || hasControl(value, field.Type == EnvText) {
			return nil, fmt.Errorf("contains control characters")
		}
		if len(value) > limit {
			return nil, fmt.Errorf("is longer than %d bytes", limit)
		}
		if field.MinLength != nil && len(value) < *field.MinLength {
			return nil, fmt.Errorf("is shorter than %d bytes", *field.MinLength)
		}
		if field.Pattern != "" {
			re, err := regexp.Compile("^(?:" + field.Pattern + ")$")
			if err != nil || !re.MatchString(value) {
				return nil, fmt.Errorf("does not match the required format")
			}
		}
		return mustMarshal(value), nil
	case EnvInteger:
		var number json.Number
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if raw[0] == '"' || dec.Decode(&number) != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil || value > maxSafeInteger || value < -maxSafeInteger {
			return nil, fmt.Errorf("must be an integer within ±9007199254740991")
		}
		if field.Min != nil && float64(value) < *field.Min || field.Max != nil && float64(value) > *field.Max {
			return nil, fmt.Errorf("is out of range")
		}
		return mustMarshal(value), nil
	case EnvNumber:
		var value float64
		if json.Unmarshal(raw, &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || raw[0] == '"' {
			return nil, fmt.Errorf("must be a number")
		}
		if field.Min != nil && value < *field.Min || field.Max != nil && value > *field.Max {
			return nil, fmt.Errorf("is out of range")
		}
		return mustMarshal(value), nil
	case EnvBoolean:
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return nil, fmt.Errorf("must be true or false")
		}
		return mustMarshal(value), nil
	case EnvSelect:
		var value string
		if json.Unmarshal(raw, &value) != nil || !optionDeclared(field, value) {
			return nil, fmt.Errorf("must be one of the declared options")
		}
		return mustMarshal(value), nil
	case EnvMultiSelect:
		var values []string
		if json.Unmarshal(raw, &values) != nil {
			return nil, fmt.Errorf("must be a list of options")
		}
		chosen := map[string]bool{}
		for _, value := range values {
			if !optionDeclared(field, value) || chosen[value] {
				return nil, fmt.Errorf("contains an undeclared or duplicate option")
			}
			chosen[value] = true
		}
		if err := itemBounds(field, len(values)); err != nil {
			return nil, err
		}
		ordered := []string{}
		for _, option := range field.Options {
			if chosen[option.Value] {
				ordered = append(ordered, option.Value)
			}
		}
		return mustMarshal(ordered), nil
	case EnvServer:
		id, err := parseServerIDValue(raw)
		if err != nil {
			return nil, err
		}
		return mustMarshal(strconv.FormatInt(id, 10)), nil
	case EnvServers:
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil, fmt.Errorf("must be a list of server IDs")
		}
		ids := make([]int64, 0, len(items))
		seen := map[int64]bool{}
		for _, item := range items {
			id, err := parseServerIDValue(item)
			if err != nil {
				return nil, err
			}
			if seen[id] {
				return nil, fmt.Errorf("contains a duplicate server")
			}
			seen[id] = true
			ids = append(ids, id)
		}
		limit := maxServersItems
		if len(ids) > limit {
			return nil, fmt.Errorf("selects more than %d servers", limit)
		}
		if err := itemBounds(field, len(ids)); err != nil {
			return nil, err
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = strconv.FormatInt(id, 10)
		}
		return mustMarshal(out), nil
	case EnvURL:
		var value string
		if json.Unmarshal(raw, &value) != nil || len(value) == 0 || len(value) > maxURLBytes {
			return nil, fmt.Errorf("must be a URL")
		}
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" || hasControl(value, false) {
			return nil, fmt.Errorf("must be an absolute URL without credentials")
		}
		schemes := field.Schemes
		if len(schemes) == 0 {
			schemes = []string{"https"}
		}
		allowed := false
		for _, scheme := range schemes {
			allowed = allowed || u.Scheme == scheme
		}
		if !allowed {
			return nil, fmt.Errorf("scheme must be %s", strings.Join(schemes, " or "))
		}
		return mustMarshal(u.String()), nil
	case EnvDuration:
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return nil, fmt.Errorf("must be a duration such as 5m")
		}
		d, canonical, err := ParseDuration(value)
		if err != nil {
			return nil, err
		}
		minD, maxD, err := durationBounds(field)
		if err != nil {
			return nil, err
		}
		if d < minD || d > maxD {
			return nil, fmt.Errorf("must be between %s and %s", FormatDuration(minD), FormatDuration(maxD))
		}
		return mustMarshal(canonical), nil
	case EnvJSON:
		if len(raw) > maxJSONBytes {
			return nil, fmt.Errorf("is larger than 16 KiB")
		}
		var value any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&value) != nil || dec.More() {
			return nil, fmt.Errorf("must be valid JSON")
		}
		if jsonDepth(value, 1) > maxJSONDepth {
			return nil, fmt.Errorf("is nested too deeply")
		}
		var compact bytes.Buffer
		if json.Compact(&compact, raw) != nil {
			return nil, fmt.Errorf("must be valid JSON")
		}
		return compact.Bytes(), nil
	case EnvSecret:
		return nil, fmt.Errorf("secret values are stored separately")
	default:
		return nil, fmt.Errorf("unsupported type")
	}
}

func optionDeclared(field EnvField, value string) bool {
	for _, option := range field.Options {
		if option.Value == value {
			return true
		}
	}
	return false
}

func itemBounds(field EnvField, n int) error {
	if field.MinItems != nil && n < *field.MinItems {
		return fmt.Errorf("select at least %d", *field.MinItems)
	}
	if field.MaxItems != nil && n > *field.MaxItems {
		return fmt.Errorf("select at most %d", *field.MaxItems)
	}
	return nil
}

func parseServerIDValue(raw json.RawMessage) (int64, error) {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var number json.Number
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if dec.Decode(&number) != nil {
			return 0, fmt.Errorf("must be a server ID")
		}
		text = number.String()
	}
	id, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != strings.TrimSpace(text) {
		return 0, fmt.Errorf("must be a stable server ID, not a name")
	}
	return id, nil
}

func jsonDepth(value any, depth int) int {
	switch typed := value.(type) {
	case map[string]any:
		deepest := depth
		for _, child := range typed {
			if d := jsonDepth(child, depth+1); d > deepest {
				deepest = d
			}
		}
		return deepest
	case []any:
		deepest := depth
		for _, child := range typed {
			if d := jsonDepth(child, depth+1); d > deepest {
				deepest = d
			}
		}
		return deepest
	default:
		return depth
	}
}

func mustMarshal(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

// FieldActive evaluates depends_on against already normalized values.
// Undeclared or unset conditions resolve through the target's default.
func FieldActive(fields []EnvField, values map[string]json.RawMessage, field EnvField) bool {
	if field.DependsOn == nil {
		return true
	}
	for _, target := range fields {
		if target.Name != field.DependsOn.Field {
			continue
		}
		if !FieldActive(fields, values, target) {
			return false
		}
		current, ok := values[target.Name]
		if !ok || len(current) == 0 {
			current = target.Default
		}
		if len(current) == 0 {
			if target.Type == EnvBoolean {
				current = json.RawMessage("false")
			} else {
				return false
			}
		}
		return bytes.Equal(current, field.DependsOn.Equals)
	}
	return false
}

// EnvironmentInput is an administrator save request for one instance.
type EnvironmentInput struct {
	Values map[string]json.RawMessage `json:"values"`
	Custom []CustomVar                `json:"custom"`
}

// FieldIssue is one validation finding surfaced to the editor/config panel.
type FieldIssue struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NormalizeEnvironment validates an administrator save (the "save" phase).
// Unknown declared names, custom names that collide with declared or
// reserved names, and invalid values are rejected; empty values clear a field.
func NormalizeEnvironment(manifest Manifest, input EnvironmentInput) (map[string]json.RawMessage, []CustomVar, []FieldIssue) {
	issues := []FieldIssue{}
	values := map[string]json.RawMessage{}
	for name, raw := range input.Values {
		field, ok := manifest.EnvField(name)
		if !ok {
			issues = append(issues, FieldIssue{Field: name, Code: CodeInvalidEnvironment, Message: "该变量未在清单中声明"})
			continue
		}
		if field.Type == EnvSecret {
			issues = append(issues, FieldIssue{Field: name, Code: CodeInvalidEnvironment, Message: "密钥只能通过密钥接口写入"})
			continue
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || string(trimmed) == "null" || string(trimmed) == `""` {
			continue
		}
		normalized, err := NormalizeEnvValue(field, raw)
		if err != nil {
			issues = append(issues, FieldIssue{Field: name, Code: CodeInvalidEnvironment, Message: err.Error()})
			continue
		}
		values[name] = normalized
	}
	if len(input.Custom) > maxCustomEnvFields {
		issues = append(issues, FieldIssue{Field: "custom", Code: CodeInvalidEnvironment, Message: "最多 32 个自定义变量"})
		return values, nil, issues
	}
	custom := make([]CustomVar, 0, len(input.Custom))
	seen := map[string]bool{}
	for _, item := range input.Custom {
		path := "custom." + item.Name
		switch {
		case !envNamePattern.MatchString(item.Name):
			issues = append(issues, FieldIssue{Field: path, Code: CodeInvalidEnvironment, Message: "名称必须是大写字母开头的 [A-Z0-9_]"})
			continue
		case strings.HasPrefix(item.Name, "OBOARD_"):
			issues = append(issues, FieldIssue{Field: path, Code: CodeInvalidEnvironment, Message: "OBOARD_ 前缀保留给系统"})
			continue
		case seen[item.Name]:
			issues = append(issues, FieldIssue{Field: path, Code: CodeInvalidEnvironment, Message: "变量名重复"})
			continue
		}
		if _, declared := manifest.EnvField(item.Name); declared {
			issues = append(issues, FieldIssue{Field: path, Code: CodeInvalidEnvironment, Message: "不能覆盖清单已声明的变量"})
			continue
		}
		if !customEnvTypes[item.Type] {
			issues = append(issues, FieldIssue{Field: path, Code: CodeInvalidEnvironment, Message: "自定义变量不支持该类型"})
			continue
		}
		seen[item.Name] = true
		if item.Type == EnvSecret {
			if len(bytes.TrimSpace(item.Value)) > 0 && string(bytes.TrimSpace(item.Value)) != "null" {
				issues = append(issues, FieldIssue{Field: path, Code: CodeInvalidEnvironment, Message: "密钥只能通过密钥接口写入"})
				continue
			}
			custom = append(custom, CustomVar{Name: item.Name, Type: item.Type})
			continue
		}
		normalized, err := NormalizeEnvValue(EnvField{Name: item.Name, Type: item.Type, Schemes: []string{"https", "http"}}, item.Value)
		if err != nil {
			issues = append(issues, FieldIssue{Field: path, Code: CodeInvalidEnvironment, Message: err.Error()})
			continue
		}
		custom = append(custom, CustomVar{Name: item.Name, Type: item.Type, Value: normalized})
	}
	return values, custom, issues
}

// ConfigStatus values for an instance.
const (
	ConfigOK       = "ok"
	ConfigRequired = "configuration_required"
	ConfigInvalid  = "configuration_invalid"
)

// ServerResolver answers whether a server still exists. It is the run-time
// ("run validate") view; saved values are never trusted on their own.
type ServerResolver func(id int64) bool

// EvaluationInput is the current reality an instance configuration is checked
// against before every run and whenever it is displayed.
type EvaluationInput struct {
	Manifest          Manifest
	Values            map[string]json.RawMessage
	Custom            []CustomVar
	SecretsConfigured map[string]bool
	ServerExists      ServerResolver
	// GrantedServers is the union of server scopes across granted server
	// capabilities. Nil means the manifest has no server-scoped capability.
	GrantedServers map[int64]bool
}

// EvaluateEnvironment performs the run-validate phase. Missing required
// values give configuration_required; a value that no longer fits the
// schema, a deleted server or a server outside the current grants gives
// configuration_invalid. Values are never silently rewritten.
func EvaluateEnvironment(in EvaluationInput) (string, []FieldIssue) {
	issues := []FieldIssue{}
	required, invalid := false, false
	checkServer := func(field, raw string) {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			invalid = true
			issues = append(issues, FieldIssue{Field: field, Code: CodeInvalidEnvironment, Message: "服务器 ID 无效"})
			return
		}
		if in.ServerExists != nil && !in.ServerExists(id) {
			invalid = true
			issues = append(issues, FieldIssue{Field: field, Code: CodeServerNotFound, Message: "所选服务器已不存在，请重新选择"})
			return
		}
		if in.GrantedServers != nil && !in.GrantedServers[id] {
			invalid = true
			issues = append(issues, FieldIssue{Field: field, Code: CodeResourceDenied, Message: "所选服务器不在当前授权范围内，请重新选择或调整授权"})
		}
	}
	checkServers := func(field string, raw json.RawMessage, single bool) {
		if single {
			var value string
			if json.Unmarshal(raw, &value) != nil {
				invalid = true
				issues = append(issues, FieldIssue{Field: field, Code: CodeInvalidEnvironment, Message: "服务器值无效"})
				return
			}
			checkServer(field, value)
			return
		}
		var values []string
		if json.Unmarshal(raw, &values) != nil {
			invalid = true
			issues = append(issues, FieldIssue{Field: field, Code: CodeInvalidEnvironment, Message: "服务器列表无效"})
			return
		}
		for _, value := range values {
			checkServer(field, value)
		}
	}
	for _, field := range in.Manifest.Environment {
		if !FieldActive(in.Manifest.Environment, in.Values, field) {
			continue
		}
		if field.Type == EnvSecret {
			if field.Required && !in.SecretsConfigured[field.Name] {
				required = true
				issues = append(issues, FieldIssue{Field: field.Name, Code: CodeSecretNotConfigured, Message: "需要配置密钥"})
			}
			continue
		}
		raw, ok := in.Values[field.Name]
		if !ok || len(raw) == 0 {
			if field.Required && len(field.Default) == 0 {
				required = true
				issues = append(issues, FieldIssue{Field: field.Name, Code: CodeConfigurationRequired, Message: "必填项尚未填写"})
			}
			continue
		}
		if _, err := NormalizeEnvValue(field, raw); err != nil {
			invalid = true
			issues = append(issues, FieldIssue{Field: field.Name, Code: CodeInvalidEnvironment, Message: "当前值不再符合清单：" + err.Error()})
			continue
		}
		switch field.Type {
		case EnvServer:
			checkServers(field.Name, raw, true)
		case EnvServers:
			checkServers(field.Name, raw, false)
		}
	}
	for _, item := range in.Custom {
		switch item.Type {
		case EnvServer:
			checkServers("custom."+item.Name, item.Value, true)
		case EnvServers:
			checkServers("custom."+item.Name, item.Value, false)
		case EnvSecret:
			if !in.SecretsConfigured[item.Name] {
				issues = append(issues, FieldIssue{Field: "custom." + item.Name, Code: CodeSecretNotConfigured, Message: "自定义密钥尚未写入"})
			}
		}
	}
	switch {
	case invalid:
		return ConfigInvalid, issues
	case required:
		return ConfigRequired, issues
	default:
		return ConfigOK, issues
	}
}

// RuntimeVar is one resolved variable delivered to the runner. Value is the
// typed runtime form; Raw is the string view exposed by env.raw().
type RuntimeVar struct {
	Type   string          `json:"type"`
	Value  json.RawMessage `json:"value"`
	Raw    string          `json:"raw"`
	Custom bool            `json:"custom,omitempty"`
}

// SecretRefPrefix identifies instance-bound secret references. The runtime
// only ever sees references; the gateway resolves them for one instance.
const SecretRefPrefix = "secret://instance/"

func SecretRef(instanceID int64, name string) string {
	return SecretRefPrefix + strconv.FormatInt(instanceID, 10) + "/" + name
}

// ParseSecretRef returns the instance and name a reference points at.
func ParseSecretRef(ref string) (int64, string, bool) {
	rest, ok := strings.CutPrefix(ref, SecretRefPrefix)
	if !ok {
		return 0, "", false
	}
	idText, name, ok := strings.Cut(rest, "/")
	id, err := strconv.ParseInt(idText, 10, 64)
	if !ok || err != nil || id <= 0 || !envNamePattern.MatchString(name) {
		return 0, "", false
	}
	return id, name, true
}

// ResolveRuntimeEnvironment builds the typed virtual environment for one run.
// Only declared, active fields and current custom variables are delivered;
// removed fields are never exposed.
func ResolveRuntimeEnvironment(instanceID int64, manifest Manifest, values map[string]json.RawMessage, custom []CustomVar, secrets map[string]bool) map[string]RuntimeVar {
	out := map[string]RuntimeVar{}
	add := func(name, typ string, stored json.RawMessage, isCustom bool) {
		if typ == EnvSecret {
			if !secrets[name] {
				return
			}
			ref := SecretRef(instanceID, name)
			out[name] = RuntimeVar{Type: typ, Value: mustMarshal(map[string]string{"$secret": ref}), Raw: ref, Custom: isCustom}
			return
		}
		if len(stored) == 0 {
			return
		}
		runtimeValue, raw := runtimeForm(typ, stored)
		out[name] = RuntimeVar{Type: typ, Value: runtimeValue, Raw: raw, Custom: isCustom}
	}
	for _, field := range manifest.Environment {
		if !FieldActive(manifest.Environment, values, field) {
			continue
		}
		stored := values[field.Name]
		if len(stored) == 0 {
			stored = field.Default
		}
		if field.Type != EnvSecret && len(stored) > 0 {
			if _, err := NormalizeEnvValue(field, stored); err != nil {
				continue
			}
		}
		add(field.Name, field.Type, stored, false)
	}
	for _, item := range custom {
		if _, declared := manifest.EnvField(item.Name); declared {
			continue
		}
		add(item.Name, item.Type, item.Value, true)
	}
	return out
}

func runtimeForm(typ string, stored json.RawMessage) (json.RawMessage, string) {
	switch typ {
	case EnvDuration:
		var text string
		_ = json.Unmarshal(stored, &text)
		d, _, err := ParseDuration(text)
		if err != nil {
			return stored, text
		}
		return mustMarshal(d.Milliseconds()), text
	case EnvString, EnvText, EnvSelect, EnvServer, EnvURL:
		var text string
		_ = json.Unmarshal(stored, &text)
		return stored, text
	case EnvMultiSelect, EnvServers:
		var list []string
		_ = json.Unmarshal(stored, &list)
		return stored, strings.Join(list, ",")
	default:
		return stored, string(stored)
	}
}

// ServerIDsInEnvironment lists every server referenced by current values, for
// status display and grant-narrowing checks.
func ServerIDsInEnvironment(manifest Manifest, values map[string]json.RawMessage, custom []CustomVar) []int64 {
	set := map[int64]bool{}
	collect := func(typ string, raw json.RawMessage) {
		switch typ {
		case EnvServer:
			var value string
			if json.Unmarshal(raw, &value) == nil {
				if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
					set[id] = true
				}
			}
		case EnvServers:
			var list []string
			if json.Unmarshal(raw, &list) == nil {
				for _, value := range list {
					if id, err := strconv.ParseInt(value, 10, 64); err == nil && id > 0 {
						set[id] = true
					}
				}
			}
		}
	}
	for _, field := range manifest.Environment {
		collect(field.Type, values[field.Name])
	}
	for _, item := range custom {
		collect(item.Type, item.Value)
	}
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
