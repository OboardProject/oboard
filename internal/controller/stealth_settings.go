package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OboardProject/oboard/internal/agentlink"
)

const settingStealthTransport = "stealth_transport"

type stealthTransportConfig struct {
	Enabled       bool   `json:"enabled"`
	ListenAddress string `json:"listen_address"`
	PublicAddress string `json:"public_address"`
}

func decodeStealthConfig(raw json.RawMessage) (stealthTransportConfig, error) {
	var cfg stealthTransportConfig
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return cfg, err
	}
	for _, key := range []string{"enabled", "listen_address", "public_address"} {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return cfg, fmt.Errorf("stealth_transport.%s is required", key)
		}
	}
	if err := strictAutomationInput(raw, &cfg); err != nil {
		return cfg, err
	}
	return normalizeStealthConfig(cfg)
}

func normalizeStealthEndpoint(raw string, listen bool) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("请填写地址:端口；IPv6 使用 [地址]:端口")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("端口必须为 1–65535")
	}
	if listen {
		if host == "" {
			host = "0.0.0.0"
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || ip.Zone() != "" || ip.IsMulticast() {
			return "", errors.New("监听地址必须使用本机 IP 或 0.0.0.0 / ::")
		}
		host = ip.String()
	} else {
		if ip, err := netip.ParseAddr(host); err == nil {
			if ip.IsUnspecified() || ip.IsMulticast() || ip.Zone() != "" {
				return "", errors.New("Agent 连接地址不能使用通配或组播地址")
			}
			host = ip.String()
		} else {
			host = strings.ToLower(host)
			if host == "" || len(host) > 253 {
				return "", errors.New("Agent 连接地址必须是 IP 或域名")
			}
			for _, label := range strings.Split(host, ".") {
				if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
					return "", errors.New("Agent 连接域名无效")
				}
				for _, c := range label {
					if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
						return "", errors.New("Agent 连接地址必须是 IP 或域名，不含协议、路径或空格")
					}
				}
			}
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(n)), nil
}

func normalizeStealthConfig(cfg stealthTransportConfig) (stealthTransportConfig, error) {
	cfg.ListenAddress = strings.TrimSpace(cfg.ListenAddress)
	cfg.PublicAddress = strings.TrimSpace(cfg.PublicAddress)
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = "0.0.0.0:24443"
	}
	var err error
	cfg.ListenAddress, err = normalizeStealthEndpoint(cfg.ListenAddress, true)
	if err != nil {
		return cfg, fmt.Errorf("安全进程监听地址：%w", err)
	}
	if cfg.Enabled || cfg.PublicAddress != "" {
		cfg.PublicAddress, err = normalizeStealthEndpoint(cfg.PublicAddress, false)
		if err != nil {
			return cfg, fmt.Errorf("安全进程 Agent 连接地址：%w", err)
		}
	}
	return cfg, nil
}

func stealthConfigFromSettings(items map[string]string) (stealthTransportConfig, error) {
	if raw, ok := items[settingStealthTransport]; ok {
		var cfg stealthTransportConfig
		if err := strictAutomationInput(json.RawMessage(raw), &cfg); err != nil {
			return cfg, err
		}
		return normalizeStealthConfig(cfg)
	}
	addr := strings.TrimSpace(os.Getenv("OBOARD_STEALTH_ADDR"))
	return normalizeStealthConfig(stealthTransportConfig{Enabled: addr != "", ListenAddress: addr, PublicAddress: addr})
}

func (s *Server) ConfigureStealthTransport(dbPath string) {
	s.stealthMu.Lock()
	defer s.stealthMu.Unlock()
	s.stealthCertPath = envOrDefault("OBOARD_STEALTH_CERT", filepath.Join(filepath.Dir(dbPath), "stealth-agent-cert.pem"))
}

func (s *Server) StartStealthTransport(ctx context.Context) error {
	s.stealthMu.Lock()
	defer s.stealthMu.Unlock()
	s.stealthContext = ctx
	items, err := s.store.ListSettings(ctx)
	if err == nil {
		var cfg stealthTransportConfig
		cfg, err = stealthConfigFromSettings(items)
		if err == nil {
			err = s.applyStealthConfigLocked(ctx, cfg, false)
		}
	}
	if err != nil {
		s.stealthError = err.Error()
	}
	return err
}

func stopStealthTransport(current *stealthTransport) {
	if current == nil {
		return
	}
	if current.done != nil {
		close(current.done)
	}
	if current.server != nil {
		current.server.Close()
	}
	if current.listener != nil {
		_ = current.listener.Close()
	}
}

func (s *Server) closeStealthTransport() {
	s.stealthMu.Lock()
	defer s.stealthMu.Unlock()
	stopStealthTransport(s.stealthTransport.Swap(nil))
}

