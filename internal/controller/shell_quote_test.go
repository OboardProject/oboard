package controller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestShellSingleQuoteSurvivesRealShell runs the quoting helper's output through
// /bin/sh. The Controller renders enrollment tokens and install parameters into
// operator-executed shell commands, and a single-quote escape that is one
// backslash off silently reopens the quoted run and turns the remainder of the
// value into shell syntax.
func TestShellSingleQuoteSurvivesRealShell(t *testing.T) {
	dir := t.TempDir()
	for index, payload := range []string{
		"ordinary-token",
		"tok'en",
		"x'$(touch " + filepath.Join(dir, "sub") + ")'",
		"x';touch " + filepath.Join(dir, "semi") + ";'",
		"x'`touch " + filepath.Join(dir, "backtick") + "`'",
		`x'\''y`,
		"x\"y",
		"a b\tc",
		"$HOME",
		"*",
	} {
		marker := filepath.Join(dir, "out")
		script := "printf '%s' " + shellSingleQuote(payload) + " > " + shellSingleQuote(marker)
		if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("case %d did not parse as POSIX sh: %v (%s)", index, err, out)
		}
		got, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != payload {
			t.Fatalf("case %d round-tripped %q as %q", index, payload, got)
		}
	}
	for _, name := range []string{"sub", "semi", "backtick"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Fatalf("quoted payload executed the %s injection", name)
		}
	}
}

// TestAgentInstallCommandQuotesRenderedValues keeps the issued install command
// free of unquoted interpolation. The enrollment token is passed through the
// environment rather than the command text, and the base URL and BBR flag are
// the only rendered values.
func TestAgentInstallCommandQuotesRenderedValues(t *testing.T) {
	hostile := "https://panel.example/base'; touch /tmp/pwned; '"
	command := agentInstallCommand(hostile, "1")
	if strings.Contains(command, "; touch /tmp/pwned; ") && !strings.Contains(command, `'\''`) {
		t.Fatalf("install command carries an unquoted payload: %s", command)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "pwned")
	// Replace the network stage with a no-op so only the quoting is exercised.
	script := strings.NewReplacer("curl -fsSL", "printf '%s' ", "| env", "| : env", " sh", " :").Replace(
		agentInstallCommand("https://panel.example/x'$(touch "+marker+")'", "0"))
	if out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("install command did not parse as POSIX sh: %v (%s)", err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("base URL substitution executed: %s", script)
	}
}
