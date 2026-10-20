package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/automation"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

type serverNameConflictError struct {
	Name     string
	Existing []model.Server
}

func (e *serverNameConflictError) Error() string {
	labels := make([]string, 0, len(e.Existing))
	for _, item := range e.Existing {
		labels = append(labels, fmt.Sprintf("%s#%d", item.Name, item.ID))
	}
	return fmt.Sprintf("服务器名称 %q 已存在（%s）。请使用 servers.enrollment.issue 为已有记录重签发接入令牌，或先删除重复记录，不要再次创建", e.Name, strings.Join(labels, ", "))
}

func sameServerName(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func (s *Server) serversNamed(ctx context.Context, name string, excludeID int64) ([]model.Server, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	items, err := s.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	matches := make([]model.Server, 0)
	for _, item := range items {
		if item.ID == excludeID || !sameServerName(item.Name, name) {
			continue
		}
		matches = append(matches, item)
	}
	return matches, nil
}

func (s *Server) authorizedServersNamed(ctx context.Context, principal application.Principal, name string) ([]model.Server, error) {
	matches, err := s.serversNamed(ctx, name, 0)
	if err != nil {
		return nil, err
	}
	allowed := make([]model.Server, 0, len(matches))
	for _, item := range matches {
		if principal.AllowsInt64("server_ids", item.ID) {
			allowed = append(allowed, item)
		}
	}
	return allowed, nil
}

func serverDisplayLabel(item model.Server) string {
	return fmt.Sprintf("%s#%d", item.Name, item.ID)
}

func (s *Server) rejectDuplicateServerName(ctx context.Context, name string, excludeID int64) error {
	matches, err := s.serversNamed(ctx, name, excludeID)
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		return nil
	}
	return &serverNameConflictError{Name: strings.TrimSpace(name), Existing: matches}
}

func (s *Server) issueServerEnrollmentToken(ctx context.Context, serverID int64) (string, time.Time, *model.Server, error) {
	srv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	token, err := security.RandomToken(32)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
	if err := s.store.SetServerEnrollmentHash(ctx, srv.ID, security.HashSecret(token), expiresAt); err != nil {
		return "", time.Time{}, nil, err
	}
	if err := s.pinServerAgentInstallBBR(ctx, *srv); err != nil {
		return "", time.Time{}, nil, err
	}
	updated, err := s.store.GetServer(ctx, srv.ID)
	if err != nil {
		return "", time.Time{}, nil, err
	}
	return token, expiresAt, updated, nil
}

func enrollmentServerView(srv model.Server) map[string]any {
	return map[string]any{
		"id": srv.ID, "name": srv.Name, "stealth_enabled": srv.StealthEnabled,
		"agent_connected": srv.AgentID != "", "status": srv.Status,
	}
}

// agentInstallBBRQdiscs lists the queue disciplines the shell installer may
// pair with BBR. "off" leaves the host's congestion control untouched.
var agentInstallBBRQdiscs = []string{"off", "fq", "cake", "fq_pie"}

const (
	agentInstallBBRDefaultQdisc       = "fq"
	agentInstallCakeBandwidthMaxMbit  = 100000
	settingServerDefaultBBRQdisc      = "server_default_bbr_qdisc"
	settingServerDefaultCakeBandwidth = "server_default_bbr_cake_bandwidth_mbit"
)

func normalizeAgentInstallBBRQdisc(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, qdisc := range agentInstallBBRQdiscs {
		if value == qdisc {
			return value, true
		}
	}
	return "", false
}

func validAgentInstallCakeBandwidth(mbit int) bool {
	return mbit >= 0 && mbit <= agentInstallCakeBandwidthMaxMbit
}

// normalizeServerBBROverride validates a server's BBR install override. An
// empty qdisc follows the panel-wide setting; the bandwidth only survives for
// a cake override.
func normalizeServerBBROverride(v *model.Server) error {
	if strings.TrimSpace(v.BBRQdisc) == "" {
		v.BBRQdisc, v.BBRCakeBandwidthMbit = "", 0
		return nil
	}
	qdisc, ok := normalizeAgentInstallBBRQdisc(v.BBRQdisc)
	if !ok {
		return errors.New("bbr_qdisc must be empty, off, fq, cake, or fq_pie")
	}
	if !validAgentInstallCakeBandwidth(v.BBRCakeBandwidthMbit) {
		return fmt.Errorf("bbr_cake_bandwidth_mbit must be between 0 and %d", agentInstallCakeBandwidthMaxMbit)
	}
	v.BBRQdisc = qdisc
	if qdisc != "cake" {
		v.BBRCakeBandwidthMbit = 0
	}
	return nil
}

// serverAgentInstallBBR applies a server's own BBR override on top of the
// panel-wide qdisc and cake bandwidth.
func serverAgentInstallBBR(srv model.Server, qdisc string, cakeMbit int) (string, int) {
	override, ok := normalizeAgentInstallBBRQdisc(srv.BBRQdisc)
	if !ok {
		return qdisc, cakeMbit
	}
	return override, srv.BBRCakeBandwidthMbit
}

