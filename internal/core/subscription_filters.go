package core

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// Subscription output filter rules (Sub-Store style) executed as an ordered
// pipeline over the final merged node list of a subscription output. A node
// dropped by any rule stays removed; later keep rules never resurrect it. An
// empty rule list passes everything through.

const (
	// subscriptionOutputMaxFilters bounds the rule count per output.
	subscriptionOutputMaxFilters = 32
	// subscriptionOutputMaxFilterValue bounds the rule value length.
	subscriptionOutputMaxFilterValue = 256
)

// SubscriptionFilterStats reports one rule's effect while applying the
// pipeline, plus the total number of dropped nodes.
type SubscriptionFilterStats struct {
	TotalDropped int                          `json:"total_dropped"`
	Remaining    int                          `json:"remaining"`
	Rules        []SubscriptionFilterRuleStat `json:"rules,omitempty"`
}

// SubscriptionFilterRuleStat is the per-rule outcome of the pipeline stage.
type SubscriptionFilterRuleStat struct {
	Type      string `json:"type"`
	Value     string `json:"value"`
	Matched   int    `json:"matched"`
	Dropped   int    `json:"dropped"`
	Remaining int    `json:"remaining"`
	// SkipReason is set when the rule was skipped entirely (for example an
	// empty node list); it is omitted on normal execution.
	SkipReason string `json:"skip_reason,omitempty"`
}

var subscriptionFilterProtocols = map[string]bool{
	"vless": true, "vmess": true, "trojan": true, "tuic": true,
	"hysteria2": true, "anytls": true, "shadowsocks": true,
	"socks5": true, "socks": true, "snell": true, "mieru": true, "ssh": true,
}

// NormalizeSubscriptionOutputFilters validates and normalizes an ordered
// filter rule list. knownGroups holds the node group IDs owned by the output
// user; group rules referencing any other ID are rejected. The returned list
// is a copy with trimmed values and canonical protocol/region forms.
func NormalizeSubscriptionOutputFilters(filters []model.SubscriptionOutputFilter, knownGroups map[int64]bool) ([]model.SubscriptionOutputFilter, error) {
	if len(filters) > subscriptionOutputMaxFilters {
		return nil, fmt.Errorf("at most %d subscription output filters are allowed", subscriptionOutputMaxFilters)
	}
	out := make([]model.SubscriptionOutputFilter, len(filters))
	for i, filter := range filters {
		filter.Type = strings.TrimSpace(filter.Type)
		filter.Value = strings.TrimSpace(filter.Value)
		if filter.Value == "" || len(filter.Value) > subscriptionOutputMaxFilterValue {
			return nil, fmt.Errorf("filter %d value must be between 1 and %d characters", i+1, subscriptionOutputMaxFilterValue)
		}
		switch filter.Type {
		case model.SubscriptionOutputFilterKeepName, model.SubscriptionOutputFilterDropName:
			if _, err := regexp.Compile(filter.Value); err != nil {
				return nil, fmt.Errorf("filter %d has an invalid regular expression: %v", i+1, err)
			}
		case model.SubscriptionOutputFilterKeepProtocol, model.SubscriptionOutputFilterDropProtocol:
			filter.Value = strings.ToLower(filter.Value)
			if !subscriptionFilterProtocols[filter.Value] {
				return nil, fmt.Errorf("filter %d uses unsupported protocol %q", i+1, filter.Value)
			}
		case model.SubscriptionOutputFilterKeepRegion, model.SubscriptionOutputFilterDropRegion:
			filter.Value = NormalizeRegionCode(filter.Value)
			if filter.Value == "" {
				return nil, fmt.Errorf("filter %d has an invalid region code", i+1)
			}
		case model.SubscriptionOutputFilterKeepGroup, model.SubscriptionOutputFilterDropGroup:
			groupID, err := strconv.ParseInt(filter.Value, 10, 64)
			if err != nil || groupID <= 0 || !knownGroups[groupID] {
				return nil, fmt.Errorf("filter %d references an unknown node group", i+1)
			}
		default:
			return nil, fmt.Errorf("filter %d has unsupported type %q", i+1, filter.Type)
		}
		out[i] = filter
	}
	return out, nil
}

