package main

import (
	"bytes"
	"strings"
	"testing"
)

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

func TestControllerLogOutputModes(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "", want: "both"},
		{value: "stdout", want: "stdout"},
		{value: " FILE ", want: "file"},
		{value: "Both", want: "both"},
	} {
		got, err := parseLogOutput(test.value)
		if err != nil || got != test.want {
			t.Fatalf("parseLogOutput(%q) = %q, %v; want %q", test.value, got, err, test.want)
		}
	}
	if _, err := parseLogOutput("stderr"); err == nil {
		t.Fatal("invalid log output mode was accepted")
	}

	var stdout, file bytes.Buffer
	if _, err := controllerLogWriter("both", &stdout, &file).Write([]byte("entry")); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "entry" || file.String() != "entry" {
		t.Fatalf("both output = stdout %q, file %q", stdout.String(), file.String())
	}
}

// TestControllerLogWriterRedactsStdoutSink covers a secret disclosure: the
// managed file sink redacts on write, but stdout was wired raw, so the default
// "both" mode published unredacted tokens to journald and container logs.
func TestControllerLogWriterRedactsStdoutSink(t *testing.T) {
	const entry = "agent hello Authorization: Bearer abc123.def-456 agent_token=super-secret password: hunter2\n"
	for _, mode := range []string{"stdout", "both"} {
		var stdout, file bytes.Buffer
		if _, err := controllerLogWriter(mode, &stdout, &file).Write([]byte(entry)); err != nil {
			t.Fatal(err)
		}
		got := stdout.String()
		for _, secret := range []string{"abc123.def-456", "super-secret", "hunter2"} {
			if strings.Contains(got, secret) {
				t.Fatalf("mode %s leaked %q to stdout: %s", mode, secret, got)
			}
		}
		if !strings.Contains(got, "[REDACTED]") || !strings.Contains(got, "agent hello") {
			t.Fatalf("mode %s stdout = %q, expected redacted but readable", mode, got)
		}
	}
}
