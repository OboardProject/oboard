package plugin

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

// Grant is an administrator decision for one plugin instance and one exact
// package version. It is never a boolean: every resource-scoped capability
// names the servers, hosts or channels it may touch.
type Grant struct {
	Capabilities map[string]CapabilityGrant `json:"capabilities"`
}

type CapabilityGrant struct {
	Servers  []int64  `json:"servers,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	Channels []int64  `json:"channels,omitempty"`
}

func ParseGrant(raw []byte) (Grant, error) {
	grant := Grant{Capabilities: map[string]CapabilityGrant{}}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return grant, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&grant); err != nil {
		return Grant{Capabilities: map[string]CapabilityGrant{}}, Fail(CodeInvalidArgument, "grant is not a closed object")
	}
	if grant.Capabilities == nil {
		grant.Capabilities = map[string]CapabilityGrant{}
	}
	return grant, nil
}

// ValidateGrant checks a proposed grant against the manifest it binds to and
// normalizes it. A grant can never exceed the declaration.
func ValidateGrant(manifest Manifest, grant *Grant, serverExists func(int64) bool, channelExists func(int64) bool) error {
	if grant.Capabilities == nil {
		grant.Capabilities = map[string]CapabilityGrant{}
	}
	for name, scope := range grant.Capabilities {
		spec, ok := LookupCapability(name)
		if !ok || forbiddenCapability(name) {
			return FailField(CodeInvalidArgument, "capabilities."+name, "unknown capability")
		}
		if !manifest.HasCapability(name) {
			return FailField(CodeInvalidArgument, "capabilities."+name, "the plugin version does not declare this capability")
		}
		switch spec.Resource {
		case ResourceServer:
			if len(scope.Hosts) > 0 || len(scope.Channels) > 0 {
				return FailField(CodeInvalidArgument, "capabilities."+name, "only a server scope is valid here")
			}
			ids, err := normalizeIDs(scope.Servers, serverExists)
			if err != nil {
				return FailField(CodeInvalidArgument, "capabilities."+name+".servers", err.Error())
			}
			if len(ids) == 0 {
				return FailField(CodeInvalidArgument, "capabilities."+name+".servers", "select at least one server or leave the capability ungranted")
			}
			scope.Servers = ids
		case ResourceHTTPHost:
			if len(scope.Servers) > 0 || len(scope.Channels) > 0 || manifest.HTTP == nil {
				return FailField(CodeInvalidArgument, "capabilities."+name, "only declared hosts are valid here")
			}
			declared := map[string]bool{}
			for _, host := range manifest.HTTP.Hosts {
				declared[host] = true
			}
			seen := map[string]bool{}
			hosts := []string{}
			for _, host := range scope.Hosts {
				host = strings.ToLower(strings.TrimSpace(host))
				if !declared[host] {
					return FailField(CodeInvalidArgument, "capabilities."+name+".hosts", "host "+host+" is not declared by the plugin")
				}
				if !seen[host] {
					seen[host] = true
					hosts = append(hosts, host)
				}
			}
			if len(hosts) == 0 {
				return FailField(CodeInvalidArgument, "capabilities."+name+".hosts", "select at least one host or leave the capability ungranted")
			}
			sort.Strings(hosts)
			scope.Hosts = hosts
		case ResourceNotificationChannel:
			if len(scope.Servers) > 0 || len(scope.Hosts) > 0 {
				return FailField(CodeInvalidArgument, "capabilities."+name, "only a channel scope is valid here")
			}
			ids, err := normalizeIDs(scope.Channels, channelExists)
			if err != nil {
				return FailField(CodeInvalidArgument, "capabilities."+name+".channels", err.Error())
			}
			if len(ids) == 0 {
				return FailField(CodeInvalidArgument, "capabilities."+name+".channels", "select at least one notification channel")
			}
			scope.Channels = ids
		default:
			if len(scope.Servers) > 0 || len(scope.Hosts) > 0 || len(scope.Channels) > 0 {
				return FailField(CodeInvalidArgument, "capabilities."+name, "this capability has no resource scope")
			}
		}
		grant.Capabilities[name] = scope
	}
	if req := manifestServerRequirement(manifest); req != nil {
		union := grant.ServerUnion()
		if len(union) > 0 && req.Max > 0 && len(union) > req.Max {
			return FailField(CodeInvalidArgument, "capabilities", "the plugin accepts at most the declared number of servers")
		}
	}
	return nil
}

func manifestServerRequirement(manifest Manifest) *ServerRequirement {
	if manifest.Resources == nil {
		return nil
	}
	return manifest.Resources.Servers
}

func normalizeIDs(ids []int64, exists func(int64) bool) ([]int64, error) {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if id <= 0 {
			return nil, Fail(CodeInvalidArgument, "IDs must be positive")
		}
		if exists != nil && !exists(id) {
			return nil, Fail(CodeNotFound, "a selected resource no longer exists")
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) > 256 {
		return nil, Fail(CodeInvalidArgument, "at most 256 resources per capability")
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// Allows reports whether the grant (already bound to the right version)
// includes the capability.
func (g Grant) Allows(capability string) bool {
	_, ok := g.Capabilities[capability]
	return ok
}

func (g Grant) AllowsServer(capability string, serverID int64) bool {
	scope, ok := g.Capabilities[capability]
	if !ok {
		return false
	}
	for _, id := range scope.Servers {
		if id == serverID {
			return true
		}
	}
	return false
}

func (g Grant) AllowsChannel(channelID int64) bool {
	scope, ok := g.Capabilities[CapNotificationsSend]
	if !ok {
		return false
	}
	for _, id := range scope.Channels {
		if id == channelID {
			return true
		}
	}
	return false
}

func (g Grant) HTTPHosts() []string {
	return append([]string(nil), g.Capabilities[CapHTTPRequest].Hosts...)
}

// ServerUnion is the set of servers any granted server-scoped capability may
// touch. Environment server values outside it are configuration_invalid.
func (g Grant) ServerUnion() map[int64]bool {
	union := map[int64]bool{}
	for name, scope := range g.Capabilities {
		if spec, ok := LookupCapability(name); ok && spec.Resource == ResourceServer {
			for _, id := range scope.Servers {
				union[id] = true
			}
		}
	}
	return union
}

func (g Grant) CanonicalJSON() []byte {
	if g.Capabilities == nil {
		g.Capabilities = map[string]CapabilityGrant{}
	}
	raw, _ := json.Marshal(g)
	return raw
}

// RestrictTo carries a grant to a new version: only capabilities and hosts the
// new manifest still declares survive. It never adds anything.
func (g Grant) RestrictTo(manifest Manifest) Grant {
	out := Grant{Capabilities: map[string]CapabilityGrant{}}
	declaredHosts := map[string]bool{}
	if manifest.HTTP != nil {
		for _, host := range manifest.HTTP.Hosts {
			declaredHosts[host] = true
		}
	}
	for name, scope := range g.Capabilities {
		if !manifest.HasCapability(name) {
			continue
		}
		if name == CapHTTPRequest {
			hosts := []string{}
			for _, host := range scope.Hosts {
				if declaredHosts[host] {
					hosts = append(hosts, host)
				}
			}
			if len(hosts) == 0 {
				continue
			}
			scope.Hosts = hosts
		}
		out.Capabilities[name] = scope
	}
	return out
}

// PermissionDiff describes how a new version changes what a plugin may ask
// for. Expanded diffs require an explicit administrator review.
type PermissionDiff struct {
	AddedCapabilities   []string `json:"added_capabilities"`
	RemovedCapabilities []string `json:"removed_capabilities"`
	AddedHosts          []string `json:"added_hosts"`
	RemovedHosts        []string `json:"removed_hosts"`
	AddedMethods        []string `json:"added_methods"`
	AddedEvents         []string `json:"added_events"`
	AddedSecrets        []string `json:"added_secrets"`
	AddedEnvironment    []string `json:"added_environment"`
	RemovedEnvironment  []string `json:"removed_environment"`
	ChangedEnvironment  []string `json:"changed_environment"`
	NewRequired         []string `json:"new_required"`
	AddedPages          []string `json:"added_pages"`
	AddedActions        []string `json:"added_actions"`
	ResourcesExpanded   bool     `json:"resources_expanded"`
	Expanded            bool     `json:"expanded"`
}

func DiffPermissions(previous *Manifest, next Manifest) PermissionDiff {
	diff := PermissionDiff{
		AddedCapabilities: []string{}, RemovedCapabilities: []string{}, AddedHosts: []string{}, RemovedHosts: []string{},
		AddedMethods: []string{}, AddedEvents: []string{}, AddedSecrets: []string{}, AddedEnvironment: []string{},
		RemovedEnvironment: []string{}, ChangedEnvironment: []string{}, NewRequired: []string{},
		AddedPages: []string{}, AddedActions: []string{},
	}
	var prev Manifest
	if previous != nil {
		prev = *previous
	}
	diff.AddedCapabilities, diff.RemovedCapabilities = setDiff(prev.Capabilities, next.Capabilities)
	var prevHosts, nextHosts, prevMethods, nextMethods []string
	if prev.HTTP != nil {
		prevHosts, prevMethods = prev.HTTP.Hosts, prev.HTTP.Methods
	}
	if next.HTTP != nil {
		nextHosts, nextMethods = next.HTTP.Hosts, next.HTTP.Methods
	}
	diff.AddedHosts, diff.RemovedHosts = setDiff(prevHosts, nextHosts)
	diff.AddedMethods, _ = setDiff(prevMethods, nextMethods)
	diff.AddedEvents, _ = setDiff(prev.Triggers.Events, next.Triggers.Events)
	prevFields := map[string]EnvField{}
	for _, field := range prev.Environment {
		prevFields[field.Name] = field
	}
	nextFields := map[string]bool{}
	for _, field := range next.Environment {
		nextFields[field.Name] = true
		old, existed := prevFields[field.Name]
		if !existed {
			diff.AddedEnvironment = append(diff.AddedEnvironment, field.Name)
			if field.Type == EnvSecret {
				diff.AddedSecrets = append(diff.AddedSecrets, field.Name)
			}
			if field.Required && len(field.Default) == 0 {
				diff.NewRequired = append(diff.NewRequired, field.Name)
			}
			continue
		}
		oldRaw, _ := json.Marshal(old)
		newRaw, _ := json.Marshal(field)
		if !bytes.Equal(oldRaw, newRaw) {
			diff.ChangedEnvironment = append(diff.ChangedEnvironment, field.Name)
			if field.Type == EnvSecret && old.Type != EnvSecret {
				diff.AddedSecrets = append(diff.AddedSecrets, field.Name)
			}
			if field.Required && !old.Required && len(field.Default) == 0 {
				diff.NewRequired = append(diff.NewRequired, field.Name)
			}
		}
	}
	for name := range prevFields {
		if !nextFields[name] {
			diff.RemovedEnvironment = append(diff.RemovedEnvironment, name)
		}
	}
	sort.Strings(diff.RemovedEnvironment)
	prevPages := map[string]map[string]bool{}
	for _, page := range prev.Pages {
		actions := map[string]bool{}
		for _, action := range page.Actions {
			actions[action] = true
		}
		prevPages[page.ID] = actions
	}
	for _, page := range next.Pages {
		old, existed := prevPages[page.ID]
		if !existed {
			diff.AddedPages = append(diff.AddedPages, page.ID)
		}
		for _, action := range page.Actions {
			if !existed || !old[action] {
				diff.AddedActions = append(diff.AddedActions, page.ID+"/"+action)
			}
		}
	}
	sort.Strings(diff.AddedPages)
	sort.Strings(diff.AddedActions)
	prevReq, nextReq := manifestServerRequirement(prev), manifestServerRequirement(next)
	if nextReq != nil && (prevReq == nil || nextReq.Min > prevReq.Min || nextReq.Max > prevReq.Max && prevReq.Max != 0 || nextReq.Max == 0 && prevReq.Max != 0) {
		diff.ResourcesExpanded = previous != nil
	}
	diff.Expanded = len(diff.AddedCapabilities) > 0 || len(diff.AddedHosts) > 0 || len(diff.AddedMethods) > 0 ||
		len(diff.AddedEvents) > 0 || len(diff.AddedSecrets) > 0 || len(diff.AddedPages) > 0 || len(diff.AddedActions) > 0 || diff.ResourcesExpanded
	return diff
}

func setDiff(before, after []string) (added, removed []string) {
	b := map[string]bool{}
	for _, item := range before {
		b[item] = true
	}
	a := map[string]bool{}
	added, removed = []string{}, []string{}
	for _, item := range after {
		a[item] = true
		if !b[item] {
			added = append(added, item)
		}
	}
	for _, item := range before {
		if !a[item] {
			removed = append(removed, item)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
