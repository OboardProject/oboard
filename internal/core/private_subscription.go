package core

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"go.yaml.in/yaml/v3"
)

const MaxPrivateSourceNodes = 2000

type ParsedPrivateNode struct {
	Protocol    model.PrivateSubscriptionProtocol `json:"protocol"`
	Name        string                            `json:"name"`
	Fingerprint string                            `json:"fingerprint"`
	Raw         map[string]any                    `json:"raw"`
}

type PrivateImportIssue struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
}

type PrivateImportResult struct {
	Nodes  []ParsedPrivateNode  `json:"nodes"`
	Issues []PrivateImportIssue `json:"issues"`
}

func ParsePrivateSubscription(content string) (PrivateImportResult, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return PrivateImportResult{}, errors.New("subscription content is empty")
	}
	if decoded, ok := decodePrivateBase64List(content); ok {
		content = decoded
	}
	var records []any
	if strings.HasPrefix(content, "{") || strings.HasPrefix(content, "[") {
		var value any
		if err := json.Unmarshal([]byte(content), &value); err != nil {
			return PrivateImportResult{}, err
		}
		records = privateRecordsFromValue(value)
	} else if parsed := parsePrivateYAML(content); len(parsed) > 0 {
		records = parsed
	} else {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "//") {
				records = append(records, line)
			}
		}
	}
	if len(records) > MaxPrivateSourceNodes {
		return PrivateImportResult{}, fmt.Errorf("subscription contains more than %d nodes", MaxPrivateSourceNodes)
	}
	result := PrivateImportResult{Nodes: []ParsedPrivateNode{}, Issues: []PrivateImportIssue{}}
	for i, record := range records {
		node, err := parsePrivateRecord(record, i+1)
		if err != nil {
			result.Issues = append(result.Issues, PrivateImportIssue{Index: i + 1, Message: err.Error()})
			continue
		}
		result.Nodes = append(result.Nodes, node)
	}
	if len(result.Nodes) == 0 {
		return result, errors.New("subscription contains no supported nodes")
	}
	return result, nil
}

func decodePrivateBase64List(value string) (string, bool) {
	if strings.Contains(value, "://") || strings.ContainsAny(value, "{}[]:") {
		return "", false
	}
	compact := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, value)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if raw, err := enc.DecodeString(compact); err == nil && strings.Contains(string(raw), "://") {
			return string(raw), true
		}
	}
	return "", false
}

func parsePrivateYAML(content string) []any {
	var value any
	if yaml.Unmarshal([]byte(content), &value) != nil {
		return nil
	}
	normalized := normalizeYAMLValue(value)
	root, ok := normalized.(map[string]any)
	if !ok {
		return nil
	}
	items, ok := root["proxies"].([]any)
	if !ok {
		return nil
	}
	return items
}
func normalizeYAMLValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range v {
			out[k] = normalizeYAMLValue(x)
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, x := range v {
			out[fmt.Sprint(k)] = normalizeYAMLValue(x)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i := range v {
			out[i] = normalizeYAMLValue(v[i])
		}
		return out
	default:
		return v
	}
}
func privateRecordsFromValue(value any) []any {
	switch v := value.(type) {
	case []any:
		return v
	case map[string]any:
		for _, key := range []string{"outbounds", "proxies"} {
			if items, ok := v[key].([]any); ok {
				return items
			}
		}
		return []any{v}
	default:
		return nil
	}
}

func parsePrivateRecord(value any, index int) (ParsedPrivateNode, error) {
	switch v := value.(type) {
	case string:
		return parsePrivateURI(v, index)
	case map[string]any:
		return privateNodeFromMap(v, index)
	default:
		return ParsedPrivateNode{}, errors.New("unsupported node record")
	}
}

