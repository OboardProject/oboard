package controller

import (
	"context"
	"database/sql"
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
	"github.com/OboardProject/oboard/internal/version"
)

// registerSystemAutomationOperations wires the system-domain capability
// operations: global settings, backups, certificates, approval policies,
// service accounts, AI providers, tool-call audits, and notification channels.

func (s *Server) registerSystemAutomationOperations() {
	s.registerSettingsOperations()
	s.registerSubscriptionRelayOperations()
	s.registerBackupOperations()
	s.registerCertificateOperations()
	s.registerAutomationAdminOperations()
	s.registerNotificationOperations()
}

type subscriptionRelayAutomationInput struct {
	RelayID   int64  `json:"relay_id"`
	Name      string `json:"name"`
	PublicURL string `json:"public_url"`
	Confirm   bool   `json:"confirm"`
}

func (s *Server) registerSubscriptionRelayOperations() {
	s.automation.RegisterValidator("subscription_relays.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, normalized, err := s.subscriptionRelayAutomationDraft(ctx, input, false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"name": request.Name, "public_url": normalized}, nil
	})
	s.automation.RegisterRevisionResolver("subscription_relays.create", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("subscription_relays.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, normalized, err := s.subscriptionRelayAutomationDraft(ctx, input, false)
		if err != nil {
			return nil, err
		}
		token, err := security.RandomToken(32)
		if err != nil {
			return nil, err
		}
		expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
		relay := model.SubscriptionRelay{Name: request.Name, PublicURL: normalized, Status: "pending", EnrollmentHash: security.HashSecret(token), EnrollmentExpiresAt: &expiresAt}
		if err := s.store.CreateSubscriptionRelay(ctx, &relay); err != nil {
			return nil, err
		}
		items, err := s.publicSubscriptionRelays(ctx)
		if err != nil {
			return nil, err
		}
		public := map[string]any{"subscription_relay": findPublicSubscriptionRelay(items, relay.ID), "enrollment_expires_at": expiresAt}
		oneTime := map[string]any{"subscription_relay": findPublicSubscriptionRelay(items, relay.ID), "enrollment_expires_at": expiresAt, "enrollment_token": token}
		return automation.MutationResult{Public: public, OneTime: oneTime}, nil
	})

	for _, capability := range []string{"subscription_relays.update", "subscription_relays.issue_enrollment", "subscription_relays.activate", "subscription_relays.request_update", "subscription_relays.delete"} {
		name := capability
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			request, err := decodeSubscriptionRelayAutomationInput(input)
			if err != nil {
				return nil, err
			}
			relay, err := s.store.GetSubscriptionRelay(ctx, request.RelayID)
			if err != nil {
				return nil, err
			}
			return map[string]string{"subscription-relay:" + strconv.FormatInt(relay.ID, 10): relay.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
		})
	}

	s.automation.RegisterValidator("subscription_relays.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, normalized, err := s.subscriptionRelayAutomationDraft(ctx, input, true)
		if err != nil {
			return nil, err
		}
		if _, err := s.store.GetSubscriptionRelay(ctx, request.RelayID); err != nil {
			return nil, err
		}
		return map[string]any{"relay_id": request.RelayID, "name": request.Name, "public_url": normalized}, nil
	})
	s.automation.Register("subscription_relays.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, normalized, err := s.subscriptionRelayAutomationDraft(ctx, input, true)
		if err != nil {
			return nil, err
		}
		relay, err := s.store.GetSubscriptionRelay(ctx, request.RelayID)
		if err != nil {
			return nil, err
		}
		settings, _ := s.store.ListSettings(ctx)
		wasActive := strings.TrimRight(settings[settingSubscriptionRelayURL], "/") == strings.TrimRight(relay.PublicURL, "/")
		relay.Name, relay.PublicURL = request.Name, normalized
		if err := s.store.UpdateSubscriptionRelay(ctx, relay); err != nil {
			return nil, err
		}
		if wasActive {
			if err := s.store.SetSetting(ctx, settingSubscriptionRelayURL, normalized); err != nil {
				return nil, err
			}
		}
		items, err := s.publicSubscriptionRelays(ctx)
		return map[string]any{"subscription_relay": findPublicSubscriptionRelay(items, relay.ID)}, err
	})

	s.automation.RegisterValidator("subscription_relays.issue_enrollment", s.validateSubscriptionRelayIDOperation)
	s.automation.Register("subscription_relays.issue_enrollment", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionRelayAutomationInput(input)
		if err != nil {
			return nil, err
		}
		token, err := security.RandomToken(32)
		if err != nil {
			return nil, err
		}
		expiresAt := time.Now().UTC().Add(enrollmentTokenTTL)
		if err := s.store.SetSubscriptionRelayEnrollment(ctx, request.RelayID, security.HashSecret(token), expiresAt); err != nil {
			return nil, err
		}
		public := map[string]any{"relay_id": request.RelayID, "enrollment_expires_at": expiresAt}
		oneTime := map[string]any{"relay_id": request.RelayID, "enrollment_expires_at": expiresAt, "enrollment_token": token}
		return automation.MutationResult{Public: public, OneTime: oneTime}, nil
	})

	s.automation.RegisterValidator("subscription_relays.activate", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		result, err := s.validateSubscriptionRelayIDOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		request, _ := decodeSubscriptionRelayAutomationInput(input)
		relay, _ := s.store.GetSubscriptionRelay(ctx, request.RelayID)
		if relay.TokenHash == "" || relay.SigningSecretEncrypted == "" {
			return nil, errors.New("中继尚未接入，不能设为订阅入口")
		}
		return result, nil
	})
	s.automation.Register("subscription_relays.activate", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionRelayAutomationInput(input)
		if err != nil {
			return nil, err
		}
		relay, err := s.store.GetSubscriptionRelay(ctx, request.RelayID)
		if err != nil {
			return nil, err
		}
		if relay.TokenHash == "" || relay.SigningSecretEncrypted == "" {
			return nil, errors.New("中继尚未接入，不能设为订阅入口")
		}
		if err := s.store.SetSettings(ctx, map[string]string{settingSubscriptionRelayURL: relay.PublicURL, settingSubscriptionControllerDirectEnabled: "false"}); err != nil {
			return nil, err
		}
		return map[string]any{"relay_id": relay.ID, "active": true}, nil
	})

	s.automation.RegisterValidator("subscription_relays.request_update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		result, err := s.validateSubscriptionRelayIDOperation(ctx, principal, input)
		if err != nil {
			return nil, err
		}
		request, _ := decodeSubscriptionRelayAutomationInput(input)
		relay, _ := s.store.GetSubscriptionRelay(ctx, request.RelayID)
		if relay.TokenHash == "" {
			return nil, errors.New("中继尚未接入，不能下发更新")
		}
		return result, nil
	})
	s.automation.Register("subscription_relays.request_update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionRelayAutomationInput(input)
		if err != nil {
			return nil, err
		}
		if err := s.store.RequestSubscriptionRelayUpdate(ctx, request.RelayID, version.Version, version.Build); err != nil {
			return nil, err
		}
		return map[string]any{"relay_id": request.RelayID, "status": "updating", "target_version": version.Version, "target_build": version.Build}, nil
	})

	s.automation.RegisterValidator("subscription_relays.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionRelayAutomationInput(input)
		if err != nil || !request.Confirm {
			return nil, errors.New("relay_id and confirm=true are required")
		}
		if _, err := s.store.GetSubscriptionRelay(ctx, request.RelayID); err != nil {
			return nil, err
		}
		return map[string]any{"relay_id": request.RelayID, "deleted": true}, nil
	})
	s.automation.Register("subscription_relays.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		request, err := decodeSubscriptionRelayAutomationInput(input)
		if err != nil || !request.Confirm {
			return nil, errors.New("relay_id and confirm=true are required")
		}
		relay, err := s.store.GetSubscriptionRelay(ctx, request.RelayID)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteSubscriptionRelay(ctx, relay.ID); err != nil {
			return nil, err
		}
		return map[string]any{"relay_id": relay.ID, "deleted": true}, nil
	})
}