func (s *Server) validateStealthChange(ctx context.Context, cfg stealthTransportConfig) error {
	current := s.stealthTransport.Load()
	if current != nil && cfg.Enabled && current.listenAddr == cfg.ListenAddress && current.addr == cfg.PublicAddress {
		return nil
	}
	items, err := s.store.ListSettings(ctx)
	if err != nil {
		return err
	}
	previous, err := stealthConfigFromSettings(items)
	if err != nil {
		return err
	}
	if !previous.Enabled || cfg == previous {
		return nil
	}
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return err
	}
	for _, server := range servers {
		if server.AgentID != "" && (server.StealthEnabled || serverSupportsCapability(server, "stealth_active_v1")) {
			return fmt.Errorf("服务器 %s 仍使用安全进程；请先关闭该服务器的安全进程并确认 Agent 已恢复普通连接，再修改或关闭主控安全传输", server.Name)
		}
	}
	return nil
}

func (s *Server) updateStealthConfig(ctx context.Context, cfg stealthTransportConfig, apply bool) error {
	normalized, err := normalizeStealthConfig(cfg)
	if err != nil {
		return err
	}
	s.stealthMu.Lock()
	defer s.stealthMu.Unlock()
	if err := s.validateStealthChange(ctx, normalized); err != nil {
		return err
	}
	if !apply {
		return nil
	}
	return s.applyStealthConfigLocked(ctx, normalized, true)
}

// Bind before saving so a port conflict cannot replace the working configuration.
func (s *Server) applyStealthConfigLocked(ctx context.Context, cfg stealthTransportConfig, persist bool) error {
	current := s.stealthTransport.Load()
	candidate := current
	if !cfg.Enabled {
		candidate = nil
	} else if current == nil || current.listenAddr != cfg.ListenAddress {
		if s.stealthCertPath == "" || s.stealthContext == nil || s.stealthContext.Err() != nil {
			return errors.New("安全传输运行环境尚未初始化")
		}
		pair, pin, err := loadOrCreateStealthCert(s.stealthCertPath, s.stealthCertPath+".key")
		if err != nil {
			return fmt.Errorf("安全传输证书：%w", err)
		}
		transport := agentlink.NewServer(agentlink.ServerConfig{Addr: cfg.ListenAddress, Certificate: pair})
		listener, err := transport.Listen()
		if err != nil {
			return fmt.Errorf("无法监听安全传输端口：%w", err)
		}
		candidate = &stealthTransport{server: transport, listener: listener, pin: pin, addr: cfg.PublicAddress, listenAddr: cfg.ListenAddress, done: make(chan struct{})}
	}
	if persist {
		raw, _ := json.Marshal(cfg)
		if err := s.store.SetSetting(ctx, settingStealthTransport, string(raw)); err != nil {
			if candidate != current {
				stopStealthTransport(candidate)
			}
			return err
		}
	}
	if candidate == current && candidate != nil && candidate.addr != cfg.PublicAddress {
		copy := *candidate
		copy.addr = cfg.PublicAddress
		candidate = &copy
		s.stealthTransport.Store(candidate)
	} else if candidate != current {
		s.stealthTransport.Store(candidate)
		stopStealthTransport(current)
		if candidate != nil {
			runtimeCtx := s.stealthContext
			go func(transport *stealthTransport) {
				select {
				case <-runtimeCtx.Done():
					s.closeStealthTransport()
				case <-transport.done:
				}
			}(candidate)
			go func(transport *stealthTransport) {
				if err := transport.server.Serve(transport.listener, s.handleStealthConnection); err != nil {
					s.stealthMu.Lock()
					if active := s.stealthTransport.Load(); active != nil && active.server == transport.server {
						s.stealthError = err.Error()
						stopStealthTransport(s.stealthTransport.Swap(nil))
					}
					s.stealthMu.Unlock()
					log.Printf("stealth agent transport stopped: %v", err)
				}
			}(candidate)
		}
	}
	s.stealthError = ""
	s.invalidateSettingsSnapshot()
	return nil
}

func (s *Server) publicStealthSettings(items map[string]string) map[string]any {
	cfg, err := stealthConfigFromSettings(items)
	s.stealthMu.Lock()
	defer s.stealthMu.Unlock()
	message := s.stealthError
	if err != nil {
		message = err.Error()
	}
	active := s.stealthTransport.Load()
	source := "environment"
	if _, ok := items[settingStealthTransport]; ok {
		source = "settings"
	}
	return map[string]any{"enabled": cfg.Enabled, "listen_address": cfg.ListenAddress, "public_address": cfg.PublicAddress, "active": active != nil, "error": message, "source": source}
}
