package controller

import (
	"net/http"

	"github.com/OboardProject/oboard/internal/core"
)

// shadowReport serves GET /api/v1/shadow-report. It computes the full legacy
// vs plan comparison: per-user node sets, per-server authentication users,
// SSH users and effective speed/traffic policies. It never changes runtime
// behavior and is useful in every authorization mode for migration planning.
func (s *Server) shadowReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	snap, err := s.buildAccessSnapshot(r.Context(), data)
	if err != nil {
		fail(w, err, 500)
		return
	}
	legacy := core.LegacyAccessInput{
		Inbounds:                     data.Inbounds,
		InboundUsers:                 data.InboundUsers,
		UserGroups:                   data.UserGroups,
		UserGroupMembers:             data.UserGroupMembers,
		InboundAccessGrants:          data.InboundAccessGrants,
		ExternalOutbounds:            data.ExternalOutbounds,
		ExternalOutboundAccessGrants: data.ExternalOutboundAccessGrants,
		Paths:                        data.ProxyPaths,
		Steps:                        data.ProxyPathSteps,
	}
	comparison := core.CompareLegacyAndPlanAccess(data.Users, legacy, snap, 10)
	usernames := map[int64]string{}
	for _, user := range data.Users {
		usernames[user.ID] = user.Username
	}
	comparison.ServerDivergences = core.CompareServerAuthUserSets(data.Servers, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, usernames,
		effectiveInboundUsersForRouting(data), effectiveProxyPathUsersForRouting(data),
		snap.InboundUserBindings(), snap.ProxyPathUserBindings(), 20)
	comparison.SSHDivergences = core.CompareSSHUserSets(data.ProxyPaths, data.Inbounds,
		effectiveProxyPathUsersForRouting(data), snap.ProxyPathUserBindings(), usernames, 20)
	comparison.PolicyDivergences = core.CompareUserPolicies(data.Users, data.UserGroups, data.UserGroupMembers, snap.UserLimitPolicyMap(), 20)
	write(w, 200, map[string]any{
		"shadow":                     comparison,
		"runtime_authorization_mode": s.authorizationMode(r.Context()),
	})
}