func (s *Server) subscriptionRelayAutomationDraft(ctx context.Context, input json.RawMessage, requireID bool) (subscriptionRelayAutomationInput, string, error) {
	request, err := decodeSubscriptionRelayAutomationInput(input)
	if err != nil {
		return request, "", err
	}
	request.Name = strings.TrimSpace(request.Name)
	if (requireID && request.RelayID <= 0) || request.Name == "" || len(request.Name) > 80 {
		return request, "", errors.New("有效的 relay_id 和 1-80 字符名称是必需的")
	}
	normalized, err := s.normalizeSubscriptionRelayURL(request.PublicURL)
	if err != nil || normalized == "" {
		return request, "", errors.New("中继公开地址必须是使用当前基础路径的 HTTPS 地址")
	}
	excludeID := int64(0)
	if requireID {
		excludeID = request.RelayID
	}
	if exists, err := s.store.SubscriptionRelayURLExists(ctx, normalized, excludeID); err != nil {
		return request, "", err
	} else if exists {
		return request, "", errors.New("该中继公开地址已存在")
	}
	return request, normalized, nil
}

func decodeSubscriptionRelayAutomationInput(input json.RawMessage) (subscriptionRelayAutomationInput, error) {
	var request subscriptionRelayAutomationInput
	if err := strictAutomationInput(input, &request); err != nil {
		return request, err
	}
	return request, nil
}

