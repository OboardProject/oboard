package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	defaultSurgeMacMihomoExec = "/usr/local/bin/mihomo"
	defaultSurgeMacMergeName  = "Mihomo-Core"
	defaultSurgeMacPortBase   = 52000
	surgeMacPortHashMin       = 40000
	surgeMacPortHashSpan      = 15000
	maxSurgeMacMihomoExecLen  = 512
	maxSurgeMacMergeNameLen   = 80
)

// SurgeMacMihomoMode selects how Surge Mac hands unsupported (or all) nodes
// to a local Mihomo process.
type SurgeMacMihomoMode string

const (
	SurgeMacMihomoAuto   SurgeMacMihomoMode = "auto"
	SurgeMacMihomoAlways SurgeMacMihomoMode = "always"
	SurgeMacMihomoOff    SurgeMacMihomoMode = "off"
)

// SurgeMacOptions controls the Surge Mac native/Mihomo adapter.
type SurgeMacOptions struct {
	Mode         SurgeMacMihomoMode
	Merge        bool
	Exec         string
	LocalPort    int
	MergeName    string
	PortScopeKey string
}

// SubscriptionRenderOptions carries target-specific render knobs that do not
// change the authorized node set.
type SubscriptionRenderOptions struct {
	SurgeMac       SurgeMacOptions
	Template       string
	TemplateDigest string
	UserAgent      string
	RequestedFormat model.SubscriptionFormat
}

func defaultSurgeMacOptions() SurgeMacOptions {
	return SurgeMacOptions{
		Mode:      SurgeMacMihomoAuto,
		Merge:     true,
		Exec:      defaultSurgeMacMihomoExec,
		MergeName: defaultSurgeMacMergeName,
	}
}

func withNormalizedSurgeMacOptions(opts SurgeMacOptions) SurgeMacOptions {
	if opts == (SurgeMacOptions{}) {
		return defaultSurgeMacOptions()
	}
	if opts.Mode == "" {
		opts.Mode = SurgeMacMihomoAuto
	}
	if strings.TrimSpace(opts.Exec) == "" {
		opts.Exec = defaultSurgeMacMihomoExec
	}
	if strings.TrimSpace(opts.MergeName) == "" {
		opts.MergeName = defaultSurgeMacMergeName
	}
	return opts
}

// ParseSubscriptionRenderOptions reads Surge Mac Mihomo knobs from a
// subscription URL. Other formats ignore these keys so extra query parameters
// stay harmless.
func ParseSubscriptionRenderOptions(format model.SubscriptionFormat, query url.Values, portScopeKey string) (SubscriptionRenderOptions, error) {
	var out SubscriptionRenderOptions
	if normalizeSubscriptionFormat(format) != model.SubscriptionFormatSurgeMac {
		return out, nil
	}
	opts := SurgeMacOptions{
		Mode:         SurgeMacMihomoAuto,
		Merge:        true,
		Exec:         defaultSurgeMacMihomoExec,
		MergeName:    defaultSurgeMacMergeName,
		PortScopeKey: strings.TrimSpace(portScopeKey),
	}
	if raw := strings.TrimSpace(query.Get("mihomo")); raw != "" {
		switch strings.ToLower(raw) {
		case string(SurgeMacMihomoAuto), string(SurgeMacMihomoAlways), string(SurgeMacMihomoOff):
			opts.Mode = SurgeMacMihomoMode(strings.ToLower(raw))
		default:
			return SubscriptionRenderOptions{}, fmt.Errorf("unsupported mihomo mode %q", raw)
		}
	}
	if raw := strings.TrimSpace(query.Get("mihomoMerge")); raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			opts.Merge = true
		case "0", "false", "no", "off":
			opts.Merge = false
		default:
			return SubscriptionRenderOptions{}, fmt.Errorf("invalid mihomoMerge %q", raw)
		}
	}
	if raw := strings.TrimSpace(query.Get("mihomoExec")); raw != "" {
		if err := validateSurgeMacMihomoExec(raw); err != nil {
			return SubscriptionRenderOptions{}, err
		}
		opts.Exec = raw
	}
	if raw := strings.TrimSpace(query.Get("mihomoLocalPort")); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil || port < 1 || port > 65535 {
			return SubscriptionRenderOptions{}, fmt.Errorf("invalid mihomoLocalPort %q", raw)
		}
		opts.LocalPort = port
	}
	if raw := strings.TrimSpace(query.Get("mihomoMergeName")); raw != "" {
		name := sanitizeConfName(raw)
		if name == "" || len(name) > maxSurgeMacMergeNameLen {
			return SubscriptionRenderOptions{}, fmt.Errorf("invalid mihomoMergeName %q", raw)
		}
		opts.MergeName = name
	}
	out.SurgeMac = opts
	return out, nil
}