func privateNodeFromMap(input map[string]any, index int) (ParsedPrivateNode, error) {
	raw := cloneSubscriptionValue(input).(map[string]any)
	typ := strings.ToLower(strings.TrimSpace(stringFromAny(raw["type"])))
	if typ == "ss" {
		typ = "shadowsocks"
	}
	if typ == "socks" {
		typ = "socks5"
	}
	if typ == "hy2" {
		typ = "hysteria2"
	}
	protocol, err := privateProtocol(typ)
	if err != nil {
		return ParsedPrivateNode{}, err
	}
	name := strings.TrimSpace(stringFromAny(raw["tag"]))
	if name == "" {
		name = strings.TrimSpace(stringFromAny(raw["name"]))
	}
	if name == "" {
		name = fmt.Sprintf("%s-%d", typ, index)
	}
	server := firstPrivateString(raw, "server", "address")
	port := firstPrivateInt(raw, "server_port", "port")
	if server == "" || port <= 0 || port > 65535 {
		return ParsedPrivateNode{}, errors.New("node requires a valid server and port")
	}
	raw["type"], raw["tag"], raw["server"], raw["server_port"] = typ, name, server, port
	if raw["uuid"] == nil {
		raw["uuid"] = raw["id"]
	}
	if raw["password"] == nil {
		raw["password"] = raw["auth"]
	}
	if raw["method"] == nil {
		raw["method"] = raw["cipher"]
	}
	normalizePrivateClashFields(raw)
	return finalizePrivateNode(protocol, name, raw)
}

func normalizePrivateClashFields(raw map[string]any) {
	if raw["packet_encoding"] == nil {
		raw["packet_encoding"] = raw["packet-encoding"]
	}
	if raw["tls"] == true {
		raw["tls"] = map[string]any{"enabled": true, "server_name": firstPrivateString(raw, "servername", "sni"), "insecure": boolFromAny(raw["skip-cert-verify"])}
	} else if raw["tls"] == nil && (firstPrivateString(raw, "servername", "sni") != "") {
		raw["tls"] = map[string]any{"enabled": true, "server_name": firstPrivateString(raw, "servername", "sni")}
	}
	if network := firstPrivateString(raw, "network"); network != "" && raw["transport"] == nil {
		transport := map[string]any{"type": network}
		if opts, ok := raw[network+"-opts"].(map[string]any); ok {
			for k, v := range opts {
				transport[strings.ReplaceAll(k, "-", "_")] = v
			}
		}
		raw["transport"] = transport
	}
}

func parsePrivateURI(line string, index int) (ParsedPrivateNode, error) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(strings.ToLower(line), "vmess://") {
		return parseVMessURI(line, index)
	}
	u, err := url.Parse(line)
	if err != nil || u.Scheme == "" {
		return ParsedPrivateNode{}, errors.New("invalid node URI")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme == "hy2" {
		scheme = "hysteria2"
	}
	if scheme == "ss" {
		return parsePrivateSSURI(line, index)
	}
	protocol, err := privateProtocol(scheme)
	if err != nil {
		return ParsedPrivateNode{}, err
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if host == "" || port <= 0 {
		return ParsedPrivateNode{}, errors.New("node URI requires host and port")
	}
	name, _ := url.PathUnescape(u.Fragment)
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s-%d", scheme, index)
	}
	raw := map[string]any{"type": scheme, "tag": name, "server": host, "server_port": port}
	user := ""
	password := ""
	if u.User != nil {
		user = u.User.Username()
		password, _ = u.User.Password()
	}
	q := u.Query()
	switch scheme {
	case "vless", "vmess":
		raw["uuid"] = user
	case "trojan", "hysteria2", "anytls":
		if password != "" {
			raw["password"] = password
		} else {
			raw["password"] = user
		}
	case "tuic":
		raw["uuid"], raw["password"] = user, password
	case "socks5":
		raw["username"], raw["password"] = user, password
	case "mierus":
		raw["type"] = "mieru"
		raw["username"], raw["password"] = user, password
		protocol = model.PrivateProtocolMieru
	}
	applyPrivateURIOptions(raw, q)
	return finalizePrivateNode(protocol, name, raw)
}

func parseVMessURI(line string, index int) (ParsedPrivateNode, error) {
	payload := strings.TrimPrefix(line, "vmess://")
	var decoded []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err = enc.DecodeString(payload)
		if err == nil {
			break
		}
	}
	if err != nil {
		return ParsedPrivateNode{}, errors.New("invalid vmess payload")
	}
	var v map[string]any
	if json.Unmarshal(decoded, &v) != nil {
		return ParsedPrivateNode{}, errors.New("invalid vmess JSON")
	}
	raw := map[string]any{"type": "vmess", "tag": firstPrivateString(v, "ps"), "server": firstPrivateString(v, "add"), "server_port": firstPrivateInt(v, "port"), "uuid": firstPrivateString(v, "id"), "alter_id": firstPrivateInt(v, "aid"), "security": firstPrivateString(v, "scy")}
	if raw["tag"] == "" {
		raw["tag"] = fmt.Sprintf("vmess-%d", index)
	}
	network := firstPrivateString(v, "net")
	if network != "" {
		raw["transport"] = map[string]any{"type": network, "path": firstPrivateString(v, "path"), "host": firstPrivateString(v, "host")}
	}
	tlsMode := firstPrivateString(v, "tls")
	if tlsMode != "" && tlsMode != "none" {
		raw["tls"] = map[string]any{"enabled": true, "server_name": firstPrivateString(v, "sni")}
	}
	return privateNodeFromMap(raw, index)
}