func (s *Server) validateSubscriptionRelayIDOperation(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
	request, err := decodeSubscriptionRelayAutomationInput(input)
	if err != nil || request.RelayID <= 0 {
		return nil, errors.New("valid relay_id is required")
	}
	if _, err := s.store.GetSubscriptionRelay(ctx, request.RelayID); err != nil {
		return nil, err
	}
	return map[string]any{"relay_id": request.RelayID}, nil
}

// ---- settings ----

var settingsAutomationFields = map[string]bool{
	"audit_enabled": true, "subscription_audit_enabled": true, "connection_audit_enabled": true,
	"audit_action": true, "traffic_timezone": true, "traffic_enforcement_mode": true,
	"subscription_age_policy": true, "subscription_custom_path_mode": true,
	"subscription_relay_url": true, "subscription_controller_direct_enabled": true,
	"server_default_mtu_mode": true, "server_default_bbr_enabled": true,
	"server_default_time_correction_mode": true, "time_check_ntp_servers": true,
	"trusted_proxy_cidrs": true, "controller_log_max_mb": true, "controller_log_backups": true,
	"registration_enabled": true,
}

func (s *Server) registerSettingsOperations() {
	s.automation.RegisterValidator("settings.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		changed, err := s.settingsUpdateCandidate(ctx, input, false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"changed_fields": changed}, nil
	})
	s.automation.RegisterRevisionResolver("settings.update", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("settings.update", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		changed, err := s.settingsUpdateCandidate(ctx, input, true)
		if err != nil {
			return nil, err
		}
		return map[string]any{"changed_fields": changed}, nil
	})
}

