package controller

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
)

// The Agent validates every SSH inbound user before it applies a deployment and
// rejects the whole plan when one user violates the contract, which takes down
// every other account on that server. These patterns mirror the Agent's
// validateSSHInboundPlan exactly; the two sides implement one wire contract and
// must be changed together.
var (
	sshPlanDeviceIDHashPattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	sshPlanRouteInboundPattern  = regexp.MustCompile(`^in-[1-9][0-9]*$`)
	sshPlanRouteAuthUserPattern = regexp.MustCompile(`^u[0-9a-f]{32}$`)
)

var sshPlanCredentialStates = map[string]bool{"active": true, "reject_new": true, "disconnect_and_reject": true, "revoked": true, "disabled": true}

// sshInboundPlanRejection records one authorized account that could not be
// projected into a server's SSH plan, so the omission is reportable instead of
// silent.
type sshInboundPlanRejection struct {
	InboundID int64
	UserID    int64
	Reason    string
}

// sshInboundPlanResult carries the deployable plan together with the accounts
// left out of it.
type sshInboundPlanResult struct {
	Plan       model.SSHInboundPlan
	Rejections []sshInboundPlanRejection
}

// sshInboundUserContractViolation reports why the Agent would refuse this user,
// or an empty string when the user satisfies the contract.
func sshInboundUserContractViolation(user model.SSHInboundUser) string {
	if user.UserID <= 0 || !validSSHPlanUsername(user.Username) {
		return "invalid_username"
	}
	if strings.TrimSpace(user.Password) == "" {
		return "missing_password"
	}
	deviceIDHash := strings.TrimSpace(user.DeviceIDHash)
	if deviceIDHash == "" {
		if user.CredentialEpoch != 0 {
			return "incoherent_device_identity"
		}
	} else if !sshPlanDeviceIDHashPattern.MatchString(deviceIDHash) || user.CredentialEpoch <= 0 {
		return "incoherent_device_identity"
	}
	status := strings.TrimSpace(user.CredentialStatus)
	if status == "" {
		status = "active"
	}
	if !sshPlanCredentialStates[status] {
		return "invalid_credential_status"
	}
	if user.PathID <= 0 {
		return "missing_path_id"
	}
	if user.RouteKind != "kernel" || strings.TrimSpace(user.OutboundTag) != "" {
		return "invalid_route_kind"
	}
	if !sshPlanRouteInboundPattern.MatchString(strings.TrimSpace(user.RouteInboundTag)) {
		return "invalid_route_inbound_tag"
	}
	if !sshPlanRouteAuthUserPattern.MatchString(user.RouteAuthUser) || user.RouteAuthUser != user.Username || len(user.AuthorizationKey) != 32 {
		return "invalid_route_auth_user"
	}
	return ""
}

// validSSHPlanUsername mirrors the Agent's validSSHInboundUsername.
func validSSHPlanUsername(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

// sanitizeSSHInboundUsers keeps only the users the Agent will accept on one
// inbound. A violating user is dropped rather than allowed to fail the whole
// deployment; the caller reports the omission. Inbound-level structure is left
// alone because dropping a listener would change the deployed listener digest
// and silently mark converged servers as drifted.
func sanitizeSSHInboundUsers(inbound model.SSHInbound) ([]model.SSHInboundUser, []sshInboundPlanRejection) {
	kept := make([]model.SSHInboundUser, 0, len(inbound.Users))
	rejections := []sshInboundPlanRejection{}
	seen := map[string]bool{}
	for _, user := range inbound.Users {
		if !user.Enabled {
			kept = append(kept, user)
			continue
		}
		if reason := sshInboundUserContractViolation(user); reason != "" {
			rejections = append(rejections, sshInboundPlanRejection{InboundID: inbound.InboundID, UserID: user.UserID, Reason: reason})
			continue
		}
		if seen[user.Username] {
			rejections = append(rejections, sshInboundPlanRejection{InboundID: inbound.InboundID, UserID: user.UserID, Reason: "duplicate_username"})
			continue
		}
		seen[user.Username] = true
		kept = append(kept, user)
	}
	return kept, rejections
}

// describeSSHInboundRejections renders a bounded, credential-free summary for
// operator-facing status and logs.
func describeSSHInboundRejections(rejections []sshInboundPlanRejection) string {
	if len(rejections) == 0 {
		return ""
	}
	reasons := map[string]int{}
	for _, rejection := range rejections {
		reasons[rejection.Reason]++
	}
	parts := make([]string, 0, len(reasons))
	for _, reason := range sortedRejectionReasons(reasons) {
		parts = append(parts, fmt.Sprintf("%s=%d", reason, reasons[reason]))
	}
	return strings.Join(parts, ",")
}

func sortedRejectionReasons(reasons map[string]int) []string {
	out := make([]string, 0, len(reasons))
	for reason := range reasons {
		out = append(out, reason)
	}
	sort.Strings(out)
	return out
}
