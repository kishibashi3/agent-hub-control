package fleet

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// TestEnvFilePermWarning: an existing env file readable by group/other gets a warning that
// names the file and the chmod fix; a 0600 (or 0400) file and a missing file stay silent
// (issue #51).
func TestEnvFilePermWarning(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		perm os.FileMode
		warn bool
	}{
		{"0644-warns", 0o644, true},
		{"0640-warns", 0o640, true},
		{"0604-warns", 0o604, true},
		{"0600-silent", 0o600, false},
		{"0400-silent", 0o400, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name+".env")
			if err := os.WriteFile(p, []byte("GITHUB_PAT=x\n"), tc.perm); err != nil {
				t.Fatal(err)
			}
			// WriteFile honours umask; force the exact mode under test.
			if err := os.Chmod(p, tc.perm); err != nil {
				t.Fatal(err)
			}
			w := envFilePermWarning(p)
			if tc.warn && (w == "" || !strings.Contains(w, "chmod 600 "+p)) {
				t.Errorf("perm %04o: expected warning with chmod hint, got %q", tc.perm, w)
			}
			if !tc.warn && w != "" {
				t.Errorf("perm %04o: expected no warning, got %q", tc.perm, w)
			}
		})
	}
	if w := envFilePermWarning(filepath.Join(dir, "missing.env")); w != "" {
		t.Errorf("missing file: expected no warning, got %q", w)
	}
}

// TestWriteEnvScaffoldDoesNotChmodExisting: a loose existing env file is warned about but
// never chmod'ed or rewritten by install (issue #51: warning only, no auto-fix).
func TestWriteEnvScaffoldDoesNotChmodExisting(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "fleet.env")
	if err := os.WriteFile(envFile, []byte("GITHUB_PAT=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(envFile, 0o644); err != nil {
		t.Fatal(err)
	}
	c := &Config{Scope: ScopeUser, EnvFile: envFile}
	if err := c.writeEnvScaffold("# scaffold\n"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("install changed mode of existing env file to %04o (must not auto-chmod)", got)
	}
	b, _ := os.ReadFile(envFile)
	if string(b) != "GITHUB_PAT=secret\n" {
		t.Errorf("install rewrote existing env file: %q", b)
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

// TestCrossScopeGuardUserScopeSystemOrphanNonRoot: a non-root user-scope install that finds a
// system-scope orphan cannot remove it (no sudo). Without --force it still aborts; with --force
// it proceeds (warning only) so the sudo-less user watchdog is not blocked behind root
// (issue #47). Probing the real /etc/systemd/system is unavoidable here, so the test only runs
// when that orphan actually exists on the host; otherwise the guard is a trivial no-op.
func TestCrossScopeGuardUserScopeSystemOrphanNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root only")
	}
	c := &Config{Scope: ScopeUser, Home: t.TempDir()}
	t.Setenv("XDG_CONFIG_HOME", "")
	if len(c.otherScopeSystemdUnits()) == 0 {
		t.Skip("no system-scope agent-hub-fleet units on this host")
	}
	c.Force = false
	if err := c.crossScopeGuard(); err == nil {
		t.Fatal("expected abort without --force when a system-scope orphan exists")
	}
	c.Force = true
	if err := c.crossScopeGuard(); err != nil {
		t.Fatalf("--force as non-root should proceed with a warning, got: %v", err)
	}
}
