package controller

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

func (s *Server) stepUpBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	claims, ok := r.Context().Value(claimsKey).(security.TokenClaims)
	if user == nil || !ok {
		failCode(w, "terminal_auth_expired", "invalid session", http.StatusUnauthorized)
		return
	}
	var req struct {
		Purpose  string `json:"purpose"`
		Resource struct {
			Type string `json:"type"`
			ID   any    `json:"id"`
		} `json:"resource"`
	}
	if !decode(w, r, &req) {
		return
	}
	purpose := strings.TrimSpace(req.Purpose)
	if !validStepUpPurpose(purpose) {
		fail(w, errors.New("unsupported step-up purpose"), http.StatusBadRequest)
		return
	}
	resourceID := stringifyResourceID(req.Resource.ID)
	nonce, err := security.RandomToken(18)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	challengeID, err := security.RandomToken(18)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	item := model.StepUpChallenge{
		ID: "suc_" + challengeID, UserID: user.ID, SessionID: claims.SessionID, SessionVersion: claims.SessionVersion,
		Purpose: purpose, ResourceType: strings.TrimSpace(req.Resource.Type), ResourceID: resourceID, Nonce: nonce,
		ExpiresAt: now.Add(2 * time.Minute), CreatedAt: now,
	}
	passkeys, _ := s.store.ListPasskeyCredentials(r.Context(), user.ID)
	passkeyAvailable := len(passkeys) > 0 && webAuthnSupportedForRequest(r)
	response := map[string]any{
		"challenge_id":      item.ID,
		"purpose":           purpose,
		"expires_at":        item.ExpiresAt,
		"methods":           []string{"password"},
		"passkey_available": passkeyAvailable,
	}
	if passkeyAvailable {
		handler, _, err := webAuthnForRequest(r)
		if err == nil {
			webUser, loadErr := s.loadWebAuthnUser(r, *user)
			if loadErr == nil {
				options, session, beginErr := handler.BeginLogin(webUser, webauthn.WithUserVerification(protocol.VerificationPreferred))
				if beginErr == nil {
					raw, _ := json.Marshal(session)
					item.WebAuthnSessionJSON = raw
					response["passkey"] = options
					response["methods"] = []string{"passkey", "password"}
				}
			}
		}
	}
	if err := s.store.CreateStepUpChallenge(r.Context(), item); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, response)
}

func (s *Server) stepUpPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		failCode(w, "terminal_auth_expired", "invalid session", http.StatusUnauthorized)
		return
	}
	var req struct {
		ChallengeID string `json:"challenge_id"`
		Password    string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !security.VerifyPassword(req.Password, user.PasswordHash) {
		fail(w, errors.New("password is incorrect"), http.StatusForbidden)
		return
	}
	s.finishStepUp(w, r, req.ChallengeID)
}

func (s *Server) stepUpPasskeyFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		failCode(w, "terminal_auth_expired", "invalid session", http.StatusUnauthorized)
		return
	}
	var req struct {
		ChallengeID string          `json:"challenge_id"`
		Credential  json.RawMessage `json:"credential"`
	}
	if !decode(w, r, &req) {
		return
	}
	challenge, err := s.store.GetStepUpChallenge(r.Context(), req.ChallengeID)
	if err != nil {
		failCode(w, "terminal_auth_expired", "step-up challenge expired", http.StatusUnauthorized)
		return
	}
	if challenge.UserID != user.ID || len(challenge.WebAuthnSessionJSON) == 0 {
		fail(w, errors.New("passkey step-up is not available"), http.StatusBadRequest)
		return
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(challenge.WebAuthnSessionJSON, &session); err != nil {
		fail(w, errors.New("passkey step-up is not available"), http.StatusBadRequest)
		return
	}
	handler, _, err := webAuthnForRequest(r)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	webUser, err := s.loadWebAuthnUser(r, *user)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		fail(w, errors.New("invalid passkey assertion"), http.StatusBadRequest)
		return
	}
	if _, err := handler.ValidateLogin(webUser, session, parsed); err != nil {
		fail(w, errors.New("passkey verification failed"), http.StatusForbidden)
		return
	}
	s.finishStepUp(w, r, req.ChallengeID)
}

