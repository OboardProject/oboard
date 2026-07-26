package controller

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/model"
)

var (
	acmeDomainPattern   = regexp.MustCompile(`(?i)Domain:\s*['"]([^'"]+)['"]`)
	acmeTXTValuePattern = regexp.MustCompile(`(?i)TXT\s+value:\s*['"]([^'"]+)['"]`)
)

func (s *Server) startACMECertificateIssue(ctx context.Context, certificate *model.Certificate, renew, resumeManual bool) error {
	s.certificateIssueMu.Lock()
	if s.certificateIssues[certificate.ID] {
		s.certificateIssueMu.Unlock()
		return errors.New("certificate issuance is already running")
	}
	if certificate.Status == model.CertificateStatusIssuing {
		s.certificateIssueMu.Unlock()
		return errors.New("certificate issuance is already running")
	}
	s.certificateIssues[certificate.ID] = true
	s.certificateIssueMu.Unlock()

	certificate.Status = model.CertificateStatusIssuing
	certificate.LastError = ""
	now := time.Now().UTC()
	certificate.LastRenewalAttemptAt = &now
	if err := s.store.UpdateCertificate(ctx, certificate); err != nil {
		s.releaseCertificateIssue(certificate.ID)
		return err
	}
	if certificate.ChallengeType == model.CertificateChallengeHTTP {
		payload := model.IssueCertificateHTTPTaskPayload{CertificateID: certificate.ID, Domains: certificate.Domains, AccountEmail: certificate.AccountEmail, ACMECA: certificate.ACMECA, Renew: renew}
		if _, err := s.queueAgentTask(ctx, *certificate.IssuanceServerID, model.AgentTaskTypeIssueCertificateHTTP, payload, time.Now().Unix()); err != nil {
			s.markCertificateIssueFailed(context.WithoutCancel(ctx), certificate, err)
			s.releaseCertificateIssue(certificate.ID)
			return err
		}
		s.releaseCertificateIssue(certificate.ID)
		return nil
	}
	issueCtx := context.WithoutCancel(ctx)
	go func(ctx context.Context) {
		defer s.releaseCertificateIssue(certificate.ID)
		if err := s.runDNSCertificateIssue(ctx, certificate, renew, resumeManual); err != nil {
			s.markCertificateIssueFailed(ctx, certificate, err)
		}
	}(issueCtx)
	return nil
}

func (s *Server) releaseCertificateIssue(id int64) {
	s.certificateIssueMu.Lock()
	delete(s.certificateIssues, id)
	s.certificateIssueMu.Unlock()
}

func (s *Server) markCertificateIssueFailed(ctx context.Context, certificate *model.Certificate, issueErr error) {
	certificate.Status = model.CertificateStatusFailed
	certificate.LastError = trimACMEError(issueErr.Error())
	if err := s.store.UpdateCertificate(ctx, certificate); err != nil {
		log.Printf("certificate %d: persist issuance failure: %v", certificate.ID, err)
	}
}