// ApplySubscriptionOutputFilters executes the ordered filter pipeline against
// the node list. groupByNodeKey maps each node's Key to the node group it
// belongs to inside the output composition (private nodes use their owning
// group, OBoard nodes use the system oboard group). The returned list keeps
// the input order of the surviving nodes.
func ApplySubscriptionOutputFilters(nodes []SubscriptionNode, filters []model.SubscriptionOutputFilter, groupByNodeKey map[string]int64) ([]SubscriptionNode, SubscriptionFilterStats) {
	stats := SubscriptionFilterStats{}
	if len(nodes) == 0 {
		stats.Remaining = 0
		for _, filter := range filters {
			stats.Rules = append(stats.Rules, SubscriptionFilterRuleStat{Type: filter.Type, Value: filter.Value, SkipReason: "empty node list"})
		}
		return nil, stats
	}
	if len(filters) == 0 {
		stats.Remaining = len(nodes)
		return nodes, stats
	}
	remaining := append([]SubscriptionNode(nil), nodes...)
	for _, filter := range filters {
		ruleStat := SubscriptionFilterRuleStat{Type: filter.Type, Value: filter.Value, Remaining: len(remaining)}
		kept := remaining[:0]
		for _, node := range remaining {
			matched := subscriptionOutputFilterMatches(filter, node, groupByNodeKey)
			if matched {
				ruleStat.Matched++
				if !subscriptionOutputFilterKeeps(filter.Type) {
					ruleStat.Dropped++
					stats.TotalDropped++
					continue
				}
			} else if subscriptionOutputFilterKeeps(filter.Type) {
				ruleStat.Dropped++
				stats.TotalDropped++
				continue
			}
			kept = append(kept, node)
		}
		remaining = kept
		ruleStat.Remaining = len(remaining)
		stats.Rules = append(stats.Rules, ruleStat)
		if len(remaining) == 0 {
			break
		}
	}
	stats.Remaining = len(remaining)
	return remaining, stats
}

func subscriptionOutputFilterKeeps(filterType string) bool {
	return filterType == model.SubscriptionOutputFilterKeepName ||
		filterType == model.SubscriptionOutputFilterKeepProtocol ||
		filterType == model.SubscriptionOutputFilterKeepRegion ||
		filterType == model.SubscriptionOutputFilterKeepGroup
}

func subscriptionOutputFilterMatches(filter model.SubscriptionOutputFilter, node SubscriptionNode, groupByNodeKey map[string]int64) bool {
	switch filter.Type {
	case model.SubscriptionOutputFilterKeepName, model.SubscriptionOutputFilterDropName:
		expression, err := regexp.Compile(filter.Value)
		if err != nil {
			return false
		}
		return expression.MatchString(StripSubscriptionNodeRegionFlag(node.Name))
	case model.SubscriptionOutputFilterKeepProtocol, model.SubscriptionOutputFilterDropProtocol:
		return strings.ToLower(stringFromAny(node.Raw["type"])) == filter.Value
	case model.SubscriptionOutputFilterKeepRegion, model.SubscriptionOutputFilterDropRegion:
		region := NormalizeRegionCode(node.ExitRegion)
		if region == "" {
			region = NormalizeRegionCode(node.EntryRegion)
		}
		return region == filter.Value && region != ""
	case model.SubscriptionOutputFilterKeepGroup, model.SubscriptionOutputFilterDropGroup:
		groupID, err := strconv.ParseInt(filter.Value, 10, 64)
		if err != nil {
			return false
		}
		return groupByNodeKey != nil && groupByNodeKey[node.Key] == groupID
	}
	return false
}

// StripSubscriptionNodeRegionFlag removes a leading region-flag prefix (two
// regional-indicator code points plus one optional following space) from a
// rendered subscription node name so regex filters match the base name the
// operator configures in the panel.
func StripSubscriptionNodeRegionFlag(name string) string {
	runes := []rune(name)
	if len(runes) < 2 {
		return name
	}
	if runes[0] < 0x1F1E6 || runes[0] > 0x1F1FF || runes[1] < 0x1F1E6 || runes[1] > 0x1F1FF {
		return name
	}
	end := 2
	if len(runes) > end && runes[end] == ' ' {
		end++
	}
	return string(runes[end:])
}

// SortSubscriptionOutputFiltersForDigest returns a stable copy of the rule
// list sorted by type and value, used only for immutable comparisons (the
// executed pipeline always keeps the configured order).
func SortSubscriptionOutputFiltersForDigest(filters []model.SubscriptionOutputFilter) []model.SubscriptionOutputFilter {
	out := append([]model.SubscriptionOutputFilter(nil), filters...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Value < out[j].Value
	})
	return out
}