func (s *Server) finishStepUp(w http.ResponseWriter, r *http.Request, challengeID string) {
	user := currentUser(r)
	claims, ok := r.Context().Value(claimsKey).(security.TokenClaims)
	if user == nil || !ok {
		failCode(w, "terminal_auth_expired", "invalid session", http.StatusUnauthorized)
		return
	}
	challenge, err := s.store.GetStepUpChallenge(r.Context(), challengeID)
	if err != nil {
		failCode(w, "terminal_auth_expired", "step-up challenge expired", http.StatusUnauthorized)
		return
	}
	if challenge.UserID != user.ID || challenge.SessionID != claims.SessionID || challenge.SessionVersion != claims.SessionVersion {
		fail(w, errors.New("step-up challenge does not match this session"), http.StatusForbidden)
		return
	}
	if err := s.store.ConsumeStepUpChallenge(r.Context(), challenge.ID); err != nil {
		failCode(w, "terminal_auth_expired", "step-up challenge already used", http.StatusUnauthorized)
		return
	}
	now := time.Now().UTC()
	token, err := security.SignStepUpToken(s.sessionSecret, security.StepUpTokenClaims{
		UserID: user.ID, SessionID: claims.SessionID, SessionVersion: claims.SessionVersion,
		Purpose: challenge.Purpose, ResourceType: challenge.ResourceType, ResourceID: challenge.ResourceID,
		Nonce: challenge.Nonce, IssuedAt: now, ExpiresAt: now.Add(security.StepUpTokenTTL),
	})
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"step_up_token": token, "expires_at": now.Add(security.StepUpTokenTTL), "purpose": challenge.Purpose})
}

func (s *Server) consumeStepUp(r *http.Request, token, purpose, resourceType, resourceID string) error {
	user := currentUser(r)
	claims, ok := r.Context().Value(claimsKey).(security.TokenClaims)
	if user == nil || !ok {
		return codedError("terminal_auth_expired", "invalid session")
	}
	parsed, err := security.VerifyStepUpToken(s.sessionSecret, token, time.Now().UTC())
	if err != nil {
		if strings.Contains(err.Error(), "expired") {
			return codedError("terminal_auth_expired", err.Error())
		}
		return codedError("terminal_auth_expired", "invalid step-up token")
	}
	if parsed.UserID != user.ID || parsed.SessionID != claims.SessionID || parsed.SessionVersion != claims.SessionVersion {
		return codedError("terminal_auth_expired", "step-up token does not match this session")
	}
	if parsed.Purpose != purpose {
		return codedError("terminal_auth_expired", "step-up token purpose mismatch")
	}
	if resourceType != "" && parsed.ResourceType != resourceType {
		return codedError("terminal_auth_expired", "step-up token resource mismatch")
	}
	if resourceID != "" && parsed.ResourceID != resourceID {
		return codedError("terminal_auth_expired", "step-up token resource mismatch")
	}
	if err := s.store.ConsumeStepUpToken(r.Context(), security.StepUpTokenHash(token), parsed.ExpiresAt); err != nil {
		return codedError("terminal_auth_expired", "step-up token already used")
	}
	return nil
}

func validStepUpPurpose(purpose string) bool {
	switch purpose {
	case model.StepUpPurposeRemoteTerminal, model.StepUpPurposeGrantMCPExec, model.StepUpPurposeGrantMCPRawShell, model.StepUpPurposeGrantMCPOperations, model.StepUpPurposePrivilegedGrant:
		return true
	default:
		return false
	}
}

func stringifyResourceID(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	case json.Number:
		return typed.String()
	default:
		return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(fmtSprint(value), ""), ""))
	}
}

func fmtSprint(value any) string {
	raw, _ := json.Marshal(value)
	return strings.Trim(string(raw), `"`)
}

type codedErr struct {
	code    string
	message string
}

func codedError(code, message string) error {
	return codedErr{code: code, message: message}
}

func (e codedErr) Error() string { return e.message }
func (e codedErr) Code() string  { return e.code }
