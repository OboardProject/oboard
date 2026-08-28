package core

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"go.yaml.in/yaml/v3"
)

type yamlSubscriptionGroup struct {
	Name    string   `yaml:"name"`
	Type    string   `yaml:"type"`
	Proxies []string `yaml:"proxies"`
}

// encodeProtocolFragment renders one normalized proxy as a target-safe node
// fragment. Templates consume this output and must not rebuild protocol fields.
func encodeProtocolFragment(proxy subscriptionProxy, format model.SubscriptionFormat) (string, error) {
	format = normalizeSubscriptionFormat(format)
	if err := assertConcreteSubscriptionFormat(format); err != nil {
		return "", err
	}
	switch format {
	case model.SubscriptionFormatMihomo, model.SubscriptionFormatStash:
		item, err := mihomoStyleProxyMap(proxy, format)
		if err != nil {
			return "", err
		}
		return marshalSubscriptionYAML(item)
	case model.SubscriptionFormatEgern:
		return marshalSubscriptionYAML(egernProxyMap(proxy))
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru:
		return encodeSingBoxOutboundFragment(proxy)
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatSurfboard, model.SubscriptionFormatLoon, model.SubscriptionFormatQX:
		return renderLineForFormat(proxy, format)
	case model.SubscriptionFormatShadowrocket, model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
		return canonicalShareURI(proxy)
	default:
		return "", fmt.Errorf("unsupported subscription format %q", format)
	}
}

func buildSubscriptionTemplateFragments(proxies []subscriptionProxy, format model.SubscriptionFormat, opts SubscriptionRenderOptions) (subscriptionTemplateFragments, error) {
	format = normalizeSubscriptionFormat(format)
	fragments := subscriptionTemplateFragments{values: map[string]string{}}
	primary, groups := subscriptionGroupPlan(proxies)
	fragments.values[MarkerPrimaryGroup] = primary
	switch format {
	case model.SubscriptionFormatMihomo, model.SubscriptionFormatStash:
		yamlProxies, err := encodeYAMLProxyList(proxies, format)
		if err != nil {
			return fragments, err
		}
		groupYAML, err := marshalSubscriptionYAML(groups)
		if err != nil {
			return fragments, err
		}
		fragments.values[MarkerProxiesYAML] = yamlListOrEmpty(yamlProxies)
		fragments.values[MarkerProxyGroupsYAML] = yamlListOrEmpty(groupYAML)
	case model.SubscriptionFormatEgern:
		yamlProxies, err := encodeYAMLProxyList(proxies, format)
		if err != nil {
			return fragments, err
		}
		fragments.values[MarkerProxiesYAML] = yamlListOrEmpty(yamlProxies)
	case model.SubscriptionFormatSurgeMac:
		lines, err := renderSurgeMacTarget(proxies, opts.SurgeMac)
		if err != nil {
			return fragments, err
		}
		fragments.values[MarkerProxyLines] = strings.TrimSuffix(lines, "\n")
		// Groups follow the same proxy.Group plan as native Surge. Do not
		// collapse every rendered line into the primary group, and do not put
		// the optional merged Mihomo-Core helper into user groups.
		fragments.values[MarkerProxyGroupLines] = encodeConfGroupLines(proxies, format)
	case model.SubscriptionFormatSurge, model.SubscriptionFormatLoon, model.SubscriptionFormatQX, model.SubscriptionFormatSurfboard:
		lines, err := renderClientLines(proxies, format)
		if err != nil {
			return fragments, err
		}
		fragments.values[MarkerProxyLines] = strings.TrimSuffix(lines, "\n")
		fragments.values[MarkerProxyGroupLines] = encodeConfGroupLines(proxies, format)
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru:
		outbounds, err := encodeSingBoxOutboundsJSON(proxies)
		if err != nil {
			return fragments, err
		}
		fragments.values[MarkerOutboundsJSON] = outbounds
		fragments.values[MarkerRouteRulesJSON] = encodeSingBoxRouteRulesJSON()
	case model.SubscriptionFormatShadowrocket, model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
		list, err := renderCanonicalURIList(proxies)
		if err != nil {
			return fragments, err
		}
		fragments.values[MarkerURILines] = strings.TrimSuffix(list, "\n")
	default:
		return fragments, fmt.Errorf("unsupported subscription format %q", format)
	}
	return fragments, nil
}

