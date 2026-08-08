package aiprovider

import "strings"

const redacted = "[redacted]"

func Redact(value string, secrets ...string) string {
	result := value
	for _, secret := range secrets {
		if secret != "" {
			result = strings.ReplaceAll(result, secret, redacted)
		}
	}
	return result
}

func Bounded(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}