func (s *Server) settingsUpdateCandidate(ctx context.Context, input json.RawMessage, apply bool) ([]string, error) {
	var request struct {
		Changes json.RawMessage `json:"changes"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	fields, err := decodeClosedAutomationFields(request.Changes, settingsAutomationFields, "changes")
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, errors.New("changes must contain at least one setting")
	}
	changed := make([]string, 0, len(fields))
	for field := range fields {
		changed = append(changed, field)
	}
	updates := map[string]string{}
	setBool := func(key string, value json.RawMessage) error {
		var v bool
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		updates[key] = strconv.FormatBool(v)
		return nil
	}
	setString := func(key string, value json.RawMessage) error {
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		updates[key] = strings.TrimSpace(v)
		return nil
	}
	if value, ok := fields["audit_enabled"]; ok {
		if err := setBool(settingAuditEnabled, value); err != nil {
			return nil, err
		}
	}
	if value, ok := fields["subscription_audit_enabled"]; ok {
		if err := setBool(settingSubscriptionAuditEnabled, value); err != nil {
			return nil, err
		}
	}
	if value, ok := fields["connection_audit_enabled"]; ok {
		if err := setBool(settingConnectionAuditEnabled, value); err != nil {
			return nil, err
		}
	}
	if value, ok := fields["audit_action"]; ok {
		var action string
		if err := json.Unmarshal(value, &action); err != nil {
			return nil, err
		}
		if action != string(model.AuditActionRestrict) && action != string(model.AuditActionWarn) {
			return nil, errors.New("audit_action must be restrict or warn")
		}
		updates[settingAuditAction] = action
	}
	if value, ok := fields["traffic_timezone"]; ok {
		var zone string
		if err := json.Unmarshal(value, &zone); err != nil {
			return nil, err
		}
		if _, err := time.LoadLocation(strings.TrimSpace(zone)); err != nil {
			return nil, errors.New("traffic_timezone is not a valid IANA timezone")
		}
		updates["traffic_timezone"] = strings.TrimSpace(zone)
	}
	if value, ok := fields["traffic_enforcement_mode"]; ok {
		var mode string
		if err := json.Unmarshal(value, &mode); err != nil {
			return nil, err
		}
		if mode != "reject_new" && mode != "disconnect_and_reject" {
			return nil, errors.New("traffic_enforcement_mode must be reject_new or disconnect_and_reject")
		}
		updates["traffic_enforcement_mode"] = mode
	}
	if value, ok := fields["subscription_age_policy"]; ok {
		var policy string
		if err := json.Unmarshal(value, &policy); err != nil {
			return nil, err
		}
		if policy != "optional" && policy != "required" {
			return nil, errors.New("subscription_age_policy must be optional or required")
		}
		updates[settingSubscriptionAgePolicy] = policy
	}
	if value, ok := fields["subscription_custom_path_mode"]; ok {
		var mode string
		if err := json.Unmarshal(value, &mode); err != nil {
			return nil, err
		}
		switch model.SubscriptionCustomPathMode(mode) {
		case model.SubscriptionCustomPathDisabled, model.SubscriptionCustomPathSelective, model.SubscriptionCustomPathEnabled:
		default:
			return nil, errors.New("subscription_custom_path_mode must be disabled, selective or enabled")
		}
		updates[settingSubscriptionCustomPathMode] = mode
	}
	if value, ok := fields["subscription_relay_url"]; ok {
		var raw string
		if err := json.Unmarshal(value, &raw); err != nil {
			return nil, err
		}
		normalized, err := s.normalizeSubscriptionRelayURL(raw)
		if err != nil {
			return nil, err
		}
		updates[settingSubscriptionRelayURL] = normalized
	}
	if value, ok := fields["subscription_controller_direct_enabled"]; ok {
		if err := setBool(settingSubscriptionControllerDirectEnabled, value); err != nil {
			return nil, err
		}
	}
	if value, ok := fields["server_default_mtu_mode"]; ok {
		if err := setString(settingServerDefaultMTUMode, value); err != nil {
			return nil, err
		}
	}
	if value, ok := fields["server_default_bbr_enabled"]; ok {
		if err := setBool(settingServerDefaultBBREnabled, value); err != nil {
			return nil, err
		}
	}
	if value, ok := fields["server_default_time_correction_mode"]; ok {
		if err := setString(settingServerDefaultTimeCorrection, value); err != nil {
			return nil, err
		}
	}
	if value, ok := fields["time_check_ntp_servers"]; ok {
		var servers []string
		if err := json.Unmarshal(value, &servers); err != nil {
			return nil, err
		}
		normalized, err := normalizeTimeCheckNTPServers(servers)
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(normalized)
		updates[settingTimeCheckNTPServers] = string(encoded)
	}
	if value, ok := fields["trusted_proxy_cidrs"]; ok {
		var cidrs []string
		if err := json.Unmarshal(value, &cidrs); err != nil {
			return nil, err
		}
		normalized, err := normalizeTrustedProxyCIDRs(cidrs)
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(normalized)
		updates[settingTrustedProxyCIDRs] = string(encoded)
	}
	if value, ok := fields["controller_log_max_mb"]; ok {
		var v int
		if err := json.Unmarshal(value, &v); err != nil || v < 1 || v > 1024 {
			return nil, errors.New("controller_log_max_mb must be between 1 and 1024")
		}
		updates["controller_log_max_mb"] = strconv.Itoa(v)
	}
	if value, ok := fields["controller_log_backups"]; ok {
		var v int
		if err := json.Unmarshal(value, &v); err != nil || v < 0 || v > 30 {
			return nil, errors.New("controller_log_backups must be between 0 and 30")
		}
		updates["controller_log_backups"] = strconv.Itoa(v)
	}
	if value, ok := fields["registration_enabled"]; ok {
		if err := setBool(settingRegistrationEnabled, value); err != nil {
			return nil, err
		}
	}
	if !apply {
		return changed, nil
	}
	if err := s.store.SetSettings(ctx, updates); err != nil {
		return nil, err
	}
	return changed, nil
}

// ---- backups ----

func (s *Server) registerBackupOperations() {
	s.automation.RegisterValidator("backups.create", func(context.Context, application.Principal, json.RawMessage) (any, error) {
		if !s.backupConfigured || s.backupManager == nil {
			return nil, errors.New("主控备份目录不可用，请检查 OBOARD_BACKUP_DIR")
		}
		return map[string]any{}, nil
	})
	s.automation.RegisterRevisionResolver("backups.create", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("backups.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		if !s.backupConfigured || s.backupManager == nil {
			return nil, errors.New("主控备份目录不可用，请检查 OBOARD_BACKUP_DIR")
		}
		var request struct {
			UploadRemote *bool `json:"upload_remote"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		s.backupMu.Lock()
		defer s.backupMu.Unlock()
		settings, err := s.loadControllerBackupSettings(ctx)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(settings.Secrets.RecoveryPassword) == "" {
			return nil, errors.New("请先设置备份恢复密码")
		}
		uploadRemote := true
		if request.UploadRemote != nil {
			uploadRemote = *request.UploadRemote
		}
		item, err := s.createControllerDataBackup(ctx, settings, "manual", uploadRemote, false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"backup": map[string]any{"id": item.ID, "name": item.Name, "origin": item.Origin, "status": item.LocalStatus}}, nil
	})
}

// ---- certificates ----