func encodeYAMLProxyList(proxies []subscriptionProxy, format model.SubscriptionFormat) (string, error) {
	items := make([]map[string]any, 0, len(proxies))
	for _, proxy := range proxies {
		item, err := proxyMapForYAML(proxy, format)
		if err != nil {
			return "", err
		}
		items = append(items, item)
	}
	return marshalSubscriptionYAML(items)
}

func yamlListOrEmpty(encoded string) string {
	if strings.TrimSpace(encoded) == "" || strings.TrimSpace(encoded) == "null" {
		return "[]"
	}
	return strings.TrimSuffix(encoded, "\n")
}

func subscriptionGroupPlan(proxies []subscriptionProxy) (string, []yamlSubscriptionGroup) {
	grouped := map[string][]string{}
	for _, proxy := range proxies {
		group := proxy.Group
		if group == "" {
			group = "default"
		}
		grouped[group] = append(grouped[group], proxy.Name)
	}
	names := make([]string, 0, len(grouped))
	for name := range grouped {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]yamlSubscriptionGroup, 0, len(names))
	for _, name := range names {
		members := append([]string(nil), grouped[name]...)
		members = append(members, "DIRECT")
		out = append(out, yamlSubscriptionGroup{Name: name, Type: "select", Proxies: members})
	}
	if len(names) == 0 {
		return "DIRECT", []yamlSubscriptionGroup{}
	}
	return names[0], out
}

func encodeConfGroupLines(proxies []subscriptionProxy, format model.SubscriptionFormat) string {
	_, groups := subscriptionGroupPlan(proxies)
	lines := make([]string, 0, len(groups))
	for _, group := range groups {
		members := make([]string, 0, len(group.Proxies))
		for _, member := range group.Proxies {
			if member == "DIRECT" {
				members = append(members, member)
				continue
			}
			members = append(members, sanitizeConfName(member))
		}
		lines = append(lines, sanitizeConfName(group.Name)+" = select, "+strings.Join(members, ", "))
	}
	return strings.Join(lines, "\n")
}

func encodeConfGroupLinesFromProxyLines(lines, primary string) string {
	names := []string{}
	for _, line := range strings.Split(strings.TrimSpace(lines), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		names = append(names, strings.TrimSpace(name))
	}
	if len(names) == 0 {
		return ""
	}
	group := primary
	if group == "" || group == "DIRECT" {
		group = "default"
	}
	return sanitizeConfName(group) + " = select, " + strings.Join(append(names, "DIRECT"), ", ")
}

func validateRenderedSubscription(format model.SubscriptionFormat, body string) error {
	format = normalizeSubscriptionFormat(format)
	switch format {
	case model.SubscriptionFormatMihomo, model.SubscriptionFormatStash, model.SubscriptionFormatEgern:
		var parsed any
		if err := yaml.Unmarshal([]byte(body), &parsed); err != nil {
			return fmt.Errorf("rendered %s document is not valid YAML: %w", format, err)
		}
	case model.SubscriptionFormatSingBox, model.SubscriptionFormatSingBoxMieru:
		var parsed any
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			return fmt.Errorf("rendered %s document is not valid JSON: %w", format, err)
		}
	case model.SubscriptionFormatSurge, model.SubscriptionFormatSurgeMac, model.SubscriptionFormatLoon, model.SubscriptionFormatQX, model.SubscriptionFormatSurfboard:
		if !strings.Contains(body, "[General]") || !strings.Contains(body, "[Proxy]") || !strings.Contains(body, "[Proxy Group]") || !strings.Contains(body, "[Rule]") {
			return fmt.Errorf("rendered %s document is missing required sections", format)
		}
	case model.SubscriptionFormatV2Ray:
		if strings.TrimSpace(body) == "" {
			return nil
		}
		if _, err := base64.StdEncoding.DecodeString(body); err != nil {
			return fmt.Errorf("rendered v2ray document is not valid base64: %w", err)
		}
	}
	return nil
}
