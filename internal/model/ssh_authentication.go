package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// SSHAuthenticationPlanDigest binds a verification to listener and credential
// identities without exposing passwords or a password-derived public digest.
func SSHAuthenticationPlanDigest(plan SSHInboundPlan) string {
	type listener struct {
		ID       int64            `json:"id"`
		ServerID int64            `json:"server_id"`
		ListenIP string           `json:"listen_ip"`
		Port     int              `json:"port"`
		Users    []SSHInboundUser `json:"users"`
	}
	entries := make([]listener, 0, len(plan.Inbounds))
	for _, inbound := range plan.Inbounds {
		if !inbound.Enabled {
			continue
		}
		entry := listener{ID: inbound.InboundID, ServerID: inbound.ServerID, ListenIP: inbound.ListenIP, Port: inbound.Port, Users: []SSHInboundUser{}}
		for _, user := range inbound.Users {
			if !user.Enabled {
				continue
			}
			user.Password = ""
			user.CredentialStatus = strings.TrimSpace(user.CredentialStatus)
			if user.CredentialStatus == "" {
				user.CredentialStatus = "active"
			}
			entry.Users = append(entry.Users, user)
		}
		sort.Slice(entry.Users, func(i, j int) bool { return entry.Users[i].Username < entry.Users[j].Username })
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	encoded, _ := json.Marshal(entries)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
