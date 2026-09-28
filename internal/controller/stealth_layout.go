package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
)

type stealthIdentity struct {
	AgentName      string `json:"agent_name"`
	InstallDirName string `json:"install_dir_name"`
	CoreName       string `json:"core_name"`
	RealmName      string `json:"realm_name"`
	ConfigDirName  string `json:"config_dir_name"`
	StateDirName   string `json:"state_dir_name"`
	AgentLogName   string `json:"agent_log_name"`
	CoreLogName    string `json:"core_log_name"`
	SocketName     string `json:"socket_name"`
	SshdName       string `json:"sshd_name"`
	SshName        string `json:"ssh_name"`
	StagingPrefix  string `json:"staging_prefix"`
	ConfigFileName string `json:"config_file_name"`
	KeyFileName    string `json:"key_file_name"`
}

var stealthNamePattern = regexp.MustCompile(`^[a-z][a-z0-9]{9}$`)

func newStealthIdentity() (stealthIdentity, error) {
	names := make([]string, 14)
	seen := make(map[string]bool, len(names))
	for i := range names {
		for {
			var random [5]byte
			if _, err := rand.Read(random[:]); err != nil {
				return stealthIdentity{}, err
			}
			name := fmt.Sprintf("a%09x", (uint64(random[0])<<32|uint64(random[1])<<24|uint64(random[2])<<16|uint64(random[3])<<8|uint64(random[4]))&0xfffffffff)
			if !seen[name] {
				names[i] = name
				seen[name] = true
				break
			}
		}
	}
	return stealthIdentity{
		AgentName: names[0], InstallDirName: names[1], CoreName: names[2], RealmName: names[3],
		ConfigDirName: names[4], StateDirName: names[5], AgentLogName: names[6], CoreLogName: names[7],
		SocketName: names[8], SshdName: names[9], SshName: names[10], StagingPrefix: names[11],
		ConfigFileName: names[12], KeyFileName: names[13],
	}, nil
}

func (i stealthIdentity) valid() bool {
	names := []string{i.AgentName, i.InstallDirName, i.CoreName, i.RealmName, i.ConfigDirName, i.StateDirName, i.AgentLogName, i.CoreLogName, i.SocketName, i.SshdName, i.SshName, i.StagingPrefix, i.ConfigFileName, i.KeyFileName}
	seen := map[string]bool{}
	for _, name := range names {
		if !stealthNamePattern.MatchString(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return true
}

func (s *Server) serverStealthLayout(ctx context.Context, serverID int64) (string, error) {
	if serverID <= 0 {
		return "", fmt.Errorf("server ID is required for security-process layout")
	}
	key := fmt.Sprintf("server_stealth_layout.%d", serverID)
	identity, err := newStealthIdentity()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	stored, err := s.store.SetSettingIfAbsent(ctx, key, string(raw))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal([]byte(stored), &identity); err != nil || !identity.valid() {
		return "", fmt.Errorf("invalid saved security-process layout for server %d", serverID)
	}
	return base64.StdEncoding.EncodeToString([]byte(stored)), nil
}