func (s *Server) registerCertificateOperations() {
	s.automation.RegisterValidator("certificates.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		certificate, err := s.certificateOperationCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		if certificate.Status == "issued" && certificate.NotAfter != nil && time.Until(*certificate.NotAfter) > 7*24*time.Hour {
			return nil, errors.New("证书仍有效，暂不需要重新签发")
		}
		return map[string]any{"certificate": automationCertificateView(*certificate)}, nil
	})
	s.automation.RegisterRevisionResolver("certificates.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		certificate, err := s.certificateOperationCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"certificate:" + strconv.FormatInt(certificate.ID, 10): certificate.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("certificates.issue", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		certificate, err := s.certificateOperationCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		if certificate.ChallengeType == "imported" {
			return nil, errors.New("imported certificates cannot be issued")
		}
		if err := s.startCertificateIssue(context.WithoutCancel(ctx), certificate, false, false); err != nil {
			return nil, err
		}
		return map[string]any{"certificate": automationCertificateView(*certificate), "status": "accepted"}, nil
	})

	s.automation.RegisterValidator("certificates.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		certificate, err := s.certificateDeleteCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"certificate_id": certificate.ID}, nil
	})
	s.automation.RegisterRevisionResolver("certificates.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
		certificate, err := s.certificateDeleteCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		return map[string]string{"certificate:" + strconv.FormatInt(certificate.ID, 10): certificate.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
	})
	s.automation.Register("certificates.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		certificate, err := s.certificateDeleteCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteCertificate(ctx, certificate.ID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "certificate_id": certificate.ID}, nil
	})
}

func (s *Server) certificateOperationCandidate(ctx context.Context, input json.RawMessage) (*model.Certificate, error) {
	var request struct {
		CertificateID int64 `json:"certificate_id"`
	}
	if err := strictAutomationInput(input, &request); err != nil || request.CertificateID <= 0 {
		return nil, errors.New("certificate_id must be a positive integer")
	}
	return s.store.GetCertificate(ctx, request.CertificateID)
}

func (s *Server) certificateDeleteCandidate(ctx context.Context, input json.RawMessage) (*model.Certificate, error) {
	var request struct {
		CertificateID int64 `json:"certificate_id"`
		Confirm       bool  `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	if request.CertificateID <= 0 || !request.Confirm {
		return nil, errors.New("certificate_id and confirm=true are required")
	}
	return s.store.GetCertificate(ctx, request.CertificateID)
}

func automationCertificateView(certificate model.Certificate) map[string]any {
	return map[string]any{
		"id": certificate.ID, "revision": certificate.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"name": certificate.Name, "primary_domain": certificate.PrimaryDomain,
		"wildcard": certificate.Wildcard, "challenge_type": certificate.ChallengeType,
		"status": certificate.Status, "not_before": certificate.NotBefore, "not_after": certificate.NotAfter,
		"auto_renew": certificate.AutoRenew, "last_error": certificate.LastError,
		"last_issued_at": certificate.LastIssuedAt, "created_at": certificate.CreatedAt, "updated_at": certificate.UpdatedAt,
	}
}

// ---- automation admin (approval policies, service accounts, tool audits) ----

func (s *Server) registerAutomationAdminOperations() {
	s.automation.RegisterValidator("approval_policies.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		policy, err := s.approvalPolicySetCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"approval_policy": automationApprovalPolicyView(*policy)}, nil
	})
	s.automation.RegisterRevisionResolver("approval_policies.set", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("approval_policies.set", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		policy, err := s.approvalPolicySetCandidate(ctx, input)
		if err != nil {
			return nil, err
		}
		if err := s.store.UpsertApprovalPolicy(ctx, policy); err != nil {
			return nil, err
		}
		return map[string]any{"approval_policy": automationApprovalPolicyView(*policy)}, nil
	})

	s.automation.RegisterValidator("approval_policies.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		policyID, err := approvalPolicyDeleteInput(input)
		if err != nil {
			return nil, err
		}
		return map[string]any{"policy_id": policyID}, nil
	})
	s.automation.RegisterRevisionResolver("approval_policies.delete", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("approval_policies.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		policyID, err := approvalPolicyDeleteInput(input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteApprovalPolicy(ctx, policyID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "policy_id": policyID}, nil
	})

	s.automation.RegisterValidator("api_principals.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		principalID, err := apiPrincipalDeleteInput(input)
		if err != nil {
			return nil, err
		}
		item, err := s.store.GetAPIPrincipal(ctx, principalID)
		if err != nil || item.Type != model.APIPrincipalServiceAccount {
			return nil, errors.New("service account not found")
		}
		return map[string]any{"principal_id": principalID}, nil
	})
	s.automation.RegisterRevisionResolver("api_principals.delete", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("api_principals.delete", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		principalID, err := apiPrincipalDeleteInput(input)
		if err != nil {
			return nil, err
		}
		if err := s.store.DeleteAPIPrincipal(ctx, principalID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true, "principal_id": principalID}, nil
	})
}

func (s *Server) approvalPolicySetCandidate(ctx context.Context, input json.RawMessage) (*model.ApprovalPolicy, error) {
	var request struct {
		PrincipalID    string          `json:"principal_id"`
		Capability     string          `json:"capability"`
		ResourceFilter json.RawMessage `json:"resource_filter"`
		Mode           string          `json:"mode"`
		AllowRisk4     bool            `json:"allow_risk4"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return nil, err
	}
	principalID, capabilityName := strings.TrimSpace(request.PrincipalID), strings.TrimSpace(request.Capability)
	if principalID == "" || capabilityName == "" {
		return nil, errors.New("principal_id and capability are required")
	}
	principal, err := s.store.GetAPIPrincipal(ctx, principalID)
	if err != nil || principal.Type == model.APIPrincipalOAuth {
		return nil, errors.New("审批策略仅支持服务账号 Principal")
	}
	descriptor, ok := s.capabilities.Get(capabilityName)
	if !ok || !descriptor.Executable || len(descriptor.RequiredScopes) == 0 {
		return nil, errors.New("capability must be an executable capability")
	}
	if !(application.Principal{Scopes: principal.Scopes}).HasScope(descriptor.RequiredScopes[0]) {
		return nil, errors.New("Principal 的范围不包含该能力")
	}
	mode := model.ApprovalMode(strings.ToLower(strings.TrimSpace(request.Mode)))
	if mode != model.ApprovalDenied && mode != model.ApprovalRequired && mode != model.ApprovalAutomatic {
		return nil, errors.New("mode must be denied, required, or automatic")
	}
	if request.AllowRisk4 && descriptor.RiskClass < 4 {
		return nil, errors.New("allow_risk4 仅对风险 4 的能力有效")
	}
	filter := request.ResourceFilter
	if len(filter) == 0 || string(filter) == "null" {
		filter = json.RawMessage(`{}`)
	}
	var probe map[string]any
	if json.Unmarshal(filter, &probe) != nil {
		return nil, errors.New("resource_filter must be a JSON object")
	}
	id, _ := security.RandomToken(18)
	if existing, findErr := s.store.GetApprovalPolicy(ctx, principal.ID, descriptor.Name, time.Time{}); findErr == nil {
		id = strings.TrimPrefix(existing.ID, "pol_")
	}
	return &model.ApprovalPolicy{ID: "pol_" + id, PrincipalID: principal.ID, Capability: descriptor.Name, ResourceFilter: filter, Mode: mode, AllowRisk4: request.AllowRisk4}, nil
}

