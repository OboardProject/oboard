package controller

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/application"
	"github.com/OboardProject/oboard/internal/core"
	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/store"
)

type agentTrafficReportEnvelope struct {
	AgentInstanceID string                           `json:"agent_instance_id"`
	PeriodKey       string                           `json:"period_key"`
	Items           json.RawMessage                  `json:"items"`
	Streams         []model.TrafficStreamObservation `json:"streams"`
	Reports         []agentTrafficRangeItem          `json:"reports"`
}

type agentTrafficRangeItem struct {
	ReportID     string `json:"report_id"`
	Source       string `json:"source"`
	StreamID     string `json:"stream_id"`
	CounterEpoch string `json:"counter_epoch"`
	PeriodKey    string `json:"period_key"`
	UserID       int64  `json:"user_id"`
	InboundID    *int64 `json:"inbound_id"`
	PathID       *int64 `json:"path_id"`
	FromUpload   int64  `json:"from_upload_bytes"`
	ToUpload     int64  `json:"to_upload_bytes"`
	FromDownload int64  `json:"from_download_bytes"`
	ToDownload   int64  `json:"to_download_bytes"`
	StartedAt    string `json:"started_at"`
	EndedAt      string `json:"ended_at"`
}

func (s *Server) handleAgentTrafficLedger(w http.ResponseWriter, r *http.Request, server *model.Server, req agentTrafficReportEnvelope) {
	s.trafficReportsReceivedTotal.Add(1)
	if len(req.Streams) > 1000 {
		fail(w, errors.New("too many traffic streams in one report"), 400)
		return
	}
	if len(req.Reports) > 500 {
		fail(w, errors.New("too many traffic items in one report"), 400)
		return
	}
	settings, _ := s.store.ListSettings(r.Context())
	loc := trafficLocation(settings)
	data, err := s.store.FullRoutingConfigData(r.Context())
	if err != nil {
		fail(w, err, 500)
		return
	}
	snapshot, err := s.buildAccessSnapshot(r.Context(), data)
	if err != nil {
		fail(w, err, 500)
		return
	}
	planPolicies := snapshot.UserLimitPolicyMap()
	access, err := newTrafficReportAccess(server, data, snapshot)
	if err != nil {
		fail(w, err, 500)
		return
	}
	periods := map[int64]model.TrafficPeriod{}
	reports := make([]model.TrafficReport, 0, len(req.Reports))
	// Reports whose user, binding, inbound, or path is gone can never be
	// accounted. They are answered as terminally rejected so the Agent drops
	// them locally instead of resending the same failing batch forever.
	rejected := make([]model.TrafficAcceptedReport, 0)
	for _, item := range req.Reports {
		report, period, err := s.validateAgentTrafficRangeItem(r, server, item, req.PeriodKey, access, planPolicies, loc)
		if err != nil {
			var rejection *trafficRejection
			if errors.As(err, &rejection) {
				// An unbound pair is the one rejection that is also the signal
				// a misbehaving Agent would produce. A removed binding yields a
				// bounded burst that drains; a sustained stream from one Agent
				// does not, so the pair is logged rather than folded into the
				// aggregate line below.
				if rejection.Reason == "binding_removed" {
					log.Printf("traffic ledger rejected an unbound pair agent=%s server_id=%d user_id=%d inbound_id=%v report_id=%s",
						server.AgentID, server.ID, item.UserID, item.InboundID, strings.TrimSpace(item.ReportID))
				}
				rejected = append(rejected, model.TrafficAcceptedReport{ReportID: strings.TrimSpace(item.ReportID), Status: "rejected", Reason: rejection.Reason})
				continue
			}
			fail(w, err, trafficReportFailureStatus(err))
			return
		}
		reports = append(reports, report)
		periods[report.UserID] = period
	}
	streams := make([]model.TrafficStreamObservation, 0, len(req.Streams))
	for _, stream := range req.Streams {
		if err := access.validateStream(stream); err != nil {
			var rejection *trafficRejection
			if errors.As(err, &rejection) {
				// The stream's owner is gone; skip the observation instead of
				// failing the accounting batch it travels with.
				continue
			}
			fail(w, err, trafficReportFailureStatus(err))
			return
		}
		streams = append(streams, stream)
		if _, ok := periods[stream.UserID]; !ok {
			if period, periodErr := s.trafficPeriodForUser(r, stream.UserID, stream.PeriodKey, planPolicies, loc); periodErr == nil {
				periods[stream.UserID] = period
			}
		}
	}
	result, err := s.store.CommitTrafficLedger(r.Context(), store.TrafficLedgerCommit{
		ServerID: server.ID, AgentInstanceID: strings.TrimSpace(req.AgentInstanceID), Periods: periods, Streams: streams, Reports: reports,
	})
	if err != nil {
		fail(w, err, 500)
		return
	}
	// Traffic reports are accounting + runtime policy reconciliation only.
	// They must never call NextConfigVersion, MarkConfigurationSyncPending,
	// markConfigurationRevision, apply_deployment, or apply_core_config.
	for _, accepted := range result.AcceptedReports {
		if accepted.Status == "accepted" {
			s.trafficReportsAcceptedTotal.Add(1)
		}
	}
	for _, accepted := range result.AcceptedReports {
		if accepted.Status != "accepted" {
			continue
		}
		if u, ok := access.userByID[userIDFromAccepted(accepted, reports)]; ok {
			if period, err := s.store.GetTrafficPeriod(r.Context(), u.ID, accepted.PeriodKey); err == nil {
				s.notifyTrafficQuotaExceeded(r.Context(), u, period)
			}
		}
	}
	accountingUsers := core.TrafficAccountingUsersForServer(server.ID, data.ProxyPaths, data.ProxyPathSteps, data.Inbounds, snapshot.InboundUserBindings(), snapshot.ProxyPathUserBindings())
	policies, err := s.trafficRuntimePolicies(r.Context(), server.ID, data.Users, accountingUsers, planPolicies)
	if err != nil {
		fail(w, err, 500)
		return
	}
	s.trafficPolicyRuntimeAppliesTotal.Add(1)
	acceptedReports := result.AcceptedReports
	if len(rejected) > 0 {
		s.trafficReportsRejectedTotal.Add(uint64(len(rejected)))
		log.Printf("traffic ledger rejected %d unaccountable report(s) from agent=%s first_reason=%s", len(rejected), server.AgentID, rejected[0].Reason)
		acceptedReports = append(append(make([]model.TrafficAcceptedReport, 0, len(acceptedReports)+len(rejected)), acceptedReports...), rejected...)
	}
	revision, _ := s.store.TrafficPolicyRevision(r.Context())
	write(w, 200, map[string]any{
		"ok":                  true,
		"policy_revision":     revision,
		"stream_checkpoints":  result.StreamCheckpoints,
		"accepted_reports":    acceptedReports,
		"accepted_report_ids": acceptedReportIDs(acceptedReports),
		"policies":            policies,
	})
}

