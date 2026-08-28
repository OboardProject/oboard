package core

import (
	"encoding/base64"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

func renderSubscriptionTarget(nodes []SubscriptionNode, format model.SubscriptionFormat) (string, error) {
	return renderSubscriptionDocument(nodes, format, SubscriptionRenderOptions{})
}

// renderSubscriptionDocument encodes protocol fragments first, then injects
// them into the client template. Templates never see raw proxy fields.
func renderSubscriptionDocument(nodes []SubscriptionNode, format model.SubscriptionFormat, opts SubscriptionRenderOptions) (string, error) {
	format = normalizeSubscriptionFormat(format)
	if err := assertConcreteSubscriptionFormat(format); err != nil {
		return "", err
	}
	proxies, err := normalizeSubscriptionNodes(nodes)
	if err != nil {
		return "", err
	}
	compatible := filterCompatibleSubscriptionProxies(proxies, format, opts)
	fragments, err := buildSubscriptionTemplateFragments(compatible, format, opts)
	if err != nil {
		return "", err
	}
	template := opts.Template
	if strings.TrimSpace(template) == "" {
		template, err = BuiltinSubscriptionTemplate(format)
		if err != nil {
			return "", err
		}
	}
	rendered, err := renderClientTemplate(format, template, fragments)
	if err != nil {
		return "", err
	}
	if isURISubscriptionFormat(format) && strings.TrimSpace(rendered) == "" {
		rendered = ""
	}
	if format == model.SubscriptionFormatV2Ray {
		rendered = base64.StdEncoding.EncodeToString([]byte(strings.TrimSuffix(rendered, "\n")))
	}
	if err := validateRenderedSubscription(format, rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

func EffectiveSubscriptionTemplate(format model.SubscriptionFormat, override string) (string, string, error) {
	format = normalizeSubscriptionFormat(format)
	if strings.TrimSpace(override) != "" {
		if err := ValidateSubscriptionTemplate(format, override); err != nil {
			return "", "", err
		}
		return override, subscriptionTemplateDigest(override), nil
	}
	builtin, err := BuiltinSubscriptionTemplate(format)
	if err != nil {
		return "", "", err
	}
	return builtin, subscriptionTemplateDigest(builtin), nil
}

func isURISubscriptionFormat(format model.SubscriptionFormat) bool {
	switch normalizeSubscriptionFormat(format) {
	case model.SubscriptionFormatShadowrocket, model.SubscriptionFormatV2Ray, model.SubscriptionFormatV2RayURI:
		return true
	default:
		return false
	}
}
