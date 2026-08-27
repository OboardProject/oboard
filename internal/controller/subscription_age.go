package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	age "github.com/metacubex/age"
	"github.com/metacubex/age/armor"

	"github.com/OboardProject/oboard/internal/model"
)

const (
	settingSubscriptionAgePolicy           = "subscription_age_policy"
	settingSubscriptionAlwaysUseDomainHost = "subscription_always_use_domain_host"
)

var (
	errSubscriptionAgeKeyRequired = errors.New("age public key is required")
	errSubscriptionAgeNotEnabled  = errors.New("age encryption is not enabled for this user")
	errSubscriptionAgeFormat      = errors.New("age encryption is only supported for Mihomo subscriptions")
)

func normalizeSubscriptionAgePolicy(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), "required") {
		return "required"
	}
	return "optional"
}

func (s *Server) subscriptionAlwaysUseDomainHost(ctx context.Context) bool {
	items, err := s.store.ListSettings(ctx)
	if err != nil {
		return false
	}
	return settingBool(items, settingSubscriptionAlwaysUseDomainHost, false)
}

func subscriptionAgeCapableFormat(format model.SubscriptionFormat) bool {
	switch format {
	case model.SubscriptionFormatMihomo, model.SubscriptionFormatClashMeta, model.SubscriptionFormatClash:
		return true
	default:
		return false
	}
}

func subscriptionAgeRequested(r *http.Request) (bool, error) {
	value := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("age")))
	switch value {
	case "":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, errors.New("age must be true or false")
	}
}

func parseSubscriptionAgeRecipient(publicKey string) (age.Recipient, string, error) {
	publicKey = strings.TrimSpace(publicKey)
	if publicKey == "" {
		return nil, "", errSubscriptionAgeKeyRequired
	}
	if len(publicKey) > 8192 {
		return nil, "", errors.New("age public key is too long")
	}
	if strings.HasPrefix(strings.ToUpper(publicKey), "AGE-SECRET-KEY-") {
		return nil, "", errors.New("do not upload an age secret key; provide the public key")
	}
	recipients, err := age.ParseRecipients(strings.NewReader(publicKey + "\n"))
	if err != nil {
		return nil, "", fmt.Errorf("invalid age public key: %w", err)
	}
	if len(recipients) != 1 {
		return nil, "", errors.New("exactly one age public key is required")
	}
	canonical := strings.TrimSpace(fmt.Sprint(recipients[0]))
	return recipients[0], canonical, nil
}

func resolveSubscriptionAgeRecipient(r *http.Request, user model.User, policy string, format model.SubscriptionFormat) (age.Recipient, bool, error) {
	requested, err := subscriptionAgeRequested(r)
	if err != nil {
		return nil, false, err
	}
	headerKey := strings.TrimSpace(r.Header.Get("X-Age-Public-Key"))
	required := normalizeSubscriptionAgePolicy(policy) == "required" && subscriptionAgeCapableFormat(format)
	if !subscriptionAgeCapableFormat(format) {
		if requested || headerKey != "" {
			return nil, false, errSubscriptionAgeFormat
		}
		return nil, false, nil
	}
	if headerKey != "" {
		recipient, _, err := parseSubscriptionAgeRecipient(headerKey)
		return recipient, err == nil, err
	}
	if !required && !requested {
		return nil, false, nil
	}
	if !required && !user.SubscriptionAgeEnabled {
		return nil, false, errSubscriptionAgeNotEnabled
	}
	recipient, _, err := parseSubscriptionAgeRecipient(user.SubscriptionAgePublicKey)
	return recipient, err == nil, err
}

func encryptSubscriptionAgeArmor(plain string, recipient age.Recipient) ([]byte, error) {
	var out bytes.Buffer
	armorWriter := armor.NewWriter(&out)
	encryptedWriter, err := age.Encrypt(armorWriter, recipient)
	if err != nil {
		_ = armorWriter.Close()
		return nil, err
	}
	if _, err := io.WriteString(encryptedWriter, plain); err != nil {
		_ = encryptedWriter.Close()
		_ = armorWriter.Close()
		return nil, err
	}
	if err := encryptedWriter.Close(); err != nil {
		_ = armorWriter.Close()
		return nil, err
	}
	if err := armorWriter.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
