package agentlink

import (
	"crypto/sha256"
	"encoding/hex"
)

// sha256HexString derives the hex pin for a DER certificate.
func sha256HexString(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}
