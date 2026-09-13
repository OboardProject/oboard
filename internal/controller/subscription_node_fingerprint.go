package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

// Per-node rendering fingerprints.
//
// A published subscription used to be keyed by the global routing revision,
// which meant editing any one inbound in the fleet invalidated every user's
// published body. The fingerprint below replaces that with what a node is
// actually rendered from, so a change reaches the users whose nodes it is part
// of and leaves the rest alone.
//
// The digest is taken over the rows themselves rather than a chosen set of
// fields: adding a column to an inbound or a step changes the digest without
// anyone remembering to update this file. What still has to be right is the
// set of rows a node reaches, which is why the relations below are kept
// together with the reason each one is part of rendering.
//
// The map is built once per routing revision and shared by every pull in it.

// subscriptionNodeFingerprints returns node key -> digest for every assignable
// node in this snapshot.
func (r *routingSnapshot) subscriptionNodeFingerprints() map[string]string {
	r.nodeFingerprintOnce.Do(func() {
		r.nodeFingerprints = buildSubscriptionNodeFingerprints(r.data)
	})
	return r.nodeFingerprints
}

func buildSubscriptionNodeFingerprints(data store.FullRoutingConfig) map[string]string {
	serversByID := make(map[int64]model.Server, len(data.Servers))
	for _, server := range data.Servers {
		serversByID[server.ID] = server
	}
	inboundsByID := make(map[int64]model.Inbound, len(data.Inbounds))
	for _, inbound := range data.Inbounds {
		inboundsByID[inbound.ID] = inbound
	}
	externalByID := make(map[int64]model.ExternalOutbound, len(data.ExternalOutbounds))
	for _, external := range data.ExternalOutbounds {
		externalByID[external.ID] = external
	}
	stepsByPath := map[int64][]model.ProxyPathStep{}
	for _, step := range data.ProxyPathSteps {
		stepsByPath[step.PathID] = append(stepsByPath[step.PathID], step)
	}
	for pathID := range stepsByPath {
		steps := stepsByPath[pathID]
		sort.Slice(steps, func(i, j int) bool { return steps[i].Position < steps[j].Position })
	}
	egressByPath := map[int64][]model.ProxyPathEgressResult{}
	for _, result := range data.ProxyPathEgressResults {
		egressByPath[result.PathID] = append(egressByPath[result.PathID], result)
	}
	// A path port allocation names its owner through the scope key the
	// generator writes as "<path id>:<step position>". Attributing by that
	// prefix is what ties a reallocated port to the chains that advertise it;
	// TestPublishedSubscriptionTracksItsOwnInputs fails loudly if the
	// convention ever changes.
	allocationsByPath := map[int64][]model.ProxyPathPortAllocation{}
	for _, allocation := range data.ProxyPathPortAllocations {
		pathID, ok := proxyPathIDFromScopeKey(allocation.ScopeKey)
		if !ok {
			continue
		}
		allocationsByPath[pathID] = append(allocationsByPath[pathID], allocation)
	}
	rulesByPath := map[int64][]model.RoutingRule{}
	rulesByTemplate := map[int64][]model.RoutingRule{}
	for _, rule := range data.RoutingRules {
		if rule.ProxyPathID != nil {
			rulesByPath[*rule.ProxyPathID] = append(rulesByPath[*rule.ProxyPathID], rule)
		}
		if rule.TargetProxyPathID != nil {
			rulesByPath[*rule.TargetProxyPathID] = append(rulesByPath[*rule.TargetProxyPathID], rule)
		}
		if rule.FamilySplitTemplateID != nil {
			rulesByTemplate[*rule.FamilySplitTemplateID] = append(rulesByTemplate[*rule.FamilySplitTemplateID], rule)
		}
	}

	out := make(map[string]string, len(data.Inbounds)+len(data.ProxyPaths))
	for _, inbound := range data.Inbounds {
		// A standalone inbound node renders from the inbound and the server it
		// is advertised on.
		out[core.NodeKeyOf(model.AssignableNodeInbound, inbound.ID)] = digestSubscriptionInputs(map[string]any{
			"inbound": inbound,
			"server":  serversByID[inbound.ServerID],
		})
	}
	for _, path := range data.ProxyPaths {
		steps := stepsByPath[path.ID]
		stepServers := make([]model.Server, 0, len(steps)+1)
		stepInbounds := make([]model.Inbound, 0, len(steps)+1)
		externals := make([]model.ExternalOutbound, 0, len(steps))
		// The entry the path is rooted at, and the server that advertises it.
		if rootInbound, ok := inboundsByID[path.InboundID]; ok {
			stepInbounds = append(stepInbounds, rootInbound)
			stepServers = append(stepServers, serversByID[rootInbound.ServerID])
		}
		for _, step := range steps {
			// Every hop contributes the server it sits on and the inbound it
			// dials, because both shape the listeners and addresses rendered
			// for the chain.
			if step.ServerID != nil {
				stepServers = append(stepServers, serversByID[*step.ServerID])
			}
			if step.InboundID != nil {
				hop := inboundsByID[*step.InboundID]
				stepInbounds = append(stepInbounds, hop)
				stepServers = append(stepServers, serversByID[hop.ServerID])
			}
			if step.ExternalOutboundID != nil {
				externals = append(externals, externalByID[*step.ExternalOutboundID])
			}
		}
		rules := rulesByPath[path.ID]
		if path.TemplateID != nil {
			// A family-split rule can hide a branch it does not name directly,
			// so the rules bound to the branch's template are part of it.
			rules = append(rules, rulesByTemplate[*path.TemplateID]...)
		}
		out[core.NodeKeyOf(model.AssignableNodeProxyPath, path.ID)] = digestSubscriptionInputs(map[string]any{
			"path":        path,
			"steps":       steps,
			"servers":     stepServers,
			"inbounds":    stepInbounds,
			"externals":   externals,
			"egress":      egressByPath[path.ID],
			"allocations": allocationsByPath[path.ID],
			"rules":       rules,
		})
	}
	return out
}

