package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// UsersEnvelopeFields are the signed fields of a runtime-user envelope.
// UsersJSON is hashed rather than embedded so the canonical string stays short.
type UsersEnvelopeFields struct {
	ServerID  int64
	MessageID string
	Revision  int64
	Digest    string
	UsersJSON string
}

// canonicalUsersEnvelope is the shared canonical form:
//
//	users_v1\n<server_id>\n<message_id>\n<users_revision>\n<users_digest>\n<sha256(users_json)>
func canonicalUsersEnvelope(f UsersEnvelopeFields) string {
	sum := sha256.Sum256([]byte(f.UsersJSON))
	return strings.Join([]string{
		"users_v1",
		strconv.FormatInt(f.ServerID, 10),
		f.MessageID,
		strconv.FormatInt(f.Revision, 10),
		f.Digest,
		hex.EncodeToString(sum[:]),
	}, "\n")
}

func SignUsersEnvelope(secret string, f UsersEnvelopeFields) string {
	return sign(secret, canonicalUsersEnvelope(f))
}

func VerifyUsersEnvelope(secret string, f UsersEnvelopeFields, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	return hmac.Equal([]byte(SignUsersEnvelope(secret, f)), []byte(signature))
}
