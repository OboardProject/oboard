package controller

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
)

const (
	authChallengeTOTPLogin       = "totp-login"
	authChallengePasskeyLogin    = "passkey-login"
	authChallengePasskeyRegister = "passkey-register"
	authChallengeLifetime        = 5 * time.Minute
)

type authChallengePayload struct {
	Kind             string                `json:"kind"`
	SessionVersion   int64                 `json:"session_version"`
	WebAuthnSession  *webauthn.SessionData `json:"webauthn_session,omitempty"`
	RelyingPartyID   string                `json:"relying_party_id,omitempty"`
	RelyingPartyName string                `json:"relying_party_name,omitempty"`
	Origins          []string              `json:"origins,omitempty"`
	PasskeyName      string                `json:"passkey_name,omitempty"`
	Discoverable     bool                  `json:"discoverable,omitempty"`
}

type webAuthnUser struct {
	user        model.User
	handle      []byte
	credentials []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte { return u.handle }

func (u *webAuthnUser) WebAuthnName() string { return u.user.Username }

func (u *webAuthnUser) WebAuthnDisplayName() string {
	if value := strings.TrimSpace(u.user.Nickname); value != "" {
		return value
	}
	return u.user.Username
}

func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential { return u.credentials }

func (s *Server) finishUserLogin(w http.ResponseWriter, r *http.Request, user *model.User, auditAction string) {
	payload, err := s.newSessionPayload(w, r, user)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	_ = s.store.AddAudit(r.Context(), model.AuditLog{ActorID: &user.ID, Action: auditAction, Target: "user", Detail: user.Username, IP: clientIP(r)})
	write(w, http.StatusOK, payload)
}

func (s *Server) renewCurrentSession(w http.ResponseWriter, r *http.Request, user *model.User) (map[string]any, error) {
	version, err := s.store.BumpSessionVersion(r.Context(), user.ID)
	if err != nil {
		return nil, err
	}
	user.SessionVersion = version
	return s.newSessionPayload(w, r, user)
}

func (s *Server) newSessionPayload(w http.ResponseWriter, r *http.Request, user *model.User) (map[string]any, error) {
	effectiveRole, err := s.store.EffectiveUserRole(r.Context(), *user)
	if err != nil {
		return nil, err
	}
	user.Role = effectiveRole
	expiresAt := time.Now().Add(sessionLifetime)
	token, err := security.SignSession(s.sessionSecret, security.TokenClaims{
		Subject:        user.ID,
		Role:           string(user.Role),
		SessionVersion: user.SessionVersion,
		ClientBinding:  s.sessionBindingForRequest(r),
		Expiry:         expiresAt,
	})
	if err != nil {
		return nil, err
	}
	csrfToken := s.csrfTokenForSession(token)
	s.setSessionCookie(w, r, token, expiresAt)
	return map[string]any{"token": token, "csrf_token": csrfToken, "user": user}, nil
}

func (s *Server) authenticationStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	status, err := s.userAuthenticationResponse(r, user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, status)
}

func (s *Server) userAuthenticationResponse(r *http.Request, userID int64) (map[string]any, error) {
	authentication, err := s.store.GetUserAuthentication(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	passkeys, err := s.store.ListPasskeyCredentials(r.Context(), userID)
	if err != nil {
		return nil, err
	}
	var recoveryHashes []string
	if err := json.Unmarshal([]byte(authentication.RecoveryCodeHashesJSON), &recoveryHashes); err != nil {
		return nil, err
	}
	return map[string]any{
		"totp_enabled":             authentication.TOTPEnabled,
		"recovery_codes_remaining": len(recoveryHashes),
		"passkeys":                 passkeys,
		"passkey_supported":        webAuthnSupportedForRequest(r),
	}, nil
}

func (s *Server) beginSecondFactorLogin(w http.ResponseWriter, r *http.Request, user *model.User) bool {
	authentication, err := s.store.GetUserAuthentication(r.Context(), user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return true
	}
	if !authentication.TOTPEnabled {
		return false
	}
	token, err := s.createAuthChallenge(r, user.ID, authChallengePayload{Kind: authChallengeTOTPLogin, SessionVersion: user.SessionVersion})
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return true
	}
	passkeys, err := s.store.ListPasskeyCredentials(r.Context(), user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return true
	}
	write(w, http.StatusOK, map[string]any{
		"two_factor_required": true,
		"challenge_token":     token,
		"methods":             []string{"totp", "recovery_code"},
		"passkey_available":   len(passkeys) > 0 && webAuthnSupportedForRequest(r),
	})
	return true
}

