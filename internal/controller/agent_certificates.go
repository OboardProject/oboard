package controller

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) agentManagedAssets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowRate(w, r, "agent-assets:"+server.AgentID, 120, time.Minute) {
		return
	}
	var req model.ManagedAssetRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.Assets) > 64 {
		fail(w, errors.New("too many managed asset requests"), 400)
		return
	}
	authorized, err := s.authorizedCertificateAssets(r.Context(), server.ID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	response := model.ManagedAssetResponse{Assets: make([]model.ManagedAsset, 0, len(req.Assets))}
	seen := map[string]bool{}
	routingRuleSets, err := s.authorizedRoutingRuleSetAssets(r.Context(), server.ID)
	if err != nil {
		fail(w, err, 500)
		return
	}
	for _, reference := range req.Assets {
		key := fmt.Sprintf("%s:%d", reference.Kind, reference.ID)
		if reference.ID <= 0 || reference.Revision == "" || seen[key] {
			fail(w, errors.New("invalid managed asset request"), 400)
			return
		}
		seen[key] = true
		if reference.Kind == "routing_rule_set" {
			set, ok := routingRuleSets[reference.ID]
			if !ok {
				fail(w, errors.New("managed asset is not authorized for this agent"), 403)
				return
			}
			if set.Revision != reference.Revision || len(set.Content) == 0 {
				fail(w, errors.New("managed asset revision is unavailable"), http.StatusConflict)
				return
			}
			filename := "rules.json"
			if set.Format == model.RoutingRuleSetFormatSingBoxBinary {
				filename = "rules.srs"
			}
			response.Assets = append(response.Assets, model.ManagedAsset{ManagedAssetReference: reference, Files: []model.ManagedAssetFile{{Name: filename, ContentB64: base64.StdEncoding.EncodeToString(set.Content), Mode: 0o600}}})
			continue
		}
		if reference.Kind != "certificate" {
			fail(w, errors.New("invalid managed asset request"), 400)
			return
		}
		certificate, ok := authorized[reference.ID]
		if !ok {
			fail(w, errors.New("managed asset is not authorized for this agent"), 403)
			return
		}
		if certificate.Status != model.CertificateStatusReady || certificate.Revision != reference.Revision {
			fail(w, errors.New("managed asset revision is unavailable"), http.StatusConflict)
			return
		}
		privateKey, err := security.DecryptSecret(s.sessionSecret, "certificate-private-key", certificate.PrivateKeyEncrypted)
		if err != nil {
			fail(w, err, 500)
			return
		}
		response.Assets = append(response.Assets, model.ManagedAsset{
			ManagedAssetReference: reference,
			Files: []model.ManagedAssetFile{
				{Name: "fullchain.pem", ContentB64: base64.StdEncoding.EncodeToString([]byte(certificate.FullchainPEM)), Mode: 0o600},
				{Name: "privkey.pem", ContentB64: base64.StdEncoding.EncodeToString([]byte(privateKey)), Mode: 0o600},
			},
		})
	}
	write(w, 200, response)
}

func routingRuleSetAssetReferences(serverID int64, rules []model.RoutingRule, sets []model.RoutingRuleSet) []model.ManagedAssetReference {
	wanted := map[int64]bool{}
	for _, rule := range rules {
		if rule.Enabled && rule.ServerID == serverID && rule.MatchSource == model.RoutingMatchSourceRuleSet && rule.RuleSetID != nil {
			wanted[*rule.RuleSetID] = true
		}
	}
	assets := make([]model.ManagedAssetReference, 0, len(wanted))
	for _, set := range sets {
		if wanted[set.ID] && set.Revision != "" && len(set.Content) > 0 {
			assets = append(assets, model.ManagedAssetReference{Kind: "routing_rule_set", ID: set.ID, Revision: set.Revision})
		}
	}
	return assets
}