func validateSurgeMacMihomoExec(value string) error {
	if len(value) > maxSurgeMacMihomoExecLen || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("invalid mihomoExec %q", value)
	}
	if !strings.HasPrefix(value, "/") {
		return fmt.Errorf("mihomoExec must be an absolute path")
	}
	return nil
}

func renderSubscriptionTargetWithOptions(nodes []SubscriptionNode, format model.SubscriptionFormat, opts SubscriptionRenderOptions) (string, error) {
	return renderSubscriptionDocument(nodes, format, opts)
}

func subscriptionTargetSupportsWithOptions(format model.SubscriptionFormat, proxy subscriptionProxy, opts SubscriptionRenderOptions) bool {
	if proxy.Type == "snell" {
		caps := ResolveTargetCapabilities(format, opts.UserAgent)
		// For SurgeMac, also check Mihomo bridge gating
		if normalizeSubscriptionFormat(format) == model.SubscriptionFormatSurgeMac {
			// SurgeMac native vs Mihomo bridge already handles version checks,
			// but still need to gate snell_multi_user_userkey for Mihomo path.
			// Use the shared surgeMacRouteWithOpts path instead.
			native, viaMihomo := surgeMacRouteWithOpts(proxy, opts)
			return native || viaMihomo
		}
		for _, feature := range RequiredFeaturesForProxy(proxy) {
			if !IsFeatureSupported(caps, feature) {
				return false
			}
		}
	}
	if normalizeSubscriptionFormat(format) == model.SubscriptionFormatSurgeMac {
		return surgeMacSupports(proxy, opts.SurgeMac)
	}
	return subscriptionTargetSupports(format, proxy)
}

func surgeMacSupports(proxy subscriptionProxy, opts SurgeMacOptions) bool {
	native, viaMihomo := surgeMacRoute(proxy, withNormalizedSurgeMacOptions(opts))
	return native || viaMihomo
}

func surgeNativeSupports(proxy subscriptionProxy) bool {
	if proxy.Type == "snell" {
		return snellFormatSupports(model.SubscriptionFormatSurge, proxy)
	}
	return surgeStyleSupports(model.SubscriptionFormatSurge, proxy)
}

func surgeMacRoute(proxy subscriptionProxy, opts SurgeMacOptions) (native bool, viaMihomo bool) {
	opts = withNormalizedSurgeMacOptions(opts)
	nativeOK := surgeNativeSupports(proxy)
	mihomoOK := subscriptionTargetSupports(model.SubscriptionFormatMihomo, proxy)
	switch opts.Mode {
	case SurgeMacMihomoOff:
		return nativeOK, false
	case SurgeMacMihomoAlways:
		if mihomoOK {
			return false, true
		}
		return nativeOK, false
	default:
		if nativeOK {
			return true, false
		}
		return false, mihomoOK
	}
}

