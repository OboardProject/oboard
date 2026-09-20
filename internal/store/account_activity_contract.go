package store

import (
	"encoding/hex"
	"errors"
	"strings"
)

var (
	ErrAccountActivityInvalid  = errors.New("invalid account activity")
	ErrAccountActivityExpired  = errors.New("account activity expired")
	ErrAccountActivityConflict = errors.New("account activity identity conflict")
	ErrAccountActivityCapacity = errors.New("account activity capacity reached")
)

func activityHex(s string, size int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == size && strings.ToLower(s) == s
}
