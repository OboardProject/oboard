package controller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/OboardProject/oboard/internal/model"
	"github.com/OboardProject/oboard/internal/security"
	"github.com/OboardProject/oboard/internal/store"
)

func (s *Server) selfUserDevices(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if user == nil {
		fail(w, errors.New("invalid session"), http.StatusUnauthorized)
		return
	}
	parts := pathParts(r.URL.Path, "/api/v1/me/devices/")
	if r.URL.Path == "/api/v1/me/devices" {
		parts = nil
	}
	s.userDevices(w, r, user.ID, parts)
}

func (s *Server) userDevices(w http.ResponseWriter, r *http.Request, userID int64, parts []string) {
	if userID <= 0 {
		fail(w, errors.New("invalid user"), http.StatusBadRequest)
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		fail(w, err, http.StatusNotFound)
		return
	}
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			devices, err := s.store.ListUserDevices(r.Context(), userID)
			if err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			write(w, http.StatusOK, map[string]any{"devices": devices, "device_limit": user.DeviceLimit, "legacy_proxy_enabled": user.LegacyProxyEnabled})
		case http.MethodPost:
			if user.Status != "active" {
				fail(w, errors.New("active user required"), http.StatusConflict)
				return
			}
			var request struct {
				Name string `json:"name"`
			}
			if !decode(w, r, &request) {
				return
			}
			credential, err := s.createUserDevice(r, userID, request.Name)
			if err != nil {
				s.writeUserDeviceError(w, err)
				return
			}
			if err := s.queueUserDeviceCredentialDeployment(r.Context()); err != nil {
				fail(w, err, http.StatusInternalServerError)
				return
			}
			auditReq(s, r, "create", "user-device", fmt.Sprintf("%d:%s", userID, credential.Device.ID))
			write(w, http.StatusCreated, map[string]any{"device": credential.Device, "device_token": credential.Token})
		default:
			method(w)
		}
		return
	}
	deviceID := strings.TrimSpace(parts[0])
	if deviceID == "" || len(parts) > 2 {
		fail(w, errors.New("invalid device route"), http.StatusNotFound)
		return
	}
	if len(parts) == 2 {
		s.userDeviceAction(w, r, userID, deviceID, parts[1])
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var request struct {
			Name string `json:"name"`
		}
		if !decode(w, r, &request) {
			return
		}
		device, err := s.store.RenameUserDevice(r.Context(), userID, deviceID, request.Name)
		if err != nil {
			s.writeUserDeviceError(w, err)
			return
		}
		auditReq(s, r, "update", "user-device", fmt.Sprintf("%d:%s", userID, deviceID))
		write(w, http.StatusOK, map[string]any{"device": device})
	case http.MethodDelete:
		device, err := s.store.RevokeUserDevice(r.Context(), userID, deviceID)
		if err != nil {
			s.writeUserDeviceError(w, err)
			return
		}
		if err := s.queueUserDeviceCredentialDeployment(r.Context()); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		auditReq(s, r, "revoke", "user-device", fmt.Sprintf("%d:%s", userID, deviceID))
		write(w, http.StatusOK, map[string]any{"device": device})
	default:
		method(w)
	}
}

func (s *Server) userDeviceAction(w http.ResponseWriter, r *http.Request, userID int64, deviceID, action string) {
	if r.Method != http.MethodPost {
		method(w)
		return
	}
	switch action {
	case "rotate":
		token, err := newDeviceToken()
		if err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		device, err := s.store.RotateUserDevice(r.Context(), userID, deviceID, security.HashAPISecret(s.sessionSecret, token), deviceTokenPrefix(token))
		if err != nil {
			s.writeUserDeviceError(w, err)
			return
		}
		if err := s.queueUserDeviceCredentialDeployment(r.Context()); err != nil {
			fail(w, err, http.StatusInternalServerError)
			return
		}
		auditReq(s, r, "rotate", "user-device", fmt.Sprintf("%d:%s", userID, deviceID))
		write(w, http.StatusOK, map[string]any{"device": device, "device_token": token})
	case "suspend-subscription", "resume-subscription":
		suspended := action == "suspend-subscription"
		device, err := s.store.SetUserDeviceSubscriptionSuspended(r.Context(), userID, deviceID, suspended)
		if err != nil {
			s.writeUserDeviceError(w, err)
			return
		}
		auditReq(s, r, map[bool]string{true: "suspend", false: "resume"}[suspended], "user-device-subscription", fmt.Sprintf("%d:%s", userID, deviceID))
		write(w, http.StatusOK, map[string]any{"device": device})
	default:
		fail(w, errors.New("unsupported device action"), http.StatusNotFound)
	}
}

func (s *Server) queueUserDeviceCredentialDeployment(ctx context.Context) error {
	revision, err := s.store.ConfigurationRevision(ctx)
	if err != nil {
		return err
	}
	s.markConfigurationRevision(ctx, revision, nil)
	return nil
}

func (s *Server) createUserDevice(r *http.Request, userID int64, name string) (model.UserDeviceCredential, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "新设备"
	}
	if len([]rune(name)) > 80 {
		return model.UserDeviceCredential{}, errors.New("device name must be at most 80 characters")
	}
	randomID, err := security.RandomToken(18)
	if err != nil {
		return model.UserDeviceCredential{}, err
	}
	deviceID := "dev_" + randomID
	token, err := newDeviceToken()
	if err != nil {
		return model.UserDeviceCredential{}, err
	}
	device := model.UserDevice{
		ID:              deviceID,
		DeviceIDHash:    security.HashAPISecret(s.sessionSecret, deviceID),
		UserID:          userID,
		Name:            name,
		TokenHash:       security.HashAPISecret(s.sessionSecret, token),
		TokenPrefix:     deviceTokenPrefix(token),
		CredentialEpoch: 1,
	}
	if err := s.store.CreateUserDevice(r.Context(), &device); err != nil {
		return model.UserDeviceCredential{}, err
	}
	return model.UserDeviceCredential{Device: device, Token: token}, nil
}

func newDeviceToken() (string, error) {
	value, err := security.RandomToken(24)
	if err != nil {
		return "", err
	}
	return "obd_" + value, nil
}

func deviceTokenPrefix(token string) string {
	if len(token) <= 12 {
		return token
	}
	return token[:12]
}

func (s *Server) writeUserDeviceError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, sql.ErrNoRows):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrDeviceLimitReached):
		status = http.StatusConflict
	case strings.Contains(strings.ToLower(err.Error()), "device name"), strings.Contains(strings.ToLower(err.Error()), "invalid user device"):
		status = http.StatusBadRequest
	}
	fail(w, err, status)
}