func renderSurgeMacTarget(proxies []subscriptionProxy, opts SurgeMacOptions) (string, error) {
	opts = withNormalizedSurgeMacOptions(opts)
	if err := validateSurgeMacMihomoExec(opts.Exec); err != nil {
		return "", err
	}
	ports := newSurgeMacPortAllocator(opts)
	lines := make([]string, 0, len(proxies)+1)
	usedNames := map[string]bool{}
	merged := surgeMacMergedState{}
	var corePort int
	if opts.Merge {
		var err error
		corePort, err = ports.Next()
		if err != nil {
			return "", err
		}
	}
	for _, proxy := range proxies {
		native, viaMihomo := surgeMacRoute(proxy, opts)
		switch {
		case native:
			line, err := renderSurgeLine(proxy, model.SubscriptionFormatSurgeMac)
			if err != nil {
				return "", err
			}
			if line != "" {
				lines = append(lines, line)
				usedNames[sanitizeConfName(proxy.Name)] = true
			}
		case viaMihomo:
			clashProxy, err := mihomoStyleProxyMap(proxy, model.SubscriptionFormatMihomo)
			if err != nil {
				return "", err
			}
			port, err := ports.Next()
			if err != nil {
				return "", err
			}
			name := sanitizeConfName(proxy.Name)
			if opts.Merge {
				proxyName := fmt.Sprintf("p-%d", port)
				merged.Proxies = append(merged.Proxies, renameSubscriptionProxyMap(clashProxy, proxyName))
				merged.Listeners = append(merged.Listeners, surgeMacMihomoListener{
					Name:   fmt.Sprintf("socks-%d", port),
					Type:   "socks",
					Listen: "127.0.0.1",
					Port:   port,
					UDP:    true,
					Proxy:  proxyName,
				})
				merged.Addresses = appendUniqueIPs(merged.Addresses, surgeMacAddresses(proxy)...)
				lines = append(lines, renderSurgeMacLocalSOCKS(name, port))
				usedNames[name] = true
				continue
			}
			config := surgeMacPerNodeConfig(clashProxy, port)
			encoded, err := encodeSurgeMacMihomoConfig(config)
			if err != nil {
				return "", err
			}
			lines = append(lines, renderSurgeMacExternal(name, opts.Exec, port, encoded, surgeMacAddresses(proxy)))
			usedNames[name] = true
		}
	}
	if opts.Merge && len(merged.Proxies) > 0 {
		config := surgeMacMergedConfig(merged, corePort)
		encoded, err := encodeSurgeMacMihomoConfig(config)
		if err != nil {
			return "", err
		}
		coreName := uniqueSurgeMacName(opts.MergeName, usedNames)
		lines = append(lines, renderSurgeMacExternal(coreName, opts.Exec, corePort, encoded, merged.Addresses))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

type surgeMacMergedState struct {
	Proxies   []map[string]any
	Listeners []surgeMacMihomoListener
	Addresses []string
}

type surgeMacMihomoListener struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Listen string `json:"listen"`
	Port   int    `json:"port"`
	UDP    bool   `json:"udp"`
	Proxy  string `json:"proxy"`
}

type surgeMacMihomoGroup struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Proxies []string `json:"proxies"`
}

func surgeMacPerNodeConfig(clashProxy map[string]any, port int) map[string]any {
	return map[string]any{
		"mixed-port": port,
		"ipv6":       true,
		"mode":       "global",
		"proxies":    []any{renameSubscriptionProxyMap(clashProxy, "proxy")},
		"proxy-groups": []any{
			map[string]any{
				"name":    "GLOBAL",
				"type":    "select",
				"proxies": []any{"proxy"},
			},
		},
	}
}

func surgeMacMergedConfig(state surgeMacMergedState, corePort int) map[string]any {
	proxies := make([]any, 0, len(state.Proxies))
	for _, proxy := range state.Proxies {
		proxies = append(proxies, proxy)
	}
	listeners := make([]any, 0, len(state.Listeners))
	for _, listener := range state.Listeners {
		listeners = append(listeners, map[string]any{
			"name":   listener.Name,
			"type":   listener.Type,
			"listen": listener.Listen,
			"port":   listener.Port,
			"udp":    listener.UDP,
			"proxy":  listener.Proxy,
		})
	}
	return map[string]any{
		"mixed-port": corePort,
		"ipv6":       true,
		"mode":       "global",
		"proxies":    proxies,
		"listeners":  listeners,
	}
}

func renameSubscriptionProxyMap(proxy map[string]any, name string) map[string]any {
	out := make(map[string]any, len(proxy)+1)
	for key, value := range proxy {
		out[key] = value
	}
	out["name"] = name
	return out
}

