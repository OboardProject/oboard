package controller

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

var inboundKindProtocols = map[string]model.Protocol{
	"vless-reality":        model.ProtocolVLESS,
	"vless-ws":             model.ProtocolVLESS,
	"vless-tcp":            model.ProtocolVLESS,
	"hy2-tls":              model.ProtocolHY2,
	"hy2-salamander":       model.ProtocolHY2,
	"anytls-basic":         model.ProtocolAnyTLS,
	"anytls-large-padding": model.ProtocolAnyTLS,
	"ss-aes-128-gcm":       model.ProtocolSS,
	"ss-aes-256-gcm":       model.ProtocolSS,
	"ss-2022-128":          model.ProtocolSS,
	"ss-2022-256":          model.ProtocolSS,
	"mieru-basic":          model.ProtocolMieru,
	"snell-v4":             model.ProtocolSnell,
	"snell-v6":             model.ProtocolSnell,
	"socks5-auth":          model.ProtocolSocks,
	"ssh-restricted":       model.ProtocolSSH,
}

func applyInboundKindDefaults(v *model.Inbound, current *model.Inbound) error {
	if v == nil {
		return errors.New("inbound required")
	}
	v.Kind = strings.ToLower(strings.TrimSpace(v.Kind))
	if v.Kind == "" {
		if v.Reality != nil || v.RotateRealityKey {
			return &core.ConfigFieldError{Path: "kind", Problem: "must be vless-reality when reality settings are provided"}
		}
		return nil
	}
	protocol, ok := inboundKindProtocols[v.Kind]
	if !ok {
		return &core.ConfigFieldError{Path: "kind", Problem: fmt.Sprintf("unsupported inbound kind %q", v.Kind)}
	}
	if v.Protocol == "" {
		v.Protocol = protocol
	} else if v.Protocol != protocol {
		return &core.ConfigFieldError{Path: "protocol", Problem: fmt.Sprintf("must be %s for kind %s", protocol, v.Kind)}
	}
	if v.Kind != "vless-reality" {
		if v.Reality != nil {
			return &core.ConfigFieldError{Path: "reality", Problem: "is only valid for kind vless-reality"}
		}
		if v.RotateRealityKey {
			return &core.ConfigFieldError{Path: "rotate_reality_key", Problem: "is only valid for kind vless-reality"}
		}
		return applyNonRealityInboundKindDefaults(v)
	}
	return applyControlledRealityDefaults(v, current)
}

func applyNonRealityInboundKindDefaults(v *model.Inbound) error {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(v.ConfigJSON)), &cfg); err != nil || cfg == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return &core.ConfigFieldError{Path: "config_json", Problem: err.Error()}
	}
	switch v.Kind {
	case "vless-ws":
		tls := ensureObject(cfg, "tls")
		tls["enabled"] = true
		delete(tls, "reality")
		transport := ensureObject(cfg, "transport")
		transport["type"] = "ws"
		if strings.TrimSpace(stringFromMap(transport, "path")) == "" {
			transport["path"] = "/vless"
		}
		v.TLS = true
	case "vless-tcp":
		delete(cfg, "flow")
		delete(cfg, "tls")
		delete(cfg, "transport")
		v.TLS = false
		v.CertificateMode = model.CertificateModeExternal
		v.CertificateID = nil
		v.CertificateDomain = ""
	case "hy2-tls":
		ensureObject(cfg, "tls")["enabled"] = true
		applyHY2BandwidthDefaults(cfg)
		delete(cfg, "obfs")
		v.TLS = true
	case "hy2-salamander":
		ensureObject(cfg, "tls")["enabled"] = true
		applyHY2BandwidthDefaults(cfg)
		if err := applyHY2SalamanderObfs(cfg); err != nil {
			return err
		}
		v.TLS = true
	case "anytls-basic":
		ensureObject(cfg, "tls")["enabled"] = true
		v.TLS = true
	case "anytls-large-padding":
		ensureObject(cfg, "tls")["enabled"] = true
		v.TLS = true
	case "ss-aes-128-gcm":
		cfg["method"] = "aes-128-gcm"
	case "ss-aes-256-gcm":
		cfg["method"] = "aes-256-gcm"
	case "ss-2022-128":
		cfg["method"] = "2022-blake3-aes-128-gcm"
	case "ss-2022-256":
		cfg["method"] = "2022-blake3-aes-256-gcm"
	case "mieru-basic":
		if cfg["transport"] == nil {
			cfg["transport"] = "TCP"
		}
		if cfg["multiplexing"] == nil {
			cfg["multiplexing"] = "MULTIPLEXING_DEFAULT"
		}
		if cfg["user_hint_is_mandatory"] == nil {
			cfg["user_hint_is_mandatory"] = true
		}
	case "snell-v4":
		cfg["version"] = 4
	case "snell-v6":
		cfg["version"] = 6
	case "ssh-restricted":
		if cfg["access_mode"] == nil {
			cfg["access_mode"] = "restricted_proxy"
		}
	}
	v.ConfigJSON = encodeInboundJSON(cfg)
	return nil
}

