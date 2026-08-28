package core

import (
	"strings"
	"unicode"

	"github.com/OboardProject/oboard/internal/model"
)

// SubscriptionClientMatch is the shared User-Agent detection result used by
// format=auto and subscription audit. One rule table only.
type SubscriptionClientMatch struct {
	RequestedFormat model.SubscriptionFormat
	ResolvedFormat  model.SubscriptionFormat
	ClientName      string
	RuleID          string
	Matched         bool
}

type subscriptionClientRule struct {
	id       string
	name     string
	format   model.SubscriptionFormat
	matched  bool
	match    func(lower string) bool
}

func subscriptionClientRules() []subscriptionClientRule {
	contains := func(needles ...string) func(string) bool {
		return func(lower string) bool {
			for _, needle := range needles {
				if strings.Contains(lower, needle) {
					return true
				}
			}
			return false
		}
	}
	return []subscriptionClientRule{
		{id: "mihomo", name: "Mihomo", format: model.SubscriptionFormatMihomo, matched: true, match: contains("mihomo")},
		{id: "clash-meta", name: "Clash Meta", format: model.SubscriptionFormatMihomo, matched: true, match: contains("clash.meta", "clash meta", "clash-meta", "clashmeta")},
		{id: "clash-verge", name: "Clash Verge", format: model.SubscriptionFormatMihomo, matched: true, match: contains("clash verge", "clash-verge", "clashverge", "verge rev", "verge-rev")},
		{id: "nyanpasu", name: "Clash Nyanpasu", format: model.SubscriptionFormatMihomo, matched: true, match: contains("nyanpasu")},
		{id: "flclash", name: "FlClash", format: model.SubscriptionFormatMihomo, matched: true, match: contains("flclash")},
		{id: "clashx-meta", name: "ClashX Meta", format: model.SubscriptionFormatMihomo, matched: true, match: contains("clashx meta", "clashx-meta", "clashxmeta")},
		{id: "clash", name: "Clash", format: model.SubscriptionFormatMihomo, matched: true, match: contains("clash")},
		{id: "surge-mac", name: "Surge Mac", format: model.SubscriptionFormatSurgeMac, matched: true, match: func(lower string) bool {
			if !strings.Contains(lower, "surge") {
				return false
			}
			return strings.Contains(lower, "surge mac") || strings.Contains(lower, "surgemac") || strings.Contains(lower, " macos") || strings.Contains(lower, " mac/") || strings.HasSuffix(lower, " mac") || strings.Contains(lower, " mac ")
		}},
		{id: "surge", name: "Surge", format: model.SubscriptionFormatSurge, matched: true, match: contains("surge")},
		{id: "stash", name: "Stash", format: model.SubscriptionFormatStash, matched: true, match: contains("stash")},
		{id: "shadowrocket", name: "Shadowrocket", format: model.SubscriptionFormatShadowrocket, matched: true, match: contains("shadowrocket")},
		{id: "loon", name: "Loon", format: model.SubscriptionFormatLoon, matched: true, match: contains("loon")},
		{id: "egern", name: "Egern", format: model.SubscriptionFormatEgern, matched: true, match: contains("egern")},
		{id: "quantumult-x", name: "Quantumult X", format: model.SubscriptionFormatQX, matched: true, match: contains("quantumult")},
		{id: "surfboard", name: "Surfboard", format: model.SubscriptionFormatSurfboard, matched: true, match: contains("surfboard")},
		{id: "sfa", name: "SFA", format: model.SubscriptionFormatSingBox, matched: true, match: contains("sfa/")},
		{id: "sfi", name: "SFI", format: model.SubscriptionFormatSingBox, matched: true, match: contains("sfi/")},
		{id: "sfm", name: "SFM", format: model.SubscriptionFormatSingBox, matched: true, match: contains("sfm/")},
		{id: "sing-box", name: "sing-box", format: model.SubscriptionFormatSingBox, matched: true, match: contains("sing-box", "singbox")},
		{id: "v2rayng", name: "v2rayNG", format: model.SubscriptionFormatV2Ray, matched: true, match: contains("v2rayng")},
		{id: "v2rayn", name: "v2rayN", format: model.SubscriptionFormatV2Ray, matched: true, match: contains("v2rayn")},
		{id: "mieru", name: "Mieru", format: model.SubscriptionFormatMihomo, matched: false, match: contains("mieru")},
	}
}

// DetectSubscriptionClient maps a User-Agent to a concrete client format.
// Official Mieru Client is recognized for audit naming only and never reopens
// format=mieru; it falls back to Mihomo.
func DetectSubscriptionClient(userAgent string) SubscriptionClientMatch {
	lower := strings.ToLower(sanitizeDetectedUserAgent(userAgent))
	for _, rule := range subscriptionClientRules() {
		if rule.match(lower) {
			return SubscriptionClientMatch{
				ResolvedFormat: rule.format,
				ClientName:     rule.name,
				RuleID:         rule.id,
				Matched:        rule.matched,
			}
		}
	}
	return SubscriptionClientMatch{
		ResolvedFormat: model.SubscriptionFormatMihomo,
		ClientName:     unknownSubscriptionClientName(lower),
		RuleID:         "unknown",
		Matched:        false,
	}
}

// SubscriptionFormatResolution is the request-vs-render split: Auto is never a
// renderer input.
type SubscriptionFormatResolution struct {
	Requested model.SubscriptionFormat
	Resolved  model.SubscriptionFormat
	Match     SubscriptionClientMatch
	Auto      bool
}

func ResolveSubscriptionFormat(requested model.SubscriptionFormat, userAgent string) SubscriptionFormatResolution {
	raw := strings.TrimSpace(string(requested))
	match := DetectSubscriptionClient(userAgent)
	if raw == "" {
		match.RequestedFormat = model.SubscriptionFormatSingBox
		match.ResolvedFormat = model.SubscriptionFormatSingBox
		return SubscriptionFormatResolution{
			Requested: model.SubscriptionFormatSingBox,
			Resolved:  model.SubscriptionFormatSingBox,
			Match:     match,
		}
	}
	normalized := normalizeSubscriptionFormat(model.SubscriptionFormat(raw))
	if normalized == model.SubscriptionFormatAuto {
		resolved := match.ResolvedFormat
		if resolved == "" {
			resolved = model.SubscriptionFormatMihomo
		}
		match.RequestedFormat = model.SubscriptionFormatAuto
		match.ResolvedFormat = resolved
		return SubscriptionFormatResolution{
			Requested: model.SubscriptionFormatAuto,
			Resolved:  resolved,
			Match:     match,
			Auto:      true,
		}
	}
	match.RequestedFormat = normalized
	match.ResolvedFormat = normalized
	return SubscriptionFormatResolution{
		Requested: normalized,
		Resolved:  normalized,
		Match:     match,
	}
}

func sanitizeDetectedUserAgent(raw string) string {
	raw = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	runes := []rune(raw)
	if len(runes) > 512 {
		raw = string(runes[:512])
	}
	return raw
}

func unknownSubscriptionClientName(lower string) string {
	if lower == "" {
		return "未知客户端"
	}
	product := strings.Fields(lower)[0]
	if index := strings.IndexByte(product, '/'); index > 0 {
		product = product[:index]
	}
	runes := []rune(product)
	if len(runes) > 48 {
		product = string(runes[:48])
	}
	if product == "" {
		return "其他客户端"
	}
	return product
}
