package main

import (
	"bytes"
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