// pinServerAgentInstallBBR fixes the panel-wide BBR choice onto a server that
// still follows the panel when its enrollment token is issued. The install
// command carries that choice as a literal, so the server keeps it after the
// panel setting changes; choosing "follow" again re-pins at the next issue.
func (s *Server) pinServerAgentInstallBBR(ctx context.Context, srv model.Server) error {
	if strings.TrimSpace(srv.BBRQdisc) != "" {
		return nil
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return err
	}
	qdisc, cakeMbit, _ := agentInstallTuningSettings(settings)
	qdisc, cakeValue := agentInstallBBRValues(qdisc, cakeMbit)
	cakeMbit, _ = strconv.Atoi(cakeValue)
	return s.store.PinServerBBRQdisc(ctx, srv.ID, qdisc, cakeMbit)
}

// agentInstallBBRValues renders the explicit BBR qdisc and cake bandwidth
// literals. The bandwidth only matters for cake; other choices always carry 0
// so the installer never shapes traffic for a qdisc that cannot.
func agentInstallBBRValues(qdisc string, cakeMbit int) (string, string) {
	normalized, ok := normalizeAgentInstallBBRQdisc(qdisc)
	if !ok {
		normalized = agentInstallBBRDefaultQdisc
	}
	if normalized != "cake" || !validAgentInstallCakeBandwidth(cakeMbit) {
		cakeMbit = 0
	}
	return normalized, strconv.Itoa(cakeMbit)
}