func ensureObject(parent map[string]any, key string) map[string]any {
	object := objectMap(parent[key])
	if object == nil {
		object = map[string]any{}
		parent[key] = object
	}
	return object
}

func applyControlledRealityDefaults(v *model.Inbound, current *model.Inbound) error {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(v.ConfigJSON)), &cfg); err != nil || cfg == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return &core.ConfigFieldError{Path: "config_json", Problem: err.Error()}
	}
	tls := objectMap(cfg["tls"])
	if tls == nil {
		tls = map[string]any{}
		cfg["tls"] = tls
	}
	reality := objectMap(tls["reality"])
	if reality == nil {
		reality = map[string]any{}
		tls["reality"] = reality
	}
	currentReality := inboundRealityConfig(current)
	for _, field := range []string{"private_key", "public_key"} {
		provided := strings.TrimSpace(stringFromMap(reality, field))
		stored := strings.TrimSpace(stringFromMap(currentReality, field))
		if provided != "" && (current == nil || provided != stored) {
			return &core.ConfigFieldError{Path: "config_json.tls.reality." + field, Problem: "is managed by the Controller and must not be supplied"}
		}
	}

	handshake := objectMap(reality["handshake"])
	if handshake == nil {
		handshake = map[string]any{}
		reality["handshake"] = handshake
	}
	serverName := ""
	port := 0
	shortID := ""
	if v.Reality != nil {
		serverName = strings.TrimSpace(v.Reality.HandshakeServer)
		port = v.Reality.HandshakePort
		shortID = strings.TrimSpace(v.Reality.ShortID)
	}
	if serverName == "" {
		serverName = strings.TrimSpace(stringFromMap(handshake, "server"))
	}
	if serverName == "" {
		serverName = strings.TrimSpace(stringFromMap(tls, "server_name"))
	}
	if serverName == "" {
		serverName = defaultVLESSRealityServerName
	}
	if port == 0 {
		port = handshakePortFromMap(handshake)
	}
	if port == 0 {
		port = 443
	}
	if port < 1 || port > 65535 {
		return &core.ConfigFieldError{Path: "reality.handshake_port", Problem: "must be between 1 and 65535"}
	}
	if shortID == "" && !v.RotateRealityKey {
		shortID = strings.TrimSpace(stringFromMap(reality, "short_id"))
	}
	if shortID != "" && !validControlledRealityShortID(shortID) {
		return &core.ConfigFieldError{Path: "reality.short_id", Problem: "must be an even-length hexadecimal string between 2 and 16 characters"}
	}

	privateKey := ""
	if !v.RotateRealityKey {
		privateKey = strings.TrimSpace(stringFromMap(currentReality, "private_key"))
		if privateKey == "" {
			privateKey = strings.TrimSpace(stringFromMap(reality, "private_key"))
		}
	}
	if privateKey == "" {
		pair, err := generateRealityKeyPair()
		if err != nil {
			return err
		}
		privateKey = pair.PrivateKey
		if shortID == "" || v.RotateRealityKey {
			shortID = pair.ShortID
		}
	}
	publicKey, err := deriveRealityPublicKey(privateKey)
	if err != nil {
		return &core.ConfigFieldError{Path: "config_json.tls.reality.private_key", Problem: err.Error()}
	}
	if shortID == "" {
		shortID, err = randomRealityShortID()
		if err != nil {
			return err
		}
	}

	cfg["flow"] = "xtls-rprx-vision"
	tls["enabled"] = true
	tls["server_name"] = serverName
	delete(tls, "certificate_path")
	delete(tls, "key_path")
	delete(tls, "acme")
	reality["enabled"] = true
	reality["handshake"] = map[string]any{"server": serverName, "server_port": port}
	reality["private_key"] = privateKey
	reality["public_key"] = publicKey
	reality["short_id"] = strings.ToLower(shortID)
	v.TLS = false
	v.CertificateMode = model.CertificateModeExternal
	v.CertificateID = nil
	v.CertificateDomain = ""
	v.RotateRealityKey = false
	v.ConfigJSON = encodeInboundJSON(cfg)
	return nil
}