func (s *Server) runDNSCertificateIssue(ctx context.Context, certificate *model.Certificate, renew, resumeManual bool) error {
	if err := os.MkdirAll(s.acmeHome, 0o700); err != nil {
		return fmt.Errorf("create ACME home: %w", err)
	}
	if err := os.Chmod(s.acmeHome, 0o700); err != nil { // #nosec G302 -- ACME home is a private directory and requires its execute bit.
		return fmt.Errorf("secure ACME home: %w", err)
	}
	if !resumeManual {
		output, runErr := s.runACME(ctx, issueACMEArgs(s.acmeHome, *certificate, renew)...)
		records := parseACMEDNSChallenges(output)
		if len(records) == 0 {
			if runErr == nil {
				return errors.New("acme.sh did not return DNS validation records")
			}
			return fmt.Errorf("start ACME DNS challenge: %s", trimACMEError(output))
		}
		certificate.ValidationRecords = records
		if certificate.ChallengeType == model.CertificateChallengeDNSManual {
			certificate.Status = model.CertificateStatusAwaitingDNS
			certificate.LastError = ""
			return s.store.UpdateCertificate(ctx, certificate)
		}
		if certificate.DNSCredentialID == nil {
			return errors.New("managed DNS certificate has no DNS credential")
		}
		credential, err := s.store.GetDNSCredential(ctx, *certificate.DNSCredentialID)
		if err != nil || !credential.Enabled || credential.VerifiedAt == nil {
			return errors.New("DNS credential is unavailable or not verified")
		}
		cleanups := make([]func(context.Context) error, 0, len(records))
		for _, record := range records {
			zone, err := selectDNSCredentialZone(*credential, record.Name, 0)
			if err != nil {
				return err
			}
			scopedCredential := credentialForDNSZone(*credential, *zone)
			client, err := s.dnsProviderClient(scopedCredential)
			if err != nil {
				return err
			}
			record.CredentialID = credential.ID
			record.CredentialZoneID = zone.ID
			record.TTL = 60
			record.Enabled = true
			if err := validateDNSRecord(scopedCredential, record); err != nil {
				return err
			}
			cleanup, err := createACMEDNSRecord(ctx, client, record)
			if err != nil {
				cleanupACMEDNSChanges(context.WithoutCancel(ctx), cleanups)
				return fmt.Errorf("create ACME DNS record: %w", err)
			}
			if cleanup != nil {
				cleanups = append(cleanups, cleanup)
			}
		}
		defer cleanupACMEDNSChanges(context.WithoutCancel(ctx), cleanups)
		wait := acmeDNSWaitDuration()
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	output, err := s.runACME(ctx, resumeACMEArgs(s.acmeHome, *certificate)...)
	if err != nil {
		return fmt.Errorf("complete ACME DNS challenge: %s", trimACMEError(output))
	}
	return s.installAndStoreACMECertificate(ctx, certificate)
}

func issueACMEArgs(home string, certificate model.Certificate, renew bool) []string {
	args := []string{"--home", home, "--config-home", home, "--server", certificate.ACMECA, "--issue", "--keylength", "ec-256", "--dns", "--yes-I-know-dns-manual-mode-enough-go-ahead-please"}
	if renew {
		args = append(args, "--force")
	}
	if certificate.AccountEmail != "" {
		args = append(args, "--accountemail", certificate.AccountEmail)
	}
	for _, domain := range certificate.Domains {
		args = append(args, "-d", domain)
	}
	return args
}

func resumeACMEArgs(home string, certificate model.Certificate) []string {
	args := []string{"--home", home, "--config-home", home, "--server", certificate.ACMECA, "--renew", "--ecc", "--yes-I-know-dns-manual-mode-enough-go-ahead-please", "-d", certificate.PrimaryDomain}
	return args
}

func (s *Server) installAndStoreACMECertificate(ctx context.Context, certificate *model.Certificate) error {
	workDir, err := os.MkdirTemp(s.acmeHome, "install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	if err := os.Chmod(workDir, 0o700); err != nil { // #nosec G302 -- certificate staging uses a private traversable directory.
		return err
	}
	certPath := filepath.Join(workDir, "cert.pem")
	fullchainPath := filepath.Join(workDir, "fullchain.pem")
	keyPath := filepath.Join(workDir, "privkey.pem")
	args := []string{"--home", s.acmeHome, "--config-home", s.acmeHome, "--install-cert", "--ecc", "-d", certificate.PrimaryDomain, "--cert-file", certPath, "--fullchain-file", fullchainPath, "--key-file", keyPath}
	if output, err := s.runACME(ctx, args...); err != nil {
		return fmt.Errorf("install ACME certificate: %s", trimACMEError(output))
	}
	certificatePEM, err := os.ReadFile(certPath) // #nosec G304 -- fixed file in a private controller-created directory.
	if err != nil {
		return err
	}
	fullchainPEM, err := os.ReadFile(fullchainPath) // #nosec G304 -- fixed file in a private controller-created directory.
	if err != nil {
		return err
	}
	privateKeyPEM, err := os.ReadFile(keyPath) // #nosec G304 -- fixed file in a private controller-created directory.
	if err != nil {
		return err
	}
	return s.storeCertificateMaterial(ctx, certificate, string(certificatePEM), string(fullchainPEM), string(privateKeyPEM), certificateMaterialPolicy{})
}

func (s *Server) runACME(ctx context.Context, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, s.acmeCommand, args...) // #nosec G204,G702 -- binary path is local startup configuration; validated values are passed as separate arguments without a shell.
	cmd.Env = append(os.Environ(), "LE_WORKING_DIR="+s.acmeHome)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func parseACMEDNSChallenges(output string) []model.DNSRecord {
	var records []model.DNSRecord
	pendingDomain := ""
	for _, line := range strings.Split(output, "\n") {
		if match := acmeDomainPattern.FindStringSubmatch(line); len(match) == 2 {
			pendingDomain = normalizeDomainName(match[1])
			continue
		}
		if match := acmeTXTValuePattern.FindStringSubmatch(line); len(match) == 2 && pendingDomain != "" {
			records = append(records, model.DNSRecord{Type: "TXT", Name: pendingDomain, Content: strings.TrimSpace(match[1]), TTL: 60, Enabled: true})
			pendingDomain = ""
		}
	}
	return records
}

func createACMEDNSRecord(ctx context.Context, client dnsProviderClient, record model.DNSRecord) (func(context.Context) error, error) {
	existing, err := client.ListRecords(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range existing {
		if strings.EqualFold(item.Type, record.Type) && normalizeDomainName(item.Name) == normalizeDomainName(record.Name) && item.Content == record.Content {
			return nil, nil
		}
	}
	if huawei, ok := client.(*huaweiDNSProvider); ok {
		return huawei.addTXTValue(ctx, record, existing)
	}
	saved, err := client.UpsertRecord(ctx, record)
	if err != nil {
		return nil, err
	}
	if saved.ID == "" {
		return nil, errors.New("DNS provider did not return a record id")
	}
	return func(cleanupCtx context.Context) error { return client.DeleteRecord(cleanupCtx, saved.ID) }, nil
}

func cleanupACMEDNSChanges(ctx context.Context, cleanups []func(context.Context) error) {
	for index := len(cleanups) - 1; index >= 0; index-- {
		if err := cleanups[index](ctx); err != nil {
			log.Printf("certificate DNS cleanup error=%v", err)
		}
	}
}

func acmeDNSWaitDuration() time.Duration {
	raw := strings.TrimSpace(os.Getenv("OBOARD_ACME_DNS_WAIT_SECONDS"))
	if raw == "" {
		return 30 * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds < 0 || seconds > 3600 {
		return 30 * time.Second
	}
	return time.Duration(seconds) * time.Second
}

func trimACMEError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 4000 {
		value = value[len(value)-4000:]
	}
	if value == "" {
		return "acme.sh failed without output"
	}
	return value
}

func (s *Server) StartCertificateRenewal(ctx context.Context) {
	s.renewCertificates(ctx)
	ticker := time.NewTicker(12 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.renewCertificates(ctx)
		}
	}
}

func (s *Server) renewCertificates(ctx context.Context) {
	certificates, err := s.store.ListCertificates(ctx)
	if err != nil {
		log.Printf("certificate renewal: list certificates: %v", err)
		return
	}
	deadline := time.Now().Add(30 * 24 * time.Hour)
	for i := range certificates {
		certificate := &certificates[i]
		if !certificate.AutoRenew || certificate.Status != model.CertificateStatusReady || certificate.NotAfter == nil || certificate.NotAfter.After(deadline) || certificate.ChallengeType == model.CertificateChallengeDNSManual || certificate.ChallengeType == "imported" {
			continue
		}
		if err := s.startACMECertificateIssue(ctx, certificate, true, false); err != nil {
			log.Printf("certificate renewal id=%d error=%v", certificate.ID, err)
		}
	}
}
