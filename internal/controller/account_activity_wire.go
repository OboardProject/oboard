package controller

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/auditactivity"
)

type accountActivityWireItem struct {
	UserID        int64  `json:"user_id"`
	InboundID     int64  `json:"inbound_id"`
	PathID        int64  `json:"path_id"`
	SourcePrefix  string `json:"source_prefix"`
	ActivityBits  uint16 `json:"activity_bits"`
	UploadBytes   int64  `json:"upload_bytes"`
	DownloadBytes int64  `json:"download_bytes"`
}

type accountActivityWireReport struct {
	CollectorBootID      string                    `json:"collector_boot_id"`
	CollectorStartedAt   int64                     `json:"collector_started_at"`
	StreamType           string                    `json:"stream_type"`
	Sequence             int64                     `json:"sequence"`
	MinuteUnix           int64                     `json:"minute_unix"`
	ClockState           string                    `json:"clock_state"`
	Complete             bool                      `json:"complete"`
	DroppedUpdates       uint64                    `json:"dropped_updates"`
	UnknownSourceUpdates uint64                    `json:"unknown_source_updates"`
	Items                []accountActivityWireItem `json:"items"`
}

func validateAccountActivityWire(r accountActivityWireReport) error {
	boot, err := hex.DecodeString(r.CollectorBootID)
	if r.CollectorStartedAt <= 0 || err != nil || len(boot) != 16 || strings.ToLower(r.CollectorBootID) != r.CollectorBootID || (r.StreamType != "kernel" && r.StreamType != "ssh") || r.Sequence <= 0 || r.MinuteUnix <= 0 || r.MinuteUnix%60 != 0 || len(r.Items) > 4096 {
		return errors.New("invalid activity identity")
	}
	if r.ClockState != "aligned" && r.ClockState != "unknown" && r.ClockState != "unaligned" {
		return errors.New("invalid activity clock")
	}
	for _, item := range r.Items {
		if item.UserID <= 0 || item.InboundID <= 0 || item.PathID < 0 || item.ActivityBits > 4095 || item.UploadBytes < 0 || item.DownloadBytes < 0 || item.UploadBytes > math.MaxInt64-item.DownloadBytes || (item.ActivityBits != 0 && item.UploadBytes+item.DownloadBytes == 0) {
			return errors.New("invalid activity item")
		}
	}
	return nil
}

func accountActivitySourceGroup(secret string, userID int64, raw string, configured ...auditactivity.SourcePolicy) (string, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || prefix.String() != raw || prefix.Masked() != prefix || prefix.Addr().Is4In6() {
		return "", errors.New("invalid activity source prefix")
	}
	bits := 56
	if prefix.Addr().Is4() {
		bits = 24
	}
	if prefix.Bits() != bits {
		return "", errors.New("unsupported activity source prefix")
	}
	key, policy := auditactivity.ControllerSourcePolicy(secret)
	if len(configured) > 0 {
		policy = configured[0]
	}
	return auditactivity.SourceGroup(key, userID, prefix.Addr().String(), true, policy)
}

// The keyed digest preserves wire idempotency across source-policy changes
// without retaining raw prefixes or exposing an enumerable prefix checksum.
func accountActivityReceiptDigest(secret string, report accountActivityWireReport) [32]byte {
	report.Items = append([]accountActivityWireItem(nil), report.Items...)
	sort.Slice(report.Items, func(i, j int) bool {
		a, b := report.Items[i], report.Items[j]
		if a.UserID != b.UserID {
			return a.UserID < b.UserID
		}
		if a.InboundID != b.InboundID {
			return a.InboundID < b.InboundID
		}
		if a.PathID != b.PathID {
			return a.PathID < b.PathID
		}
		return a.SourcePrefix < b.SourcePrefix
	})
	encoded, _ := json.Marshal(report)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("account-activity-receipt-v1\x00"))
	mac.Write(encoded)
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}
