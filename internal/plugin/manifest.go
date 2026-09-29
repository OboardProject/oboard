package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	RuntimeJS    = "oboard-js"
	EntryFile    = "main.js"
	ManifestFile = "manifest.json"

	MaxManifestBytes   = 64 << 10
	MaxSourceBytes     = 512 << 10
	maxNameBytes       = 80
	maxDescribeBytes   = 2000
	maxHTTPHosts       = 32
	maxEnvFields       = 64
	maxCustomEnvFields = 32
)

const (
	EventServerOnline  = "server.online"
	EventServerOffline = "server.offline"
)

var (
	pluginIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)+$`)
	versionPattern  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	envNamePattern  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	httpMethods     = map[string]bool{"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}
	supportedEvents = map[string]bool{EventServerOnline: true, EventServerOffline: true}
)

// Manifest is the only declaration source for a plugin: identity, runtime,
// capabilities, HTTP scope, environment, triggers, resources and limits.
// Nothing is inferred from source code.
type Manifest struct {
	ID           string                `json:"id"`
	Name         string                `json:"name"`
	Version      string                `json:"version"`
	Description  string                `json:"description"`
	Runtime      string                `json:"runtime"`
	Entry        string                `json:"entry"`
	Capabilities []string              `json:"capabilities"`
	HTTP         *HTTPScope            `json:"http,omitempty"`
	Resources    *ResourceRequirements `json:"resources,omitempty"`
	Environment  []EnvField            `json:"environment,omitempty"`
	Triggers     Triggers              `json:"triggers"`
	Limits       DeclaredLimits        `json:"limits"`
}

// HTTPScope declares the hosts and methods http.request may ever reach.
// Hosts are exact hostnames or "*.example.com" wildcards (subdomains only).
type HTTPScope struct {
	Hosts   []string `json:"hosts"`
	Methods []string `json:"methods"`
}

type ResourceRequirements struct {
	Servers *ServerRequirement `json:"servers,omitempty"`
}

type ServerRequirement struct {
	Min    int    `json:"min"`
	Max    int    `json:"max,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Triggers declares which automatic triggers an administrator may bind.
// Manual runs are always available.
type Triggers struct {
	Schedule bool     `json:"schedule"`
	Events   []string `json:"events,omitempty"`
}

// DeclaredLimits may only lower host ceilings.
type DeclaredLimits struct {
	Timeout         string `json:"timeout,omitempty"`
	MemoryMiB       int    `json:"memory_mib,omitempty"`
	SDKCalls        int    `json:"sdk_calls,omitempty"`
	HTTPRequests    int    `json:"http_requests,omitempty"`
	AgentOperations int    `json:"agent_operations,omitempty"`
	LogBytes        int    `json:"log_bytes,omitempty"`
}

