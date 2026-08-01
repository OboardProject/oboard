package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const encryptedSecretPrefix = "v1."

const (
	argonTime    = 1
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonSaltLen = 16
	argonKeyLen  = 32
)

func RandomToken(bytes int) (string, error) {
	b := make([]byte, bytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func ValidateControllerURL(raw string, devBuild bool, allowInsecure bool) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("controller_url must be a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https", "wss":
		return u, nil
	case "http", "ws":
		if devBuild || allowInsecure || EnvBool("OBOARD_ALLOW_INSECURE_AGENT_HTTP", false) || isLocalControllerHost(u.Hostname()) {
			return u, nil
		}
		return nil, errors.New("controller_url must use https/wss in production; http/ws is only allowed for localhost, dev builds, or explicit insecure override")
	default:
		return nil, errors.New("controller_url must use http(s) or ws(s)")
	}
}

func EnvBool(key string, fallback bool) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	case "0", "false", "FALSE", "no", "NO", "off", "OFF":
		return false
	default:
		return fallback
	}
}

func isLocalControllerHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func RandomUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func ValidUUID(v string) bool {
	if len(v) != 36 {
		return false
	}
	for i, c := range v {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func HashAPISecret(masterSecret, token string) string {
	mac := hmac.New(sha256.New, []byte(masterSecret))
	_, _ = mac.Write([]byte("oboard-api-token-v1\x00"))
	_, _ = mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func EncryptSecret(masterSecret, purpose, plaintext string) (string, error) {
	if masterSecret == "" {
		return "", errors.New("empty encryption secret")
	}
	block, err := aes.NewCipher(deriveEncryptionKey(masterSecret, purpose))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(plaintext), []byte(purpose))
	payload := append(nonce, ciphertext...)
	return encryptedSecretPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecryptSecret(masterSecret, purpose, encrypted string) (string, error) {
	if masterSecret == "" {
		return "", errors.New("empty encryption secret")
	}
	if !strings.HasPrefix(encrypted, encryptedSecretPrefix) {
		return "", errors.New("unsupported encrypted secret format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(encrypted, encryptedSecretPrefix))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(deriveEncryptionKey(masterSecret, purpose))
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize() {
		return "", errors.New("encrypted secret payload is too short")
	}
	plaintext, err := gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], []byte(purpose))
	if err != nil {
		return "", errors.New("decrypt secret")
	}
	return string(plaintext), nil
}

func deriveEncryptionKey(masterSecret, purpose string) []byte {
	mac := hmac.New(sha256.New, []byte(masterSecret))
	_, _ = mac.Write([]byte("oboard-secret:" + purpose))
	return mac.Sum(nil)
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" || parts[1] != "v=19" {
		return false
	}
	var memory uint32
	var iterations uint32
	var threads uint8
	for _, item := range strings.Split(parts[2], ",") {
		kv := strings.SplitN(item, "=", 2)
		if len(kv) != 2 {
			return false
		}
		switch kv[0] {
		case "m":
			v, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false
			}
			memory = uint32(v)
		case "t":
			v, err := strconv.ParseUint(kv[1], 10, 32)
			if err != nil {
				return false
			}
			iterations = uint32(v)
		case "p":
			v, err := strconv.ParseUint(kv[1], 10, 8)
			if err != nil {
				return false
			}
			threads = uint8(v)
		default:
			return false
		}
	}
	if memory != argonMemory || iterations != argonTime || threads != argonThreads {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(salt) != argonSaltLen {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(expected) != argonKeyLen {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, threads, argonKeyLen)
	return hmac.Equal(actual, expected)
}

type TokenClaims struct {
	Subject        int64
	Role           string
	SessionVersion int64
	ClientBinding  string
	SessionID      string
	Expiry         time.Time
}

func SignSession(secret string, claims TokenClaims) (string, error) {
	if secret == "" {
		return "", errors.New("empty session secret")
	}
	if claims.ClientBinding == "" {
		return "", errors.New("empty client binding")
	}
	if claims.SessionID == "" {
		var err error
		claims.SessionID, err = RandomToken(16)
		if err != nil {
			return "", err
		}
	}
	if strings.Contains(claims.SessionID, "|") {
		return "", errors.New("invalid session ID")
	}
	// Session version supports account-wide revocation while the random session
	// ID keeps separate logins independently revocable.
	payload := fmt.Sprintf("%d|%s|%d|%s|%s|%d", claims.Subject, claims.Role, claims.SessionVersion, claims.ClientBinding, claims.SessionID, claims.Expiry.Unix())
	sig := sign(secret, payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sig, nil
}

func VerifySession(secret, token string) (TokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return TokenClaims{}, errors.New("invalid token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return TokenClaims{}, err
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(sign(secret, payload)), []byte(parts[1])) {
		return TokenClaims{}, errors.New("invalid signature")
	}
	fields := strings.Split(payload, "|")
	if len(fields) != 6 {
		return TokenClaims{}, errors.New("invalid claims")
	}
	subject, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return TokenClaims{}, err
	}
	version, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return TokenClaims{}, err
	}
	if fields[3] == "" {
		return TokenClaims{}, errors.New("invalid client binding")
	}
	if fields[4] == "" {
		return TokenClaims{}, errors.New("invalid session ID")
	}
	expiryUnix, err := strconv.ParseInt(fields[5], 10, 64)
	if err != nil {
		return TokenClaims{}, err
	}
	claims := TokenClaims{Subject: subject, Role: fields[1], SessionVersion: version, ClientBinding: fields[3], SessionID: fields[4], Expiry: time.Unix(expiryUnix, 0)}
	if time.Now().After(claims.Expiry) {
		return TokenClaims{}, errors.New("expired token")
	}
	return claims, nil
}

type TaskEnvelope struct {
	ID            int64  `json:"id"`
	ServerID      int64  `json:"server_id"`
	Type          string `json:"type"`
	ConfigVersion int64  `json:"config_version"`
	Nonce         string `json:"nonce"`
	PayloadJSON   string `json:"payload_json"`
}

func SignTaskEnvelope(secret string, task TaskEnvelope) string {
	return sign(secret, canonicalTaskEnvelope(task))
}

func canonicalTaskEnvelope(task TaskEnvelope) string {
	// Fixed struct field order gives a stable canonical representation for this
	// narrowly scoped envelope and avoids signing mutable task status fields.
	b, _ := json.Marshal(struct {
		ID            int64  `json:"id"`
		ServerID      int64  `json:"server_id"`
		Type          string `json:"type"`
		ConfigVersion int64  `json:"config_version"`
		Nonce         string `json:"nonce"`
		PayloadJSON   string `json:"payload_json"`
	}{task.ID, task.ServerID, task.Type, task.ConfigVersion, task.Nonce, task.PayloadJSON})
	return string(b)
}

func sign(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