func renderSurgeMacLocalSOCKS(name string, port int) string {
	return strings.Join([]string{
		fmt.Sprintf("%s=socks5,127.0.0.1,%d", name, port),
		"udp-relay=true",
	}, ",")
}

func renderSurgeMacExternal(name, exec string, port int, encoded string, addresses []string) string {
	parts := []string{
		fmt.Sprintf("%s=external", name),
		"exec=" + quoteConf(exec),
		"local-port=" + strconv.Itoa(port),
		`args="-config"`,
		"args=" + quoteConf(encoded),
	}
	if len(addresses) > 0 {
		parts = append(parts, "addresses="+strings.Join(addresses, ","))
	}
	parts = append(parts, "udp-relay=true")
	return strings.Join(parts, ",")
}

func surgeMacAddresses(proxy subscriptionProxy) []string {
	host := strings.Trim(strings.TrimSpace(proxy.Server), "[]")
	if net.ParseIP(host) == nil {
		return nil
	}
	return []string{host}
}

func appendUniqueIPs(values []string, extras ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values)+len(extras))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	for _, value := range extras {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func uniqueSurgeMacName(name string, used map[string]bool) string {
	name = sanitizeConfName(name)
	if name == "" {
		name = defaultSurgeMacMergeName
	}
	if !used[name] {
		used[name] = true
		return name
	}
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s-%d", name, index)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

type surgeMacPortAllocator struct {
	next int
}

func newSurgeMacPortAllocator(opts SurgeMacOptions) *surgeMacPortAllocator {
	return &surgeMacPortAllocator{next: surgeMacResolvedPortBase(opts)}
}

func (a *surgeMacPortAllocator) Next() (int, error) {
	if a.next < 1 || a.next > 65535 {
		return 0, fmt.Errorf("Surge Mac Mihomo local port %d is out of range", a.next)
	}
	port := a.next
	a.next++
	return port, nil
}

func surgeMacResolvedPortBase(opts SurgeMacOptions) int {
	if opts.LocalPort > 0 {
		return opts.LocalPort
	}
	return surgeMacDerivedPortBase(opts.PortScopeKey)
}

func surgeMacDerivedPortBase(scopeKey string) int {
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return defaultSurgeMacPortBase
	}
	sum := sha256.Sum256([]byte("oboard-surgemac-port\x00" + scopeKey))
	return surgeMacPortHashMin + int(binary.BigEndian.Uint32(sum[:4])%uint32(surgeMacPortHashSpan))
}

func encodeSurgeMacMihomoConfig(config map[string]any) (string, error) {
	data, err := marshalStableJSON(config)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func decodeSurgeMacMihomoConfig(encoded string) (map[string]any, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func marshalStableJSON(value any) ([]byte, error) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var builder strings.Builder
		builder.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				builder.WriteByte(',')
			}
			keyJSON, err := json.Marshal(key)
			if err != nil {
				return nil, err
			}
			valueJSON, err := marshalStableJSON(typed[key])
			if err != nil {
				return nil, err
			}
			builder.Write(keyJSON)
			builder.WriteByte(':')
			builder.Write(valueJSON)
		}
		builder.WriteByte('}')
		return []byte(builder.String()), nil
	case []any:
		var builder strings.Builder
		builder.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				builder.WriteByte(',')
			}
			itemJSON, err := marshalStableJSON(item)
			if err != nil {
				return nil, err
			}
			builder.Write(itemJSON)
		}
		builder.WriteByte(']')
		return []byte(builder.String()), nil
	case []map[string]any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = item
		}
		return marshalStableJSON(items)
	case []surgeMacMihomoListener:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = map[string]any{
				"name":   item.Name,
				"type":   item.Type,
				"listen": item.Listen,
				"port":   item.Port,
				"udp":    item.UDP,
				"proxy":  item.Proxy,
			}
		}
		return marshalStableJSON(items)
	case []surgeMacMihomoGroup:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = map[string]any{
				"name":    item.Name,
				"type":    item.Type,
				"proxies": stringSliceAsAny(item.Proxies),
			}
		}
		return marshalStableJSON(items)
	default:
		return json.Marshal(typed)
	}
}

func stringSliceAsAny(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}
