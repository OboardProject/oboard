package core

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// Client templates assemble a configuration document from protocol-encoded
// fragments. They must never reconstruct SSH, Snell, Mieru, or other protocol
// fields from a raw proxy object.
const (
	MarkerProxiesYAML      = "OBOARD_PROXIES_YAML"
	MarkerProxyGroupsYAML  = "OBOARD_PROXY_GROUPS_YAML"
	MarkerPrimaryGroup     = "OBOARD_PRIMARY_GROUP"
	MarkerProxyLines       = "OBOARD_PROXY_LINES"
	MarkerProxyGroupLines  = "OBOARD_PROXY_GROUP_LINES"
	MarkerOutboundsJSON    = "OBOARD_OUTBOUNDS_JSON"
	MarkerRouteRulesJSON   = "OBOARD_ROUTE_RULES_JSON"
	MarkerURILines         = "OBOARD_URI_LINES"
)

var (
	subscriptionMarkerPattern = regexp.MustCompile(`\{\{\s*(OBOARD_[A-Z0-9_]+)\s*\}\}`)
	subscriptionOpenMarker    = regexp.MustCompile(`\{\{`)
)

type subscriptionTemplateFragments struct {
	values map[string]string
}

func subscriptionTemplateMarkers(format model.SubscriptionFormat) []string {
	switch normalizeSubscriptionFormat(format) {
	case model.SubscriptionFormatMihomo, model.SubscriptionFormatStash:
		return []string{MarkerProxiesYAML, MarkerProxyGroupsYAML, MarkerPrimaryGroup}
	case model.SubscriptionFormatEgern:
		return []string{MarkerProxiesYAML}
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatLoon, model.SubscriptionFormatQX, model.SubscriptionFormatSurfboard:
		return []string{MarkerProxyLines, MarkerProxyGroupLines, MarkerPrimaryGroup}
	case model.SubscriptionFormatSingBox:
		return []string{MarkerOutboundsJSON, MarkerRouteRulesJSON}
	case model.SubscriptionFormatShadowrocket, model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
		return []string{MarkerURILines}
	default:
		return nil
	}
}

func BuiltinSubscriptionTemplate(format model.SubscriptionFormat) (string, error) {
	format = normalizeSubscriptionFormat(format)
	name := string(format) + ".tmpl"
	data, err := subscriptionBuiltinTemplates.ReadFile("subscription_templates/" + name)
	if err != nil {
		return "", fmt.Errorf("missing builtin subscription template for %s", format)
	}
	return string(data), nil
}

func BuiltinSubscriptionTemplateDigest(format model.SubscriptionFormat) (string, error) {
	content, err := BuiltinSubscriptionTemplate(format)
	if err != nil {
		return "", err
	}
	return subscriptionTemplateDigest(content), nil
}

func MustSubscriptionTemplateDigest(content string) string {
	return subscriptionTemplateDigest(content)
}

func SubscriptionTemplateMarkerNames(format model.SubscriptionFormat) []string {
	markers := subscriptionTemplateMarkers(format)
	out := make([]string, 0, len(markers))
	for _, marker := range markers {
		out = append(out, "{{ "+marker+" }}")
	}
	return out
}

func subscriptionTemplateDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func ValidateSubscriptionTemplate(format model.SubscriptionFormat, content string) error {
	format = normalizeSubscriptionFormat(format)
	if !IsConcreteSubscriptionFormat(format) {
		return fmt.Errorf("subscription format %q is not a concrete renderer", format)
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("template content is required")
	}
	allowed := map[string]bool{}
	required := subscriptionTemplateMarkers(format)
	if len(required) == 0 {
		return fmt.Errorf("subscription format %q has no template markers", format)
	}
	for _, marker := range required {
		allowed[marker] = true
	}
	seen := map[string]bool{}
	for _, loc := range subscriptionOpenMarker.FindAllStringIndex(content, -1) {
		rest := content[loc[0]:]
		match := subscriptionMarkerPattern.FindStringSubmatch(rest)
		if match == nil || !strings.HasPrefix(rest, match[0]) {
			return fmt.Errorf("unsupported template syntax; only typed OBOARD markers are allowed")
		}
		name := match[1]
		if !allowed[name] {
			return fmt.Errorf("marker {{ %s }} is not valid for %s", name, format)
		}
		seen[name] = true
	}
	for _, marker := range required {
		if !seen[marker] {
			return fmt.Errorf("template for %s must include {{ %s }}", format, marker)
		}
	}
	return nil
}

func renderClientTemplate(format model.SubscriptionFormat, template string, fragments subscriptionTemplateFragments) (string, error) {
	if err := ValidateSubscriptionTemplate(format, template); err != nil {
		return "", err
	}
	lines := strings.Split(template, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		matches := subscriptionMarkerPattern.FindAllStringSubmatchIndex(line, -1)
		if len(matches) == 0 {
			if strings.Contains(line, "{{") {
				return "", fmt.Errorf("unsupported template syntax; only typed OBOARD markers are allowed")
			}
			out = append(out, line)
			continue
		}
		trimmed := strings.TrimSpace(line)
		exclusive := subscriptionMarkerPattern.ReplaceAllString(trimmed, "")
		if strings.TrimSpace(exclusive) == "" && len(matches) == 1 {
			name := line[matches[0][2]:matches[0][3]]
			indent := leadingSubscriptionIndent(line)
			out = append(out, indentFragmentLines(fragments.values[name], indent))
			continue
		}
		replaced := subscriptionMarkerPattern.ReplaceAllStringFunc(line, func(raw string) string {
			name := subscriptionMarkerPattern.FindStringSubmatch(raw)[1]
			return fragments.values[name]
		})
		out = append(out, replaced)
	}
	return strings.Join(out, "\n"), nil
}

func leadingSubscriptionIndent(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	return line[:len(line)-len(trimmed)]
}

func indentFragmentLines(fragment, indent string) string {
	if fragment == "" {
		return ""
	}
	trailing := strings.HasSuffix(fragment, "\n")
	text := strings.TrimSuffix(fragment, "\n")
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = indent + part
	}
	out := strings.Join(parts, "\n")
	if trailing {
		return strings.TrimSuffix(out, "\n")
	}
	return out
}
