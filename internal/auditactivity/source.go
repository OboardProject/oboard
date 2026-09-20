// Package auditactivity contains bounded account activity primitives. It does not
// infer devices, authorization, or collection coverage from network addresses.
package auditactivity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/netip"
)

// ControllerSourcePolicy is shared by subscription ingress and authenticated
// node report normalization. Rotating the secret changes both key and epoch, so
// incomparable source histories cannot become false first observations.
func ControllerSourcePolicy(secret string) ([]byte, SourcePolicy) {
	key := sha256.Sum256([]byte("oboard-account-activity-source-key\x00" + secret))
	epoch := sha256.Sum256(key[:])
	return key[:], SourcePolicy{Version: "prefix-v1", Epoch: hex.EncodeToString(epoch[:8]), IPv4Bits: 24, IPv6Bits: 56}
}

func (p SourcePolicy) ID() string { return p.Version + ":" + p.Epoch }

type SourcePolicy struct {
	Version  string `json:"version"`
	Epoch    string `json:"epoch"`
	IPv4Bits int    `json:"ipv4_bits"`
	IPv6Bits int    `json:"ipv6_bits"`
}

func (p SourcePolicy) Validate() error {
	if p.Version == "" || len(p.Version) > 64 || p.Epoch == "" || len(p.Epoch) > 64 || p.IPv4Bits < 0 || p.IPv4Bits > 32 || p.IPv6Bits < 0 || p.IPv6Bits > 128 {
		return errors.New("invalid source policy")
	}
	return nil
}

// SourceGroup accepts an address obtained at a trusted ingress, never a forwarded
// header or client-supplied account. A caller unable to establish that boundary
// must pass trusted=false. The 256-bit digest is scoped to one account and epoch.
func SourceGroup(key []byte, accountID int64, raw string, trusted bool, p SourcePolicy) (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	if len(key) < 32 || accountID <= 0 {
		return "", errors.New("invalid source key or account")
	}
	if !trusted {
		return "", errors.New("source_untrusted")
	}
	address, err := netip.ParseAddr(raw)
	if err != nil || address.Zone() != "" {
		return "", errors.New("source_unusable")
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return "", errors.New("source_unusable")
	}
	bits := p.IPv6Bits
	if address.Is4() {
		bits = p.IPv4Bits
	}
	prefix := netip.PrefixFrom(address, bits).Masked().String()
	h := hmac.New(sha256.New, key)
	h.Write([]byte("oboard-account-source\x00"))
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], uint64(accountID))
	h.Write(id[:])
	for _, v := range []string{p.Version, p.Epoch, prefix} {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(v)))
		h.Write(size[:])
		h.Write([]byte(v))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
