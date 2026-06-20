package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

// fakeEuid overrides the package-level geteuid so cross-user (root) teardown branches can be
// exercised host-independently. Returns a restore func for defer.
func fakeEuid(t *testing.T, uid int) func() {
	t.Helper()
	prev := geteuid
	geteuid = func() int { return uid }
	return func() { geteuid = prev }
}

// TestSystemctlInvocationCrossUserWrapsSudo: a root-driven user-scope teardown of another
// user's units must reach that user's session bus (issue #44), not root's empty --user bus.
func TestSystemctlInvocationCrossUserWrapsSudo(t *testing.T) {
	defer fakeEuid(t, 0)()
	c := &Config{Scope: ScopeUser, User: "alice", UID: 1001}
	name, argv := c.systemctlInvocation("disable", "--now", serviceName+".timer")
	if name != "sudo" {
		t.Fatalf("exec name = %q, want sudo", name)
	}
	want := []string{
		"-u", "alice", "env",
		"XDG_RUNTIME_DIR=/run/user/1001",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus",
		"systemctl", "--user", "disable", "--now", serviceName + ".timer",
	}
	if !reflect.DeepEqual(argv, want) {
		t.Errorf("cross-user argv =\n  %v\nwant\n  %v", argv, want)
	}
}

// TestSystemctlInvocationSameUserPlain: a user tearing down their own units uses plain
// `systemctl --user` (their own bus is correct).
func TestSystemctlInvocationSameUserPlain(t *testing.T) {
	defer fakeEuid(t, 1000)()
	c := &Config{Scope: ScopeUser, User: "alice", UID: 1000}
	name, argv := c.systemctlInvocation("daemon-reload")
	if name != "systemctl" || !reflect.DeepEqual(argv, []string{"--user", "daemon-reload"}) {
		t.Errorf("same-user invocation = %q %v, want systemctl [--user daemon-reload]", name, argv)
	}
}

// TestSystemctlInvocationSystemScopePlain: system scope (even as root) uses plain systemctl,
// no --user, no sudo wrapping — the system bus is correct.
func TestSystemctlInvocationSystemScopePlain(t *testing.T) {
	defer fakeEuid(t, 0)()
	c := &Config{Scope: ScopeSystem, UID: 0}
	name, argv := c.systemctlInvocation("daemon-reload")
	if name != "systemctl" || !reflect.DeepEqual(argv, []string{"daemon-reload"}) {
		t.Errorf("system-scope invocation = %q %v, want systemctl [daemon-reload]", name, argv)
	}
}

// TestSystemdUnitDirForCrossUserIgnoresProcessXDG: under sudo, the process's own
// XDG_CONFIG_HOME (root's) must not be applied to the target user's unit dir (issue #44 #1).
func TestSystemdUnitDirForCrossUserIgnoresProcessXDG(t *testing.T) {
	defer fakeEuid(t, 0)()
	t.Setenv("XDG_CONFIG_HOME", "/root/.config") // root's — must be ignored for the target
	c := &Config{Scope: ScopeSystem, Home: "/home/alice", UID: 1001}
	got := c.systemdUnitDirFor(ScopeUser)
	want := filepath.Join("/home/alice", ".config", "systemd", "user")
	if got != want {
		t.Errorf("cross-user user unit dir = %q, want %q (process XDG must be ignored)", got, want)
	}
}

// TestStopSystemdUnitsWarnsWhenInvokingUserUnknown: root tearing down a user-scope install
// without a known SUDO_USER can't reach the real user's bus — must warn, not silently skip.
func TestStopSystemdUnitsWarnsWhenInvokingUserUnknown(t *testing.T) {
	defer fakeEuid(t, 0)()
	c := &Config{Scope: ScopeUser, UID: 0}
	if warn := c.stopSystemdUnits(); warn == "" {
		t.Error("expected warning when invoking user is unknown, got none")
	}
}

// TestStopSystemdUnitsWarnsWhenNoUserSession: cross-user teardown with no /run/user/<uid>
// means no live user manager — files are removed but the operator is told it self-heals.
func TestStopSystemdUnitsWarnsWhenNoUserSession(t *testing.T) {
	defer fakeEuid(t, 0)()
	c := &Config{Scope: ScopeUser, User: "ghost", UID: 2147480000} // no such runtime dir
	if warn := c.stopSystemdUnits(); warn == "" {
		t.Error("expected warning when target user has no active session, got none")
	}
}

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

func TestOtherScope(t *testing.T) {
	if got := (&Config{Scope: ScopeSystem}).otherScope(); got != ScopeUser {
		t.Errorf("otherScope(system) = %q, want user", got)
	}
	if got := (&Config{Scope: ScopeUser}).otherScope(); got != ScopeSystem {
		t.Errorf("otherScope(user) = %q, want system", got)
	}
}

// seedUserScopeTimer writes a fake user-scope timer (+service) into home and returns the
// .timer path. Used to simulate an orphaned prior --user install.
func seedUserScopeTimer(t *testing.T, home string) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", "")
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmr := filepath.Join(dir, serviceName+".timer")
	for _, name := range []string{serviceName + ".service", serviceName + ".timer"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return tmr
}

// TestCrossScopeGuardNoOrphanPasses: with no opposite-scope units, the guard is a no-op.
// Target system scope so the probed opposite scope is the per-home user dir (an empty
// tempdir) rather than the host's real /etc/systemd/system, keeping the test hermetic.
func TestCrossScopeGuardNoOrphanPasses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	c := &Config{Scope: ScopeSystem, Home: home}
	if err := c.crossScopeGuard(); err != nil {
		t.Errorf("guard with no orphan returned error: %v", err)
	}
}

// TestCrossScopeGuardAbortsWithoutForce: a user-scope orphan blocks a system-scope install
// when --force is absent, and the orphan files are left untouched.
func TestCrossScopeGuardAbortsWithoutForce(t *testing.T) {
	home := t.TempDir()
	tmr := seedUserScopeTimer(t, home)

	c := &Config{Scope: ScopeSystem, Home: home, Force: false}
	err := c.crossScopeGuard()
	if err == nil {
		t.Fatal("expected abort error for cross-scope orphan, got nil")
	}
	if _, statErr := os.Stat(tmr); statErr != nil {
		t.Errorf("abort path removed the orphan timer (should be untouched): %v", statErr)
	}
}

// TestCrossScopeGuardForceRemovesOtherScope: with --force, the user-scope orphan is removed
// before the system-scope install proceeds. (systemctl calls are best-effort/quiet, so this
// runs even where systemd is absent.)
func TestCrossScopeGuardForceRemovesOtherScope(t *testing.T) {
	home := t.TempDir()
	tmr := seedUserScopeTimer(t, home)

	c := &Config{
		Scope: ScopeSystem, Home: home, Force: true,
		EnvFile: filepath.Join(home, ".agent-hub", "fleet.env"),
	}
	if err := c.crossScopeGuard(); err != nil {
		t.Fatalf("--force guard returned error: %v", err)
	}
	if _, statErr := os.Stat(tmr); !os.IsNotExist(statErr) {
		t.Errorf("--force did not remove the orphan timer %s (err=%v)", tmr, statErr)
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