// agentInstallTCPTuningValue renders the TCP tuning switch for install
// commands. Like BBR, the literal is always explicit (0 or 1) so a stale
// default can never leak through.
func agentInstallTCPTuningValue(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

// agentInstallStealthValue renders the security-process switch for install
// commands. Like BBR, the literal is always explicit (0 or 1) so a stale
// default can never leak through.
func agentInstallStealthValue(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func agentInstallCommand(baseURL, bbrValue, cakeBandwidthValue, tcpTuningValue, stealthValue string) string {
	return "curl -fsSL " + shellSingleQuote(strings.TrimRight(baseURL, "/")+"/install/agent.sh") +
		` | env OBOARD_ENROLL_TOKEN="$OBOARD_ENROLL_TOKEN" OBOARD_INSTALL_BBR=` + shellSingleQuote(bbrValue) +
		` OBOARD_INSTALL_BBR_CAKE_BANDWIDTH=` + shellSingleQuote(cakeBandwidthValue) +
		` OBOARD_INSTALL_TCP_TUNING=` + shellSingleQuote(tcpTuningValue) +
		` OBOARD_INSTALL_STEALTH=` + shellSingleQuote(stealthValue) + " sh"
}

// agentEnrollmentCommand renders the Linux install command. Every issued
// command reads the current panel-wide BBR qdisc and TCP tuning settings; a
// server's own BBR override, pinned when its enrollment token is issued,
// replaces the panel qdisc.
func (s *Server) agentEnrollmentCommand(ctx context.Context, stealth bool, serverID ...int64) (string, map[string]any, error) {
	base, err := s.publicBaseURL(ctx)
	if err != nil {
		return "", nil, err
	}
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		return "", nil, err
	}
	qdisc, cakeMbit, tcpTuning := agentInstallTuningSettings(settings)
	if len(serverID) == 1 && serverID[0] > 0 {
		srv, err := s.store.GetServer(ctx, serverID[0])
		if err != nil {
			return "", nil, err
		}
		qdisc, cakeMbit = serverAgentInstallBBR(*srv, qdisc, cakeMbit)
	}
	bbrValue, cakeBandwidthValue := agentInstallBBRValues(qdisc, cakeMbit)
	tcpTuningValue, stealthValue := agentInstallTCPTuningValue(tcpTuning), agentInstallStealthValue(stealth)
	command := agentInstallCommand(base, bbrValue, cakeBandwidthValue, tcpTuningValue, stealthValue)
	env := map[string]any{"OBOARD_INSTALL_BBR": bbrValue, "OBOARD_INSTALL_BBR_CAKE_BANDWIDTH": cakeBandwidthValue, "OBOARD_INSTALL_TCP_TUNING": tcpTuningValue, "OBOARD_INSTALL_STEALTH": stealthValue}
	if stealth {
		if len(serverID) != 1 {
			return "", nil, fmt.Errorf("server ID is required for security-process layout")
		}
		addr, pin, err := s.agentStealthInstallEnv()
		if err != nil {
			return "", nil, err
		}
		layout, err := s.serverStealthLayout(ctx, serverID[0])
		if err != nil {
			return "", nil, err
		}
		addr = strings.TrimPrefix(addr, "OBOARD_STEALTH_ADDR=")
		pin = strings.TrimPrefix(pin, "OBOARD_STEALTH_PIN=")
		command = strings.TrimSuffix(command, " sh") + " OBOARD_STEALTH_ADDR=" + shellSingleQuote(addr) + " OBOARD_STEALTH_PIN=" + shellSingleQuote(pin) + " OBOARD_STEALTH_LAYOUT=" + shellSingleQuote(layout) + " sh"
		env["OBOARD_STEALTH_ADDR"], env["OBOARD_STEALTH_PIN"], env["OBOARD_STEALTH_LAYOUT"] = addr, pin, layout
	}
	return command, env, nil
}

func (s *Server) agentStealthUpdateCommand(ctx context.Context, serverID int64) (string, error) {
	return s.agentStealthScriptCommand(ctx, serverID, "update")
}

func (s *Server) agentStealthScriptCommand(ctx context.Context, serverID int64, action string) (string, error) {
	base, err := s.publicBaseURL(ctx)
	if err != nil {
		return "", err
	}
	layout, err := s.serverStealthLayout(ctx, serverID)
	if err != nil {
		return "", err
	}
	if layout == "" {
		return "", nil
	}
	return "curl -fsSL " + shellSingleQuote(strings.TrimRight(base, "/")+"/install/agent.sh") + " | env OBOARD_ACTION=" + shellSingleQuote(action) + " OBOARD_INSTALL_STEALTH=1 OBOARD_STEALTH_LAYOUT=" + shellSingleQuote(layout) + " sh", nil
}

// agentStealthInstallEnv renders the transport parameters appended to the
// stealth install command: the dedicated listener address and its certificate
// pin, both from the running Controller configuration.
func (s *Server) agentStealthInstallEnv() (addrEnv, pinEnv string, err error) {
	addr, pin, enabled := s.StealthTransportInfo()
	if !enabled {
		return "", "", errors.New("安全进程传输端口未启用：请先在系统设置 → 面板访问中启用安全进程传输")
	}
	return "OBOARD_STEALTH_ADDR=" + addr, "OBOARD_STEALTH_PIN=" + pin, nil
}

func (s *Server) registerServerLifecycleOperations() {
	s.automation.RegisterValidator("servers.enrollment.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		srv, err := s.serverEnrollmentIssueCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server_id": srv.ID, "name": srv.Name}, nil
	})
	s.automation.RegisterRevisionResolver("servers.enrollment.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		srv, err := s.serverEnrollmentIssueCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(srv.ID, 10): srv.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.enrollment.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		current, err := s.serverEnrollmentIssueCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		token, expiresAt, srv, err := s.issueServerEnrollmentToken(ctx, current.ID)
		if err != nil {
			return nil, err
		}
		view := enrollmentServerView(*srv)
		public := map[string]any{"server": view, "enrollment_expires_at": expiresAt}
		oneTime := map[string]any{"server": view, "enrollment_expires_at": expiresAt, "enrollment_token": token}
		if srv.StealthEnabled || serverSupportsCapability(*srv, "stealth_active_v1") {
			update, err := s.agentStealthUpdateCommand(ctx, srv.ID)
			if err != nil {
				return nil, err
			}
			if update != "" {
				oneTime["update_command"] = update
			}
			uninstall, err := s.agentStealthScriptCommand(ctx, srv.ID, "uninstall")
			if err != nil {
				return nil, err
			}
			if uninstall != "" {
				oneTime["uninstall_command"] = uninstall
			}
		}
		return automation.MutationResult{Public: public, OneTime: oneTime}, nil
	})

	s.automation.RegisterValidator("servers.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		srv, err := s.serverDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server_id": srv.ID, "name": srv.Name, "agent_connected": srv.AgentID != ""}, nil
	})
	s.automation.RegisterRevisionResolver("servers.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		srv, err := s.serverDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"server:" + strconv.FormatInt(srv.ID, 10): srv.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("servers.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		srv, err := s.serverDeleteCandidate(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		if _, err := s.deleteServerRecord(ctx, srv.ID, principal.UserID, "mcp"); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "server_id": srv.ID}, nil
	})
}

func (s *Server) serverEnrollmentIssueCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.Server, error) {
	var request struct {
		ServerID int64 `json:"server_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.Server{}, err
	}
	if request.ServerID <= 0 || !principal.AllowsInt64("server_ids", request.ServerID) {
		return model.Server{}, errors.New("authorized server_id is required")
	}
	srv, err := s.store.GetServer(ctx, request.ServerID)
	if err != nil {
		return model.Server{}, err
	}
	return *srv, nil
}

func (s *Server) serverDeleteCandidate(ctx context.Context, principal application.Principal, input json.RawMessage) (model.Server, error) {
	var request struct {
		ServerID int64 `json:"server_id"`
		Confirm  bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return model.Server{}, err
	}
	if request.ServerID <= 0 || !request.Confirm {
		return model.Server{}, errors.New("server_id and confirm=true are required")
	}
	if !principal.AllowsInt64("server_ids", request.ServerID) {
		return model.Server{}, errors.New("authorized server_id is required")
	}
	srv, err := s.store.GetServer(ctx, request.ServerID)
	if err != nil {
		return model.Server{}, err
	}
	return *srv, nil
}
