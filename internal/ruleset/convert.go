package ruleset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/OboardProject/oboard/internal/model"
)

const MaxContentSize = 8 << 20

type sourceDocument struct {
	Version int              `json:"version"`
	Rules   []map[string]any `json:"rules"`
}

func Convert(format string, content []byte) ([]byte, error) {
	if len(content) == 0 {
		return nil, errors.New("rule set is empty")
	}
	if len(content) > MaxContentSize {
		return nil, errors.New("rule set exceeds the 8 MiB limit")
	}
	switch format {
	case model.RoutingRuleSetFormatSingBoxSource:
		return canonicalSource(content)
	case model.RoutingRuleSetFormatSingBoxBinary:
		return append([]byte(nil), content...), nil
	case model.RoutingRuleSetFormatMihomoDomain, model.RoutingRuleSetFormatMihomoIPCIDR, model.RoutingRuleSetFormatMihomoClassical:
		return convertMihomo(format, content)
	default:
		return nil, fmt.Errorf("unsupported rule set format %q", format)
	}
}

func canonicalSource(content []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var document sourceDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("invalid sing-box source JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("invalid sing-box source JSON: multiple documents")
		}
		return nil, fmt.Errorf("invalid sing-box source JSON: %w", err)
	}
	if document.Version != 1 {
		return nil, errors.New("sing-box source JSON version must be 1")
	}
	if len(document.Rules) == 0 {
		return nil, errors.New("sing-box source JSON must contain at least one rule")
	}
	for index, rule := range document.Rules {
		if len(rule) == 0 {
			return nil, fmt.Errorf("sing-box source JSON rule %d is empty", index+1)
		}
	}
	return json.Marshal(document)
}

func convertMihomo(format string, content []byte) ([]byte, error) {
	entries, err := mihomoEntries(content)
	if err != nil {
		return nil, err
	}
	rules := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		var rule map[string]any
		switch format {
		case model.RoutingRuleSetFormatMihomoDomain:
			rule, err = convertDomain(entry.value)
		case model.RoutingRuleSetFormatMihomoIPCIDR:
			rule, err = convertIPCIDR(entry.value)
		default:
			rule, err = convertClassical(entry.value)
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", entry.line, err)
		}
		rules = append(rules, rule)
	}
	if len(rules) == 0 {
		return nil, errors.New("Mihomo rule set contains no rules")
	}
	return json.Marshal(sourceDocument{Version: 1, Rules: rules})
}

type mihomoEntry struct {
	line  int
	value string
}

func mihomoEntries(content []byte) ([]mihomoEntry, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(content, &root); err == nil && len(root.Content) > 0 {
		node := root.Content[0]
		if node.Kind == yaml.MappingNode {
			for index := 0; index+1 < len(node.Content); index += 2 {
				if strings.EqualFold(strings.TrimSpace(node.Content[index].Value), "payload") {
					return yamlSequenceEntries(node.Content[index+1])
				}
			}
			return nil, errors.New("Mihomo YAML requires a payload sequence")
		}
		if node.Kind == yaml.SequenceNode {
			return yamlSequenceEntries(node)
		}
	}
	entries := make([]mihomoEntry, 0)
	for index, raw := range strings.Split(string(content), "\n") {
		value := strings.TrimSpace(raw)
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		value = strings.TrimSpace(strings.TrimPrefix(value, "-"))
		if value != "" {
			entries = append(entries, mihomoEntry{line: index + 1, value: value})
		}
	}
	return entries, nil
}

func yamlSequenceEntries(node *yaml.Node) ([]mihomoEntry, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("line %d: payload must be a sequence", node.Line)
	}
	entries := make([]mihomoEntry, 0, len(node.Content))
	for _, child := range node.Content {
		if child.Kind != yaml.ScalarNode || strings.TrimSpace(child.Value) == "" {
			return nil, fmt.Errorf("line %d: payload entries must be non-empty strings", child.Line)
		}
		entries = append(entries, mihomoEntry{line: child.Line, value: strings.TrimSpace(child.Value)})
	}
	return entries, nil
}

func convertDomain(value string) (map[string]any, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "+.") {
		value = strings.TrimPrefix(value, "+.")
		if err := validateDomain(value); err != nil {
			return nil, err
		}
		return map[string]any{"domain_suffix": []string{"." + value}}, nil
	}
	if strings.HasPrefix(value, ".") {
		domain := strings.TrimPrefix(value, ".")
		if err := validateDomain(domain); err != nil {
			return nil, err
		}
		return map[string]any{"domain_suffix": []string{"." + domain}}, nil
	}
	if err := validateDomain(value); err != nil {
		return nil, err
	}
	return map[string]any{"domain": []string{value}}, nil
}

func convertIPCIDR(value string) (map[string]any, error) {
	value = strings.TrimSpace(strings.Split(value, ",")[0])
	if _, err := netip.ParsePrefix(value); err != nil {
		return nil, fmt.Errorf("invalid IP CIDR %q", value)
	}
	return map[string]any{"ip_cidr": []string{value}}, nil
}

func convertClassical(value string) (map[string]any, error) {
	parts := strings.Split(value, ",")
	if len(parts) < 2 {
		return nil, errors.New("classical rule requires TYPE,VALUE")
	}
	kind := strings.ToUpper(strings.TrimSpace(parts[0]))
	target := strings.TrimSpace(parts[1])
	switch kind {
	case "DOMAIN":
		if err := validateDomain(target); err != nil {
			return nil, err
		}
		return map[string]any{"domain": []string{target}}, nil
	case "DOMAIN-SUFFIX":
		if err := validateDomain(target); err != nil {
			return nil, err
		}
		return map[string]any{"domain_suffix": []string{"." + target}}, nil
	case "DOMAIN-KEYWORD":
		if target == "" {
			return nil, errors.New("domain keyword is empty")
		}
		return map[string]any{"domain_keyword": []string{target}}, nil
	case "IP-CIDR", "IP-CIDR6":
		return convertIPCIDR(target)
	case "GEOIP":
		if target == "" {
			return nil, errors.New("GeoIP code is empty")
		}
		return map[string]any{"geoip": []string{strings.ToLower(target)}}, nil
	case "GEOSITE":
		if target == "" {
			return nil, errors.New("Geosite code is empty")
		}
		return map[string]any{"geosite": []string{strings.ToLower(target)}}, nil
	case "DST-PORT":
		port, err := strconv.Atoi(target)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid destination port %q", target)
		}
		return map[string]any{"port": []int{port}}, nil
	default:
		return nil, fmt.Errorf("unsupported Mihomo rule type %q", kind)
	}
}

func validateDomain(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 253 || strings.ContainsAny(value, " /\\:*?\"") {
		return fmt.Errorf("invalid domain %q", value)
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("invalid domain %q", value)
		}
	}
	return nil
}
