package main

import "testing"

func TestDefaultListenAddress(t *testing.T) {
	if defaultListenAddress != ":2787" {
		t.Fatalf("default listen address = %q", defaultListenAddress)
	}
}

func TestValidateSessionSecret(t *testing.T) {
	for _, value := range []string{"", " ", "\t\n", "short", "persistent-secret"} {
		if err := validateSessionSecret(value); err == nil {
			t.Fatalf("validateSessionSecret(%q) succeeded", value)
		}
	}
	if err := validateSessionSecret("persistent-secret-at-least-32-chars!"); err != nil {
		t.Fatalf("valid secret rejected: %v", err)
	}
}