var errTrafficForbidden = errors.New("inbound does not belong to this agent")

// trafficReportFailureStatus keeps cross-tenant ownership at the request level.
//
// A report against another server's inbound or path is the one authorization
// boundary a correct Agent can never produce: it is only ever given the
// configuration for its own server. Failing the whole request there is
// fail-closed and cannot stall a healthy node, so it stays a 403.
//
// The narrower boundary - this server's own live inbound and live user with no
// current binding - is answered per report instead; see validateIdentity.
func trafficReportFailureStatus(err error) int {
	if errors.Is(err, errTrafficForbidden) {
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

// trafficRejection marks a report that this Controller will never accept
// because the entity it accounts against is gone or disabled. Failing the whole
// request for these would make the Agent retry the same batch forever, so they
// are answered per report with a terminal status instead.
type trafficRejection struct {
	Reason  string
	Message string
}

func (e *trafficRejection) Error() string { return e.Message }

func trafficReject(reason, message string) error {
	return &trafficRejection{Reason: reason, Message: message}
}

type trafficReportAccess struct {
	server      *model.Server
	data        store.FullRoutingConfig
	userByID    map[int64]model.User
	inboundByID map[int64]model.Inbound
	pathByID    map[int64]model.ProxyPath
	allowed     map[trafficAccessPair]struct{}
}

type trafficAccessPair struct{ inboundID, userID, pathID int64 }

func newTrafficReportAccess(server *model.Server, data store.FullRoutingConfig, snapshot *core.EffectiveAccessSnapshot) (trafficReportAccess, error) {
	access := trafficReportAccess{server: server, data: data, userByID: map[int64]model.User{}, inboundByID: map[int64]model.Inbound{}, pathByID: map[int64]model.ProxyPath{}, allowed: map[trafficAccessPair]struct{}{}}
	for _, u := range data.Users {
		access.userByID[u.ID] = u
	}
	for _, inbound := range data.Inbounds {
		access.inboundByID[inbound.ID] = inbound
	}
	for _, path := range data.ProxyPaths {
		access.pathByID[path.ID] = path
	}
	for _, binding := range snapshot.InboundUserBindings() {
		if binding.Enabled {
			access.allowed[trafficAccessPair{inboundID: binding.InboundID, userID: binding.UserID}] = struct{}{}
		}
	}
	for _, binding := range snapshot.ProxyPathUserBindings() {
		if binding.Enabled {
			access.allowed[trafficAccessPair{inboundID: binding.InboundID, userID: binding.UserID, pathID: binding.ProxyPathID}] = struct{}{}
		}
	}
	return access, nil
}

// validateIdentity separates three outcomes. A malformed report is a client
// error, a claim against another server's inbound or path is a security
// violation, and an entity that has been deleted or disabled is terminal for
// that single report only.
func (a trafficReportAccess) validateIdentity(userID int64, inboundID *int64, pathID *int64) error {
	if inboundID == nil {
		return errors.New("traffic report must identify an inbound")
	}
	inbound, ok := a.inboundByID[*inboundID]
	if !ok {
		return trafficReject("inbound_deleted", "traffic report inbound no longer exists")
	}
	if !inbound.Enabled {
		return trafficReject("inbound_disabled", "traffic report inbound is disabled")
	}
	accountingLocation := inbound.ServerID == a.server.ID
	if pathID != nil {
		if *pathID <= 0 {
			return errors.New("traffic report path_id must be positive")
		}
		if _, exists := a.pathByID[*pathID]; !exists {
			return trafficReject("path_removed", "traffic report proxy path no longer exists")
		}
		accountingLocation = core.IsProxyPathAccountingLocation(a.server.ID, inbound.ID, *pathID, a.data.ProxyPaths, a.data.ProxyPathSteps, a.data.Inbounds)
	} else if core.ProxyPathRequiresAccountingPathID(inbound.ID, a.data.ProxyPaths, a.data.ProxyPathSteps, a.data.Inbounds) {
		return errors.New("traffic report must identify the transparent proxy path")
	}
	if !accountingLocation {
		return errTrafficForbidden
	}
	u, ok := a.userByID[userID]
	if !ok {
		return trafficReject("user_deleted", "traffic report user no longer exists")
	}
	if u.Status != "active" {
		return trafficReject("user_inactive", "traffic report user is not active")
	}
	resolvedPath := int64(0)
	if pathID != nil {
		resolvedPath = *pathID
	}
	// Reaching this point means the report is for this server's own live
	// inbound and a live active user: cross-tenant ownership was refused above
	// by errTrafficForbidden, and a deleted or disabled subject was answered
	// per report. The only thing left is a binding this Controller removed
	// while the Agent still held traffic for it, so it is terminal for this one
	// report rather than a reason to fail the batch - failing it would stall
	// every other report and the policy response that renews traffic leases,
	// until every lease on the server ran out.
	if _, ok := a.allowed[trafficAccessPair{inboundID: *inboundID, userID: userID, pathID: resolvedPath}]; !ok {
		return trafficReject("binding_removed", "traffic report user is not bound to this inbound")
	}
	return nil
}

func (a trafficReportAccess) validateStream(stream model.TrafficStreamObservation) error {
	if stream.UserID <= 0 || strings.TrimSpace(stream.StreamID) == "" || strings.TrimSpace(stream.CounterEpoch) == "" {
		return errors.New("traffic stream is invalid")
	}
	source := strings.TrimSpace(stream.Source)
	if source != "core" && source != "ssh" {
		return errors.New("traffic stream source is invalid")
	}
	if stream.InboundID > 0 {
		inboundID := stream.InboundID
		var pathID *int64
		if stream.PathID > 0 {
			pathID = &stream.PathID
		}
		return a.validateIdentity(stream.UserID, &inboundID, pathID)
	}
	u, ok := a.userByID[stream.UserID]
	if !ok {
		return trafficReject("user_deleted", "traffic stream user no longer exists")
	}
	if u.Status != "active" {
		return trafficReject("user_inactive", "traffic stream user is not active")
	}
	return nil
}

func (s *Server) validateAgentTrafficRangeItem(r *http.Request, server *model.Server, item agentTrafficRangeItem, requestPeriod string, access trafficReportAccess, planPolicies map[int64]core.UserLimitPolicy, loc *time.Location) (model.TrafficReport, model.TrafficPeriod, error) {
	if item.UserID <= 0 || strings.TrimSpace(item.ReportID) == "" || strings.TrimSpace(item.StreamID) == "" || strings.TrimSpace(item.CounterEpoch) == "" {
		return model.TrafficReport{}, model.TrafficPeriod{}, errors.New("traffic report is invalid")
	}
	if item.ToUpload < item.FromUpload || item.ToDownload < item.FromDownload || item.FromUpload < 0 || item.FromDownload < 0 {
		return model.TrafficReport{}, model.TrafficPeriod{}, errors.New("traffic report is invalid")
	}
	source := strings.TrimSpace(item.Source)
	if source != "core" && source != "ssh" {
		return model.TrafficReport{}, model.TrafficPeriod{}, errors.New("traffic report source is invalid")
	}
	if err := access.validateIdentity(item.UserID, item.InboundID, item.PathID); err != nil {
		return model.TrafficReport{}, model.TrafficPeriod{}, err
	}
	reportedPeriodKey := strings.TrimSpace(item.PeriodKey)
	if reportedPeriodKey == "" {
		reportedPeriodKey = strings.TrimSpace(requestPeriod)
	}
	period, err := s.trafficPeriodForUser(r, item.UserID, reportedPeriodKey, planPolicies, loc)
	if err != nil {
		return model.TrafficReport{}, model.TrafficPeriod{}, err
	}
	report := model.TrafficReport{
		ReportID: item.ReportID, ServerID: server.ID, UserID: item.UserID, InboundID: item.InboundID, PathID: item.PathID,
		PeriodKey: period.PeriodKey, StartedAt: parseReportTime(item.StartedAt), EndedAt: parseReportTime(item.EndedAt),
		CounterSource: source, StreamID: item.StreamID, CounterEpoch: item.CounterEpoch,
		FromUploadBytes: item.FromUpload, ToUploadBytes: item.ToUpload, FromDownloadBytes: item.FromDownload, ToDownloadBytes: item.ToDownload,
	}
	return report, period, nil
}

func (s *Server) trafficPeriodForUser(r *http.Request, userID int64, reportedPeriodKey string, planPolicies map[int64]core.UserLimitPolicy, loc *time.Location) (model.TrafficPeriod, error) {
	u, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		return model.TrafficPeriod{}, err
	}
	limit, okLimit := planPolicies[userID]
	if !okLimit {
		limit = defaultUserLimitPolicy(*u)
	}
	resolvedFromTransition := false
	if resolved, changed, resolveErr := s.store.ResolveTrafficPeriodKey(r.Context(), userID, reportedPeriodKey); resolveErr != nil {
		return model.TrafficPeriod{}, resolveErr
	} else if changed {
		reportedPeriodKey = resolved
		resolvedFromTransition = true
	}
	periodKey := reportedPeriodKey
	var start, end time.Time
	if resolvedFromTransition || strings.Contains(reportedPeriodKey, "#migration-") {
		storedPeriod, periodErr := s.store.GetTrafficPeriod(r.Context(), userID, reportedPeriodKey)
		if periodErr != nil {
			return model.TrafficPeriod{}, periodErr
		}
		start, end = storedPeriod.StartedAt, storedPeriod.EndsAt
		return model.TrafficPeriod{UserID: userID, PeriodKey: storedPeriod.PeriodKey, StartedAt: start, EndsAt: end, Limit: limit.TrafficLimitBytes}, nil
	}
	var windowErr error
	periodKey, start, end, windowErr = trafficWindowForPeriodKey(time.Now(), reportedPeriodKey, limit.TrafficResetMode, limit.TrafficResetDay, limit.TrafficResetAnchor, loc)
	if windowErr != nil {
		return model.TrafficPeriod{}, windowErr
	}
	return model.TrafficPeriod{UserID: userID, PeriodKey: periodKey, StartedAt: start, EndsAt: end, Limit: limit.TrafficLimitBytes}, nil
}

func acceptedReportIDs(items []model.TrafficAcceptedReport) []string {
	out := []string{}
	for _, item := range items {
		switch item.Status {
		case "accepted", "duplicate", "covered":
			out = append(out, item.ReportID)
		}
	}
	return out
}

func userIDFromAccepted(accepted model.TrafficAcceptedReport, reports []model.TrafficReport) int64 {
	for _, report := range reports {
		if report.ReportID == accepted.ReportID {
			return report.UserID
		}
	}
	return 0
}

func (s *Server) userTrafficLedger(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	if userID <= 0 {
		fail(w, errors.New("user id is required"), 400)
		return
	}
	periodKey := strings.TrimSpace(r.URL.Query().Get("period_key"))
	serverID := int64Query(r, "server_id", 0)
	view, err := s.store.GetTrafficLedger(r.Context(), userID, serverID, periodKey)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"traffic_ledger": view})
}