func inboundRealityConfig(inbound *model.Inbound) map[string]any {
	if inbound == nil {
		return nil
	}
	var cfg map[string]any
	if json.Unmarshal([]byte(inbound.ConfigJSON), &cfg) != nil {
		return nil
	}
	tls := objectMap(cfg["tls"])
	return objectMap(tls["reality"])
}

func objectMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func validControlledRealityShortID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) < 2 || len(value) > 16 || len(value)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

const (
	defaultHY2UpMbps   = 1000
	defaultHY2DownMbps = 500
)

func applyHY2BandwidthDefaults(cfg map[string]any) {
	if cfg["up_mbps"] == nil {
		cfg["up_mbps"] = defaultHY2UpMbps
	}
	if cfg["down_mbps"] == nil {
		cfg["down_mbps"] = defaultHY2DownMbps
	}
}

func applyHY2SalamanderObfs(cfg map[string]any) error {
	obfs := ensureObject(cfg, "obfs")
	obfs["type"] = "salamander"
	if strings.TrimSpace(stringFromMap(obfs, "password")) == "" {
		secret, err := security.RandomToken(18)
		if err != nil {
			return err
		}
		obfs["password"] = secret
	}
	return nil
}

func hy2ObfsType(cfg map[string]any) string {
	return strings.ToLower(strings.TrimSpace(stringFromMap(objectMap(cfg["obfs"]), "type")))
}

func encodeInboundJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// inboundRequiresOwnDomain is true for TLS kinds that must use an operator-owned
// hostname (AnyTLS, HY2, VLESS WebSocket, leftover VLESS TLS). External and
// explicit certificate modes keep their existing SNI/path workflow.
func inboundRequiresOwnDomain(v model.Inbound) bool {
	if v.CertificateMode == model.CertificateModeExternal || v.CertificateMode == model.CertificateModeExplicit {
		return false
	}
	kind := inferredInboundKind(v)
	if inboundKindUsesManagedCertificate(kind) {
		return true
	}
	return v.TLS && v.Protocol == model.ProtocolVLESS && kind != "vless-reality"
}

func inboundKindUsesManagedCertificate(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "vless-ws", "hy2-tls", "hy2-salamander", "anytls-basic", "anytls-large-padding":
		return true
	default:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(kind)), "anytls-")
	}
}

func inferredInboundKind(v model.Inbound) string {
	var cfg map[string]any
	_ = json.Unmarshal([]byte(v.ConfigJSON), &cfg)
	if v.Protocol == model.ProtocolVLESS {
		tls := objectMap(cfg["tls"])
		if reality := objectMap(tls["reality"]); reality != nil && (reality["enabled"] == true || strings.TrimSpace(stringFromMap(reality, "public_key")) != "") {
			return "vless-reality"
		}
		transport := objectMap(cfg["transport"])
		if strings.EqualFold(stringFromMap(transport, "type"), "ws") {
			return "vless-ws"
		}
		if tls["enabled"] == true {
			return ""
		}
		return "vless-tcp"
	}
	switch v.Protocol {
	case model.ProtocolHY2:
		if hy2ObfsType(cfg) == "salamander" {
			return "hy2-salamander"
		}
		return "hy2-tls"
	case model.ProtocolAnyTLS:
		actual, _ := json.Marshal(cfg["padding_scheme"])
		large, _ := json.Marshal(core.AnyTLSLargePaddingScheme())
		if string(actual) == string(large) {
			return "anytls-large-padding"
		}
		return "anytls-basic"
	case model.ProtocolSS:
		switch strings.ToLower(strings.TrimSpace(stringFromMap(cfg, "method"))) {
		case "aes-128-gcm":
			return "ss-aes-128-gcm"
		case "aes-256-gcm":
			return "ss-aes-256-gcm"
		case "2022-blake3-aes-256-gcm":
			return "ss-2022-256"
		default:
			return "ss-2022-128"
		}
	case model.ProtocolMieru:
		return "mieru-basic"
	case model.ProtocolSnell:
		if version := intFromAnyController(cfg["version"]); version >= 6 {
			return "snell-v6"
		}
		return "snell-v4"
	case model.ProtocolSocks:
		return "socks5-auth"
	case model.ProtocolSSH:
		return "ssh-restricted"
	}
	return ""
}