func parsePrivateSSURI(line string, index int) (ParsedPrivateNode, error) {
	u, err := url.Parse(line)
	if err != nil {
		return ParsedPrivateNode{}, err
	}
	name, _ := url.PathUnescape(u.Fragment)
	if name == "" {
		name = fmt.Sprintf("ss-%d", index)
	}
	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	userinfo := ""
	if u.User != nil {
		userinfo = u.User.Username()
		if p, ok := u.User.Password(); ok {
			userinfo += ":" + p
		}
	}
	if host == "" {
		payload := strings.TrimPrefix(strings.SplitN(line, "#", 2)[0], "ss://")
		for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
			if decoded, e := enc.DecodeString(payload); e == nil {
				u, err = url.Parse("ss://" + string(decoded))
				if err == nil {
					host = u.Hostname()
					port, _ = strconv.Atoi(u.Port())
					if u.User != nil {
						userinfo = u.User.Username()
						if p, ok := u.User.Password(); ok {
							userinfo += ":" + p
						}
					}
				}
				break
			}
		}
	}
	if !strings.Contains(userinfo, ":") {
		for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
			if decoded, e := enc.DecodeString(userinfo); e == nil {
				userinfo = string(decoded)
				break
			}
		}
	}
	method, password, ok := strings.Cut(userinfo, ":")
	if !ok || host == "" || port <= 0 {
		return ParsedPrivateNode{}, errors.New("invalid shadowsocks URI")
	}
	return finalizePrivateNode(model.PrivateProtocolShadowsocks, name, map[string]any{"type": "shadowsocks", "tag": name, "server": host, "server_port": port, "method": method, "password": password})
}

func applyPrivateURIOptions(raw map[string]any, q url.Values) {
	network := firstPrivateNonEmpty(q.Get("type"), q.Get("network"))
	if network != "" {
		raw["transport"] = map[string]any{"type": network, "path": q.Get("path"), "host": q.Get("host"), "service_name": firstPrivateNonEmpty(q.Get("serviceName"), q.Get("service_name"))}
	}
	security := q.Get("security")
	if security == "tls" || security == "reality" || q.Get("sni") != "" {
		tls := map[string]any{"enabled": true, "server_name": q.Get("sni"), "insecure": q.Get("insecure") == "1" || q.Get("allowInsecure") == "1"}
		if security == "reality" {
			tls["reality"] = map[string]any{"public_key": q.Get("pbk"), "short_id": q.Get("sid")}
			tls["fingerprint"] = q.Get("fp")
		}
		raw["tls"] = tls
	}
	for _, pair := range [][2]string{{"flow", "flow"}, {"packetEncoding", "packet_encoding"}, {"obfs", "obfs"}, {"obfs-password", "obfs_password"}, {"congestion_control", "congestion_control"}} {
		if v := q.Get(pair[0]); v != "" {
			raw[pair[1]] = v
		}
	}
}

func privateProtocol(value string) (model.PrivateSubscriptionProtocol, error) {
	switch value {
	case "vless":
		return model.PrivateProtocolVLESS, nil
	case "vmess":
		return model.PrivateProtocolVMess, nil
	case "trojan":
		return model.PrivateProtocolTrojan, nil
	case "tuic":
		return model.PrivateProtocolTUIC, nil
	case "hysteria2":
		return model.PrivateProtocolHysteria2, nil
	case "anytls":
		return model.PrivateProtocolAnyTLS, nil
	case "shadowsocks", "ss":
		return model.PrivateProtocolShadowsocks, nil
	case "socks", "socks5":
		return model.PrivateProtocolSOCKS5, nil
	case "mieru", "mierus":
		return model.PrivateProtocolMieru, nil
	default:
		return "", fmt.Errorf("unsupported protocol %q", value)
	}
}

