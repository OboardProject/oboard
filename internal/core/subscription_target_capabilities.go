package core

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// ProtocolFeature is the client capability dimension. Renderers must never
// guess – filtering happens before rendering based on this matrix.
type ProtocolFeature string

const (
	FeatureSnell                 ProtocolFeature = "snell"
	FeatureSnellMultiUserUserKey ProtocolFeature = "snell_multi_user_userkey"
	FeatureSnellV4               ProtocolFeature = "snell_v4"
	FeatureSnellV6               ProtocolFeature = "snell_v6"
	FeatureSnellReuse            ProtocolFeature = "snell_reuse"
)

// CapabilityState tracks whether a feature is known to be supported.
type CapabilityState string

const (
	CapabilitySupported   CapabilityState = "supported"
	CapabilityUnsupported CapabilityState = "unsupported"
	CapabilityUnknown     CapabilityState = "unknown"
)

// SubscriptionTargetCapabilities is the single capability source for one
// concrete subscription target + detected client version.
type SubscriptionTargetCapabilities struct {
	Features map[ProtocolFeature]CapabilityState
}

// mihomoSnellUserKeyThreshold is the first Mihomo version that supports
// snell_multi_user_userkey. 99.99.99 means currently none, i.e. baseline
// is unsupported. When Mihomo releases support, only this constant needs
// to change and no renderer needs to be rewritten.
const mihomoSnellUserKeyThreshold = "99.99.99"

// mihomoVersionPattern extracts a version like 1.19.30 from a UA such as
// "Mihomo/1.19.30" or "Clash-Meta/1.19.30".
var mihomoVersionPattern = regexp.MustCompile(`(?i)(?:mihomo|clash[-_ ]?meta|clash)[/\s]v?([0-9]+\.[0-9]+\.[0-9]+)`)

// RequiredFeaturesForProxy returns the features a normalized proxy requires
// from the target client. For Snell the server is always multi-user
// (psk + per-user userkey) and the renderer must not strip or demote it.
func RequiredFeaturesForProxy(proxy subscriptionProxy) []ProtocolFeature {
	switch proxy.Type {
	case "snell":
		features := []ProtocolFeature{FeatureSnell, FeatureSnellMultiUserUserKey}
		if proxy.Version == SnellVersionV4 {
			features = append(features, FeatureSnellV4)
		} else if proxy.Version == SnellVersionV6 {
			features = append(features, FeatureSnellV6)
		}
		if proxy.Reuse {
			features = append(features, FeatureSnellReuse)
		}
		return features
	default:
		return nil
	}
}

// RequiredFeaturesForInbound returns the features a Snell inbound requires.
// It is used by MCP form validation without constructing a full proxy.
func RequiredFeaturesForInbound(inbound model.Inbound) []ProtocolFeature {
	if inbound.Protocol != model.ProtocolSnell {
		return nil
	}
	extra := parseExtra(inbound.ConfigJSON)
	version, _ := snellPanelVersion(extra)
	features := []ProtocolFeature{FeatureSnell, FeatureSnellMultiUserUserKey}
	if version == SnellVersionV4 {
		features = append(features, FeatureSnellV4)
	} else if version == SnellVersionV6 {
		features = append(features, FeatureSnellV6)
	}
	if reuse, ok := extra["reuse"]; ok {
		if b, ok := reuse.(bool); ok && b {
			features = append(features, FeatureSnellReuse)
		}
	}
	return features
}

// ResolveTargetCapabilities returns the feature matrix for a concrete format
// + userAgent. For format=mihomo with no reliable version the conservative
// baseline is used (snell_multi_user_userkey = unsupported).
func ResolveTargetCapabilities(format model.SubscriptionFormat, userAgent string) SubscriptionTargetCapabilities {
	format = normalizeSubscriptionFormat(format)
	caps := SubscriptionTargetCapabilities{Features: map[ProtocolFeature]CapabilityState{}}
	switch format {
	case model.SubscriptionFormatSingBox:
		caps.Features[FeatureSnell] = CapabilitySupported
		caps.Features[FeatureSnellMultiUserUserKey] = CapabilitySupported
		caps.Features[FeatureSnellV4] = CapabilitySupported
		caps.Features[FeatureSnellV6] = CapabilitySupported
		caps.Features[FeatureSnellReuse] = CapabilitySupported
		return caps
	case model.SubscriptionFormatMihomo:
		caps.Features[FeatureSnell] = CapabilitySupported
		caps.Features[FeatureSnellV4] = CapabilitySupported
		caps.Features[FeatureSnellV6] = CapabilityUnsupported
		caps.Features[FeatureSnellReuse] = CapabilitySupported
		version := extractMihomoVersion(userAgent)
		if version != "" && versionGTE(version, mihomoSnellUserKeyThreshold) {
			caps.Features[FeatureSnellMultiUserUserKey] = CapabilitySupported
		} else {
			caps.Features[FeatureSnellMultiUserUserKey] = CapabilityUnsupported
		}
		return caps
	default:
		// For all other formats keep the previous behaviour: do not filter
		// Snell on multi-user grounds. Mark as unknown so the filter does
		// not act conservatively without evidence.
		caps.Features[FeatureSnell] = CapabilityUnknown
		caps.Features[FeatureSnellMultiUserUserKey] = CapabilityUnknown
		caps.Features[FeatureSnellV4] = CapabilityUnknown
		caps.Features[FeatureSnellV6] = CapabilityUnknown
		caps.Features[FeatureSnellReuse] = CapabilityUnknown
		return caps
	}
}

// IsFeatureSupported reports whether a required feature is supported for the
// target. Unknown is treated as not filtering (preserve existing nodes) to
// avoid regressions for unvalidated clients. Only explicit unsupported
// triggers filtering.
func IsFeatureSupported(caps SubscriptionTargetCapabilities, feature ProtocolFeature) bool {
	state, ok := caps.Features[feature]
	if !ok || state == CapabilityUnknown {
		return true
	}
	return state == CapabilitySupported
}

// FilteredNode describes why a node was filtered.
type FilteredNode struct {
	NodeID   int64  `json:"node_id"`
	Protocol string `json:"protocol"`
	Format   string `json:"format"`
	Reason   string `json:"reason"`
	Feature  string `json:"feature"`
}

func extractMihomoVersion(userAgent string) string {
	m := mihomoVersionPattern.FindStringSubmatch(userAgent)
	if len(m) >= 2 {
		return m[1]
	}
	// Fallback: look for generic version after Mihomo
	lower := strings.ToLower(userAgent)
	if idx := strings.Index(lower, "mihomo"); idx >= 0 {
		rest := userAgent[idx:]
		// find first version token
		re := regexp.MustCompile(`([0-9]+\.[0-9]+\.[0-9]+)`)
		if v := re.FindString(rest); v != "" {
			return v
		}
	}
	return ""
}

func versionGTE(a, b string) bool {
	pa := parseVersion(a)
	pb := parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] > pb[i] {
			return true
		}
		if pa[i] < pb[i] {
			return false
		}
	}
	return true
}

func parseVersion(v string) [3]int {
	parts := strings.Split(strings.TrimSpace(v), ".")
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(parts[i])
		out[i] = n
	}
	return out
}
