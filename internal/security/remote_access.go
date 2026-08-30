package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	StepUpTokenTTL            = 120 * time.Second
	InteractivePrepareTTL     = 60 * time.Second
	InteractiveSignatureV1    = 1
	InteractiveSignatureV2    = 2
	MaxClockSkew              = 30 * time.Second
)

type StepUpTokenClaims struct {
	UserID         int64
	SessionID      string
	SessionVersion int64
	Purpose        string
	ResourceType   string
	ResourceID     string
	Nonce          string
	IssuedAt       time.Time
	ExpiresAt      time.Time
}

func SignStepUpToken(secret string, claims StepUpTokenClaims) (string, error) {
	if secret == "" || claims.UserID <= 0 || claims.SessionID == "" || claims.Purpose == "" || claims.Nonce == "" {
		return "", errors.New("incomplete step-up token claims")
	}
	payload := strings.Join([]string{
		strconv.FormatInt(claims.UserID, 10),
		claims.SessionID,
		strconv.FormatInt(claims.SessionVersion, 10),
		claims.Purpose,
		claims.ResourceType,
		claims.ResourceID,
		claims.Nonce,
		strconv.FormatInt(claims.IssuedAt.UTC().Unix(), 10),
		strconv.FormatInt(claims.ExpiresAt.UTC().Unix(), 10),
	}, "|")
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + sign(secret, payload), nil
}

func VerifyStepUpToken(secret, token string, now time.Time) (StepUpTokenClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return StepUpTokenClaims{}, errors.New("invalid step-up token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return StepUpTokenClaims{}, errors.New("invalid step-up token")
	}
	payload := string(payloadBytes)
	if !hmac.Equal([]byte(sign(secret, payload)), []byte(parts[1])) {
		return StepUpTokenClaims{}, errors.New("invalid step-up token")
	}
	fields := strings.Split(payload, "|")
	if len(fields) != 9 {
		return StepUpTokenClaims{}, errors.New("invalid step-up token")
	}
	userID, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || userID <= 0 {
		return StepUpTokenClaims{}, errors.New("invalid step-up token")
	}
	sessionVersion, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return StepUpTokenClaims{}, errors.New("invalid step-up token")
	}
	issuedUnix, err := strconv.ParseInt(fields[7], 10, 64)
	if err != nil {
		return StepUpTokenClaims{}, errors.New("invalid step-up token")
	}
	expiresUnix, err := strconv.ParseInt(fields[8], 10, 64)
	if err != nil {
		return StepUpTokenClaims{}, errors.New("invalid step-up token")
	}
	claims := StepUpTokenClaims{
		UserID: userID, SessionID: fields[1], SessionVersion: sessionVersion,
		Purpose: fields[3], ResourceType: fields[4], ResourceID: fields[5], Nonce: fields[6],
		IssuedAt: time.Unix(issuedUnix, 0).UTC(), ExpiresAt: time.Unix(expiresUnix, 0).UTC(),
	}
	if !claims.ExpiresAt.After(now) {
		return StepUpTokenClaims{}, errors.New("step-up token expired")
	}
	return claims, nil
}

func StepUpTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type InteractiveEnvelope struct {
	Type      string `json:"type"`
	ServerID  int64  `json:"server_id"`
	SessionID string `json:"session_id"`
	Nonce     string `json:"nonce"`
	IssuedAt  string `json:"issued_at"`
	ExpiresAt string `json:"expires_at"`
	Kind      string `json:"kind"`
	Origin    string `json:"origin,omitempty"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
	Mode      string `json:"mode,omitempty"`
}

func SignInteractiveEnvelope(secret string, env InteractiveEnvelope) string {
	return sign(secret, canonicalInteractiveEnvelope(env))
}

func VerifyInteractiveEnvelope(secret string, env InteractiveEnvelope, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	expected := SignInteractiveEnvelope(secret, env)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func SignInteractiveEnvelopeV2(secret string, env InteractiveEnvelope) string {
	env.Origin = normalizeInteractiveOrigin(env.Origin)
	return sign(secret, canonicalInteractiveEnvelopeV2(env))
}

func VerifyInteractiveEnvelopeV2(secret string, env InteractiveEnvelope, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	expected := SignInteractiveEnvelopeV2(secret, env)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func normalizeInteractiveOrigin(origin string) string {
	switch strings.TrimSpace(origin) {
	case "mcp":
		return "mcp"
	default:
		return "human"
	}
}

func canonicalInteractiveEnvelope(env InteractiveEnvelope) string {
	b, _ := json.Marshal(struct {
		Type      string `json:"type"`
		ServerID  int64  `json:"server_id"`
		SessionID string `json:"session_id"`
		Nonce     string `json:"nonce"`
		IssuedAt  string `json:"issued_at"`
		ExpiresAt string `json:"expires_at"`
		Kind      string `json:"kind"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
	}{env.Type, env.ServerID, env.SessionID, env.Nonce, env.IssuedAt, env.ExpiresAt, env.Kind, env.Cols, env.Rows})
	return string(b)
}

func canonicalInteractiveEnvelopeV2(env InteractiveEnvelope) string {
	b, _ := json.Marshal(struct {
		Type      string `json:"type"`
		ServerID  int64  `json:"server_id"`
		SessionID string `json:"session_id"`
		Nonce     string `json:"nonce"`
		IssuedAt  string `json:"issued_at"`
		ExpiresAt string `json:"expires_at"`
		Kind      string `json:"kind"`
		Origin    string `json:"origin"`
		Cols      int    `json:"cols"`
		Rows      int    `json:"rows"`
		Mode      string `json:"mode"`
	}{env.Type, env.ServerID, env.SessionID, env.Nonce, env.IssuedAt, env.ExpiresAt, env.Kind, normalizeInteractiveOrigin(env.Origin), env.Cols, env.Rows, strings.TrimSpace(env.Mode)})
	return string(b)
}

func InteractiveProof(secret, sessionID string, serverID int64, nonce, expiresAt string) string {
	payload := fmt.Sprintf("%s|%d|%s|%s", sessionID, serverID, nonce, expiresAt)
	return sign(secret, payload)
}

func VerifyInteractiveProof(secret, sessionID string, serverID int64, nonce, expiresAt, proof string) bool {
	if secret == "" || proof == "" {
		return false
	}
	expected := InteractiveProof(secret, sessionID, serverID, nonce, expiresAt)
	return hmac.Equal([]byte(expected), []byte(proof))
}