// ParseManifest strictly decodes and validates a manifest. Unknown fields,
// the previous schema and any unsupported value make the manifest invalid.
func ParseManifest(raw []byte) (Manifest, error) {
	var manifest Manifest
	if len(bytes.TrimSpace(raw)) == 0 {
		return manifest, Fail(CodeInvalidManifest, "manifest.json is empty")
	}
	if len(raw) > MaxManifestBytes {
		return manifest, Fail(CodeInvalidManifest, "manifest.json exceeds 64 KiB")
	}
	if !utf8.Valid(raw) {
		return manifest, Fail(CodeInvalidManifest, "manifest.json is not UTF-8")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return manifest, Fail(CodeInvalidManifest, "manifest.json is not a JSON object")
	}
	for _, retired := range []string{"plugin_id", "sdk_version", "schema_version", "params_schema", "config_schema", "env"} {
		if _, ok := probe[retired]; ok {
			return manifest, FailField(CodeInvalidManifest, retired, "incompatible manifest: this plugin targets the retired plugin runtime")
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, Fail(CodeInvalidManifest, "manifest.json: "+sanitizeDecodeError(err))
	}
	if dec.More() {
		return Manifest{}, Fail(CodeInvalidManifest, "manifest.json has trailing data")
	}
	if err := ValidateManifest(&manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ValidateManifest checks every field and normalizes ordering-insensitive
// lists so equal declarations produce equal canonical JSON.
func ValidateManifest(m *Manifest) error {
	if len(m.ID) > 128 || !pluginIDPattern.MatchString(m.ID) {
		return FailField(CodeInvalidManifest, "id", "id must be a dotted lowercase identifier such as acme.trace-monitor")
	}
	if strings.HasPrefix(m.ID, "oboard.") {
		return FailField(CodeInvalidManifest, "id", "the oboard. namespace is reserved")
	}
	if strings.TrimSpace(m.Name) == "" || len(m.Name) > maxNameBytes || hasControl(m.Name, false) {
		return FailField(CodeInvalidManifest, "name", "name is required and at most 80 bytes")
	}
	if len(m.Version) > 64 || !versionPattern.MatchString(m.Version) {
		return FailField(CodeInvalidManifest, "version", "version must be semantic, e.g. 1.2.0")
	}
	if len(m.Description) > maxDescribeBytes || hasControl(m.Description, true) {
		return FailField(CodeInvalidManifest, "description", "description is at most 2000 bytes")
	}
	if m.Runtime != RuntimeJS {
		return FailField(CodeInvalidManifest, "runtime", "runtime must be "+RuntimeJS)
	}
	if m.Entry != EntryFile {
		return FailField(CodeInvalidManifest, "entry", "entry must be "+EntryFile)
	}
	if err := validateCapabilities(m); err != nil {
		return err
	}
	if err := validateHTTPScope(m); err != nil {
		return err
	}
	if err := validateResources(m); err != nil {
		return err
	}
	if err := validateTriggers(m); err != nil {
		return err
	}
	if err := ValidateEnvironmentSchema(m.Environment, m.Capabilities); err != nil {
		return err
	}
	if _, err := EffectiveLimits(m.Limits, 0); err != nil {
		return err
	}
	return nil
}

func validateCapabilities(m *Manifest) error {
	if m.Capabilities == nil {
		m.Capabilities = []string{}
	}
	seen := map[string]bool{}
	for _, name := range m.Capabilities {
		if forbiddenCapability(name) {
			return FailField(CodeInvalidManifest, "capabilities", "capability "+name+" can never be granted to a plugin")
		}
		if _, ok := LookupCapability(name); !ok {
			return FailField(CodeInvalidManifest, "capabilities", "unknown capability "+name)
		}
		if seen[name] {
			return FailField(CodeInvalidManifest, "capabilities", "duplicate capability "+name)
		}
		seen[name] = true
	}
	sort.Strings(m.Capabilities)
	return nil
}

func validateHTTPScope(m *Manifest) error {
	wantsHTTP := m.HasCapability(CapHTTPRequest)
	if m.HTTP == nil {
		if wantsHTTP {
			return FailField(CodeInvalidManifest, "http", "http.request requires an http host declaration")
		}
		return nil
	}
	if !wantsHTTP {
		return FailField(CodeInvalidManifest, "http", "http is declared without the http.request capability")
	}
	if len(m.HTTP.Hosts) == 0 || len(m.HTTP.Hosts) > maxHTTPHosts {
		return FailField(CodeInvalidManifest, "http.hosts", "declare between 1 and 32 hosts")
	}
	hosts := map[string]bool{}
	for i, host := range m.HTTP.Hosts {
		normalized, err := NormalizeHostPattern(host)
		if err != nil {
			return FailField(CodeInvalidManifest, "http.hosts", err.Error())
		}
		if hosts[normalized] {
			return FailField(CodeInvalidManifest, "http.hosts", "duplicate host "+normalized)
		}
		hosts[normalized] = true
		m.HTTP.Hosts[i] = normalized
	}
	sort.Strings(m.HTTP.Hosts)
	if len(m.HTTP.Methods) == 0 {
		return FailField(CodeInvalidManifest, "http.methods", "declare at least one HTTP method")
	}
	methods := map[string]bool{}
	for i, method := range m.HTTP.Methods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if !httpMethods[method] || methods[method] {
			return FailField(CodeInvalidManifest, "http.methods", "unsupported or duplicate method "+method)
		}
		methods[method] = true
		m.HTTP.Methods[i] = method
	}
	sort.Strings(m.HTTP.Methods)
	return nil
}

// NormalizeHostPattern accepts "api.example.com", "api.example.com:8443" and
// "*.example.com". IP literals, single-label and internal suffixes are refused.
func NormalizeHostPattern(raw string) (string, error) {
	pattern := strings.ToLower(strings.TrimSpace(raw))
	host, port, hasPort := strings.Cut(pattern, ":")
	if hasPort {
		if port == "" || len(port) > 5 || strings.TrimLeft(port, "0123456789") != "" || port[0] == '0' {
			return "", fmt.Errorf("invalid port in host %q", raw)
		}
		var n int
		fmt.Sscanf(port, "%d", &n)
		if n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid port in host %q", raw)
		}
	}
	name := strings.TrimPrefix(host, "*.")
	if strings.Contains(name, "*") {
		return "", fmt.Errorf("wildcards are only allowed as a leading *. label: %q", raw)
	}
	if !ValidHostname(name) {
		return "", fmt.Errorf("host %q is not a public DNS name", raw)
	}
	if strings.HasPrefix(host, "*.") && strings.Count(name, ".") < 1 {
		return "", fmt.Errorf("wildcard %q is too broad", raw)
	}
	return pattern, nil
}

// ValidHostname accepts multi-label DNS names outside internal-only suffixes.
func ValidHostname(host string) bool {
	if len(host) == 0 || len(host) > 253 || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa", ".lan", ".intranet", ".corp", ".test", ".invalid", ".onion"} {
		if strings.HasSuffix(host, suffix) || host == strings.TrimPrefix(suffix, ".") {
			return false
		}
	}
	labels := strings.Split(host, ".")
	last := labels[len(labels)-1]
	if strings.TrimLeft(last, "0123456789") == "" {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

func validateResources(m *Manifest) error {
	if m.Resources == nil || m.Resources.Servers == nil {
		return nil
	}
	req := m.Resources.Servers
	if !m.usesResource(ResourceServer) {
		return FailField(CodeInvalidManifest, "resources.servers", "server resources require a server-scoped capability")
	}
	if req.Min < 0 || req.Min > 256 || req.Max < 0 || req.Max > 256 || (req.Max > 0 && req.Max < req.Min) {
		return FailField(CodeInvalidManifest, "resources.servers", "server requirement bounds are invalid")
	}
	if len(req.Reason) > 256 || hasControl(req.Reason, false) {
		return FailField(CodeInvalidManifest, "resources.servers.reason", "reason is at most 256 bytes")
	}
	return nil
}

func validateTriggers(m *Manifest) error {
	seen := map[string]bool{}
	for _, event := range m.Triggers.Events {
		if !supportedEvents[event] || seen[event] {
			return FailField(CodeInvalidManifest, "triggers.events", "unsupported or duplicate event "+event)
		}
		seen[event] = true
	}
	if len(m.Triggers.Events) > 0 && !m.HasCapability(CapEventsServerStatus) {
		return FailField(CodeInvalidManifest, "triggers.events", "server events require the events.server_status capability")
	}
	if m.HasCapability(CapEventsServerStatus) && len(m.Triggers.Events) == 0 {
		return FailField(CodeInvalidManifest, "triggers.events", "events.server_status requires at least one declared event")
	}
	sort.Strings(m.Triggers.Events)
	return nil
}

func (m Manifest) HasCapability(name string) bool {
	for _, item := range m.Capabilities {
		if item == name {
			return true
		}
	}
	return false
}

func (m Manifest) usesResource(resource string) bool {
	for _, name := range m.Capabilities {
		if spec, ok := LookupCapability(name); ok && spec.Resource == resource {
			return true
		}
	}
	return false
}

// UsesServerResources reports whether any declared capability is scoped by
// server grants.
func (m Manifest) UsesServerResources() bool { return m.usesResource(ResourceServer) }

func (m Manifest) EnvField(name string) (EnvField, bool) {
	for _, field := range m.Environment {
		if field.Name == name {
			return field, true
		}
	}
	return EnvField{}, false
}

// CanonicalJSON is the stored and hashed manifest form.
func (m Manifest) CanonicalJSON() []byte {
	raw, _ := json.Marshal(m)
	return raw
}

func hasControl(value string, allowNewlines bool) bool {
	for _, r := range value {
		if r == '\n' || r == '\t' {
			if allowNewlines {
				continue
			}
			return true
		}
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func sanitizeDecodeError(err error) string {
	message := err.Error()
	message = strings.TrimPrefix(message, "json: ")
	if len(message) > 200 {
		message = message[:200]
	}
	return message
}

// ParseDuration accepts Go-style durations ("90s", "5m", "1h30m") without
// fractional or negative values and returns the canonical string.
func ParseDuration(raw string) (time.Duration, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 32 || strings.ContainsAny(value, "-+. ") {
		return 0, "", fmt.Errorf("duration must look like 30s, 5m or 1h30m")
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 || d%time.Second != 0 {
		return 0, "", fmt.Errorf("duration must be a whole number of seconds such as 30s, 5m or 1h30m")
	}
	return d, FormatDuration(d), nil
}

// FormatDuration renders a whole-second duration without zero units: 1h30m, 5m, 45s.
func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	var b strings.Builder
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	d -= minutes * time.Minute
	seconds := d / time.Second
	hours += days * 24
	if hours > 0 {
		fmt.Fprintf(&b, "%dh", hours)
	}
	if minutes > 0 {
		fmt.Fprintf(&b, "%dm", minutes)
	}
	if seconds > 0 {
		fmt.Fprintf(&b, "%ds", seconds)
	}
	return b.String()
}