func (s *Server) verifyTOTPLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowRate(w, r, "totp-login-ip:"+clientIP(r), 20, time.Minute) {
		return
	}
	var req struct {
		ChallengeToken string `json:"challenge_token"`
		Code           string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	challenge, payload, err := s.loadAuthChallenge(r, req.ChallengeToken, authChallengeTOTPLogin)
	if err != nil {
		fail(w, errors.New("验证码已失效，请重新输入密码"), http.StatusUnauthorized)
		return
	}
	if !s.allowRate(w, r, "totp-login-challenge:"+challenge.TokenHash, 8, time.Minute) {
		return
	}
	user, err := s.store.GetUser(r.Context(), challenge.UserID)
	if err != nil || user.Status != "active" || user.SessionVersion != payload.SessionVersion {
		fail(w, errors.New("验证码已失效，请重新输入密码"), http.StatusUnauthorized)
		return
	}
	valid, err := s.verifyUserTOTP(r, user.ID, req.Code, true)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if !valid {
		fail(w, errors.New("验证码或恢复码错误"), http.StatusUnauthorized)
		return
	}
	_ = s.store.DeleteAuthChallenge(r.Context(), challenge.TokenHash, authChallengeTOTPLogin)
	s.finishUserLogin(w, r, user, "login_totp")
}