func proxyPathIDFromScopeKey(scopeKey string) (int64, bool) {
	head, _, found := strings.Cut(scopeKey, ":")
	if !found {
		return 0, false
	}
	id, err := strconv.ParseInt(head, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func digestSubscriptionInputs(value map[string]any) string {
	var buf strings.Builder
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		buf.WriteString(key)
		buf.WriteByte('\x00')
		writeStableValue(&buf, reflect.ValueOf(value[key]))
		buf.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(buf.String()))
	return hex.EncodeToString(sum[:])
}

// writeStableValue encodes every exported field, deliberately ignoring struct
// tags.
//
// JSON would have been shorter, but it skips `json:"-"` fields, and two of
// those decide what a client sees: a proxy path's stored name template is the
// node's displayed name, and a server carries state that is hidden from API
// responses. Fingerprinting through JSON would have kept serving the previous
// name after an operator renamed a path. Encoding by reflection instead means a
// column added later is covered without anyone remembering this file.
func writeStableValue(buf *strings.Builder, value reflect.Value) {
	if !value.IsValid() {
		buf.WriteString("nil")
		return
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if value.IsNil() {
			buf.WriteString("nil")
			return
		}
		writeStableValue(buf, value.Elem())
	case reflect.Struct:
		if stamp, ok := value.Interface().(time.Time); ok {
			buf.WriteString(strconv.FormatInt(stamp.UTC().UnixNano(), 10))
			return
		}
		valueType := value.Type()
		for i := 0; i < value.NumField(); i++ {
			if !valueType.Field(i).IsExported() {
				continue
			}
			buf.WriteString(valueType.Field(i).Name)
			buf.WriteByte('=')
			writeStableValue(buf, value.Field(i))
			buf.WriteByte(';')
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			writeStableValue(buf, value.Index(i))
			buf.WriteByte(',')
		}
	case reflect.Map:
		entries := make([]string, 0, value.Len())
		for _, key := range value.MapKeys() {
			var entry strings.Builder
			writeStableValue(&entry, key)
			entry.WriteByte(':')
			writeStableValue(&entry, value.MapIndex(key))
			entries = append(entries, entry.String())
		}
		sort.Strings(entries)
		buf.WriteString(strings.Join(entries, ","))
	default:
		fmt.Fprintf(buf, "%v", value.Interface())
	}
}

// subscriptionNodeFingerprintsFor narrows the map to the nodes one user is
// authorized on, which is what their published body is rendered from.
func subscriptionNodeFingerprintsFor(all map[string]string, nodes map[string]bool) map[string]string {
	out := make(map[string]string, len(nodes))
	for key, granted := range nodes {
		if !granted {
			continue
		}
		digest, ok := all[key]
		if !ok {
			// A node with no fingerprint is one this snapshot does not know
			// about. Marking it keeps the key honest instead of silently
			// matching a previous publication.
			digest = "unknown"
		}
		out[key] = digest
	}
	return out
}