func (s *Server) trafficLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	userID := int64Query(r, "user_id", 0)
	serverID := int64Query(r, "server_id", 0)
	periodKey := strings.TrimSpace(r.URL.Query().Get("period_key"))
	if userID > 0 {
		view, err := s.store.GetTrafficLedger(r.Context(), userID, serverID, periodKey)
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"traffic_ledger": view})
		return
	}
	issues, err := s.store.ListTrafficReconciliationEvents(r.Context(), 0, serverID, periodKey, "", 200)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"issues": issues})
}

func (s *Server) trafficLedgerReconcile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	var req struct {
		UserID    int64  `json:"user_id"`
		ServerID  int64  `json:"server_id"`
		PeriodKey string `json:"period_key"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.MarkTrafficStreamsRecovering(r.Context(), req.UserID, req.ServerID, strings.TrimSpace(req.PeriodKey)); err != nil {
		fail(w, err, 500)
		return
	}
	if req.UserID > 0 {
		view, err := s.store.GetTrafficLedger(r.Context(), req.UserID, req.ServerID, strings.TrimSpace(req.PeriodKey))
		if err != nil {
			fail(w, err, 500)
			return
		}
		write(w, 200, map[string]any{"ok": true, "traffic_ledger": view})
		return
	}
	issues, err := s.store.ListTrafficReconciliationEvents(r.Context(), 0, req.ServerID, strings.TrimSpace(req.PeriodKey), "", 200)
	if err != nil {
		fail(w, err, 500)
		return
	}
	write(w, 200, map[string]any{"ok": true, "issues": issues})
}

func (s *Server) queryTrafficLedgerCapability(ctx context.Context, principal application.Principal, capabilityName string, input json.RawMessage) (any, error) {
	switch capabilityName {
	case "traffic.get_user_ledger":
		var request struct {
			UserID    int64  `json:"user_id"`
			ServerID  int64  `json:"server_id"`
			PeriodKey string `json:"period_key"`
		}
		if err := strictAutomationInput(input, &request); err != nil || request.UserID <= 0 {
			return nil, errors.New("valid user_id is required")
		}
		if !principal.AllowsInt64("user_ids", request.UserID) {
			return nil, errors.New("authorized user not found")
		}
		return s.store.GetTrafficLedger(ctx, request.UserID, request.ServerID, strings.TrimSpace(request.PeriodKey))
	case "traffic.get_server_sync_state":
		var request struct {
			ServerID  int64  `json:"server_id"`
			UserID    int64  `json:"user_id"`
			PeriodKey string `json:"period_key"`
		}
		if err := strictAutomationInput(input, &request); err != nil || request.ServerID <= 0 {
			return nil, errors.New("valid server_id is required")
		}
		if !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server not found")
		}
		if request.UserID > 0 {
			if !principal.AllowsInt64("user_ids", request.UserID) {
				return nil, errors.New("authorized user not found")
			}
			return s.store.GetTrafficLedger(ctx, request.UserID, request.ServerID, strings.TrimSpace(request.PeriodKey))
		}
		issues, err := s.store.ListTrafficReconciliationEvents(ctx, 0, request.ServerID, strings.TrimSpace(request.PeriodKey), "", 200)
		if err != nil {
			return nil, err
		}
		return map[string]any{"server_id": request.ServerID, "issues": issues}, nil
	case "traffic.list_reconciliation_issues":
		var request struct {
			UserID    int64  `json:"user_id"`
			ServerID  int64  `json:"server_id"`
			PeriodKey string `json:"period_key"`
			Kind      string `json:"kind"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		if request.UserID > 0 && !principal.AllowsInt64("user_ids", request.UserID) {
			return nil, errors.New("authorized user not found")
		}
		if request.ServerID > 0 && !principal.AllowsInt64("server_ids", request.ServerID) {
			return nil, errors.New("authorized server not found")
		}
		issues, err := s.store.ListTrafficReconciliationEvents(ctx, request.UserID, request.ServerID, strings.TrimSpace(request.PeriodKey), strings.TrimSpace(request.Kind), 200)
		if err != nil {
			return nil, err
		}
		return map[string]any{"issues": issues}, nil
	default:
		return nil, errors.New("unsupported query capability")
	}
}
