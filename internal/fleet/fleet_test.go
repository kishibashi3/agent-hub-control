package fleet

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestWriteEnvScaffoldDoesNotClobber verifies an existing env file (which may hold
// secrets) is never overwritten by a repeat install.
func TestWriteEnvScaffoldDoesNotClobber(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".agent-hub", "fleet.env")
	if err := os.MkdirAll(filepath.Dir(envFile), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "GITHUB_PAT=ghp_supersecret\n"
	if err := os.WriteFile(envFile, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Config{Scope: ScopeUser, EnvFile: envFile}
	if err := c.writeEnvScaffold("PATH=/should/not/appear\n"); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != secret {
		t.Errorf("existing env file was clobbered:\n got: %q\nwant: %q", got, secret)
	}
}

// TestWriteEnvScaffoldCreatesWith0600 verifies the scaffold is created with secret-safe
// permissions when absent.
func TestWriteEnvScaffoldCreatesWith0600(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".agent-hub", "fleet.env")

	c := &Config{Scope: ScopeUser, EnvFile: envFile}
	if err := c.writeEnvScaffold("PATH=/opt/bin\n"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("env scaffold perm = %o, want 0600", perm)
	}
}

// TestInstallSystemdDryRunWritesNothing verifies --dry-run touches no files and runs no
// commands (it returns before any write/exec).
func TestInstallSystemdDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	c := &Config{
		OS: "linux", Init: initSystemd, Scope: ScopeUser,
		User: "testuser", Home: home,
		Binary: "/opt/bin/agenthubctl", EnvFile: filepath.Join(home, ".agent-hub", "fleet.env"),
		Timeout: 40, BootSec: 30, ActiveSec: 30, Interval: 180,
	}
	if err := c.installSystemd(true); err != nil {
		t.Fatal(err)
	}
	// No unit dir, no env file should have been created.
	if _, err := os.Stat(c.EnvFile); !os.IsNotExist(err) {
		t.Errorf("dry-run created env file %s", c.EnvFile)
	}
	if _, err := os.Stat(c.systemdUnitDir()); !os.IsNotExist(err) {
		t.Errorf("dry-run created unit dir %s", c.systemdUnitDir())
	}
}

func TestSystemdUnitDir(t *testing.T) {
	sys := &Config{Scope: ScopeSystem}
	if got := sys.systemdUnitDir(); got != "/etc/systemd/system" {
		t.Errorf("system unit dir = %q, want /etc/systemd/system", got)
	}

	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	usr := &Config{Scope: ScopeUser, Home: home}
	want := filepath.Join(home, ".config", "systemd", "user")
	if got := usr.systemdUnitDir(); got != want {
		t.Errorf("user unit dir = %q, want %q", got, want)
	}
}

func TestSystemctlArgsUserScopePrefix(t *testing.T) {
	usr := &Config{Scope: ScopeUser}
	got := usr.systemctlArgs("daemon-reload")
	if len(got) != 2 || got[0] != "--user" || got[1] != "daemon-reload" {
		t.Errorf("user systemctlArgs = %v, want [--user daemon-reload]", got)
	}
	sys := &Config{Scope: ScopeSystem}
	if got := sys.systemctlArgs("daemon-reload"); len(got) != 1 || got[0] != "daemon-reload" {
		t.Errorf("system systemctlArgs = %v, want [daemon-reload]", got)
	}
}

func TestResolveRejectsConflictingScopeFlags(t *testing.T) {
	if _, err := Resolve(ResolveOptions{ForceSystem: true, ForceUser: true}); err == nil {
		t.Error("expected error for --system + --user, got nil")
	}
}

func TestResolveRelativeBinaryRejected(t *testing.T) {
	if runtime.GOOS != "linux" || !systemdAvailable() {
		t.Skip("requires linux+systemd")
	}
	if _, err := Resolve(ResolveOptions{Binary: "agenthubctl", ForceUser: true}); err == nil {
		t.Error("expected error for relative --binary, got nil")
	}
}

// TestResolveBakesAbsoluteBinaryAndDefaults checks the happy path on a systemd host.
func TestResolveBakesAbsoluteBinaryAndDefaults(t *testing.T) {
	if runtime.GOOS != "linux" || !systemdAvailable() {
		t.Skip("requires linux+systemd")
	}
	c, err := Resolve(ResolveOptions{Binary: "/opt/bin/agenthubctl", ForceUser: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Binary != "/opt/bin/agenthubctl" {
		t.Errorf("binary = %q", c.Binary)
	}
	if c.Init != initSystemd || c.Scope != ScopeUser {
		t.Errorf("init/scope = %q/%q, want systemd/user", c.Init, c.Scope)
	}
	if c.Timeout != defaultTimeout || c.Interval != defaultInterval {
		t.Errorf("defaults not applied: timeout=%d interval=%d", c.Timeout, c.Interval)
	}
	if !filepath.IsAbs(c.EnvFile) {
		t.Errorf("env file not absolute: %q", c.EnvFile)
	}
}