func (s *Server) authorizedRoutingRuleSetAssets(ctx context.Context, serverID int64) (map[int64]model.RoutingRuleSet, error) {
	rules, err := s.store.ListRoutingRules(ctx)
	if err != nil {
		return nil, err
	}
	sets, err := s.store.ListRoutingRuleSets(ctx)
	if err != nil {
		return nil, err
	}
	wanted := map[int64]bool{}
	for _, rule := range rules {
		if rule.Enabled && rule.ServerID == serverID && rule.MatchSource == model.RoutingMatchSourceRuleSet && rule.RuleSetID != nil {
			wanted[*rule.RuleSetID] = true
		}
	}
	out := map[int64]model.RoutingRuleSet{}
	for _, set := range sets {
		if wanted[set.ID] {
			out[set.ID] = set
		}
	}
	return out, nil
}

func (s *Server) authorizedCertificateAssets(ctx context.Context, serverID int64) (map[int64]model.Certificate, error) {
	inbounds, err := s.store.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	allowedIDs := map[int64]bool{}
	for _, inbound := range inbounds {
		if inbound.ServerID == serverID && inbound.Enabled && inbound.CertificateMode != model.CertificateModeExternal && inbound.CertificateID != nil {
			allowedIDs[*inbound.CertificateID] = true
		}
	}
	out := make(map[int64]model.Certificate, len(allowedIDs))
	for id := range allowedIDs {
		certificate, err := s.store.GetCertificate(ctx, id)
		if err != nil {
			return nil, err
		}
		out[id] = *certificate
	}
	return out, nil
}

func (s *Server) agentCertificateIssues(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	server, ok := s.authAgent(w, r)
	if !ok {
		return
	}
	if !s.allowRate(w, r, "agent-certificate-issue:"+server.AgentID, 20, time.Hour) {
		return
	}
	var report model.CertificateIssueReport
	if !decode(w, r, &report) {
		return
	}
	if report.TaskID <= 0 || report.CertificateID <= 0 || len(report.Domains) == 0 {
		fail(w, errors.New("invalid certificate issue report"), 400)
		return
	}
	task, err := s.store.GetTask(r.Context(), report.TaskID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if task.ServerID != server.ID || task.Type != model.AgentTaskTypeIssueCertificateHTTP {
		fail(w, errors.New("certificate task does not belong to this agent"), 403)
		return
	}
	// A settled task must not accept further material, otherwise a node can
	// keep rewriting the stored certificate and force every node bound to it
	// to re-sync on each new revision.
	if store.IsTerminalTaskStatus(task.Status) {
		fail(w, errors.New("certificate task is already settled"), http.StatusConflict)
		return
	}
	var payload model.IssueCertificateHTTPTaskPayload
	if err := json.Unmarshal([]byte(task.PayloadJSON), &payload); err != nil || payload.CertificateID != report.CertificateID {
		fail(w, errors.New("certificate report does not match task payload"), 400)
		return
	}
	requested, err := normalizeCertificateDomains(payload.Domains)
	if err != nil {
		fail(w, err, 400)
		return
	}
	reported, err := normalizeCertificateDomains(report.Domains)
	if err != nil || strings.Join(requested, "\x00") != strings.Join(reported, "\x00") {
		fail(w, errors.New("certificate report domains do not match task payload"), 400)
		return
	}
	certificate, err := s.store.GetCertificate(r.Context(), report.CertificateID)
	if err != nil {
		fail(w, err, 404)
		return
	}
	if certificate.IssuanceServerID == nil || *certificate.IssuanceServerID != server.ID || certificate.ChallengeType != model.CertificateChallengeHTTP {
		fail(w, errors.New("certificate is not authorized for this agent"), 403)
		return
	}
	if err := s.storeCertificateMaterial(r.Context(), certificate, report.CertificatePEM, report.FullchainPEM, report.PrivateKeyPEM, untrustedCertificateMaterial); err != nil {
		fail(w, fmt.Errorf("store issued certificate: %w", err), 400)
		return
	}
	write(w, 200, map[string]any{"ok": true, "revision": certificate.Revision})
}