func (s *Server) totpSetupBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	if !s.allowRate(w, r, fmt.Sprintf("totp-setup-begin:%d:%s", user.ID, clientIP(r)), 5, 10*time.Minute) {
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !security.VerifyPassword(req.CurrentPassword, user.PasswordHash) {
		fail(w, errors.New("current password is incorrect"), http.StatusForbidden)
		return
	}
	authentication, err := s.store.GetUserAuthentication(r.Context(), user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if authentication.TOTPEnabled {
		fail(w, errors.New("双重认证已经开启"), http.StatusConflict)
		return
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: "OBoard", AccountName: user.Username, Period: 30, SecretSize: 20, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	encrypted, err := security.EncryptSecret(s.sessionSecret, totpSecretPurpose(user.ID), key.Secret())
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.store.SetTOTPSetup(r.Context(), user.ID, encrypted); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	image, err := key.Image(240, 240)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	var pngData strings.Builder
	encoder := base64.NewEncoder(base64.StdEncoding, &pngData)
	if err := png.Encode(encoder, image); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := encoder.Close(); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{
		"secret":      key.Secret(),
		"otpauth_url": key.URL(),
		"qr_data_url": "data:image/png;base64," + pngData.String(),
	})
}

func (s *Server) totpSetupConfirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	if !s.allowRate(w, r, fmt.Sprintf("totp-setup-confirm:%d:%s", user.ID, clientIP(r)), 10, time.Minute) {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	authentication, err := s.store.GetUserAuthentication(r.Context(), user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if authentication.TOTPEnabled || authentication.TOTPSecretEncrypted == "" {
		fail(w, errors.New("请重新开始双重认证设置"), http.StatusConflict)
		return
	}
	secret, err := security.DecryptSecret(s.sessionSecret, totpSecretPurpose(user.ID), authentication.TOTPSecretEncrypted)
	if err != nil {
		fail(w, errors.New("无法读取双重认证密钥，请重新设置"), http.StatusConflict)
		return
	}
	step, valid := matchingTOTPStep(secret, req.Code, time.Now().UTC())
	if !valid {
		fail(w, errors.New("六位验证码错误"), http.StatusUnauthorized)
		return
	}
	codes, hashes, err := s.newRecoveryCodes(user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.store.EnableTOTP(r.Context(), user.ID, authentication.TOTPSecretEncrypted, hashes, step); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	payload, err := s.renewCurrentSession(w, r, user)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	payload["recovery_codes"] = codes
	payload["ok"] = true
	auditReq(s, r, "enable", "totp", fmt.Sprint(user.ID))
	write(w, http.StatusOK, payload)
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	if !s.allowRate(w, r, fmt.Sprintf("totp-disable:%d:%s", user.ID, clientIP(r)), 8, time.Minute) {
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		Code            string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !security.VerifyPassword(req.CurrentPassword, user.PasswordHash) {
		fail(w, errors.New("current password is incorrect"), http.StatusForbidden)
		return
	}
	valid, err := s.verifyUserTOTP(r, user.ID, req.Code, true)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if !valid {
		fail(w, errors.New("验证码或恢复码错误"), http.StatusUnauthorized)
		return
	}
	if err := s.store.DisableTOTP(r.Context(), user.ID); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	payload, err := s.renewCurrentSession(w, r, user)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	payload["ok"] = true
	auditReq(s, r, "disable", "totp", fmt.Sprint(user.ID))
	write(w, http.StatusOK, payload)
}

func (s *Server) totpRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	if !s.allowRate(w, r, fmt.Sprintf("totp-recovery:%d:%s", user.ID, clientIP(r)), 8, time.Minute) {
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		Code            string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !security.VerifyPassword(req.CurrentPassword, user.PasswordHash) {
		fail(w, errors.New("current password is incorrect"), http.StatusForbidden)
		return
	}
	valid, err := s.verifyUserTOTP(r, user.ID, req.Code, true)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if !valid {
		fail(w, errors.New("验证码或恢复码错误"), http.StatusUnauthorized)
		return
	}
	codes, hashes, err := s.newRecoveryCodes(user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if err := s.store.ReplaceTOTPRecoveryCodes(r.Context(), user.ID, hashes); err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	auditReq(s, r, "rotate", "totp-recovery-codes", fmt.Sprint(user.ID))
	write(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (s *Server) passkeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	if !s.allowRate(w, r, fmt.Sprintf("passkey-register-begin:%d:%s", user.ID, clientIP(r)), 10, 10*time.Minute) {
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		Name            string `json:"name"`
		Code            string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !security.VerifyPassword(req.CurrentPassword, user.PasswordHash) {
		fail(w, errors.New("current password is incorrect"), http.StatusForbidden)
		return
	}
	authentication, err := s.store.GetUserAuthentication(r.Context(), user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if authentication.TOTPEnabled {
		valid, err := s.verifyUserTOTP(r, user.ID, req.Code, true)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		if !valid {
			fail(w, errors.New("验证码或恢复码错误"), http.StatusUnauthorized)
			return
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "通行密钥"
	}
	if len([]rune(name)) > 40 {
		fail(w, errors.New("通行密钥名称不能超过 40 个字符"), http.StatusBadRequest)
		return
	}
	passkeys, err := s.store.ListPasskeyCredentials(r.Context(), user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if len(passkeys) >= 10 {
		fail(w, errors.New("每个账号最多添加 10 个通行密钥"), http.StatusConflict)
		return
	}
	handler, config, err := webAuthnForRequest(r)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	webUser, err := s.loadWebAuthnUser(r, *user)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	options, session, err := handler.BeginRegistration(webUser,
		webauthn.WithExclusions(webauthn.Credentials(webUser.credentials).CredentialDescriptors()),
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementPreferred),
	)
	if err != nil {
		fail(w, errors.New("无法创建通行密钥注册请求"), http.StatusBadRequest)
		return
	}
	token, err := s.createAuthChallenge(r, user.ID, authChallengePayload{Kind: authChallengePasskeyRegister, SessionVersion: user.SessionVersion, WebAuthnSession: session, RelyingPartyID: config.RPID, RelyingPartyName: config.RPDisplayName, Origins: config.RPOrigins, PasskeyName: name})
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"options": options, "challenge_token": token})
}

func (s *Server) passkeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	if !s.allowRate(w, r, fmt.Sprintf("passkey-register-finish:%d:%s", user.ID, clientIP(r)), 15, 10*time.Minute) {
		return
	}
	var req struct {
		ChallengeToken string          `json:"challenge_token"`
		Credential     json.RawMessage `json:"credential"`
	}
	if !decode(w, r, &req) {
		return
	}
	challenge, payload, err := s.loadAuthChallenge(r, req.ChallengeToken, authChallengePasskeyRegister)
	if err != nil || challenge.UserID != user.ID || payload.SessionVersion != user.SessionVersion || payload.WebAuthnSession == nil {
		fail(w, errors.New("通行密钥注册请求已失效"), http.StatusUnauthorized)
		return
	}
	_ = s.store.DeleteAuthChallenge(r.Context(), challenge.TokenHash, authChallengePasskeyRegister)
	handler, err := webAuthnFromChallenge(payload)
	if err != nil {
		fail(w, errors.New("通行密钥注册配置无效"), http.StatusBadRequest)
		return
	}
	webUser, err := s.loadWebAuthnUser(r, *user)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	if err != nil {
		fail(w, errors.New("通行密钥响应格式无效"), http.StatusBadRequest)
		return
	}
	credential, err := handler.CreateCredential(webUser, *payload.WebAuthnSession, parsed)
	if err != nil {
		fail(w, errors.New("通行密钥验证失败"), http.StatusUnauthorized)
		return
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	item := &model.PasskeyCredential{UserID: user.ID, Name: payload.PasskeyName, CredentialID: base64.RawURLEncoding.EncodeToString(credential.ID), CredentialJSON: string(encoded)}
	if err := s.store.CreatePasskeyCredential(r.Context(), item); err != nil {
		fail(w, errors.New("该通行密钥已经添加"), http.StatusConflict)
		return
	}
	auditReq(s, r, "create", "passkey", fmt.Sprint(item.ID))
	status, err := s.userAuthenticationResponse(r, user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusCreated, status)
}

func (s *Server) passkeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		method(w)
		return
	}
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	if !s.allowRate(w, r, fmt.Sprintf("passkey-delete:%d:%s", user.ID, clientIP(r)), 10, 10*time.Minute) {
		return
	}
	id := idFromPath(r.URL.Path, "/api/v1/me/passkeys/")
	if id <= 0 {
		fail(w, errors.New("invalid passkey"), http.StatusBadRequest)
		return
	}
	var req struct {
		CurrentPassword string `json:"current_password"`
		Code            string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !security.VerifyPassword(req.CurrentPassword, user.PasswordHash) {
		fail(w, errors.New("current password is incorrect"), http.StatusForbidden)
		return
	}
	authentication, err := s.store.GetUserAuthentication(r.Context(), user.ID)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	if authentication.TOTPEnabled {
		valid, err := s.verifyUserTOTP(r, user.ID, req.Code, true)
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		if !valid {
			fail(w, errors.New("验证码或恢复码错误"), http.StatusUnauthorized)
			return
		}
	}
	if err := s.store.DeletePasskeyCredential(r.Context(), user.ID, id); err != nil {
		fail(w, errors.New("通行密钥不存在"), http.StatusNotFound)
		return
	}
	auditReq(s, r, "delete", "passkey", fmt.Sprint(id))
	write(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) passkeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowRate(w, r, "passkey-login-ip:"+clientIP(r), 20, time.Minute) {
		return
	}
	var req struct {
		Username string `json:"username"`
	}
	if !decode(w, r, &req) {
		return
	}
	handler, config, err := webAuthnForRequest(r)
	if err != nil {
		fail(w, err, http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		options, session, err := handler.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
		if err != nil {
			fail(w, errors.New("无法创建通行密钥登录请求"), http.StatusBadRequest)
			return
		}
		token, err := s.createAuthChallenge(r, 0, authChallengePayload{Kind: authChallengePasskeyLogin, Discoverable: true, WebAuthnSession: session, RelyingPartyID: config.RPID, RelyingPartyName: config.RPDisplayName, Origins: config.RPOrigins})
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		write(w, http.StatusOK, map[string]any{"options": options, "challenge_token": token})
		return
	}
	user, err := s.store.GetUserByUsername(r.Context(), username)
	if err != nil || user.Status != "active" {
		fail(w, errors.New("该账号无法使用通行密钥"), http.StatusUnauthorized)
		return
	}
	webUser, err := s.loadWebAuthnUser(r, *user)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	options, session, err := handler.BeginLogin(webUser, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		fail(w, errors.New("该账号无法使用通行密钥"), http.StatusUnauthorized)
		return
	}
	token, err := s.createAuthChallenge(r, user.ID, authChallengePayload{Kind: authChallengePasskeyLogin, SessionVersion: user.SessionVersion, WebAuthnSession: session, RelyingPartyID: config.RPID, RelyingPartyName: config.RPDisplayName, Origins: config.RPOrigins})
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"options": options, "challenge_token": token})
}

func (s *Server) passkeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	if !s.allowRate(w, r, "passkey-login-finish:"+clientIP(r), 30, time.Minute) {
		return
	}
	var req struct {
		ChallengeToken string          `json:"challenge_token"`
		Credential     json.RawMessage `json:"credential"`
	}
	if !decode(w, r, &req) {
		return
	}
	challenge, payload, err := s.loadAuthChallenge(r, req.ChallengeToken, authChallengePasskeyLogin)
	if err != nil || payload.WebAuthnSession == nil {
		fail(w, errors.New("通行密钥登录请求已失效"), http.StatusUnauthorized)
		return
	}
	_ = s.store.DeleteAuthChallenge(r.Context(), challenge.TokenHash, authChallengePasskeyLogin)
	handler, err := webAuthnFromChallenge(payload)
	if err != nil {
		fail(w, errors.New("通行密钥登录配置无效"), http.StatusBadRequest)
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		fail(w, errors.New("通行密钥响应格式无效"), http.StatusBadRequest)
		return
	}
	var user *model.User
	var credential *webauthn.Credential
	if payload.Discoverable {
		_, credential, err = handler.ValidatePasskeyLogin(func(rawID, userHandle []byte) (webauthn.User, error) {
			candidate, lookupErr := s.store.GetUserByPasskey(r.Context(), base64.RawURLEncoding.EncodeToString(rawID), base64.RawURLEncoding.EncodeToString(userHandle))
			if lookupErr != nil || candidate.Status != "active" {
				return nil, errors.New("通行密钥不存在")
			}
			webUser, loadErr := s.loadWebAuthnUser(r, *candidate)
			if loadErr != nil {
				return nil, loadErr
			}
			user = candidate
			return webUser, nil
		}, *payload.WebAuthnSession, parsed)
	} else {
		user, err = s.store.GetUser(r.Context(), challenge.UserID)
		if err == nil && (user.Status != "active" || user.SessionVersion != payload.SessionVersion) {
			err = errors.New("stale user session")
		}
		if err == nil {
			var webUser *webAuthnUser
			webUser, err = s.loadWebAuthnUser(r, *user)
			if err == nil {
				credential, err = handler.ValidateLogin(webUser, *payload.WebAuthnSession, parsed)
			}
		}
	}
	if err != nil || user == nil || credential == nil {
		fail(w, errors.New("通行密钥验证失败"), http.StatusUnauthorized)
		return
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		fail(w, err, http.StatusInternalServerError)
		return
	}
	credentialID := base64.RawURLEncoding.EncodeToString(credential.ID)
	if err := s.store.UpdatePasskeyCredential(r.Context(), user.ID, credentialID, string(encoded)); err != nil {
		fail(w, errors.New("通行密钥不存在"), http.StatusUnauthorized)
		return
	}
	s.finishUserLogin(w, r, user, "login_passkey")
}

func (s *Server) createAuthChallenge(r *http.Request, userID int64, payload authChallengePayload) (string, error) {
	token, err := security.RandomToken(32)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encrypted, err := security.EncryptSecret(s.sessionSecret, authChallengePurpose(payload.Kind), string(encoded))
	if err != nil {
		return "", err
	}
	err = s.store.CreateAuthChallenge(r.Context(), model.AuthChallenge{TokenHash: security.HashSecret(token), Kind: payload.Kind, UserID: userID, DataEncrypted: encrypted, ExpiresAt: time.Now().UTC().Add(authChallengeLifetime)})
	return token, err
}

func (s *Server) loadAuthChallenge(r *http.Request, token, kind string) (model.AuthChallenge, authChallengePayload, error) {
	if strings.TrimSpace(token) == "" {
		return model.AuthChallenge{}, authChallengePayload{}, errors.New("missing challenge")
	}
	challenge, err := s.store.GetAuthChallenge(r.Context(), security.HashSecret(token), kind)
	if err != nil {
		return model.AuthChallenge{}, authChallengePayload{}, err
	}
	plaintext, err := security.DecryptSecret(s.sessionSecret, authChallengePurpose(kind), challenge.DataEncrypted)
	if err != nil {
		return model.AuthChallenge{}, authChallengePayload{}, err
	}
	var payload authChallengePayload
	if err := json.Unmarshal([]byte(plaintext), &payload); err != nil {
		return model.AuthChallenge{}, authChallengePayload{}, err
	}
	if payload.Kind != kind {
		return model.AuthChallenge{}, authChallengePayload{}, errors.New("challenge kind mismatch")
	}
	return challenge, payload, nil
}

func authChallengePurpose(kind string) string { return "auth-challenge:" + kind }

func totpSecretPurpose(userID int64) string { return fmt.Sprintf("user-totp:%d", userID) }

func (s *Server) verifyUserTOTP(r *http.Request, userID int64, code string, allowRecovery bool) (bool, error) {
	authentication, err := s.store.GetUserAuthentication(r.Context(), userID)
	if err != nil {
		return false, err
	}
	if !authentication.TOTPEnabled || authentication.TOTPSecretEncrypted == "" {
		return false, nil
	}
	secret, err := security.DecryptSecret(s.sessionSecret, totpSecretPurpose(userID), authentication.TOTPSecretEncrypted)
	if err != nil {
		return false, err
	}
	if step, valid := matchingTOTPStep(secret, code, time.Now().UTC()); valid {
		return s.store.ConsumeTOTPStep(r.Context(), userID, step)
	}
	if !allowRecovery {
		return false, nil
	}
	normalized := normalizeRecoveryCode(code)
	if len(normalized) < 10 {
		return false, nil
	}
	return s.store.ConsumeTOTPRecoveryCode(r.Context(), userID, s.hashRecoveryCode(userID, normalized))
}

func matchingTOTPStep(secret, code string, now time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	for _, value := range code {
		if value < '0' || value > '9' {
			return 0, false
		}
	}
	current := now.Unix() / 30
	for _, step := range []int64{current, current - 1, current + 1} {
		candidate, err := totp.GenerateCodeCustom(secret, time.Unix(step*30, 0).UTC(), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err == nil && hmac.Equal([]byte(candidate), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

func (s *Server) newRecoveryCodes(userID int64) ([]string, []string, error) {
	codes := make([]string, 10)
	hashes := make([]string, 10)
	for i := range codes {
		raw := make([]byte, 9)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		value := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		codes[i] = value[:5] + "-" + value[5:10] + "-" + value[10:15]
		hashes[i] = s.hashRecoveryCode(userID, normalizeRecoveryCode(codes[i]))
	}
	return codes, hashes, nil
}

func normalizeRecoveryCode(value string) string {
	var out strings.Builder
	for _, character := range strings.ToUpper(strings.TrimSpace(value)) {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			out.WriteRune(character)
		}
	}
	return out.String()
}

func (s *Server) hashRecoveryCode(userID int64, normalized string) string {
	mac := hmac.New(sha256.New, []byte(s.sessionSecret))
	_, _ = fmt.Fprintf(mac, "oboard-totp-recovery:%d:%s", userID, normalized)
	return hex.EncodeToString(mac.Sum(nil))
}

func webAuthnSupportedForRequest(r *http.Request) bool {
	host := requestHostname(r)
	return requestUsesHTTPS(r) || host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()
}

func webAuthnForRequest(r *http.Request) (*webauthn.WebAuthn, *webauthn.Config, error) {
	if !webAuthnSupportedForRequest(r) {
		return nil, nil, errors.New("通行密钥需要通过 HTTPS 访问面板")
	}
	rpID := strings.TrimSpace(os.Getenv("OBOARD_WEBAUTHN_RP_ID"))
	if rpID == "" {
		rpID = requestHostname(r)
	}
	if rpID == "" {
		return nil, nil, errors.New("无法确定通行密钥域名")
	}
	origins := splitNonEmpty(os.Getenv("OBOARD_WEBAUTHN_ORIGINS"))
	if len(origins) == 0 {
		scheme := "http"
		if requestUsesHTTPS(r) {
			scheme = "https"
		}
		if r.Host == "" {
			return nil, nil, errors.New("无法确定通行密钥来源")
		}
		origins = []string{scheme + "://" + r.Host}
	}
	config := &webauthn.Config{
		RPID:                  rpID,
		RPDisplayName:         "OBoard",
		RPOrigins:             origins,
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: authChallengeLifetime},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: authChallengeLifetime},
		},
	}
	handler, err := webauthn.New(config)
	if err != nil {
		return nil, nil, err
	}
	return handler, config, nil
}

func webAuthnFromChallenge(payload authChallengePayload) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPID:                  payload.RelyingPartyID,
		RPDisplayName:         payload.RelyingPartyName,
		RPOrigins:             payload.Origins,
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementPreferred,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: authChallengeLifetime},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: authChallengeLifetime},
		},
	})
}

func (s *Server) loadWebAuthnUser(r *http.Request, user model.User) (*webAuthnUser, error) {
	handleToken, err := security.RandomToken(32)
	if err != nil {
		return nil, err
	}
	handleToken, err = s.store.EnsureWebAuthnUserHandle(r.Context(), user.ID, handleToken)
	if err != nil {
		return nil, err
	}
	handle, err := base64.RawURLEncoding.DecodeString(handleToken)
	if err != nil {
		return nil, err
	}
	items, err := s.store.ListPasskeyCredentials(r.Context(), user.ID)
	if err != nil {
		return nil, err
	}
	credentials := make([]webauthn.Credential, 0, len(items))
	for _, item := range items {
		var credential webauthn.Credential
		if err := json.Unmarshal([]byte(item.CredentialJSON), &credential); err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	return &webAuthnUser{user: user, handle: handle, credentials: credentials}, nil
}

func requestHostname(r *http.Request) string {
	host := strings.TrimSpace(r.Host)
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	return strings.Trim(host, "[]")
}

func splitNonEmpty(raw string) []string {
	out := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
