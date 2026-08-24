package controller

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const defaultVLESSRealityServerName = "gateway.icloud.com"

type realityKeyPair struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	ShortID    string `json:"short_id"`
}

func generateRealityKeyPair() (realityKeyPair, error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return realityKeyPair{}, err
	}
	shortID, err := randomRealityShortID()
	if err != nil {
		return realityKeyPair{}, err
	}
	return realityKeyPair{
		PrivateKey: base64.RawURLEncoding.EncodeToString(key.Bytes()),
		PublicKey:  base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		ShortID:    shortID,
	}, nil
}

func deriveRealityPublicKey(privateKey string) (string, error) {
	decoded, err := decodeRealityKey(privateKey)
	if err != nil {
		return "", err
	}
	key, err := ecdh.X25519().NewPrivateKey(decoded)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), nil
}

func decodeRealityKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty Reality private key")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(value)
	}
	if err != nil {
		return nil, err
	}
	if len(decoded) != 32 {
		return nil, errors.New("decoded Reality private key must be 32 bytes")
	}
	return decoded, nil
}

func randomRealityShortID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func applyVLESSRealityDefaults(cfg map[string]any) error {
	tlsRaw, ok := cfg["tls"].(map[string]any)
	if !ok {
		return nil
	}
	realityRaw, ok := tlsRaw["reality"].(map[string]any)
	if !ok || !realityConfigEnabled(realityRaw) {
		return nil
	}
	// Match the panel's vless-reality preset and the chain-service template:
	// Vision flow, TLS enabled, and a concrete handshake port are mandatory for
	// a usable Reality inbound. Without them the generated kernel config would
	// silently skip TLS and the subscription would lose the Vision flow.
	if strings.TrimSpace(stringFromMap(cfg, "flow")) == "" {
		cfg["flow"] = "xtls-rprx-vision"
	}
	if _, exists := tlsRaw["enabled"]; !exists {
		tlsRaw["enabled"] = true
	}
	handshake, _ := realityRaw["handshake"].(map[string]any)
	if handshake == nil {
		handshake = map[string]any{}
		realityRaw["handshake"] = handshake
	}
	serverName := strings.TrimSpace(stringFromMap(tlsRaw, "server_name"))
	if serverName == "" {
		serverName = strings.TrimSpace(stringFromMap(handshake, "server"))
	}
	if serverName == "" {
		serverName = defaultVLESSRealityServerName
	}
	tlsRaw["server_name"] = serverName
	handshake["server"] = serverName
	if port := handshakePortFromMap(handshake); port <= 0 {
		handshake["server_port"] = 443
	}
	privateKey := strings.TrimSpace(stringFromMap(realityRaw, "private_key"))
	if privateKey == "" {
		pair, err := generateRealityKeyPair()
		if err != nil {
			return err
		}
		realityRaw["private_key"] = pair.PrivateKey
		realityRaw["public_key"] = pair.PublicKey
		if strings.TrimSpace(stringFromMap(realityRaw, "short_id")) == "" {
			realityRaw["short_id"] = pair.ShortID
		}
		return nil
	}
	publicKey, err := deriveRealityPublicKey(privateKey)
	if err != nil {
		return err
	}
	realityRaw["public_key"] = publicKey
	if strings.TrimSpace(stringFromMap(realityRaw, "short_id")) == "" {
		shortID, err := randomRealityShortID()
		if err != nil {
			return err
		}
		realityRaw["short_id"] = shortID
	}
	return nil
}

func handshakePortFromMap(m map[string]any) int {
	switch value := m["server_port"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return 0
}

func realityConfigEnabled(reality map[string]any) bool {
	if enabled, ok := reality["enabled"].(bool); ok && enabled {
		return true
	}
	if strings.TrimSpace(stringFromMap(reality, "private_key")) != "" || strings.TrimSpace(stringFromMap(reality, "public_key")) != "" {
		return true
	}
	if _, ok := reality["handshake"].(map[string]any); ok {
		return true
	}
	raw, _ := json.Marshal(reality)
	return strings.TrimSpace(string(raw)) != "{}"
}