func automationApprovalPolicyView(policy model.ApprovalPolicy) map[string]any {
	return map[string]any{
		"id": policy.ID, "principal_id": policy.PrincipalID, "capability": policy.Capability,
		"mode": policy.Mode, "allow_risk4": policy.AllowRisk4, "expires_at": policy.ExpiresAt,
		"resource_filter_configured": len(policy.ResourceFilter) > 0 && string(policy.ResourceFilter) != "{}",
		"created_at":                 policy.CreatedAt, "updated_at": policy.UpdatedAt,
	}
}

func approvalPolicyDeleteInput(input json.RawMessage) (string, error) {
	var request struct {
		PolicyID string `json:"policy_id"`
		Confirm  bool   `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return "", err
	}
	policyID := strings.TrimSpace(request.PolicyID)
	if policyID == "" || !request.Confirm {
		return "", errors.New("policy_id and confirm=true are required")
	}
	return policyID, nil
}

func apiPrincipalDeleteInput(input json.RawMessage) (string, error) {
	var request struct {
		PrincipalID string `json:"principal_id"`
		Confirm     bool   `json:"confirm"`
	}
	if err := strictAutomationInput(input, &request); err != nil {
		return "", err
	}
	principalID := strings.TrimSpace(request.PrincipalID)
	if principalID == "" || !request.Confirm {
		return "", errors.New("principal_id and confirm=true are required")
	}
	return principalID, nil
}

// ---- notification channels ----

var notificationChannelAutomationFields = map[string]bool{
	"name": true, "type": true, "enabled": true, "events": true, "config_json": true,
	"templates_json": true, "user_ids": true,
}

func (s *Server) registerNotificationOperations() {
	for _, name := range []string{"notification_channels.create", "notification_channels.update"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			channel, changed, err := s.notificationChannelAutomationCandidate(ctx, principal, input, name)
			if err != nil {
				return nil, err
			}
			return automationNotificationChannelResult(channel, changed)
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			channel, _, err := s.notificationChannelAutomationCandidate(ctx, principal, input, name)
			if err != nil {
				return nil, err
			}
			if name == "notification_channels.create" || channel.ID == 0 {
				return map[string]string{}, nil
			}
			return map[string]string{"notification_channel:" + strconv.FormatInt(channel.ID, 10): channel.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			return s.applyNotificationChannelOperation(ctx, principal, input, name)
		})
	}
	for _, name := range []string{"notification_channels.delete", "notification_channels.test"} {
		s.automation.RegisterValidator(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			channel, err := s.notificationChannelByID(ctx, principal, input)
			if err != nil {
				return nil, err
			}
			return map[string]any{"channel_id": channel.ID}, nil
		})
		s.automation.RegisterRevisionResolver(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (map[string]string, error) {
			channel, err := s.notificationChannelByID(ctx, principal, input)
			if err != nil {
				return nil, err
			}
			return map[string]string{"notification_channel:" + strconv.FormatInt(channel.ID, 10): channel.UpdatedAt.UTC().Format(time.RFC3339Nano)}, nil
		})
		s.automation.Register(name, func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
			channel, err := s.notificationChannelByID(ctx, principal, input)
			if err != nil {
				return nil, err
			}
			if name == "notification_channels.delete" {
				if err := s.store.DeleteNotificationChannel(ctx, channel.ID, *principal.UserID); err != nil {
					return nil, err
				}
				return map[string]any{"deleted": true, "channel_id": channel.ID}, nil
			}
			if err := validateNotificationChannel(channel, principal.Role); err != nil {
				return nil, err
			}
			title, body := notificationTestMessage(channel.Name, channel.Type)
			if err := s.notificationSender(ctx, *channel, title, body); err != nil {
				return nil, fmt.Errorf("发送测试通知失败: %w", err)
			}
			return map[string]any{"sent": true, "channel_id": channel.ID}, nil
		})
	}

	s.automation.RegisterValidator("notification_announcements.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct {
			Title   string  `json:"title"`
			Body    string  `json:"body"`
			UserIDs []int64 `json:"user_ids"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Body) == "" {
			return nil, errors.New("title and body are required")
		}
		if len([]rune(request.Title)) > 120 || len([]rune(request.Body)) > 3000 {
			return nil, errors.New("通知内容过长")
		}
		for _, userID := range request.UserIDs {
			if !principal.AllowsInt64("user_ids", userID) {
				return nil, errors.New("announcement includes an unauthorized user")
			}
		}
		return map[string]any{"title": strings.TrimSpace(request.Title), "queued_count": len(request.UserIDs)}, nil
	})
	s.automation.RegisterRevisionResolver("notification_announcements.create", func(context.Context, application.Principal, json.RawMessage) (map[string]string, error) {
		return map[string]string{}, nil
	})
	s.automation.Register("notification_announcements.create", func(ctx context.Context, principal application.Principal, input json.RawMessage) (any, error) {
		var request struct {
			Title   string  `json:"title"`
			Body    string  `json:"body"`
			UserIDs []int64 `json:"user_ids"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return nil, err
		}
		if principal.UserID == nil {
			return nil, errors.New("authentication required")
		}
		announcement := &model.NotificationAnnouncement{
			ActorUserID: *principal.UserID, Title: strings.TrimSpace(request.Title),
			Body: strings.TrimSpace(request.Body), UserIDs: request.UserIDs,
		}
		if err := s.store.CreateNotificationAnnouncement(ctx, announcement); err != nil {
			return nil, err
		}
		queued := 0
		actorName := strings.TrimSpace(principal.Name)
		for _, userID := range announcement.UserIDs {
			queued += s.enqueueNotificationEvent(ctx, notificationEvent{
				Name:         notificationAdminAnnouncement,
				Key:          fmt.Sprintf("announcement:%d:user:%d", announcement.ID, userID),
				TargetUserID: userID,
				Data:         map[string]string{"Title": announcement.Title, "Message": announcement.Body, "Sender": actorName, "Time": s.notificationNow(ctx)},
			})
		}
		_ = s.store.UpdateNotificationAnnouncementQueuedCount(ctx, announcement.ID, queued)
		return map[string]any{"announcement_id": announcement.ID, "queued_count": queued}, nil
	})
}

func (s *Server) notificationChannelByID(ctx context.Context, principal application.Principal, input json.RawMessage) (*model.NotificationChannel, error) {
	var request struct {
		ChannelID int64 `json:"channel_id"`
	}
	if err := json.Unmarshal(input, &request); err != nil || request.ChannelID <= 0 {
		return nil, errors.New("channel_id must be a positive integer")
	}
	if principal.UserID == nil {
		return nil, errors.New("authentication required")
	}
	channel, err := s.store.GetNotificationChannel(ctx, request.ChannelID)
	if err != nil || channel.OwnerUserID != *principal.UserID {
		if err == nil {
			err = sql.ErrNoRows
		}
		return nil, err
	}
	return channel, nil
}

func (s *Server) notificationChannelAutomationCandidate(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (model.NotificationChannel, []string, error) {
	if principal.UserID == nil {
		return model.NotificationChannel{}, nil, errors.New("authentication required")
	}
	if name == "notification_channels.create" {
		var request struct {
			NotificationChannel json.RawMessage `json:"notification_channel"`
		}
		if err := strictAutomationInput(input, &request); err != nil {
			return model.NotificationChannel{}, nil, err
		}
		fields, err := decodeClosedAutomationFields(request.NotificationChannel, notificationChannelAutomationFields, "notification_channel")
		if err != nil {
			return model.NotificationChannel{}, nil, err
		}
		if _, ok := fields["name"]; !ok {
			return model.NotificationChannel{}, nil, errors.New("notification_channel.name is required")
		}
		if _, ok := fields["type"]; !ok {
			return model.NotificationChannel{}, nil, errors.New("notification_channel.type is required")
		}
		var channel model.NotificationChannel
		if err := json.Unmarshal(request.NotificationChannel, &channel); err != nil {
			return model.NotificationChannel{}, nil, err
		}
		channel.OwnerUserID = *principal.UserID
		if err := validateNotificationChannel(&channel, principal.Role); err != nil {
			return model.NotificationChannel{}, nil, err
		}
		if err := s.validateNotificationTargets(ctx, &channel, *principal.UserID, principal.Role); err != nil {
			return model.NotificationChannel{}, nil, err
		}
		return channel, nil, nil
	}
	var request struct {
		ChannelID int64           `json:"channel_id"`
		Changes   json.RawMessage `json:"changes"`
	}
	if err := strictAutomationInput(input, &request); err != nil || request.ChannelID <= 0 {
		return model.NotificationChannel{}, nil, errors.New("channel_id must be a positive integer")
	}
	fields, err := decodeClosedAutomationFields(request.Changes, notificationChannelAutomationFields, "changes")
	if err != nil {
		return model.NotificationChannel{}, nil, err
	}
	if len(fields) == 0 {
		return model.NotificationChannel{}, nil, errors.New("changes must contain at least one channel field")
	}
	current, err := s.store.GetNotificationChannel(ctx, request.ChannelID)
	if err != nil || current.OwnerUserID != *principal.UserID {
		if err == nil {
			err = sql.ErrNoRows
		}
		return model.NotificationChannel{}, nil, err
	}
	var patch model.NotificationChannel
	if err := json.Unmarshal(request.Changes, &patch); err != nil {
		return model.NotificationChannel{}, nil, err
	}
	merged := mergeNotificationChannelPatch(*current, patch, fields)
	merged.ID = current.ID
	merged.OwnerUserID = *principal.UserID
	if err := validateNotificationChannel(&merged, principal.Role); err != nil {
		return model.NotificationChannel{}, nil, err
	}
	if err := s.validateNotificationTargets(ctx, &merged, *principal.UserID, principal.Role); err != nil {
		return model.NotificationChannel{}, nil, err
	}
	return merged, automationChangedFields(fields), nil
}

func mergeNotificationChannelPatch(current model.NotificationChannel, patch model.NotificationChannel, fields map[string]json.RawMessage) model.NotificationChannel {
	merged := current
	if _, ok := fields["name"]; ok {
		merged.Name = patch.Name
	}
	if _, ok := fields["type"]; ok {
		merged.Type = patch.Type
	}
	if _, ok := fields["enabled"]; ok {
		merged.Enabled = patch.Enabled
	}
	if _, ok := fields["events"]; ok {
		merged.Events = patch.Events
	}
	if _, ok := fields["config_json"]; ok {
		merged.ConfigJSON = patch.ConfigJSON
	}
	if _, ok := fields["templates_json"]; ok {
		merged.TemplatesJSON = patch.TemplatesJSON
	}
	if _, ok := fields["user_ids"]; ok {
		merged.UserIDs = patch.UserIDs
	}
	return merged
}

func (s *Server) applyNotificationChannelOperation(ctx context.Context, principal application.Principal, input json.RawMessage, name string) (any, error) {
	channel, changed, err := s.notificationChannelAutomationCandidate(ctx, principal, input, name)
	if err != nil {
		return nil, err
	}
	if name == "notification_channels.create" {
		if err := s.store.CreateNotificationChannel(ctx, &channel); err != nil {
			return nil, err
		}
		return automationNotificationChannelResult(channel, nil)
	}
	if err := s.store.UpdateNotificationChannel(ctx, &channel); err != nil {
		return nil, err
	}
	return automationNotificationChannelResult(channel, changed)
}

func automationNotificationChannelResult(channel model.NotificationChannel, changed []string) (any, error) {
	view := map[string]any{
		"id": channel.ID, "revision": channel.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"name": channel.Name, "type": channel.Type, "enabled": channel.Enabled,
		"events_configured": strings.TrimSpace(channel.Events) != "", "user_count": len(channel.UserIDs),
		"created_at": channel.CreatedAt, "updated_at": channel.UpdatedAt,
	}
	if len(changed) == 0 {
		return map[string]any{"notification_channel": view}, nil
	}
	return map[string]any{"notification_channel": view, "changed_fields": changed}, nil
}
