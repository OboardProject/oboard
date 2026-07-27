package controllerupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const RuntimeStateName = "controller-runtime.json"

type RuntimeState struct {
	ListenAddress string   `json:"listen_address"`
	BasePaths     []string `json:"base_paths"`
}

func WriteRuntimeState(path string, state RuntimeState) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("runtime state path is required")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode Controller runtime state: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create Controller runtime state directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create Controller runtime state: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure Controller runtime state: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write Controller runtime state: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync Controller runtime state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close Controller runtime state: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish Controller runtime state: %w", err)
	}
	return nil
}

func readRuntimeState(path string) (RuntimeState, error) {
	var state RuntimeState
	info, err := os.Lstat(path)
	if err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() {
		return state, errors.New("Controller runtime state must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return state, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return RuntimeState{}, fmt.Errorf("decode Controller runtime state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return RuntimeState{}, errors.New("Controller runtime state contains trailing data")
		}
		return RuntimeState{}, fmt.Errorf("decode Controller runtime state: %w", err)
	}
	return state, nil
}

func (s *Service) healthURLs(route string) []string {
	values, _ := readEnv(s.config.BinaryEnvPath)
	states := make([]RuntimeState, 0, 3)
	runtimePaths := []string{s.config.RuntimeStatePath}
	if dbPath := strings.TrimSpace(values["OBOARD_DB"]); filepath.IsAbs(dbPath) {
		derived := filepath.Join(filepath.Dir(dbPath), RuntimeStateName)
		if derived != s.config.RuntimeStatePath {
			runtimePaths = append([]string{derived}, runtimePaths...)
		}
	}
	for _, runtimePath := range runtimePaths {
		if state, err := readRuntimeState(runtimePath); err == nil {
			states = append(states, state)
		}
	}
	listenAddress := strings.TrimSpace(values["OBOARD_ADDR"])
	if listenAddress == "" {
		listenAddress = ":2787"
	}
	states = append(states, RuntimeState{
		ListenAddress: listenAddress,
		BasePaths:     []string{values["OBOARD_BASE_PATH"]},
	})

	seen := make(map[string]bool)
	urls := make([]string, 0, len(states)*2)
	for _, state := range states {
		hosts, port, err := localHealthAddress(state.ListenAddress)
		if err != nil || len(state.BasePaths) == 0 {
			continue
		}
		basePaths := make([]string, 0, len(state.BasePaths))
		valid := true
		for _, candidate := range state.BasePaths {
			basePath, ok := normalizeRuntimeBasePath(candidate)
			if !ok {
				valid = false
				break
			}
			basePaths = append(basePaths, basePath)
		}
		if !valid {
			continue
		}
		for _, basePath := range basePaths {
			for _, host := range hosts {
				url := "http://" + net.JoinHostPort(host, port) + basePath + route
				if !seen[url] {
					seen[url] = true
					urls = append(urls, url)
				}
			}
		}
	}
	return urls
}

func localHealthAddress(value string) ([]string, string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return nil, "", errors.New("invalid Controller listen address")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, "", errors.New("invalid Controller listen port")
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return []string{"127.0.0.1", "::1"}, port, nil
	}
	if strings.EqualFold(host, "localhost") {
		return []string{"127.0.0.1", "::1"}, port, nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil, "", errors.New("Controller listen host must be a local IP address")
	}
	if ip.IsUnspecified() {
		if ip.To4() != nil {
			return []string{"127.0.0.1"}, port, nil
		}
		return []string{"::1", "127.0.0.1"}, port, nil
	}
	if !ip.IsLoopback() && !isLocalInterfaceIP(ip) {
		return nil, "", errors.New("Controller listen address is not assigned to this host")
	}
	return []string{ip.String()}, port, nil
}

func isLocalInterfaceIP(target net.IP) bool {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		value := address.String()
		if host, _, ok := strings.Cut(value, "/"); ok {
			value = host
		}
		if ip := net.ParseIP(value); ip != nil && ip.Equal(target) {
			return true
		}
	}
	return false
}

func normalizeRuntimeBasePath(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return "", true
	}
	if !strings.HasPrefix(value, "/") {
		return "", false
	}
	value = strings.TrimRight(value, "/")
	if len(value) > 128 || strings.ContainsAny(value, "?#%\\") {
		return "", false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
		for _, char := range segment {
			valid := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-._~", char)
			if !valid {
				return "", false
			}
		}
	}
	return value, true
}
