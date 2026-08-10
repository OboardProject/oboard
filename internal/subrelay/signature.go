package subrelay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderTimestamp = "X-OBoard-Relay-Timestamp"
	HeaderNonce     = "X-OBoard-Relay-Nonce"
	HeaderClientIP  = "X-OBoard-Relay-Client-IP"
	HeaderSignature = "X-OBoard-Relay-Signature"
	MaxClockSkew    = 2 * time.Minute
)

func ValidateSecret(secret string) error {
	if len(strings.TrimSpace(secret)) < 32 {
		return errors.New("subscription relay secret must be at least 32 characters")
	}
	return nil
}

func Sign(secret, method, requestURI, timestamp, nonce, clientIP, userAgent, ifNoneMatch string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical(method, requestURI, timestamp, nonce, clientIP, userAgent, ifNoneMatch)))
	return hex.EncodeToString(mac.Sum(nil))
}

func Verify(secret, method, requestURI, timestamp, nonce, clientIP, userAgent, ifNoneMatch, signature string, now time.Time) error {
	if err := ValidateSecret(secret); err != nil {
		return err
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("invalid relay timestamp")
	}
	requestedAt := time.Unix(seconds, 0)
	if delta := now.Sub(requestedAt); delta < -MaxClockSkew || delta > MaxClockSkew {
		return errors.New("expired relay signature")
	}
	if len(nonce) < 24 || len(nonce) > 128 {
		return errors.New("invalid relay nonce")
	}
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return errors.New("invalid relay signature")
	}
	expected := Sign(secret, method, requestURI, timestamp, nonce, clientIP, userAgent, ifNoneMatch)
	expectedBytes, _ := hex.DecodeString(expected)
	if !hmac.Equal(provided, expectedBytes) {
		return errors.New("invalid relay signature")
	}
	return nil
}

func canonical(values ...string) string {
	var out strings.Builder
	for _, value := range values {
		fmt.Fprintf(&out, "%d:%s\n", len(value), value)
	}
	return out.String()
}
