package core

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"golang.org/x/crypto/hkdf"
)

// ExpandDeviceUsers projects each account into the authentication identities
// that may be installed on the data plane. A nil device slice means the caller
// is outside the Controller routing snapshot and preserves the supplied users.
func ExpandDeviceUsers(users []model.User, devices []model.UserDevice) []model.User {
	if devices == nil {
		return append([]model.User(nil), users...)
	}
	byUser := make(map[int64][]model.UserDevice)
	for _, device := range devices {
		if device.UserID > 0 && device.Status == "active" {
			byUser[device.UserID] = append(byUser[device.UserID], device)
		}
	}
	out := make([]model.User, 0, len(users)+len(devices))
	for _, user := range users {
		if user.Status != "active" {
			continue
		}
		if user.LegacyProxyEnabled {
			out = append(out, user)
		}
		for _, device := range byUser[user.ID] {
			out = append(out, UserForDevice(user, device))
		}
	}
	return out
}

func UserForDevice(user model.User, device model.UserDevice) model.User {
	out := user
	out.Username = deviceAuthUsername(user.ID, device.DeviceIDHash)
	out.SSHRandomID = deviceSSHRandomID(user.ProxyPassword, device.DeviceIDHash, device.CredentialEpoch)
	out.DeviceIDHash = device.DeviceIDHash
	out.CredentialEpoch = device.CredentialEpoch
	out.CredentialSeed = user.ProxyPassword
	out.CredentialStatus = device.ProxyAccessState
	if out.CredentialStatus == "" {
		out.CredentialStatus = "active"
	}
	out = credentialUser(out, 0, 0, "device")
	return out
}

func UserCredentialForRoute(user model.User, inboundID, pathID int64, protocol model.Protocol) model.User {
	return credentialUser(user, inboundID, pathID, string(protocol))
}

func credentialUsersForInbound(users []model.User, inbound model.Inbound) []model.User {
	out := make([]model.User, 0, len(users))
	for _, user := range users {
		pathID := runtimePathIDFromUsername(user.Username)
		out = append(out, credentialUser(user, inbound.ID, pathID, string(inbound.Protocol)))
	}
	return out
}

func credentialUserForInbound(user model.User, inbound model.Inbound) model.User {
	return credentialUser(user, inbound.ID, runtimePathIDFromUsername(user.Username), string(inbound.Protocol))
}

func credentialUser(user model.User, inboundID, pathID int64, protocol string) model.User {
	if user.DeviceIDHash == "" || user.CredentialEpoch <= 0 || user.CredentialSeed == "" {
		return user
	}
	key := deviceKey(user.CredentialSeed, user.DeviceIDHash, user.CredentialEpoch)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(strconv.FormatInt(inboundID, 10)))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(strconv.FormatInt(pathID, 10)))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(protocol))
	sum := mac.Sum(nil)
	uuid := append([]byte(nil), sum[:16]...)
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	user.ProxyUUID = fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
	user.ProxyPassword = base64.RawURLEncoding.EncodeToString(sum)
	return user
}

func deviceKey(userSecret, deviceIDHash string, epoch int64) []byte {
	info := []byte("oboard-device-v1\x00" + deviceIDHash + "\x00" + strconv.FormatInt(epoch, 10))
	reader := hkdf.New(sha256.New, []byte(userSecret), nil, info)
	key := make([]byte, 32)
	if _, err := io.ReadFull(reader, key); err != nil {
		panic(err)
	}
	return key
}

func deviceAuthUsername(userID int64, deviceIDHash string) string {
	suffix := strings.ToLower(strings.TrimSpace(deviceIDHash))
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return fmt.Sprintf("u%d__oboard_device_%s", userID, suffix)
}

func deviceSSHRandomID(userSecret, deviceIDHash string, epoch int64) string {
	key := deviceKey(userSecret, deviceIDHash, epoch)
	value := uint64(0)
	for _, b := range key[:8] {
		value = value<<8 | uint64(b)
	}
	return fmt.Sprintf("%012d", value%1_000_000_000_000)
}
