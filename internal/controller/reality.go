package controller

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type realityKeyPair struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	ShortID    string `json:"short_id"`
}

func (s *Server) realityKeypair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	pair, err := generateRealityKeyPair()
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	auditReq(s, r, "generate", "reality-keypair", "auto")
	write(w, http.StatusOK, pair)
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