func finalizePrivateNode(protocol model.PrivateSubscriptionProtocol, name string, raw map[string]any) (ParsedPrivateNode, error) {
	raw["type"] = string(protocol)
	encoded, err := json.Marshal(canonicalPrivateFingerprintValue(raw))
	if err != nil {
		return ParsedPrivateNode{}, err
	}
	sum := sha256.Sum256(encoded)
	return ParsedPrivateNode{Protocol: protocol, Name: name, Fingerprint: hex.EncodeToString(sum[:]), Raw: raw}, nil
}
func canonicalPrivateFingerprintValue(raw map[string]any) map[string]any {
	out := cloneSubscriptionValue(raw).(map[string]any)
	delete(out, "tag")
	delete(out, "name")
	return out
}
func firstPrivateString(v map[string]any, keys ...string) string {
	for _, k := range keys {
		if x := strings.TrimSpace(stringFromAny(v[k])); x != "" {
			return x
		}
	}
	return ""
}
func firstPrivateInt(v map[string]any, keys ...string) int {
	for _, k := range keys {
		if x := intFromAny(v[k]); x > 0 {
			return x
		}
	}
	return 0
}

func firstPrivateNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
func boolFromAny(v any) bool { x, _ := v.(bool); return x }

func SubscriptionNodeFromPrivate(node model.ImportedNode, raw map[string]any, groupName string) SubscriptionNode {
	name := strings.TrimSpace(node.Name)
	if name == "" {
		name = string(node.Protocol)
	}
	raw = cloneSubscriptionValue(raw).(map[string]any)
	raw["tag"] = name
	return SubscriptionNode{Key: fmt.Sprintf("private:%d", node.ID), Name: name, Group: groupName, SourceName: name, Raw: raw}
}

func RenderSubscriptionNodes(nodes []SubscriptionNode, format model.SubscriptionFormat) (string, error) {
	return RenderSubscriptionNodesWithOptions(nodes, format, SubscriptionRenderOptions{})
}

func RenderSubscriptionNodesWithOptions(nodes []SubscriptionNode, format model.SubscriptionFormat, opts SubscriptionRenderOptions) (string, error) {
	return renderSubscriptionTargetWithOptions(nodes, format, opts)
}

type SubscriptionPreview struct {
	Content        string             `json:"content"`
	Nodes          []SubscriptionNode `json:"nodes"`
	FilteredCount  int                `json:"filtered_count"`
	InvalidReasons []string           `json:"invalid_reasons"`
}

func PreviewSubscriptionNodes(nodes []SubscriptionNode, format model.SubscriptionFormat) (SubscriptionPreview, error) {
	return PreviewSubscriptionNodesWithOptions(nodes, format, SubscriptionRenderOptions{})
}

func PreviewSubscriptionNodesWithOptions(nodes []SubscriptionNode, format model.SubscriptionFormat, opts SubscriptionRenderOptions) (SubscriptionPreview, error) {
	format = normalizeSubscriptionFormat(format)
	preview := SubscriptionPreview{Nodes: []SubscriptionNode{}, InvalidReasons: []string{}}
	for _, node := range nodes {
		proxy, err := normalizeSubscriptionNode(node)
		if err != nil {
			preview.InvalidReasons = append(preview.InvalidReasons, err.Error())
			continue
		}
		if subscriptionTargetSupportsWithOptions(format, proxy, opts) {
			preview.Nodes = append(preview.Nodes, node)
		} else {
			preview.FilteredCount++
		}
	}
	content, err := renderSubscriptionTargetWithOptions(preview.Nodes, format, opts)
	if err != nil {
		return SubscriptionPreview{}, err
	}
	preview.Content = content
	return preview, nil
}
func CanonicalShareURIForNode(node SubscriptionNode) (string, error) {
	proxy, err := normalizeSubscriptionNode(node)
	if err != nil {
		return "", err
	}
	return canonicalShareURI(proxy)
}

func SubscriptionNodeFingerprint(node SubscriptionNode) (string, error) {
	proxy, err := normalizeSubscriptionNode(node)
	if err != nil {
		return "", err
	}
	native := cloneSubscriptionValue(proxy.Native).(map[string]any)
	delete(native, "tag")
	encoded, err := json.Marshal(native)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
