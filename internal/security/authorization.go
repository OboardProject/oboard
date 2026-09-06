package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// AuthorizationEnvelopeFields are the signed fields of an authorization
// envelope. LeaseJSON is hashed rather than embedded so the canonical string
// stays short and independent of how the receiver decodes the lease.
type AuthorizationEnvelopeFields struct {
	ServerID  int64
	MessageID string
	Revision  int64
	Sequence  int64
	IssuedAt  string
	ExpiresAt string
	LeaseJSON string
}

// canonicalAuthorizationEnvelope is the shared canonical form:
//
//	authz_v1\n<server_id>\n<message_id>\n<revision>\n<sequence>\n<issued_at>\n<expires_at>\n<sha256(lease_json)>
func canonicalAuthorizationEnvelope(f AuthorizationEnvelopeFields) string {
	sum := sha256.Sum256([]byte(f.LeaseJSON))
	return strings.Join([]string{
		"authz_v1",
		strconv.FormatInt(f.ServerID, 10),
		f.MessageID,
		strconv.FormatInt(f.Revision, 10),
		strconv.FormatInt(f.Sequence, 10),
		f.IssuedAt,
		f.ExpiresAt,
		hex.EncodeToString(sum[:]),
	}, "\n")
}

// SignAuthorizationEnvelope produces the HMAC-SHA256 signature of an
// authorization envelope keyed with the Agent token hash.
func SignAuthorizationEnvelope(secret string, f AuthorizationEnvelopeFields) string {
	return sign(secret, canonicalAuthorizationEnvelope(f))
}

// VerifyAuthorizationEnvelope checks an envelope signature in constant time.
func VerifyAuthorizationEnvelope(secret string, f AuthorizationEnvelopeFields, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	return hmac.Equal([]byte(SignAuthorizationEnvelope(secret, f)), []byte(signature))
}
