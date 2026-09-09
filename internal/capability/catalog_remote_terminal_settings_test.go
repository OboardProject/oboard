package capability

import (
	"strings"
	"testing"
)

func TestSettingsSchemaExcludesHumanStepUpSetting(t *testing.T) {
	descriptor, ok := NewCatalog().Get("settings.update")
	if !ok {
		t.Fatal("settings.update missing")
	}
	if strings.Contains(string(descriptor.InputSchema), "remote_terminal_password_confirmation_enabled") {
		t.Fatal("machine settings schema exposes a setting requiring human step-up")
	}
	if !strings.Contains(descriptor.Description, "重新验证身份") {
		t.Fatal("machine settings capability must explain the human verification requirement")
	}
}
