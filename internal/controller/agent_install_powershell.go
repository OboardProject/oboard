package controller

import (
	_ "embed"
	"net/http"
	"strings"

	"github.com/OboardProject/oboard/internal/version"
)

// agentInstallPowerShellTemplate is the Windows Agent installer served at
// /install/agent.ps1. It installs, updates, and uninstalls the Agent, the
// kernel, and realm as Windows services and verifies the signed release
// manifest itself.
//
//go:embed assets/install-agent.ps1
var agentInstallPowerShellTemplate string

// powershellSingleQuote renders value as a PowerShell single-quoted literal,
// where the only escape is a doubled single quote.
func powershellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (s *Server) agentInstallPowerShell(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		method(w)
		return
	}
	baseURL, err := s.publicBaseURL(r.Context())
	if err != nil {
		fail(w, err, http.StatusPreconditionFailed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	script := strings.ReplaceAll(agentInstallPowerShellTemplate, "__BASE_URL__", powershellSingleQuote(strings.TrimRight(baseURL, "/")))
	script = strings.ReplaceAll(script, "__RELEASE_PUBLIC_KEY__", powershellSingleQuote(version.ReleasePublicKey))
	_, _ = w.Write([]byte(script))
}

// agentWindowsInstallCommand renders the command an operator pastes into an
// elevated Windows PowerShell. The script is decoded as UTF-8 explicitly
// because WebClient otherwise falls back to the system ANSI code page, and it
// runs as a script block so a failure never closes the operator's window. The
// enrollment token is read from OBOARD_ENROLL_TOKEN in the session, which the
// installer clears as soon as it has read it.
func agentWindowsInstallCommand(baseURL string) string {
	url := strings.TrimRight(baseURL, "/") + "/install/agent.ps1"
	return "[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12; " +
		"$oboardClient = New-Object Net.WebClient; $oboardClient.Encoding = [Text.Encoding]::UTF8; " +
		"& ([scriptblock]::Create($oboardClient.DownloadString(" + powershellSingleQuote(url) + ")))"
}

// agentWindowsInstallCommandWithToken prefixes the one-time enrollment token.
func agentWindowsInstallCommandWithToken(baseURL, token string) string {
	return "$env:OBOARD_ENROLL_TOKEN = " + powershellSingleQuote(token) + "; " + agentWindowsInstallCommand(baseURL)
}
